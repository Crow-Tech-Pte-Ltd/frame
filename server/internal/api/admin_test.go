package api_test

import (
	"bytes"
	"context"
	"encoding/base32"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/api"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/config"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/devseed"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/totp"
)

// ---- helpers ------------------------------------------------------------

type adminClient struct {
	e      *env
	cookie string
}

// adminReq sends an admin API request. By default it carries the CSRF
// header and a same-origin Origin; hdr overrides (empty value = remove).
func (e *env) adminReq(method, path string, body any, cookie string, hdr map[string]string) *resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.ts.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != "GET" && method != "HEAD" {
		req.Header.Set("X-Frame-Admin", "1")
		req.Header.Set("Origin", e.ts.URL)
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	client := *e.ts.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return &resp{Code: res.StatusCode, Body: b, Header: res.Header}
}

func (c *adminClient) do(method, path string, body any) *resp {
	c.e.t.Helper()
	return c.e.adminReq(method, "/admin/api/v1/"+path, body, c.cookie, nil)
}

func adminCookieFrom(t testing.TB, r *resp) (*http.Cookie, string) {
	t.Helper()
	for _, c := range (&http.Response{Header: r.Header}).Cookies() {
		if c.Name == "frame_admin" && c.Value != "" {
			return c, c.Name + "=" + c.Value
		}
	}
	t.Fatalf("no admin cookie in %v", r.Header)
	return nil, ""
}

func (e *env) makeAdmin(u *user, username string) {
	e.t.Helper()
	if _, err := e.db.W.Exec(`UPDATE users SET is_admin = 1, username = ? WHERE id = ?`, username, u.ID); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) adminLogin(ident, password string, code any) (*adminClient, *resp) {
	e.t.Helper()
	r := e.adminReq("POST", "/admin/api/v1/login", map[string]any{"identifier": ident, "password": password, "totp_code": code}, "", nil)
	if r.Code != 200 {
		return nil, r
	}
	_, ck := adminCookieFrom(e.t, r)
	return &adminClient{e: e, cookie: ck}, r
}

// newAdminEnv returns an env with an admin "root" (password password123)
// signed in to the panel.
func newAdminEnv(t *testing.T, opts ...envOpt) (*env, *user, *adminClient) {
	e := newEnv(t, opts...)
	root := e.register("Root")
	e.makeAdmin(root, "root")
	c, r := e.adminLogin("root", "password123", nil)
	if c == nil {
		t.Fatalf("admin login: %d %s", r.Code, r.Body)
	}
	return e, root, c
}

// ---- first-run setup ------------------------------------------------------

func TestAdminSetupFlow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	info := e.must(e.adminReq("GET", "/admin/api/v1/setup", nil, "", nil), 200).m(t)
	if info["needed"] != true || info["server_name"] != "Test Frame" {
		t.Fatalf("setup info: %v", info)
	}
	old, _, err := e.srv.NewSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tok, exp, err := e.srv.NewSetupToken(ctx) // a new link replaces the old one
	if err != nil || exp.Sub(e.clock.Now()) != api.SetupTokenTTL {
		t.Fatalf("token: %v %v", err, exp)
	}
	for _, bad := range []string{old, "", "short", strings.Repeat("A", 43)} {
		if r := e.adminReq("POST", "/admin/api/v1/setup/verify", map[string]any{"token": bad}, "", nil); r.Code != 403 || r.errCode(t) != "setup_token_invalid" {
			t.Fatalf("verify %q: %d %s", bad, r.Code, r.Body)
		}
	}
	e.must(e.adminReq("POST", "/admin/api/v1/setup/verify", map[string]any{"token": tok}, "", nil), 204)
	// The token is never stored in plaintext.
	var stored string
	e.db.R.QueryRow(`SELECT value FROM meta WHERE key = 'admin_setup'`).Scan(&stored)
	if stored == "" || strings.Contains(stored, tok) {
		t.Fatal("setup token stored in plaintext")
	}

	form := map[string]any{"token": tok, "display_name": "Daniel", "username": "daniel", "method": "email",
		"identifier": "Daniel@Example.com", "password": "correct horse battery"}
	bad := map[string]any{}
	for k, v := range form {
		bad[k] = v
	}
	bad["username"] = ""
	if r := e.adminReq("POST", "/admin/api/v1/setup", bad, "", nil); r.Code != 400 {
		t.Fatalf("setup without username: %d", r.Code)
	}
	bad["username"], bad["token"] = "daniel", old
	if r := e.adminReq("POST", "/admin/api/v1/setup", bad, "", nil); r.Code != 403 || r.errCode(t) != "setup_token_invalid" {
		t.Fatalf("setup with old token: %d %s", r.Code, r.Body)
	}
	r := e.must(e.adminReq("POST", "/admin/api/v1/setup", form, "", nil), 200)
	sess := r.m(t)
	if a := sess["admin"].(map[string]any); a["username"] != "daniel" || a["email"] != "daniel@example.com" || a["totp_enabled"] != false {
		t.Fatalf("session: %v", sess)
	}
	ck, cookie := adminCookieFrom(t, r)
	if !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/admin" || ck.Secure || ck.MaxAge != 12*3600 {
		t.Fatalf("cookie flags: %+v", ck)
	}
	(&adminClient{e: e, cookie: cookie}).do("GET", "overview", nil)
	// Single use.
	if r := e.adminReq("POST", "/admin/api/v1/setup", form, "", nil); r.Code != 409 || r.errCode(t) != "setup_done" {
		t.Fatalf("setup twice: %d %s", r.Code, r.Body)
	}
	if _, _, err := e.srv.NewSetupToken(ctx); err != api.ErrSetupDone {
		t.Fatalf("new token after setup: %v", err)
	}
	if info := e.must(e.adminReq("GET", "/admin/api/v1/setup", nil, "", nil), 200).m(t); info["needed"] != false {
		t.Fatal("setup still needed")
	}
	// The admin is a normal app user too.
	app := e.must(e.do("POST", "/api/v1/auth/login", "", map[string]any{"method": "email", "identifier": "daniel@example.com",
		"password": "correct horse battery"}), 200).m(t)
	if app["user"].(map[string]any)["is_admin"] != true {
		t.Fatalf("app login: %v", app)
	}
	audit := (&adminClient{e: e, cookie: cookie}).do("GET", "audit", nil)
	if !strings.Contains(string(audit.Body), "setup.complete") {
		t.Fatalf("audit: %s", audit.Body)
	}
}

