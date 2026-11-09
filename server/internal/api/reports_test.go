package api_test

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// ---- helpers ------------------------------------------------------------

func reportBody(typ, id, reason string, note any, block bool) map[string]any {
	return map[string]any{"target": map[string]any{"type": typ, "id": id}, "reason": reason, "note": note, "block": block}
}

func (e *env) report(u *user, typ, id, reason string, note any, block bool) *resp {
	e.t.Helper()
	return e.do("POST", "/api/v1/reports", u.Token, reportBody(typ, id, reason, note, block))
}

func (e *env) reportCount(where string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.R.QueryRow(`SELECT COUNT(*) FROM reports WHERE `+where, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func feedSet(t *testing.T, e *env, u *user) map[string]bool {
	t.Helper()
	var out struct{ Moments []struct{ ID string } }
	e.must(e.do("GET", "/api/v1/moments", u.Token, nil), 200).decode(t, &out)
	ids := map[string]bool{}
	for _, m := range out.Moments {
		ids[m.ID] = true
	}
	return ids
}

type adminReportJSON struct {
	ID            string
	TargetType    string  `json:"target_type"`
	ContentExists bool    `json:"content_exists"`
	Reason        string  `json:"reason"`
	Note          *string `json:"note"`
	Status        string
	Resolution    *string
	ResolvedBy    *struct{ ID string } `json:"resolved_by"`
	Reporter      *struct{ ID string }
	ReportedUser  *struct {
		ID        string
		Suspended bool
	} `json:"reported_user"`
}

func adminReports(t *testing.T, c *adminClient, query string) (list []adminReportJSON, open int) {
	t.Helper()
	var out struct {
		Reports []adminReportJSON
		Open    int
		Total   int
	}
	r := c.do("GET", "reports"+query, nil)
	if r.Code != 200 {
		t.Fatalf("reports: %d %s", r.Code, r.Body)
	}
	r.decode(t, &out)
	return out.Reports, out.Open
}

func auditActions(t *testing.T, c *adminClient) []string {
	t.Helper()
	var out struct {
		Entries []struct {
			Action string
			Target *struct{ Type, ID, Label string }
			Detail *string
		}
	}
	c.do("GET", "audit?per_page=200", nil).decode(t, &out)
	var acts []string
	for _, e := range out.Entries {
		acts = append(acts, e.Action)
	}
	return acts
}

func countAction(t *testing.T, c *adminClient, action string) int {
	n := 0
	for _, a := range auditActions(t, c) {
		if a == action {
			n++
		}
	}
	return n
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// ---- app endpoint -----------------------------------------------------------

// You can only report what you can see; everything else is the same 404 as
// the rest of the API, and errors never echo other people's data.
func TestReportAuthorization(t *testing.T) {
	e := newEnv(t)
	alice, bob, carol, dave := e.register("Alice"), e.register("Bob"), e.register("Carol"), e.register("Dave")
	e.befriend(alice, bob)
	e.befriend(alice, dave)
	m := e.postMoment(alice, momentOpts{audience: "selected", recipients: []string{bob.ID},
		caption: map[string]any{"type": "text", "text": "PRIVATECAPTION", "style": "classic"}})
	msg := e.sendMessage(alice, bob, "PRIVATEMESSAGE", "")
	msgID := msg["id"].(string)

	// Moments: stranger and a friend who isn't a recipient get 404, exactly
	// like an id that doesn't exist.
	missing := e.report(carol, "moment", "01JC3V9Z4X8Q2M7T5R1N6K0B3H", "spam", nil, false)
	e.must(missing, 404)
	for _, u := range []*user{carol, dave} {
		r := e.must(e.report(u, "moment", m.ID, "spam", nil, false), 404)
		if r.errCode(t) != "not_found" || !bytes.Equal(r.Body, missing.Body) {
			t.Fatalf("%s: %s (want the body of a missing moment %s)", u.Name, r.Body, missing.Body)
		}
	}
	e.must(e.report(alice, "moment", m.ID, "spam", nil, false), 400) // own moment
	e.must(e.report(bob, "moment", m.ID, "nudity", nil, false), 204)

	// Messages: only the other participant may report.
	e.must(e.report(carol, "message", msgID, "spam", nil, false), 404)
	e.must(e.report(dave, "message", msgID, "spam", nil, false), 404)
	e.must(e.report(alice, "message", msgID, "spam", nil, false), 400) // own message
	e.must(e.report(bob, "message", msgID, "harassment", nil, false), 204)

	// Users: anyone who can see the profile, never yourself, and not
	// someone who blocked you.
	e.must(e.report(carol, "user", alice.ID, "spam", nil, false), 204)
	e.must(e.report(carol, "user", carol.ID, "spam", nil, false), 400)
	e.must(e.report(carol, "user", "01JC3V9Z4X8Q2M7T5R1N6K0B3H", "spam", nil, false), 404)
	e.must(e.do("POST", "/api/v1/blocks", dave.Token, map[string]any{"user_id": carol.ID}), 204)
	e.must(e.report(carol, "user", dave.ID, "spam", nil, false), 404)
	e.must(e.report(carol, "user", "not-an-id", "spam", nil, false), 404)

	// Unauthenticated.
	e.must(e.do("POST", "/api/v1/reports", "", reportBody("user", alice.ID, "spam", nil, false)), 401)

	// Nothing about the target leaks in any error body.
	for _, u := range []*user{carol, dave} {
		for _, typ := range []string{"moment", "message"} {
			id := m.ID
			if typ == "message" {
				id = msgID
			}
			b := string(e.report(u, typ, id, "spam", nil, false).Body)
			for _, bad := range []string{"PRIVATE", alice.ID, "Alice"} {
				if strings.Contains(b, bad) {
					t.Fatalf("error body leaks %q: %s", bad, b)
				}
			}
		}
	}
	if n := e.reportCount("1"); n != 3 {
		t.Fatalf("stored %d reports, want 3", n)
	}
	if n := e.reportCount("owner_id = ? AND status = 'open'", alice.ID); n != 3 {
		t.Fatalf("owner not recorded: %d", n)
	}
}

func TestReportValidation(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.register("Alice"), e.register("Bob")
	bad := []any{
		map[string]any{"reason": "spam", "note": nil, "block": false},                       // no target
		reportBody("photo", alice.ID, "spam", nil, false),                                   // bad type
		reportBody("user", alice.ID, "rude", nil, false),                                    // bad reason
		reportBody("user", alice.ID, "", nil, false),                                        // empty reason
		reportBody("user", alice.ID, "spam", strings.Repeat("é", 501), false),               // note too long
		reportBody("user", alice.ID, "spam", "bell\x07", false),                             // control char
		map[string]any{"target": "user", "reason": "spam"},                                  // wrong shape
		`{"target":{"type":"user","id":"` + alice.ID + `"},"reason":"spam"} {"extra":true}`, // two values
		`not json`,
	}
	for i, b := range bad {
		r := e.do("POST", "/api/v1/reports", bob.Token, b)
		if r.Code != 400 || r.errCode(t) != "bad_request" {
			t.Errorf("case %d: %d %s", i, r.Code, r.Body)
		}
	}
	if n := e.reportCount("1"); n != 0 {
		t.Fatalf("invalid requests stored %d reports", n)
	}
	// 500 characters (multi-byte) is fine and stored trimmed; a second open
	// report on the same target is a no-op; a blank note is stored as NULL.
	e.must(e.report(bob, "user", alice.ID, "self_harm", "  "+strings.Repeat("é", 500)+"\n", false), 204)
	e.must(e.report(bob, "user", alice.ID, "other", "again", false), 204)
	var note string
	e.db.R.QueryRow(`SELECT note FROM reports WHERE reporter_id = ?`, bob.ID).Scan(&note)
	if note != strings.Repeat("é", 500) {
		t.Fatalf("note stored as %q", note)
	}
	if n := e.reportCount("reporter_id = ?", bob.ID); n != 1 {
		t.Fatalf("duplicate open report stored: %d", n)
	}
	e.must(e.report(alice, "user", bob.ID, "violence", "   ", false), 204)
	if n := e.reportCount("reporter_id = ? AND note IS NULL", alice.ID); n != 1 {
		t.Fatal("blank note not stored as NULL")
	}
	// Oversized bodies.
	r := e.do("POST", "/api/v1/reports", bob.Token, reportBody("user", alice.ID, "spam", strings.Repeat("a", 70<<10), false))
	if r.Code != 413 && r.Code != 400 {
		t.Fatalf("huge body: %d", r.Code)
	}
}

func TestReportRateLimit(t *testing.T) {
	e := newEnv(t, withStrictLimits())
	alice, bob := e.register("Alice"), e.register("Bob")
	for i := 0; i < 30; i++ {
		e.must(e.report(bob, "user", alice.ID, "spam", nil, false), 204)
	}
	r := e.report(bob, "user", alice.ID, "spam", nil, false)
	if r.Code != 429 || r.Header.Get("Retry-After") == "" {
		t.Fatalf("31st report in an hour: %d %v", r.Code, r.Header)
	}
	// Per user: someone else can still report.
	e.must(e.report(alice, "user", bob.ID, "spam", nil, false), 204)
}

// Reporting a moment hides it for the reporter; block:true blocks the
// author exactly like POST /blocks.
func TestReportHidesMomentAndBlocks(t *testing.T) {
	e := newEnv(t)
	alice, bob, carol := e.register("Alice"), e.register("Bob"), e.register("Carol")
	e.befriend(alice, bob)
	e.befriend(alice, carol)
	m1 := e.postMoment(alice, momentOpts{})
	m2 := e.postMoment(alice, momentOpts{})

	e.must(e.report(bob, "moment", m1.ID, "spam", nil, false), 204)
	feed := feedSet(t, e, bob)
	if feed[m1.ID] || !feed[m2.ID] {
		t.Fatalf("after reporting m1 bob's feed = %v", feed)
	}
	if !feedSet(t, e, carol)[m1.ID] || !feedSet(t, e, alice)[m1.ID] {
		t.Fatal("hiding leaked to other users")
	}
	var latest struct{ Moment *struct{ ID string } }
	e.must(e.do("GET", "/api/v1/moments/latest", bob.Token, nil), 200).decode(t, &latest)
	if latest.Moment == nil || latest.Moment.ID != m2.ID {
		t.Fatalf("widget still shows the reported moment: %+v", latest.Moment)
	}
	var friends struct {
		Friends []struct{ User struct{ ID string } }
	}
	e.must(e.do("GET", "/api/v1/friends", bob.Token, nil), 200).decode(t, &friends)
	if len(friends.Friends) != 1 {
		t.Fatal("report without block changed the friendship")
	}

	// Block via a message report: unfriended, blocked, both told.
	aliceStream := e.subscribe(alice)
	defer aliceStream.close()
	msg := e.sendMessage(alice, carol, "hey", "")
	e.must(e.report(carol, "message", msg["id"].(string), "harassment", "keeps texting", true), 204)
	aliceStream.waitMatch(t, "friend.removed", carol.ID)
	var blocks struct{ Users []struct{ ID string } }
	e.must(e.do("GET", "/api/v1/blocks", carol.Token, nil), 200).decode(t, &blocks)
	if len(blocks.Users) != 1 || blocks.Users[0].ID != alice.ID {
		t.Fatalf("carol's blocks = %+v", blocks.Users)
	}
	e.must(e.do("GET", "/api/v1/friends", carol.Token, nil), 200).decode(t, &friends)
	if len(friends.Friends) != 0 {
		t.Fatal("block via report kept the friendship")
	}
	if r := e.do("POST", "/api/v1/conversations/"+carol.ID+"/messages", alice.Token,
		map[string]any{"client_id": newClientID(), "text": "hello?", "reply_to_moment_id": nil}); r.Code != 403 {
		t.Fatalf("blocked author could still message: %d", r.Code)
	}

	// Block via a user report; reporting someone you already blocked works
	// (you can still see them) and stays blocked.
	dave := e.register("Dave")
	e.must(e.report(bob, "user", dave.ID, "spam", nil, true), 204)
	e.must(e.do("GET", "/api/v1/blocks", bob.Token, nil), 200).decode(t, &blocks)
	if len(blocks.Users) != 1 || blocks.Users[0].ID != dave.ID {
		t.Fatalf("bob's blocks = %+v", blocks.Users)
	}
	e.must(e.report(bob, "user", dave.ID, "spam", nil, true), 204)
}

// ---- admin ----------------------------------------------------------------------

func TestAdminReports(t *testing.T) {
	e, root, c := newAdminEnv(t)
	alice, bob, carol := e.register("Alice"), e.register("Bob"), e.register("Carol")
	e.befriend(alice, bob)
	e.befriend(alice, carol)
	m := e.postMoment(alice, momentOpts{caption: map[string]any{"type": "text", "text": "SECRETCAPTION", "style": "classic"}})
	msg := e.sendMessage(alice, bob, "SECRETMESSAGE", "")
	msgID := msg["id"].(string)

	e.must(e.report(bob, "moment", m.ID, "nudity", "not ok", false), 204)
	e.must(e.report(carol, "moment", m.ID, "violence", nil, false), 204)
	e.must(e.report(bob, "message", msgID, "harassment", nil, false), 204)
	e.must(e.report(carol, "user", alice.ID, "spam", nil, false), 204)

	list, open := adminReports(t, c, "")
	if len(list) != 4 || open != 4 {
		t.Fatalf("list %d, open %d", len(list), open)
	}
	var ov struct{ Reports struct{ Open int } }
	c.do("GET", "overview", nil).decode(t, &ov)
	if ov.Reports.Open != 4 {
		t.Fatalf("overview open = %d", ov.Reports.Open)
	}
	byType := map[string]adminReportJSON{}
	for _, r := range list {
		if r.Status != "open" || r.Reporter == nil || r.ReportedUser == nil || r.ReportedUser.ID != alice.ID || !r.ContentExists {
			t.Fatalf("bad row %+v", r)
		}
		byType[r.TargetType+":"+r.Reporter.ID] = r
	}
	momentReport, otherMomentReport := byType["moment:"+bob.ID], byType["moment:"+carol.ID]
	msgReport, userReport := byType["message:"+bob.ID], byType["user:"+carol.ID]
	if momentReport.Note == nil || *momentReport.Note != "not ok" || momentReport.Reason != "nudity" {
		t.Fatalf("moment report = %+v", momentReport)
	}

	// Filters and validation.
	if l, _ := adminReports(t, c, "?status=resolved"); len(l) != 0 {
		t.Fatal("resolved filter")
	}
	if r := c.do("GET", "reports?status=bogus", nil); r.Code != 400 {
		t.Fatalf("bad status filter: %d", r.Code)
	}
	if r := c.do("GET", "reports/01JC3V9Z4X8Q2M7T5R1N6K0B3H", nil); r.Code != 404 {
		t.Fatalf("unknown report: %d", r.Code)
	}
	if r := c.do("POST", "reports/"+userReport.ID+"/delete-content", map[string]any{}); r.Code != 400 {
		t.Fatalf("delete content of a user report: %d", r.Code)
	}

	// Dismiss: resolved, audited, count drops.
	e.must(c.do("POST", "reports/"+userReport.ID+"/dismiss", map[string]any{}), 200)
	list, open = adminReports(t, c, "")
	if open != 3 || list[len(list)-1].ID != userReport.ID || list[len(list)-1].Resolution == nil ||
		*list[len(list)-1].Resolution != "dismissed" || list[len(list)-1].ResolvedBy.ID != root.ID {
		t.Fatalf("after dismiss: open %d, last %+v", open, list[len(list)-1])
	}

	// Delete the reported moment: gone for everyone, media gone, both open
	// reports on it resolved; the report rows stay.
	e.must(c.do("POST", "reports/"+momentReport.ID+"/delete-content", map[string]any{}), 200)
	e.must(e.do("GET", "/api/v1/moments/"+m.ID, alice.Token, nil), 404)
	if r := e.request("GET", m.Media.URL, "", nil, ""); r.Code != 404 {
		t.Fatalf("media after deletion: %d", r.Code)
	}
	if n := e.reportCount("target_id = ? AND status = 'resolved' AND resolution = 'deleted_content'", m.ID); n != 2 {
		t.Fatalf("resolved moment reports = %d", n)
	}
	var det struct {
		Report  adminReportJSON
		Content map[string]any
	}
	e.must(c.do("GET", "reports/"+otherMomentReport.ID, nil), 200).decode(t, &det)
	if det.Content != nil || det.Report.ContentExists || det.Report.Status != "resolved" {
		t.Fatalf("detail after deletion: %+v", det)
	}
	if r := c.do("POST", "reports/"+momentReport.ID+"/delete-content", map[string]any{}); r.Code != 409 || r.errCode(t) != "content_gone" {
		t.Fatalf("second delete: %d %s", r.Code, r.Body)
	}
	if r := c.do("GET", "reports/"+momentReport.ID+"/media", nil); r.Code != 409 {
		t.Fatalf("media of a deleted moment: %d", r.Code)
	}

	// Delete the reported message (after looking at it: that is audited,
	// once per admin per half hour).
	e.must(c.do("GET", "reports/"+msgReport.ID, nil), 200)
	e.must(c.do("GET", "reports/"+msgReport.ID, nil), 200)
	if n := countAction(t, c, "report.view"); n != 1 {
		t.Fatalf("report.view logged %d times", n)
	}
	e.clock.Add(20 * time.Minute) // in two steps: the admin session idles out after 30
	e.must(c.do("GET", "overview", nil), 200)
	e.clock.Add(11 * time.Minute)
	e.must(c.do("GET", "reports/"+msgReport.ID, nil), 200)
	if n := countAction(t, c, "report.view"); n != 2 {
		t.Fatalf("report.view after 31 min logged %d times", n)
	}
	e.must(c.do("POST", "reports/"+msgReport.ID+"/delete-content", map[string]any{}), 200)
	var msgs struct{ Messages []struct{ ID string } }
	e.must(e.do("GET", "/api/v1/conversations/"+alice.ID+"/messages", bob.Token, nil), 200).decode(t, &msgs)
	if len(msgs.Messages) != 0 {
		t.Fatal("reported message still there")
	}

	// Suspend the reported user from a (new) report.
	e.must(e.report(bob, "user", alice.ID, "harassment", nil, false), 204)
	list, _ = adminReports(t, c, "?status=open")
	if len(list) != 1 {
		t.Fatalf("open reports = %d", len(list))
	}
	e.must(c.do("POST", "reports/"+list[0].ID+"/suspend-user", map[string]any{}), 200)
	if r := e.do("GET", "/api/v1/me", alice.Token, nil); r.Code != 401 || r.errCode(t) != "account_disabled" {
		t.Fatalf("suspended author: %d %s", r.Code, r.Body)
	}
	list, open = adminReports(t, c, "")
	if open != 0 || !list[0].ReportedUser.Suspended {
		t.Fatalf("after suspend: open %d, %+v", open, list[0].ReportedUser)
	}
	// Suspending an already-suspended user just resolves.
	e.must(c.do("POST", "reports/"+list[0].ID+"/suspend-user", map[string]any{}), 200)

	acts := auditActions(t, c)
	for _, want := range []string{"report.view", "report.dismiss", "report.delete_content", "report.suspend_user", "user.disable"} {
		if !contains(acts, want) {
			t.Errorf("audit log lacks %s: %v", want, acts)
		}
	}
	// Audit entries never carry the content.
	b := string(c.do("GET", "audit?per_page=200", nil).Body)
	for _, bad := range []string{"SECRETCAPTION", "SECRETMESSAGE", m.ID, msgID, "/m/"} {
		if strings.Contains(b, bad) {
			t.Fatalf("audit log contains %q", bad)
		}
	}

	// You can't suspend yourself from a report.
	e.befriend(root, bob)
	e.must(e.report(bob, "user", root.ID, "spam", nil, false), 204)
	list, _ = adminReports(t, c, "?status=open")
	if r := c.do("POST", "reports/"+list[0].ID+"/suspend-user", map[string]any{}); r.Code != 409 || r.errCode(t) != "self_action" {
		t.Fatalf("self suspend: %d %s", r.Code, r.Body)
	}

	// Reports outlive the reporter's account.
	e.must(e.do("DELETE", "/api/v1/me", bob.Token, map[string]any{"confirm": "DELETE"}), 204)
	list, _ = adminReports(t, c, "?per_page=100")
	if len(list) != 6 {
		t.Fatalf("reports after reporter deletion = %d", len(list))
	}
	for _, r := range list {
		if r.Reporter != nil && r.Reporter.ID == bob.ID {
			t.Fatal("deleted reporter still referenced")
		}
	}

	// Plain CSRF rules apply to the actions.
	if r := e.adminReq("POST", "/admin/api/v1/reports/"+list[0].ID+"/dismiss", map[string]any{}, c.cookie,
		map[string]string{"X-Frame-Admin": ""}); r.Code != 403 {
		t.Fatalf("dismiss without CSRF header: %d", r.Code)
	}
}

// The report detail is the one admin endpoint that returns content, and it
// returns exactly the reported item: not other moments, messages or
// captions of the same people.
func TestAdminReportDetailShowsOnlyReportedItem(t *testing.T) {
	e, _, c := newAdminEnv(t)
	alice, bob := e.register("Alice"), e.register("Bob")
	e.befriend(alice, bob)
	reported := e.postMoment(alice, momentOpts{caption: map[string]any{"type": "text", "text": "REPORTEDCAPTION", "style": "classic"}})
	other := e.postMoment(alice, momentOpts{caption: map[string]any{"type": "text", "text": "OTHERCAPTION", "style": "classic"}})
	e.sendMessage(alice, bob, "OTHERMESSAGE", "")
	msg := e.sendMessage(alice, bob, "REPORTEDMESSAGE", "")
	e.sendMessage(bob, alice, "BOBSREPLY", "")

	e.must(e.report(bob, "moment", reported.ID, "spam", nil, false), 204)
	e.must(e.report(bob, "message", msg["id"].(string), "harassment", nil, false), 204)
	e.must(e.report(bob, "user", alice.ID, "other", nil, false), 204)
	list, _ := adminReports(t, c, "")
	ids := map[string]string{}
	for _, r := range list {
		ids[r.TargetType] = r.ID
	}

	momentDet := c.do("GET", "reports/"+ids["moment"], nil)
	e.must(momentDet, 200)
	var md struct {
		Content struct {
			Type, ID, Kind string
			Caption        struct{ Text string }
			Media          struct {
				URL         string
				ContentType string `json:"content_type"`
			}
			Thumb struct{ URL string }
		}
	}
	momentDet.decode(t, &md)
	if md.Content.Type != "moment" || md.Content.ID != reported.ID || md.Content.Caption.Text != "REPORTEDCAPTION" ||
		md.Content.Media.ContentType != "image/jpeg" {
		t.Fatalf("moment detail = %s", momentDet.Body)
	}
	// Media is served by the admin API (works on FRAME_ADMIN_LISTEN, needs
	// the admin cookie) — never as a signed /m/ URL.
	if strings.Contains(string(momentDet.Body), "/m/") || !strings.HasPrefix(md.Content.Media.URL, "/admin/api/v1/reports/") {
		t.Fatalf("media URL = %q", md.Content.Media.URL)
	}
	photo, thumb := testPhoto()
	for url, want := range map[string][]byte{md.Content.Media.URL: photo, md.Content.Thumb.URL: thumb} {
		r := e.adminReq("GET", url, nil, c.cookie, nil)
		if r.Code != 200 || !bytes.Equal(r.Body, want) || r.Header.Get("Content-Type") != "image/jpeg" ||
			r.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: %d %v", url, r.Code, r.Header)
		}
		if r := e.adminReq("GET", url, nil, "", nil); r.Code != 401 {
			t.Fatalf("%s without session: %d", url, r.Code)
		}
	}
	if r := c.do("GET", "reports/"+ids["message"]+"/media", nil); r.Code != 404 {
		t.Fatalf("media of a message report: %d", r.Code)
	}

	msgDet := e.must(c.do("GET", "reports/"+ids["message"], nil), 200)
	userDet := e.must(c.do("GET", "reports/"+ids["user"], nil), 200)
	if !strings.Contains(string(msgDet.Body), "REPORTEDMESSAGE") {
		t.Fatalf("message detail = %s", msgDet.Body)
	}
	for name, b := range map[string]string{"moment": string(momentDet.Body), "message": string(msgDet.Body), "user": string(userDet.Body)} {
		for _, bad := range []string{"OTHERCAPTION", "OTHERMESSAGE", "BOBSREPLY", other.ID} {
			if strings.Contains(b, bad) {
				t.Fatalf("%s detail contains %q: %s", name, bad, b)
			}
		}
	}
	if strings.Contains(string(userDet.Body), "REPORTED") {
		t.Fatalf("user detail contains content: %s", userDet.Body)
	}
}
