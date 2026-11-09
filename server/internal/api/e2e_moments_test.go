package api_test

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/config"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/devseed"
)

func feedIDs(t *testing.T, e *env, u *user, query string) ([]string, *string) {
	t.Helper()
	var page struct {
		Moments    []struct{ ID string }
		NextCursor *string `json:"next_cursor"`
	}
	e.must(e.do("GET", "/api/v1/moments"+query, u.Token, nil), 200).decode(t, &page)
	var out []string
	for _, m := range page.Moments {
		out = append(out, m.ID)
	}
	return out, page.NextCursor
}

func TestMomentCreateValidation(t *testing.T) {
	e := newEnv(t)
	a, b, stranger := e.register("A"), e.register("B"), e.register("S")
	e.befriend(a, b)
	photo, thumb := testPhoto()

	m := e.postMoment(a, momentOpts{clientID: "6f1c2b1e-3d4a-4b5c-8d9e-0a1b2c3d4e5f", caption: map[string]any{"text": " hi ", "style": "color", "color": "#ffffff", "background": "#e0245e"}})
	if m.Kind != "photo" || m.Media.ContentType != "image/jpeg" || len(m.RecipientIDs) != 1 || m.RecipientIDs[0] != b.ID {
		t.Fatalf("moment: %+v", m)
	}
	full := e.must(e.do("GET", "/api/v1/moments/"+m.ID, a.Token, nil), 200).m(t)
	cap := full["caption"].(map[string]any)
	if cap["type"] != "text" || cap["text"] != "hi" || cap["color"] != "#FFFFFF" || cap["background"] != "#E0245E" || cap["data"] != nil {
		t.Fatalf("caption normalisation: %v", cap)
	}
	// Idempotent retry returns the original with 200.
	r := e.postMomentRaw(a, momentOpts{clientID: "6f1c2b1e-3d4a-4b5c-8d9e-0a1b2c3d4e5f"})
	if r.Code != 200 || !strings.Contains(string(r.Body), m.ID) {
		t.Fatalf("idempotent retry: %d %s", r.Code, r.Body)
	}
	// Video + HEIC.
	v := e.postMoment(a, momentOpts{kind: "video"})
	if v.Kind != "video" || v.Media.ContentType != "video/mp4" {
		t.Fatalf("video: %+v", v)
	}
	heic := append([]byte{0, 0, 0, 0x18}, []byte("ftypheic\x00\x00\x00\x00mif1heic")...)
	heic = append(heic, bytes.Repeat([]byte{1}, 100)...)
	h := e.postMoment(a, momentOpts{media: heic, mediaType: "image/heic"})
	if h.Media.ContentType != "image/heic" {
		t.Fatalf("heic: %+v", h)
	}

	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 64)...)
	cases := []struct {
		name string
		o    momentOpts
		code int
		err  string
	}{
		{"png as photo", momentOpts{media: png, mediaType: "image/jpeg"}, 415, "unsupported_media"},
		{"jpeg as video", momentOpts{kind: "video", media: photo, mediaType: "video/mp4"}, 415, "unsupported_media"},
		{"mp4 as photo", momentOpts{media: devseed.DemoMP4(), mediaType: "image/jpeg"}, 415, "unsupported_media"},
		{"declared type mismatch", momentOpts{media: photo, mediaType: "image/heic"}, 415, "unsupported_media"},
		{"png thumb", momentOpts{thumb: png}, 415, "unsupported_media"},
		{"thumb too big", momentOpts{thumb: devseed.PhotoJPEG(2, 600)}, 400, "bad_request"},
		{"caption too long", momentOpts{caption: map[string]any{"text": strings.Repeat("é", 65)}}, 400, "bad_request"},
		{"caption multiline", momentOpts{caption: map[string]any{"text": "a\nb"}}, 400, "bad_request"},
		{"caption bad color", momentOpts{caption: map[string]any{"text": "a", "color": "red"}}, 400, "bad_request"},
		{"caption data not object", momentOpts{caption: map[string]any{"text": "a", "data": []int{1}}}, 400, "bad_request"},
		{"caption data too big", momentOpts{caption: map[string]any{"text": "a", "data": map[string]string{"x": strings.Repeat("y", 1100)}}}, 400, "bad_request"},
		{"selected empty", momentOpts{audience: "selected"}, 400, "bad_request"},
		{"selected stranger", momentOpts{audience: "selected", recipients: []string{stranger.ID}}, 403, "not_friends"},
		{"bad audience", momentOpts{audience: "everyone"}, 400, "bad_request"},
		{"bad kind", momentOpts{meta: map[string]any{"kind": "gif"}}, 400, "bad_request"},
		{"video too long", momentOpts{kind: "video", meta: map[string]any{"duration_ms": 16000}}, 400, "bad_request"},
		{"photo with duration", momentOpts{meta: map[string]any{"duration_ms": 1000}}, 400, "bad_request"},
		{"bad client id", momentOpts{meta: map[string]any{"client_id": "has spaces"}}, 400, "bad_request"},
		{"bad local date", momentOpts{localDate: "07/11/2026"}, 400, "bad_request"},
		{"bad dims", momentOpts{meta: map[string]any{"width": 0}}, 400, "bad_request"},
	}
	for _, c := range cases {
		r := e.postMomentRaw(a, c.o)
		if r.Code != c.code || r.errCode(t) != c.err {
			t.Errorf("%s: %d %s", c.name, r.Code, r.Body)
		}
	}
	// Missing parts / not multipart.
	body, ct := multipartBody(map[string]any{"client_id": newClientID(), "kind": "photo", "width": 100, "height": 100}, photo, "image/jpeg", nil)
	if r := e.request("POST", "/api/v1/moments", a.Token, body, ct); r.Code != 400 {
		t.Fatalf("missing thumb: %d", r.Code)
	}
	if r := e.do("POST", "/api/v1/moments", a.Token, map[string]any{"kind": "photo"}); r.Code != 415 {
		t.Fatalf("json body: %d", r.Code)
	}
	_ = thumb
	// Nothing leaked into storage from the rejected uploads beyond the 3
	// accepted moments (2 blobs each).
	var n int
	e.db.R.QueryRow(`SELECT COUNT(*) FROM media`).Scan(&n)
	if n != 6 {
		t.Fatalf("media rows = %d, want 6", n)
	}
}

