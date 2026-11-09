// Package oidc verifies Sign in with Apple and Google ID tokens by hand.
//
// Only RS256 (RSA PKCS#1 v1.5 with SHA-256) is accepted; every other
// algorithm — including "none", the HMAC family (key-confusion attacks) and
// the ECDSA / PSS families — is rejected before any key is looked up.
// Signing keys come from the provider's JWKS document, fetched over HTTPS
// and cached according to its Cache-Control header (see keys.go).
package oidc

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrInvalidToken is wrapped by every verification failure (use errors.Is).
var ErrInvalidToken = errors.New("oidc: invalid token")

// ErrKeysUnavailable is additionally wrapped when the signing key could not
// be resolved because the JWKS document could not be fetched (network or
// provider outage) rather than because the token is bad.
var ErrKeysUnavailable = errors.New("oidc: signing keys unavailable")

const (
	maxTokenSize = 16 << 10 // bytes
	clockSkew    = 2 * time.Minute
	maxAudiences = 32
	maxSubject   = 255
)

// Provider describes an OpenID provider.
type Provider struct {
	Name    string
	Issuers []string // accepted `iss` values (exact match)
	JWKSURL string   // must be https
}

// AppleProvider returns the Sign in with Apple provider.
func AppleProvider() Provider {
	return Provider{
		Name:    "apple",
		Issuers: []string{"https://appleid.apple.com"},
		JWKSURL: "https://appleid.apple.com/auth/keys",
	}
}

// GoogleProvider returns the Google Sign-In provider.
func GoogleProvider() Provider {
	return Provider{
		Name:    "google",
		Issuers: []string{"accounts.google.com", "https://accounts.google.com"},
		JWKSURL: "https://www.googleapis.com/oauth2/v3/certs",
	}
}

// Options tunes a Verifier. The zero value is fine for production.
type Options struct {
	HTTPClient         *http.Client     // nil → client with 10 s timeout
	Now                func() time.Time // nil → time.Now
	MinRefetchInterval time.Duration    // unknown-kid refetch rate limit; 0 → 30 s
}

// Claims are the verified claims of an ID token.
type Claims struct {
	Issuer        string
	Subject       string
	Audience      string // the configured audience that matched
	Nonce         string
	Email         string
	EmailVerified bool
	IssuedAt      time.Time
	ExpiresAt     time.Time
}

// Verifier verifies ID tokens for one provider. It is safe for concurrent use.
type Verifier struct {
	provider   Provider
	audiences  map[string]struct{}
	client     *http.Client
	now        func() time.Time
	minRefetch time.Duration

	mu          sync.Mutex
	set         *keySet    // last successfully fetched key set
	lastAttempt time.Time  // last fetch attempt (successful or not)
	lastErr     error      // error of the last fetch attempt
	inflight    *fetchCall // single-flight fetch in progress
}

// NewVerifier returns a verifier accepting tokens whose `aud` is one of
// audiences. With no audiences the verifier is disabled.
func NewVerifier(p Provider, audiences []string, opts Options) *Verifier {
	v := &Verifier{
		provider:   p,
		audiences:  make(map[string]struct{}, len(audiences)),
		now:        opts.Now,
		minRefetch: opts.MinRefetchInterval,
	}
	for _, a := range audiences {
		if a = strings.TrimSpace(a); a != "" {
			v.audiences[a] = struct{}{}
		}
	}
	if v.now == nil {
		v.now = time.Now
	}
	if v.minRefetch <= 0 {
		v.minRefetch = 30 * time.Second
	}
	var c http.Client
	if opts.HTTPClient != nil {
		c = *opts.HTTPClient // shallow copy: keeps the caller's transport
	} else {
		c.Timeout = 10 * time.Second
	}
	c.CheckRedirect = httpsOnlyRedirects
	v.client = &c
	return v
}

// Enabled reports whether at least one audience is configured.
func (v *Verifier) Enabled() bool { return len(v.audiences) > 0 }

