package storage

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"io"
	"testing"
)

func newEncMem(t testing.TB) (*memStorage, *Encrypted) {
	t.Helper()
	m := newMem()
	e, err := NewEncrypted(m, testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	return m, e
}

func TestEncryptedMemRoundTrip(t *testing.T) {
	_, e := newEncMem(t)
	exerciseStorage(t, e)
}

func TestNewEncryptedValidation(t *testing.T) {
	if _, err := NewEncrypted(newMem(), make([]byte, 16)); err == nil {
		t.Fatal("accepted 16-byte master key")
	}
	if _, err := NewEncrypted(nil, testMasterKey); err == nil {
		t.Fatal("accepted nil inner storage")
	}
}

func TestEncryptedSizes(t *testing.T) {
	for _, n := range []int64{0, 1, encChunkSize - 1, encChunkSize, encChunkSize + 1, 3*encChunkSize + 5, 1 << 30} {
		ct := encCiphertextSize(n)
		got, err := encPlaintextSize(ct)
		if err != nil || got != n {
			t.Fatalf("encPlaintextSize(encCiphertextSize(%d)) = %d, %v", n, got, err)
		}
	}
	// Impossible ciphertext lengths are rejected.
	for _, ct := range []int64{0, 7, 8, 23, 8 + encSealedSize + 5, 8 + encSealedSize + 16, 8 + 2*encSealedSize + 16} {
		if _, err := encPlaintextSize(ct); err == nil {
			t.Errorf("encPlaintextSize(%d) accepted an impossible length", ct)
		}
	}
}

// TestEncryptedFormat decrypts a stored object by hand, following the format
// documented in encrypted.go, so the documentation stays truthful.
func TestEncryptedFormat(t *testing.T) {
	m, e := newEncMem(t)
	plain := randomBytes(3, encChunkSize+10)
	if err := e.Put(context.Background(), "doc", bytes.NewReader(plain), int64(len(plain)), "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	raw := m.raw("doc")
	header := []byte{'F', 'R', 'M', '1', 1, 16, 0, 0}
	if !bytes.Equal(raw[:8], header) {
		t.Fatalf("header = %x", raw[:8])
	}
	k := hmacSHA256(testMasterKey, []byte("frame-storage-v1:doc"))
	block, _ := aes.NewCipher(k)
	gcm, _ := cipher.NewGCM(block)
	nonce0 := make([]byte, 12) // index 0, not final
	nonce1 := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1}
	c0 := raw[8 : 8+65536+16]
	c1 := raw[8+65536+16:]
	p0, err := gcm.Open(nil, nonce0, c0, header)
	if err != nil {
		t.Fatalf("chunk 0: %v", err)
	}
	p1, err := gcm.Open(nil, nonce1, c1, header)
	if err != nil {
		t.Fatalf("chunk 1: %v", err)
	}
	if !bytes.Equal(append(p0, p1...), plain) {
		t.Fatal("hand-decrypted plaintext differs")
	}
}

// readAllAt opens key and reads everything (or from off).
func readEncrypted(e *Encrypted, key string, off int64) ([]byte, error) {
	obj, err := e.Open(context.Background(), key)
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	if _, err := obj.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(obj)
}

func TestEncryptedTamper(t *testing.T) {
	plain := randomBytes(11, 3*encChunkSize+5)
	setup := func(t *testing.T) (*memStorage, *Encrypted, []byte) {
		m, e := newEncMem(t)
		if err := e.Put(context.Background(), "obj", bytes.NewReader(plain), int64(len(plain)), ""); err != nil {
			t.Fatal(err)
		}
		return m, e, bytes.Clone(m.raw("obj"))
	}
	expectCorrupt := func(t *testing.T, e *Encrypted, off int64) {
		t.Helper()
		got, err := readEncrypted(e, "obj", off)
		if !errors.Is(err, ErrCorrupt) {
			t.Fatalf("read at %d: err = %v, want ErrCorrupt", off, err)
		}
		// Bytes returned before the failure must come only from earlier,
		// authenticated chunks.
		if !bytes.Equal(got, plain[off:off+int64(len(got))]) {
			t.Fatal("returned unauthenticated plaintext")
		}
	}

	t.Run("flip byte in each chunk", func(t *testing.T) {
		for chunk := int64(0); chunk < 4; chunk++ {
			m, e, raw := setup(t)
			pos := encHeaderSize + chunk*encSealedSize + 100
			if chunk == 3 {
				pos = int64(len(raw)) - 3 // inside the last tag
			}
			raw[pos] ^= 0x01
			m.setRaw("obj", raw)
			expectCorrupt(t, e, chunk*encChunkSize)
		}
	})
	t.Run("flip header byte", func(t *testing.T) {
		m, e, raw := setup(t)
		raw[4] = 2 // unknown version
		m.setRaw("obj", raw)
		expectCorrupt(t, e, 0)
	})
	t.Run("truncate last chunk", func(t *testing.T) {
		m, e, raw := setup(t)
		m.setRaw("obj", raw[:len(raw)-1])
		if _, err := e.Open(context.Background(), "obj"); err == nil {
			// Length may still be self-consistent; then reads must fail.
			expectCorrupt(t, e, 3*encChunkSize)
		}
	})
	t.Run("drop whole last chunk", func(t *testing.T) {
		m, e, raw := setup(t)
		// Remaining object is 3 full chunks: consistent length, but chunk 2
		// was sealed as non-final.
		m.setRaw("obj", raw[:encHeaderSize+3*encSealedSize])
		expectCorrupt(t, e, 2*encChunkSize)
	})
	t.Run("swap chunks", func(t *testing.T) {
		m, e, raw := setup(t)
		c0 := bytes.Clone(raw[encHeaderSize : encHeaderSize+encSealedSize])
		c1 := bytes.Clone(raw[encHeaderSize+encSealedSize : encHeaderSize+2*encSealedSize])
		copy(raw[encHeaderSize:], c1)
		copy(raw[encHeaderSize+encSealedSize:], c0)
		m.setRaw("obj", raw)
		expectCorrupt(t, e, 0)
		expectCorrupt(t, e, encChunkSize)
	})
	t.Run("object moved to another key", func(t *testing.T) {
		m, e, raw := setup(t)
		m.setRaw("other", raw)
		obj, err := e.Open(context.Background(), "other")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadAll(obj); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("read of moved object = %v, want ErrCorrupt", err)
		}
	})
	t.Run("wrong master key", func(t *testing.T) {
		m, _, _ := setup(t)
		other, err := NewEncrypted(m, bytes.Repeat([]byte{0x24}, 32))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := readEncrypted(other, "obj", 0); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("read with wrong key = %v, want ErrCorrupt", err)
		}
	})
	t.Run("impossible length", func(t *testing.T) {
		m, e, _ := setup(t)
		m.setRaw("obj", []byte("short"))
		if _, err := e.Open(context.Background(), "obj"); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("Open = %v, want ErrCorrupt", err)
		}
		if _, err := e.Stat(context.Background(), "obj"); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("Stat = %v, want ErrCorrupt", err)
		}
	})
}

