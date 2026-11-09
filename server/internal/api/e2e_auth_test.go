package api_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/config"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/oidc"
)

func TestHealthAndServerInfo(t *testing.T) {
	e := newEnv(t)
	r := e.request("GET", "/healthz", "", nil, "")
	if r.Code != 200 || strings.TrimSpace(string(r.Body)) != "ok" {
		t.Fatalf("healthz: %d %q", r.Code, r.Body)
	}
	info := e.must(e.do("GET", "/api/v1/server", "", nil), 200).m(t)
	if info["name"] != "Test Frame" || info["api_version"].(float64) != 1 || info["registration"] != "open" || info["push"] != nil {
		t.Fatalf("server info: %v", info)
	}
	limits := info["limits"].(map[string]any)
	if limits["max_upload_bytes"].(float64) != 50<<20 || limits["max_video_ms"].(float64) != 15000 || limits["max_friends"] != nil {
		t.Fatalf("limits: %v", limits)
	}
	if info["password_reset"] != "admin" {
		t.Fatalf("password_reset: %v", info["password_reset"])
	}
	// Security headers and no CORS.
	for k, want := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer", "Cache-Control": "no-store"} {
		if got := r2h(e.do("GET", "/api/v1/server", "", nil), k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	pre := e.request("OPTIONS", "/api/v1/me", "", nil, "")
	if pre.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("CORS header present")
	}
}

func r2h(r *resp, k string) string { return r.Header.Get(k) }

func TestJSONErrorsForUnknownRoutesAndBadBodies(t *testing.T) {
	e := newEnv(t)
	u := e.register("Al")
	if r := e.do("GET", "/api/v1/nope", "", nil); r.Code != 404 || r.errCode(t) != "not_found" {
		t.Fatalf("unknown route: %d %s", r.Code, r.Body)
	}
	if r := e.do("PUT", "/api/v1/server", "", nil); r.Code != 405 || r.errCode(t) != "method_not_allowed" || r.Header.Get("Allow") == "" {
		t.Fatalf("405: %d %s %v", r.Code, r.Body, r.Header)
	}
	if r := e.do("GET", "/media/../etc/passwd", "", nil); r.Code != 404 {
		t.Fatalf("traversal: %d", r.Code)
	}
	if r := e.do("PATCH", "/api/v1/me", u.Token, `{"display_name":"x"} {"a":1}`); r.Code != 400 {
		t.Fatalf("trailing data: %d", r.Code)
	}
	if r := e.do("PATCH", "/api/v1/me", u.Token, `{"display_name":`); r.Code != 400 || r.errCode(t) != "bad_request" {
		t.Fatalf("malformed: %d %s", r.Code, r.Body)
	}
	big := `{"display_name":"` + strings.Repeat("a", 70<<10) + `"}`
	if r := e.do("PATCH", "/api/v1/me", u.Token, big); r.Code != 413 || r.errCode(t) != "payload_too_large" {
		t.Fatalf("too large: %d %s", r.Code, r.Body)
	}
	if r := e.do("GET", "/api/v1/me", "", nil); r.Code != 401 || r.errCode(t) != "unauthorized" {
		t.Fatalf("no token: %d", r.Code)
	}
	if r := e.do("GET", "/api/v1/me", "garbage", nil); r.Code != 401 {
		t.Fatalf("bad token: %d", r.Code)
	}
	if r := e.do("GET", "/api/v1/me", strings.Repeat("A", 43), nil); r.Code != 401 {
		t.Fatalf("unknown token: %d", r.Code)
	}
}