func TestAdminSetupTokenExpiry(t *testing.T) {
	e := newEnv(t)
	tok, _, err := e.srv.NewSetupToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Add(api.SetupTokenTTL + time.Second)
	if r := e.adminReq("POST", "/admin/api/v1/setup/verify", map[string]any{"token": tok}, "", nil); r.Code != 403 {
		t.Fatalf("expired token accepted: %d", r.Code)
	}
	r := e.adminReq("POST", "/admin/api/v1/setup", map[string]any{"token": tok, "display_name": "X", "username": "xx",
		"method": "email", "identifier": "x@example.com", "password": "password123"}, "", nil)
	if r.Code != 403 || r.errCode(t) != "setup_token_invalid" {
		t.Fatalf("expired setup: %d %s", r.Code, r.Body)
	}
}

// ---- sessions, cookies, CSRF ----------------------------------------------

func TestAdminLoginAndCookie(t *testing.T) {
	e := newEnv(t)
	root := e.register("Root")
	e.makeAdmin(root, "root")
	plain := e.register("Plain")
	// Non-admins (even with the right password) look like bad credentials.
	if _, r := e.adminLogin(plain.Email, "password123", nil); r.Code != 401 || r.errCode(t) != "invalid_credentials" {
		t.Fatalf("non-admin login: %d %s", r.Code, r.Body)
	}
	if _, r := e.adminLogin("root", "wrong-password", nil); r.Code != 401 || r.errCode(t) != "invalid_credentials" {
		t.Fatalf("wrong password: %d", r.Code)
	}
	for _, ident := range []string{"root", "@ROOT", root.Email, strings.ToUpper(root.Email)} {
		if c, r := e.adminLogin(ident, "password123", nil); c == nil {
			t.Fatalf("login as %q: %d %s", ident, r.Code, r.Body)
		}
	}
	c, _ := e.adminLogin("root", "password123", nil)
	s := c.do("GET", "session", nil)
	if s.Code != 200 || s.Header.Get("Cache-Control") != "no-store" || s.Header.Get("Content-Security-Policy") != api.AdminCSP {
		t.Fatalf("session headers: %d %v", s.Code, s.Header)
	}
	// The cookie value is only stored hashed.
	var n int
	e.db.R.QueryRow(`SELECT COUNT(*) FROM admin_sessions WHERE instr(CAST(token_hash AS TEXT), ?) > 0`, strings.TrimPrefix(c.cookie, "frame_admin=")).Scan(&n)
	if n != 0 {
		t.Fatal("cookie stored in plaintext")
	}
	// Logout clears it server-side.
	r := c.do("POST", "logout", nil)
	if r.Code != 204 {
		t.Fatalf("logout %d", r.Code)
	}
	if ck, _ := adminCookieFromAny(r); ck == nil || ck.MaxAge >= 0 {
		t.Fatalf("logout must clear the cookie: %v", r.Header)
	}
	if r := c.do("GET", "session", nil); r.Code != 401 {
		t.Fatalf("session after logout: %d", r.Code)
	}
}

func adminCookieFromAny(r *resp) (*http.Cookie, bool) {
	for _, c := range (&http.Response{Header: r.Header}).Cookies() {
		if c.Name == "frame_admin" {
			return c, true
		}
	}
	return nil, false
}

func TestAdminCookieSecureBehindTLSProxy(t *testing.T) {
	e := newEnv(t, withConfig(func(c *config.Config) {
		c.TrustedProxies = append(c.TrustedProxies, mustPrefix(t, "127.0.0.1/32"))
	}))
	root := e.register("Root")
	e.makeAdmin(root, "root")
	body := map[string]any{"identifier": "root", "password": "password123"}
	r := e.adminReq("POST", "/admin/api/v1/login", body, "", map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-For": "198.51.100.7",
		"Origin": "https://frame.example.com"})
	if r.Code != 200 {
		t.Fatalf("login via proxy: %d %s", r.Code, r.Body)
	}
	if ck, _ := adminCookieFrom(t, r); !ck.Secure {
		t.Fatal("cookie must be Secure behind an https proxy")
	}
	// Directly over plain HTTP the Origin of the public URL doesn't match
	// the request's own origin, but configured public origins are accepted.
	r = e.adminReq("POST", "/admin/api/v1/login", body, "", map[string]string{"Origin": "https://frame.example.com"})
	if r.Code != 200 {
		t.Fatalf("public origin: %d %s", r.Code, r.Body)
	}
	if ck, _ := adminCookieFrom(t, r); ck.Secure {
		t.Fatal("plain HTTP must not set Secure (localhost/SSH tunnel use)")
	}
}

func TestAdminCSRF(t *testing.T) {
	e, _, c := newAdminEnv(t)
	login := map[string]any{"identifier": "root", "password": "password123"}
	for name, hdr := range map[string]map[string]string{
		"no header":             {"X-Frame-Admin": ""},
		"wrong header":          {"X-Frame-Admin": "true"},
		"foreign origin":        {"Origin": "https://evil.example"},
		"null origin":           {"Origin": "null"},
		"cross-site fetch":      {"Origin": "", "Sec-Fetch-Site": "cross-site"},
		"same-site fetch":       {"Sec-Fetch-Site": "same-site"},
		"no origin or metadata": {"Origin": ""},
		"origin with path":      {"Origin": e.ts.URL + "/admin"},
	} {
		for _, p := range []struct {
			method, path string
			body         any
		}{{"POST", "login", login}, {"PATCH", "settings", map[string]any{"name": "Pwned"}}, {"POST", "logout", nil}} {
			r := e.adminReq(p.method, "/admin/api/v1/"+p.path, p.body, c.cookie, hdr)
			if r.Code != 403 || r.errCode(t) != "csrf_failed" {
				t.Errorf("%s %s %s: %d %s", name, p.method, p.path, r.Code, r.Body)
			}
		}
	}
	// Sec-Fetch-Site: same-origin without Origin passes.
	r := e.adminReq("PATCH", "/admin/api/v1/settings", map[string]any{"name": "Renamed"}, c.cookie, map[string]string{"Origin": "", "Sec-Fetch-Site": "same-origin"})
	if r.Code != 200 {
		t.Fatalf("same-origin fetch: %d %s", r.Code, r.Body)
	}
	if info := e.must(e.do("GET", "/api/v1/server", "", nil), 200).m(t); info["name"] != "Renamed" {
		t.Fatalf("name not applied: %v", info["name"])
	}
	// GETs need no header (they never change state).
	if r := e.adminReq("GET", "/admin/api/v1/overview", nil, c.cookie, nil); r.Code != 200 {
		t.Fatalf("GET: %d", r.Code)
	}
}

