package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/admin"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/api"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/config"
)

// Regression tests for the v1.1 security review (docs/API.md changelog).

func (e *env) doFrom(ip, method, path, token string, body any) *resp {
	e.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, e.ts.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", ip)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := e.ts.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	rb, _ := io.ReadAll(res.Body)
	return &resp{Code: res.StatusCode, Body: rb, Header: res.Header}
}

// HIGH: a malicious Frame server (evil.example.com) obtains a user's
// {id_token, raw_nonce} and replays it to the user's real server.
func TestOIDCCrossServerReplayRejected(t *testing.T) {
	ap, gp, apple, google, clock := newVerifiers(t)
	e := newEnv(t, withVerifiers(apple, google))
	now := clock().Unix()
	for _, provider := range []string{"apple", "google"} {
		raw := serverNonce("evil.example.com")
		tok := ap.token(t, appleClaims(now, "victim", raw))
		if provider == "google" {
			tok = gp.token(t, googleClaims(now, "victim", raw))
		}
		body := fmt.Sprintf(`{"id_token":%q,"raw_nonce":%q}`, tok, raw)
		if r := e.request("POST", "/api/v1/auth/"+provider, "", strings.NewReader(body), "application/json"); r.Code != 401 || r.errCode(t) != "invalid_credentials" {
			t.Errorf("%s: replay accepted: %d %s", provider, r.Code, r.Body)
		}
		// Spoofing the Host header doesn't help: only configured hosts count.
		req, _ := http.NewRequest("POST", e.ts.URL+"/api/v1/auth/"+provider, strings.NewReader(body))
		req.Host = "evil.example.com"
		req.Header.Set("Content-Type", "application/json")
		res, err := e.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 401 {
			t.Errorf("%s: Host-header spoof accepted: %d", provider, res.StatusCode)
		}
	}
	// Malformed nonces are rejected too.
	for _, raw := range []string{"plain-nonce", "frame1:frame.example.com", "frame1:frame.example.com:short", "frame2:frame.example.com:" + strings.Repeat("A", 43)} {
		tok := ap.token(t, appleClaims(now, "x", raw))
		if r := e.do("POST", "/api/v1/auth/apple", "", map[string]any{"id_token": tok, "raw_nonce": raw}); r.Code != 401 {
			t.Errorf("nonce %q accepted: %d", raw, r.Code)
		}
	}
}

func TestOIDCNonceSingleUseAndRequired(t *testing.T) {
	ap, gp, apple, google, clock := newVerifiers(t)
	e := newEnv(t, withVerifiers(apple, google))
	now := clock().Unix()
	raw := serverNonce("frame.example.com")
	body := map[string]any{"id_token": ap.token(t, appleClaims(now, "u1", raw)), "raw_nonce": raw}
	e.must(e.do("POST", "/api/v1/auth/apple", "", body), 200)
	if r := e.do("POST", "/api/v1/auth/apple", "", body); r.Code != 401 {
		t.Fatalf("nonce reuse accepted: %d", r.Code)
	}
	var n int
	e.db.R.QueryRow(`SELECT COUNT(*) FROM used_nonces`).Scan(&n)
	if n != 1 {
		t.Fatalf("used nonce not persisted (survives restarts): %d", n)
	}
	// Google without a nonce (in token and/or request) is rejected.
	if r := e.do("POST", "/api/v1/auth/google", "", map[string]any{"id_token": gp.token(t, googleClaims(now, "g", "")), "raw_nonce": nil}); r.Code != 401 {
		t.Fatalf("google without nonce: %d", r.Code)
	}
	gr := serverNonce("frame.example.com")
	if r := e.do("POST", "/api/v1/auth/google", "", map[string]any{"id_token": gp.token(t, googleClaims(now, "g", "")), "raw_nonce": gr}); r.Code != 401 {
		t.Fatalf("google token without nonce claim: %d", r.Code)
	}
	e.must(e.do("POST", "/api/v1/auth/google", "", map[string]any{"id_token": gp.token(t, googleClaims(now, "g", gr)), "raw_nonce": gr}), 200)
}

