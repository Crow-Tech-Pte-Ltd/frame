// Package api implements the Frame HTTP API (docs/API.md): JSON endpoints
// under /api/v1, Server-Sent Events, signed media under /m/ and /healthz.
package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/config"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/db"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/events"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/media"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/oidc"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/push"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ratelimit"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/storage"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

// Deps are the server's collaborators.
type Deps struct {
	Config   *config.Config
	DB       *db.DB
	Storage  storage.Storage
	MediaKey []byte // HMAC key for signed media URLs (≥ 32 bytes)
	ServerID string
	Version  string
	Log      *slog.Logger
	Now      func() time.Time // nil → time.Now

	Apple, Google  *oidc.Verifier // nil → built from Config
	PushHTTPClient *http.Client   // nil → default
	Mailer         Mailer         // nil → SMTP from Config (if configured)
	TmpDir         string         // upload spool dir; default <data>/tmp
	PushOptions    *push.Options  // test hook
	// RateLimitScale multiplies every rate limit (0 → 1, or 100 in dev mode).
	RateLimitScale int
}

// Server is the HTTP API.
type Server struct {
	cfg       *config.Config
	db        *db.DB
	media     *media.Service
	hub       *events.Hub
	push      *push.Dispatcher
	apple     *oidc.Verifier
	google    *oidc.Verifier
	mailer    Mailer
	log       *slog.Logger
	now       func() time.Time
	version   string
	serverID  string
	tmpDir    string
	rl        limiters
	inviteKey []byte // AES-256 key for invite codes at rest (derived from MediaKey)
	totpKey   []byte // AES-256 key for admin TOTP secrets at rest
	recKey    []byte // HMAC key for admin recovery codes
	handler   http.Handler
	admin     http.Handler // separate admin listener (FRAME_ADMIN_LISTEN) or nil
	stop      chan struct{}
	started   time.Time
	uploads   uploadGate
	settings  atomic.Pointer[liveSettings]

	relayClient *http.Client
	relayMu     sync.Mutex
	relayState  relayHealth

	backupMu     sync.Mutex
	backupTokens map[string]backupGrant

	adminPatterns []adminPattern
}

type limiters struct {
	authIP, authID, authIDGlobal, general, moments, messages, contacts, invites, publicIP, mediaIP, typing, reports *ratelimit.Limiter

	secondFactor *ratelimit.Limiter // admin 2FA code attempts per account
}

func (l *limiters) all() []*ratelimit.Limiter {
	return []*ratelimit.Limiter{l.authIP, l.authID, l.authIDGlobal, l.general, l.moments, l.messages, l.contacts, l.invites, l.publicIP, l.mediaIP, l.typing, l.reports, l.secondFactor}
}

