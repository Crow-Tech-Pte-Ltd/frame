package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- shared fixtures -------------------------------------------------------

var (
	keysOnce                 sync.Once
	keyA, keyB, keyC, keyBad *rsa.PrivateKey // 2048-bit
	keyShort                 *rsa.PrivateKey // 1024-bit, must be ignored
)

func testKeys(t testing.TB) {
	t.Helper()
	keysOnce.Do(func() {
		gen := func(bits int) *rsa.PrivateKey {
			k, err := rsa.GenerateKey(rand.Reader, bits)
			if err != nil {
				panic(err)
			}
			return k
		}
		keyA, keyB, keyC, keyBad = gen(2048), gen(2048), gen(2048), gen(2048)
		keyShort = gen(1024)
	})
}

const (
	appleAud = "com.crowtech.frame"
	rawNonce = "raw-nonce-0123456789"
)

var baseTime = time.Unix(1_800_000_000, 0)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type jwksServer struct {
	srv          *httptest.Server
	fetches      atomic.Int32
	mu           sync.Mutex
	keys         map[string]*rsa.PublicKey
	order        []string
	raw          []map[string]any // extra raw JWK entries
	cacheControl string
	status       int
	delay        time.Duration
}

func newJWKSServer(t *testing.T) *jwksServer {
	t.Helper()
	j := &jwksServer{keys: map[string]*rsa.PublicKey{}, status: http.StatusOK, cacheControl: "public, max-age=3600"}
	j.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		j.fetches.Add(1)
		j.mu.Lock()
		delay, status, cc := j.delay, j.status, j.cacheControl
		var entries []map[string]any
		for _, kid := range j.order {
			entries = append(entries, jwk(kid, j.keys[kid]))
		}
		entries = append(entries, j.raw...)
		j.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		if cc != "" {
			w.Header().Set("Cache-Control", cc)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": entries})
	}))
	t.Cleanup(j.srv.Close)
	return j
}

func (j *jwksServer) add(kid string, pub *rsa.PublicKey) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := j.keys[kid]; !ok {
		j.order = append(j.order, kid)
	}
	j.keys[kid] = pub
}

func (j *jwksServer) set(f func(j *jwksServer)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	f(j)
}

func jwk(kid string, pub *rsa.PublicKey) map[string]any {
	return map[string]any{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid,
		"n": b64(pub.N.Bytes()),
		"e": b64(big.NewInt(int64(pub.E)).Bytes()),
	}
}

type env struct {
	clock *fakeClock
	jwks  *jwksServer
	v     *Verifier
}

func newEnv(t *testing.T, p Provider, auds ...string) *env {
	t.Helper()
	testKeys(t)
	e := &env{clock: &fakeClock{t: baseTime}, jwks: newJWKSServer(t)}
	e.jwks.add("A", &keyA.PublicKey)
	p.JWKSURL = e.jwks.srv.URL + "/keys"
	if len(auds) == 0 {
		auds = []string{appleAud, "com.crowtech.frame.signin"}
	}
	e.v = NewVerifier(p, auds, Options{
		HTTPClient:         e.jwks.srv.Client(),
		Now:                e.clock.Now,
		MinRefetchInterval: 30 * time.Second,
	})
	return e
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func nonceHash(raw string) string {
	s := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(s[:])
}

func appleClaims(now time.Time) map[string]any {
	return map[string]any{
		"iss":            "https://appleid.apple.com",
		"aud":            appleAud,
		"sub":            "001234.0123456789abcdef.0123",
		"iat":            now.Add(-10 * time.Second).Unix(),
		"exp":            now.Add(10 * time.Minute).Unix(),
		"nonce":          nonceHash(rawNonce),
		"email":          "abc@privaterelay.appleid.com",
		"email_verified": "true",
	}
}

func rsHeader(kid string) map[string]any {
	return map[string]any{"alg": "RS256", "kid": kid, "typ": "JWT"}
}

func segments(t testing.TB, hdr, claims map[string]any) string {
	t.Helper()
	h, err := json.Marshal(hdr)
	if err != nil {
		t.Fatal(err)
	}
	p, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return b64(h) + "." + b64(p)
}

func signRS256(t testing.TB, priv *rsa.PrivateKey, input string) string {
	t.Helper()
	d := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return b64(sig)
}

func mint(t testing.TB, priv *rsa.PrivateKey, hdr, claims map[string]any) string {
	t.Helper()
	in := segments(t, hdr, claims)
	return in + "." + signRS256(t, priv, in)
}

func mustInvalid(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected verification failure, got nil")
	}
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("error %v does not wrap ErrInvalidToken", err)
	}
}

// ---- tests -----------------------------------------------------------------

