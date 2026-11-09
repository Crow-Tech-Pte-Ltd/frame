// Command frame-server is the self-hosted Frame server.
//
//	frame-server [serve] [--config FILE]   run the server (default)
//	frame-server dev                       dev server with seeded demo data
//	frame-server admin …                   user/invite administration
//	frame-server backup <file.db>          online, consistent SQLite backup
//	frame-server version
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/admin"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/api"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/config"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/db"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/devseed"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ids"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/storage"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "frame-server:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	configPath := fs.String("config", "", "path to a KEY=value config file")
	// Admin subcommands carry their own flags; only peel --config off.
	rest := args
	if cmd != "admin" {
		if err := fs.Parse(args); err != nil {
			return err
		}
		rest = fs.Args()
	} else {
		rest, *configPath = extractConfigFlag(args)
	}

	switch cmd {
	case "version":
		fmt.Println("frame-server", version)
		return nil
	case "serve":
		return serve(*configPath, false)
	case "dev":
		return serve(*configPath, true)
	case "backup":
		if len(rest) != 1 {
			return errors.New("usage: frame-server backup <file.db>")
		}
		return backup(*configPath, rest[0])
	case "admin":
		return adminCmd(*configPath, rest)
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	}
	return fmt.Errorf("unknown command %q\n%s", cmd, usage)
}

const usage = `usage:
  frame-server [serve] [--config FILE]   run the server (default command)
  frame-server dev                       dev server on :8787, data in server/.devdata, seeded demo users
  frame-server admin setup-link          one-time link to create the first admin (web panel)
  frame-server admin user list
  frame-server admin user create --email E | --phone P [--name N] [--username U] [--admin]
  frame-server admin user promote|demote <username>
  frame-server admin user reset-2fa <username>
  frame-server admin user reset-password <username|email|phone>
  frame-server admin user unlink <username|email|phone> apple|google
  frame-server admin user delete <username>
  frame-server admin invite create <username>
  frame-server admin invite server [--uses N] [--days D] [--note TEXT]
  frame-server backup <file.db>          online, consistent SQLite backup
  frame-server version`

func extractConfigFlag(args []string) ([]string, string) {
	var out []string
	cfg := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "--config" || a == "-config") && i+1 < len(args):
			cfg = args[i+1]
			i++
		case strings.HasPrefix(a, "--config="):
			cfg = strings.TrimPrefix(a, "--config=")
		default:
			out = append(out, a)
		}
	}
	return out, cfg
}

// devOverrides are dev-mode defaults; real environment variables still win
// (e.g. FRAME_LISTEN=0.0.0.0:8788 FRAME_DATA_DIR=/tmp/x frame-server dev).
func devOverrides() map[string]string {
	dir := ".devdata"
	if st, err := os.Stat(filepath.Join("server", "go.mod")); err == nil && !st.IsDir() {
		dir = filepath.Join("server", ".devdata")
	}
	return map[string]string{
		"FRAME_LISTEN":       "0.0.0.0:8787",
		"FRAME_DATA_DIR":     dir,
		"FRAME_REGISTRATION": "open",
		"FRAME_PUSH_RELAY":   "",
		"FRAME_STORAGE":      "local",
		"FRAME_NAME":         "Frame Dev",
		"FRAME_DOMAIN":       "",
	}
}

func loadConfig(path string, dev bool) (*config.Config, error) {
	var ov map[string]string
	if dev {
		ov = devOverrides()
	}
	cfg, err := config.LoadFile(path, ov)
	if err != nil {
		return nil, err
	}
	cfg.Dev = dev
	return cfg, nil
}

// secrets are generated on first start and stored in <data>/secrets.json.
type secrets struct {
	MediaKey   string `json:"media_key"`   // HMAC key for signed media URLs
	StorageKey string `json:"storage_key"` // AES-256 master key for S3 encryption at rest
}

func loadSecrets(dir string, log *slog.Logger) (media, storageKey []byte, err error) {
	path := filepath.Join(dir, "secrets.json")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		s := secrets{MediaKey: randKey(), StorageKey: randKey()}
		out, _ := json.MarshalIndent(s, "", "  ")
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, nil, err
		}
		if _, err := f.Write(append(out, '\n')); err != nil {
			f.Close()
			return nil, nil, err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return nil, nil, err
		}
		if err := f.Close(); err != nil {
			return nil, nil, err
		}
		log.Info("generated new secrets", "path", path)
		b, err = os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
	} else if err != nil {
		return nil, nil, err
	}
	if st, err := os.Stat(path); err == nil && st.Mode().Perm()&0o077 != 0 {
		log.Warn("secrets.json is readable by others; fixing permissions to 0600", "path", path)
		_ = os.Chmod(path, 0o600)
	}
	var s secrets
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, nil, fmt.Errorf("secrets.json: %w", err)
	}
	media, err1 := base64.StdEncoding.DecodeString(s.MediaKey)
	storageKey, err2 := base64.StdEncoding.DecodeString(s.StorageKey)
	if err1 != nil || err2 != nil || len(media) != 32 || len(storageKey) != 32 {
		return nil, nil, errors.New("secrets.json: keys must be 32 bytes, base64")
	}
	return media, storageKey, nil
}