func TestRegisterLoginSessionsPassword(t *testing.T) {
	e := newEnv(t)
	dev := map[string]any{"name": "Pixel", "platform": "android"}
	reg := func(body map[string]any) *resp { return e.do("POST", "/api/v1/auth/register", "", body) }
	ok := e.must(reg(map[string]any{"method": "email", "identifier": "  Alice@Example.COM ", "password": "password123", "display_name": "Alice", "device": dev}), 200).m(t)
	if ok["is_new"] != true || ok["user"].(map[string]any)["email"] != "alice@example.com" || len(ok["token"].(string)) != 43 {
		t.Fatalf("register: %v", ok)
	}
	me := ok["user"].(map[string]any)
	for _, k := range []string{"id", "username", "display_name", "avatar", "email", "phone", "auth_methods", "discoverable", "searchable", "read_receipts", "streak", "created_at"} {
		if _, present := me[k]; !present {
			t.Errorf("Me missing %q", k)
		}
	}
	cases := []struct {
		body map[string]any
		code int
		err  string
	}{
		{map[string]any{"method": "email", "identifier": "alice@example.com", "password": "password123"}, 409, "identifier_taken"},
		{map[string]any{"method": "email", "identifier": "not-an-email", "password": "password123"}, 400, "bad_request"},
		{map[string]any{"method": "email", "identifier": "x@example.com", "password": "short"}, 400, "bad_request"},
		{map[string]any{"method": "email", "identifier": "x@example.com", "password": strings.Repeat("p", 129)}, 400, "bad_request"},
		{map[string]any{"method": "phone", "identifier": "0600111222", "password": "password123"}, 400, "bad_request"},
		{map[string]any{"method": "fax", "identifier": "1", "password": "password123"}, 400, "bad_request"},
		{map[string]any{"method": "email", "identifier": "y@example.com", "password": "password123", "device": map[string]any{"platform": "windows"}}, 400, "bad_request"},
		{map[string]any{"method": "email", "identifier": "z@example.com", "password": "password123", "display_name": strings.Repeat("n", 41)}, 400, "bad_request"},
	}
	for i, c := range cases {
		r := reg(c.body)
		if r.Code != c.code || r.errCode(t) != c.err {
			t.Errorf("case %d: %d %s", i, r.Code, r.Body)
		}
	}
	phone := e.must(reg(map[string]any{"method": "phone", "identifier": "+34 600 111 222", "password": "password123", "display_name": "Pepe"}), 200).m(t)
	if phone["user"].(map[string]any)["phone"] != "+34600111222" {
		t.Fatalf("phone normalisation: %v", phone["user"])
	}

	login := func(method, id, pw string) *resp {
		return e.do("POST", "/api/v1/auth/login", "", map[string]any{"method": method, "identifier": id, "password": pw, "device": dev})
	}
	l := e.must(login("email", "ALICE@example.com", "password123"), 200).m(t)
	if l["is_new"] != false {
		t.Fatal("login is_new")
	}
	tok2 := l["token"].(string)
	for _, c := range [][3]string{{"email", "alice@example.com", "wrongpass1"}, {"email", "nobody@example.com", "password123"}, {"phone", "+34600111223", "password123"}, {"email", "bad", "x"}} {
		if r := login(c[0], c[1], c[2]); r.Code != 401 || r.errCode(t) != "invalid_credentials" {
			t.Errorf("login %v: %d %s", c, r.Code, r.Body)
		}
	}
	e.must(login("phone", "+34600111222", "password123"), 200)

	// Sessions.
	tok1 := ok["token"].(string)
	var sl struct {
		Sessions []struct {
			ID         string
			DeviceName string `json:"device_name"`
			Platform   string
			Current    bool
		}
	}
	e.must(e.do("GET", "/api/v1/auth/sessions", tok1, nil), 200).decode(t, &sl)
	if len(sl.Sessions) != 2 {
		t.Fatalf("sessions: %+v", sl)
	}
	var other string
	for _, s := range sl.Sessions {
		if !s.Current {
			other = s.ID
		}
		if s.Platform != "android" {
			t.Errorf("platform %q", s.Platform)
		}
	}
	e.must(e.do("DELETE", "/api/v1/auth/sessions/"+other, tok1, nil), 204)
	if r := e.do("GET", "/api/v1/me", tok2, nil); r.Code != 401 {
		t.Fatal("revoked session still works")
	}
	if r := e.do("DELETE", "/api/v1/auth/sessions/"+other, tok1, nil); r.Code != 404 {
		t.Fatalf("double revoke: %d", r.Code)
	}

	// Password change.
	if r := e.do("POST", "/api/v1/auth/password", tok1, map[string]any{"current_password": "nope-nope", "new_password": "newpassword1"}); r.Code != 401 || r.errCode(t) != "invalid_credentials" {
		t.Fatalf("wrong current: %d %s", r.Code, r.Body)
	}
	if r := e.do("POST", "/api/v1/auth/password", tok1, map[string]any{"current_password": nil, "new_password": "newpassword1"}); r.Code != 400 {
		t.Fatalf("null current with password: %d", r.Code)
	}
	e.must(e.do("POST", "/api/v1/auth/password", tok1, map[string]any{"current_password": "password123", "new_password": "newpassword1"}), 204)
	e.must(login("email", "alice@example.com", "newpassword1"), 200)
	if r := login("email", "alice@example.com", "password123"); r.Code != 401 {
		t.Fatal("old password still works")
	}

	// Logout revokes the session.
	e.must(e.do("POST", "/api/v1/auth/logout", tok1, nil), 204)
	if r := e.do("GET", "/api/v1/me", tok1, nil); r.Code != 401 {
		t.Fatal("logged-out token still works")
	}
	// Reset endpoints are 404 without SMTP.
	if r := e.do("POST", "/api/v1/auth/password-reset/request", "", map[string]any{"email": "alice@example.com"}); r.Code != 404 {
		t.Fatalf("reset without smtp: %d", r.Code)
	}
}

