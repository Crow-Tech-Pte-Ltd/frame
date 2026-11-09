package api_test

import (
	"strings"
	"testing"
)

// TestAuthzMatrix checks that a stranger, an ex-friend, a blocked user and
// a friend who is not a recipient can neither read nor act on a moment, its
// media, its reactions or a conversation they are not part of — while the
// recipient friend (positive control) can.
//
// Setup: author A posts moment M with audience "selected" = {F, X, B}; then
// A unfriends X and blocks B. N is A's friend but not a recipient; S is a
// stranger. A and F have a conversation with message MSG (a reply to M).
func TestAuthzMatrix(t *testing.T) {
	e := newEnv(t)
	A, F, X, B, N, S := e.register("Author"), e.register("Friend"), e.register("ExFriend"), e.register("Blocked"), e.register("NonRecipient"), e.register("Stranger")
	for _, u := range []*user{F, X, B, N} {
		e.befriend(A, u)
	}
	e.befriend(X, F) // X still has *some* friend: access must come from A's moment only
	M := e.postMoment(A, momentOpts{audience: "selected", recipients: []string{F.ID, X.ID, B.ID}, caption: map[string]any{"text": "secret"}})
	all := e.postMoment(A, momentOpts{}) // audience all, also checked for X/B/S
	e.must(e.do("PUT", "/api/v1/moments/"+M.ID+"/reaction", F.Token, map[string]any{"emoji": "❤️"}), 204)
	msg := e.sendMessage(F, A, "love it", M.ID)
	msgID := msg["id"].(string)
	e.must(e.do("DELETE", "/api/v1/friends/"+X.ID, A.Token, nil), 204)
	e.must(e.do("POST", "/api/v1/blocks", A.Token, map[string]any{"user_id": B.ID}), 204)

	// Positive control: the recipient friend can do everything.
	e.must(e.do("GET", "/api/v1/moments/"+M.ID, F.Token, nil), 200)
	e.must(e.request("GET", M.Media.URL, "", nil, ""), 200)
	e.must(e.do("PUT", "/api/v1/moments/"+M.ID+"/reaction", F.Token, map[string]any{"emoji": "🔥"}), 204)
	e.sendMessage(F, A, "again", M.ID)

	type actor struct {
		name string
		u    *user
		// canMessageA: N is still A's friend, so N may chat with A (but not
		// reference M, which N cannot see).
		canMessageA bool
		seesAll     bool // sees the audience-"all" moment
	}
	actors := []actor{{"stranger", S, false, false}, {"ex-friend", X, false, false}, {"blocked", B, false, false}, {"non-recipient", N, true, true}}
	for _, ac := range actors {
		t.Run(ac.name, func(t *testing.T) {
			u := ac.u
			expect := func(what string, r *resp, codes ...int) {
				t.Helper()
				for _, c := range codes {
					if r.Code == c {
						if strings.Contains(string(r.Body), "secret") || strings.Contains(string(r.Body), M.Media.URL[3:29]) {
							t.Errorf("%s leaked moment data: %s", what, r.Body)
						}
						return
					}
				}
				t.Errorf("%s: status %d (%s), want %v", what, r.Code, r.Body, codes)
			}
			// Moments.
			expect("get moment", e.do("GET", "/api/v1/moments/"+M.ID, u.Token, nil), 404)
			expect("feed", e.do("GET", "/api/v1/moments?limit=100", u.Token, nil), 200)
			expect("feed by author", e.do("GET", "/api/v1/moments?user_id="+A.ID, u.Token, nil), 200)
			expect("latest by author", e.do("GET", "/api/v1/moments/latest?user_id="+A.ID, u.Token, nil), 200)
			expect("latest", e.do("GET", "/api/v1/moments/latest", u.Token, nil), 200)
			if ids, _ := feedIDs(t, e, u, "?user_id="+A.ID); (len(ids) > 0) != ac.seesAll {
				t.Errorf("author feed visibility = %v", ids)
			}
			expect("react", e.do("PUT", "/api/v1/moments/"+M.ID+"/reaction", u.Token, map[string]any{"emoji": "😈"}), 404)
			expect("unreact", e.do("DELETE", "/api/v1/moments/"+M.ID+"/reaction", u.Token, nil), 404)
			expect("hide", e.do("POST", "/api/v1/moments/"+M.ID+"/hide", u.Token, nil), 404)
			expect("delete", e.do("DELETE", "/api/v1/moments/"+M.ID, u.Token, nil), 404)
			expect("view", e.do("POST", "/api/v1/moments/views", u.Token, map[string]any{"ids": []string{M.ID, all.ID}}), 204)
			if !ac.seesAll {
				expect("get all-moment", e.do("GET", "/api/v1/moments/"+all.ID, u.Token, nil), 404)
				expect("react all-moment", e.do("PUT", "/api/v1/moments/"+all.ID+"/reaction", u.Token, map[string]any{"emoji": "😈"}), 404)
			}
			// Media: a forged signature for M's media id is rejected.
			path, _, _ := strings.Cut(M.Media.URL, "?")
			expect("forged media", e.request("GET", path+"?e=4102444800&s="+strings.Repeat("x", 43), "", nil, ""), 403)
			// Messages.
			expect("reply referencing M", e.do("POST", "/api/v1/conversations/"+A.ID+"/messages", u.Token,
				map[string]any{"client_id": newClientID(), "text": "hi", "reply_to_moment_id": M.ID}), 403, 404)
			if ac.canMessageA {
				expect("message A", e.do("POST", "/api/v1/conversations/"+A.ID+"/messages", u.Token, map[string]any{"client_id": newClientID(), "text": "hi"}), 201)
			} else {
				r := e.do("POST", "/api/v1/conversations/"+A.ID+"/messages", u.Token, map[string]any{"client_id": newClientID(), "text": "hi"})
				expect("message A", r, 403)
				if r.errCode(t) != "not_friends" {
					t.Errorf("message A code %s", r.errCode(t))
				}
				expect("typing A", e.do("POST", "/api/v1/conversations/"+A.ID+"/typing", u.Token, nil), 403)
			}
			if hist, _ := messages(t, e, u, A, "?limit=100"); containsID(hist, msgID) {
				t.Error("sees A↔F message via A")
			}
			if hist, _ := messages(t, e, u, F, "?limit=100"); containsID(hist, msgID) {
				t.Error("sees A↔F message via F")
			}
			expect("delete A↔F message", e.do("DELETE", "/api/v1/messages/"+msgID, u.Token, nil), 404)
			expect("react A↔F message", e.do("PUT", "/api/v1/messages/"+msgID+"/reaction", u.Token, map[string]any{"emoji": "😈"}), 404)
			expect("read A↔F", e.do("POST", "/api/v1/conversations/"+F.ID+"/read", u.Token, map[string]any{"up_to": msgID}), 204)
		})
	}

	// Nothing the actors did leaked into A's view of M.
	var av momentJSON
	e.must(e.do("GET", "/api/v1/moments/"+M.ID, A.Token, nil), 200).decode(t, &av)
	if len(av.Reactions) != 1 || av.Reactions[0].User.ID != F.ID {
		t.Fatalf("reactions on M: %+v", av.Reactions)
	}
	for _, v := range av.Viewers {
		if v.User.ID != F.ID {
			t.Fatalf("non-recipient recorded as viewer: %+v", av.Viewers)
		}
	}
	// A↔F message still unread for A (nobody else could mark it read).
	hist, _ := messages(t, e, F, A, "?limit=100")
	for _, m := range hist {
		if m.ID == msgID && m.ReadAt != nil {
			t.Fatal("third party marked message read")
		}
	}
	// The blocked user cannot even see A.
	if r := e.do("GET", "/api/v1/users/"+A.ID, B.Token, nil); r.Code != 404 {
		t.Fatalf("blocked user sees blocker: %d", r.Code)
	}
	if r := e.do("POST", "/api/v1/friends/requests", B.Token, map[string]any{"user_id": A.ID}); r.Code != 404 {
		t.Fatalf("blocked user re-requests: %d", r.Code)
	}
	// Re-friending X restores access to M (access follows friendship).
	e.befriend(A, X)
	e.must(e.do("GET", "/api/v1/moments/"+M.ID, X.Token, nil), 200)
}

func containsID(ms []msgJSON, id string) bool {
	for _, m := range ms {
		if m.ID == id {
			return true
		}
	}
	return false
}
