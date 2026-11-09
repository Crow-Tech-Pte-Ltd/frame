package totp

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

var rfcSecret = []byte("12345678901234567890")

// RFC 4226 Appendix D (HOTP, 6 digits).
func TestHOTPVectors(t *testing.T) {
	want := []string{"755224", "287082", "359152", "969429", "338314", "254676", "287922", "162583", "399871", "520489"}
	for i, w := range want {
		if got := HOTP(rfcSecret, uint64(i), 6); got != w {
			t.Errorf("HOTP(%d) = %s, want %s", i, got, w)
		}
	}
}

// RFC 6238 Appendix B (TOTP, SHA-1, 8 digits; the 6-digit code is the last
// six digits of the same truncated value).
func TestTOTPVectors(t *testing.T) {
	for _, v := range []struct {
		unix int64
		want string
	}{
		{59, "94287082"}, {1111111109, "07081804"}, {1111111111, "14050471"},
		{1234567890, "89005924"}, {2000000000, "69279037"}, {20000000000, "65353130"},
	} {
		tm := time.Unix(v.unix, 0)
		if got := HOTP(rfcSecret, uint64(StepAt(tm)), 8); got != v.want {
			t.Errorf("T=%d: %s, want %s", v.unix, got, v.want)
		}
		if got := Code(rfcSecret, tm); got != v.want[2:] {
			t.Errorf("T=%d 6 digits: %s, want %s", v.unix, got, v.want[2:])
		}
	}
}

func TestVerifySkew(t *testing.T) {
	now := time.Unix(1234567890, 0)
	cur := StepAt(now)
	for d := int64(-3); d <= 3; d++ {
		code := HOTP(rfcSecret, uint64(cur+d), 6)
		step, ok := Verify(rfcSecret, code, now)
		if want := d >= -1 && d <= 1; ok != want {
			t.Errorf("offset %d: ok=%v", d, ok)
		} else if ok && step != cur+d {
			t.Errorf("offset %d: step %d", d, step)
		}
	}
	code := Code(rfcSecret, now)
	if _, ok := Verify(rfcSecret, code[:3]+" "+code[3:], now); !ok {
		t.Error("spaces not tolerated")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef", "12345x"} {
		if _, ok := Verify(rfcSecret, bad, now); ok {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, ok := Verify([]byte("another secret 12345"), code, now); ok {
		t.Error("accepted code for another secret")
	}
}

func TestURIAndSecret(t *testing.T) {
	s := NewSecret()
	if len(s) != SecretLen || len(EncodeSecret(s)) != 32 || strings.Contains(EncodeSecret(s), "=") {
		t.Fatalf("secret %q", EncodeSecret(s))
	}
	u, err := url.Parse(URI("Daniel's Frame", "alice", s))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Scheme != "otpauth" || u.Host != "totp" || q.Get("secret") != EncodeSecret(s) || q.Get("issuer") != "Daniel's Frame" ||
		q.Get("digits") != "6" || q.Get("period") != "30" || q.Get("algorithm") != "SHA1" || u.Path != "/Daniel's Frame:alice" {
		t.Fatalf("uri %s", u)
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes := NewRecoveryCodes(RecoveryCodeCount)
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != 11 || c[5] != '-' || seen[c] {
			t.Fatalf("bad code %q", c)
		}
		seen[c] = true
		n := NormalizeRecoveryCode(strings.ToUpper(c))
		if n != strings.ReplaceAll(c, "-", "") {
			t.Fatalf("normalize %q → %q", c, n)
		}
	}
	if NormalizeRecoveryCode("abcde-fghjk") != "abcdefghjk" || NormalizeRecoveryCode("ABCDE FGHJK") != "abcdefghjk" {
		t.Fatal("normalize")
	}
	if NormalizeRecoveryCode("oiIlo-00000") != "01110"+"00000" {
		t.Fatal("look-alikes")
	}
	for _, bad := range []string{"", "abcd", "abcde-fghjkm", "abcde-fghj!", "123456"} {
		if NormalizeRecoveryCode(bad) != "" {
			t.Errorf("accepted %q", bad)
		}
	}
}
