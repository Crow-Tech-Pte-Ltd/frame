package api_test

// Regression tests for the 2026-11-09 pre-publication security review.

import (
	"bytes"
	"encoding/base32"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/config"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/storage"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/totp"
)

// countingS3 is a minimal path-style S3 endpoint (no signature checks) that
// counts object GETs, to measure how many backend requests one client
// request causes.
type countingS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	gets    int
}

func (f *countingS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		f.objects[r.URL.Path] = body
	case http.MethodHead, http.MethodGet:
		obj, ok := f.objects[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(obj)))
			return
		}
		f.gets++
		start, end := 0, len(obj)-1
		if rg := r.Header.Get("Range"); rg != "" {
			fmt.Sscanf(rg, "bytes=%d-%d", &start, &end)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(obj)))
			w.WriteHeader(http.StatusPartialContent)
		}
		_, _ = w.Write(obj[start : end+1])
	case http.MethodDelete:
		delete(f.objects, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (f *countingS3) getCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.gets }

// A signed media URL is a capability anyone with a profile picture or a
// moment can get (even unauthenticated, via GET /invites/{code}). A
// multi-range request must not turn one request into thousands of storage
// backend fetches (each a separate ranged GET against S3/R2).
func TestMediaMultiRangeIsNotAmplified(t *testing.T) {
	fake := &countingS3{objects: map[string][]byte{}}
	s3srv := httptest.NewServer(fake)
	t.Cleanup(s3srv.Close)
	s3, err := storage.NewS3(storage.S3Config{Endpoint: s3srv.URL, Bucket: "frame", AccessKeyID: "k", SecretAccessKey: "s", Prefix: "frame/"})
	if err != nil {
		t.Fatal(err)
	}
	enc, err := storage.NewEncrypted(s3, bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, withStorage(enc))
	alice := e.register("Alice")
	// A 300 KB "photo" spans several 64 KiB encryption chunks.
	big := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{0x11}, 300<<10)...)
	m := e.postMoment(alice, momentOpts{media: big, mediaType: "image/jpeg"})

	// Sanity: a normal single range still works.
	r := e.requestWithHeader("GET", m.Media.URL, "", "Range", "bytes=70000-70009")
	if r.Code != http.StatusPartialContent || !bytes.Equal(r.Body, big[70000:70010]) {
		t.Fatalf("single range: %d %q", r.Code, r.Body)
	}

	// 1-byte ranges alternating between two chunks: Go's ServeContent
	// accepts up to 200 of them (their sum is below the object size), and
	// before the fix each one was a separate ranged GET to the bucket.
	var parts []string
	for i := 0; i < 199; i++ {
		parts = append(parts, fmt.Sprintf("%d-%d", (i%2)*70000, (i%2)*70000))
	}
	before := fake.getCount()
	r = e.requestWithHeader("GET", m.Media.URL, "", "Range", "bytes="+strings.Join(parts, ","))
	gets := fake.getCount() - before
	if r.Code != http.StatusOK || !bytes.Equal(r.Body, big) {
		t.Fatalf("multi-range: status %d, %d bytes; want the whole object", r.Code, len(r.Body))
	}
	if gets > 2 {
		t.Fatalf("one multi-range request caused %d storage GETs", gets)
	}
}

