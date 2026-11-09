package storage

// Encrypted-at-rest object format (version 1)
//
// Encrypted wraps another Storage and seals every object with AES-256-GCM in
// fixed-size chunks, so that ranged reads (video seeking, http.ServeContent)
// only fetch and decrypt the chunks they touch.
//
//	object  = header || chunk_0 || chunk_1 || … || chunk_{n-1}
//	header  = "FRM1" || 0x01 (version) || 0x10 (log2 chunk size = 16) || 0x00 0x00   (8 bytes)
//	chunk_i = AES-256-GCM-Seal(K_obj, nonce_i, plaintext_i, aad = header)          (len(plaintext_i) + 16 bytes)
//
//   - Plaintext is split into 64 KiB (65536-byte) chunks; only the last chunk
//     may be shorter. An empty object is a single empty final chunk, so
//     n = max(1, ceil(plaintext_len / 65536)).
//   - K_obj = HMAC-SHA256(master_key, "frame-storage-v1:" || storage_key) — a
//     distinct AES-256 key per object, derived from the 32-byte master key in
//     secrets.json and the object's storage key (its media ID).
//   - nonce_i (12 bytes) = 0x00 × 7 || uint32_be(i) || final_flag, where
//     final_flag is 0x01 for the last chunk and 0x00 otherwise (the STREAM
//     construction): chunks cannot be reordered, dropped or the object
//     truncated/extended without failing authentication.
//   - The header is the AAD of every chunk, binding the format version and
//     chunk size.
//   - Sizes: ciphertext_len = 8 + plaintext_len + 16·n. The plaintext length
//     is recovered from the stored object length alone.
//
// Nonces repeat only if the same storage key were sealed twice with different
// content; keys are fresh random IDs and objects are write-once, so a
// (K_obj, nonce) pair is never reused. Losing the master key loses the media.

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	encChunkLog2  = 16
	encChunkSize  = 1 << encChunkLog2 // plaintext bytes per chunk
	encTagSize    = 16
	encHeaderSize = 8
	encSealedSize = encChunkSize + encTagSize // ciphertext bytes per full chunk
	encMaxChunks  = 1 << 32                   // chunk index is a uint32
	encKeyContext = "frame-storage-v1:"
)

var encHeader = [encHeaderSize]byte{'F', 'R', 'M', '1', 0x01, encChunkLog2, 0x00, 0x00}

// ErrCorrupt is returned when an encrypted object fails authentication or has
// an impossible size. It is never accompanied by plaintext.
var ErrCorrupt = errors.New("storage: encrypted object is corrupt or was encrypted with a different key")

// Encrypted is a Storage that encrypts objects before handing them to inner.
type Encrypted struct {
	inner  Storage
	master []byte
}

var _ Storage = (*Encrypted)(nil)

// NewEncrypted wraps inner; masterKey must be 32 bytes.
func NewEncrypted(inner Storage, masterKey []byte) (*Encrypted, error) {
	if inner == nil {
		return nil, errors.New("storage: nil inner storage")
	}
	if len(masterKey) != 32 {
		return nil, errors.New("storage: encryption master key must be 32 bytes")
	}
	return &Encrypted{inner: inner, master: bytes.Clone(masterKey)}, nil
}

