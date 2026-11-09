package api_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/config"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/devseed"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

func TestProfileAndAvatar(t *testing.T) {
	e := newEnv(t)
	a, b := e.register("Ann"), e.register("Ben")
	e.befriend(a, b)
	sb := e.subscribe(b)

	bad := map[string]string{
		"ab": "at least 3", "abcdefghijklmnopqrstu": "longer than 20", "ann-x": "Only letters", "1ann": "start with a letter",
		"ann.": "end with a letter", "an..n": "consecutive", "an__n": "consecutive", "an._n": "consecutive", "an_.n": "consecutive",
	}
	for u, msg := range bad {
		r := e.do("PATCH", "/api/v1/me", a.Token, map[string]any{"username": u})
		if r.Code != 400 || !strings.Contains(string(r.Body), msg) {
			t.Errorf("username %q: %d %s", u, r.Code, r.Body)
		}
	}
	me := e.must(e.do("PATCH", "/api/v1/me", a.Token, map[string]any{"username": "Ann.Lee_1"}), 200).m(t)
	if me["username"] != "ann.lee_1" {
		t.Fatalf("username not lowercased: %v", me["username"])
	}
	if r := e.do("PATCH", "/api/v1/me", b.Token, map[string]any{"username": "ANN.LEE_1"}); r.Code != 409 || r.errCode(t) != "username_taken" {
		t.Fatalf("taken: %d %s", r.Code, r.Body)
	}
	if r := e.do("PATCH", "/api/v1/me", a.Token, map[string]any{"display_name": "   "}); r.Code != 400 {
		t.Fatalf("blank name: %d", r.Code)
	}
	me = e.must(e.do("PATCH", "/api/v1/me", a.Token, map[string]any{"display_name": "Ann Lee", "discoverable": false, "searchable": false, "read_receipts": false}), 200).m(t)
	if me["display_name"] != "Ann Lee" || me["discoverable"] != false || me["searchable"] != false || me["read_receipts"] != false {
		t.Fatalf("patch: %v", me)
	}
	sb.waitMatch(t, "profile.updated", `"display_name":"Ann Lee"`)

	// Avatar.
	if r := e.request("PUT", "/api/v1/me/avatar", a.Token, bytes.NewReader([]byte("\x89PNG\r\n\x1a\n....")), "image/png"); r.Code != 415 {
		t.Fatalf("png: %d", r.Code)
	}
	if r := e.request("PUT", "/api/v1/me/avatar", a.Token, bytes.NewReader([]byte("\x89PNG\r\n\x1a\n....")), "image/jpeg"); r.Code != 415 {
		t.Fatalf("png bytes as jpeg: %d", r.Code)
	}
	if r := e.request("PUT", "/api/v1/me/avatar", a.Token, bytes.NewReader(devseed.PhotoJPEG(1, 1100)), "image/jpeg"); r.Code != 400 {
		t.Fatalf("too large dims: %d %s", r.Code, r.Body)
	}
	if r := e.request("PUT", "/api/v1/me/avatar", a.Token, bytes.NewReader(make([]byte, 3<<20)), "image/jpeg"); r.Code != 413 {
		t.Fatalf("too many bytes: %d", r.Code)
	}
	me = e.must(e.request("PUT", "/api/v1/me/avatar", a.Token, bytes.NewReader(devseed.AvatarJPEG(10)), "image/jpeg"), 200).m(t)
	av := me["avatar"].(map[string]any)
	if av["width"].(float64) != 512 || av["content_type"] != "image/jpeg" {
		t.Fatalf("avatar: %v", av)
	}
	firstURL := av["url"].(string)
	e.must(e.request("GET", firstURL, "", nil, ""), 200)
	me = e.must(e.request("PUT", "/api/v1/me/avatar", a.Token, bytes.NewReader(devseed.AvatarJPEG(90)), "image/jpeg"), 200).m(t)
	if r := e.request("GET", firstURL, "", nil, ""); r.Code != 404 {
		t.Fatalf("replaced avatar still served: %d", r.Code)
	}
	second := me["avatar"].(map[string]any)["url"].(string)
	me = e.must(e.do("DELETE", "/api/v1/me/avatar", a.Token, nil), 200).m(t)
	if me["avatar"] != nil {
		t.Fatal("avatar not removed")
	}
	if r := e.request("GET", second, "", nil, ""); r.Code != 404 {
		t.Fatalf("deleted avatar still served: %d", r.Code)
	}
}

