package auth

import (
	"context"
	"strings"
	"testing"
)

func TestHashVerify(t *testing.T) {
	ctx := context.Background()
	h, err := HashPassword(ctx, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("PHC = %s", h)
	}
	if err := VerifyPassword(ctx, "correct horse", h); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPassword(ctx, "correct horsf", h); err != ErrMismatch {
		t.Fatalf("mismatch: %v", err)
	}
	h2, _ := HashPassword(ctx, "correct horse")
	if h == h2 {
		t.Fatal("salt not random")
	}
	// Hashes made with other parameters still verify (params live in the PHC string).
	cheap, _ := hashWith(ctx, "pw", Params{MemoryKiB: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32})
	if err := VerifyPassword(ctx, "pw", cheap); err != nil {
		t.Fatal(err)
	}
}

func TestPHCRejectsTampering(t *testing.T) {
	ctx := context.Background()
	for _, bad := range []string{
		"", "plain", "$argon2i$v=19$m=19456,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=18$m=19456,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=999999999,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA", // memory bomb
		"$argon2id$v=19$m=19456,t=99,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=19456,t=2,p=0$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=19456,t=2$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=19456,t=2,p=1$!!$aGFzaGhhc2hoYXNoaGFzaA",
	} {
		if err := VerifyPassword(ctx, "pw", bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestTokens(t *testing.T) {
	tok, h := NewSessionToken()
	if len(tok) != 43 || !ValidTokenSyntax(tok) || len(h) != 32 || string(HashToken(tok)) != string(h) {
		t.Fatal("token")
	}
	for _, bad := range []string{"", "short", strings.Repeat("a", 42), strings.Repeat("a", 42) + "=", strings.Repeat("a", 42) + "/"} {
		if ValidTokenSyntax(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
	if p := RandomPassword(); len(p) != 16 {
		t.Fatal(p)
	}
}

func TestIdentifiers(t *testing.T) {
	if e, ok := NormalizeEmail("  Alice@Example.COM "); !ok || e != "alice@example.com" {
		t.Fatal(e)
	}
	for _, bad := range []string{"", "a@", "@b.c", "a b@c.d", "nodot@local"} {
		if _, ok := NormalizeEmail(bad); ok {
			t.Errorf("email %q accepted", bad)
		}
	}
	if p, ok := NormalizePhone("+34 (600) 111-222"); !ok || p != "+34600111222" {
		t.Fatal(p)
	}
	for _, bad := range []string{"600111222", "+0600111222", "+12345", "+1234567890123456", "+34abc"} {
		if _, ok := NormalizePhone(bad); ok {
			t.Errorf("phone %q accepted", bad)
		}
	}
	if !ValidPassword("12345678") || ValidPassword("1234567") || ValidPassword(strings.Repeat("x", 129)) || !ValidPassword(strings.Repeat("é", 128)) {
		t.Fatal("password length")
	}
	for u, want := range map[string]string{"alice": "", "a.b_c9": "", "al": "Must be at least 3 characters", "Alice": "Only letters, numbers, _ and . allowed",
		"_alice": "Must start with a letter", "alice_": "Must end with a letter or number", "a__b": "Can't contain consecutive . or _"} {
		if got := UsernameError(u); got != want {
			t.Errorf("UsernameError(%q) = %q", u, got)
		}
	}
	if _, ok := CleanDisplayName("a\x00b"); ok {
		t.Fatal("control char accepted")
	}
	if n, ok := CleanDisplayName("  Ana  "); !ok || n != "Ana" {
		t.Fatal(n)
	}
}

func BenchmarkHashPassword(b *testing.B) {
	for i := 0; i < b.N; i++ {
		HashPassword(context.Background(), "password123")
	}
}