// objectAEAD derives the per-object AES-256-GCM instance.
func (e *Encrypted) objectAEAD(key string) (cipher.AEAD, error) {
	k := hmacSHA256(e.master, []byte(encKeyContext+key))
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// encChunks is the number of chunks for a plaintext length.
func encChunks(plain int64) int64 {
	if plain == 0 {
		return 1
	}
	return (plain + encChunkSize - 1) / encChunkSize
}

// encCiphertextSize is the stored size for a plaintext length.
func encCiphertextSize(plain int64) int64 {
	return encHeaderSize + plain + encChunks(plain)*encTagSize
}

// encPlaintextSize inverts encCiphertextSize, rejecting impossible lengths.
func encPlaintextSize(ct int64) (int64, error) {
	body := ct - encHeaderSize
	if body < encTagSize {
		return 0, ErrCorrupt
	}
	n := (body + encSealedSize - 1) / encSealedSize
	plain := body - n*encTagSize
	if plain < 0 || encChunks(plain) != n || n > encMaxChunks {
		return 0, ErrCorrupt
	}
	return plain, nil
}

// chunkNonce is 7 zero bytes || uint32_be(idx) || final flag.
func chunkNonce(idx int64, final bool) []byte {
	var n [12]byte
	binary.BigEndian.PutUint32(n[7:11], uint32(idx))
	if final {
		n[11] = 1
	}
	return n[:]
}

// Put encrypts body while streaming it to inner. The ciphertext is exposed as
// a seekable view computed on demand from the plaintext, so inner backends
// can hash it and re-read it without buffering the object.
func (e *Encrypted) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, contentType string) error {
	if !ValidKey(key) {
		return ErrInvalidKey
	}
	if size < 0 {
		return errors.New("storage: negative size")
	}
	if encChunks(size) > encMaxChunks {
		return errors.New("storage: object too large to encrypt")
	}
	aead, err := e.objectAEAD(key)
	if err != nil {
		return err
	}
	view := &sealingReader{
		aead:      aead,
		src:       body,
		plainSize: size,
		ctSize:    encCiphertextSize(size),
		nChunks:   encChunks(size),
		cached:    -1,
	}
	// The real content type is deliberately not passed down: the bucket only
	// ever sees opaque ciphertext.
	return e.inner.Put(ctx, key, view, view.ctSize, "application/octet-stream")
}

// sealingReader is a read-only, seekable view of the ciphertext of src.
type sealingReader struct {
	aead      cipher.AEAD
	src       io.ReadSeeker
	plainSize int64
	ctSize    int64
	nChunks   int64

	pos    int64  // ciphertext offset
	cached int64  // index of the chunk held in sealed (-1 = none)
	sealed []byte // sealed chunk: ciphertext || tag
	plain  []byte // scratch plaintext buffer
}

func (s *sealingReader) Read(p []byte) (int, error) {
	if s.pos >= s.ctSize {
		return 0, io.EOF
	}
	if s.pos < encHeaderSize {
		n := copy(p, encHeader[s.pos:])
		s.pos += int64(n)
		return n, nil
	}
	off := s.pos - encHeaderSize
	idx := off / encSealedSize
	if err := s.seal(idx); err != nil {
		return 0, err
	}
	n := copy(p, s.sealed[off-idx*encSealedSize:])
	s.pos += int64(n)
	return n, nil
}

// seal makes s.sealed hold chunk idx.
func (s *sealingReader) seal(idx int64) error {
	if s.cached == idx {
		return nil
	}
	start := idx * encChunkSize
	n := min(int64(encChunkSize), s.plainSize-start)
	if s.plain == nil {
		s.plain = make([]byte, encChunkSize)
		s.sealed = make([]byte, 0, encSealedSize)
	}
	if _, err := s.src.Seek(start, io.SeekStart); err != nil {
		return fmt.Errorf("storage: seek plaintext: %w", err)
	}
	if _, err := io.ReadFull(s.src, s.plain[:n]); err != nil {
		return fmt.Errorf("storage: read plaintext chunk %d: %w", idx, err)
	}
	s.sealed = s.aead.Seal(s.sealed[:0], chunkNonce(idx, idx == s.nChunks-1), s.plain[:n], encHeader[:])
	s.cached = idx
	return nil
}

func (s *sealingReader) Seek(offset int64, whence int) (int64, error) {
	abs, err := seekPos(s.pos, s.ctSize, offset, whence)
	if err != nil {
		return 0, err
	}
	s.pos = abs
	return abs, nil
}

func seekPos(cur, size, offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = cur + offset
	case io.SeekEnd:
		abs = size + offset
	default:
		return 0, errors.New("storage: invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("storage: negative position")
	}
	return abs, nil
}

