package storage

import (
	"bytes"
	"context"
	"io"
	"math/rand/v2"
	"sync"
	"testing"
)

// memStorage is an in-memory Storage used to test wrappers in isolation.
type memStorage struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMem() *memStorage { return &memStorage{objects: map[string][]byte{}} }

func (m *memStorage) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, contentType string) error {
	if !ValidKey(key) {
		return ErrInvalidKey
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(body, size+1))
	if err != nil {
		return err
	}
	if int64(len(b)) != size {
		return io.ErrUnexpectedEOF
	}
	m.mu.Lock()
	m.objects[key] = b
	m.mu.Unlock()
	return nil
}

type memObject struct {
	*bytes.Reader
}

func (o memObject) Size() int64  { return o.Reader.Size() }
func (o memObject) Close() error { return nil }

func (m *memStorage) Open(ctx context.Context, key string) (Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objects[key]
	if !ok {
		return nil, ErrNotFound
	}
	return memObject{Reader: bytes.NewReader(b)}, nil
}

func (m *memStorage) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	delete(m.objects, key)
	m.mu.Unlock()
	return nil
}

func (m *memStorage) Stat(ctx context.Context, key string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objects[key]
	if !ok {
		return 0, ErrNotFound
	}
	return int64(len(b)), nil
}

// raw returns the stored bytes (for tamper tests).
func (m *memStorage) raw(key string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.objects[key]
}

func (m *memStorage) setRaw(key string, b []byte) {
	m.mu.Lock()
	m.objects[key] = b
	m.mu.Unlock()
}

// testSizes covers chunk boundaries of the encrypted format.
var testSizes = []int{0, 1, 65535, 65536, 65537, 3*65536 + 5}

func randomBytes(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

var testMasterKey = bytes.Repeat([]byte{0x42}, 32)

// exerciseStorage runs the common contract checks against s.
func exerciseStorage(t *testing.T, s Storage) {
	t.Helper()
	ctx := context.Background()
	for i, n := range testSizes {
		key := "obj-" + string(rune('a'+i))
		want := randomBytes(uint64(n)+1, n)
		if err := s.Put(ctx, key, bytes.NewReader(want), int64(n), "image/jpeg"); err != nil {
			t.Fatalf("Put(%d bytes): %v", n, err)
		}
		size, err := s.Stat(ctx, key)
		if err != nil || size != int64(n) {
			t.Fatalf("Stat(%d bytes) = %d, %v", n, size, err)
		}
		obj, err := s.Open(ctx, key)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if obj.Size() != int64(n) {
			t.Fatalf("Size = %d, want %d", obj.Size(), n)
		}
		got, err := io.ReadAll(obj)
		if err != nil {
			t.Fatalf("ReadAll(%d bytes): %v", n, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("round trip of %d bytes mismatched", n)
		}
		checkRandomAccess(t, obj, want, uint64(n))
		obj.Close()
		if err := s.Delete(ctx, key); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := s.Open(ctx, key); err != ErrNotFound {
			t.Fatalf("Open after delete = %v, want ErrNotFound", err)
		}
		if _, err := s.Stat(ctx, key); err != ErrNotFound {
			t.Fatalf("Stat after delete = %v, want ErrNotFound", err)
		}
		if err := s.Delete(ctx, key); err != nil {
			t.Fatalf("Delete of missing object = %v, want nil", err)
		}
	}
	// A body shorter than the declared size must fail.
	if err := s.Put(ctx, "short", bytes.NewReader([]byte("abc")), 10, ""); err == nil {
		t.Fatal("Put with short body succeeded")
	}
	// Invalid keys are rejected everywhere.
	for _, bad := range []string{"", "../x", "a/b", ".hidden", "a..b"} {
		if err := s.Put(ctx, bad, bytes.NewReader(nil), 0, ""); err != ErrInvalidKey {
			t.Fatalf("Put(%q) = %v, want ErrInvalidKey", bad, err)
		}
		if _, err := s.Open(ctx, bad); err != ErrInvalidKey {
			t.Fatalf("Open(%q) = %v, want ErrInvalidKey", bad, err)
		}
	}
}

// checkRandomAccess compares random Seek/Read patterns against want.
func checkRandomAccess(t *testing.T, obj Object, want []byte, seed uint64) {
	t.Helper()
	r := rand.New(rand.NewPCG(seed, 7))
	n := int64(len(want))
	for i := 0; i < 40; i++ {
		var off int64
		if n > 0 {
			off = r.Int64N(n + 1)
		}
		switch i % 3 {
		case 0:
			if _, err := obj.Seek(off, io.SeekStart); err != nil {
				t.Fatal(err)
			}
		case 1:
			if _, err := obj.Seek(off-n, io.SeekEnd); err != nil {
				t.Fatal(err)
			}
		case 2:
			cur, _ := obj.Seek(0, io.SeekCurrent)
			if _, err := obj.Seek(off-cur, io.SeekCurrent); err != nil {
				t.Fatal(err)
			}
		}
		l := r.IntN(150000) + 1
		buf := make([]byte, l)
		got, err := io.ReadFull(obj, buf)
		end := min(off+int64(l), n)
		if got != int(end-off) {
			t.Fatalf("read at %d len %d: got %d bytes (err %v), want %d", off, l, got, err, end-off)
		}
		if !bytes.Equal(buf[:got], want[off:end]) {
			t.Fatalf("read at %d len %d: bytes differ", off, l)
		}
	}
	if _, err := obj.Seek(-1, io.SeekStart); err == nil {
		t.Fatal("negative seek succeeded")
	}
}