func TestOIDCPublicHosts(t *testing.T) {
	ap, _, apple, google, clock := newVerifiers(t)
	now := clock().Unix()
	// No public host configured: methods disabled.
	e := newEnv(t, withVerifiers(apple, google), withConfig(func(c *config.Config) { c.PublicURL, c.PublicHosts = "", nil }))
	am := e.must(e.do("GET", "/api/v1/server", "", nil), 200).m(t)["auth_methods"].(map[string]any)
	if am["apple"] != false || am["google"] != false {
		t.Fatalf("auth methods without public host: %v", am)
	}
	raw := serverNonce("frame.example.com")
	if r := e.do("POST", "/api/v1/auth/apple", "", map[string]any{"id_token": ap.token(t, appleClaims(now, "a", raw)), "raw_nonce": raw}); r.Code != 403 {
		t.Fatalf("apple without public host: %d", r.Code)
	}
	// Several configured hosts (FRAME_PUBLIC_URL list): each one works.
	cfg, err := config.Load(config.Source{Overrides: map[string]string{"FRAME_PUBLIC_URL": "https://Frame.Example.com:443/, http://192.168.1.5:8080", "FRAME_DOMAIN": "alt.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.PublicHosts, ",") != "frame.example.com,192.168.1.5:8080,alt.example.com" || cfg.PublicURL != "https://Frame.Example.com:443" {
		t.Fatalf("public hosts: %v %s", cfg.PublicHosts, cfg.PublicURL)
	}
	e2 := newEnv(t, withVerifiers(apple, google), withConfig(func(c *config.Config) { c.PublicHosts = cfg.PublicHosts }))
	lan := serverNonce("192.168.1.5:8080")
	e2.must(e2.do("POST", "/api/v1/auth/apple", "", map[string]any{"id_token": ap.token(t, appleClaims(now, "b", lan)), "raw_nonce": lan}), 200)
	// Dev mode accepts any host (still single-use and well-formed).
	e3 := newEnv(t, withVerifiers(apple, google), withConfig(func(c *config.Config) { c.Dev, c.PublicURL, c.PublicHosts = true, "", nil }))
	devNonce := serverNonce("10.0.2.2:8787")
	e3.must(e3.do("POST", "/api/v1/auth/apple", "", map[string]any{"id_token": ap.token(t, appleClaims(now, "c", devNonce)), "raw_nonce": devNonce}), 200)
}

// MEDIUM: an old invite link must not re-befriend someone the inviter
// unfriended or blocked.
func TestInviteAfterUnfriendOrBlock(t *testing.T) {
	e := newEnv(t)
	a, b, c := e.register("Inviter"), e.register("Bee"), e.register("Cee")
	code := e.must(e.do("POST", "/api/v1/invites", a.Token, nil), 200).m(t)["code"].(string)
	redeem := func(u *user) *resp { return e.do("POST", "/api/v1/invites/"+code+"/redeem", u.Token, nil) }

	r := e.must(redeem(b), 200).m(t)
	if r["status"] != "friends" || r["request"] != nil || r["friend"].(map[string]any)["user"].(map[string]any)["id"] != a.ID {
		t.Fatalf("first redeem: %v", r)
	}
	if again := e.must(redeem(b), 200).m(t); again["status"] != "friends" {
		t.Fatalf("no-op redeem: %v", again)
	}
	e.must(e.do("DELETE", "/api/v1/friends/"+b.ID, a.Token, nil), 204)
	r = e.must(redeem(b), 200).m(t)
	if r["status"] != "requested" || r["friend"] != nil || r["request"] == nil {
		t.Fatalf("redeem after unfriend must only request: %v", r)
	}
	if rel := e.must(e.do("GET", "/api/v1/users/"+a.ID, b.Token, nil), 200).m(t)["friendship"]; rel != "outgoing" {
		t.Fatalf("relation after redeem: %v", rel)
	}
	// The inviter accepts → friends again, and the removal record is cleared.
	reqID := r["request"].(map[string]any)["id"].(string)
	e.must(e.do("POST", "/api/v1/friends/requests/"+reqID+"/accept", a.Token, nil), 200)
	e.must(e.do("DELETE", "/api/v1/friends/"+a.ID, b.Token, nil), 204) // now *B* removes A
	if r := e.must(redeem(b), 200).m(t); r["status"] != "friends" {
		t.Fatalf("redeemer's own unfriend doesn't restrict them: %v", r)
	}

	// Block: blocked redeemer gets `blocked`; after unblocking, only a request.
	e.must(redeem(c), 200)
	e.must(e.do("POST", "/api/v1/blocks", a.Token, map[string]any{"user_id": c.ID}), 204)
	if r := redeem(c); r.Code != 403 || r.errCode(t) != "blocked" {
		t.Fatalf("blocked redeem: %d %s", r.Code, r.Body)
	}
	e.must(e.do("DELETE", "/api/v1/blocks/"+c.ID, a.Token, nil), 204)
	if r := e.must(redeem(c), 200).m(t); r["status"] != "requested" {
		t.Fatalf("redeem after unblock: %v", r)
	}
}

func TestInviteListAndRevoke(t *testing.T) {
	e := newEnv(t)
	a, b := e.register("Owner"), e.register("Other")
	c1 := e.must(e.do("POST", "/api/v1/invites", a.Token, nil), 200).m(t)["code"].(string)
	e.clock.Add(time.Second)
	c2 := e.must(e.do("POST", "/api/v1/invites", a.Token, nil), 200).m(t)["code"].(string)
	e.must(e.do("POST", "/api/v1/invites/"+c1+"/redeem", b.Token, nil), 200)
	var list struct {
		Invites []struct {
			Code, URL            string
			CreatedAt, ExpiresAt int64 `json:"-"`
			Uses                 int
		}
	}
	e.must(e.do("GET", "/api/v1/invites", a.Token, nil), 200).decode(t, &list)
	if len(list.Invites) != 2 || list.Invites[0].Code != c2 || list.Invites[1].Code != c1 || list.Invites[1].Uses != 1 ||
		!strings.HasSuffix(list.Invites[0].URL, "&c="+c2) {
		t.Fatalf("list: %+v", list)
	}
	raw := e.must(e.do("GET", "/api/v1/invites", a.Token, nil), 200).m(t)["invites"].([]any)[0].(map[string]any)
	for _, k := range []string{"code", "url", "created_at", "expires_at", "uses"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("invite missing %q", k)
		}
	}
	if r := e.do("DELETE", "/api/v1/invites/"+c1, b.Token, nil); r.Code != 404 {
		t.Fatalf("non-owner revoke: %d", r.Code)
	}
	e.must(e.do("DELETE", "/api/v1/invites/"+c1, a.Token, nil), 204)
	if r := e.do("GET", "/api/v1/invites/"+c1, "", nil); r.Code != 404 {
		t.Fatalf("revoked invite still resolves: %d", r.Code)
	}
	e.must(e.do("GET", "/api/v1/invites", b.Token, nil), 200).decode(t, &list)
	if len(list.Invites) != 0 {
		t.Fatal("other user's invites listed")
	}
	// Stored encrypted + hashed, never as plaintext.
	var leaked int
	e.db.R.QueryRow(`SELECT COUNT(*) FROM invites WHERE instr(CAST(code_enc AS TEXT), ?) > 0`, c2).Scan(&leaked)
	if leaked != 0 {
		t.Fatal("invite code stored in plaintext")
	}
}

// MEDIUM: revocations made outside the process (admin CLI) must end live SSE
// streams; in-process ones end them immediately.
func TestSSEClosedOnRevocation(t *testing.T) {
	e := newEnv(t)
	u := e.register("Victim")
	s := e.subscribe(u)
	env := &admin.Env{DB: e.db, API: e.srv, Out: io.Discard}
	if err := admin.Run(context.Background(), env, []string{"user", "reset-password", u.Email}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.done: // closed at the next ping (200 ms in tests, 25 s in production)
	case <-time.After(3 * time.Second):
		t.Fatal("SSE stream survived admin reset-password")
	}
	if r := e.do("GET", "/api/v1/me", u.Token, nil); r.Code != 401 {
		t.Fatal("session survived reset")
	}
}

// LOW: password change revokes all other sessions, their devices and streams.
func TestPasswordChangeRevokesOtherSessions(t *testing.T) {
	e := newEnv(t)
	u := e.register("Pw")
	other := e.must(e.do("POST", "/api/v1/auth/login", "", map[string]any{"method": "email", "identifier": u.Email, "password": "password123"}), 200).m(t)["token"].(string)
	ou := &user{ID: u.ID, Token: other}
	e.must(e.do("PUT", "/api/v1/devices/current", other, map[string]any{"platform": "ios", "relay_token": "other-device", "push_key": "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="}), 204)
	s := e.subscribe(ou)
	e.must(e.do("POST", "/api/v1/auth/password", u.Token, map[string]any{"current_password": "password123", "new_password": "newpassword1"}), 204)
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
		t.Fatal("other session's stream not closed")
	}
	if r := e.do("GET", "/api/v1/me", other, nil); r.Code != 401 {
		t.Fatal("other session still valid")
	}
	e.must(e.do("GET", "/api/v1/me", u.Token, nil), 200)
	var n int
	e.db.R.QueryRow(`SELECT COUNT(*) FROM devices WHERE relay_token = 'other-device'`).Scan(&n)
	if n != 0 {
		t.Fatal("revoked session's device kept")
	}
}

