// Package db opens the SQLite database (WAL, one writer connection plus a
// read-only pool) and applies embedded, versioned migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver (pure Go)
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB bundles the single writer connection and the reader pool. All writes
// must go through W (ideally via Tx) so SQLite never sees competing writers.
type DB struct {
	W    *sql.DB
	R    *sql.DB
	Path string
}

// Querier is satisfied by *sql.DB and *sql.Tx.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func dsn(path string, readOnly bool) string {
	q := url.Values{}
	for _, p := range []string{
		"busy_timeout(10000)",
		"journal_mode(WAL)",
		"foreign_keys(1)",
		"synchronous(NORMAL)",
		"temp_store(MEMORY)",
		"cache_size(-16000)", // ~16 MiB page cache per connection
	} {
		q.Add("_pragma", p)
	}
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Set("_txlock", "immediate")
	}
	return "file:" + path + "?" + q.Encode()
}

// Open opens (creating if needed) the database at path and migrates it.
func Open(ctx context.Context, path string) (*DB, error) {
	w, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	w.SetConnMaxIdleTime(0)
	if err := w.PingContext(ctx); err != nil {
		w.Close()
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	if err := migrate(ctx, w); err != nil {
		w.Close()
		return nil, err
	}
	// The database holds password hashes and contact details: owner-only.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(path+suffix, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			w.Close()
			return nil, fmt.Errorf("db: chmod: %w", err)
		}
	}
	r, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		w.Close()
		return nil, err
	}
	n := max(4, runtime.NumCPU())
	r.SetMaxOpenConns(n)
	r.SetMaxIdleConns(n)
	r.SetConnMaxIdleTime(5 * time.Minute)
	if err := r.PingContext(ctx); err != nil {
		w.Close()
		r.Close()
		return nil, err
	}
	return &DB{W: w, R: r, Path: path}, nil
}

// Close closes both pools; it checkpoints the WAL on the writer first.
func (d *DB) Close() error {
	_, _ = d.W.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return errors.Join(d.R.Close(), d.W.Close())
}

// Tx runs fn inside a write transaction (BEGIN IMMEDIATE) on the writer.
func (d *DB) Tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Backup writes a consistent copy of the live database to dst (VACUUM INTO).
// dst must not exist: it is created owner-only (0600) first, so the copy
// (password hashes, e-mail addresses, phone numbers) is never readable by
// others, not even briefly under a permissive umask, and an existing file
// or symlink at dst is never written through.
func (d *DB) Backup(ctx context.Context, dst string) error {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("db: backup: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	if _, err := d.W.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		os.Remove(dst)
		return err
	}
	return nil
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		name := e.Name()
		num, _, ok := strings.Cut(name, "_")
		if !ok || !strings.HasSuffix(name, ".sql") {
			return nil, fmt.Errorf("db: bad migration file name %q", name)
		}
		v, err := strconv.Atoi(num)
		if err != nil {
			return nil, fmt.Errorf("db: bad migration file name %q", name)
		}
		b, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{version: v, name: name, sql: string(b)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i := range ms {
		if ms[i].version != i+1 {
			return nil, fmt.Errorf("db: migrations must be numbered 1..n without gaps (got %s)", ms[i].name)
		}
	}
	return ms, nil
}

// SchemaVersion returns the newest embedded migration version.
func SchemaVersion() int {
	ms, _ := loadMigrations()
	return len(ms)
}

func migrate(ctx context.Context, w *sql.DB) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	if _, err := w.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("db: %w", err)
	}
	var cur int
	if err := w.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&cur); err != nil {
		return fmt.Errorf("db: %w", err)
	}
	if cur > len(ms) {
		return fmt.Errorf("db: database schema version %d is newer than this binary (%d); upgrade frame-server", cur, len(ms))
	}
	for _, m := range ms[cur:] {
		tx, err := w.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("db: migration %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`,
			m.version, m.name, time.Now().UnixMilli()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("db: migration %s: %w", m.name, err)
		}
	}
	return nil
}

// Placeholders returns "?,?,?" for n parameters.
func Placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// Args converts a string slice to []any for variadic query arguments.
func Args(prefix []any, ss []string) []any {
	out := make([]any, 0, len(prefix)+len(ss))
	out = append(out, prefix...)
	for _, s := range ss {
		out = append(out, s)
	}
	return out
}