// New builds the server (no goroutines are started until Start).
func New(d Deps) (*Server, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.TmpDir == "" {
		d.TmpDir = filepath.Join(d.Config.DataDir, "tmp")
	}
	if err := os.MkdirAll(d.TmpDir, 0o700); err != nil {
		return nil, err
	}
	s := &Server{
		cfg: d.Config, db: d.DB, log: d.Log, now: d.Now, version: d.Version, serverID: d.ServerID,
		tmpDir: d.TmpDir, hub: events.NewHub(), stop: make(chan struct{}), started: d.Now(),
		backupTokens: map[string]backupGrant{},
	}
	s.inviteKey = deriveKey(d.MediaKey, "frame-invite-v1")
	s.totpKey = deriveKey(d.MediaKey, "frame-totp-v1")
	s.recKey = deriveKey(d.MediaKey, "frame-recovery-v1")
	if err := s.ReloadSettings(context.Background()); err != nil {
		return nil, err
	}
	s.relayClient = d.PushHTTPClient
	if s.relayClient == nil {
		s.relayClient = &http.Client{Timeout: 4 * time.Second}
	}
	s.media = &media.Service{DB: d.DB, Storage: d.Storage, Signer: media.NewSigner(d.MediaKey, d.Now), Log: d.Log, Now: d.Now}

	s.apple, s.google = d.Apple, d.Google
	if s.apple == nil {
		var aud []string
		if d.Config.AuthApple {
			aud = d.Config.AppleAudiences
		}
		s.apple = oidc.NewVerifier(oidc.AppleProvider(), aud, oidc.Options{Now: d.Now})
	}
	if s.google == nil {
		var aud []string
		if d.Config.AuthGoogle {
			aud = d.Config.GoogleClientIDs
		}
		s.google = oidc.NewVerifier(oidc.GoogleProvider(), aud, oidc.Options{Now: d.Now})
	}

	s.mailer = d.Mailer
	if s.mailer == nil && d.Config.SMTPEnabled() {
		s.mailer = &smtpMailer{url: d.Config.SMTPURL, from: d.Config.SMTPFrom}
	}

	popt := push.Options{
		RelayURL:   d.Config.PushRelay,
		HTTPClient: d.PushHTTPClient,
		Log:        d.Log,
		Devices: func(ctx context.Context, userIDs []string) ([]store.Device, error) {
			return store.DevicesFor(ctx, d.DB.R, userIDs)
		},
		OnUnregistered: func(ctx context.Context, token string) {
			if err := store.DeleteDeviceByToken(ctx, d.DB.W, token); err != nil {
				d.Log.Warn("deleting unregistered device failed", "err", err)
			}
		},
	}
	if d.PushOptions != nil {
		po := *d.PushOptions
		if po.Devices == nil {
			po.Devices = popt.Devices
		}
		if po.OnUnregistered == nil {
			po.OnUnregistered = popt.OnUnregistered
		}
		if po.Log == nil {
			po.Log = d.Log
		}
		popt = po
	}
	s.push = push.NewDispatcher(popt)

	// Rate limits (docs/API.md). Dev mode multiplies them so automated UI
	// tests are not throttled.
	m := d.RateLimitScale
	if m <= 0 {
		m = 1
		if d.Config.Dev {
			m = 100
		}
	}
	s.rl = limiters{
		authIP: ratelimit.New(10*m, time.Minute, 0),
		// Per (identifier, client IP) — strict, so guessing is slow — plus a
		// looser per-identifier global cap, so an attacker elsewhere can't
		// lock the victim out of their own account.
		authID:       ratelimit.New(5*m, time.Minute, 0),
		authIDGlobal: ratelimit.New(30*m, time.Minute, 0),
		general:      ratelimit.New(600*m, time.Minute, 0),
		moments:      ratelimit.New(60*m, time.Hour, 0),
		messages:     ratelimit.New(60*m, time.Minute, 0),
		contacts:     ratelimit.New(10*m, time.Hour, 0),
		invites:      ratelimit.New(30*m, time.Minute, 0),
		publicIP:     ratelimit.New(120*m, time.Minute, 0),
		mediaIP:      ratelimit.New(1200*m, time.Minute, 300*m),
		typing:       ratelimit.New(30*m, time.Minute, 0),
		reports:      ratelimit.New(30*m, time.Hour, 0),
		// Admin 2FA code attempts per account, whatever identifier or IP
		// they come from: 6-digit codes must not be guessable at the
		// (per-identifier) login rate.
		secondFactor: ratelimit.New(10*m, 15*time.Minute, 0),
	}
	for _, l := range s.rl.all() {
		l.WithClock(d.Now)
	}
	api := s.routes()
	var admin http.Handler
	if d.Config.Admin != "off" {
		admin = s.adminRoutes()
	}
	if admin != nil && d.Config.AdminListen != "" {
		// /admin lives only on its own listener; the main one 404s it.
		s.handler = s.outer(s.dispatch(api, nil))
		s.admin = s.outer(s.dispatch(nil, admin))
	} else {
		s.handler = s.outer(s.dispatch(api, admin))
	}
	return s, nil
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

// AdminHandler returns the handler for the separate admin listener
// (FRAME_ADMIN_LISTEN), or nil when /admin is served by Handler (or off).
func (s *Server) AdminHandler() http.Handler { return s.admin }

// dispatch sends /admin paths to admin (when served on this listener) and
// everything else to api.
func (s *Server) dispatch(api, admin http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/admin" || strings.HasPrefix(p, "/admin/") {
			if admin == nil || !s.adminAllowed(r) {
				writeError(w, errNotFound("Not found"))
				return
			}
			admin.ServeHTTP(w, r)
			return
		}
		if api == nil {
			writeError(w, errNotFound("Not found"))
			return
		}
		api.ServeHTTP(w, r)
	})
}