// Open returns a decrypting view of key. Only the chunks that are read are
// fetched and authenticated.
func (e *Encrypted) Open(ctx context.Context, key string) (Object, error) {
	if !ValidKey(key) {
		return nil, ErrInvalidKey
	}
	aead, err := e.objectAEAD(key)
	if err != nil {
		return nil, err
	}
	inner, err := e.inner.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	plain, err := encPlaintextSize(inner.Size())
	if err != nil {
		inner.Close()
		return nil, err
	}
	return &openingReader{
		inner:     inner,
		aead:      aead,
		plainSize: plain,
		ctSize:    inner.Size(),
		nChunks:   encChunks(plain),
		cached:    -1,
		innerPos:  -1,
	}, nil
}

// openingReader decrypts an encrypted object chunk by chunk.
type openingReader struct {
	inner     Object
	aead      cipher.AEAD
	plainSize int64
	ctSize    int64
	nChunks   int64

	pos      int64  // plaintext offset
	cached   int64  // chunk index held in plain (-1 = none)
	plain    []byte // decrypted chunk
	buf      []byte // ciphertext scratch (header + sealed chunk)
	innerPos int64  // known offset of inner's read position (-1 = unknown)
	failed   error  // sticky authentication failure
}

func (o *openingReader) Size() int64 { return o.plainSize }

func (o *openingReader) Read(p []byte) (int, error) {
	if o.failed != nil {
		return 0, o.failed
	}
	if o.pos >= o.plainSize {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	idx := o.pos / encChunkSize
	if err := o.load(idx); err != nil {
		return 0, err
	}
	n := copy(p, o.plain[o.pos-idx*encChunkSize:])
	o.pos += int64(n)
	return n, nil
}

// load fetches, authenticates and decrypts chunk idx into o.plain.
func (o *openingReader) load(idx int64) error {
	if o.cached == idx {
		return nil
	}
	o.cached = -1
	ctOff := encHeaderSize + idx*encSealedSize
	ctLen := min(int64(encSealedSize), o.ctSize-ctOff)
	start := ctOff
	if idx == 0 {
		// Read the header together with chunk 0 so a sequential read issues a
		// single request; it is checked for a clearer error on format mismatch.
		start = 0
	}
	total := ctOff + ctLen - start
	if o.buf == nil {
		o.buf = make([]byte, encHeaderSize+encSealedSize)
		o.plain = make([]byte, 0, encChunkSize)
	}
	buf := o.buf[:total]
	if o.innerPos != start {
		if _, err := o.inner.Seek(start, io.SeekStart); err != nil {
			o.innerPos = -1
			return fmt.Errorf("storage: seek: %w", err)
		}
	}
	if _, err := io.ReadFull(o.inner, buf); err != nil {
		o.innerPos = -1
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			o.failed = ErrCorrupt
			return ErrCorrupt
		}
		return fmt.Errorf("storage: read encrypted chunk %d: %w", idx, err)
	}
	o.innerPos = start + total
	sealed := buf
	if idx == 0 {
		if !bytes.Equal(buf[:encHeaderSize], encHeader[:]) {
			o.failed = ErrCorrupt
			return ErrCorrupt
		}
		sealed = buf[encHeaderSize:]
	}
	plain, err := o.aead.Open(o.plain[:0], chunkNonce(idx, idx == o.nChunks-1), sealed, encHeader[:])
	if err != nil {
		o.failed = ErrCorrupt
		return ErrCorrupt
	}
	o.plain = plain
	o.cached = idx
	return nil
}

func (o *openingReader) Seek(offset int64, whence int) (int64, error) {
	abs, err := seekPos(o.pos, o.plainSize, offset, whence)
	if err != nil {
		return 0, err
	}
	o.pos = abs
	return abs, nil
}

func (o *openingReader) Close() error { return o.inner.Close() }

// Delete removes key from inner.
func (e *Encrypted) Delete(ctx context.Context, key string) error {
	if !ValidKey(key) {
		return ErrInvalidKey
	}
	return e.inner.Delete(ctx, key)
}

// Stat returns the plaintext size of key.
func (e *Encrypted) Stat(ctx context.Context, key string) (int64, error) {
	if !ValidKey(key) {
		return 0, ErrInvalidKey
	}
	ct, err := e.inner.Stat(ctx, key)
	if err != nil {
		return 0, err
	}
	return encPlaintextSize(ct)
}
