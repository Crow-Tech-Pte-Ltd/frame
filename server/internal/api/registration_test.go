package api_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/config"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

// TestRegistrationGateMatrix covers docs/API.md "Registration gate" for
// password sign-up: every mode × every kind of code.
func TestRegistrationGateMatrix(t *testing.T) {
	e := newEnv(t)
	ivy := e.register("Ivy")
	friendCode := e.must(e.do("POST", "/api/v1/invites", ivy.Token, nil), 200).m(t)["code"].(string)
	soon := e.clock.Now().Add(time.Hour)
	expiring := e.serverInvite(5, &soon, "expires soon")
	revoked := e.serverInvite(5, nil, "revoked")
	{
		var id string
		e.db.R.QueryRow(`SELECT id FROM server_invites WHERE note = 'revoked'`).Scan(&id)
		e.db.W.Exec(`UPDATE server_invites SET revoked_at = 1 WHERE id = ?`, id)
	}
	n := 0
	email := func() string { n++; return fmt.Sprintf("gate%d@example.com", n) }

	type tc struct {
		mode        string
		userInvites bool
		code        any
		wantStatus  int
		wantCode    string
	}
	unknown := "AAAAAAAAAAAAAAAAAAAAAA"
	cases := []tc{
		{"invite", true, nil, 403, "invite_required"},
		{"invite", true, "", 403, "invite_required"},
		{"invite", true, "  ", 403, "invite_required"},
		{"invite", true, "garbage", 403, "invite_invalid"},
		{"invite", true, unknown, 403, "invite_invalid"},
		{"invite", true, revoked, 403, "invite_invalid"},
		{"invite", false, friendCode, 403, "invite_invalid"},
		{"invite", true, friendCode, 200, ""},
		{"invite", false, expiring, 200, ""}, // server invites don't depend on user_invites
		{"closed", true, friendCode, 403, "registration_closed"},
		{"closed", true, expiring, 403, "registration_closed"},
		{"closed", true, nil, 403, "registration_closed"},
		{"open", false, nil, 200, ""},
		{"open", false, unknown, 200, ""}, // an unusable code is ignored on open servers
		{"open", false, friendCode, 200, ""},
	}
	for i, c := range cases {
		e.setRegistration(c.mode, c.userInvites)
		r := e.registerWith(email(), c.code)
		if r.Code != c.wantStatus || (c.wantCode != "" && r.errCode(t) != c.wantCode) {
			t.Errorf("case %d %+v: %d %s", i, c, r.Code, r.Body)
		}
	}

	// Friend invite (members' link): instant friendship, invited_by recorded,
	// use counted — also on open servers.
	e.setRegistration("invite", true)
	r := e.must(e.registerWith(email(), friendCode), 200).m(t)
	newID := r["user"].(map[string]any)["id"].(string)
	var invitedBy string
	e.db.R.QueryRow(`SELECT invited_by FROM users WHERE id = ?`, newID).Scan(&invitedBy)
	if invitedBy != ivy.ID {
		t.Fatalf("invited_by = %q", invitedBy)
	}
	var fl struct {
		Friends []struct{ User struct{ ID string } }
	}
	e.must(e.do("GET", "/api/v1/friends", r["token"].(string), nil), 200).decode(t, &fl)
	if len(fl.Friends) != 1 || fl.Friends[0].User.ID != ivy.ID {
		t.Fatalf("friend invite must befriend: %+v", fl)
	}

	// Server invite: no friendship, uses limited, server_invite_id recorded,
	// a failed registration (identifier taken) doesn't consume a use.
	one := e.serverInvite(1, nil, "single")
	if r := e.registerWith(ivy.Email, one); r.Code != 409 || r.errCode(t) != "identifier_taken" {
		t.Fatalf("taken: %d %s", r.Code, r.Body)
	}
	r = e.must(e.registerWith(email(), one), 200).m(t)
	e.must(e.do("GET", "/api/v1/friends", r["token"].(string), nil), 200).decode(t, &fl)
	if len(fl.Friends) != 0 {
		t.Fatalf("server invite must not befriend anyone: %+v", fl)
	}
	var uses int
	var sid string
	e.db.R.QueryRow(`SELECT s.uses, u.server_invite_id FROM server_invites s JOIN users u ON u.server_invite_id = s.id WHERE s.note = 'single'`).Scan(&uses, &sid)
	if uses != 1 || sid == "" {
		t.Fatalf("uses %d sid %q", uses, sid)
	}
	if r := e.registerWith(email(), one); r.Code != 403 || r.errCode(t) != "invite_invalid" {
		t.Fatalf("used-up invite: %d %s", r.Code, r.Body)
	}
	// Expiry.
	e.clock.Add(2 * time.Hour)
	if r := e.registerWith(email(), expiring); r.Code != 403 || r.errCode(t) != "invite_invalid" {
		t.Fatalf("expired invite: %d %s", r.Code, r.Body)
	}
	// A suspended member's links stop working.
	if err := e.srv.SuspendUser(context.Background(), ivy.ID, true); err != nil {
		t.Fatal(err)
	}
	if r := e.registerWith(email(), friendCode); r.Code != 403 || r.errCode(t) != "invite_invalid" {
		t.Fatalf("suspended inviter: %d %s", r.Code, r.Body)
	}
	if r := e.do("GET", "/api/v1/invites/"+friendCode, "", nil); r.Code != 404 {
		t.Fatalf("suspended inviter lookup: %d", r.Code)
	}
}

