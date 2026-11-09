package main

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/config"
	_ "github.com/Crow-Tech-Pte-Ltd/frame/server/internal/testutil"
)

func TestSecretsGeneratedOnceWithTightPermissions(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m1, s1, err := loadSecrets(dir, log)
	if err != nil || len(m1) != 32 || len(s1) != 32 || bytes.Equal(m1, s1) {
		t.Fatalf("%v", err)
	}
	st, _ := os.Stat(filepath.Join(dir, "secrets.json"))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	m2, s2, err := loadSecrets(dir, log)
	if err != nil || !bytes.Equal(m1, m2) || !bytes.Equal(s1, s2) {
		t.Fatal("secrets not stable across restarts")
	}
	os.Chmod(filepath.Join(dir, "secrets.json"), 0o644)
	loadSecrets(dir, log)
	if st, _ := os.Stat(filepath.Join(dir, "secrets.json")); st.Mode().Perm() != 0o600 {
		t.Fatal("permissions not repaired")
	}
	os.WriteFile(filepath.Join(dir, "secrets.json"), []byte(`{"media_key":"c2hvcnQ="}`), 0o600)
	if _, _, err := loadSecrets(dir, log); err == nil {
		t.Fatal("short keys accepted")
	}
}

func TestCLIParsing(t *testing.T) {
	rest, cfg := extractConfigFlag([]string{"user", "create", "--config", "/etc/frame.conf", "--email", "a@b.c"})
	if cfg != "/etc/frame.conf" || strings.Join(rest, " ") != "user create --email a@b.c" {
		t.Fatal(rest, cfg)
	}
	if _, cfg := extractConfigFlag([]string{"--config=/x"}); cfg != "/x" {
		t.Fatal(cfg)
	}
	if err := run([]string{"version"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"bogus"}); err == nil {
		t.Fatal("unknown command accepted")
	}
	if err := run([]string{"backup"}); err == nil {
		t.Fatal("backup without file accepted")
	}
}

func TestDevDefaultsRespectEnv(t *testing.T) {
	t.Setenv("FRAME_LISTEN", "0.0.0.0:8788")
	t.Setenv("FRAME_DATA_DIR", t.TempDir())
	t.Setenv("FRAME_PUSH_RELAY", "https://relay.example.com")
	c, err := loadConfig("", true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "0.0.0.0:8788" || !c.Dev || c.Registration != "open" || c.PushRelay != "https://relay.example.com" || c.Storage != "local" {
		t.Fatalf("%+v", c)
	}
	os.Unsetenv("FRAME_PUSH_RELAY")
	os.Unsetenv("FRAME_LISTEN")
	c, _ = loadConfig("", true)
	if c.PushRelay != "" || c.Listen != "0.0.0.0:8787" {
		t.Fatalf("dev defaults: %+v", c)
	}
	var out bytes.Buffer
	printDevBanner(&out, c)
	if !strings.HasSuffix(out.String(), "FRAME DEV READY http://0.0.0.0:8787\n") || !strings.Contains(out.String(), "alice@example.com") {
		t.Fatal(out.String())
	}
	_ = config.Keys
}

func TestAdminBaseURL(t *testing.T) {
	for _, c := range []struct {
		cfg  config.Config
		want string
	}{
		{config.Config{Listen: ":8080"}, "http://localhost:8080"},
		{config.Config{Listen: "127.0.0.1:9000", PublicURL: "https://frame.example.com"}, "https://frame.example.com"},
		{config.Config{Listen: ":8080", PublicURL: "https://frame.example.com", AdminListen: "127.0.0.1:8081"}, "http://127.0.0.1:8081"},
		{config.Config{AdminListen: ":8081"}, "http://localhost:8081"},
		{config.Config{AdminListen: "[::]:8081"}, "http://localhost:8081"},
	} {
		if got := adminBaseURL(&c.cfg); got != c.want {
			t.Errorf("%+v: %s, want %s", c.cfg, got, c.want)
		}
	}
}

func TestAnnounceSetup(t *testing.T) {
	t.Setenv("FRAME_DATA_DIR", t.TempDir())
	t.Setenv("FRAME_PUBLIC_URL", "https://frame.example.com")
	t.Setenv("FRAME_PUSH_RELAY", "")
	cfg, err := loadConfig("", false)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := t.Context()
	a, err := bootstrap(ctx, cfg, log, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.db.Close()
	var out bytes.Buffer
	if err := announceSetup(ctx, a, log, &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	i := strings.Index(s, "Create your admin account: https://frame.example.com/admin/setup#t=")
	if i < 0 {
		t.Fatalf("banner: %q", s)
	}
	// A second start prints a new link (the first one stops working).
	var out2 bytes.Buffer
	if err := announceSetup(ctx, a, log, &out2); err != nil || out2.String() == s {
		t.Fatalf("second banner: %v %q", err, out2.String())
	}
	// Once an admin exists nothing is printed.
	if _, err := a.db.W.Exec(`INSERT INTO users(id, display_name, created_at, updated_at, is_admin) VALUES ('01JC3V9Z4X8Q2M7T5R1N6K0B3H', 'A', 0, 0, 1)`); err != nil {
		t.Fatal(err)
	}
	var out3 bytes.Buffer
	if err := announceSetup(ctx, a, log, &out3); err != nil || out3.Len() != 0 {
		t.Fatalf("banner with an admin: %v %q", err, out3.String())
	}
}
