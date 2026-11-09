package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/api"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/auth"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/config"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/db"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/devseed"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ids"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/oidc"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/push"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/storage"
	_ "github.com/Crow-Tech-Pte-Ltd/frame/server/internal/testutil"
)

func TestMain(m *testing.M) {
	// Cheap Argon2 parameters keep the HTTP suite fast; internal/auth tests
	// cover the production parameters.
	auth.DefaultParams = auth.Params{MemoryKiB: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}
	api.PingInterval = 200 * time.Millisecond
	code := m.Run()
	for i := len(suiteCleanups) - 1; i >= 0; i-- {
		suiteCleanups[i]()
	}
	os.Exit(code)
}

// suiteCleanups run after all tests/benchmarks (shared benchmark fixtures).
var suiteCleanups []func()

type seededReader struct{ r *rand.ChaCha8 }

func (s seededReader) Read(p []byte) (int, error) { return s.r.Read(p) }

// fakeClock is a settable clock shared by the server and the test.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeRelay records pushes and can answer with a chosen status.
type fakeRelay struct {
	mu     sync.Mutex
	pushes []map[string]any
	status int
	ts     *httptest.Server
}

func (f *fakeRelay) handler(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.pushes = append(f.pushes, body)
	st := f.status
	f.mu.Unlock()
	if st == 0 {
		st = 200
	}
	w.WriteHeader(st)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (f *fakeRelay) all() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.pushes...)
}

type env struct {
	t      testing.TB
	srv    *api.Server
	ts     *httptest.Server
	db     *db.DB
	clock  *fakeClock
	cfg    *config.Config
	relay  *fakeRelay
	dir    string
	userNo int
}

type envOpt func(*config.Config, *api.Deps)

func withConfig(f func(*config.Config)) envOpt {
	return func(c *config.Config, _ *api.Deps) { f(c) }
}

func withVerifiers(apple, google *oidc.Verifier) envOpt {
	return func(_ *config.Config, d *api.Deps) { d.Apple, d.Google = apple, google }
}

func withMailer(m api.Mailer) envOpt {
	return func(_ *config.Config, d *api.Deps) { d.Mailer = m }
}

// withStrictLimits uses the production rate limits.
func withStrictLimits() envOpt {
	return func(_ *config.Config, d *api.Deps) { d.RateLimitScale = 1 }
}

func withLogger(l *slog.Logger) envOpt {
	return func(_ *config.Config, d *api.Deps) { d.Log = l }
}

func withStorage(s storage.Storage) envOpt {
	return func(_ *config.Config, d *api.Deps) { d.Storage = s }
}

func newEnv(t testing.TB, opts ...envOpt) *env {
	t.Helper()
	ids.SetDefault(ids.NewGenerator(seededReader{rand.NewChaCha8([32]byte{7})}))
	dir := t.TempDir()
	cfg, err := config.Load(config.Source{Overrides: map[string]string{
		"FRAME_DATA_DIR": dir, "FRAME_NAME": "Test Frame", "FRAME_PUSH_RELAY": "",
		"FRAME_GOOGLE_CLIENT_IDS": "", "FRAME_PUBLIC_URL": "https://frame.example.com",
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Production defaults to invite-only registration; most tests just need
	// accounts, so the suite runs open (not marked as set in the config, so
	// the admin panel can still change it). Registration-gate tests switch
	// modes explicitly.
	cfg.Registration = "open"
	// The overrides above are test plumbing, not an operator's frame.conf:
	// nothing is locked in the admin panel unless a test says so.
	cfg.Explicit = map[string]bool{}
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(dir, "frame.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	local, err := storage.NewLocal(filepath.Join(dir, "media"))
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{t: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}
	deps := api.Deps{Config: cfg, DB: d, Storage: local, MediaKey: bytes.Repeat([]byte{0x42}, 32),
		ServerID: "01JC3V9Z4X8Q2M7T5R1N6K0B3H", Version: "0.1.0", Now: clock.Now,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), RateLimitScale: 100}
	for _, o := range opts {
		o(cfg, &deps)
	}
	e := &env{t: t, db: d, clock: clock, cfg: cfg, dir: dir}
	if cfg.PushRelay == "relay" {
		e.relay = &fakeRelay{}
		e.relay.ts = httptest.NewServer(http.HandlerFunc(e.relay.handler))
		t.Cleanup(e.relay.ts.Close)
		cfg.PushRelay = e.relay.ts.URL
		deps.PushOptions = &push.Options{RelayURL: cfg.PushRelay, BaseBackoff: 10 * time.Millisecond}
	}
	srv, err := api.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	srv.Start()
	e.srv = srv
	e.ts = httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
		e.ts.Close()
	})
	return e
}

type resp struct {
	Code   int
	Body   []byte
	Header http.Header
}

func (r *resp) decode(t testing.TB, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("decode %q: %v", r.Body, err)
	}
}