func TestAdminSessionTimeouts(t *testing.T) {
	e, _, c := newAdminEnv(t)
	e.clock.Add(29 * time.Minute)
	e.must(c.do("GET", "session", nil), 200)
	e.clock.Add(31 * time.Minute) // idle > 30 min
	if r := c.do("GET", "session", nil); r.Code != 401 {
		t.Fatalf("idle session still valid: %d", r.Code)
	}
	c, _ = e.adminLogin("root", "password123", nil)
	for range 24 { // active every 25 min for 10 h
		e.clock.Add(25 * time.Minute)
		e.must(c.do("GET", "overview", nil), 200)
	}
	e.clock.Add(2*time.Hour + time.Minute) // absolute 12 h
	e.must(c.do("GET", "session", nil), 401)
}

func TestAdminLoginRateLimits(t *testing.T) {
	e := newEnv(t, withStrictLimits())
	root := e.register("Root")
	e.makeAdmin(root, "root")
	codes := map[int]int{}
	for range 7 {
		_, r := e.adminLogin("root", "wrong-password", nil)
		codes[r.Code]++
	}
	if codes[401] != 5 || codes[429] != 2 {
		t.Fatalf("codes %v", codes)
	}
	// Failed admin logins are audited.
	var n int
	e.db.R.QueryRow(`SELECT COUNT(*) FROM admin_audit WHERE action = 'login.failed'`).Scan(&n)
	if n != 5 {
		t.Fatalf("login.failed entries: %d", n)
	}
}

// Every private admin route rejects missing sessions, app bearer tokens and
// accounts that are no longer admins.
func TestAdminAuthzEveryRoute(t *testing.T) {
	e, _, c := newAdminEnv(t)
	plain := e.register("Plain")
	routes := api.AdminRoutesForTest(e.srv)
	if len(routes) < 30 {
		t.Fatalf("only %d admin routes registered", len(routes))
	}
	paths := 0
	// Demote root behind the panel's back: the existing session must now
	// get 403 everywhere.
	e.db.W.Exec(`UPDATE users SET is_admin = 0 WHERE username = 'root'`)
	for _, rt := range routes {
		method, path, _ := strings.Cut(rt.Pattern, " ")
		path = strings.NewReplacer("{id}", plain.ID, "{sid}", "01JC3V9Z4X8Q2M7T5R1N6K0B3H", "{token}", strings.Repeat("A", 43)).Replace(path)
		if !rt.Private {
			continue
		}
		paths++
		body := map[string]any{"password": "password123", "confirm": "plain", "name": "x", "code": "123456"}
		if r := e.adminReq(method, path, body, "", nil); r.Code != 401 {
			t.Errorf("%s without session: %d", rt.Pattern, r.Code)
		}
		if r := e.adminReq(method, path, body, "", map[string]string{"Authorization": "Bearer " + plain.Token}); r.Code != 401 {
			t.Errorf("%s with app token: %d", rt.Pattern, r.Code)
		}
		if r := e.adminReq(method, path, body, c.cookie, nil); r.Code != 403 || r.errCode(t) != "forbidden" {
			t.Errorf("%s as demoted admin: %d %s", rt.Pattern, r.Code, r.Body)
		}
	}
	if paths < 30 {
		t.Fatalf("checked only %d private routes", paths)
	}
	var stillThere int
	e.db.R.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, plain.ID).Scan(&stillThere)
	if stillThere != 1 {
		t.Fatal("a forbidden request changed state")
	}
}

// ---- TOTP -----------------------------------------------------------------