func TestUsersSearchAndContacts(t *testing.T) {
	e := newEnv(t, withStrictLimits())
	me, alice, alina, hidden, blocker := e.register("Me"), e.register("Alice"), e.register("Alina"), e.register("Hidden"), e.register("Blocker")
	for u, name := range map[*user]string{me: "me_user", alice: "alice", alina: "alina", hidden: "alicia", blocker: "alissa"} {
		e.must(e.do("PATCH", "/api/v1/me", u.Token, map[string]any{"username": name}), 200)
	}
	e.must(e.do("PATCH", "/api/v1/me", hidden.Token, map[string]any{"searchable": false, "discoverable": false}), 200)
	e.must(e.do("POST", "/api/v1/blocks", blocker.Token, map[string]any{"user_id": me.ID}), 204)

	var sr struct {
		Users []struct {
			User       struct{ ID, Username string }
			Friendship string
		}
	}
	e.must(e.do("GET", "/api/v1/users/search?q=ALI", me.Token, nil), 200).decode(t, &sr)
	var names []string
	for _, u := range sr.Users {
		names = append(names, u.User.Username)
	}
	if strings.Join(names, ",") != "alice,alina" {
		t.Fatalf("search = %v", names)
	}
	e.must(e.do("GET", "/api/v1/users/search?q=alicia", me.Token, nil), 200).decode(t, &sr)
	if len(sr.Users) != 0 {
		t.Fatal("non-searchable exact match leaked")
	}
	e.must(e.do("GET", "/api/v1/users/search?q=@alina", me.Token, nil), 200).decode(t, &sr)
	if len(sr.Users) != 1 || sr.Users[0].User.Username != "alina" {
		t.Fatalf("exact + @ prefix: %+v", sr)
	}
	if r := e.do("GET", "/api/v1/users/search?q=a", me.Token, nil); r.Code != 400 {
		t.Fatalf("short q: %d", r.Code)
	}
	e.must(e.do("GET", "/api/v1/users/search?q=a%27--", me.Token, nil), 200)

	// Relations via GET /users/{id}.
	rel := func(viewer, target *user) string {
		return e.must(e.do("GET", "/api/v1/users/"+target.ID, viewer.Token, nil), 200).m(t)["friendship"].(string)
	}
	if rel(me, me) != "self" || rel(me, alice) != "none" {
		t.Fatal("self/none")
	}
	e.must(e.do("POST", "/api/v1/friends/requests", me.Token, map[string]any{"user_id": alice.ID}), 200)
	if rel(me, alice) != "outgoing" || rel(alice, me) != "incoming" {
		t.Fatal("outgoing/incoming")
	}
	if r := e.do("GET", "/api/v1/users/"+blocker.ID, me.Token, nil); r.Code != 404 {
		t.Fatalf("blocked-by user visible: %d", r.Code)
	}
	if rel(blocker, me) != "blocked" {
		t.Fatal("blocked")
	}
	if r := e.do("GET", "/api/v1/users/NOTANID", me.Token, nil); r.Code != 404 {
		t.Fatal("bad id")
	}

	// Contacts: hashes of alice (discoverable) and hidden (not) and an unknown.
	h := func(s string) string { return fmt.Sprintf("%x", store.ContactHash(s)) }
	var cm struct {
		Matches []struct {
			Hash       string
			User       struct{ ID string }
			Friendship string
		}
	}
	e.must(e.do("POST", "/api/v1/contacts/match", me.Token, map[string]any{"hashes": []string{
		strings.ToUpper(h(alice.Email)), h(hidden.Email), h("nobody@example.com"), h(blocker.Email)}}), 200).decode(t, &cm)
	if len(cm.Matches) != 1 || cm.Matches[0].User.ID != alice.ID || cm.Matches[0].Hash != h(alice.Email) || cm.Matches[0].Friendship != "outgoing" {
		t.Fatalf("contacts: %+v", cm)
	}
	if r := e.do("POST", "/api/v1/contacts/match", me.Token, map[string]any{"hashes": []string{"xyz"}}); r.Code != 400 {
		t.Fatalf("bad hash: %d", r.Code)
	}
	many := make([]string, 2001)
	for i := range many {
		many[i] = h(fmt.Sprint(i))
	}
	if r := e.do("POST", "/api/v1/contacts/match", me.Token, map[string]any{"hashes": many}); r.Code != 400 && r.Code != 413 {
		t.Fatalf("too many: %d", r.Code)
	}
	// 10 calls/hour/user.
	var last *resp
	for i := 0; i < 10; i++ {
		last = e.do("POST", "/api/v1/contacts/match", alina.Token, map[string]any{"hashes": []string{}})
	}
	if last.Code != 200 {
		t.Fatalf("10th call: %d", last.Code)
	}
	if r := e.do("POST", "/api/v1/contacts/match", alina.Token, map[string]any{"hashes": []string{}}); r.Code != 429 || r.Header.Get("Retry-After") == "" {
		t.Fatalf("11th call: %d", r.Code)
	}
	// The server never stores uploaded hashes: only identifier hashes of
	// discoverable users exist.
	var n int
	e.db.R.QueryRow(`SELECT COUNT(*) FROM contact_hashes`).Scan(&n)
	if n != 4 { // me, alice, alina, blocker (hidden is not discoverable)
		t.Fatalf("contact_hashes rows = %d", n)
	}
}