// The ciphertext view used by Put is a deterministic, seekable function of
// the plaintext.
func TestSealingReaderSeekable(t *testing.T) {
	_, e := newEncMem(t)
	plain := randomBytes(5, 2*encChunkSize+77)
	aead, _ := e.objectAEAD("k")
	mk := func() *sealingReader {
		return &sealingReader{aead: aead, src: bytes.NewReader(plain), plainSize: int64(len(plain)),
			ctSize: encCiphertextSize(int64(len(plain))), nChunks: encChunks(int64(len(plain))), cached: -1}
	}
	full, err := io.ReadAll(mk())
	if err != nil || int64(len(full)) != encCiphertextSize(int64(len(plain))) {
		t.Fatalf("ReadAll: %d bytes, %v", len(full), err)
	}
	checkRandomAccess(t, readSeekerObject{mk(), int64(len(full))}, full, 1)
}

type readSeekerObject struct {
	io.ReadSeeker
	size int64
}

func (r readSeekerObject) Size() int64  { return r.size }
func (r readSeekerObject) Close() error { return nil }

func FuzzEncryptedRoundTrip(f *testing.F) {
	f.Add([]byte("hello"), uint16(1), uint32(0))
	f.Add([]byte{}, uint16(0), uint32(3))
	f.Add(bytes.Repeat([]byte{0xAB}, 1000), uint16(200), uint32(65530))
	f.Fuzz(func(t *testing.T, seed []byte, repeat uint16, off uint32) {
		plain := bytes.Repeat(seed, int(repeat%300)+1)
		if len(seed) == 0 {
			plain = nil
		}
		_, e := newEncMem(t)
		ctx := context.Background()
		if err := e.Put(ctx, "fuzz", bytes.NewReader(plain), int64(len(plain)), ""); err != nil {
			t.Fatal(err)
		}
		got, err := readEncrypted(e, "fuzz", 0)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("round trip failed: %v", err)
		}
		start := int64(0)
		if len(plain) > 0 {
			start = int64(off) % int64(len(plain)+1)
		}
		got, err = readEncrypted(e, "fuzz", start)
		if err != nil || !bytes.Equal(got, plain[start:]) {
			t.Fatalf("read from %d failed: %v", start, err)
		}
	})
}

// Arbitrary stored bytes must never decrypt (and must not panic).
func FuzzEncryptedOpenGarbage(f *testing.F) {
	f.Add([]byte("FRM1\x01\x10\x00\x00garbage-garbage-garbage"), uint32(0))
	f.Add(make([]byte, 24), uint32(0))
	f.Fuzz(func(t *testing.T, stored []byte, off uint32) {
		m, e := newEncMem(t)
		m.setRaw("g", stored)
		obj, err := e.Open(context.Background(), "g")
		if err != nil {
			return
		}
		defer obj.Close()
		if obj.Size() > 0 {
			obj.Seek(int64(off)%obj.Size(), io.SeekStart)
		}
		got, err := io.ReadAll(obj)
		if err == nil && len(got) > 0 {
			t.Fatalf("garbage decrypted to %d bytes", len(got))
		}
	})
}
