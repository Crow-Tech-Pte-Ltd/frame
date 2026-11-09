package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/devseed"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/testutil"
)

const fixtureDir = "../../../docs/fixtures"

// eventOrder is the SSE event table of docs/API.md.
var eventOrder = []string{
	"moment.new", "moment.deleted", "moment.reaction", "moment.viewed", "message.new", "message.deleted",
	"message.read", "message.reaction", "typing", "friend.request", "friend.accepted", "friend.removed", "profile.updated",
}

// TestWriteFixtures records real handler responses as golden fixtures for
// the mobile apps' decoding tests. Run with -update to rewrite
// docs/fixtures; without it, the test fails if the response *shapes* drift
// from the committed fixtures.
func TestWriteFixtures(t *testing.T) {
	e := newEnv(t)
	step := func() { e.clock.Add(37 * time.Second) }
	fx := map[string][]byte{}
	capture := func(name string, r *resp) {
		t.Helper()
		var buf bytes.Buffer
		if err := json.Indent(&buf, r.Body, "", "  "); err != nil {
			t.Fatalf("%s: %v (%s)", name, err, r.Body)
		}
		buf.WriteByte('\n')
		fx[name] = buf.Bytes()
	}

	// Accounts.
	reg := e.must(e.do("POST", "/api/v1/auth/register", "", map[string]any{
		"method": "email", "identifier": "alice@example.com", "password": "password123", "display_name": "Alice",
		"invite_code": nil, "device": map[string]any{"name": "Alice's iPhone", "platform": "ios"},
	}), 200)
	var ar struct {
		Token string
		User  struct{ ID string }
	}
	reg.decode(t, &ar)
	capture("auth_register.json", reg)
	alice := &user{ID: ar.User.ID, Token: ar.Token, Name: "Alice"}
	step()
	bob, carol, dave := e.register("Bob"), e.register("Carol"), e.register("Dave")
	for _, u := range []*user{alice, bob, carol, dave} {
		e.must(e.do("PATCH", "/api/v1/me", u.Token, map[string]any{"username": strings.ToLower(u.Name)}), 200)
	}
	for i, u := range []*user{alice, bob} {
		e.must(e.request("PUT", "/api/v1/me/avatar", u.Token, bytes.NewReader(devseed.AvatarJPEG(float64(120*i))), "image/jpeg"), 200)
	}

	sA, sB, sC, sD := e.subscribe(alice), e.subscribe(bob), e.subscribe(carol), e.subscribe(dave)
	step()
	e.befriend(bob, alice) // friend.request (alice) + friend.accepted (bob)
	step()
	e.must(e.do("POST", "/api/v1/friends/requests", alice.Token, map[string]any{"user_id": carol.ID}), 200)
	e.must(e.do("POST", "/api/v1/friends/requests", carol.Token, map[string]any{"user_id": dave.ID}), 200)
	step()
	e.must(e.do("PATCH", "/api/v1/me", alice.Token, map[string]any{"display_name": "Alice L."}), 200) // profile.updated → bob

	// Moments.
	step()
	aliceMoment := e.postMoment(alice, momentOpts{caption: map[string]any{"type": "text", "text": "sunset 🌅", "style": "classic"}})
	step()
	bobMoment := e.postMoment(bob, momentOpts{caption: map[string]any{"type": "location", "text": "Café Central", "style": "glass", "data": map[string]any{"name": "Café Central"}}})
	step()
	video := e.postMoment(bob, momentOpts{kind: "video"})
	step()
	e.must(e.do("POST", "/api/v1/moments/views", alice.Token, map[string]any{"ids": []string{bobMoment.ID}}), 204)
	e.must(e.do("POST", "/api/v1/moments/views", bob.Token, map[string]any{"ids": []string{aliceMoment.ID}}), 204)
	e.must(e.do("PUT", "/api/v1/moments/"+bobMoment.ID+"/reaction", alice.Token, map[string]any{"emoji": "🔥"}), 204)
	e.must(e.do("PUT", "/api/v1/moments/"+aliceMoment.ID+"/reaction", bob.Token, map[string]any{"emoji": "😍"}), 204)
	step()
	tmp := e.postMoment(bob, momentOpts{})
	e.must(e.do("DELETE", "/api/v1/moments/"+tmp.ID, bob.Token, nil), 204) // moment.deleted

	// Chat.
	step()
	m1 := e.sendMessage(bob, alice, "love this", aliceMoment.ID)
	step()
	m2 := e.sendMessage(alice, bob, "thanks! it was gorgeous", "")
	e.must(e.do("PUT", "/api/v1/messages/"+m2["id"].(string)+"/reaction", bob.Token, map[string]any{"emoji": "😍"}), 204)
	step()
	e.must(e.do("POST", "/api/v1/conversations/"+bob.ID+"/typing", alice.Token, nil), 204)
	oops := e.sendMessage(alice, bob, "oops wrong chat", "")
	e.must(e.do("DELETE", "/api/v1/messages/"+oops["id"].(string), alice.Token, nil), 204)
	e.must(e.do("POST", "/api/v1/conversations/"+alice.ID+"/read", bob.Token, map[string]any{"up_to": m2["id"]}), 204)
	e.must(e.do("POST", "/api/v1/conversations/"+bob.ID+"/read", alice.Token, map[string]any{"up_to": m1["id"]}), 204)
	step()
	e.sendMessage(bob, alice, "see you saturday?", "")

	// friend.removed: carol and dave become friends, then dave unfriends.
	var reqs struct{ Incoming []struct{ ID string } }
	e.must(e.do("GET", "/api/v1/friends/requests", dave.Token, nil), 200).decode(t, &reqs)
	e.must(e.do("POST", "/api/v1/friends/requests/"+reqs.Incoming[0].ID+"/accept", dave.Token, nil), 200)
	e.must(e.do("DELETE", "/api/v1/friends/"+carol.ID, dave.Token, nil), 204)
	e.must(e.do("POST", "/api/v1/friends/requests", dave.Token, map[string]any{"user_id": carol.ID}), 200)
	step()

	// Snapshots.
	capture("me.json", e.must(e.do("GET", "/api/v1/me", alice.Token, nil), 200))
	capture("user.json", e.must(e.do("GET", "/api/v1/users/"+bob.ID, alice.Token, nil), 200))
	capture("moments_page.json", e.must(e.do("GET", "/api/v1/moments?limit=3", alice.Token, nil), 200))
	capture("moment_video.json", e.must(e.do("GET", "/api/v1/moments/"+video.ID, alice.Token, nil), 200))
	capture("moments_latest.json", e.must(e.do("GET", "/api/v1/moments/latest", alice.Token, nil), 200))
	capture("conversations.json", e.must(e.do("GET", "/api/v1/conversations", alice.Token, nil), 200))
	capture("messages_page.json", e.must(e.do("GET", "/api/v1/conversations/"+bob.ID+"/messages?limit=50", alice.Token, nil), 200))
	capture("friends.json", e.must(e.do("GET", "/api/v1/friends", alice.Token, nil), 200))
	capture("friend_requests.json", e.must(e.do("GET", "/api/v1/friends/requests", carol.Token, nil), 200))
	capture("users_search.json", e.must(e.do("GET", "/api/v1/users/search?q=ca", alice.Token, nil), 200))
	bobHash := fmt.Sprintf("%x", store.ContactHash(bob.Email))
	capture("contacts_match.json", e.must(e.do("POST", "/api/v1/contacts/match", alice.Token, map[string]any{
		"hashes": []string{bobHash, strings.Repeat("ab", 32)}}), 200))
	invRes := e.must(e.do("POST", "/api/v1/invites", alice.Token, nil), 200)
	capture("invite.json", invRes)
	code := invRes.m(t)["code"].(string)
	capture("invite_lookup.json", e.must(e.do("GET", "/api/v1/invites/"+code, "", nil), 200))
	_, serverCode, err := e.srv.CreateServerInvite(context.Background(), nil, 1, nil, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	capture("invite_lookup_server.json", e.must(e.do("GET", "/api/v1/invites/"+serverCode, "", nil), 200))
	eve := e.register("Eve")
	capture("invite_redeem.json", e.must(e.do("POST", "/api/v1/invites/"+code+"/redeem", eve.Token, nil), 200))
	e.must(e.do("DELETE", "/api/v1/friends/"+eve.ID, alice.Token, nil), 204)
	capture("invite_redeem_requested.json", e.must(e.do("POST", "/api/v1/invites/"+code+"/redeem", eve.Token, nil), 200))
	capture("invites.json", e.must(e.do("GET", "/api/v1/invites", alice.Token, nil), 200))
	capture("sessions.json", e.must(e.do("GET", "/api/v1/auth/sessions", alice.Token, nil), 200))
	capture("error.json", e.must(e.do("GET", "/api/v1/moments/01JC3V9Z4X8Q2M7T5R1N6K0B3H", alice.Token, nil), 404))
	// v1.2.1: a suspended account's revoked bearer token.
	mallory := e.register("Mallory")
	if err := e.srv.SuspendUser(context.Background(), mallory.ID, true); err != nil {
		t.Fatal(err)
	}
	capture("error_account_disabled.json", e.must(e.do("GET", "/api/v1/me", mallory.Token, nil), 401))
	e.setRegistration("invite", true) // the production default
	capture("server.json", e.must(e.do("GET", "/api/v1/server", "", nil), 200))
	capture("error_invite_required.json", e.registerWith("frank@example.com", nil))

	// One example of each SSE event.
	var events strings.Builder
	events.WriteString(": Example Server-Sent Events from GET /api/v1/events (one of each type, real server output).\n")
	events.WriteString(": Lines starting with ':' are comments; the server also sends ': ping' every 25 s.\n\n")
	for _, typ := range eventOrder {
		raw := ""
		for deadline := time.Now().Add(3 * time.Second); raw == "" && time.Now().Before(deadline); {
			// Prefer a recipient's view (bob sees alice's moment) so nullable
			// author-only fields are exercised.
			for _, s := range []*sse{sB, sA, sC, sD} {
				if raw = s.rawFor(typ); raw != "" {
					break
				}
			}
			if raw == "" {
				time.Sleep(5 * time.Millisecond)
			}
		}
		if raw == "" {
			t.Fatalf("no %s event captured (alice %v, bob %v, carol %v, dave %v)", typ, sA.types(), sB.types(), sC.types(), sD.types())
		}
		events.WriteString(raw)
	}
	fx["events.txt"] = []byte(events.String())

	if *testutil.Update {
		if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, b := range fx {
			if err := os.WriteFile(filepath.Join(fixtureDir, name), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("wrote %d fixtures to %s", len(fx), fixtureDir)
		return
	}
	for name, b := range fx {
		old, err := os.ReadFile(filepath.Join(fixtureDir, name))
		if err != nil {
			t.Errorf("%s missing: run `go test ./... -run TestWriteFixtures -update`", name)
			continue
		}
		if strings.HasSuffix(name, ".txt") {
			if eventTypes(string(old)) != eventTypes(string(b)) {
				t.Errorf("%s event set changed: run with -update", name)
			}
			continue
		}
		if a, b := shape(t, old), shape(t, b); a != b {
			t.Errorf("%s shape changed: run `go test ./... -run TestWriteFixtures -update`\nold: %s\nnew: %s", name, a, b)
		}
	}
}

func eventTypes(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(l, "event: "); ok {
			out = append(out, v)
		}
	}
	return strings.Join(out, ",")
}

// shape renders the structure (keys and JSON types) of a document.
func shape(t *testing.T, b []byte) string {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			sb.WriteString("{")
			for _, k := range keys {
				sb.WriteString(k + ":")
				walk(x[k])
				sb.WriteString(",")
			}
			sb.WriteString("}")
		case []any:
			sb.WriteString("[")
			if len(x) > 0 {
				walk(x[0])
			}
			sb.WriteString("]")
		case string:
			sb.WriteString("s")
		case float64:
			sb.WriteString("n")
		case bool:
			sb.WriteString("b")
		case nil:
			sb.WriteString("null")
		}
	}
	walk(v)
	return sb.String()
}