func TestProviders(t *testing.T) {
	a, g := AppleProvider(), GoogleProvider()
	if a.Name != "apple" || len(a.Issuers) != 1 || a.Issuers[0] != "https://appleid.apple.com" || a.JWKSURL != "https://appleid.apple.com/auth/keys" {
		t.Fatalf("apple provider: %+v", a)
	}
	if g.Name != "google" || len(g.Issuers) != 2 || g.JWKSURL != "https://www.googleapis.com/oauth2/v3/certs" {
		t.Fatalf("google provider: %+v", g)
	}
}

func TestVerifyAppleValid(t *testing.T) {
	e := newEnv(t, AppleProvider())
	tok := mint(t, keyA, rsHeader("A"), appleClaims(e.clock.Now()))
	c, err := e.v.Verify(context.Background(), tok, rawNonce, true)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if c.Subject != "001234.0123456789abcdef.0123" || c.Issuer != "https://appleid.apple.com" || c.Audience != appleAud {
		t.Fatalf("claims: %+v", c)
	}
	if !c.EmailVerified || c.Email != "abc@privaterelay.appleid.com" || c.Nonce != nonceHash(rawNonce) {
		t.Fatalf("claims: %+v", c)
	}
	if !c.ExpiresAt.Equal(baseTime.Add(10*time.Minute)) || !c.IssuedAt.Equal(baseTime.Add(-10*time.Second)) {
		t.Fatalf("times: %+v", c)
	}
	// Cached: a second verify does not refetch.
	if _, err := e.v.Verify(context.Background(), tok, rawNonce, true); err != nil {
		t.Fatal(err)
	}
	if n := e.jwks.fetches.Load(); n != 1 {
		t.Fatalf("fetches = %d, want 1", n)
	}
}

func TestVerifyGoogleBothIssuers(t *testing.T) {
	const aud = "1234-abc.apps.googleusercontent.com"
	e := newEnv(t, GoogleProvider(), aud)
	for _, iss := range []string{"accounts.google.com", "https://accounts.google.com"} {
		cl := map[string]any{
			"iss": iss, "aud": aud, "azp": aud, "sub": "1098765432109876543",
			"iat": e.clock.Now().Unix(), "exp": e.clock.Now().Add(time.Hour).Unix(),
			"email": "alice@gmail.com", "email_verified": true,
		}
		c, err := e.v.Verify(context.Background(), mint(t, keyA, rsHeader("A"), cl), "", false)
		if err != nil {
			t.Fatalf("iss %q: %v", iss, err)
		}
		if c.Issuer != iss || !c.EmailVerified || c.Email != "alice@gmail.com" {
			t.Fatalf("claims: %+v", c)
		}
	}
	// Apple's issuer is not accepted by the Google verifier.
	cl := appleClaims(e.clock.Now())
	cl["aud"] = aud
	_, err := e.v.Verify(context.Background(), mint(t, keyA, rsHeader("A"), cl), "", false)
	mustInvalid(t, err)
}

func TestAudienceArray(t *testing.T) {
	e := newEnv(t, AppleProvider())
	now := e.clock.Now()
	cases := []struct {
		name string
		aud  any
		azp  any
		ok   bool
	}{
		{"array with match", []string{"other", appleAud}, nil, true},
		{"array match + good azp", []string{"other", appleAud}, appleAud, true},
		{"array match + foreign azp", []string{"other", appleAud}, "evil", false},
		{"single-entry array, foreign azp ignored", []string{appleAud}, "evil", true},
		{"array no match", []string{"other", "evil"}, nil, false},
		{"empty array", []string{}, nil, false},
		{"empty string", "", nil, false},
		{"number", 42, nil, false},
		{"missing", nil, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cl := appleClaims(now)
			delete(cl, "aud")
			if tc.aud != nil {
				cl["aud"] = tc.aud
			}
			if tc.azp != nil {
				cl["azp"] = tc.azp
			}
			c, err := e.v.Verify(context.Background(), mint(t, keyA, rsHeader("A"), cl), rawNonce, true)
			if tc.ok {
				if err != nil {
					t.Fatalf("want ok, got %v", err)
				}
				if c.Audience != appleAud {
					t.Fatalf("audience = %q", c.Audience)
				}
				return
			}
			mustInvalid(t, err)
		})
	}
}

