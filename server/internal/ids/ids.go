// Package ids generates identifiers and opaque random tokens.
//
// Object IDs are ULIDs: 48-bit millisecond timestamp + 80 bits of randomness,
// encoded as 26 Crockford base32 characters. They sort lexicographically in
// creation order, which the store relies on for cursors ("id < cursor").
package ids

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// Len is the length of an encoded ULID.
const Len = 26

var decTable = func() [256]byte {
	var t [256]byte
	for i := range t {
		t[i] = 0xFF
	}
	for i := 0; i < len(crockford); i++ {
		t[crockford[i]] = byte(i)
		// Accept lowercase on input; we always emit uppercase.
		if c := crockford[i]; c >= 'A' && c <= 'Z' {
			t[c+('a'-'A')] = byte(i)
		}
	}
	return t
}()

// Generator produces monotonic ULIDs. The zero value is not usable; use New.
type Generator struct {
	mu      sync.Mutex
	entropy io.Reader
	lastMS  uint64
	lastRnd [10]byte
}

// NewGenerator returns a generator reading randomness from entropy
// (crypto/rand.Reader when nil). Tests pass a seeded reader for stable output.
func NewGenerator(entropy io.Reader) *Generator {
	if entropy == nil {
		entropy = rand.Reader
	}
	return &Generator{entropy: entropy}
}

var defaultGen = NewGenerator(nil)

// SetDefault replaces the package-level generator (tests only).
func SetDefault(g *Generator) { defaultGen = g }

// New returns a ULID for the current time from the default generator.
func New() string { return defaultGen.At(time.Now()) }

// At returns a ULID for t from the default generator.
func At(t time.Time) string { return defaultGen.At(t) }

// At returns a ULID whose timestamp is t. IDs generated within the same
// millisecond are strictly increasing (the random part is incremented).
func (g *Generator) At(t time.Time) string {
	ms := uint64(t.UnixMilli())
	g.mu.Lock()
	defer g.mu.Unlock()
	var rnd [10]byte
	if ms == g.lastMS {
		rnd = g.lastRnd
		// Increment the 80-bit random part; on (astronomically unlikely)
		// overflow fall through to fresh randomness.
		overflow := true
		for i := len(rnd) - 1; i >= 0; i-- {
			rnd[i]++
			if rnd[i] != 0 {
				overflow = false
				break
			}
		}
		if overflow {
			g.fill(&rnd)
		}
	} else {
		g.fill(&rnd)
	}
	g.lastMS, g.lastRnd = ms, rnd
	return encode(ms, rnd)
}

func (g *Generator) fill(b *[10]byte) {
	if _, err := io.ReadFull(g.entropy, b[:]); err != nil {
		panic("ids: entropy source failed: " + err.Error())
	}
}

// Floor returns the smallest ULID with timestamp t (random part all zero).
// Useful to turn a time bound into an ID bound.
func Floor(t time.Time) string {
	ms := t.UnixMilli()
	if ms < 0 {
		ms = 0
	}
	return encode(uint64(ms), [10]byte{})
}

func encode(ms uint64, rnd [10]byte) string {
	var b [16]byte
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	copy(b[6:], rnd[:])
	return encodeBytes(b)
}

func encodeBytes(b [16]byte) string {
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:])
	var out [Len]byte
	// 128 bits → 26 chars × 5 bits = 130 bits; top 2 bits are zero.
	for i := Len - 1; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:])
}

// Valid reports whether s is a syntactically valid ULID (uppercase or
// lowercase Crockford base32, first char ≤ '7').
func Valid(s string) bool {
	if len(s) != Len {
		return false
	}
	for i := 0; i < Len; i++ {
		if decTable[s[i]] == 0xFF {
			return false
		}
	}
	return decTable[s[0]] <= 7
}

// Normalize uppercases a valid ULID; returns "" for invalid input.
func Normalize(s string) string {
	if !Valid(s) {
		return ""
	}
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - ('a' - 'A')
		}
	}
	return string(b)
}

// Time extracts the timestamp of a valid ULID.
func Time(s string) (time.Time, error) {
	if !Valid(s) {
		return time.Time{}, errors.New("ids: invalid ulid")
	}
	var ms uint64
	// The 26 chars carry 130 bits: 2 zero bits, 48-bit timestamp, 80-bit
	// random part. The first 10 chars are therefore exactly the timestamp.
	for i := 0; i < 10; i++ {
		ms = ms<<5 | uint64(decTable[s[i]])
	}
	return time.UnixMilli(int64(ms)), nil
}

// Token returns n random bytes encoded as unpadded base64url.
func Token(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("ids: crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// RandomBytes returns n bytes from crypto/rand.
func RandomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("ids: crypto/rand failed: " + err.Error())
	}
	return b
}

// ValidUUID reports whether s looks like a UUID string (8-4-4-4-12 hex),
// which is what clients send as client_id.
func ValidUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}