// FRAME_ADMIN=local trusts loopback. A reverse proxy or tunnel on the same
// host (Caddy, nginx, cloudflared) that the operator forgot to list in
// FRAME_TRUSTED_PROXIES also connects from loopback, so before the fix the
// "local-only" panel answered every visitor on the internet. A loopback
// peer that forwards someone else's request must not count as local.
func TestAdminLocalIgnoresUnlistedLoopbackProxy(t *testing.T) {
	local := newEnv(t, withConfig(func(c *config.Config) { c.Admin = "local" })) // nothing trusted
	if r := local.adminReq("GET", "/admin/api/v1/setup", nil, "", nil); r.Code != 200 {
		t.Fatalf("direct loopback client: %d", r.Code)
	}
	for _, h := range []map[string]string{
		{"X-Forwarded-For": "203.0.113.9"},  // Caddy, nginx
		{"CF-Connecting-IP": "203.0.113.9"}, // cloudflared without the "cloudflare" keyword
		{"Forwarded": "for=203.0.113.9"},    // RFC 7239 proxies
		{"X-Real-Ip": "203.0.113.9"},        // nginx proxy_set_header convention
		{"X-Forwarded-For": "127.0.0.1"},    // a spoofable value proves nothing either
	} {
		if r := local.adminReq("GET", "/admin/api/v1/setup", nil, "", h); r.Code != 404 {
			t.Errorf("proxied request %v from an unlisted loopback proxy: %d, want 404", h, r.Code)
		}
		if r := local.adminReq("GET", "/admin/", nil, "", h); r.Code != 404 {
			t.Errorf("SPA via unlisted proxy %v: %d, want 404", h, r.Code)
		}
	}
	// The app API is unaffected.
	if r := local.adminReq("GET", "/api/v1/server", nil, "", map[string]string{"X-Forwarded-For": "203.0.113.9"}); r.Code != 200 {
		t.Fatalf("app API: %d", r.Code)
	}

	// "cloudflare" alone trusts cloudflared on loopback for CF-Connecting-IP:
	// the real visitor IP is used (404), and a loopback proxy that only sends
	// X-Forwarded-For (not listed) is still refused.
	cf := newEnv(t, withConfig(func(c *config.Config) { c.Admin = "local"; c.TrustCloudflare = true }))
	if r := cf.adminReq("GET", "/admin/api/v1/setup", nil, "", map[string]string{"CF-Connecting-IP": "203.0.113.9"}); r.Code != 404 {
		t.Errorf("cloudflared visitor: %d", r.Code)
	}
	if r := cf.adminReq("GET", "/admin/api/v1/setup", nil, "", map[string]string{"X-Forwarded-For": "203.0.113.9"}); r.Code != 404 {
		t.Errorf("unlisted XFF proxy with cloudflare keyword: %d", r.Code)
	}

	// A listed proxy forwarding a local client keeps working.
	listed := newEnv(t, withConfig(func(c *config.Config) {
		c.Admin = "local"
		c.TrustedProxies = append(c.TrustedProxies, mustPrefix(t, "127.0.0.1/32"))
	}))
	if r := listed.adminReq("GET", "/admin/api/v1/setup", nil, "", map[string]string{"X-Forwarded-For": "127.0.0.1"}); r.Code != 200 {
		t.Errorf("listed proxy, local client: %d", r.Code)
	}
}

// Viewing reported content is the panel's one deliberate exception to "no
// content", and ADMIN.md promises it is audit-logged (report.view). Before
// the fix the photo/video and thumbnail could be streamed through
// reports/{id}/media and /thumb without GET reports/{id}, leaving no trace.
func TestReportMediaViewIsAudited(t *testing.T) {
	e, _, admin := newAdminEnv(t)
	alice, bob := e.register("Alice"), e.register("Bob")
	e.befriend(alice, bob)
	m := e.postMoment(alice, momentOpts{})
	e.must(e.report(bob, "moment", m.ID, "nudity", nil, false), 204)
	list, _ := adminReports(t, admin, "")
	if len(list) != 1 {
		t.Fatalf("reports: %d", len(list))
	}
	id := list[0].ID
	if n := countAction(t, admin, "report.view"); n != 0 {
		t.Fatalf("report.view before viewing: %d", n)
	}
	for _, p := range []string{"media", "thumb"} {
		if r := admin.do("GET", "reports/"+id+"/"+p, nil); r.Code != 200 || len(r.Body) == 0 {
			t.Fatalf("%s: %d", p, r.Code)
		}
	}
	if n := countAction(t, admin, "report.view"); n != 1 {
		t.Fatalf("report.view after streaming the reported media: %d, want 1", n)
	}
	// Opening the detail and re-fetching (video players issue many ranged
	// requests) is still logged once per half hour.
	admin.do("GET", "reports/"+id, nil)
	admin.do("GET", "reports/"+id+"/media", nil)
	if n := countAction(t, admin, "report.view"); n != 1 {
		t.Fatalf("report.view after repeats: %d, want 1", n)
	}
	for i := 0; i < 2; i++ { // stay within the 30-minute idle timeout
		e.clock.Add(16 * time.Minute)
		admin.do("GET", "session", nil)
	}
	admin.do("GET", "reports/"+id+"/thumb", nil)
	if n := countAction(t, admin, "report.view"); n != 2 {
		t.Fatalf("report.view after 32 min: %d, want 2", n)
	}
}