func TestRegistrationModes(t *testing.T) {
	e := newEnv(t, withConfig(func(c *config.Config) { c.Registration = "closed" }))
	r := e.do("POST", "/api/v1/auth/register", "", map[string]any{"method": "email", "identifier": "a@example.com", "password": "password123"})
	if r.Code != 403 || r.errCode(t) != "registration_closed" {
		t.Fatalf("closed: %d %s", r.Code, r.Body)
	}

	e = newEnv(t)
	inviter := e.register("Inviter")
	inv := e.must(e.do("POST", "/api/v1/invites", inviter.Token, nil), 200).m(t)
	e.setRegistration("invite", true)
	body := map[string]any{"method": "email", "identifier": "b@example.com", "password": "password123", "display_name": "B"}
	if r := e.do("POST", "/api/v1/auth/register", "", body); r.Code != 403 || r.errCode(t) != "invite_required" {
		t.Fatalf("invite mode without code: %d %s", r.Code, r.Body)
	}
	body["invite_code"] = "AAAAAAAAAAAAAAAAAAAAAA"
	if r := e.do("POST", "/api/v1/auth/register", "", body); r.Code != 403 || r.errCode(t) != "invite_invalid" {
		t.Fatalf("invite mode bad code: %d", r.Code)
	}
	body["invite_code"] = inv["code"]
	s := e.subscribe(inviter)
	nu := e.must(e.do("POST", "/api/v1/auth/register", "", body), 200).m(t)
	var fl struct {
		Friends []struct{ User struct{ ID string } }
	}
	e.must(e.do("GET", "/api/v1/friends", nu["token"].(string), nil), 200).decode(t, &fl)
	if len(fl.Friends) != 1 || fl.Friends[0].User.ID != inviter.ID {
		t.Fatalf("not befriended with inviter: %+v", fl)
	}
	s.wait(t, "friend.accepted")
}

func TestAuthRateLimits(t *testing.T) {
	e := newEnv(t, withStrictLimits())
	body := map[string]any{"method": "email", "identifier": "victim@example.com", "password": "wrongwrong"}
	codes := map[int]int{}
	var last *resp
	for i := 0; i < 7; i++ {
		last = e.do("POST", "/api/v1/auth/login", "", body)
		codes[last.Code]++
	}
	// 5/min per identifier: the 6th attempt on the same identifier is throttled.
	if codes[401] != 5 || codes[429] != 2 || last.errCode(t) != "rate_limited" || last.Header.Get("Retry-After") == "" {
		t.Fatalf("identifier limit: %v %s", codes, last.Header)
	}
	// 10/min per IP across identifiers.
	for i := 0; i < 5; i++ {
		e.do("POST", "/api/v1/auth/login", "", map[string]any{"method": "email", "identifier": fmt.Sprintf("u%d@example.com", i), "password": "wrongwrong"})
	}
	if r := e.do("POST", "/api/v1/auth/login", "", map[string]any{"method": "email", "identifier": "new@example.com", "password": "x"}); r.Code != 429 {
		t.Fatalf("ip limit: %d", r.Code)
	}
	e.clock.Add(61 * time.Second)
	if r := e.do("POST", "/api/v1/auth/login", "", body); r.Code != 401 {
		t.Fatalf("after refill: %d", r.Code)
	}
}

// ---- OIDC (Apple / Google) with a fake JWKS ----

type idp struct {
	key *rsa.PrivateKey
	ts  *httptest.Server
	kid string
	mu  sync.Mutex
}

func newIDP(t *testing.T) *idp {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &idp{key: k, kid: "test-kid"}
	p.ts = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(k.N.Bytes())
		ex := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes())
		w.Header().Set("Cache-Control", "max-age=3600")
		fmt.Fprintf(w, `{"keys":[{"kty":"RSA","use":"sig","alg":"RS256","kid":%q,"n":%q,"e":%q}]}`, p.kid, n, ex)
	}))
	t.Cleanup(p.ts.Close)
	return p
}