func TestWrongAlgorithms(t *testing.T) {
	e := newEnv(t, AppleProvider())
	cl := appleClaims(e.clock.Now())

	pubDER, err := x509.MarshalPKIXPublicKey(&keyA.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	hs := func(secret []byte, input string) string {
		m := hmac.New(sha256.New, secret)
		m.Write([]byte(input))
		return b64(m.Sum(nil))
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]func() string{
		"none empty sig": func() string {
			return segments(t, map[string]any{"alg": "none", "kid": "A"}, cl) + "."
		},
		"none with junk sig": func() string {
			return segments(t, map[string]any{"alg": "none", "kid": "A"}, cl) + "." + b64([]byte("x"))
		},
		"HS256 keyed with public PEM": func() string {
			in := segments(t, map[string]any{"alg": "HS256", "kid": "A"}, cl)
			return in + "." + hs(pubPEM, in)
		},
		"HS256 keyed with modulus": func() string {
			in := segments(t, map[string]any{"alg": "HS256", "kid": "A"}, cl)
			return in + "." + hs(keyA.N.Bytes(), in)
		},
		"ES256": func() string {
			in := segments(t, map[string]any{"alg": "ES256", "kid": "A"}, cl)
			d := sha256.Sum256([]byte(in))
			sig, err := ecdsa.SignASN1(rand.Reader, ecKey, d[:])
			if err != nil {
				t.Fatal(err)
			}
			return in + "." + b64(sig)
		},
		"PS256": func() string {
			in := segments(t, map[string]any{"alg": "PS256", "kid": "A"}, cl)
			d := sha256.Sum256([]byte(in))
			sig, err := rsa.SignPSS(rand.Reader, keyA, crypto.SHA256, d[:], nil)
			if err != nil {
				t.Fatal(err)
			}
			return in + "." + b64(sig)
		},
		"RS256 header but PSS signature": func() string {
			in := segments(t, rsHeader("A"), cl)
			d := sha256.Sum256([]byte(in))
			sig, err := rsa.SignPSS(rand.Reader, keyA, crypto.SHA256, d[:], nil)
			if err != nil {
				t.Fatal(err)
			}
			return in + "." + b64(sig)
		},
		"RS512": func() string { return mint(t, keyA, map[string]any{"alg": "RS512", "kid": "A"}, cl) },
		"lowercase rs256": func() string {
			return mint(t, keyA, map[string]any{"alg": "rs256", "kid": "A"}, cl)
		},
		"alg missing":    func() string { return mint(t, keyA, map[string]any{"kid": "A"}, cl) },
		"alg not string": func() string { return mint(t, keyA, map[string]any{"alg": 256, "kid": "A"}, cl) },
		"crit header": func() string {
			return mint(t, keyA, map[string]any{"alg": "RS256", "kid": "A", "crit": []string{"exp"}}, cl)
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := e.v.Verify(context.Background(), mk(), rawNonce, true)
			mustInvalid(t, err)
		})
	}
}

func TestMissingKid(t *testing.T) {
	e := newEnv(t, AppleProvider())
	cl := appleClaims(e.clock.Now())
	for _, hdr := range []map[string]any{
		{"alg": "RS256"},
		{"alg": "RS256", "kid": ""},
		{"alg": "RS256", "kid": 7},
	} {
		_, err := e.v.Verify(context.Background(), mint(t, keyA, hdr, cl), rawNonce, true)
		mustInvalid(t, err)
	}
	if n := e.jwks.fetches.Load(); n != 0 {
		t.Fatalf("structurally invalid tokens must not trigger fetches, got %d", n)
	}
}

func TestUnknownKidRefetchRateLimited(t *testing.T) {
	e := newEnv(t, AppleProvider())
	ctx := context.Background()
	if _, err := e.v.Verify(ctx, mint(t, keyA, rsHeader("A"), appleClaims(e.clock.Now())), rawNonce, true); err != nil {
		t.Fatal(err)
	}
	if n := e.jwks.fetches.Load(); n != 1 {
		t.Fatalf("fetches = %d, want 1", n)
	}

	// Provider rotates in key B; a token with kid B triggers one refetch.
	e.clock.Advance(31 * time.Second)
	e.jwks.add("B", &keyB.PublicKey)
	if _, err := e.v.Verify(ctx, mint(t, keyB, rsHeader("B"), appleClaims(e.clock.Now())), rawNonce, true); err != nil {
		t.Fatalf("rotated key: %v", err)
	}
	if n := e.jwks.fetches.Load(); n != 2 {
		t.Fatalf("fetches = %d, want 2", n)
	}

	// Another unknown kid within the interval: no refetch.
	e.clock.Advance(5 * time.Second)
	_, err := e.v.Verify(ctx, mint(t, keyC, rsHeader("C"), appleClaims(e.clock.Now())), rawNonce, true)
	mustInvalid(t, err)
	if errors.Is(err, ErrKeysUnavailable) {
		t.Fatalf("unknown kid should not report keys unavailable: %v", err)
	}
	if n := e.jwks.fetches.Load(); n != 2 {
		t.Fatalf("fetches = %d, want 2 (rate limited)", n)
	}

	// After the interval one more refetch happens; still unknown.
	e.clock.Advance(30 * time.Second)
	_, err = e.v.Verify(ctx, mint(t, keyC, rsHeader("C"), appleClaims(e.clock.Now())), rawNonce, true)
	mustInvalid(t, err)
	if n := e.jwks.fetches.Load(); n != 3 {
		t.Fatalf("fetches = %d, want 3", n)
	}
	// Known keys are still served from the cache.
	if _, err := e.v.Verify(ctx, mint(t, keyA, rsHeader("A"), appleClaims(e.clock.Now())), rawNonce, true); err != nil {
		t.Fatal(err)
	}
	if n := e.jwks.fetches.Load(); n != 3 {
		t.Fatalf("fetches = %d, want 3", n)
	}
}

