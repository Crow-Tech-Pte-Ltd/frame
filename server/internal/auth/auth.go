// Package auth implements password hashing (Argon2id, PHC strings), opaque
// session tokens and identifier validation.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Params are Argon2id parameters. Default follows the OWASP recommendation
// (m=19 MiB, t=2, p=1).
type Params struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
	SaltLen   int
	KeyLen    uint32
}

// DefaultParams are used for all new hashes.
var DefaultParams = Params{MemoryKiB: 19 * 1024, Time: 2, Threads: 1, SaltLen: 16, KeyLen: 32}

// Upper bounds accepted when verifying stored hashes (defence against a
// tampered hash causing huge allocations).
const (
	maxMemoryKiB = 256 * 1024
	maxTime      = 10
	maxThreads   = 16
)

var ErrMismatch = errors.New("auth: password mismatch")

// sem bounds concurrent Argon2 computations so a login flood cannot exhaust
// memory (each hash allocates MemoryKiB).
var sem = make(chan struct{}, max(2, runtime.NumCPU()))

// HashPassword returns a PHC-format Argon2id hash:
// $argon2id$v=19$m=19456,t=2,p=1$<salt b64>$<hash b64>
func HashPassword(ctx context.Context, password string) (string, error) {
	return hashWith(ctx, password, DefaultParams)
}

func hashWith(ctx context.Context, password string, p Params) (string, error) {
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	if err := acquire(ctx); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.MemoryKiB, p.Threads, p.KeyLen)
	<-sem
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.MemoryKiB, p.Time, p.Threads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

func acquire(ctx context.Context) error {
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// VerifyPassword checks password against a PHC hash in constant time.
func VerifyPassword(ctx context.Context, password, phc string) error {
	p, salt, want, err := parsePHC(phc)
	if err != nil {
		return err
	}
	if err := acquire(ctx); err != nil {
		return err
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.MemoryKiB, p.Threads, uint32(len(want)))
	<-sem
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}

// dummyHash is verified against when the user does not exist, so login
// timing does not reveal whether an identifier is registered.
var dummyHash = sync.OnceValue(func() string {
	h, err := hashWith(context.Background(), "frame-dummy-password", DefaultParams)
	if err != nil {
		panic(err)
	}
	return h
})

// BurnTime performs a verification that always fails, matching the cost of
// a real password check.
func BurnTime(ctx context.Context, password string) {
	_ = VerifyPassword(ctx, password+"\x00", dummyHash())
}

func parsePHC(s string) (Params, []byte, []byte, error) {
	bad := errors.New("auth: malformed password hash")
	parts := strings.Split(s, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Params{}, nil, nil, bad
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return Params{}, nil, nil, bad
	}
	var p Params
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return Params{}, nil, nil, bad
		}
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return Params{}, nil, nil, bad
		}
		switch k {
		case "m":
			p.MemoryKiB = uint32(n)
		case "t":
			p.Time = uint32(n)
		case "p":
			if n > 255 {
				return Params{}, nil, nil, bad
			}
			p.Threads = uint8(n)
		default:
			return Params{}, nil, nil, bad
		}
	}
	if p.MemoryKiB < 8 || p.MemoryKiB > maxMemoryKiB || p.Time < 1 || p.Time > maxTime || p.Threads < 1 || p.Threads > maxThreads {
		return Params{}, nil, nil, bad
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return Params{}, nil, nil, bad
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 128 {
		return Params{}, nil, nil, bad
	}
	return p, salt, key, nil
}

// NewSessionToken returns a 256-bit random bearer token (base64url) and the
// SHA-256 digest that is stored server-side.
func NewSessionToken() (token string, hash []byte) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("auth: crypto/rand failed")
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token)
}

// HashToken returns SHA-256(token).
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// ValidTokenSyntax rejects obviously malformed bearer tokens before hitting
// the database.
func ValidTokenSyntax(t string) bool {
	if len(t) != 43 {
		return false
	}
	for i := 0; i < len(t); i++ {
		c := t[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// RandomPassword returns a human-typable one-time password (admin CLI).
func RandomPassword() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("auth: crypto/rand failed")
	}
	out := make([]byte, len(b))
	for i, c := range b {
		out[i] = alphabet[int(c)%len(alphabet)]
	}
	return string(out)
}

var (
	phoneRE = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)
	emailRE = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
)

// NormalizeEmail lowercases/trims and validates an email address.
func NormalizeEmail(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) > 254 || !emailRE.MatchString(s) {
		return "", false
	}
	return s, true
}

// NormalizePhone validates an E.164 phone number (spaces, dashes, dots and
// parentheses are stripped first).
func NormalizePhone(s string) (string, bool) {
	s = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '.', '(', ')':
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if !phoneRE.MatchString(s) {
		return "", false
	}
	return s, true
}

// NormalizeIdentifier validates an identifier for method "email" or "phone".
func NormalizeIdentifier(method, id string) (string, bool) {
	switch method {
	case "email":
		return NormalizeEmail(id)
	case "phone":
		return NormalizePhone(id)
	}
	return "", false
}

// ValidPassword enforces 8–128 characters.
func ValidPassword(p string) bool {
	n := utf8.RuneCountInString(p)
	return utf8.ValidString(p) && n >= 8 && n <= 128
}

// UsernameError explains why a username is invalid ("" when valid). The
// messages match the client-side copy in docs/API.md.
func UsernameError(u string) string {
	n := utf8.RuneCountInString(u)
	switch {
	case n < 3:
		return "Must be at least 3 characters"
	case n > 20:
		return "Can't be longer than 20 characters"
	}
	for _, r := range u {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_') {
			return "Only letters, numbers, _ and . allowed"
		}
	}
	if c := u[0]; !(c >= 'a' && c <= 'z') {
		return "Must start with a letter"
	}
	if c := u[len(u)-1]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
		return "Must end with a letter or number"
	}
	for _, bad := range []string{"..", "__", "._", "_."} {
		if strings.Contains(u, bad) {
			return "Can't contain consecutive . or _"
		}
	}
	return ""
}

// CleanDisplayName trims and validates a display name (1–40 characters, no
// control characters). Returns ok=false if invalid.
func CleanDisplayName(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) {
		return "", false
	}
	n := utf8.RuneCountInString(s)
	if n < 1 || n > 40 {
		return "", false
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return "", false
		}
	}
	return s, true
}