func (p *idp) token(t *testing.T, claims map[string]any) string {
	h, _ := json.Marshal(map[string]any{"alg": "RS256", "kid": p.kid, "typ": "JWT"})
	c, _ := json.Marshal(claims)
	in := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func sha256hex(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// serverNonce builds a server-bound raw nonce: frame1:<host>:<base64url(32)>.
func serverNonce(host string) string {
	b := make([]byte, 32)
	rand.Read(b)
	return "frame1:" + host + ":" + base64.RawURLEncoding.EncodeToString(b)
}

func newVerifiers(t *testing.T) (*idp, *idp, *oidc.Verifier, *oidc.Verifier, func() time.Time) {
	ap, gp := newIDP(t), newIDP(t)
	clock := func() time.Time { return time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC) }
	apple := oidc.NewVerifier(oidc.Provider{Name: "apple", Issuers: []string{"https://appleid.apple.com"}, JWKSURL: ap.ts.URL},
		[]string{"com.crowtech.frame"}, oidc.Options{HTTPClient: ap.ts.Client(), Now: clock})
	google := oidc.NewVerifier(oidc.Provider{Name: "google", Issuers: []string{"accounts.google.com", "https://accounts.google.com"}, JWKSURL: gp.ts.URL},
		[]string{"web-client.apps.googleusercontent.com"}, oidc.Options{HTTPClient: gp.ts.Client(), Now: clock})
	return ap, gp, apple, google, clock
}

func appleClaims(now int64, sub, raw string) map[string]any {
	return map[string]any{"iss": "https://appleid.apple.com", "aud": "com.crowtech.frame", "sub": sub,
		"iat": now, "exp": now + 600, "nonce": sha256hex(raw)}
}

func googleClaims(now int64, sub, raw string) map[string]any {
	c := map[string]any{"iss": "accounts.google.com", "aud": "web-client.apps.googleusercontent.com", "sub": sub, "iat": now, "exp": now + 600}
	if raw != "" {
		c["nonce"] = sha256hex(raw)
	}
	return c
}

func TestAppleAndGoogleSignIn(t *testing.T) {
	ap, gp, apple, google, clock := newVerifiers(t)
	e := newEnv(t, withVerifiers(apple, google))
	now := clock().Unix()
	host := "frame.example.com" // FRAME_PUBLIC_URL of the test env

	info := e.must(e.do("GET", "/api/v1/server", "", nil), 200).m(t)
	if am := info["auth_methods"].(map[string]any); am["apple"] != true || am["google"] != true {
		t.Fatalf("auth methods: %v", am)
	}

	existing := e.register("Mallory") // owns mallory1@example.com
	raw := serverNonce(host)
	claims := appleClaims(now, "apple-sub-1", raw)
	claims["email"], claims["email_verified"] = existing.Email, "true"
	tok := ap.token(t, claims)
	r := e.must(e.do("POST", "/api/v1/auth/apple", "", map[string]any{"id_token": tok, "raw_nonce": raw, "display_name": "Apple User",
		"device": map[string]any{"name": "iPhone", "platform": "ios"}}), 200).m(t)
	u := r["user"].(map[string]any)
	if r["is_new"] != true || u["id"] == existing.ID || u["email"] != nil || u["display_name"] != "Apple User" {
		t.Fatalf("apple signup must create a separate account without auto-linking email: %v", r)
	}
	if am := u["auth_methods"].([]any); len(am) != 1 || am[0] != "apple" {
		t.Fatalf("auth_methods %v", am)
	}
	raw2 := serverNonce(host)
	r2 := e.must(e.do("POST", "/api/v1/auth/apple", "", map[string]any{"id_token": ap.token(t, appleClaims(now, "apple-sub-1", raw2)), "raw_nonce": raw2}), 200).m(t)
	if r2["is_new"] != false || r2["user"].(map[string]any)["id"] != u["id"] {
		t.Fatalf("second apple login: %v", r2)
	}
	n3 := serverNonce(host)
	for name, body := range map[string]map[string]any{
		"wrong nonce":   {"id_token": ap.token(t, appleClaims(now, "s", n3)), "raw_nonce": serverNonce(host)},
		"missing nonce": {"id_token": ap.token(t, appleClaims(now, "s", n3)), "raw_nonce": nil},
		"garbage":       {"id_token": "a.b.c", "raw_nonce": serverNonce(host)},
		"google key":    {"id_token": gp.token(t, appleClaims(now, "x", n3)), "raw_nonce": n3},
		"expired": {"id_token": ap.token(t, map[string]any{"iss": "https://appleid.apple.com", "aud": "com.crowtech.frame", "sub": "s",
			"iat": now - 4000, "exp": now - 3000, "nonce": sha256hex(n3)}), "raw_nonce": n3},
	} {
		if r := e.do("POST", "/api/v1/auth/apple", "", body); r.Code != 401 || r.errCode(t) != "invalid_credentials" {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
		}
	}

	// Google: explicit linking from a signed-in session.
	gn := serverNonce(host)
	gtokFor := func(raw string) string { return gp.token(t, googleClaims(now, "g-1", raw)) }
	e.must(e.request("POST", "/api/v1/auth/google", existing.Token, strings.NewReader(fmt.Sprintf(`{"id_token":%q,"raw_nonce":%q}`, gtokFor(gn), gn)), "application/json"), 200)
	me := e.must(e.do("GET", "/api/v1/me", existing.Token, nil), 200).m(t)
	if am := me["auth_methods"].([]any); len(am) != 2 || am[1] != "google" {
		t.Fatalf("google not linked: %v", am)
	}
	gn2 := serverNonce(host)
	gl := e.must(e.do("POST", "/api/v1/auth/google", "", map[string]any{"id_token": gtokFor(gn2), "raw_nonce": gn2}), 200).m(t)
	if gl["user"].(map[string]any)["id"] != existing.ID || gl["is_new"] != false {
		t.Fatalf("google login after link: %v", gl)
	}
	// The Apple user cannot link the same Google account.
	gn3 := serverNonce(host)
	if r := e.request("POST", "/api/v1/auth/google", r2["token"].(string), strings.NewReader(fmt.Sprintf(`{"id_token":%q,"raw_nonce":%q}`, gtokFor(gn3), gn3)), "application/json"); r.Code != 409 {
		t.Fatalf("link taken: %d", r.Code)
	}
	// Apple user can add password login with an identifier.
	at := r2["token"].(string)
	if r := e.do("POST", "/api/v1/auth/password", at, map[string]any{"current_password": nil, "new_password": "applepass1"}); r.Code != 400 {
		t.Fatalf("missing identifier: %d", r.Code)
	}
	if r := e.do("POST", "/api/v1/auth/password", at, map[string]any{"current_password": nil, "new_password": "applepass1", "identifier": map[string]any{"method": "email", "identifier": existing.Email}}); r.Code != 409 {
		t.Fatalf("taken identifier: %d", r.Code)
	}
	e.must(e.do("POST", "/api/v1/auth/password", at, map[string]any{"current_password": nil, "new_password": "applepass1", "identifier": map[string]any{"method": "email", "identifier": "apple-user@example.com"}}), 204)
	e.must(e.do("POST", "/api/v1/auth/login", "", map[string]any{"method": "email", "identifier": "apple-user@example.com", "password": "applepass1"}), 200)
	_ = context.Background()
}

type memMailer struct {
	mu   sync.Mutex
	sent []string
}

func (m *memMailer) Send(_ context.Context, to, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, body)
	return nil
}