func TestKidSwapAndBadSignature(t *testing.T) {
	e := newEnv(t, AppleProvider())
	e.jwks.add("B", &keyB.PublicKey)
	ctx := context.Background()
	cl := appleClaims(e.clock.Now())

	// kid says A, signed with B.
	_, err := e.v.Verify(ctx, mint(t, keyB, rsHeader("A"), cl), rawNonce, true)
	mustInvalid(t, err)
	// kid says B, signed with A.
	_, err = e.v.Verify(ctx, mint(t, keyA, rsHeader("B"), cl), rawNonce, true)
	mustInvalid(t, err)
	// Signed by a key the provider never published, under a published kid.
	_, err = e.v.Verify(ctx, mint(t, keyBad, rsHeader("A"), cl), rawNonce, true)
	mustInvalid(t, err)

	good := mint(t, keyA, rsHeader("A"), cl)
	parts := strings.Split(good, ".")

	// Tampered payload (sub changed) with the original signature.
	tampered := appleClaims(e.clock.Now())
	tampered["sub"] = "attacker"
	p, _ := json.Marshal(tampered)
	_, err = e.v.Verify(ctx, parts[0]+"."+b64(p)+"."+parts[2], rawNonce, true)
	mustInvalid(t, err)

	// Tampered signature byte.
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sig[len(sig)/2] ^= 0x01
	_, err = e.v.Verify(ctx, parts[0]+"."+parts[1]+"."+b64(sig), rawNonce, true)
	mustInvalid(t, err)

	// Truncated signature.
	_, err = e.v.Verify(ctx, parts[0]+"."+parts[1]+"."+b64(sig[:len(sig)-1]), rawNonce, true)
	mustInvalid(t, err)

	// Header swapped to another valid kid after signing.
	h, _ := json.Marshal(rsHeader("B"))
	_, err = e.v.Verify(ctx, b64(h)+"."+parts[1]+"."+parts[2], rawNonce, true)
	mustInvalid(t, err)

	if _, err := e.v.Verify(ctx, good, rawNonce, true); err != nil {
		t.Fatalf("untampered token: %v", err)
	}
}

func TestClaimChecks(t *testing.T) {
	e := newEnv(t, AppleProvider())
	now := e.clock.Now()
	cases := []struct {
		name string
		mod  func(m map[string]any)
		ok   bool
	}{
		{"valid", func(m map[string]any) {}, true},
		{"wrong iss", func(m map[string]any) { m["iss"] = "https://evil.example" }, false},
		{"iss trailing slash", func(m map[string]any) { m["iss"] = "https://appleid.apple.com/" }, false},
		{"iss google", func(m map[string]any) { m["iss"] = "accounts.google.com" }, false},
		{"iss missing", func(m map[string]any) { delete(m, "iss") }, false},
		{"iss not string", func(m map[string]any) { m["iss"] = 1 }, false},
		{"wrong aud", func(m map[string]any) { m["aud"] = "com.evil.app" }, false},
		{"second configured aud", func(m map[string]any) { m["aud"] = "com.crowtech.frame.signin" }, true},
		{"expired beyond skew", func(m map[string]any) { m["exp"] = now.Add(-3 * time.Minute).Unix() }, false},
		{"expired exactly at skew", func(m map[string]any) { m["exp"] = now.Add(-2 * time.Minute).Unix() }, false},
		{"expired within skew", func(m map[string]any) { m["exp"] = now.Add(-1 * time.Minute).Unix() }, true},
		{"fractional exp", func(m map[string]any) { m["exp"] = float64(now.Add(time.Minute).Unix()) + 0.5 }, true},
		{"exp as string", func(m map[string]any) { m["exp"] = "1900000000" }, false},
		{"exp negative", func(m map[string]any) { m["exp"] = -1 }, false},
		{"exp huge", func(m map[string]any) { m["exp"] = 1e300 }, false},
		{"exp null", func(m map[string]any) { m["exp"] = nil }, false},
		{"missing exp", func(m map[string]any) { delete(m, "exp") }, false},
		{"iat future beyond skew", func(m map[string]any) { m["iat"] = now.Add(3 * time.Minute).Unix() }, false},
		{"iat future within skew", func(m map[string]any) { m["iat"] = now.Add(1 * time.Minute).Unix() }, true},
		{"missing iat", func(m map[string]any) { delete(m, "iat") }, false},
		{"iat as string", func(m map[string]any) { m["iat"] = "now" }, false},
		{"missing sub", func(m map[string]any) { delete(m, "sub") }, false},
		{"empty sub", func(m map[string]any) { m["sub"] = "" }, false},
		{"oversized sub", func(m map[string]any) { m["sub"] = strings.Repeat("s", 256) }, false},
		{"sub not string", func(m map[string]any) { m["sub"] = 12345 }, false},
		{"email_verified string false", func(m map[string]any) { m["email_verified"] = "false" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cl := appleClaims(now)
			tc.mod(cl)
			c, err := e.v.Verify(context.Background(), mint(t, keyA, rsHeader("A"), cl), rawNonce, true)
			if tc.ok {
				if err != nil {
					t.Fatalf("want ok, got %v", err)
				}
				if tc.name == "email_verified string false" && c.EmailVerified {
					t.Fatal("email_verified \"false\" parsed as true")
				}
				return
			}
			mustInvalid(t, err)
		})
	}
}

