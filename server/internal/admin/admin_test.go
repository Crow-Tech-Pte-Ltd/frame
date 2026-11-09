package admin

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/api"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/auth"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/config"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/db"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/storage"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

func newEnv(t *testing.T) (*Env, *bytes.Buffer) {
	auth.DefaultParams = auth.Params{MemoryKiB: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}
	dir := t.TempDir()
	cfg, _ := config.Load(config.Source{Overrides: map[string]string{"FRAME_DATA_DIR": dir, "FRAME_PUSH_RELAY": ""}})
	d, err := db.Open(context.Background(), filepath.Join(dir, "frame.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	st, _ := storage.NewLocal(filepath.Join(dir, "media"))
	srv, err := api.New(api.Deps{Config: cfg, DB: d, Storage: st, MediaKey: bytes.Repeat([]byte{1}, 32), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	return &Env{DB: d, API: srv, PublicURL: "https://frame.example.com", InviteBase: cfg.InviteBase,
		AdminURL: "https://frame.example.com", Out: out}, out
}

func TestAdminCommands(t *testing.T) {
	ctx := context.Background()
	env, out := newEnv(t)
	run := func(args ...string) string {
		t.Helper()
		out.Reset()
		if err := Run(ctx, env, args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out.String()
	}
	created := run("user", "create", "--email", "Admin@Example.com", "--name", "Admin", "--username", "admin")
	pw := strings.TrimSpace(created[strings.Index(created, "one-time password: ")+len("one-time password: "):])
	u, err := store.UserBy(ctx, env.DB.R, "email", "admin@example.com")
	if err != nil || *u.Username != "admin" || auth.VerifyPassword(ctx, pw, *u.PasswordHash) != nil {
		t.Fatalf("created user: %+v %v", u, err)
	}
	run("user", "create", "--phone", "+34600111222")
	if err := Run(ctx, env, []string{"user", "create", "--email", "admin@example.com"}); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := Run(ctx, env, []string{"user", "create"}); err == nil {
		t.Fatal("missing identifier accepted")
	}
	list := run("user", "list")
	if !strings.Contains(list, "admin@example.com") || !strings.Contains(list, "+34600111222") {
		t.Fatal(list)
	}
	reset := run("user", "reset-password", "admin")
	pw2 := strings.TrimSpace(reset[strings.Index(reset, "one-time password: ")+len("one-time password: "):])
	u, _ = store.UserBy(ctx, env.DB.R, "email", "admin@example.com")
	if pw2 == pw || auth.VerifyPassword(ctx, pw2, *u.PasswordHash) != nil {
		t.Fatal("reset failed")
	}
	run("user", "reset-password", "+34600111222")
	inv := run("invite", "create", "admin")
	if !strings.HasPrefix(inv, "https://frame.crow.sg/i#s=https%3A%2F%2Fframe.example.com&c=") {
		t.Fatal(inv)
	}
	run("user", "delete", "admin")
	if _, err := store.UserBy(ctx, env.DB.R, "email", "admin@example.com"); err != store.ErrNotFound {
		t.Fatal("not deleted")
	}
	for _, bad := range [][]string{{"user", "delete", "nobody"}, {"user"}, {"frobnicate", "x"}, {"invite", "create", "ghost"}} {
		if err := Run(ctx, env, bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestAdminPanelCommands(t *testing.T) {
	ctx := context.Background()
	env, out := newEnv(t)
	run := func(args ...string) string {
		t.Helper()
		out.Reset()
		if err := Run(ctx, env, args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out.String()
	}
	// No admin yet: setup links (each replaces the previous one).
	link := run("setup-link")
	if !strings.HasPrefix(link, "Create your admin account: https://frame.example.com/admin/setup#t=") {
		t.Fatal(link)
	}
	tok := strings.Fields(link[strings.Index(link, "#t=")+3:])[0]
	if len(tok) != 43 {
		t.Fatalf("token %q", tok)
	}
	run("user", "create", "--email", "boss@example.com", "--username", "boss", "--admin")
	if err := Run(ctx, env, []string{"setup-link"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("setup-link with an admin: %v", err)
	}
	run("user", "create", "--email", "pal@example.com", "--username", "pal")
	if err := Run(ctx, env, []string{"user", "demote", "boss"}); err == nil || !strings.Contains(err.Error(), "last admin") {
		t.Fatalf("demoted the last admin: %v", err)
	}
	if err := Run(ctx, env, []string{"user", "delete", "boss"}); err == nil || !strings.Contains(err.Error(), "last admin") {
		t.Fatalf("deleted the last admin: %v", err)
	}
	run("user", "promote", "pal")
	list := run("user", "list")
	if strings.Count(list, " yes ") != 2 || !strings.Contains(list, "ADMIN") {
		t.Fatal(list)
	}
	run("user", "demote", "boss")
	if u, _ := store.UserBy(ctx, env.DB.R, "username", "boss"); u.IsAdmin {
		t.Fatal("demote failed")
	}
	if out := run("user", "promote", "pal"); !strings.Contains(out, "already") {
		t.Fatal(out)
	}
	// reset-2fa clears TOTP state.
	pal, _ := store.UserBy(ctx, env.DB.R, "username", "pal")
	env.DB.W.Exec(`INSERT INTO admin_totp(user_id, secret_enc, enabled_at) VALUES (?, x'00', 1)`, pal.ID)
	env.DB.W.Exec(`INSERT INTO admin_recovery_codes(user_id, code_hash) VALUES (?, x'01')`, pal.ID)
	run("user", "reset-2fa", "pal")
	var n int
	env.DB.R.QueryRow(`SELECT (SELECT COUNT(*) FROM admin_totp) + (SELECT COUNT(*) FROM admin_recovery_codes)`).Scan(&n)
	if n != 0 {
		t.Fatal("2fa not reset")
	}
	// Server invites.
	inv := run("invite", "server", "--uses", "3", "--days", "0", "--note", "for the band")
	if !strings.HasPrefix(inv, "https://frame.crow.sg/i#s=https%3A%2F%2Fframe.example.com&c=") || !strings.Contains(inv, "3 use(s), never expires") {
		t.Fatal(inv)
	}
	for _, bad := range [][]string{{"invite", "server", "--uses", "0"}, {"invite", "server", "--days", "400"}, {"user", "promote"}, {"user", "reset-2fa", "ghost"}} {
		if err := Run(ctx, env, bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	// CLI actions are audited as "CLI".
	var cli int
	env.DB.R.QueryRow(`SELECT COUNT(*) FROM admin_audit WHERE actor_label = 'CLI'`).Scan(&cli)
	if cli < 6 {
		t.Fatalf("audited CLI actions: %d", cli)
	}
	env.AdminOff = true
	if err := Run(ctx, env, []string{"setup-link"}); err == nil {
		t.Fatal("setup-link with the panel off")
	}
}

// A session that was stolen (or a phone left unlocked) can link the
// attacker's Apple/Google account; changing the password or revoking
// sessions doesn't remove that. `admin user unlink` does, and signs the
// account out everywhere.
func TestUnlinkIdentity(t *testing.T) {
	ctx := context.Background()
	env, out := newEnv(t)
	if err := Run(ctx, env, []string{"user", "create", "--email", "vic@example.com", "--username", "vic"}); err != nil {
		t.Fatal(err)
	}
	u, _ := store.UserBy(ctx, env.DB.R, "username", "vic")
	if err := store.LinkSub(ctx, env.DB.W, u.ID, "apple", "attacker-sub", 1); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertSession(ctx, env.DB.W, &store.Session{ID: "01JC3V9Z4X8Q2M7T5R1N6K0B3H", UserID: u.ID, DeviceName: "x", Platform: "ios"}, []byte("h")); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, env, []string{"user", "unlink", "vic", "facebook"}); err == nil {
		t.Fatal("unknown provider accepted")
	}
	out.Reset()
	if err := Run(ctx, env, []string{"user", "unlink", "vic@example.com", "apple"}); err != nil {
		t.Fatal(err)
	}
	u, _ = store.UserBy(ctx, env.DB.R, "username", "vic")
	if u.AppleSub != nil {
		t.Fatal("apple identity still linked")
	}
	if _, err := store.UserBy(ctx, env.DB.R, "apple_sub", "attacker-sub"); err != store.ErrNotFound {
		t.Fatal("attacker's apple sub still signs in")
	}
	if list, _ := store.ListSessions(ctx, env.DB.R, u.ID); len(list) != 0 || !strings.Contains(out.String(), "1 sessions signed out") {
		t.Fatalf("sessions not revoked: %d, %q", len(list), out.String())
	}
	var n int
	env.DB.R.QueryRow(`SELECT COUNT(*) FROM admin_audit WHERE action = 'user.unlink' AND actor_label = 'CLI'`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit entries: %d", n)
	}
	if err := Run(ctx, env, []string{"user", "unlink", "vic", "apple"}); err == nil {
		t.Fatal("unlinking a missing identity succeeded")
	}
	// Never remove an account's only way to sign in.
	if _, err := env.DB.W.Exec(`UPDATE users SET password_hash = NULL, google_sub = 'own-sub' WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, env, []string{"user", "unlink", "vic", "google"}); err == nil || !strings.Contains(err.Error(), "only way") {
		t.Fatalf("removed the only sign-in method: %v", err)
	}
}
