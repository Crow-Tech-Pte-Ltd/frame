//go:build unix

package db

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The backup holds password hashes, e-mail addresses and phone numbers. It
// must be owner-only from the moment it exists — not created with the
// process umask (often 0644) and fixed up afterwards — and must never write
// through a file or symlink that is already there.
func TestBackupIsOwnerOnlyAndExclusive(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	dir := t.TempDir()
	d, err := Open(context.Background(), filepath.Join(dir, "frame.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	dst := filepath.Join(dir, "backup.db")
	if err := d.Backup(context.Background(), dst); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode %v, want 0600", st.Mode().Perm())
	}
	// An existing path (here: a symlink planted by someone else) is refused.
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.db")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	if err := d.Backup(context.Background(), link); err == nil {
		t.Fatal("backup wrote through an existing symlink")
	}
	if b, _ := os.ReadFile(victim); len(b) != 0 {
		t.Fatal("symlink target was overwritten")
	}
}