func TestEmailVerifiedForms(t *testing.T) {
	e := newEnv(t, AppleProvider())
	for _, tc := range []struct {
		val  any
		want bool
	}{
		{"true", true}, {true, true}, {"false", false}, {false, false}, {"yes", false}, {1, false}, {nil, false},
	} {
		cl := appleClaims(e.clock.Now())
		cl["email_verified"] = tc.val
		c, err := e.v.Verify(context.Background(), mint(t, keyA, rsHeader("A"), cl), rawNonce, true)
		if err != nil {
			t.Fatalf("%v: %v", tc.val, err)
		}
		if c.EmailVerified != tc.want {
			t.Fatalf("email_verified %v → %v, want %v", tc.val, c.EmailVerified, tc.want)
		}
	}
}

func TestNonce(t *testing.T) {
	e := newEnv(t, AppleProvider())
	ctx := context.Background()
	now := e.clock.Now()
	withNonce := mint(t, keyA, rsHeader("A"), appleClaims(now))
	noNonceClaims := appleClaims(now)
	delete(noNonceClaims, "nonce")
	noNonce := mint(t, keyA, rsHeader("A"), noNonceClaims)
	rawClaims := appleClaims(now)
	rawClaims["nonce"] = rawNonce // raw (unhashed) nonce in token: must not match
	rawInToken := mint(t, keyA, rsHeader("A"), rawClaims)
	upperClaims := appleClaims(now)
	upperClaims["nonce"] = strings.ToUpper(nonceHash(rawNonce))
	upper := mint(t, keyA, rsHeader("A"), upperClaims)

	if _, err := e.v.Verify(ctx, withNonce, rawNonce, true); err != nil {
		t.Fatalf("match: %v", err)
	}
	if _, err := e.v.Verify(ctx, withNonce, rawNonce, false); err != nil {
		t.Fatalf("match (optional): %v", err)
	}
	_, err := e.v.Verify(ctx, withNonce, "other-raw-nonce", true)
	mustInvalid(t, err)
	_, err = e.v.Verify(ctx, withNonce, "", true) // required but not sent
	mustInvalid(t, err)
	_, err = e.v.Verify(ctx, noNonce, rawNonce, false) // raw sent, token has none
	mustInvalid(t, err)
	_, err = e.v.Verify(ctx, rawInToken, rawNonce, true)
	mustInvalid(t, err)
	_, err = e.v.Verify(ctx, upper, rawNonce, true)
	mustInvalid(t, err)
	// Optional and not sent: token nonce is ignored (Google without nonce).
	if _, err := e.v.Verify(ctx, withNonce, "", false); err != nil {
		t.Fatalf("optional nonce: %v", err)
	}
	if _, err := e.v.Verify(ctx, noNonce, "", false); err != nil {
		t.Fatalf("no nonce at all: %v", err)
	}
}

func TestMalformedTokens(t *testing.T) {
	e := newEnv(t, AppleProvider())
	good := mint(t, keyA, rsHeader("A"), appleClaims(e.clock.Now()))
	p := strings.Split(good, ".")
	padded := func(s string) string {
		for len(s)%4 != 0 {
			s += "="
		}
		return s
	}
	cases := map[string]string{
		"empty":               "",
		"one segment":         p[0],
		"two segments":        p[0] + "." + p[1],
		"four segments":       good + "." + p[2],
		"empty header":        "." + p[1] + "." + p[2],
		"empty payload":       p[0] + ".." + p[2],
		"empty signature":     p[0] + "." + p[1] + ".",
		"padded header":       padded(p[0]+"x") + "." + p[1] + "." + p[2],
		"padded signature":    p[0] + "." + p[1] + "." + p[2] + "==",
		"std base64 chars":    p[0] + "." + p[1] + "+/" + "." + p[2],
		"newline in segment":  p[0] + "." + p[1][:10] + "\n" + p[1][10:] + "." + p[2],
		"space":               " " + good,
		"header not json":     b64([]byte("not json")) + "." + p[1] + "." + p[2],
		"header json array":   b64([]byte(`["RS256"]`)) + "." + p[1] + "." + p[2],
		"header null":         b64([]byte(`null`)) + "." + p[1] + "." + p[2],
		"payload not json":    p[0] + "." + b64([]byte("nope")) + "." + signRS256(t, keyA, p[0]+"."+b64([]byte("nope"))),
		"payload json array":  p[0] + "." + b64([]byte("[1]")) + "." + signRS256(t, keyA, p[0]+"."+b64([]byte("[1]"))),
		"payload json string": p[0] + "." + b64([]byte(`"x"`)) + "." + signRS256(t, keyA, p[0]+"."+b64([]byte(`"x"`))),
		"oversized":           p[0] + "." + strings.Repeat("A", maxTokenSize) + "." + p[2],
	}
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := e.v.Verify(context.Background(), tok, rawNonce, true)
			mustInvalid(t, err)
		})
	}
}