// Admin two-factor codes are 6 digits (3 valid per 90 s window). Before the
// fix, someone who knew an admin's password could guess codes at the login
// limits — 30/min per identifier, and the username, e-mail and phone are
// separate identifiers — breaking the second factor within days. Code
// attempts are now limited per account, whatever the identifier or IP.
func TestAdminSecondFactorGuessingLimited(t *testing.T) {
	e, _, c := newAdminEnv(t, withStrictLimits(), withConfig(func(c *config.Config) {
		c.TrustedProxies = append(c.TrustedProxies, mustPrefix(t, "127.0.0.1/32"))
	}))
	setup := e.must(c.do("POST", "security/totp/setup", map[string]any{"password": "password123"}), 200).m(t)
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(setup["secret"].(string))
	e.must(c.do("POST", "security/totp/enable", map[string]any{"code": totp.Code(secret, e.clock.Now())}), 200)
	e.clock.Add(30 * time.Second)

	login := func(i int, ident, code string) *resp {
		return e.adminReq("POST", "/admin/api/v1/login", map[string]any{"identifier": ident, "password": "password123", "totp_code": code},
			"", map[string]string{"X-Forwarded-For": fmt.Sprintf("203.0.113.%d", i)})
	}
	idents := []string{"root", "root1@example.com"} // username and e-mail of the same account
	for i := 0; i < 10; i++ {
		if r := login(i, idents[i%2], "000000"); r.Code != 401 || r.errCode(t) != "totp_invalid" {
			t.Fatalf("guess %d: %d %s", i, r.Code, r.Body)
		}
	}
	good := totp.Code(secret, e.clock.Now())
	if r := login(50, "root", good); r.Code != http.StatusTooManyRequests {
		t.Fatalf("code attempt after 10 wrong guesses: %d %s, want 429", r.Code, r.Body)
	}
	// The budget refills: the real admin gets in later.
	e.clock.Add(15 * time.Minute)
	if r := login(51, "root", totp.Code(secret, e.clock.Now())); r.Code != 200 {
		t.Fatalf("after waiting: %d %s", r.Code, r.Body)
	}
}

// slowBody yields body in small chunks with a delay between them (a phone
// uploading over a poor connection).
type slowBody struct {
	b     []byte
	chunk int
	delay time.Duration
}

func (s *slowBody) Read(p []byte) (int, error) {
	if len(s.b) == 0 {
		return 0, io.EOF
	}
	time.Sleep(s.delay)
	n := copy(p[:min(len(p), s.chunk)], s.b)
	s.b = s.b[n:]
	return n, nil
}

// The production http.Server has WriteTimeout 60 s, counted from when the
// request headers were read. An upload taking longer than that used to
// lose its 201 response (the client saw a dropped connection).
func TestSlowUploadStillGetsResponse(t *testing.T) {
	e := newEnv(t)
	alice := e.register("Alice")
	srv := &http.Server{Handler: e.srv.Handler(), ReadTimeout: time.Second, WriteTimeout: time.Second}
	ts := httptest.NewUnstartedServer(srv.Handler)
	ts.Config = srv
	ts.Start()
	t.Cleanup(ts.Close)

	photo, thumb := testPhoto()
	meta := map[string]any{"client_id": newClientID(), "kind": "photo", "caption": nil, "audience": "all",
		"recipient_ids": nil, "width": 1440, "height": 1440, "duration_ms": nil, "local_date": "2026-11-07"}
	body, ct := multipartBody(meta, photo, "image/jpeg", thumb)
	n := body.Len()
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/moments", &slowBody{b: body.Bytes(), chunk: n/10 + 1, delay: 250 * time.Millisecond})
	req.ContentLength = int64(n)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer "+alice.Token)
	res, err := ts.Client().Do(req) // ~2.5 s upload vs 1 s WriteTimeout
	if err != nil {
		t.Fatalf("slow upload lost its response: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d %s", res.StatusCode, b)
	}
}

// Behind a reverse proxy or tunnel that terminates TLS (Caddy, Cloudflare
// Tunnel) the server never sees r.TLS, so it sent no HSTS header, although
// it already trusts that proxy's scheme for the admin cookie's Secure flag.
func TestHSTSBehindTrustedTLSProxy(t *testing.T) {
	trusted := newEnv(t, withConfig(func(c *config.Config) {
		c.TrustedProxies = append(c.TrustedProxies, mustPrefix(t, "127.0.0.1/32"))
	}))
	if r := trusted.requestWithHeader("GET", "/api/v1/server", "", "X-Forwarded-Proto", "https"); r.Header.Get("Strict-Transport-Security") == "" {
		t.Fatal("no HSTS behind a trusted TLS-terminating proxy")
	}
	if r := trusted.requestWithHeader("GET", "/api/v1/server", "", "X-Forwarded-Proto", "http"); r.Header.Get("Strict-Transport-Security") != "" {
		t.Fatal("HSTS on a plain-HTTP request")
	}
	cf := newEnv(t, withConfig(func(c *config.Config) { c.TrustCloudflare = true }))
	if r := cf.requestWithHeader("GET", "/healthz", "", "CF-Visitor", `{"scheme":"https"}`); r.Header.Get("Strict-Transport-Security") == "" {
		t.Fatal("no HSTS behind cloudflared")
	}
	// An untrusted client can't switch it on (or claim https) by itself.
	untrusted := newEnv(t)
	if r := untrusted.requestWithHeader("GET", "/api/v1/server", "", "X-Forwarded-Proto", "https"); r.Header.Get("Strict-Transport-Security") != "" {
		t.Fatal("HSTS from an untrusted X-Forwarded-Proto")
	}
}
