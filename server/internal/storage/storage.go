// Package storage stores media blobs. It has two backends — Local (sharded
// files under the data directory) and S3 (any S3-compatible bucket, signed
// with a hand-written AWS Signature V4 client) — plus Encrypted, a wrapper
// that seals objects with chunked AES-256-GCM so a leaked bucket reveals
// nothing while ranged reads still work.
//
// Keys are opaque, flat identifiers (media IDs). Objects are write-once: a key
// is never rewritten with different content.
package storage

import (
	"context"
	"errors"
	"io"
	"strings"
)

// ErrNotFound is returned when an object does not exist.
var ErrNotFound = errors.New("storage: object not found")

// ErrInvalidKey is returned for keys that fail ValidKey.
var ErrInvalidKey = errors.New("storage: invalid key")

// Object is an open stored object. Reads may be lazy (S3 issues ranged GETs
// on Read after Seek). It is safe to pass to http.ServeContent.
type Object interface {
	io.ReadSeekCloser
	// Size is the object's length in bytes.
	Size() int64
}

// Storage is a blob store.
type Storage interface {
	// Put stores body (exactly size bytes, read from offset 0) under key.
	// Objects are write-once (never overwritten with different content).
	Put(ctx context.Context, key string, body io.ReadSeeker, size int64, contentType string) error
	// Open opens key for reading. It returns ErrNotFound if it is missing.
	Open(ctx context.Context, key string) (Object, error)
	// Delete removes key. It returns nil if the object is already missing.
	Delete(ctx context.Context, key string) error
	// Stat returns the size of key, or ErrNotFound if it is missing.
	Stat(ctx context.Context, key string) (int64, error)
}

// maxKeyLen bounds key length (one leading char + up to 127 more).
const maxKeyLen = 128

// ValidKey reports whether key is acceptable to every backend:
// ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ and it must not contain "..".
// In particular keys never contain slashes, so they cannot escape a
// directory or a bucket prefix.
func ValidKey(key string) bool {
	if len(key) == 0 || len(key) > maxKeyLen {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case (c == '.' || c == '_' || c == '-') && i > 0:
		default:
			return false
		}
	}
	return !strings.Contains(key, "..")
}

// ctxReader aborts a copy when ctx is cancelled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