func TestCacheControlHonoured(t *testing.T) {
	e := newEnv(t, AppleProvider())
	e.jwks.set(func(j *jwksServer) { j.cacheControl = "public, max-age=600, must-revalidate" })
	ctx := context.Background()
	verify := func() {
		t.Helper()
		if _, err := e.v.Verify(ctx, mint(t, keyA, rsHeader("A"), appleClaims(e.clock.Now())), rawNonce, true); err != nil {
			t.Fatal(err)
		}
	}
	verify()
	e.clock.Advance(599 * time.Second)
	verify()
	if n := e.jwks.fetches.Load(); n != 1 {
		t.Fatalf("fetches = %d before max-age elapsed, want 1", n)
	}
	e.clock.Advance(2 * time.Second)
	verify()
	if n := e.jwks.fetches.Load(); n != 2 {
		t.Fatalf("fetches = %d after max-age elapsed, want 2", n)
	}
}

func TestCacheTTL(t *testing.T) {
	cases := []struct {
		cc   []string
		want time.Duration
	}{
		{nil, time.Hour},
		{[]string{"max-age=600"}, 10 * time.Minute},
		{[]string{"public, max-age=21600, must-revalidate"}, 6 * time.Hour},
		{[]string{"max-age=10"}, 5 * time.Minute},
		{[]string{"max-age=0"}, 5 * time.Minute},
		{[]string{"max-age=999999999999"}, 24 * time.Hour},
		{[]string{"max-age=99999999999999999999999"}, time.Hour}, // unparsable
		{[]string{"MAX-AGE=\"900\""}, 15 * time.Minute},
		{[]string{"max-age=-5"}, time.Hour},
		{[]string{"max-age=abc"}, time.Hour},
		{[]string{"no-store"}, time.Hour},
		{[]string{"max-age=7200, no-cache"}, time.Hour},
		{[]string{"private", "max-age=1800"}, 30 * time.Minute},
	}
	for _, tc := range cases {
		h := http.Header{}
		for _, v := range tc.cc {
			h.Add("Cache-Control", v)
		}
		if got := cacheTTL(h); got != tc.want {
			t.Errorf("cacheTTL(%q) = %v, want %v", tc.cc, got, tc.want)
		}
	}
}

func TestStaleKeysServedWhenRefreshFails(t *testing.T) {
	e := newEnv(t, AppleProvider())
	ctx := context.Background()
	verify := func() error {
		_, err := e.v.Verify(ctx, mint(t, keyA, rsHeader("A"), appleClaims(e.clock.Now())), rawNonce, true)
		return err
	}
	if err := verify(); err != nil {
		t.Fatal(err)
	}
	e.jwks.set(func(j *jwksServer) { j.status = http.StatusInternalServerError })
	e.clock.Advance(90 * time.Minute) // past 1 h TTL, within 2× TTL
	if err := verify(); err != nil {
		t.Fatalf("stale keys should be served while refresh fails: %v", err)
	}
	if n := e.jwks.fetches.Load(); n != 2 {
		t.Fatalf("fetches = %d, want 2 (one failed refresh)", n)
	}
	e.clock.Advance(31 * time.Minute) // past 2× TTL
	err := verify()
	mustInvalid(t, err)
	if !errors.Is(err, ErrKeysUnavailable) {
		t.Fatalf("want ErrKeysUnavailable, got %v", err)
	}
	// Provider recovers.
	e.jwks.set(func(j *jwksServer) { j.status = http.StatusOK })
	e.clock.Advance(31 * time.Second)
	if err := verify(); err != nil {
		t.Fatalf("after recovery: %v", err)
	}
}