// A server invite's max_uses holds under concurrent registrations.
func TestServerInviteConcurrentUse(t *testing.T) {
	e := newEnv(t)
	e.setRegistration("invite", true)
	code := e.serverInvite(2, nil, "race")
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := e.registerWith(fmt.Sprintf("race%d@example.com", i), code)
			if r.Code == 200 {
				mu.Lock()
				ok++
				mu.Unlock()
			} else if r.errCode(t) != "invite_invalid" {
				t.Errorf("unexpected %d %s", r.Code, r.Body)
			}
		}()
	}
	wg.Wait()
	var uses int
	e.db.R.QueryRow(`SELECT uses FROM server_invites`).Scan(&uses)
	if ok != 2 || uses != 2 {
		t.Fatalf("ok=%d uses=%d, want 2/2", ok, uses)
	}
}

// Apple and Google sign-up go through the same gate; signing in to an
// existing account never needs a code; suspended accounts get
// account_disabled.
func TestRegistrationGateOIDC(t *testing.T) {
	ap, gp, apple, google, clock := newVerifiers(t)
	e := newEnv(t, withVerifiers(apple, google))
	now := clock().Unix()
	host := "frame.example.com"
	ivy := e.register("Ivy")
	friendCode := e.must(e.do("POST", "/api/v1/invites", ivy.Token, nil), 200).m(t)["code"].(string)
	server := e.serverInvite(1, nil, "oidc")
	e.setRegistration("invite", true)

	appleBody := func(sub string, code any) map[string]any {
		raw := serverNonce(host)
		return map[string]any{"id_token": ap.token(t, appleClaims(now, sub, raw)), "raw_nonce": raw, "display_name": "A", "invite_code": code}
	}
	googleBody := func(sub string, code any) map[string]any {
		raw := serverNonce(host)
		return map[string]any{"id_token": gp.token(t, googleClaims(now, sub, raw)), "raw_nonce": raw, "invite_code": code}
	}
	if r := e.do("POST", "/api/v1/auth/apple", "", appleBody("a-1", nil)); r.Code != 403 || r.errCode(t) != "invite_required" {
		t.Fatalf("apple without invite: %d %s", r.Code, r.Body)
	}
	if r := e.do("POST", "/api/v1/auth/apple", "", appleBody("a-1", "AAAAAAAAAAAAAAAAAAAAAA")); r.Code != 403 || r.errCode(t) != "invite_invalid" {
		t.Fatalf("apple bad invite: %d %s", r.Code, r.Body)
	}
	a1 := e.must(e.do("POST", "/api/v1/auth/apple", "", appleBody("a-1", server)), 200).m(t)
	if a1["is_new"] != true {
		t.Fatal("apple with server invite")
	}
	// Existing account: no code needed (server invite already used up).
	if r := e.must(e.do("POST", "/api/v1/auth/apple", "", appleBody("a-1", nil)), 200).m(t); r["is_new"] != false {
		t.Fatal("apple sign-in")
	}
	g := e.must(e.do("POST", "/api/v1/auth/google", "", googleBody("g-1", friendCode)), 200).m(t)
	var fl struct {
		Friends []struct{ User struct{ ID string } }
	}
	e.must(e.do("GET", "/api/v1/friends", g["token"].(string), nil), 200).decode(t, &fl)
	if len(fl.Friends) != 1 || fl.Friends[0].User.ID != ivy.ID {
		t.Fatalf("google with friend invite must befriend: %+v", fl)
	}
	e.setRegistration("invite", false)
	if r := e.do("POST", "/api/v1/auth/google", "", googleBody("g-2", friendCode)); r.Code != 403 || r.errCode(t) != "invite_invalid" {
		t.Fatalf("google with member link, user_invites off: %d %s", r.Code, r.Body)
	}
	e.setRegistration("closed", true)
	if r := e.do("POST", "/api/v1/auth/google", "", googleBody("g-3", friendCode)); r.Code != 403 || r.errCode(t) != "registration_closed" {
		t.Fatalf("closed google: %d %s", r.Code, r.Body)
	}
	// Closed servers still let existing accounts in.
	e.must(e.do("POST", "/api/v1/auth/google", "", googleBody("g-1", nil)), 200)

	// Suspended Apple account.
	if err := e.srv.SuspendUser(context.Background(), a1["user"].(map[string]any)["id"].(string), true); err != nil {
		t.Fatal(err)
	}
	if r := e.do("POST", "/api/v1/auth/apple", "", appleBody("a-1", nil)); r.Code != 403 || r.errCode(t) != "account_disabled" {
		t.Fatalf("suspended apple: %d %s", r.Code, r.Body)
	}
}