// LOW: requesting new codes must not reset the confirm-attempt budget.
func TestResetConfirmAttemptCap(t *testing.T) {
	mm := &memMailer{}
	e := newEnv(t, withMailer(mm))
	u := e.register("Reset")
	latestCode := func() string {
		for i := 0; i < 400; i++ {
			if body := mm.last(); strings.Contains(body, "code is: ") {
				j := strings.Index(body, "code is: ")
				return body[j+9 : j+17]
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("no email")
		return ""
	}
	request := func() string {
		mm.mu.Lock()
		mm.sent = nil
		mm.mu.Unlock()
		e.must(e.do("POST", "/api/v1/auth/password-reset/request", "", map[string]any{"email": u.Email}), 204)
		return latestCode()
	}
	confirm := func(code string) int {
		return e.do("POST", "/api/v1/auth/password-reset/confirm", "", map[string]any{"email": u.Email, "code": code, "new_password": "brandnew12"}).Code
	}
	for i := 0; i < 10; i++ {
		if i%4 == 0 {
			request() // a fresh code resets the per-code counter …
		}
		if c := confirm("99999999"); c != 401 {
			t.Fatalf("wrong code %d: %d", i, c)
		}
	}
	good := request()
	if c := confirm(good); c != 401 { // … but not the 24 h budget
		t.Fatalf("11th attempt allowed: %d", c)
	}
	e.clock.Add(25 * time.Hour)
	good = request()
	if c := confirm(good); c != 204 {
		t.Fatalf("after window: %d", c)
	}
}

// LOW: the per-identifier limit must not let an attacker lock a victim out.
func TestIdentifierLimitNotALockout(t *testing.T) {
	e := newEnv(t, withStrictLimits(), withConfig(func(c *config.Config) {
		c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	}))
	victim := e.register("Victim")
	login := func(ip, pw string) int {
		return e.doFrom(ip, "POST", "/api/v1/auth/login", "", map[string]any{"method": "email", "identifier": victim.Email, "password": pw}).Code
	}
	for i := 0; i < 5; i++ {
		if c := login("203.0.113.1", "wrongwrong"); c != 401 {
			t.Fatalf("attempt %d: %d", i, c)
		}
	}
	if c := login("203.0.113.1", "wrongwrong"); c != 429 {
		t.Fatalf("attacker not throttled: %d", c)
	}
	if c := login("198.51.100.2", "password123"); c != 200 {
		t.Fatalf("victim locked out: %d", c)
	}
	// A distributed guesser still hits the per-identifier global cap (30/min).
	got429 := false
	for i := 0; i < 8 && !got429; i++ {
		for j := 0; j < 5; j++ {
			if login(fmt.Sprintf("192.0.2.%d", i+10), "wrongwrong") == 429 {
				got429 = true
			}
		}
	}
	if !got429 {
		t.Fatal("no global per-identifier cap")
	}
}

// A session revoked by another process (row deleted directly in the DB, as
// `frame-server admin user reset-password` does) must not receive even the
// very next event: the stream closes instead of writing it.
func TestSSENoEventAfterExternalRevocation(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.register("Alice"), e.register("Bob")
	e.befriend(alice, bob)
	// Long ping interval: the close must come from the per-event check, not
	// from the ping re-validation.
	old := api.PingInterval
	api.PingInterval = time.Hour
	t.Cleanup(func() { api.PingInterval = old })
	s := e.subscribe(bob)
	e.sendMessage(alice, bob, "before reset", "")
	s.waitMatch(t, "message.new", "before reset")
	if _, err := e.db.W.Exec(`DELETE FROM sessions WHERE user_id = ?`, bob.ID); err != nil {
		t.Fatal(err)
	}
	e.sendMessage(alice, bob, "secret after reset", "")
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
		t.Fatal("stream not closed after external revocation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range s.events {
		if strings.Contains(ev.Data, "secret after reset") {
			t.Fatal("event delivered to a revoked session")
		}
	}
}