// Hub exposes the realtime hub (shutdown hook, tests).
func (s *Server) Hub() *events.Hub { return s.hub }

// Media exposes the media service (dev seeding, admin).
func (s *Server) Media() *media.Service { return s.media }

// Start launches push workers and periodic maintenance.
func (s *Server) Start() {
	s.push.Start()
	go s.maintenance()
}

// Shutdown stops background work: SSE connections are closed and queued
// pushes get until ctx's deadline to drain.
func (s *Server) Shutdown(ctx context.Context) {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	s.hub.Close()
	s.push.Stop(ctx)
}

func (s *Server) maintenance() {
	tick := time.NewTicker(10 * time.Minute)
	defer tick.Stop()
	s.runMaintenance()
	for {
		select {
		case <-s.stop:
			return
		case <-tick.C:
			s.runMaintenance()
		}
	}
}

// OrphanGrace is how long unreferenced media survives (covers uploads in
// flight between blob write and moment commit).
const OrphanGrace = time.Hour

func (s *Server) runMaintenance() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for _, l := range s.rl.all() {
		l.Sweep()
	}
	if n, err := s.media.Sweep(ctx, OrphanGrace); err != nil {
		s.log.Warn("orphan media sweep failed", "err", err)
	} else if n > 0 {
		s.log.Info("orphan media removed", "count", n)
	}
	if _, err := store.DeleteExpiredInvites(ctx, s.db.W, s.now().UnixMilli()); err != nil {
		s.log.Warn("expired invite cleanup failed", "err", err)
	}
	if err := store.DeleteExpiredNonces(ctx, s.db.W, s.now().UnixMilli()); err != nil {
		s.log.Warn("nonce cleanup failed", "err", err)
	}
	now := s.now()
	if err := store.DeleteExpiredAdminSessions(ctx, s.db.W, now.Add(-adminIdleTimeout).UnixMilli(), now.Add(-adminMaxAge).UnixMilli()); err != nil {
		s.log.Warn("admin session cleanup failed", "err", err)
	}
	// Stale upload spool files (crashed uploads).
	if entries, err := os.ReadDir(s.tmpDir); err == nil {
		for _, e := range entries {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > OrphanGrace {
				_ = os.Remove(filepath.Join(s.tmpDir, e.Name()))
			}
		}
	}
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	handle := func(pattern string, h http.Handler) { mux.Handle(pattern, h) }

	handle("GET /healthz", http.HandlerFunc(s.handleHealth))
	handle("GET /m/{id}", http.HandlerFunc(s.handleMedia))
	handle("HEAD /m/{id}", http.HandlerFunc(s.handleMedia))
	handle("GET /api/v1/server", s.public(s.handleServerInfo))

	// Auth
	handle("POST /api/v1/auth/register", s.public(s.handleRegister))
	handle("POST /api/v1/auth/login", s.public(s.handleLogin))
	handle("POST /api/v1/auth/apple", s.public(s.handleApple))
	handle("POST /api/v1/auth/google", s.public(s.handleGoogle))
	handle("POST /api/v1/auth/logout", s.authed(s.handleLogout))
	handle("GET /api/v1/auth/sessions", s.authed(s.handleSessions))
	handle("DELETE /api/v1/auth/sessions/{id}", s.authed(s.handleDeleteSession))
	handle("POST /api/v1/auth/password", s.authed(s.handleChangePassword))
	handle("POST /api/v1/auth/password-reset/request", s.public(s.handleResetRequest))
	handle("POST /api/v1/auth/password-reset/confirm", s.public(s.handleResetConfirm))

	// Me
	handle("GET /api/v1/me", s.authed(s.handleGetMe))
	handle("PATCH /api/v1/me", s.authed(s.handlePatchMe))
	handle("PUT /api/v1/me/avatar", s.authed(s.handlePutAvatar))
	handle("DELETE /api/v1/me/avatar", s.authed(s.handleDeleteAvatar))
	handle("DELETE /api/v1/me", s.authed(s.handleDeleteMe))

	// Users & discovery
	handle("GET /api/v1/users/search", s.authed(s.handleSearch))
	handle("GET /api/v1/users/{id}", s.authed(s.handleGetUser))
	handle("POST /api/v1/contacts/match", s.authed(s.handleContactsMatch))

	// Friends & blocks
	handle("GET /api/v1/friends", s.authed(s.handleFriends))
	handle("DELETE /api/v1/friends/{user_id}", s.authed(s.handleUnfriend))
	handle("GET /api/v1/friends/requests", s.authed(s.handleFriendRequests))
	handle("POST /api/v1/friends/requests", s.authed(s.handleCreateFriendRequest))
	handle("POST /api/v1/friends/requests/{id}/accept", s.authed(s.handleAcceptRequest))
	handle("POST /api/v1/friends/requests/{id}/decline", s.authed(s.handleDeclineRequest))
	handle("DELETE /api/v1/friends/requests/{id}", s.authed(s.handleCancelRequest))
	handle("GET /api/v1/blocks", s.authed(s.handleBlocks))
	handle("POST /api/v1/blocks", s.authed(s.handleBlock))
	handle("DELETE /api/v1/blocks/{user_id}", s.authed(s.handleUnblock))

	// Invites
	handle("POST /api/v1/invites", s.authed(s.handleCreateInvite))
	handle("GET /api/v1/invites", s.authed(s.handleListInvites))
	handle("DELETE /api/v1/invites/{code}", s.authed(s.handleDeleteInvite))
	handle("GET /api/v1/invites/{code}", s.public(s.handleGetInvite))
	handle("POST /api/v1/invites/{code}/redeem", s.authed(s.handleRedeemInvite))

	// Moments
	handle("POST /api/v1/moments", s.authed(s.handleCreateMoment))
	handle("GET /api/v1/moments", s.authed(s.handleListMoments))
	handle("GET /api/v1/moments/latest", s.authed(s.handleLatestMoment))
	handle("POST /api/v1/moments/views", s.authed(s.handleMomentViews))
	handle("GET /api/v1/moments/{id}", s.authed(s.handleGetMoment))
	handle("DELETE /api/v1/moments/{id}", s.authed(s.handleDeleteMoment))
	handle("POST /api/v1/moments/{id}/hide", s.authed(s.handleHideMoment))
	handle("PUT /api/v1/moments/{id}/reaction", s.authed(s.handlePutReaction))
	handle("DELETE /api/v1/moments/{id}/reaction", s.authed(s.handleDeleteReaction))

	// Chat
	handle("GET /api/v1/conversations", s.authed(s.handleConversations))
	handle("GET /api/v1/conversations/{peer_id}/messages", s.authed(s.handleMessages))
	handle("POST /api/v1/conversations/{peer_id}/messages", s.authed(s.handleSendMessage))
	handle("POST /api/v1/conversations/{peer_id}/read", s.authed(s.handleRead))
	handle("POST /api/v1/conversations/{peer_id}/typing", s.authed(s.handleTyping))
	handle("DELETE /api/v1/messages/{id}", s.authed(s.handleDeleteMessage))
	handle("PUT /api/v1/messages/{id}/reaction", s.authed(s.handleReactToMessage))
	handle("DELETE /api/v1/messages/{id}/reaction", s.authed(s.handleDeleteMessageReaction))

	// Reports (reviewed by the server's admin in /admin)
	handle("POST /api/v1/reports", s.authed(s.handleReport))

	// Realtime & push
	handle("GET /api/v1/events", s.authed(s.handleEvents))
	handle("PUT /api/v1/devices/current", s.authed(s.handlePutDevice))
	handle("DELETE /api/v1/devices/current", s.authed(s.handleDeleteDevice))

	// Anything unmatched gets a JSON 404/405 instead of the mux's text.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		rec := &headerRecorder{h: http.Header{}}
		h.ServeHTTP(rec, r)
		switch rec.status {
		case http.StatusMethodNotAllowed:
			if allow := rec.h.Get("Allow"); allow != "" {
				w.Header().Set("Allow", allow)
			}
			writeError(w, apiErr(http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed"))
		case http.StatusMovedPermanently, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			writeError(w, errNotFound("Not found"))
		default:
			writeError(w, errNotFound("Not found"))
		}
	})
}

