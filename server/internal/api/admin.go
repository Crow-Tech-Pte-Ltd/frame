package api

// Web admin panel API under /admin/api/v1 (docs/ADMIN.md). It is a separate
// surface from the app API: cookie sessions instead of bearer tokens, CSRF
// checks on every state-changing request, its own strict CSP, and no
// endpoint that returns moments, media URLs, captions or messages — except
// the detail of a report, which shows the one item a member reported
// (admin_reports.go).

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/adminui"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/auth"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/config"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

const (
	adminCookie      = "frame_admin"
	adminIdleTimeout = 30 * time.Minute
	adminMaxAge      = 12 * time.Hour
	adminTouchEvery  = 15 * time.Second

	// AdminCSP is the Content-Security-Policy of every /admin response.
	AdminCSP = "default-src 'self'; img-src 'self' data: blob:; style-src 'self'; script-src 'self'; " +
		"frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
)

var (
	errCSRF         = apiErr(http.StatusForbidden, "csrf_failed", "Cross-site request rejected")
	errNotAdmin     = apiErr(http.StatusForbidden, "forbidden", "This account is not an administrator")
	errSelfAction   = apiErr(http.StatusConflict, "self_action", "You can't do this to your own account")
	errLastAdmin    = apiErr(http.StatusConflict, "last_admin", "The server needs at least one admin")
	errTOTPRequired = apiErr(http.StatusUnauthorized, "totp_required", "Enter the code from your authenticator app")
	errTOTPInvalid  = apiErr(http.StatusUnauthorized, "totp_invalid", "That code isn't valid")
)

// adminCtx is the signed-in admin.
type adminCtx struct {
	User    *store.User
	Session *store.AdminSession
}

type adminFunc func(w http.ResponseWriter, r *http.Request, a *adminCtx) error

// adminPattern records a registered admin route (tests check every
// private route rejects non-admins).
type adminPattern struct {
	Pattern string
	Private bool
}

// adminRoutes builds the /admin handler: JSON API plus the embedded SPA.
func (s *Server) adminRoutes() http.Handler {
	mux := http.NewServeMux()
	pub := func(pattern string, fn handlerFunc) {
		s.adminPatterns = append(s.adminPatterns, adminPattern{pattern, false})
		mux.Handle(pattern, s.adminPublic(fn))
	}
	priv := func(pattern string, fn adminFunc) {
		s.adminPatterns = append(s.adminPatterns, adminPattern{pattern, true})
		mux.Handle(pattern, s.adminAuthed(fn))
	}
	const p = "/admin/api/v1/"

	pub("GET "+p+"setup", s.handleAdminSetupInfo)
	pub("POST "+p+"setup/verify", s.handleAdminSetupVerify)
	pub("POST "+p+"setup", s.handleAdminSetup)
	pub("POST "+p+"login", s.handleAdminLogin)
	pub("POST "+p+"logout", s.handleAdminLogout)
	priv("GET "+p+"session", s.handleAdminSession)

	priv("GET "+p+"overview", s.handleAdminOverview)

	priv("GET "+p+"users", s.handleAdminUsers)
	priv("POST "+p+"users", s.handleAdminCreateUser)
	priv("GET "+p+"users/{id}", s.handleAdminUser)
	priv("GET "+p+"users/{id}/avatar", s.handleAdminAvatar)
	priv("POST "+p+"users/{id}/reset-password", s.handleAdminResetPassword)
	priv("POST "+p+"users/{id}/disable", s.handleAdminDisable)
	priv("POST "+p+"users/{id}/enable", s.handleAdminEnable)
	priv("POST "+p+"users/{id}/promote", s.handleAdminPromote)
	priv("POST "+p+"users/{id}/demote", s.handleAdminDemote)
	priv("DELETE "+p+"users/{id}/sessions/{sid}", s.handleAdminRevokeSession)
	priv("DELETE "+p+"users/{id}/sessions", s.handleAdminRevokeSessions)
	priv("DELETE "+p+"users/{id}", s.handleAdminDeleteUser)

	priv("GET "+p+"invites", s.handleAdminInvites)
	priv("POST "+p+"invites", s.handleAdminCreateInvite)
	priv("DELETE "+p+"invites/{id}", s.handleAdminRevokeInvite)
	priv("GET "+p+"friend-invites", s.handleAdminFriendInvites)
	priv("DELETE "+p+"friend-invites/{id}", s.handleAdminRevokeFriendInvite)

	priv("GET "+p+"settings", s.handleAdminSettings)
	priv("PATCH "+p+"settings", s.handleAdminPatchSettings)

	priv("GET "+p+"reports", s.handleAdminReports)
	priv("GET "+p+"reports/{id}", s.handleAdminReport)
	priv("GET "+p+"reports/{id}/media", s.handleAdminReportMedia)
	priv("GET "+p+"reports/{id}/thumb", s.handleAdminReportThumb)
	priv("POST "+p+"reports/{id}/delete-content", s.handleAdminReportDelete)
	priv("POST "+p+"reports/{id}/suspend-user", s.handleAdminReportSuspend)
	priv("POST "+p+"reports/{id}/dismiss", s.handleAdminReportDismiss)

	priv("GET "+p+"audit", s.handleAdminAudit)

	priv("POST "+p+"maintenance/backup", s.handleAdminBackupToken)
	priv("GET "+p+"maintenance/backup/{token}", s.handleAdminBackupDownload)
	priv("POST "+p+"maintenance/cleanup", s.handleAdminCleanup)

	priv("GET "+p+"security", s.handleAdminSecurity)
	priv("DELETE "+p+"security/sessions/{id}", s.handleAdminRevokeAdminSession)
	priv("POST "+p+"security/password", s.handleAdminChangePassword)
	priv("POST "+p+"security/totp/setup", s.handleAdminTOTPSetup)
	priv("POST "+p+"security/totp/enable", s.handleAdminTOTPEnable)
	priv("POST "+p+"security/totp/disable", s.handleAdminTOTPDisable)
	priv("POST "+p+"security/recovery-codes", s.handleAdminRecoveryCodes)

	// Everything else under /admin: unknown API paths get a JSON 404 (never
	// the SPA); other GETs get the embedded single-page app.
	spa := adminui.Handler()
	mux.Handle("/admin/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/admin/api/"):
			writeError(w, errNotFound("Not found"))
		case r.Method == http.MethodGet || r.Method == http.MethodHead:
			spa.ServeHTTP(w, r)
		default:
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, apiErr(http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed"))
		}
	}))
	mux.Handle("GET /admin", http.RedirectHandler("/admin/", http.StatusMovedPermanently))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", AdminCSP)
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
		if strings.HasPrefix(r.URL.Path, "/admin/api/") {
			h.Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(w, r)
	})
}