func TestContactHashVectors(t *testing.T) {
	b, err := os.ReadFile("../../../docs/test-vectors/contact-hash.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Vectors []struct{ Normalized, Hash string }
	}
	if err := jsonUnmarshal(b, &doc); err != nil || len(doc.Vectors) == 0 {
		t.Fatal(err)
	}
	for _, v := range doc.Vectors {
		if got := fmt.Sprintf("%x", store.ContactHash(v.Normalized)); got != v.Hash {
			t.Errorf("%s: %s != %s", v.Normalized, got, v.Hash)
		}
	}
}

func TestFriendsAndBlocks(t *testing.T) {
	e := newEnv(t)
	a, b, c := e.register("A"), e.register("B"), e.register("C")
	sb := e.subscribe(b)

	r := e.must(e.do("POST", "/api/v1/friends/requests", a.Token, map[string]any{"user_id": b.ID}), 200).m(t)
	if r["status"] != "requested" {
		t.Fatal(r)
	}
	reqID := r["request"].(map[string]any)["id"].(string)
	sb.wait(t, "friend.request")
	// Idempotent re-request.
	r2 := e.must(e.do("POST", "/api/v1/friends/requests", a.Token, map[string]any{"user_id": b.ID}), 200).m(t)
	if r2["request"].(map[string]any)["id"] != reqID {
		t.Fatal("duplicate request created")
	}
	var lists struct{ Incoming, Outgoing []struct{ ID string } }
	e.must(e.do("GET", "/api/v1/friends/requests", b.Token, nil), 200).decode(t, &lists)
	if len(lists.Incoming) != 1 || len(lists.Outgoing) != 0 {
		t.Fatalf("b requests: %+v", lists)
	}
	// Only the recipient can accept/decline; only the sender can cancel.
	if r := e.do("POST", "/api/v1/friends/requests/"+reqID+"/accept", a.Token, nil); r.Code != 404 {
		t.Fatalf("sender accept: %d", r.Code)
	}
	if r := e.do("POST", "/api/v1/friends/requests/"+reqID+"/accept", c.Token, nil); r.Code != 404 {
		t.Fatalf("stranger accept: %d", r.Code)
	}
	if r := e.do("DELETE", "/api/v1/friends/requests/"+reqID, b.Token, nil); r.Code != 404 {
		t.Fatalf("recipient cancel: %d", r.Code)
	}
	// Mutual request auto-accepts.
	auto := e.must(e.do("POST", "/api/v1/friends/requests", b.Token, map[string]any{"user_id": a.ID}), 200).m(t)
	if auto["status"] != "friends" || auto["request"] != nil {
		t.Fatalf("auto-accept: %v", auto)
	}
	if r := e.do("POST", "/api/v1/friends/requests", a.Token, map[string]any{"user_id": b.ID}); r.Code != 409 || r.errCode(t) != "conflict" {
		t.Fatalf("already friends: %d", r.Code)
	}
	if r := e.do("POST", "/api/v1/friends/requests", a.Token, map[string]any{"user_id": a.ID}); r.Code != 400 {
		t.Fatalf("self request: %d", r.Code)
	}
	var fl struct {
		Friends []struct {
			User  struct{ ID string }
			Since int64
		}
	}
	e.must(e.do("GET", "/api/v1/friends", a.Token, nil), 200).decode(t, &fl)
	if len(fl.Friends) != 1 || fl.Friends[0].User.ID != b.ID || fl.Friends[0].Since == 0 {
		t.Fatalf("friends: %+v", fl)
	}

	// Decline and cancel.
	cr := e.must(e.do("POST", "/api/v1/friends/requests", c.Token, map[string]any{"user_id": a.ID}), 200).m(t)["request"].(map[string]any)["id"].(string)
	e.must(e.do("POST", "/api/v1/friends/requests/"+cr+"/decline", a.Token, nil), 204)
	cr2 := e.must(e.do("POST", "/api/v1/friends/requests", c.Token, map[string]any{"user_id": a.ID}), 200).m(t)["request"].(map[string]any)["id"].(string)
	e.must(e.do("DELETE", "/api/v1/friends/requests/"+cr2, c.Token, nil), 204)
	e.must(e.do("GET", "/api/v1/friends/requests", a.Token, nil), 200).decode(t, &lists)
	if len(lists.Incoming) != 0 {
		t.Fatal("request not removed")
	}

	// Unfriend.
	e.must(e.do("DELETE", "/api/v1/friends/"+b.ID, a.Token, nil), 204)
	sb.wait(t, "friend.removed")
	e.must(e.do("DELETE", "/api/v1/friends/"+b.ID, a.Token, nil), 204) // idempotent
	e.must(e.do("GET", "/api/v1/friends", b.Token, nil), 200).decode(t, &fl)
	if len(fl.Friends) != 0 {
		t.Fatal("still friends")
	}

	// Block: removes friendship and requests; blocked user can't request.
	e.befriend(a, c)
	e.must(e.do("POST", "/api/v1/friends/requests", b.Token, map[string]any{"user_id": a.ID}), 200)
	e.must(e.do("POST", "/api/v1/blocks", a.Token, map[string]any{"user_id": c.ID}), 204)
	e.must(e.do("POST", "/api/v1/blocks", a.Token, map[string]any{"user_id": b.ID}), 204)
	e.must(e.do("GET", "/api/v1/friends", a.Token, nil), 200).decode(t, &fl)
	e.must(e.do("GET", "/api/v1/friends/requests", a.Token, nil), 200).decode(t, &lists)
	if len(fl.Friends) != 0 || len(lists.Incoming) != 0 {
		t.Fatalf("block did not clean up: %+v %+v", fl, lists)
	}
	var bl struct{ Users []struct{ ID string } }
	e.must(e.do("GET", "/api/v1/blocks", a.Token, nil), 200).decode(t, &bl)
	if len(bl.Users) != 2 {
		t.Fatalf("blocks: %+v", bl)
	}
	if r := e.do("POST", "/api/v1/friends/requests", a.Token, map[string]any{"user_id": c.ID}); r.Code != 403 || r.errCode(t) != "blocked" {
		t.Fatalf("request to blocked: %d %s", r.Code, r.Body)
	}
	if r := e.do("POST", "/api/v1/friends/requests", c.Token, map[string]any{"user_id": a.ID}); r.Code != 404 {
		t.Fatalf("blocked user requesting blocker: %d", r.Code)
	}
	if r := e.do("POST", "/api/v1/blocks", a.Token, map[string]any{"user_id": a.ID}); r.Code != 400 {
		t.Fatal("self block")
	}
	e.must(e.do("DELETE", "/api/v1/blocks/"+c.ID, a.Token, nil), 204)
	e.must(e.do("POST", "/api/v1/friends/requests", c.Token, map[string]any{"user_id": a.ID}), 200)
}

