package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidKey(t *testing.T) {
	good := []string{"a", "Z", "0", "01JC3V9Z4X8Q2M7T5R1N6K0B3H", "a.b", "a_b-c.jpg", "a" + strings.Repeat("b", 127)}
	bad := []string{"", ".", "..", ".a", "_a", "-a", "a/b", "/a", "a\\b", "a..b", "a b", "a\x00", "é",
		"a" + strings.Repeat("b", 128), "a%2F", "a:b", "a?b"}
	for _, k := range good {
		if !ValidKey(k) {
			t.Errorf("ValidKey(%q) = false, want true", k)
		}
	}
	for _, k := range bad {
		if ValidKey(k) {
			t.Errorf("ValidKey(%q) = true, want false", k)
		}
	}
}

func TestLocalRoundTrip(t *testing.T) {
	s, err := NewLocal(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatal(err)
	}
	exerciseStorage(t, s)
}

func TestLocalEncryptedRoundTrip(t *testing.T) {
	l, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEncrypted(l, testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	exerciseStorage(t, e)
}

func TestLocalLayoutAndPermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	s, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	key := "01JC3V9Z4X8Q2M7T5R1N6K0B3H"
	if err := s.Put(context.Background(), key, bytes.NewReader([]byte("hello")), 5, "text/plain"); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(key))
	h := hex.EncodeToString(sum[:2])
	file := filepath.Join(root, h[:2], h[2:4], key)
	st, err := os.Stat(file)
	if err != nil {
		t.Fatalf("expected sharded file at %s: %v", file, err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}
	for _, d := range []string{root, filepath.Join(root, h[:2]), filepath.Join(root, h[:2], h[2:4])} {
		st, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if perm := st.Mode().Perm(); perm != 0o700 {
			t.Errorf("dir %s mode = %o, want 700", d, perm)
		}
	}
	// No temp files are left behind.
	entries, _ := os.ReadDir(filepath.Dir(file))
	if len(entries) != 1 {
		t.Errorf("shard dir has %d entries, want 1", len(entries))
	}
	// A failed Put leaves nothing behind either.
	if err := s.Put(context.Background(), "short", bytes.NewReader([]byte("ab")), 5, ""); err == nil {
		t.Fatal("short Put succeeded")
	}
	if _, err := s.Stat(context.Background(), "short"); err != ErrNotFound {
		t.Fatalf("short object exists: %v", err)
	}
}

func TestLocalContextCancelled(t *testing.T) {
	s, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Put(ctx, "k", bytes.NewReader([]byte("abc")), 3, ""); err == nil {
		t.Fatal("Put with cancelled context succeeded")
	}
}

func FuzzValidKey(f *testing.F) {
	for _, s := range []string{"a", "01JC3V9Z4X8Q2M7T5R1N6K0B3H", "../etc/passwd", "a/b", "a..b", ".x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, key string) {
		if !ValidKey(key) {
			return
		}
		if strings.ContainsAny(key, "/\\\x00") || strings.Contains(key, "..") || key[0] == '.' {
			t.Fatalf("ValidKey accepted unsafe key %q", key)
		}
		if filepath.Base(key) != key || filepath.Clean(key) != key {
			t.Fatalf("ValidKey accepted key that is not a plain file name: %q", key)
		}
		if awsURIEncode(key, false) != key {
			t.Fatalf("ValidKey accepted key needing URI encoding: %q", key)
		}
	})
}