func TestUploadSizeLimit(t *testing.T) {
	e := newEnv(t, withConfig(func(c *config.Config) { c.MaxUploadBytes = 64 << 10 }))
	a := e.register("A")
	big := append(devseed.PhotoJPEG(1, 64), bytes.Repeat([]byte{0}, 80<<10)...)
	if r := e.postMomentRaw(a, momentOpts{media: big, mediaType: "image/jpeg"}); r.Code != 413 || r.errCode(t) != "payload_too_large" {
		t.Fatalf("oversized: %d %s", r.Code, r.Body)
	}
	if r := e.postMomentRaw(a, momentOpts{thumb: append(devseed.PhotoJPEG(1, 64), bytes.Repeat([]byte{0}, 210<<10)...)}); r.Code != 413 {
		t.Fatalf("oversized thumb: %d", r.Code)
	}
}

func TestMomentRateLimit(t *testing.T) {
	e := newEnv(t, withStrictLimits())
	a := e.register("A")
	for i := 0; i < 60; i++ {
		e.postMoment(a, momentOpts{})
	}
	if r := e.postMomentRaw(a, momentOpts{}); r.Code != 429 {
		t.Fatalf("61st moment: %d", r.Code)
	}
}

func TestFeedPaginationFiltersLatestAndViews(t *testing.T) {
	e := newEnv(t)
	me, f1, f2, other := e.register("Me"), e.register("F1"), e.register("F2"), e.register("Other")
	e.befriend(me, f1)
	e.befriend(me, f2)
	var all []string
	t0 := e.clock.Now()
	for i := 0; i < 5; i++ {
		e.clock.Add(time.Minute)
		all = append([]string{e.postMoment(f1, momentOpts{}).ID}, all...)
		e.clock.Add(time.Minute)
		all = append([]string{e.postMoment(me, momentOpts{}).ID}, all...)
	}
	e.postMoment(other, momentOpts{}) // never visible
	e.clock.Add(time.Minute)
	selected := e.postMoment(f2, momentOpts{audience: "selected", recipients: []string{me.ID}})
	justMe := e.postMoment(f2, momentOpts{audience: "self"})
	all = append([]string{selected.ID}, all...)

	var got []string
	cursor := ""
	for {
		q := "?limit=4"
		if cursor != "" {
			q += "&before=" + cursor
		}
		page, next := feedIDs(t, e, me, q)
		got = append(got, page...)
		if next == nil {
			break
		}
		cursor = *next
	}
	if strings.Join(got, ",") != strings.Join(all, ",") {
		t.Fatalf("feed:\n got %v\nwant %v", got, all)
	}
	if r := e.do("GET", "/api/v1/moments?before=garbage", me.Token, nil); r.Code != 400 {
		t.Fatalf("bad cursor: %d", r.Code)
	}
	if r := e.do("GET", "/api/v1/moments?limit=1000", me.Token, nil); r.Code != 400 {
		t.Fatalf("bad limit: %d", r.Code)
	}
	if ids, _ := feedIDs(t, e, f2, "?user_id="+f2.ID); len(ids) != 2 || ids[0] != justMe.ID {
		t.Fatalf("author sees own self-audience moment: %v", ids)
	}
	if ids, _ := feedIDs(t, e, me, "?user_id="+f1.ID); len(ids) != 5 {
		t.Fatalf("user_id filter: %v", ids)
	}
	if ids, _ := feedIDs(t, e, me, "?user_id="+other.ID); len(ids) != 0 {
		t.Fatalf("stranger filter: %v", ids)
	}
	// from/to (inclusive/exclusive) on created_at.
	from := t0.Add(3 * time.Minute).UnixMilli()
	to := t0.Add(6 * time.Minute).UnixMilli()
	if ids, _ := feedIDs(t, e, me, fmt.Sprintf("?from=%d&to=%d", from, to)); len(ids) != 3 {
		t.Fatalf("from/to: %v", ids)
	}

	// latest + unseen_count + ETag.
	lr := e.must(e.do("GET", "/api/v1/moments/latest", me.Token, nil), 200)
	latest := lr.m(t)
	if latest["moment"].(map[string]any)["id"] != selected.ID || latest["unseen_count"].(float64) != 6 {
		t.Fatalf("latest: id=%v unseen=%v", latest["moment"].(map[string]any)["id"], latest["unseen_count"])
	}
	etag := lr.Header.Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) {
		t.Fatalf("etag %q", etag)
	}
	req := e.request
	_ = req
	nm := e.requestWithHeader("GET", "/api/v1/moments/latest", me.Token, "If-None-Match", etag)
	if nm.Code != 304 || len(nm.Body) != 0 {
		t.Fatalf("304 expected: %d %q", nm.Code, nm.Body)
	}
	if f := e.must(e.do("GET", "/api/v1/moments/latest?user_id="+f1.ID, me.Token, nil), 200).m(t); f["unseen_count"].(float64) != 5 {
		t.Fatalf("per-friend unseen: %v", f["unseen_count"])
	}
	if o := e.must(e.do("GET", "/api/v1/moments/latest?user_id="+other.ID, me.Token, nil), 200).m(t); o["moment"] != nil || o["unseen_count"].(float64) != 0 {
		t.Fatalf("stranger latest: %v", o)
	}

	// Views: mark, author sees viewers, unseen drops, ETag changes.
	sf2 := e.subscribe(f2)
	e.must(e.do("POST", "/api/v1/moments/views", me.Token, map[string]any{"ids": []string{selected.ID, justMe.ID, all[1], "junk"}}), 204)
	sf2.wait(t, "moment.viewed")
	if e.requestWithHeader("GET", "/api/v1/moments/latest", me.Token, "If-None-Match", etag).Code != 200 {
		t.Fatal("etag must change after viewing")
	}
	var authorView momentJSON
	e.must(e.do("GET", "/api/v1/moments/"+selected.ID, f2.Token, nil), 200).decode(t, &authorView)
	if len(authorView.Viewers) != 1 || authorView.Viewers[0].User.ID != me.ID {
		t.Fatalf("viewers: %+v", authorView)
	}
	var mine momentJSON
	e.must(e.do("GET", "/api/v1/moments/"+selected.ID, me.Token, nil), 200).decode(t, &mine)
	if !mine.Viewed || mine.Viewers != nil || mine.RecipientIDs != nil {
		t.Fatalf("recipient view: %+v", mine)
	}
	if r := e.do("POST", "/api/v1/moments/views", me.Token, map[string]any{"ids": make([]string, 101)}); r.Code != 400 {
		t.Fatal("max 100 ids")
	}

	// Hide: gone from feed/latest/unseen, still fetchable directly.
	e.must(e.do("POST", "/api/v1/moments/"+selected.ID+"/hide", me.Token, nil), 204)
	if ids, _ := feedIDs(t, e, me, "?limit=1"); ids[0] == selected.ID {
		t.Fatal("hidden moment in feed")
	}
	if l := e.must(e.do("GET", "/api/v1/moments/latest", me.Token, nil), 200).m(t); l["moment"].(map[string]any)["id"] == selected.ID {
		t.Fatal("hidden moment is latest")
	}
	e.must(e.do("GET", "/api/v1/moments/"+selected.ID, me.Token, nil), 200)
	if r := e.do("POST", "/api/v1/moments/"+all[1]+"/hide", me.Token, nil); r.Code != 400 {
		t.Fatalf("hide own: %d", r.Code)
	}

	// Streak: me posted 5 moments today → 1 day.
	meV := e.must(e.do("GET", "/api/v1/me", me.Token, nil), 200).m(t)
	if s := meV["streak"].(map[string]any); s["count"].(float64) != 1 || s["last_date"] != "2026-11-07" {
		t.Fatalf("streak: %v", s)
	}
}