func TestFriendLimit(t *testing.T) {
	e := newEnv(t, withConfig(func(c *config.Config) { c.MaxFriends = 1 }))
	a, b, c := e.register("A"), e.register("B"), e.register("C")
	e.befriend(a, b)
	if info := e.must(e.do("GET", "/api/v1/server", "", nil), 200).m(t); info["limits"].(map[string]any)["max_friends"].(float64) != 1 {
		t.Fatal("max_friends not advertised")
	}
	if r := e.do("POST", "/api/v1/friends/requests", a.Token, map[string]any{"user_id": c.ID}); r.Code != 403 {
		t.Fatalf("over limit: %d %s", r.Code, r.Body)
	}
}

func TestInvites(t *testing.T) {
	e := newEnv(t)
	a, b := e.register("A"), e.register("B")
	inv := e.must(e.do("POST", "/api/v1/invites", a.Token, nil), 200).m(t)
	code := inv["code"].(string)
	if len(code) != 22 || inv["url"] != "https://frame.crow.sg/i#s=https%3A%2F%2Fframe.example.com&c="+code {
		t.Fatalf("invite: %v", inv)
	}
	if exp := int64(inv["expires_at"].(float64)); exp != e.clock.Now().Add(30*24*time.Hour).UnixMilli() {
		t.Fatalf("expires_at %d", exp)
	}
	look := e.must(e.do("GET", "/api/v1/invites/"+code, "", nil), 200).m(t)
	if look["inviter"].(map[string]any)["id"] != a.ID || look["server_name"] != "Test Frame" {
		t.Fatalf("lookup: %v", look)
	}
	if r := e.do("GET", "/api/v1/invites/AAAAAAAAAAAAAAAAAAAAAA", "", nil); r.Code != 404 {
		t.Fatalf("unknown code: %d", r.Code)
	}
	if r := e.do("POST", "/api/v1/invites/"+code+"/redeem", a.Token, nil); r.Code != 400 {
		t.Fatalf("own invite: %d", r.Code)
	}
	sa := e.subscribe(a)
	f := e.must(e.do("POST", "/api/v1/invites/"+code+"/redeem", b.Token, nil), 200).m(t)
	if f["friend"].(map[string]any)["user"].(map[string]any)["id"] != a.ID {
		t.Fatalf("redeem: %v", f)
	}
	sa.wait(t, "friend.accepted")
	e.must(e.do("POST", "/api/v1/invites/"+code+"/redeem", b.Token, nil), 200) // no-op
	// Codes are stored hashed.
	var stored int
	e.db.R.QueryRow(`SELECT COUNT(*) FROM invites WHERE code_hash = ?`, code).Scan(&stored)
	if stored != 0 {
		t.Fatal("invite code stored in plaintext")
	}
	e.clock.Add(31 * 24 * time.Hour)
	if r := e.do("GET", "/api/v1/invites/"+code, "", nil); r.Code != 404 {
		t.Fatalf("expired invite: %d", r.Code)
	}
}