// Verify checks token and returns its claims. If rawNonce is non-empty the
// token's nonce claim must equal lowercase hex(sha256(rawNonce)); if
// requireNonce is set an empty rawNonce is an error.
func (v *Verifier) Verify(ctx context.Context, token, rawNonce string, requireNonce bool) (*Claims, error) {
	if !v.Enabled() {
		return nil, invalid("provider %q is not configured", v.provider.Name)
	}
	if requireNonce && rawNonce == "" {
		return nil, invalid("nonce required")
	}
	pt, err := parseToken(token)
	if err != nil {
		return nil, err
	}
	key, err := v.publicKey(ctx, pt.kid)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(pt.signingInput))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], pt.signature); err != nil {
		return nil, invalid("bad signature")
	}
	rc, err := parseClaims(pt.payload)
	if err != nil {
		return nil, err
	}
	return v.checkClaims(rc, rawNonce)
}

func (v *Verifier) checkClaims(rc *rawClaims, rawNonce string) (*Claims, error) {
	now := v.now()

	issOK := false
	for _, iss := range v.provider.Issuers {
		if rc.iss == iss {
			issOK = true
			break
		}
	}
	if !issOK {
		return nil, invalid("wrong issuer")
	}

	matched := ""
	for _, a := range rc.aud {
		if _, ok := v.audiences[a]; ok {
			matched = a
			break
		}
	}
	if matched == "" {
		return nil, invalid("wrong audience")
	}
	if len(rc.aud) > 1 && rc.azp != "" {
		if _, ok := v.audiences[rc.azp]; !ok {
			return nil, invalid("wrong authorized party")
		}
	}

	if !rc.hasExp {
		return nil, invalid("missing exp")
	}
	if !now.Before(rc.exp.Add(clockSkew)) {
		return nil, invalid("token expired")
	}
	if !rc.hasIat {
		return nil, invalid("missing iat")
	}
	if rc.iat.After(now.Add(clockSkew)) {
		return nil, invalid("token issued in the future")
	}
	if rc.sub == "" || len(rc.sub) > maxSubject {
		return nil, invalid("missing or invalid sub")
	}

	if rawNonce != "" {
		sum := sha256.Sum256([]byte(rawNonce))
		want := hex.EncodeToString(sum[:])
		if subtle.ConstantTimeCompare([]byte(rc.nonce), []byte(want)) != 1 {
			return nil, invalid("nonce mismatch")
		}
	}

	return &Claims{
		Issuer:        rc.iss,
		Subject:       rc.sub,
		Audience:      matched,
		Nonce:         rc.nonce,
		Email:         rc.email,
		EmailVerified: rc.emailVerified,
		IssuedAt:      rc.iat,
		ExpiresAt:     rc.exp,
	}, nil
}

// parsedToken is the structurally valid, not yet verified token.
type parsedToken struct {
	alg, kid     string
	signingInput string // "header.payload" as transmitted
	payload      []byte // decoded claims JSON
	signature    []byte
}

// parseToken performs every check that does not need a key: size, segment
// structure, strict base64url (no padding, no stray characters), header
// JSON, alg == RS256, kid present and no `crit` header.
func parseToken(token string) (*parsedToken, error) {
	if token == "" {
		return nil, invalid("empty token")
	}
	if len(token) > maxTokenSize {
		return nil, invalid("token too large")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, invalid("token must have 3 segments")
	}
	for _, p := range parts {
		if p == "" {
			return nil, invalid("empty segment")
		}
	}
	headerJSON, err := decodeSegment(parts[0])
	if err != nil {
		return nil, err
	}
	payload, err := decodeSegment(parts[1])
	if err != nil {
		return nil, err
	}
	sig, err := decodeSegment(parts[2])
	if err != nil {
		return nil, err
	}

	var hdr map[string]json.RawMessage
	if err := json.Unmarshal(headerJSON, &hdr); err != nil || hdr == nil {
		return nil, invalid("malformed header")
	}
	if _, ok := hdr["crit"]; ok {
		return nil, invalid("crit header not supported")
	}
	alg, ok := jsonString(hdr["alg"])
	if !ok || alg != "RS256" {
		return nil, invalid("unsupported alg")
	}
	kid, ok := jsonString(hdr["kid"])
	if !ok || kid == "" {
		return nil, invalid("missing kid")
	}
	return &parsedToken{
		alg:          alg,
		kid:          kid,
		signingInput: parts[0] + "." + parts[1],
		payload:      payload,
		signature:    sig,
	}, nil
}

