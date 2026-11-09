package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Local stores objects as files under a directory, sharded two levels deep
// by the first two bytes of sha256(key): <dir>/<hh>/<hh>/<key>. Writes are
// atomic (temp file + fsync + rename). Directories are 0700, files 0600.
type Local struct {
	dir string
}

var _ Storage = (*Local)(nil)

// NewLocal returns a Local store rooted at dir, creating it (0700) if needed.
func NewLocal(dir string) (*Local, error) {
	if dir == "" {
		return nil, errors.New("storage: local directory is empty")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("storage: local directory: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("storage: create local directory: %w", err)
	}
	return &Local{dir: abs}, nil
}

// paths returns the shard directory and final file path for key.
func (l *Local) paths(key string) (dir, file string, err error) {
	if !ValidKey(key) {
		return "", "", ErrInvalidKey
	}
	sum := sha256.Sum256([]byte(key))
	h := hex.EncodeToString(sum[:2])
	dir = filepath.Join(l.dir, h[0:2], h[2:4])
	return dir, filepath.Join(dir, key), nil
}

// Put writes body atomically.
func (l *Local) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, contentType string) error {
	dir, file, err := l.paths(key)
	if err != nil {
		return err
	}
	if size < 0 {
		return errors.New("storage: negative size")
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("storage: rewind body: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("storage: create shard directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-"+key+"-*")
	if err != nil {
		return fmt.Errorf("storage: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	// Copy exactly size bytes; reading one extra byte detects a longer body.
	n, err := io.Copy(tmp, io.LimitReader(ctxReader{ctx, body}, size+1))
	if err != nil {
		return fmt.Errorf("storage: write %s: %w", key, err)
	}
	if n != size {
		return fmt.Errorf("storage: write %s: body is %d bytes, want %d", key, n, size)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("storage: chmod: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("storage: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("storage: close: %w", err)
	}
	if err := os.Rename(tmpName, file); err != nil {
		return fmt.Errorf("storage: rename: %w", err)
	}
	committed = true
	syncDir(dir) // best effort: make the rename durable
	return nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}

// localObject is an open file.
type localObject struct {
	*os.File
	size int64
}

func (o *localObject) Size() int64 { return o.size }

// Open opens key for reading.
func (l *Local) Open(ctx context.Context, key string) (Object, error) {
	_, file, err := l.paths(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("storage: open %s: %w", key, err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("storage: stat %s: %w", key, err)
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("storage: %s is not a regular file", key)
	}
	return &localObject{File: f, size: st.Size()}, nil
}

// Delete removes key; a missing object is not an error.
func (l *Local) Delete(ctx context.Context, key string) error {
	_, file, err := l.paths(key)
	if err != nil {
		return err
	}
	if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("storage: delete %s: %w", key, err)
	}
	return nil
}

// Stat returns the size of key.
func (l *Local) Stat(ctx context.Context, key string) (int64, error) {
	_, file, err := l.paths(key)
	if err != nil {
		return 0, err
	}
	st, err := os.Stat(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("storage: stat %s: %w", key, err)
	}
	return st.Size(), nil
}
