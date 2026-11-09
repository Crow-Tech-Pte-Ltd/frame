package api_test

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/config"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/push"
)

type msgJSON struct {
	ID       string
	ClientID string `json:"client_id"`
	SenderID string `json:"sender_id"`
	Text     string
	ReadAt   *int64 `json:"read_at"`
	Moment   *struct {
		ID      string
		Deleted bool
		Thumb   *struct{ URL string }
	}
	Reactions []struct {
		UserID string `json:"user_id"`
		Emoji  string
	}
}

func messages(t *testing.T, e *env, u *user, peer *user, q string) ([]msgJSON, *string) {
	var page struct {
		Messages   []msgJSON
		NextCursor *string `json:"next_cursor"`
	}
	e.must(e.do("GET", "/api/v1/conversations/"+peer.ID+"/messages"+q, u.Token, nil), 200).decode(t, &page)
	return page.Messages, page.NextCursor
}

func TestChat(t *testing.T) {
	e := newEnv(t)
	a, b, c := e.register("A"), e.register("B"), e.register("C")
	e.befriend(a, b)
	sa, sb := e.subscribe(a), e.subscribe(b)

	if r := e.do("POST", "/api/v1/conversations/"+c.ID+"/messages", a.Token, map[string]any{"client_id": newClientID(), "text": "hi"}); r.Code != 403 || r.errCode(t) != "not_friends" {
		t.Fatalf("non-friend message: %d %s", r.Code, r.Body)
	}
	if r := e.do("POST", "/api/v1/conversations/"+a.ID+"/messages", a.Token, map[string]any{"text": "me"}); r.Code != 400 {
		t.Fatalf("self message: %d", r.Code)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", 2001), "bell\x07"} {
		if r := e.do("POST", "/api/v1/conversations/"+b.ID+"/messages", a.Token, map[string]any{"client_id": newClientID(), "text": bad}); r.Code != 400 {
			t.Errorf("text %q: %d", bad, r.Code)
		}
	}
	cid := newClientID()
	first := e.must(e.do("POST", "/api/v1/conversations/"+b.ID+"/messages", a.Token, map[string]any{"client_id": cid, "text": "hello\nthere"}), 201).m(t)
	again := e.must(e.do("POST", "/api/v1/conversations/"+b.ID+"/messages", a.Token, map[string]any{"client_id": cid, "text": "hello\nthere"}), 200).m(t)
	if first["id"] != again["id"] {
		t.Fatal("idempotency broken")
	}
	sb.waitMatch(t, "message.new", first["id"].(string))
	sa.waitMatch(t, "message.new", first["id"].(string)) // sender's other devices
	if n := sb.count("message.new"); n != 1 {
		t.Fatalf("duplicate message.new events: %d", n)
	}

	// Reply to a moment visible to both; invisible moments are rejected.
	bm := e.postMoment(b, momentOpts{})
	cm := e.postMoment(c, momentOpts{})
	reply := e.sendMessage(a, b, "nice", bm.ID)
	if mr := reply["moment"].(map[string]any); mr["id"] != bm.ID || mr["deleted"] != false || mr["thumb"] == nil {
		t.Fatalf("moment ref: %v", mr)
	}
	if r := e.do("POST", "/api/v1/conversations/"+b.ID+"/messages", a.Token, map[string]any{"client_id": newClientID(), "text": "x", "reply_to_moment_id": cm.ID}); r.Code != 404 {
		t.Fatalf("reply to invisible moment: %d", r.Code)
	}
	onlyA := e.postMoment(b, momentOpts{audience: "self"})
	if r := e.do("POST", "/api/v1/conversations/"+b.ID+"/messages", a.Token, map[string]any{"client_id": newClientID(), "text": "x", "reply_to_moment_id": onlyA.ID}); r.Code != 404 {
		t.Fatalf("reply to self-only moment: %d", r.Code)
	}

	// Pagination (newest first).
	var sent []string
	for i := 0; i < 5; i++ {
		e.clock.Add(time.Second)
		sent = append(sent, e.sendMessage(b, a, "m"+string(rune('0'+i)), "")["id"].(string))
	}
	page, next := messages(t, e, a, b, "?limit=3")
	if len(page) != 3 || page[0].ID != sent[4] || next == nil {
		t.Fatalf("page1: %+v", page)
	}
	page2, _ := messages(t, e, a, b, "?limit=3&before="+*next)
	if len(page2) != 3 || page2[0].ID != sent[1] {
		t.Fatalf("page2: %+v", page2)
	}

	// Conversations + unread.
	var convs struct {
		Conversations []struct {
			Peer        struct{ ID string }
			LastMessage *msgJSON `json:"last_message"`
			UnreadCount int      `json:"unread_count"`
		}
	}
	e.must(e.do("GET", "/api/v1/conversations", a.Token, nil), 200).decode(t, &convs)
	if len(convs.Conversations) != 1 || convs.Conversations[0].UnreadCount != 5 || convs.Conversations[0].LastMessage.ID != sent[4] {
		t.Fatalf("conversations: %+v", convs)
	}

	// Read receipts.
	e.must(e.do("POST", "/api/v1/conversations/"+b.ID+"/read", a.Token, map[string]any{"up_to": sent[2]}), 204)
	sb.waitMatch(t, "message.read", sent[2])
	e.must(e.do("GET", "/api/v1/conversations", a.Token, nil), 200).decode(t, &convs)
	if convs.Conversations[0].UnreadCount != 2 {
		t.Fatalf("unread after read: %d", convs.Conversations[0].UnreadCount)
	}
	bPage, _ := messages(t, e, b, a, "?limit=10")
	readBy := map[string]bool{}
	for _, m := range bPage {
		readBy[m.ID] = m.ReadAt != nil
	}
	if !readBy[sent[0]] || !readBy[sent[2]] || readBy[sent[3]] {
		t.Fatalf("read_at seen by sender: %v", readBy)
	}
	// Reader disables read receipts: sender sees nothing, no event to sender.
	e.must(e.do("PATCH", "/api/v1/me", a.Token, map[string]any{"read_receipts": false}), 200)
	before := sb.count("message.read")
	e.must(e.do("POST", "/api/v1/conversations/"+b.ID+"/read", a.Token, map[string]any{"up_to": sent[4]}), 204)
	time.Sleep(50 * time.Millisecond)
	if sb.count("message.read") != before {
		t.Fatal("message.read sent despite read_receipts=false")
	}
	bPage, _ = messages(t, e, b, a, "?limit=1")
	aPage, _ := messages(t, e, a, b, "?limit=1")
	if bPage[0].ReadAt != nil || aPage[0].ReadAt == nil {
		t.Fatalf("receipts: sender sees %v, reader sees %v", bPage[0].ReadAt, aPage[0].ReadAt)
	}
	e.must(e.do("GET", "/api/v1/conversations", a.Token, nil), 200).decode(t, &convs)
	if convs.Conversations[0].UnreadCount != 0 {
		t.Fatal("reader's unread count must still drop")
	}

	// Typing.
	e.must(e.do("POST", "/api/v1/conversations/"+b.ID+"/typing", a.Token, nil), 204)
	sb.waitMatch(t, "typing", a.ID)
	if r := e.do("POST", "/api/v1/conversations/"+c.ID+"/typing", a.Token, nil); r.Code != 403 {
		t.Fatalf("typing to stranger: %d", r.Code)
	}

	// Message reactions (either participant, one each, replaceable).
	mid := sent[4]
	e.must(e.do("PUT", "/api/v1/messages/"+mid+"/reaction", a.Token, map[string]any{"emoji": "😍"}), 204)
	e.must(e.do("PUT", "/api/v1/messages/"+mid+"/reaction", a.Token, map[string]any{"emoji": "🤣"}), 204)
	e.must(e.do("PUT", "/api/v1/messages/"+mid+"/reaction", b.Token, map[string]any{"emoji": "💛"}), 204)
	sb.waitMatch(t, "message.reaction", "🤣")
	page, _ = messages(t, e, a, b, "?limit=1")
	if len(page[0].Reactions) != 2 {
		t.Fatalf("message reactions: %+v", page[0].Reactions)
	}
	if r := e.do("PUT", "/api/v1/messages/"+mid+"/reaction", c.Token, map[string]any{"emoji": "😍"}); r.Code != 404 {
		t.Fatalf("stranger reaction: %d", r.Code)
	}
	e.must(e.do("DELETE", "/api/v1/messages/"+mid+"/reaction", a.Token, nil), 204)
	sb.waitMatch(t, "message.reaction", `"emoji":null`)

	// Unsend.
	if r := e.do("DELETE", "/api/v1/messages/"+mid, a.Token, nil); r.Code != 403 {
		t.Fatalf("recipient unsend: %d", r.Code)
	}
	if r := e.do("DELETE", "/api/v1/messages/"+mid, c.Token, nil); r.Code != 404 {
		t.Fatalf("stranger unsend: %d", r.Code)
	}
	e.must(e.do("DELETE", "/api/v1/messages/"+mid, b.Token, nil), 204)
	sa.waitMatch(t, "message.deleted", mid)
	e.must(e.do("GET", "/api/v1/conversations", a.Token, nil), 200).decode(t, &convs)
	if convs.Conversations[0].LastMessage.ID != sent[3] {
		t.Fatal("last_message not repointed after unsend")
	}

	// Moment deleted → reply renders as deleted.
	e.must(e.do("DELETE", "/api/v1/moments/"+bm.ID, b.Token, nil), 204)
	all, _ := messages(t, e, a, b, "?limit=100")
	for _, m := range all {
		if m.ID == reply["id"] && (m.Moment == nil || !m.Moment.Deleted || m.Moment.Thumb != nil) {
			t.Fatalf("deleted moment ref: %+v", m.Moment)
		}
	}

	// Ex-friends: history readable, chat read-only.
	e.must(e.do("DELETE", "/api/v1/friends/"+b.ID, a.Token, nil), 204)
	if hist, _ := messages(t, e, a, b, "?limit=100"); len(hist) == 0 {
		t.Fatal("ex-friend history gone")
	}
	if r := e.do("POST", "/api/v1/conversations/"+b.ID+"/messages", a.Token, map[string]any{"client_id": newClientID(), "text": "hey"}); r.Code != 403 || r.errCode(t) != "not_friends" {
		t.Fatalf("ex-friend send: %d", r.Code)
	}
	if r := e.do("PUT", "/api/v1/messages/"+sent[3]+"/reaction", a.Token, map[string]any{"emoji": "😍"}); r.Code != 403 {
		t.Fatalf("ex-friend reaction: %d", r.Code)
	}
	e.must(e.do("GET", "/api/v1/conversations", a.Token, nil), 200).decode(t, &convs)
	if len(convs.Conversations) != 1 {
		t.Fatal("ex-friend conversation not listed")
	}
}

