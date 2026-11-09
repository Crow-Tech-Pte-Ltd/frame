package ids

import (
	"math/rand/v2"
	"sort"
	"testing"
	"time"
)

type seeded struct{ r *rand.ChaCha8 }

func (s seeded) Read(p []byte) (int, error) { return s.r.Read(p) }

func TestULIDRoundTripAndOrder(t *testing.T) {
	g := NewGenerator(seeded{rand.NewChaCha8([32]byte{1})})
	now := time.UnixMilli(1730000000123)
	var got []string
	for i := 0; i < 1000; i++ {
		id := g.At(now.Add(time.Duration(i/10) * time.Millisecond))
		if !Valid(id) || len(id) != Len {
			t.Fatalf("invalid id %q", id)
		}
		ts, err := Time(id)
		if err != nil || ts.UnixMilli() != now.Add(time.Duration(i/10)*time.Millisecond).UnixMilli() {
			t.Fatalf("Time(%s) = %v, %v", id, ts, err)
		}
		got = append(got, id)
	}
	if !sort.StringsAreSorted(got) {
		t.Fatal("ids not monotonic")
	}
	for i := 1; i < len(got); i++ {
		if got[i] == got[i-1] {
			t.Fatal("duplicate id")
		}
	}
}

func TestKnownEncoding(t *testing.T) {
	// Timestamp 0, zero randomness.
	if f := Floor(time.UnixMilli(0)); f != "00000000000000000000000000" {
		t.Fatalf("Floor(0) = %s", f)
	}
	// Max 48-bit timestamp → "7ZZZZZZZZZ".
	f := Floor(time.UnixMilli(1<<48 - 1))
	if f[:10] != "7ZZZZZZZZZ" {
		t.Fatalf("Floor(max) = %s", f)
	}
	if Floor(time.UnixMilli(5)) >= Floor(time.UnixMilli(6)) {
		t.Fatal("floor order")
	}
}

func TestValid(t *testing.T) {
	for _, s := range []string{"", "01JC3V9Z4X8Q2M7T5R1N6K0B3", "81JC3V9Z4X8Q2M7T5R1N6K0B3H", "01JC3V9Z4X8Q2M7T5R1N6K0B3U", "01JC3V9Z4X8Q2M7T5R1N6K0B3!"} {
		if Valid(s) {
			t.Errorf("Valid(%q) = true", s)
		}
	}
	if !Valid("01JC3V9Z4X8Q2M7T5R1N6K0B3H") || Normalize("01jc3v9z4x8q2m7t5r1n6k0b3h") != "01JC3V9Z4X8Q2M7T5R1N6K0B3H" {
		t.Fatal("valid ulid rejected")
	}
}

func TestUUIDAndTokens(t *testing.T) {
	if !ValidUUID("6f1c2b1e-3d4a-4b5c-8d9e-0a1b2c3d4e5f") || ValidUUID("6f1c2b1e3d4a4b5c8d9e0a1b2c3d4e5f") || ValidUUID("6f1c2b1e-3d4a-4b5c-8d9e-0a1b2c3d4e5g") {
		t.Fatal("ValidUUID")
	}
	if a, b := Token(32), Token(32); a == b || len(a) != 43 {
		t.Fatal("Token")
	}
}

func FuzzValid(f *testing.F) {
	f.Add("01JC3V9Z4X8Q2M7T5R1N6K0B3H")
	f.Fuzz(func(t *testing.T, s string) {
		if Valid(s) {
			if _, err := Time(s); err != nil {
				t.Fatal(err)
			}
		}
	})
}