func TestStreak(t *testing.T) {
	e := newEnv(t)
	a := e.register("A")
	for _, d := range []string{"2026-11-01", "2026-11-03", "2026-11-04", "2026-11-05", "2026-11-05"} {
		e.postMoment(a, momentOpts{localDate: d})
	}
	s := e.must(e.do("GET", "/api/v1/me", a.Token, nil), 200).m(t)["streak"].(map[string]any)
	if s["count"].(float64) != 3 || s["last_date"] != "2026-11-05" {
		t.Fatalf("streak: %v", s)
	}
	b := e.register("B")
	if s := e.must(e.do("GET", "/api/v1/me", b.Token, nil), 200).m(t)["streak"].(map[string]any); s["count"].(float64) != 0 || s["last_date"] != nil {
		t.Fatalf("empty streak: %v", s)
	}
}

func TestReactionsAndDelete(t *testing.T) {
	e := newEnv(t)
	a, b, c := e.register("A"), e.register("B"), e.register("C")
	e.befriend(a, b)
	e.befriend(a, c)
	m := e.postMoment(a, momentOpts{})
	sa := e.subscribe(a)
	for _, bad := range []string{"", "ab", "😀😀", strings.Repeat("👍", 5), " ", "‍"} {
		if r := e.do("PUT", "/api/v1/moments/"+m.ID+"/reaction", b.Token, map[string]any{"emoji": bad}); r.Code != 400 {
			t.Errorf("emoji %q: %d", bad, r.Code)
		}
	}
	for _, good := range []string{"❤️", "👍🏽", "🇸🇬", "1️⃣", "👩‍💻"} {
		e.must(e.do("PUT", "/api/v1/moments/"+m.ID+"/reaction", b.Token, map[string]any{"emoji": good}), 204)
	}
	sa.waitMatch(t, "moment.reaction", "👩‍💻")
	e.must(e.do("PUT", "/api/v1/moments/"+m.ID+"/reaction", c.Token, map[string]any{"emoji": "🔥"}), 204)
	if r := e.do("PUT", "/api/v1/moments/"+m.ID+"/reaction", a.Token, map[string]any{"emoji": "🔥"}); r.Code != 403 {
		t.Fatalf("own reaction: %d", r.Code)
	}
	var av, bv momentJSON
	e.must(e.do("GET", "/api/v1/moments/"+m.ID, a.Token, nil), 200).decode(t, &av)
	e.must(e.do("GET", "/api/v1/moments/"+m.ID, b.Token, nil), 200).decode(t, &bv)
	if len(av.Reactions) != 2 || len(bv.Reactions) != 1 || bv.Reactions[0].Emoji != "👩‍💻" {
		t.Fatalf("reactions author=%+v b=%+v", av.Reactions, bv.Reactions)
	}
	e.must(e.do("DELETE", "/api/v1/moments/"+m.ID+"/reaction", b.Token, nil), 204)
	sa.waitMatch(t, "moment.reaction", `"emoji":null`)
	e.must(e.do("GET", "/api/v1/moments/"+m.ID, b.Token, nil), 200).decode(t, &bv)
	if len(bv.Reactions) != 0 {
		t.Fatal("reaction not removed")
	}

	// Delete: only the author; media goes away; recipients get moment.deleted.
	sb := e.subscribe(b)
	if r := e.do("DELETE", "/api/v1/moments/"+m.ID, b.Token, nil); r.Code != 403 {
		t.Fatalf("non-author delete: %d", r.Code)
	}
	e.must(e.request("GET", m.Media.URL, "", nil, ""), 200)
	e.must(e.do("DELETE", "/api/v1/moments/"+m.ID, a.Token, nil), 204)
	sb.waitMatch(t, "moment.deleted", m.ID)
	for _, u := range []string{m.Media.URL, m.Thumb.URL} {
		if r := e.request("GET", u, "", nil, ""); r.Code != 404 {
			t.Fatalf("media after delete: %d", r.Code)
		}
	}
	if r := e.do("GET", "/api/v1/moments/"+m.ID, a.Token, nil); r.Code != 404 {
		t.Fatal("deleted moment still visible")
	}
	if r := e.do("DELETE", "/api/v1/moments/"+m.ID, a.Token, nil); r.Code != 404 {
		t.Fatal("double delete")
	}
}