func randKey() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

type app struct {
	cfg *config.Config
	db  *db.DB
	api *api.Server
	log *slog.Logger
}

func newLogger(cfg *config.Config) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
}

func bootstrap(ctx context.Context, cfg *config.Config, log *slog.Logger, now func() time.Time) (*app, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("data dir: %w", err)
	}
	mediaKey, storageKey, err := loadSecrets(cfg.DataDir, log)
	if err != nil {
		return nil, err
	}
	d, err := db.Open(ctx, filepath.Join(cfg.DataDir, "frame.db"))
	if err != nil {
		return nil, err
	}
	serverID, err := store.SetMetaIfAbsent(ctx, d.W, "server_id", ids.New())
	if err != nil {
		d.Close()
		return nil, err
	}
	var st storage.Storage
	switch cfg.Storage {
	case "s3":
		s3, err := storage.NewS3(storage.S3Config{Endpoint: cfg.S3Endpoint, Bucket: cfg.S3Bucket, Region: cfg.S3Region,
			AccessKeyID: cfg.S3AccessKeyID, SecretAccessKey: cfg.S3SecretAccessKey, Prefix: cfg.S3Prefix})
		if err != nil {
			d.Close()
			return nil, err
		}
		st = s3
		if cfg.S3Encrypt {
			if st, err = storage.NewEncrypted(s3, storageKey); err != nil {
				d.Close()
				return nil, err
			}
		}
	default:
		local, err := storage.NewLocal(filepath.Join(cfg.DataDir, "media"))
		if err != nil {
			d.Close()
			return nil, err
		}
		st = local
	}
	srv, err := api.New(api.Deps{Config: cfg, DB: d, Storage: st, MediaKey: mediaKey, ServerID: serverID,
		Version: version, Log: log, Now: now})
	if err != nil {
		d.Close()
		return nil, err
	}
	return &app{cfg: cfg, db: d, api: srv, log: log}, nil
}

func serve(configPath string, dev bool) error {
	cfg, err := loadConfig(configPath, dev)
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	clock := &devseed.Clock{}
	a, err := bootstrap(ctx, cfg, log, clock.Now)
	if err != nil {
		return err
	}
	defer a.db.Close()

	if dev {
		log.Info("seeding demo data (first run only)", "data_dir", cfg.DataDir)
		if err := devseed.Seed(ctx, a.db, a.api.Handler(), clock); err != nil {
			return fmt.Errorf("dev seed: %w", err)
		}
	}
	a.api.Start()

	newServer := func(h http.Handler) *http.Server {
		return &http.Server{
			Handler:           h,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       60 * time.Second, // uploads extend this per request
			WriteTimeout:      60 * time.Second, // media/SSE extend or lift this per request
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    64 << 10,
			ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelDebug),
			BaseContext:       func(net.Listener) context.Context { return ctx },
		}
	}

	var servers []*http.Server
	errc := make(chan error, 3)
	if cfg.Domain != "" {
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(cfg.Domain),
			Cache:      autocert.DirCache(filepath.Join(cfg.DataDir, "acme")),
			Email:      cfg.ACMEEmail,
		}
		https := newServer(a.api.Handler())
		https.Addr = ":443"
		https.TLSConfig = m.TLSConfig()
		redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://"+cfg.Domain+r.URL.RequestURI(), http.StatusMovedPermanently)
		})
		plain := newServer(m.HTTPHandler(redirect))
		plain.Addr = ":80"
		servers = append(servers, https, plain)
		go func() { errc <- https.ListenAndServeTLS("", "") }()
		go func() { errc <- plain.ListenAndServe() }()
		log.Info("frame-server listening", "version", version, "https", ":443", "http", ":80", "domain", cfg.Domain)
	} else {
		ln, err := net.Listen("tcp", cfg.Listen)
		if err != nil {
			return err
		}
		h := newServer(a.api.Handler())
		servers = append(servers, h)
		go func() { errc <- h.Serve(ln) }()
		log.Info("frame-server listening", "version", version, "addr", ln.Addr().String(), "registration", cfg.Registration,
			"storage", cfg.Storage, "push", cfg.PushRelay != "", "admin", cfg.Admin)
		if dev {
			printDevBanner(os.Stdout, cfg)
		}
	}
	if h := a.api.AdminHandler(); h != nil {
		ln, err := net.Listen("tcp", cfg.AdminListen)
		if err != nil {
			return fmt.Errorf("FRAME_ADMIN_LISTEN: %w", err)
		}
		as := newServer(h)
		servers = append(servers, as)
		go func() { errc <- as.Serve(ln) }()
		log.Info("admin panel listening", "addr", ln.Addr().String())
	}
	if !dev {
		if err := announceSetup(ctx, a, log, os.Stderr); err != nil {
			return err
		}
	}
	for _, s := range servers {
		s.RegisterOnShutdown(a.api.Hub().Close)
	}

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errc:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Stop accepting and finish in-flight requests first (SSE streams are
	// closed by the RegisterOnShutdown hook), then drain queued pushes.
	for _, s := range servers {
		_ = s.Shutdown(shutdownCtx)
	}
	a.api.Shutdown(shutdownCtx)
	return nil
}