func TestJWKSKeyFiltering(t *testing.T) {
	e := newEnv(t, AppleProvider())
	e.jwks.add("short", &keyShort.PublicKey)
	bpub := jwk("B", &keyB.PublicKey)
	e.jwks.set(func(j *jwksServer) {
		withField := func(k, v string) map[string]any {
			m := jwk("x", &keyB.PublicKey)
			m["kid"] = "B-" + k
			m[k] = v
			return m
		}
		evenE := jwk("B-even-e", &keyB.PublicKey)
		evenE["e"] = b64(big.NewInt(65536).Bytes())
		bigE := jwk("B-big-e", &keyB.PublicKey)
		bigE["e"] = b64([]byte{1, 0, 0, 0, 1})
		noKid := jwk("", &keyB.PublicKey)
		j.raw = []map[string]any{
			withField("kty", "EC"),
			withField("use", "enc"),
			withField("alg", "RS512"),
			withField("n", "!!!"),
			evenE, bigE, noKid,
			{"kty": "RSA", "kid": "B-plain", "n": bpub["n"], "e": bpub["e"]}, // use/alg absent: accepted
		}
	})
	ctx := context.Background()
	cl := appleClaims(e.clock.Now())
	for _, kid := range []string{"short", "B-kty", "B-use", "B-alg", "B-n", "B-even-e", "B-big-e", ""} {
		priv := keyB
		if kid == "short" {
			priv = keyShort
		}
		hdr := rsHeader(kid)
		_, err := e.v.Verify(ctx, mint(t, priv, hdr, cl), rawNonce, true)
		mustInvalid(t, err)
	}
	if _, err := e.v.Verify(ctx, mint(t, keyB, rsHeader("B-plain"), cl), rawNonce, true); err != nil {
		t.Fatalf("key without use/alg should be accepted: %v", err)
	}
}

func TestParseJWKS(t *testing.T) {
	testKeys(t)
	good, _ := json.Marshal(map[string]any{"keys": []any{jwk("A", &keyA.PublicKey)}})
	if keys, err := parseJWKS(good); err != nil || len(keys) != 1 || keys["A"] == nil {
		t.Fatalf("parseJWKS good: %v %v", keys, err)
	}
	if keys, _ := parseJWKS(good); keys["A"].N.Cmp(keyA.N) != 0 || keys["A"].E != keyA.E {
		t.Fatal("key mismatch")
	}
	for _, bad := range []string{``, `{}`, `{"keys":[]}`, `{"keys":"x"}`, `[]`, `{"keys":[{"kty":"oct","kid":"h","k":"c2VjcmV0"}]}`} {
		if _, err := parseJWKS([]byte(bad)); err == nil {
			t.Errorf("parseJWKS(%q) should fail", bad)
		}
	}
	// Duplicate kid: first wins.
	dup, _ := json.Marshal(map[string]any{"keys": []any{jwk("A", &keyA.PublicKey), jwk("A", &keyB.PublicKey)}})
	if keys, err := parseJWKS(dup); err != nil || keys["A"].N.Cmp(keyA.N) != 0 {
		t.Fatalf("duplicate kid handling: %v", err)
	}
}

func TestNonHTTPSJWKSRejected(t *testing.T) {
	testKeys(t)
	var hits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{jwk("A", &keyA.PublicKey)}})
	}))
	defer plain.Close()
	clock := &fakeClock{t: baseTime}
	p := AppleProvider()
	p.JWKSURL = plain.URL + "/keys"
	v := NewVerifier(p, []string{appleAud}, Options{HTTPClient: plain.Client(), Now: clock.Now})
	_, err := v.Verify(context.Background(), mint(t, keyA, rsHeader("A"), appleClaims(clock.Now())), rawNonce, true)
	mustInvalid(t, err)
	if !errors.Is(err, ErrKeysUnavailable) {
		t.Fatalf("want ErrKeysUnavailable, got %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("plain-http JWKS endpoint was contacted")
	}
}

func TestRedirectToHTTPRejected(t *testing.T) {
	testKeys(t)
	var plainHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{jwk("A", &keyA.PublicKey)}})
	}))
	defer plain.Close()
	tlsSrv := httptest.NewTLSServer(http.RedirectHandler(plain.URL+"/keys", http.StatusFound))
	defer tlsSrv.Close()
	clock := &fakeClock{t: baseTime}
	p := AppleProvider()
	p.JWKSURL = tlsSrv.URL + "/keys"
	v := NewVerifier(p, []string{appleAud}, Options{HTTPClient: tlsSrv.Client(), Now: clock.Now})
	_, err := v.Verify(context.Background(), mint(t, keyA, rsHeader("A"), appleClaims(clock.Now())), rawNonce, true)
	mustInvalid(t, err)
	if plainHits.Load() != 0 {
		t.Fatal("redirect to plain http was followed")
	}
}

func TestOversizedJWKSRejected(t *testing.T) {
	testKeys(t)
	big := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"keys":[],"pad":"`))
		_, _ = w.Write([]byte(strings.Repeat("x", maxJWKSSize+10)))
		_, _ = w.Write([]byte(`"}`))
	}))
	defer big.Close()
	clock := &fakeClock{t: baseTime}
	p := AppleProvider()
	p.JWKSURL = big.URL
	v := NewVerifier(p, []string{appleAud}, Options{HTTPClient: big.Client(), Now: clock.Now})
	_, err := v.Verify(context.Background(), mint(t, keyA, rsHeader("A"), appleClaims(clock.Now())), rawNonce, true)
	mustInvalid(t, err)
	if !errors.Is(err, ErrKeysUnavailable) {
		t.Fatalf("want ErrKeysUnavailable, got %v", err)
	}
}