// decodeSegment decodes one strict base64url (RFC 7515, unpadded) segment.
// Characters outside the alphabet are rejected up front because the stdlib
// decoder silently skips CR/LF.
func decodeSegment(s string) ([]byte, error) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return nil, invalid("malformed base64url segment")
		}
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil {
		return nil, invalid("malformed base64url segment")
	}
	return b, nil
}

// rawClaims are the parsed but unchecked claims.
type rawClaims struct {
	iss, sub, azp, nonce, email string
	aud                         []string
	emailVerified               bool
	exp, iat                    time.Time
	hasExp, hasIat              bool
}

func parseClaims(payload []byte) (*rawClaims, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(payload, &m); err != nil || m == nil {
		return nil, invalid("malformed claims")
	}
	rc := &rawClaims{}
	for name, dst := range map[string]*string{
		"iss": &rc.iss, "sub": &rc.sub, "azp": &rc.azp, "nonce": &rc.nonce, "email": &rc.email,
	} {
		raw, present := m[name]
		if !present || isJSONNull(raw) {
			continue
		}
		s, ok := jsonString(raw)
		if !ok {
			return nil, invalid("claim %s must be a string", name)
		}
		*dst = s
	}

	if raw, ok := m["aud"]; ok && !isJSONNull(raw) {
		aud, err := parseAudience(raw)
		if err != nil {
			return nil, err
		}
		rc.aud = aud
	}

	var err error
	if raw, ok := m["exp"]; ok && !isJSONNull(raw) {
		if rc.exp, err = parseNumericDate(raw); err != nil {
			return nil, invalid("malformed exp")
		}
		rc.hasExp = true
	}
	if raw, ok := m["iat"]; ok && !isJSONNull(raw) {
		if rc.iat, err = parseNumericDate(raw); err != nil {
			return nil, invalid("malformed iat")
		}
		rc.hasIat = true
	}

	if raw, ok := m["email_verified"]; ok {
		var b bool
		if json.Unmarshal(raw, &b) == nil {
			rc.emailVerified = b
		} else if s, ok := jsonString(raw); ok {
			rc.emailVerified = s == "true" // Apple sends "true"/"false"
		}
	}
	return rc, nil
}

func parseAudience(raw json.RawMessage) ([]string, error) {
	if s, ok := jsonString(raw); ok {
		if s == "" {
			return nil, invalid("empty aud")
		}
		return []string{s}, nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, invalid("malformed aud")
	}
	if len(list) == 0 || len(list) > maxAudiences {
		return nil, invalid("malformed aud")
	}
	return list, nil
}

// parseNumericDate parses a JSON number (seconds since the epoch, possibly
// fractional). Strings are rejected.
func parseNumericDate(raw json.RawMessage) (time.Time, error) {
	s := string(bytes.TrimSpace(raw))
	if s == "" || !(s[0] == '-' || s[0] >= '0' && s[0] <= '9') {
		return time.Time{}, errors.New("not a number")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 1e11 {
		return time.Time{}, errors.New("out of range")
	}
	sec, frac := math.Modf(f)
	return time.Unix(int64(sec), int64(frac*1e9)), nil
}

func jsonString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

// invalid builds an error wrapping ErrInvalidToken. Messages never contain
// token material.
func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidToken, fmt.Sprintf(format, args...))
}