func TestMediaServing(t *testing.T) {
	e := newEnv(t)
	a := e.register("A")
	v := e.postMoment(a, momentOpts{kind: "video"})
	full := e.request("GET", v.Media.URL, "", nil, "")
	if full.Code != 200 || !bytes.Equal(full.Body, devseed.DemoMP4()) {
		t.Fatalf("full: %d len %d", full.Code, len(full.Body))
	}
	for k, want := range map[string]string{"Content-Type": "video/mp4", "Cache-Control": "private, max-age=86400, immutable",
		"X-Content-Type-Options": "nosniff", "Accept-Ranges": "bytes"} {
		if got := full.Header.Get(k); got != want {
			t.Errorf("%s = %q", k, got)
		}
	}
	rr := e.requestWithHeader("GET", v.Media.URL, "", "Range", "bytes=10-19")
	if rr.Code != 206 || !bytes.Equal(rr.Body, devseed.DemoMP4()[10:20]) || rr.Header.Get("Content-Range") != fmt.Sprintf("bytes 10-19/%d", len(devseed.DemoMP4())) {
		t.Fatalf("range: %d %v", rr.Code, rr.Header)
	}
	if h := e.request("HEAD", v.Media.URL, "", nil, ""); h.Code != 200 || h.Header.Get("Content-Length") == "" {
		t.Fatalf("head: %d", h.Code)
	}
	if c := e.requestWithHeader("GET", v.Media.URL, "", "If-None-Match", full.Header.Get("ETag")); c.Code != 304 {
		t.Fatalf("conditional: %d", c.Code)
	}
	// Tampering.
	path, query, _ := strings.Cut(v.Media.URL, "?")
	tampered := []string{
		path + "?" + strings.Replace(query, "s=", "s=A", 1),
		path,
		strings.Replace(v.Media.URL, path[3:], v.Thumb.URL[3:3+26], 1), // signature of a different id
		path + "?e=9999999999&s=" + strings.Repeat("A", 43),
	}
	for _, u := range tampered {
		if r := e.request("GET", u, "", nil, ""); r.Code != 403 {
			t.Errorf("tampered %s: %d", u, r.Code)
		}
	}
	// Expiry: valid ~24-25 h.
	e.clock.Add(23 * time.Hour)
	e.must(e.request("GET", v.Media.URL, "", nil, ""), 200)
	e.clock.Add(3 * time.Hour)
	if r := e.request("GET", v.Media.URL, "", nil, ""); r.Code != 403 {
		t.Fatalf("expired: %d", r.Code)
	}
	_ = base64.RawURLEncoding
}

func TestOrphanSweep(t *testing.T) {
	e := newEnv(t)
	a := e.register("A")
	m := e.postMoment(a, momentOpts{})
	// Simulate a crash between blob write and moment commit: an unreferenced
	// media row + blob.
	ref, err := e.srv.Media().Put(t.Context(), a.ID, "image/jpeg", bytes.NewReader([]byte{0xFF, 0xD8, 0xFF}), 3, 1, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := e.srv.Media().Sweep(t.Context(), time.Hour); n != 0 {
		t.Fatal("swept within grace period")
	}
	e.clock.Add(2 * time.Hour)
	if n, err := e.srv.Media().Sweep(t.Context(), time.Hour); err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	if _, _, err := e.srv.Media().Open(t.Context(), ref.ID); err == nil {
		t.Fatal("orphan still present")
	}
	e.clock.Add(-2 * time.Hour)
	e.must(e.request("GET", m.Media.URL, "", nil, ""), 200)
}