func TestAdminTOTP(t *testing.T) {
	e, root, c := newAdminEnv(t)
	if r := c.do("POST", "security/totp/setup", map[string]any{"password": "nope-nope"}); r.Code != 401 {
		t.Fatalf("setup with wrong password: %d", r.Code)
	}
	setup := e.must(c.do("POST", "security/totp/setup", map[string]any{"password": "password123"}), 200).m(t)
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(setup["secret"].(string))
	if err != nil || len(secret) != 20 {
		t.Fatalf("secret: %v", setup)
	}
	u, _ := url.Parse(setup["uri"].(string))
	if u.Scheme != "otpauth" || u.Query().Get("secret") != setup["secret"] || u.Query().Get("issuer") != "Test Frame" {
		t.Fatalf("uri %s", u)
	}
	// Pending enrolment doesn't affect login yet.
	if c2, r := e.adminLogin("root", "password123", nil); c2 == nil {
		t.Fatalf("login during pending enrolment: %d", r.Code)
	}
	if r := c.do("POST", "security/totp/enable", map[string]any{"code": "000000"}); r.Code != 401 || r.errCode(t) != "totp_invalid" {
		t.Fatalf("enable with wrong code: %d %s", r.Code, r.Body)
	}
	en := e.must(c.do("POST", "security/totp/enable", map[string]any{"code": totp.Code(secret, e.clock.Now())}), 200).m(t)
	codes := en["recovery_codes"].([]any)
	if len(codes) != 8 {
		t.Fatalf("recovery codes: %v", en)
	}
	// Secrets and recovery codes are not stored in plaintext.
	var leaks int
	e.db.R.QueryRow(`SELECT COUNT(*) FROM admin_totp WHERE instr(secret_enc, ?) > 0`, secret).Scan(&leaks)
	for _, rc := range codes {
		var n int
		e.db.R.QueryRow(`SELECT COUNT(*) FROM admin_recovery_codes WHERE instr(CAST(code_hash AS TEXT), ?) > 0`, strings.ReplaceAll(rc.(string), "-", "")).Scan(&n)
		leaks += n
	}
	if leaks != 0 {
		t.Fatal("TOTP secret or recovery code stored in plaintext")
	}
	if s := c.do("GET", "session", nil).m(t); s["admin"].(map[string]any)["totp_enabled"] != true {
		t.Fatalf("session: %v", s)
	}

	// Login now needs the second factor.
	if _, r := e.adminLogin("root", "password123", nil); r.Code != 401 || r.errCode(t) != "totp_required" {
		t.Fatalf("no code: %d %s", r.Code, r.Body)
	}
	if _, r := e.adminLogin("root", "wrong-password", nil); r.Code != 401 || r.errCode(t) != "invalid_credentials" {
		t.Fatalf("wrong password must not reveal totp: %d %s", r.Code, r.Body)
	}
	if _, r := e.adminLogin("root", "password123", "123456"); r.Code != 401 || r.errCode(t) != "totp_invalid" {
		t.Fatalf("wrong code: %d %s", r.Code, r.Body)
	}
	// The code used for enabling can't be replayed in the same step.
	if _, r := e.adminLogin("root", "password123", totp.Code(secret, e.clock.Now())); r.Code != 401 {
		t.Fatalf("replayed code accepted: %d", r.Code)
	}
	e.clock.Add(30 * time.Second)
	code := totp.Code(secret, e.clock.Now())
	if c2, r := e.adminLogin("root", "password123", code); c2 == nil {
		t.Fatalf("valid code: %d %s", r.Code, r.Body)
	}
	if _, r := e.adminLogin("root", "password123", code); r.Code != 401 {
		t.Fatal("same code accepted twice")
	}
	// ±1 step skew: last step's code is still accepted after 30 s.
	e.clock.Add(30 * time.Second)
	prev := totp.HOTP(secret, uint64(totp.StepAt(e.clock.Now())-1), 6)
	if _, r := e.adminLogin("root", "password123", prev); r.Code != 401 {
		t.Fatal("older step than the last used one accepted")
	}
	e.clock.Add(60 * time.Second)
	prev = totp.HOTP(secret, uint64(totp.StepAt(e.clock.Now())-1), 6)
	if c2, r := e.adminLogin("root", "password123", prev); c2 == nil {
		t.Fatalf("previous-step code: %d", r.Code)
	}
	// Recovery codes work once (any case/spacing).
	rc := strings.ToUpper(strings.ReplaceAll(codes[0].(string), "-", " "))
	if c2, r := e.adminLogin("root", "password123", rc); c2 == nil {
		t.Fatalf("recovery code: %d %s", r.Code, r.Body)
	}
	if _, r := e.adminLogin("root", "password123", rc); r.Code != 401 {
		t.Fatal("recovery code reused")
	}
	if sec := c.do("GET", "security", nil).m(t); sec["recovery_codes_left"] != float64(7) || sec["totp_enabled"] != true {
		t.Fatalf("security: %v", sec)
	}
	// Regenerate codes: old ones stop working.
	regen := e.must(c.do("POST", "security/recovery-codes", map[string]any{"password": "password123"}), 200).m(t)["recovery_codes"].([]any)
	if _, r := e.adminLogin("root", "password123", codes[1]); r.Code != 401 {
		t.Fatal("old recovery code works after regeneration")
	}
	// Disable needs password + a valid code.
	if r := c.do("POST", "security/totp/disable", map[string]any{"password": "password123", "code": "000000"}); r.Code != 401 {
		t.Fatalf("disable with bad code: %d", r.Code)
	}
	e.must(c.do("POST", "security/totp/disable", map[string]any{"password": "password123", "code": regen[0]}), 204)
	if c2, r := e.adminLogin("root", "password123", nil); c2 == nil {
		t.Fatalf("login after disabling: %d", r.Code)
	}
	_ = root
}

// ---- users ---------------------------------------------------------------------