func (r *resp) m(t testing.TB) map[string]any {
	var v map[string]any
	r.decode(t, &v)
	return v
}

func (r *resp) errCode(t testing.TB) string {
	var v struct {
		Error struct{ Code, Message string }
	}
	_ = json.Unmarshal(r.Body, &v)
	return v.Error.Code
}

// request sends a request through the real HTTP server.
func (e *env) request(method, path, token string, body io.Reader, ctype string) *resp {
	e.t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := e.ts.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return &resp{Code: res.StatusCode, Body: b, Header: res.Header}
}

func (e *env) do(method, path, token string, body any) *resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			rd = strings.NewReader(b)
		default:
			j, _ := json.Marshal(body)
			rd = bytes.NewReader(j)
		}
	}
	return e.request(method, path, token, rd, "application/json")
}

// must asserts a status code.
func (e *env) must(r *resp, code int) *resp {
	e.t.Helper()
	if r.Code != code {
		e.t.Fatalf("status = %d, want %d; body %s", r.Code, code, r.Body)
	}
	return r
}

type user struct {
	ID, Token, Email, Name string
}

func (e *env) register(name string) *user {
	e.t.Helper()
	e.userNo++
	email := fmt.Sprintf("%s%d@example.com", strings.ToLower(name), e.userNo)
	r := e.must(e.do("POST", "/api/v1/auth/register", "", map[string]any{
		"method": "email", "identifier": email, "password": "password123", "display_name": name,
		"invite_code": nil, "device": map[string]any{"name": name + "'s iPhone", "platform": "ios"},
	}), 200)
	var ar struct {
		Token string
		User  struct{ ID string }
	}
	r.decode(e.t, &ar)
	return &user{ID: ar.User.ID, Token: ar.Token, Email: email, Name: name}
}

func (e *env) befriend(a, b *user) {
	e.t.Helper()
	r := e.must(e.do("POST", "/api/v1/friends/requests", a.Token, map[string]any{"user_id": b.ID}), 200)
	var fr struct {
		Status  string
		Request *struct{ ID string }
	}
	r.decode(e.t, &fr)
	if fr.Request != nil {
		e.must(e.do("POST", "/api/v1/friends/requests/"+fr.Request.ID+"/accept", b.Token, nil), 200)
	}
}

type momentOpts struct {
	clientID   string
	kind       string
	caption    map[string]any
	audience   string
	recipients []string
	media      []byte
	mediaType  string
	thumb      []byte
	localDate  string
	meta       map[string]any // overrides
}

var clientSeq int

func newClientID() string {
	clientSeq++
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", clientSeq, clientSeq)
}

func multipartBody(meta map[string]any, media []byte, mediaType string, thumb []byte) (*bytes.Buffer, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	add := func(name, ct string, data []byte) {
		if data == nil {
			return
		}
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, name, name))
		h.Set("Content-Type", ct)
		w, _ := mw.CreatePart(h)
		w.Write(data)
	}
	if meta != nil {
		mb, _ := json.Marshal(meta)
		add("meta", "application/json", mb)
	}
	add("media", mediaType, media)
	add("thumb", "image/jpeg", thumb)
	mw.Close()
	return &buf, mw.FormDataContentType()
}

var (
	photoOnce   sync.Once
	photoBytes  []byte
	thumbBytesG []byte
)

func testPhoto() ([]byte, []byte) {
	photoOnce.Do(func() {
		photoBytes = devseed.PhotoJPEG(3, 256)
		thumbBytesG = devseed.PhotoJPEG(3, 128)
	})
	return photoBytes, thumbBytesG
}

func (e *env) postMomentRaw(u *user, o momentOpts) *resp {
	e.t.Helper()
	photo, thumb := testPhoto()
	if o.kind == "" {
		o.kind = "photo"
	}
	if o.clientID == "" {
		o.clientID = newClientID()
	}
	if o.audience == "" {
		o.audience = "all"
	}
	if o.media == nil {
		o.media, o.mediaType = photo, "image/jpeg"
		if o.kind == "video" {
			o.media, o.mediaType = devseed.DemoMP4(), "video/mp4"
		}
	}
	if o.thumb == nil {
		o.thumb = thumb
	}
	if o.localDate == "" {
		o.localDate = e.clock.Now().Format(time.DateOnly)
	}
	meta := map[string]any{"client_id": o.clientID, "kind": o.kind, "caption": o.caption, "audience": o.audience,
		"recipient_ids": o.recipients, "width": 1440, "height": 1440, "duration_ms": nil, "local_date": o.localDate}
	if o.kind == "video" {
		meta["duration_ms"] = 4000
		meta["width"], meta["height"] = 480, 480
	}
	for k, v := range o.meta {
		meta[k] = v
	}
	body, ct := multipartBody(meta, o.media, o.mediaType, o.thumb)
	return e.request("POST", "/api/v1/moments", u.Token, body, ct)
}