// headerRecorder captures what the mux's fallback handler would write.
type headerRecorder struct {
	h      http.Header
	status int
}

func (r *headerRecorder) Header() http.Header { return r.h }
func (r *headerRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
}
func (r *headerRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return len(b), nil
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.db.R.PingContext(r.Context()); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("db unavailable\n"))
		return
	}
	_, _ = w.Write([]byte("ok\n"))
}

type serverInfo struct {
	Name          string          `json:"name"`
	Version       string          `json:"version"`
	APIVersion    int             `json:"api_version"`
	ServerID      string          `json:"server_id"`
	Registration  string          `json:"registration"`
	UserInvites   bool            `json:"user_invites"`
	AuthMethods   map[string]bool `json:"auth_methods"`
	PasswordReset string          `json:"password_reset"`
	Limits        serverLimits    `json:"limits"`
	Push          *pushInfo       `json:"push"`
}

type serverLimits struct {
	MaxVideoMS       int64 `json:"max_video_ms"`
	MaxUploadBytes   int64 `json:"max_upload_bytes"`
	MaxCaptionLength int   `json:"max_caption_length"`
	MaxFriends       *int  `json:"max_friends"`
}

type pushInfo struct {
	RelayURL string `json:"relay_url"`
}

// MaxCaptionLength is the caption length limit in Unicode scalars.
const MaxCaptionLength = 64