func TestAccountDeletion(t *testing.T) {
	e := newEnv(t)
	a, b := e.register("Gone"), e.register("Stay")
	e.befriend(a, b)
	e.must(e.request("PUT", "/api/v1/me/avatar", a.Token, bytes.NewReader(devseed.AvatarJPEG(1)), "image/jpeg"), 200)
	m := e.postMoment(a, momentOpts{})
	bm := e.postMoment(b, momentOpts{})
	e.sendMessage(a, b, "bye", bm.ID)
	e.must(e.do("PUT", "/api/v1/moments/"+bm.ID+"/reaction", a.Token, map[string]any{"emoji": "👋"}), 204)
	sb := e.subscribe(b)

	if r := e.do("DELETE", "/api/v1/me", a.Token, map[string]any{"confirm": "yes"}); r.Code != 400 {
		t.Fatalf("bad confirm: %d", r.Code)
	}
	e.must(e.do("DELETE", "/api/v1/me", a.Token, map[string]any{"confirm": "DELETE"}), 204)
	sb.wait(t, "friend.removed")
	if r := e.do("GET", "/api/v1/me", a.Token, nil); r.Code != 401 {
		t.Fatal("token survives deletion")
	}
	if r := e.request("GET", m.Media.URL, "", nil, ""); r.Code != 404 {
		t.Fatalf("media survives deletion: %d", r.Code)
	}
	var fl struct{ Friends []any }
	e.must(e.do("GET", "/api/v1/friends", b.Token, nil), 200).decode(t, &fl)
	var convs struct{ Conversations []any }
	e.must(e.do("GET", "/api/v1/conversations", b.Token, nil), 200).decode(t, &convs)
	got := e.must(e.do("GET", "/api/v1/moments/"+bm.ID, b.Token, nil), 200).m(t)
	if len(fl.Friends) != 0 || len(convs.Conversations) != 0 || len(got["reactions"].([]any)) != 0 {
		t.Fatalf("leftovers: friends %v convs %v reactions %v", fl, convs, got["reactions"])
	}
	// Only B's media remains on disk (moment media + thumb).
	files := 0
	filepath.Walk(filepath.Join(e.dir, "media"), func(_ string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() {
			files++
		}
		return nil
	})
	var rows int
	e.db.R.QueryRow(`SELECT COUNT(*) FROM media`).Scan(&rows)
	if files != 2 || rows != 2 {
		t.Fatalf("media files %d rows %d, want 2/2", files, rows)
	}
}