func TestAdminUserManagement(t *testing.T) {
	e, root, c := newAdminEnv(t)
	alice, bob := e.register("Alice"), e.register("Bob")
	e.must(e.do("PATCH", "/api/v1/me", alice.Token, map[string]any{"username": "alice"}), 200)
	e.must(e.do("PATCH", "/api/v1/me", bob.Token, map[string]any{"username": "bob"}), 200)
	e.befriend(alice, bob)
	e.postMoment(alice, momentOpts{})
	e.must(e.do("POST", "/api/v1/invites", alice.Token, nil), 200)

	var list struct {
		Users []map[string]any
		Total int
	}
	e.must(c.do("GET", "users?q=ali", nil), 200).decode(t, &list)
	if list.Total != 1 || list.Users[0]["username"] != "alice" || list.Users[0]["moments"] != float64(1) ||
		list.Users[0]["friends"] != float64(1) || list.Users[0]["storage_bytes"].(float64) <= 0 ||
		list.Users[0]["email_masked"] != "a•••"+alice.Email[len("alice"):] || list.Users[0]["last_seen_at"] == nil {
		t.Fatalf("search: %+v", list)
	}
	if _, ok := list.Users[0]["email"]; ok {
		t.Fatal("list must not include the full email")
	}
	e.must(c.do("GET", "users?status=admins", nil), 200).decode(t, &list)
	if list.Total != 1 || list.Users[0]["username"] != "root" {
		t.Fatalf("admins: %+v", list)
	}
	e.must(c.do("GET", "users?sort=display_name&order=asc&per_page=2&page=2", nil), 200).decode(t, &list)
	if list.Total != 3 || len(list.Users) != 1 || list.Users[0]["display_name"] != "Root" {
		t.Fatalf("paging: %+v", list)
	}

	detail := e.must(c.do("GET", "users/"+alice.ID, nil), 200).m(t)
	if detail["user"].(map[string]any)["email"] != alice.Email || len(detail["sessions"].([]any)) != 1 || len(detail["invites"].([]any)) != 1 {
		t.Fatalf("detail: %v", detail)
	}
	if r := c.do("GET", "users/01JC3V9Z4X8Q2M7T5R1N6K0B3H", nil); r.Code != 404 {
		t.Fatalf("unknown user: %d", r.Code)
	}

	// Reset password: one-time password, sessions revoked.
	pw := e.must(c.do("POST", "users/"+alice.ID+"/reset-password", nil), 200).m(t)["password"].(string)
	if r := e.do("GET", "/api/v1/me", alice.Token, nil); r.Code != 401 {
		t.Fatal("sessions survive reset")
	}
	e.must(e.do("POST", "/api/v1/auth/login", "", map[string]any{"method": "email", "identifier": alice.Email, "password": "password123"}), 401)
	alice.Token = e.must(e.do("POST", "/api/v1/auth/login", "", map[string]any{"method": "email", "identifier": alice.Email, "password": pw}), 200).m(t)["token"].(string)

	// Suspend / unsuspend.
	e.must(c.do("POST", "users/"+alice.ID+"/disable", nil), 204)
	if r := e.do("POST", "/api/v1/auth/login", "", map[string]any{"method": "email", "identifier": alice.Email, "password": pw}); r.errCode(t) != "account_disabled" {
		t.Fatalf("suspended login: %s", r.Body)
	}
	e.must(c.do("GET", "users?status=disabled", nil), 200).decode(t, &list)
	if list.Total != 1 {
		t.Fatalf("disabled filter: %+v", list)
	}
	e.must(c.do("POST", "users/"+alice.ID+"/enable", nil), 204)
	e.must(e.do("POST", "/api/v1/auth/login", "", map[string]any{"method": "email", "identifier": alice.Email, "password": pw}), 200)

	// Self-protection.
	for _, p := range []string{"disable", "demote", "reset-password"} {
		if r := c.do("POST", "users/"+root.ID+"/"+p, nil); r.Code != 409 || r.errCode(t) != "self_action" {
			t.Errorf("%s self: %d %s", p, r.Code, r.Body)
		}
	}
	if r := c.do("DELETE", "users/"+root.ID, map[string]any{"confirm": "root"}); r.Code != 409 {
		t.Fatalf("delete self: %d", r.Code)
	}

	// Promote bob; bob can demote root; then bob is the last admin.
	e.must(c.do("POST", "users/"+bob.ID+"/promote", nil), 204)
	bc, r := e.adminLogin("bob", "password123", nil)
	if bc == nil {
		t.Fatalf("promoted bob login: %d", r.Code)
	}
	e.must(bc.do("POST", "users/"+root.ID+"/demote", nil), 204)
	if r := c.do("GET", "session", nil); r.Code != 401 {
		t.Fatalf("demoted admin's session must end: %d", r.Code)
	}
	if err := e.srv.SetAdmin(context.Background(), bob.ID, false); err == nil {
		t.Fatal("removed the last admin")
	}

	// Sessions: revoke one.
	sid := e.must(bc.do("GET", "users/"+alice.ID, nil), 200).m(t)["sessions"].([]any)[0].(map[string]any)["id"].(string)
	e.must(bc.do("DELETE", "users/"+alice.ID+"/sessions/"+sid, nil), 204)

	// Delete requires typing the username.
	if r := bc.do("DELETE", "users/"+alice.ID, map[string]any{"confirm": "alicee"}); r.Code != 400 || r.errCode(t) != "confirm_mismatch" {
		t.Fatalf("mismatch: %d %s", r.Code, r.Body)
	}
	e.must(bc.do("DELETE", "users/"+alice.ID, map[string]any{"confirm": "alice"}), 204)
	if r := bc.do("GET", "users/"+alice.ID, nil); r.Code != 404 {
		t.Fatal("deleted user still there")
	}
	var moments int
	e.db.R.QueryRow(`SELECT COUNT(*) FROM moments`).Scan(&moments)
	if moments != 0 {
		t.Fatal("deleted user's content survives")
	}

	// Create a user (closed registration).
	e.setRegistration("closed", false)
	cr := e.must(bc.do("POST", "users", map[string]any{"display_name": "Carol", "username": "carol", "method": "phone",
		"identifier": "+34600111222", "admin": false}), 201).m(t)
	e.must(e.do("POST", "/api/v1/auth/login", "", map[string]any{"method": "phone", "identifier": "+34600111222", "password": cr["password"]}), 200)
	if r := bc.do("POST", "users", map[string]any{"display_name": "C2", "username": "carol", "method": "email", "identifier": "c2@example.com"}); r.Code != 409 || r.errCode(t) != "username_taken" {
		t.Fatalf("dup username: %d %s", r.Code, r.Body)
	}

	// Audit trail.
	var audit struct {
		Entries []struct{ Action string }
		Total   int
	}
	e.must(bc.do("GET", "audit?per_page=100", nil), 200).decode(t, &audit)
	got := map[string]bool{}
	for _, a := range audit.Entries {
		got[a.Action] = true
	}
	for _, want := range []string{"login", "user.reset_password", "user.disable", "user.enable", "user.promote", "user.demote",
		"user.session_revoke", "user.delete", "user.create"} {
		if !got[want] {
			t.Errorf("audit missing %s (have %v)", want, got)
		}
	}
	e.must(bc.do("GET", "audit?per_page=2&page=2", nil), 200).decode(t, &audit)
	if len(audit.Entries) != 2 || audit.Total < 10 {
		t.Fatalf("audit paging: %+v", audit)
	}
}

func TestAdminAvatarOnly(t *testing.T) {
	e, _, c := newAdminEnv(t)
	alice := e.register("Alice")
	if r := c.do("GET", "users/"+alice.ID+"/avatar", nil); r.Code != 404 {
		t.Fatalf("no avatar: %d", r.Code)
	}
	e.must(e.request("PUT", "/api/v1/me/avatar", alice.Token, bytes.NewReader(avatarJPEG()), "image/jpeg"), 200)
	r := e.must(c.do("GET", "users/"+alice.ID+"/avatar", nil), 200)
	if r.Header.Get("Content-Type") != "image/jpeg" || !bytes.HasPrefix(r.Body, []byte{0xFF, 0xD8, 0xFF}) {
		t.Fatalf("avatar: %v", r.Header)
	}
}

// ---- invites --------------------------------------------------------------------