// adminBaseURL is where the admin panel is reached: the separate admin
// listener, else FRAME_PUBLIC_URL, else localhost on the main port.
func adminBaseURL(cfg *config.Config) string {
	if cfg.AdminListen != "" {
		host, port, _ := net.SplitHostPort(cfg.AdminListen)
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "localhost"
		}
		return "http://" + net.JoinHostPort(host, port)
	}
	if cfg.PublicURL != "" {
		return cfg.PublicURL
	}
	return "http://localhost" + portOf(cfg.Listen)
}

// announceSetup prints a fresh one-time admin setup link while the server
// has no admin (a new link on every start; each replaces the previous one).
func announceSetup(ctx context.Context, a *app, log *slog.Logger, w io.Writer) error {
	need, err := a.api.NeedsSetup(ctx)
	if err != nil || !need {
		return err
	}
	if a.cfg.Admin == "off" {
		log.Warn("this server has no admin account; FRAME_ADMIN=off, so create one with: frame-server admin user create --email you@example.com --admin")
		return nil
	}
	tok, exp, err := a.api.NewSetupToken(ctx)
	if err != nil {
		return err
	}
	link := adminBaseURL(a.cfg) + "/admin/setup#t=" + tok
	fmt.Fprintf(w, "\n==============================================================================\n"+
		"  This server has no admin yet.\n"+
		"  Create your admin account: %s\n"+
		"  (works once, valid until %s; run `frame-server admin setup-link` for a new one)\n"+
		"==============================================================================\n\n",
		link, exp.UTC().Format("2006-01-02 15:04 MST"))
	log.Warn("no admin account yet: open the setup link printed above to create one", "expires", exp.UTC().Format(time.RFC3339))
	return nil
}

func printDevBanner(w io.Writer, cfg *config.Config) {
	fmt.Fprintf(w, "\nFrame dev server — data in %s\n", cfg.DataDir)
	fmt.Fprintf(w, "Demo accounts (password %q):\n", devseed.Password)
	for _, u := range devseed.Users {
		fmt.Fprintf(w, "  %-6s %-20s %s\n", u.Username, u.Email, u.Phone)
	}
	fmt.Fprintf(w, "Admin panel: %s/admin/ (sign in as %s)\n", adminBaseURL(cfg), devseed.AdminUser)
	fmt.Fprintf(w, "Android emulator: http://10.0.2.2%s  ·  iOS simulator: http://localhost%s\n", portOf(cfg.Listen), portOf(cfg.Listen))
	fmt.Fprintf(w, "FRAME DEV READY http://%s\n", cfg.Listen)
}

func portOf(listen string) string {
	if _, p, err := net.SplitHostPort(listen); err == nil {
		return ":" + p
	}
	return ""
}

func openForCLI(configPath string) (*app, error) {
	cfg, err := loadConfig(configPath, false)
	if err != nil {
		return nil, err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "frame.db")); err != nil {
		return nil, fmt.Errorf("no database in %s (set FRAME_DATA_DIR or --config)", cfg.DataDir)
	}
	return bootstrap(context.Background(), cfg, log, nil)
}

func adminCmd(configPath string, args []string) error {
	a, err := openForCLI(configPath)
	if err != nil {
		return err
	}
	defer a.db.Close()
	return admin.Run(context.Background(), &admin.Env{DB: a.db, API: a.api, PublicURL: a.cfg.PublicURL,
		InviteBase: a.cfg.InviteBase, AdminURL: adminBaseURL(a.cfg), AdminOff: a.cfg.Admin == "off", Out: os.Stdout}, args)
}

func backup(configPath, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("%s already exists", dst)
	}
	a, err := openForCLI(configPath)
	if err != nil {
		return err
	}
	defer a.db.Close()
	if err := a.db.Backup(context.Background(), dst); err != nil {
		return err
	}
	_ = os.Chmod(dst, 0o600)
	fmt.Printf("backup written to %s (also back up secrets.json and media/)\n", dst)
	return nil
}
