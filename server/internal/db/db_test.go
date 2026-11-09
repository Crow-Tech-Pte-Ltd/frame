package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenMigrateBackup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	d, err := Open(ctx, filepath.Join(dir, "frame.db"))
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	d.W.QueryRow("PRAGMA journal_mode").Scan(&mode)
	var fk, bt int
	d.W.QueryRow("PRAGMA foreign_keys").Scan(&fk)
	d.R.QueryRow("PRAGMA busy_timeout").Scan(&bt)
	if st, err := os.Stat(filepath.Join(dir, "frame.db")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("db file mode: %v %v", st.Mode(), err)
	}
	if mode != "wal" || fk != 1 || bt != 10000 {
		t.Fatalf("pragmas: %s fk=%d busy=%d", mode, fk, bt)
	}
	if _, err := d.R.Exec("INSERT INTO meta(key, value) VALUES ('a','b')"); err == nil {
		t.Fatal("reader pool must be read-only")
	}
	var v int
	d.R.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&v)
	if v != SchemaVersion() || v < 1 {
		t.Fatalf("version %d", v)
	}
	if _, err := d.W.Exec("INSERT INTO sessions(id,user_id,token_hash,device_name,platform,created_at,last_seen_at) VALUES ('s','nouser',x'00','d','ios',0,0)"); err == nil {
		t.Fatal("foreign keys not enforced")
	}
	if err := d.Backup(ctx, filepath.Join(dir, "backup.db")); err != nil {
		t.Fatal(err)
	}
	d.Close()
	// Reopen is idempotent; backup is a valid database.
	for _, p := range []string{"frame.db", "backup.db"} {
		d2, err := Open(ctx, filepath.Join(dir, p))
		if err != nil {
			t.Fatal(p, err)
		}
		d2.Close()
	}
	if _, err := os.Stat(filepath.Join(dir, "backup.db")); err != nil {
		t.Fatal(err)
	}
}

func TestNewerSchemaRejected(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "frame.db")
	d, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	d.W.Exec("INSERT INTO schema_migrations(version, name, applied_at) VALUES (999, 'future', 0)")
	d.Close()
	if _, err := Open(ctx, p); err == nil {
		t.Fatal("opened a database from the future")
	}
}

func TestPlaceholders(t *testing.T) {
	if Placeholders(0) != "" || Placeholders(3) != "?,?,?" {
		t.Fatal(Placeholders(3))
	}
}