func (s *Server) handleServerInfo(w http.ResponseWriter, r *http.Request) error {
	if err := s.limitIP(s.rl.publicIP, r); err != nil {
		return err
	}
	set := s.live()
	info := serverInfo{
		Name: set.Name, Version: s.version, APIVersion: 1, ServerID: s.serverID, Registration: set.Registration,
		UserInvites: set.UserInvites,
		AuthMethods: map[string]bool{
			"email": s.cfg.AuthEmail, "phone": s.cfg.AuthPhone,
			"apple": s.oidcEnabled(s.apple), "google": s.oidcEnabled(s.google),
		},
		PasswordReset: "admin",
		Limits:        serverLimits{MaxVideoMS: set.MaxVideoMS, MaxUploadBytes: set.MaxUploadBytes, MaxCaptionLength: MaxCaptionLength},
	}
	if s.mailer != nil {
		info.PasswordReset = "email"
	}
	if set.MaxFriends > 0 {
		n := set.MaxFriends
		info.Limits.MaxFriends = &n
	}
	if s.push.Enabled() {
		info.Push = &pushInfo{RelayURL: s.cfg.PushRelay}
	}
	writeJSON(w, http.StatusOK, info)
	return nil
}

// deriveKey derives a 32-byte subkey: HMAC-SHA256(master, label).
func deriveKey(master []byte, label string) []byte {
	m := hmac.New(sha256.New, master)
	m.Write([]byte(label))
	return m.Sum(nil)
}

// oidcEnabled: Apple/Google need configured audiences and at least one
// configured public host to bind nonces to (dev mode accepts any host).
func (s *Server) oidcEnabled(v *oidc.Verifier) bool {
	return v.Enabled() && (len(s.cfg.PublicHosts) > 0 || s.cfg.Dev)
}