// adminAllowed implements FRAME_ADMIN=local: only loopback clients or clients
// inside FRAME_TRUSTED_PROXIES networks (the resolved client IP, so a reverse
// proxy doesn't make everyone local).
func (s *Server) adminAllowed(r *http.Request) bool {
	if s.cfg.Admin != "local" {
		return true
	}
	ip := clientIPFrom(r)
	if !ip.IsLoopback() && !s.trusted(ip) {
		return false
	}
	// A reverse proxy or tunnel on this host that isn't listed in
	// FRAME_TRUSTED_PROXIES also connects from loopback: if the request
	// carries forwarding headers that weren't honoured (the client IP is
	// still the direct peer), it is someone else's request, not a local one.
	peer := parseAddr(r.RemoteAddr)
	return ip != peer || s.trusted(peer) || !hasForwardingHeaders(r)
}

// hasForwardingHeaders reports whether a proxy says it forwarded r.
func hasForwardingHeaders(r *http.Request) bool {
	for _, h := range []string{"X-Forwarded-For", "Forwarded", "X-Real-Ip", "CF-Connecting-IP", "True-Client-IP"} {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	return false
}

// requestScheme is "https" when the request arrived over TLS directly or via
// a trusted proxy that says so.
func (s *Server) requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	peer := parseAddr(r.RemoteAddr)
	if p := r.Header.Get("X-Forwarded-Proto"); (p == "https" || p == "http") && s.trusted(peer) {
		return p
	}
	if s.cfg.TrustCloudflare && (peer.IsLoopback() || s.trusted(peer)) && strings.Contains(r.Header.Get("CF-Visitor"), "https") {
		return "https"
	}
	return "http"
}

// checkCSRF requires X-Frame-Admin: 1 and a same-origin request (Origin
// matching this server, or Sec-Fetch-Site: same-origin) on every
// state-changing request.
func (s *Server) checkCSRF(r *http.Request) error {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return nil
	}
	if r.Header.Get("X-Frame-Admin") != "1" {
		return errCSRF
	}
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" {
		return errCSRF
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		if r.Header.Get("Sec-Fetch-Site") == "same-origin" {
			return nil
		}
		return errCSRF
	}
	if !s.sameOrigin(r, origin) {
		return errCSRF
	}
	return nil
}

