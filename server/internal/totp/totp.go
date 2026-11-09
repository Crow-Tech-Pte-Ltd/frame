// Package totp implements RFC 6238 time-based one-time passwords (HMAC-SHA1,
// 6 digits, 30 s steps, ±1 step of clock skew) and single-use recovery codes
// for admin two-factor authentication. Standard library only.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// Period is the time step (RFC 6238 X).
	Period = 30
	// Digits is the code length.
	Digits = 6
	// Skew is how many steps before/after the current one are accepted.
	Skew = 1
	// SecretLen is the shared secret size in bytes (160 bits, RFC 4226 §4).
	SecretLen = 20
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a random shared secret.
func NewSecret() []byte {
	b := make([]byte, SecretLen)
	if _, err := rand.Read(b); err != nil {
		panic("totp: crypto/rand failed")
	}
	return b
}

// EncodeSecret renders a secret as unpadded base32 (what authenticator apps
// expect).
func EncodeSecret(secret []byte) string { return b32.EncodeToString(secret) }

// HOTP computes the RFC 4226 code for counter with the given number of
// digits (dynamic truncation of HMAC-SHA1).
func HOTP(secret []byte, counter uint64, digits int) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := (uint32(sum[off])&0x7f)<<24 | uint32(sum[off+1])<<16 | uint32(sum[off+2])<<8 | uint32(sum[off+3])
	mod := uint32(1)
	for range digits {
		mod *= 10
	}
	s := strconv.FormatUint(uint64(bin%mod), 10)
	return strings.Repeat("0", digits-len(s)) + s
}

// StepAt returns the time step containing t.
func StepAt(t time.Time) int64 { return t.Unix() / Period }

// Code returns the 6-digit code for time t.
func Code(secret []byte, t time.Time) string { return HOTP(secret, uint64(StepAt(t)), Digits) }

// NormalizeCode strips spaces from a user-typed code; it returns "" unless
// the result is exactly Digits decimal digits.
func NormalizeCode(code string) string {
	code = strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return r
	}, strings.TrimSpace(code))
	if len(code) != Digits {
		return ""
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			return ""
		}
	}
	return code
}

// Verify checks code against the steps around now and returns the matching
// step. Callers must reject steps that were already used (replay).
func Verify(secret []byte, code string, now time.Time) (int64, bool) {
	code = NormalizeCode(code)
	if code == "" {
		return 0, false
	}
	cur := StepAt(now)
	var found int64
	ok := 0
	for d := int64(-Skew); d <= Skew; d++ {
		st := cur + d
		if st < 0 {
			continue
		}
		// Compare every candidate (no early exit) in constant time.
		if subtle.ConstantTimeCompare([]byte(HOTP(secret, uint64(st), Digits)), []byte(code)) == 1 {
			found, ok = st, 1
		}
	}
	return found, ok == 1
}

// URI builds the otpauth:// URI authenticator apps import (usually as a QR
// code): otpauth://totp/<issuer>:<account>?secret=…&issuer=…&algorithm=SHA1&digits=6&period=30
func URI(issuer, account string, secret []byte) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	v := url.Values{}
	v.Set("secret", EncodeSecret(secret))
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", strconv.Itoa(Digits))
	v.Set("period", strconv.Itoa(Period))
	return "otpauth://totp/" + label + "?" + v.Encode()
}

// recoveryAlphabet is Crockford base32 in lower case (no i, l, o, u).
const recoveryAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// RecoveryCodeCount is how many recovery codes are issued at once.
const RecoveryCodeCount = 8

// NewRecoveryCodes returns n random codes formatted "xxxxx-xxxxx" (50 bits).
func NewRecoveryCodes(n int) []string {
	out := make([]string, n)
	buf := make([]byte, 10)
	for i := range out {
		if _, err := rand.Read(buf); err != nil {
			panic("totp: crypto/rand failed")
		}
		var sb strings.Builder
		for j, c := range buf {
			if j == 5 {
				sb.WriteByte('-')
			}
			sb.WriteByte(recoveryAlphabet[int(c)&31])
		}
		out[i] = sb.String()
	}
	return out
}

// NormalizeRecoveryCode canonicalises a typed recovery code (case, spaces,
// dashes, Crockford look-alikes i/l→1, o→0). It returns "" if it can't be
// a recovery code.
func NormalizeRecoveryCode(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		switch r {
		case ' ', '-', '\t':
			continue
		case 'i', 'l':
			r = '1'
		case 'o':
			r = '0'
		}
		if !strings.ContainsRune(recoveryAlphabet, r) {
			return ""
		}
		sb.WriteRune(r)
	}
	if sb.Len() != 10 {
		return ""
	}
	return sb.String()
}