func TestInviteLookupKinds(t *testing.T) {
	e := newEnv(t)
	ivy := e.register("Ivy")
	friendCode := e.must(e.do("POST", "/api/v1/invites", ivy.Token, nil), 200).m(t)["code"].(string)
	server := e.serverInvite(1, nil, "lookup")
	look := func(code string) map[string]any {
		return e.must(e.do("GET", "/api/v1/invites/"+code, "", nil), 200).m(t)
	}

	for _, c := range []struct {
		mode        string
		userInvites bool
		friendCan   bool
		serverCan   bool
	}{
		{"invite", true, true, true}, {"invite", false, false, true}, {"open", false, true, true}, {"closed", true, false, false},
	} {
		e.setRegistration(c.mode, c.userInvites)
		f := look(friendCode)
		if f["kind"] != "friend" || f["inviter"].(map[string]any)["id"] != ivy.ID || f["can_register"] != c.friendCan || f["server_name"] != "Test Frame" {
			t.Errorf("%s/%v friend: %v", c.mode, c.userInvites, f)
		}
		s := look(server)
		if s["kind"] != "server" || s["inviter"] != nil || s["can_register"] != c.serverCan {
			t.Errorf("%s/%v server: %v", c.mode, c.userInvites, s)
		}
	}
	// /server advertises user_invites.
	e.setRegistration("invite", false)
	info := e.must(e.do("GET", "/api/v1/server", "", nil), 200).m(t)
	if info["registration"] != "invite" || info["user_invites"] != false {
		t.Fatalf("server info: %v", info)
	}
	// A signed-in member can't "redeem" a server invite (nobody to befriend).
	if r := e.do("POST", "/api/v1/invites/"+server+"/redeem", ivy.Token, nil); r.Code != 400 {
		t.Fatalf("redeem server invite: %d %s", r.Code, r.Body)
	}
	e.setRegistration("invite", true)
	e.must(e.registerWith("used@example.com", server), 200)
	if r := e.do("GET", "/api/v1/invites/"+server, "", nil); r.Code != 404 {
		t.Fatalf("used-up server invite still resolves: %d", r.Code)
	}
}