func TestAdminServerInvites(t *testing.T) {
	e, _, c := newAdminEnv(t)
	e.setRegistration("invite", false)
	inv := e.must(c.do("POST", "invites", map[string]any{"note": "for Mum"}), 201).m(t)["invite"].(map[string]any)
	code := inv["code"].(string)
	if inv["max_uses"] != float64(1) || inv["status"] != "active" || inv["note"] != "for Mum" ||
		int64(inv["expires_at"].(float64)) != e.clock.Now().Add(7*24*time.Hour).UnixMilli() ||
		!strings.HasPrefix(inv["url"].(string), "https://frame.crow.sg/i#s=https%3A%2F%2Fframe.example.com&c="+code) {
		t.Fatalf("defaults: %v", inv)
	}
	never := e.must(c.do("POST", "invites", map[string]any{"max_uses": 3, "expires_in_days": nil}), 201).m(t)["invite"].(map[string]any)
	if never["expires_at"] != nil || never["max_uses"] != float64(3) {
		t.Fatalf("never: %v", never)
	}
	for _, bad := range []map[string]any{{"max_uses": 0}, {"max_uses": 1001}, {"expires_in_days": 0}, {"expires_in_days": 366},
		{"note": strings.Repeat("x", 201)}} {
		if r := c.do("POST", "invites", bad); r.Code != 400 {
			t.Errorf("accepted %v", bad)
		}
	}
	// Register through the app API with it; without it fails.
	if r := e.registerWith("nocode@example.com", nil); r.errCode(t) != "invite_required" {
		t.Fatalf("no code: %s", r.Body)
	}
	newU := e.must(e.registerWith("mum@example.com", code), 200).m(t)["user"].(map[string]any)
	var list struct{ Invites []map[string]any }
	e.must(c.do("GET", "invites?status=all", nil), 200).decode(t, &list)
	var used map[string]any
	for _, i := range list.Invites {
		if i["id"] == inv["id"] {
			used = i
		}
	}
	reg := used["registered"].([]any)
	if used["status"] != "used_up" || used["code"] != nil || used["url"] != nil || len(reg) != 1 ||
		reg[0].(map[string]any)["user"].(map[string]any)["id"] != newU["id"] {
		t.Fatalf("used invite: %v", used)
	}
	e.must(c.do("GET", "invites", nil), 200).decode(t, &list)
	if len(list.Invites) != 1 || list.Invites[0]["id"] != never["id"] {
		t.Fatalf("active list: %v", list)
	}
	detail := e.must(c.do("GET", "users/"+newU["id"].(string), nil), 200).m(t)["user"].(map[string]any)
	if detail["server_invite"].(map[string]any)["note"] != "for Mum" {
		t.Fatalf("joined via: %v", detail)
	}
	// Revoke.
	e.must(c.do("DELETE", "invites/"+never["id"].(string), nil), 204)
	if r := e.registerWith("late@example.com", never["code"]); r.errCode(t) != "invite_invalid" {
		t.Fatalf("revoked invite: %s", r.Body)
	}
	if r := c.do("DELETE", "invites/01JC3V9Z4X8Q2M7T5R1N6K0B3H", nil); r.Code != 404 {
		t.Fatalf("unknown invite: %d", r.Code)
	}

	// Members' friend invites: listed without the code, revocable.
	e.setRegistration("open", true)
	m := e.register("Member")
	fcode := e.must(e.do("POST", "/api/v1/invites", m.Token, nil), 200).m(t)["code"].(string)
	var fl struct{ Invites []map[string]any }
	e.must(c.do("GET", "friend-invites", nil), 200).decode(t, &fl)
	if len(fl.Invites) != 1 || fl.Invites[0]["owner"].(map[string]any)["id"] != m.ID || strings.Contains(string(c.do("GET", "friend-invites", nil).Body), fcode) {
		t.Fatalf("friend invites: %v", fl)
	}
	e.must(c.do("DELETE", "friend-invites/"+fl.Invites[0]["id"].(string), nil), 204)
	if r := e.do("GET", "/api/v1/invites/"+fcode, "", nil); r.Code != 404 {
		t.Fatal("revoked friend invite still resolves")
	}
	if r := c.do("DELETE", "friend-invites/zz", nil); r.Code != 404 {
		t.Fatalf("bad id: %d", r.Code)
	}
}

// ---- settings -------------------------------------------------------------------