func (s *Server) sameOrigin(r *http.Request, origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" {
		return false
	}
	got := u.Scheme + "://" + config.CanonicalHost(u.Scheme, strings.ToLower(u.Host))
	scheme := s.requestScheme(r)
	if got == scheme+"://"+config.CanonicalHost(scheme, strings.ToLower(r.Host)) {
		return true
	}
	for _, o := range s.cfg.PublicOrigins {
		if got == o {
			return true
		}
	}
	return false
}

// adminPublic wraps unauthenticated admin endpoints (setup, login, logout).
func (s *Server) adminPublic(fn handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.checkCSRF(r); err != nil {
			s.finish(w, r, err)
			return
		}
		s.finish(w, r, fn(w, r))
	})
}

// adminAuthed wraps endpoints that need a valid admin session.
func (s *Server) adminAuthed(fn adminFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.checkCSRF(r); err != nil {
			s.finish(w, r, err)
			return
		}
		a, err := s.adminAuthenticate(w, r)
		if err != nil {
			s.finish(w, r, err)
			return
		}
		*r = *r.WithContext(context.WithValue(r.Context(), ctxUserID, a.User.ID))
		if err := s.limit(s.rl.general, "admin:"+a.User.ID); err != nil {
			s.finish(w, r, err)
			return
		}
		s.finish(w, r, fn(w, r, a))
	})
}

func (s *Server) adminAuthenticate(w http.ResponseWriter, r *http.Request) (*adminCtx, error) {
	c, err := r.Cookie(adminCookie)
	if err != nil || !auth.ValidTokenSyntax(c.Value) {
		return nil, errUnauthorized
	}
	ctx := r.Context()
	sess, err := store.AdminSessionByTokenHash(ctx, s.db.R, auth.HashToken(c.Value))
	if errors.Is(err, store.ErrNotFound) {
		s.clearAdminCookie(w, r)
		return nil, errUnauthorized
	}
	if err != nil {
		return nil, err
	}
	now := s.now()
	expired := now.Sub(time.UnixMilli(sess.LastSeenAt)) > adminIdleTimeout || now.Sub(time.UnixMilli(sess.CreatedAt)) > adminMaxAge
	u, err := store.GetUser(ctx, s.db.R, sess.UserID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if expired || u == nil || u.Disabled() {
		_ = store.DeleteAdminSession(ctx, s.db.W, sess.UserID, sess.ID)
		s.clearAdminCookie(w, r)
		return nil, errUnauthorized
	}
	// Re-checked on every request: removing the admin flag takes effect
	// immediately.
	if !u.IsAdmin {
		return nil, errNotAdmin
	}
	if now.Sub(time.UnixMilli(sess.LastSeenAt)) > adminTouchEvery {
		if err := store.TouchAdminSession(ctx, s.db.W, sess.ID, now.UnixMilli()); err != nil {
			s.log.Warn("admin session touch failed", "err", err)
		}
		sess.LastSeenAt = now.UnixMilli()
	}
	return &adminCtx{User: u, Session: sess}, nil
}

func (s *Server) setAdminCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: token, Path: "/admin", MaxAge: int(adminMaxAge / time.Second),
		HttpOnly: true, Secure: s.requestScheme(r) == "https", SameSite: http.SameSiteStrictMode})
}

func (s *Server) clearAdminCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: "", Path: "/admin", MaxAge: -1,
		HttpOnly: true, Secure: s.requestScheme(r) == "https", SameSite: http.SameSiteStrictMode})
}

// ---- small helpers -------------------------------------------------------

// seal encrypts plaintext with AES-256-GCM under key (nonce prepended).
func seal(key, plaintext []byte, aad string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, []byte(aad)), nil
}

// unseal reverses seal.
func unseal(key, b []byte, aad string) ([]byte, bool) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, false
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(b) < gcm.NonceSize() {
		return nil, false
	}
	pt, err := gcm.Open(nil, b[:gcm.NonceSize()], b[gcm.NonceSize():], []byte(aad))
	return pt, err == nil
}

// pageParams parses ?page=&per_page= (1-based) with bounds.
func pageParams(r *http.Request, def, maxPer int) (page, per int) {
	page, _ = strconv.Atoi(r.URL.Query().Get("page"))
	per, _ = strconv.Atoi(r.URL.Query().Get("per_page"))
	if page < 1 {
		page = 1
	}
	if page > 100000 {
		page = 100000
	}
	if per < 1 {
		per = def
	}
	if per > maxPer {
		per = maxPer
	}
	return page, per
}