func TestMessageRateLimit(t *testing.T) {
	e := newEnv(t, withStrictLimits())
	a, b := e.register("A"), e.register("B")
	e.befriend(a, b)
	for i := 0; i < 60; i++ {
		e.sendMessage(a, b, "spam", "")
	}
	if r := e.do("POST", "/api/v1/conversations/"+b.ID+"/messages", a.Token, map[string]any{"client_id": newClientID(), "text": "x"}); r.Code != 429 {
		t.Fatalf("61st message: %d", r.Code)
	}
}

func TestEventsStream(t *testing.T) {
	e := newEnv(t)
	a, b := e.register("A"), e.register("B")
	e.befriend(a, b)
	// Two devices of B both receive events (fan-out).
	b2tok := e.must(e.do("POST", "/api/v1/auth/login", "", map[string]any{"method": "email", "identifier": b.Email, "password": "password123"}), 200).m(t)["token"].(string)
	b2 := &user{ID: b.ID, Token: b2tok}
	s1, s2 := e.subscribe(b), e.subscribe(b2)
	e.postMoment(a, momentOpts{})
	s1.wait(t, "moment.new")
	s2.wait(t, "moment.new")
	// Pings arrive (25 s in production, shortened in tests).
	raw := rawStream(t, e, b, 500*time.Millisecond)
	if !strings.Contains(raw, ": ping") || !strings.HasPrefix(raw, ": connected") {
		t.Fatalf("stream: %q", raw)
	}
	// Logging out a session closes its stream.
	e.must(e.do("POST", "/api/v1/auth/logout", b2tok, nil), 204)
	select {
	case <-s2.done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream not closed on logout")
	}
	if r := e.do("GET", "/api/v1/events", "", nil); r.Code != 401 {
		t.Fatalf("unauthenticated events: %d", r.Code)
	}
}