func TestAdminSettings(t *testing.T) {
	e, _, c := newAdminEnv(t, withConfig(func(cf *config.Config) {
		cf.Explicit["FRAME_MAX_FRIENDS"] = true // as if set in frame.conf
		cf.MaxFriends = 50
	}))
	s := e.must(c.do("GET", "settings", nil), 200).m(t)["settings"].(map[string]any)
	mf := s["max_friends"].(map[string]any)
	if mf["locked"] != true || mf["source"] != "config" || mf["value"] != float64(50) || mf["default"] != float64(0) {
		t.Fatalf("locked setting: %v", mf)
	}
	if reg := s["registration"].(map[string]any); reg["locked"] != false || reg["default"] != "invite" || reg["source"] != "default" {
		t.Fatalf("registration: %v", reg)
	}
	if r := c.do("PATCH", "settings", map[string]any{"max_friends": 10}); r.Code != 409 || r.errCode(t) != "setting_locked" {
		t.Fatalf("locked change: %d %s", r.Code, r.Body)
	}
	if r := c.do("PATCH", "settings", map[string]any{"max_friends": nil}); r.Code != 409 {
		t.Fatalf("locked reset: %d", r.Code)
	}
	for _, bad := range []map[string]any{{"registration": "maybe"}, {"user_invites": "yes"}, {"name": ""}, {"name": strings.Repeat("x", 65)},
		{"max_video_seconds": 0}, {"max_video_seconds": 1.5}, {"max_upload_mb": 2048}, {"bogus": 1}, {}} {
		if r := c.do("PATCH", "settings", bad); r.Code != 400 {
			t.Errorf("accepted %v: %d", bad, r.Code)
		}
	}
	upd := e.must(c.do("PATCH", "settings", map[string]any{"registration": "closed", "user_invites": false, "name": "Crow Nest",
		"max_video_seconds": 30, "max_upload_mb": 100}), 200).m(t)["settings"].(map[string]any)
	if upd["registration"].(map[string]any)["source"] != "panel" || upd["max_upload_mb"].(map[string]any)["value"] != float64(100) {
		t.Fatalf("updated: %v", upd)
	}
	info := e.must(e.do("GET", "/api/v1/server", "", nil), 200).m(t)
	lim := info["limits"].(map[string]any)
	if info["registration"] != "closed" || info["user_invites"] != false || info["name"] != "Crow Nest" ||
		lim["max_video_ms"] != float64(30000) || lim["max_upload_bytes"] != float64(100<<20) || lim["max_friends"] != float64(50) {
		t.Fatalf("live settings not applied: %v", info)
	}
	if r := e.registerWith("x@example.com", nil); r.errCode(t) != "registration_closed" {
		t.Fatalf("closed via panel: %s", r.Body)
	}
	// Settings persist across restarts (a new server on the same DB).
	srv2, err := api.New(api.Deps{Config: e.cfg, DB: e.db, MediaKey: bytes.Repeat([]byte{0x42}, 32), Now: e.clock.Now,
		Log: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	if rec := newRecorder(srv2.Handler(), "GET", "/api/v1/server"); !strings.Contains(rec.Body.String(), `"name":"Crow Nest"`) {
		t.Fatalf("settings lost on restart: %s", rec.Body)
	}
	// Reset to default.
	upd = e.must(c.do("PATCH", "settings", map[string]any{"registration": nil}), 200).m(t)["settings"].(map[string]any)
	if r := upd["registration"].(map[string]any); r["value"] != "open" || r["source"] != "default" {
		// the suite's base config is "open" (see newEnv)
		t.Fatalf("reset: %v", r)
	}
	if !strings.Contains(string(c.do("GET", "audit", nil).Body), "settings.update") {
		t.Fatal("settings change not audited")
	}
}

// ---- maintenance -------------------------------------------------------------------

func TestAdminMaintenance(t *testing.T) {
	e, _, c := newAdminEnv(t)
	if r := c.do("POST", "maintenance/backup", map[string]any{"password": "wrong-password"}); r.Code != 401 {
		t.Fatalf("backup without password: %d", r.Code)
	}
	b := e.must(c.do("POST", "maintenance/backup", map[string]any{"password": "password123"}), 200).m(t)
	u := b["url"].(string)
	r := e.must(e.adminReq("GET", u, nil, c.cookie, nil), 200)
	if !bytes.HasPrefix(r.Body, []byte("SQLite format 3\x00")) || !strings.HasPrefix(r.Header.Get("Content-Disposition"), "attachment; filename=\"frame-backup-") {
		t.Fatalf("backup: %v %q", r.Header, r.Body[:16])
	}
	if r := e.adminReq("GET", u, nil, c.cookie, nil); r.Code != 404 {
		t.Fatalf("backup link reused: %d", r.Code)
	}
	// Another admin session can't use someone else's link; links expire.
	b = e.must(c.do("POST", "maintenance/backup", map[string]any{"password": "password123"}), 200).m(t)
	other, _ := e.adminLogin("root", "password123", nil)
	if r := e.adminReq("GET", b["url"].(string), nil, other.cookie, nil); r.Code != 404 {
		t.Fatalf("other session: %d", r.Code)
	}
	b = e.must(c.do("POST", "maintenance/backup", map[string]any{"password": "password123"}), 200).m(t)
	e.clock.Add(3 * time.Minute)
	if r := e.adminReq("GET", b["url"].(string), nil, c.cookie, nil); r.Code != 404 {
		t.Fatalf("expired link: %d", r.Code)
	}
	cl := e.must(c.do("POST", "maintenance/cleanup", nil), 200).m(t)
	if _, ok := cl["orphan_media_removed"]; !ok {
		t.Fatalf("cleanup: %v", cl)
	}
	ov := e.must(c.do("GET", "overview", nil), 200).m(t)
	if ov["database"].(map[string]any)["bytes"].(float64) <= 0 || ov["users"].(map[string]any)["admins"] != float64(1) ||
		ov["push"].(map[string]any)["configured"] != false || len(ov["recent_actions"].([]any)) == 0 {
		t.Fatalf("overview: %v", ov)
	}
}

// ---- privacy ---------------------------------------------------------------------

// No admin endpoint returns media URLs, captions or message text — except
// the detail of a report, which shows the one item a member chose to report
// to the admin (deliberate, documented in ADMIN.md; covered by
// TestAdminReportDetailShowsOnlyReportedItem).
func TestAdminPrivacy(t *testing.T) {
	e, _, c := newAdminEnv(t)
	alice, bob := e.register("Alice"), e.register("Bob")
	e.befriend(alice, bob)
	e.must(e.request("PUT", "/api/v1/me/avatar", alice.Token, bytes.NewReader(avatarJPEG()), "image/jpeg"), 200)
	m := e.postMoment(alice, momentOpts{caption: map[string]any{"type": "text", "text": "SECRETCAPTION", "style": "classic"}})
	msg := e.sendMessage(bob, alice, "SECRETMESSAGE", m.ID)
	e.must(e.do("PUT", "/api/v1/moments/"+m.ID+"/reaction", bob.Token, map[string]any{"emoji": "🦉"}), 204)
	e.must(e.do("POST", "/api/v1/invites", alice.Token, nil), 200)
	e.must(c.do("POST", "invites", map[string]any{}), 201)
	// Open reports, so the report list and the overview count have rows.
	e.must(e.do("POST", "/api/v1/reports", bob.Token, map[string]any{
		"target": map[string]any{"type": "moment", "id": m.ID}, "reason": "spam", "note": "please look", "block": false}), 204)
	e.must(e.do("POST", "/api/v1/reports", alice.Token, map[string]any{
		"target": map[string]any{"type": "message", "id": msg["id"]}, "reason": "harassment", "note": nil, "block": false}), 204)

	// The only routes allowed to return content.
	exception := map[string]bool{
		"/admin/api/v1/reports/{id}": true, "/admin/api/v1/reports/{id}/media": true, "/admin/api/v1/reports/{id}/thumb": true,
	}
	var bodies []string
	for _, rt := range api.AdminRoutesForTest(e.srv) {
		method, path, _ := strings.Cut(rt.Pattern, " ")
		if method != "GET" || strings.Contains(path, "{token}") || strings.HasSuffix(path, "/avatar") || exception[path] {
			continue
		}
		for _, id := range []string{alice.ID, bob.ID} {
			p := strings.ReplaceAll(path, "{id}", id)
			r := e.adminReq("GET", p, nil, c.cookie, nil)
			if r.Code != 200 {
				t.Fatalf("%s: %d %s", p, r.Code, r.Body)
			}
			bodies = append(bodies, string(r.Body))
		}
	}
	sawReports := false
	for _, b := range bodies {
		for _, bad := range []string{"/m/", "SECRETCAPTION", "SECRETMESSAGE", "🦉", m.ID, msg["id"].(string), "caption"} {
			if strings.Contains(b, bad) {
				t.Fatalf("admin response contains %q: %s", bad, b)
			}
		}
		sawReports = sawReports || strings.Contains(b, "please look")
	}
	if !sawReports {
		t.Fatal("the report list wasn't checked")
	}
	// Every exempt route exists (so the exception can't silently widen).
	registered := map[string]bool{}
	for _, rt := range api.AdminRoutesForTest(e.srv) {
		_, path, _ := strings.Cut(rt.Pattern, " ")
		registered[path] = true
	}
	for p := range exception {
		if !registered[p] {
			t.Errorf("exempt route %s is not registered", p)
		}
	}
}

// ---- modes, listener split, SPA ------------------------------------------------------

func TestAdminModes(t *testing.T) {
	off := newEnv(t, withConfig(func(c *config.Config) { c.Admin = "off" }))
	for _, p := range []string{"/admin/", "/admin/api/v1/setup", "/admin/assets/x.js"} {
		if r := off.adminReq("GET", p, nil, "", nil); r.Code != 404 {
			t.Errorf("off: %s → %d", p, r.Code)
		}
	}
	local := newEnv(t, withConfig(func(c *config.Config) {
		c.Admin = "local"
		c.TrustedProxies = append(c.TrustedProxies, mustPrefix(t, "127.0.0.1/32"))
	}))
	if r := local.adminReq("GET", "/admin/api/v1/setup", nil, "", nil); r.Code != 200 {
		t.Fatalf("local from loopback: %d", r.Code)
	}
	// Through the (trusted) proxy from a public address: hidden.
	if r := local.adminReq("GET", "/admin/api/v1/setup", nil, "", map[string]string{"X-Forwarded-For": "203.0.113.9"}); r.Code != 404 {
		t.Fatalf("local from public IP: %d", r.Code)
	}
	if r := local.adminReq("GET", "/api/v1/server", nil, "", map[string]string{"X-Forwarded-For": "203.0.113.9"}); r.Code != 200 {
		t.Fatalf("app API from public IP: %d", r.Code)
	}

	split := newEnv(t, withConfig(func(c *config.Config) { c.AdminListen = "127.0.0.1:0" }))
	if r := split.adminReq("GET", "/admin/api/v1/setup", nil, "", nil); r.Code != 404 {
		t.Fatalf("main listener serves admin: %d", r.Code)
	}
	ah := split.srv.AdminHandler()
	if ah == nil {
		t.Fatal("no admin handler")
	}
	rec := newRecorder(ah, "GET", "/admin/api/v1/setup")
	if rec.Code != 200 {
		t.Fatalf("admin listener: %d", rec.Code)
	}
	if rec := newRecorder(ah, "GET", "/api/v1/server"); rec.Code != 404 {
		t.Fatalf("admin listener serves app API: %d", rec.Code)
	}
	if e := newEnv(t); e.srv.AdminHandler() != nil {
		t.Fatal("admin handler without FRAME_ADMIN_LISTEN")
	}
}

func TestAdminSPA(t *testing.T) {
	e := newEnv(t)
	r := e.adminReq("GET", "/admin", nil, "", nil)
	if r.Code != 301 || r.Header.Get("Location") != "/admin/" {
		t.Fatalf("redirect: %d %v", r.Code, r.Header)
	}
	idx := e.must(e.adminReq("GET", "/admin/", nil, "", nil), 200)
	if idx.Header.Get("Content-Security-Policy") != api.AdminCSP || idx.Header.Get("Cache-Control") != "no-store" ||
		!strings.HasPrefix(idx.Header.Get("Content-Type"), "text/html") || idx.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("index headers: %v", idx.Header)
	}
	// No inline script or style anywhere in the shell (CSP would block it).
	html := strings.ToLower(string(idx.Body))
	if strings.Contains(html, "<style") || strings.Contains(html, " style=") || strings.Contains(html, "<script>") ||
		strings.Contains(html, "javascript:") {
		t.Fatalf("inline script/style in index.html: %s", idx.Body)
	}
	for _, p := range []string{"/admin/users/01JC3V9Z4X8Q2M7T5R1N6K0B3H", "/admin/setup", "/admin/index.html"} {
		if r := e.must(e.adminReq("GET", p, nil, "", nil), 200); string(r.Body) != string(idx.Body) {
			t.Errorf("%s is not the SPA shell", p)
		}
	}
	for _, p := range []string{"/admin/assets/missing-abc123.js", "/admin/nope.png", "/admin/api/v1/nope"} {
		if r := e.adminReq("GET", p, nil, "", nil); r.Code != 404 {
			t.Errorf("%s: %d", p, r.Code)
		}
	}
	// Hashed assets referenced by the shell are cached immutably.
	for _, ref := range assetRefs(string(idx.Body)) {
		r := e.must(e.adminReq("GET", ref, nil, "", nil), 200)
		if r.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" || r.Header.Get("Content-Security-Policy") != api.AdminCSP {
			t.Errorf("%s headers: %v", ref, r.Header)
		}
	}
	if r := e.adminReq("POST", "/admin/users", nil, "", nil); r.Code != 405 {
		t.Fatalf("POST to SPA path: %d", r.Code)
	}
}

func mustPrefix(t testing.TB, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newRecorder runs one request from 127.0.0.1 through h.
func newRecorder(h http.Handler, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "127.0.0.1:4242"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func avatarJPEG() []byte { return devseed.AvatarJPEG(200) }

var assetRE = regexp.MustCompile(`(?:src|href)="(/admin/assets/[^"]+)"`)

// assetRefs lists the hashed assets referenced by the SPA shell.
func assetRefs(html string) []string {
	var out []string
	for _, m := range assetRE.FindAllStringSubmatch(html, -1) {
		out = append(out, m[1])
	}
	return out
}