func TestConcurrentColdCacheSingleFlight(t *testing.T) {
	e := newEnv(t, AppleProvider())
	e.jwks.set(func(j *jwksServer) { j.delay = 100 * time.Millisecond })
	tok := mint(t, keyA, rsHeader("A"), appleClaims(e.clock.Now()))
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.v.Verify(context.Background(), tok, rawNonce, true)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent verify: %v", err)
		}
	}
	if n := e.jwks.fetches.Load(); n != 1 {
		t.Fatalf("fetches = %d, want 1 (single flight)", n)
	}
}

func TestContextCancelled(t *testing.T) {
	e := newEnv(t, AppleProvider())
	e.jwks.set(func(j *jwksServer) { j.delay = 200 * time.Millisecond })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := e.v.Verify(ctx, mint(t, keyA, rsHeader("A"), appleClaims(e.clock.Now())), rawNonce, true)
	mustInvalid(t, err)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
}

func TestDisabledVerifier(t *testing.T) {
	testKeys(t)
	v := NewVerifier(AppleProvider(), nil, Options{})
	if v.Enabled() {
		t.Fatal("verifier without audiences must be disabled")
	}
	if NewVerifier(AppleProvider(), []string{" ", ""}, Options{}).Enabled() {
		t.Fatal("blank audiences must not enable the verifier")
	}
	_, err := v.Verify(context.Background(), "a.b.c", "", false)
	mustInvalid(t, err)
}

func TestErrorsDoNotLeakToken(t *testing.T) {
	e := newEnv(t, AppleProvider())
	cl := appleClaims(e.clock.Now())
	cl["aud"] = "com.evil.app"
	tok := mint(t, keyA, rsHeader("A"), cl)
	parts := strings.Split(tok, ".")
	checks := []string{
		tok,
		parts[0] + "." + parts[1] + "." + parts[2][:20], // bad sig
		parts[0] + ".%%%." + parts[2],                   // malformed
	}
	for _, c := range checks {
		_, err := e.v.Verify(context.Background(), c, rawNonce, true)
		mustInvalid(t, err)
		for _, p := range parts {
			if len(p) > 8 && strings.Contains(err.Error(), p[:8]) {
				t.Fatalf("error leaks token material: %v", err)
			}
		}
	}
}

// ---- fuzzing ---------------------------------------------------------------

func FuzzParseToken(f *testing.F) {
	testKeys(f)
	good := mint(f, keyA, rsHeader("A"), appleClaims(baseTime))
	p := strings.Split(good, ".")
	f.Add(good)
	f.Add("")
	f.Add("a.b.c")
	f.Add(p[0] + "." + p[1])
	f.Add(p[0] + "." + p[1] + "." + p[2] + ".x")
	f.Add(segments(f, map[string]any{"alg": "none"}, map[string]any{}) + ".")
	f.Add(b64([]byte(`{"alg":"RS256","kid":"A","crit":["x"]}`)) + "." + p[1] + "." + p[2])
	f.Add(b64([]byte(`{"alg":"RS256","kid":"A"}`)) + "." + b64([]byte(`{"aud":[1,2],"exp":"x","iat":1e400}`)) + "." + p[2])
	f.Fuzz(func(t *testing.T, tok string) {
		pt, err := parseToken(tok)
		if err != nil {
			if !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("error does not wrap ErrInvalidToken: %v", err)
			}
			return
		}
		if pt.alg != "RS256" || pt.kid == "" || len(pt.signature) == 0 {
			t.Fatalf("parseToken accepted bad header: %+v", pt)
		}
		if !strings.HasPrefix(tok, pt.signingInput+".") {
			t.Fatal("signing input mismatch")
		}
		if rc, err := parseClaims(pt.payload); err == nil && rc == nil {
			t.Fatal("nil claims without error")
		} else if err != nil && !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("claims error does not wrap ErrInvalidToken: %v", err)
		}
	})
}

func FuzzParseJWKS(f *testing.F) {
	testKeys(f)
	good, _ := json.Marshal(map[string]any{"keys": []any{jwk("A", &keyA.PublicKey)}})
	f.Add(good)
	f.Add([]byte(`{"keys":[{"kty":"RSA","kid":"x","n":"AQAB","e":"AQAB"}]}`))
	f.Add([]byte(`{"keys":[{"kty":"RSA","kid":"x","n":"","e":"AAAAAAAAAAA"}]}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		keys, err := parseJWKS(body)
		if err != nil {
			return
		}
		for kid, k := range keys {
			if kid == "" || k == nil || k.N.BitLen() < minKeyBits || k.E < 3 || k.E%2 == 0 {
				t.Fatalf("unsafe key accepted: kid=%q", kid)
			}
		}
	})
}