func TestSuspendedUser(t *testing.T) {
	e := newEnv(t)
	sam, bob := e.register("Sam"), e.register("Bob")
	e.must(e.do("PATCH", "/api/v1/me", sam.Token, map[string]any{"username": "sam"}), 200)
	e.befriend(sam, bob)
	m := e.postMoment(sam, momentOpts{})
	stream := e.subscribe(sam)
	if err := e.srv.SuspendUser(context.Background(), sam.ID, true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stream.done:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE stream of suspended user still open")
	}
	if r := e.do("GET", "/api/v1/me", sam.Token, nil); r.Code != 401 || r.errCode(t) != "account_disabled" {
		t.Fatalf("suspended token: %d %s", r.Code, r.Body)
	}
	login := map[string]any{"method": "email", "identifier": sam.Email, "password": "password123"}
	if r := e.do("POST", "/api/v1/auth/login", "", login); r.Code != 403 || r.errCode(t) != "account_disabled" {
		t.Fatalf("suspended login: %d %s", r.Code, r.Body)
	}
	// No oracle: a wrong password still says invalid_credentials.
	login["password"] = "wrong-password"
	if r := e.do("POST", "/api/v1/auth/login", "", login); r.Code != 401 || r.errCode(t) != "invalid_credentials" {
		t.Fatalf("suspended wrong password: %d %s", r.Code, r.Body)
	}
	var sessions int
	e.db.R.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, sam.ID).Scan(&sessions)
	if sessions != 0 {
		t.Fatalf("%d sessions left", sessions)
	}
	// Hidden from search and contact matching; content stays.
	if res := e.must(e.do("GET", "/api/v1/users/search?q=sa", bob.Token, nil), 200).m(t)["users"].([]any); len(res) != 0 {
		t.Fatalf("suspended user in search: %v", res)
	}
	hash := fmt.Sprintf("%x", store.ContactHash(sam.Email))
	if res := e.must(e.do("POST", "/api/v1/contacts/match", bob.Token, map[string]any{"hashes": []string{hash}}), 200).m(t)["matches"].([]any); len(res) != 0 {
		t.Fatalf("suspended user matched: %v", res)
	}
	e.must(e.do("GET", "/api/v1/moments/"+m.ID, bob.Token, nil), 200)

	// Reinstated: can sign in again and is findable.
	if err := e.srv.SuspendUser(context.Background(), sam.ID, false); err != nil {
		t.Fatal(err)
	}
	login["password"] = "password123"
	e.must(e.do("POST", "/api/v1/auth/login", "", login), 200)
	if res := e.must(e.do("GET", "/api/v1/users/search?q=sa", bob.Token, nil), 200).m(t)["users"].([]any); len(res) != 1 {
		t.Fatalf("reinstated user not in search: %v", res)
	}
}