type momentJSON struct {
	ID           string
	Kind         string
	Audience     string
	RecipientIDs []string `json:"recipient_ids"`
	Media        struct {
		URL         string `json:"url"`
		ContentType string `json:"content_type"`
	}
	Thumb struct {
		URL string `json:"url"`
	}
	Reactions []struct {
		Emoji string
		User  struct{ ID string }
	}
	Viewers []struct {
		User struct{ ID string }
	}
	Viewed bool
}

func (e *env) postMoment(u *user, o momentOpts) momentJSON {
	e.t.Helper()
	r := e.postMomentRaw(u, o)
	if r.Code != 201 && r.Code != 200 {
		e.t.Fatalf("post moment: %d %s", r.Code, r.Body)
	}
	var m momentJSON
	r.decode(e.t, &m)
	return m
}

func (e *env) sendMessage(from, to *user, text string, reply string) map[string]any {
	e.t.Helper()
	body := map[string]any{"client_id": newClientID(), "text": text, "reply_to_moment_id": nil}
	if reply != "" {
		body["reply_to_moment_id"] = reply
	}
	return e.must(e.do("POST", "/api/v1/conversations/"+to.ID+"/messages", from.Token, body), 201).m(e.t)
}

// sse is a live event-stream reader.
type sse struct {
	mu     sync.Mutex
	events []sseEvent
	raw    []string
	cancel context.CancelFunc
	done   chan struct{}
}

type sseEvent struct {
	Type string
	Data string
}

func (e *env) subscribe(u *user) *sse {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", e.ts.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+u.Token)
	res, err := e.ts.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	if res.StatusCode != 200 {
		e.t.Fatalf("events status %d", res.StatusCode)
	}
	s := &sse{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		var cur sseEvent
		var frame []string
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				if cur.Type != "" {
					s.mu.Lock()
					s.events = append(s.events, cur)
					s.raw = append(s.raw, strings.Join(frame, "\n")+"\n\n")
					s.mu.Unlock()
				}
				cur, frame = sseEvent{}, nil
				continue
			}
			frame = append(frame, line)
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				cur.Type = v
			} else if v, ok := strings.CutPrefix(line, "data: "); ok {
				cur.Data = v
			}
		}
	}()
	e.t.Cleanup(s.close)
	return s
}

func (s *sse) close() { s.cancel(); <-s.done }

// wait blocks until an event of type typ arrives (or fails after 3 s).
func (s *sse) wait(t testing.TB, typ string) sseEvent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		for _, ev := range s.events {
			if ev.Type == typ {
				s.mu.Unlock()
				return ev
			}
		}
		s.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no %s event (got %v)", typ, s.types())
	return sseEvent{}
}

func (s *sse) types() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.events {
		out = append(out, e.Type)
	}
	return out
}

func (s *sse) rawFor(typ string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range s.events {
		if e.Type == typ {
			return s.raw[i]
		}
	}
	return ""
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// waitMatch blocks until an event of type typ whose data contains substr.
func (s *sse) waitMatch(t testing.TB, typ, substr string) sseEvent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		for _, ev := range s.events {
			if ev.Type == typ && strings.Contains(ev.Data, substr) {
				s.mu.Unlock()
				return ev
			}
		}
		s.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no %s event containing %q (got %v)", typ, substr, s.types())
	return sseEvent{}
}

// count returns how many events of type typ arrived.
func (s *sse) count(typ string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func (e *env) requestWithHeader(method, path, token, key, value string) *resp {
	e.t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set(key, value)
	res, err := e.ts.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return &resp{Code: res.StatusCode, Body: b, Header: res.Header}
}

func newRequestCtx(t testing.TB, d time.Duration, url string) (*http.Request, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req, cancel
}

func base64URL(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// setRegistration switches the registration mode at runtime (as if set in
// the configuration) and reloads the live settings.
func (e *env) setRegistration(mode string, userInvites bool) {
	e.t.Helper()
	e.cfg.Registration, e.cfg.UserInvites = mode, userInvites
	if err := e.srv.ReloadSettings(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

// serverInvite creates an admin server invite directly.
func (e *env) serverInvite(maxUses int, expires *time.Time, note string) string {
	e.t.Helper()
	_, code, err := e.srv.CreateServerInvite(context.Background(), nil, maxUses, expires, note)
	if err != nil {
		e.t.Fatal(err)
	}
	return code
}

// registerWith registers a new email account with an invite code and
// returns the raw response.
func (e *env) registerWith(email string, code any) *resp {
	e.t.Helper()
	return e.do("POST", "/api/v1/auth/register", "", map[string]any{"method": "email", "identifier": email,
		"password": "password123", "display_name": "New", "invite_code": code,
		"device": map[string]any{"name": "Phone", "platform": "android"}})
}