func (m *memMailer) last() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		return ""
	}
	return m.sent[len(m.sent)-1]
}

func TestPasswordResetByEmail(t *testing.T) {
	mm := &memMailer{}
	e := newEnv(t, withMailer(mm))
	u := e.register("Reset")
	if info := e.must(e.do("GET", "/api/v1/server", "", nil), 200).m(t); info["password_reset"] != "email" {
		t.Fatal("password_reset should be email")
	}
	e.must(e.do("POST", "/api/v1/auth/password-reset/request", "", map[string]any{"email": "unknown@example.com"}), 204)
	e.must(e.do("POST", "/api/v1/auth/password-reset/request", "", map[string]any{"email": u.Email}), 204)
	var code string
	for i := 0; i < 200 && code == ""; i++ {
		if body := mm.last(); body != "" {
			i := strings.Index(body, "code is: ")
			code = body[i+9 : i+17]
		}
		time.Sleep(5 * time.Millisecond)
	}
	if code == "" {
		t.Fatal("no reset email")
	}
	if r := e.do("POST", "/api/v1/auth/password-reset/confirm", "", map[string]any{"email": u.Email, "code": "00000000", "new_password": "brandnew1"}); r.Code != 401 {
		t.Fatalf("wrong code: %d", r.Code)
	}
	e.must(e.do("POST", "/api/v1/auth/password-reset/confirm", "", map[string]any{"email": u.Email, "code": code, "new_password": "brandnew1"}), 204)
	if r := e.do("GET", "/api/v1/me", u.Token, nil); r.Code != 401 {
		t.Fatal("sessions must be revoked after reset")
	}
	if r := e.do("POST", "/api/v1/auth/password-reset/confirm", "", map[string]any{"email": u.Email, "code": code, "new_password": "again1234"}); r.Code != 401 {
		t.Fatal("code reusable")
	}
	e.must(e.do("POST", "/api/v1/auth/login", "", map[string]any{"method": "email", "identifier": u.Email, "password": "brandnew1"}), 200)
}