// A suspension tells the user's apps why they were signed out: every request
// (and the SSE reconnect) with a token revoked by the suspension gets 401
// account_disabled, while any other invalid token keeps the generic 401
// unauthorized — unknown tokens, logged-out sessions, sessions revoked by a
// password reset, and old tokens once the user is reinstated (API v1.2.1).
func TestSuspendedTokenReportsAccountDisabled(t *testing.T) {
	e := newEnv(t)
	sam, bob := e.register("Sam"), e.register("Bob")
	phone := e.must(e.do("POST", "/api/v1/auth/login", "", map[string]any{
		"method": "email", "identifier": sam.Email, "password": "password123",
		"device": map[string]any{"name": "Sam's Pixel", "platform": "android"}}), 200).m(t)["token"].(string)
	loggedOut := e.must(e.do("POST", "/api/v1/auth/login", "", map[string]any{
		"method": "email", "identifier": sam.Email, "password": "password123"}), 200).m(t)["token"].(string)
	e.must(e.do("POST", "/api/v1/auth/logout", loggedOut, nil), 204)
	stream := e.subscribe(sam)

	code := func(r *resp) string { return fmt.Sprintf("%d %s", r.Code, r.errCode(t)) }
	unknown := strings.Repeat("A", len(sam.Token))
	if got := code(e.do("GET", "/api/v1/me", unknown, nil)); got != "401 unauthorized" {
		t.Fatalf("unknown token before: %s", got)
	}

	if err := e.srv.SuspendUser(context.Background(), sam.ID, true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stream.done:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE stream of suspended user still open")
	}
	for _, tok := range []string{sam.Token, phone} {
		for _, path := range []string{"/api/v1/me", "/api/v1/events", "/api/v1/moments", "/api/v1/conversations"} {
			if got := code(e.do("GET", path, tok, nil)); got != "401 account_disabled" {
				t.Fatalf("GET %s with suspended token: %s", path, got)
			}
		}
		if got := code(e.do("POST", "/api/v1/friends/requests", tok, map[string]any{"user_id": bob.ID})); got != "401 account_disabled" {
			t.Fatalf("write with suspended token: %s", got)
		}
	}
	// Nothing leaks for tokens that weren't revoked by the suspension.
	for name, tok := range map[string]string{"unknown": unknown, "logged out": loggedOut, "malformed": "nope"} {
		if got := code(e.do("GET", "/api/v1/me", tok, nil)); got != "401 unauthorized" {
			t.Fatalf("%s token: %s", name, got)
		}
	}
	if got := code(e.do("GET", "/api/v1/me", "", nil)); got != "401 unauthorized" {
		t.Fatalf("no token: %s", got)
	}
	// Bob's sessions revoked by a password change stay generic.
	bobPhone := e.must(e.do("POST", "/api/v1/auth/login", "", map[string]any{
		"method": "email", "identifier": bob.Email, "password": "password123"}), 200).m(t)["token"].(string)
	e.must(e.do("POST", "/api/v1/auth/password", bob.Token, map[string]any{"current_password": "password123", "new_password": "password456"}), 204)
	if got := code(e.do("GET", "/api/v1/me", bobPhone, nil)); got != "401 unauthorized" {
		t.Fatalf("password-change revoked token: %s", got)
	}
	var marked int
	e.db.R.QueryRow(`SELECT COUNT(*) FROM revoked_sessions WHERE user_id = ? AND reason = 'account_disabled'`, sam.ID).Scan(&marked)
	if marked != 2 {
		t.Fatalf("revoked_sessions rows = %d, want 2", marked)
	}

	// Reinstated: old tokens are plain invalid tokens again, the markers are gone.
	if err := e.srv.SuspendUser(context.Background(), sam.ID, false); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{sam.Token, phone} {
		if got := code(e.do("GET", "/api/v1/me", tok, nil)); got != "401 unauthorized" {
			t.Fatalf("old token after reinstating: %s", got)
		}
	}
	e.db.R.QueryRow(`SELECT COUNT(*) FROM revoked_sessions WHERE user_id = ?`, sam.ID).Scan(&marked)
	if marked != 0 {
		t.Fatalf("revoked_sessions rows after reinstating = %d", marked)
	}
	fresh := e.must(e.do("POST", "/api/v1/auth/login", "", map[string]any{
		"method": "email", "identifier": sam.Email, "password": "password123"}), 200).m(t)["token"].(string)
	e.must(e.do("GET", "/api/v1/me", fresh, nil), 200)

	// A session that slipped past the suspension (created concurrently) is
	// also told the account is disabled.
	if err := store.SetDisabled(context.Background(), e.db.W, sam.ID, &[]int64{1}[0], 1); err != nil {
		t.Fatal(err)
	}
	if got := code(e.do("GET", "/api/v1/me", fresh, nil)); got != "401 account_disabled" {
		t.Fatalf("live session of a disabled user: %s", got)
	}

	// Deleting a suspended account drops its markers (FK cascade).
	if err := e.srv.SuspendUser(context.Background(), sam.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.SuspendUser(context.Background(), sam.ID, true); err != nil {
		t.Fatal(err)
	}
	e.db.R.QueryRow(`SELECT COUNT(*) FROM revoked_sessions WHERE user_id = ?`, sam.ID).Scan(&marked)
	if marked != 1 {
		t.Fatalf("revoked_sessions rows after second suspension = %d, want 1", marked)
	}
	if err := e.srv.DeleteAccount(context.Background(), sam.ID); err != nil {
		t.Fatal(err)
	}
	e.db.R.QueryRow(`SELECT COUNT(*) FROM revoked_sessions`).Scan(&marked)
	if marked != 0 {
		t.Fatalf("revoked_sessions rows after deletion = %d", marked)
	}
	if got := code(e.do("GET", "/api/v1/me", fresh, nil)); got != "401 unauthorized" {
		t.Fatalf("token of a deleted account: %s", got)
	}
}

func TestMeStatsAndAdminFlag(t *testing.T) {
	e := newEnv(t)
	a, b, c := e.register("A"), e.register("B"), e.register("C")
	e.befriend(a, b)
	e.befriend(a, c)
	e.postMoment(a, momentOpts{})
	e.postMoment(a, momentOpts{audience: "self"})
	me := e.must(e.do("GET", "/api/v1/me", a.Token, nil), 200).m(t)
	st := me["stats"].(map[string]any)
	if st["moments"] != float64(2) || st["friends"] != float64(2) || me["is_admin"] != false {
		t.Fatalf("me: %v", me)
	}
	e.db.W.Exec(`UPDATE users SET is_admin = 1 WHERE id = ?`, a.ID)
	if me := e.must(e.do("GET", "/api/v1/me", a.Token, nil), 200).m(t); me["is_admin"] != true {
		t.Fatalf("is_admin: %v", me)
	}
}

// The default configuration is invite-only with members' links allowed.
func TestDefaultRegistrationIsInvite(t *testing.T) {
	c, err := config.Load(config.Source{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Registration != "invite" || !c.UserInvites || c.Admin != "on" {
		t.Fatalf("%s %v %s", c.Registration, c.UserInvites, c.Admin)
	}
}