func rawStream(t *testing.T, e *env, u *user, d time.Duration) string {
	req, _ := newRequestCtx(t, d, e.ts.URL+"/api/v1/events")
	req.Header.Set("Authorization", "Bearer "+u.Token)
	res, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}

func TestPushFanOut(t *testing.T) {
	e := newEnv(t, withConfig(func(c *config.Config) { c.PushRelay = "relay" }))
	a, b, c := e.register("Alice"), e.register("Bob"), e.register("Carol")
	e.befriend(a, b)
	e.befriend(a, c)
	if info := e.must(e.do("GET", "/api/v1/server", "", nil), 200).m(t); info["push"] == nil {
		t.Fatal("push should be advertised")
	}
	keyB := make([]byte, 32)
	keyB[0] = 1
	keyC := make([]byte, 32)
	keyC[0] = 2
	b64 := base64.StdEncoding.EncodeToString
	for _, bad := range []map[string]any{
		{"platform": "ios", "relay_token": "", "push_key": b64(keyB)},
		{"platform": "ios", "relay_token": "tok", "push_key": "short"},
		{"platform": "web", "relay_token": "tok", "push_key": b64(keyB)},
	} {
		if r := e.do("PUT", "/api/v1/devices/current", b.Token, bad); r.Code != 400 {
			t.Errorf("bad device %v: %d", bad, r.Code)
		}
	}
	e.must(e.do("PUT", "/api/v1/devices/current", b.Token, map[string]any{"platform": "ios", "relay_token": "relay-b",
		"widget_relay_token": "widget-b", "push_key": b64(keyB), "prefs": map[string]any{"moments": true, "messages": true, "reactions": true, "friends": true}}), 204)
	// Carol (Android) muted moment alerts: gets a widget refresh only.
	e.must(e.do("PUT", "/api/v1/devices/current", c.Token, map[string]any{"platform": "android", "relay_token": "relay-c",
		"widget_relay_token": nil, "push_key": b64(keyC), "prefs": map[string]any{"moments": false}}), 204)

	m := e.postMoment(a, momentOpts{caption: map[string]any{"text": "sunset"}})
	pushes := waitPushes(t, e.relay, 3)
	byTok := map[string]map[string]any{}
	for _, p := range pushes {
		byTok[p["relay_token"].(string)+"/"+p["type"].(string)] = p
	}
	alert := byTok["relay-b/alert"]
	if alert == nil || byTok["widget-b/widget"] == nil || byTok["relay-c/widget"] == nil || byTok["relay-c/alert"] != nil {
		t.Fatalf("pushes: %v", pushes)
	}
	if alert["collapse_id"] != "moment-"+m.ID {
		t.Fatalf("collapse_id: %v", alert["collapse_id"])
	}
	plain, err := push.Open(keyB, alert["payload"].(string))
	if err != nil {
		t.Fatal(err)
	}
	var p push.Payload
	json.Unmarshal(plain, &p)
	if p.V != 1 || p.Type != "moment" || p.Title != "Alice" || p.Body != `sent a new photo: "sunset"` || p.MomentID == nil || *p.MomentID != m.ID ||
		p.ThumbPath == nil || !strings.HasPrefix(*p.ThumbPath, "/m/") {
		t.Fatalf("payload: %s", plain)
	}
	if _, err := push.Open(keyC, alert["payload"].(string)); err == nil {
		t.Fatal("payload decryptable with another device's key")
	}

	// Message push with thread id.
	e.relay.mu.Lock()
	e.relay.pushes = nil
	e.relay.mu.Unlock()
	e.sendMessage(a, b, "hey bob, "+strings.Repeat("long ", 60), "")
	mp := waitPushes(t, e.relay, 1)[0]
	if mp["thread_id"] != "chat-"+a.ID || mp["type"] != "alert" {
		t.Fatalf("message push: %v", mp)
	}
	plain, _ = push.Open(keyB, mp["payload"].(string))
	json.Unmarshal(plain, &p)
	if p.Type != "message" || len([]rune(p.Body)) > 150 || p.PeerID == nil || *p.PeerID != a.ID {
		t.Fatalf("message payload: %s", plain)
	}

	// Relay says 410 → device removed; no further pushes for it.
	e.relay.mu.Lock()
	e.relay.pushes, e.relay.status = nil, 410
	e.relay.mu.Unlock()
	e.sendMessage(a, b, "are you there?", "")
	waitPushes(t, e.relay, 1)
	deadline := time.Now().Add(2 * time.Second)
	for {
		var n int
		e.db.R.QueryRow(`SELECT COUNT(*) FROM devices WHERE relay_token = 'relay-b'`).Scan(&n)
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("device not removed after 410")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 5xx is retried (3 attempts total).
	e.relay.mu.Lock()
	e.relay.pushes, e.relay.status = nil, 503
	e.relay.mu.Unlock()
	e.sendMessage(a, c, "retry me", "") // carol: messages pref on
	waitPushes(t, e.relay, 3)

	// DELETE /devices/current removes only this session's device.
	keyA := make([]byte, 32)
	e.must(e.do("PUT", "/api/v1/devices/current", a.Token, map[string]any{"platform": "android", "relay_token": "relay-a", "push_key": b64(keyA)}), 204)
	e.must(e.do("DELETE", "/api/v1/devices/current", a.Token, nil), 204)
	var na int
	e.db.R.QueryRow(`SELECT COUNT(*) FROM devices WHERE relay_token = 'relay-a'`).Scan(&na)
	if na != 0 {
		t.Fatal("device not deleted")
	}

	// Logout removes the device.
	e.must(e.do("POST", "/api/v1/auth/logout", c.Token, nil), 204)
	var n int
	e.db.R.QueryRow(`SELECT COUNT(*) FROM devices`).Scan(&n)
	if n != 0 {
		t.Fatalf("devices after logout: %d", n)
	}
}

func waitPushes(t *testing.T, r *fakeRelay, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p := r.all(); len(p) >= n {
			time.Sleep(30 * time.Millisecond)
			return r.all()
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("got %d pushes, want %d: %v", len(r.all()), n, r.all())
	return nil
}
