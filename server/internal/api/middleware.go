package api

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/auth"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ratelimit"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

type ctxKey int

const (
	ctxClientIP ctxKey = iota
	ctxUserID
)

// statusWriter records the status code and size for access logs. It exposes
// Unwrap so http.ResponseController can reach Flush/SetWriteDeadline.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush supports streaming through the wrapper (SSE).
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// outer wraps the whole handler: client IP, body limits, security headers,
// panic recovery and access logging.
func (s *Server) outer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ip := s.clientIP(r)
		r = r.WithContext(context.WithValue(r.Context(), ctxClientIP, ip))
		sw := &statusWriter{ResponseWriter: w}

		h := sw.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; sandbox")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		// HTTPS directly, or via a trusted proxy/tunnel that terminated TLS
		// (the same rule as the admin cookie's Secure flag).
		if s.requestScheme(r) == "https" {
			h.Set("Strict-Transport-Security", "max-age=63072000")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}

		// Every request body is bounded; the upload endpoints set their own
		// (larger) limits and are exempt here.
		if r.Body != nil && !isUploadRoute(r) {
			r.Body = http.MaxBytesReader(sw, r.Body, MaxJSONBody)
		}

		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("panic", "panic", v, "route", r.Pattern, "stack", string(debug.Stack()))
				if sw.status == 0 {
					writeError(sw, errInternal)
				}
			}
			s.accessLog(r, sw, ip, start)
		}()
		next.ServeHTTP(sw, r)
	})
}

func isUploadRoute(r *http.Request) bool {
	return (r.Method == http.MethodPost && r.URL.Path == "/api/v1/moments") ||
		(r.Method == http.MethodPut && r.URL.Path == "/api/v1/me/avatar")
}

// accessLog never logs query strings (media signatures), bodies, headers or
// raw paths with capabilities in them: it logs the matched route pattern.
func (s *Server) accessLog(r *http.Request, sw *statusWriter, ip netip.Addr, start time.Time) {
	route := r.Pattern
	if route == "" {
		route = "unmatched"
	}
	lvl := slog.LevelInfo
	switch {
	case sw.status >= 500:
		lvl = slog.LevelError
	case r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/m/"):
		lvl = slog.LevelDebug
	}
	attrs := []slog.Attr{
		slog.String("method", r.Method),
		slog.String("route", route),
		slog.Int("status", sw.status),
		slog.Int64("bytes", sw.bytes),
		slog.Duration("dur", time.Since(start)),
		slog.String("ip", ip.String()),
	}
	if uid, ok := r.Context().Value(ctxUserID).(string); ok {
		attrs = append(attrs, slog.String("user", uid))
	}
	s.log.LogAttrs(r.Context(), lvl, "request", attrs...)
}

// clientIP implements FRAME_TRUSTED_PROXIES: X-Forwarded-For is honoured only
// when the direct peer is a trusted proxy (walking right-to-left past trusted
// hops); with "cloudflare", CF-Connecting-IP is honoured only from loopback
// (cloudflared on the same host) or an explicitly listed CIDR — never from
// arbitrary private addresses.
func (s *Server) clientIP(r *http.Request) netip.Addr {
	peer := parseAddr(r.RemoteAddr)
	if s.cfg.TrustCloudflare && (peer.IsLoopback() || s.trusted(peer)) {
		if a, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); err == nil {
			return a.Unmap()
		}
	}
	if len(s.cfg.TrustedProxies) == 0 || !s.trusted(peer) {
		return peer
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		a = a.Unmap()
		if !s.trusted(a) {
			return a
		}
		peer = a
	}
	return peer
}

func (s *Server) trusted(a netip.Addr) bool {
	for _, p := range s.cfg.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func parseAddr(hostport string) netip.Addr {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.IPv4Unspecified()
	}
	return a.Unmap()
}

func clientIPFrom(r *http.Request) netip.Addr {
	if a, ok := r.Context().Value(ctxClientIP).(netip.Addr); ok {
		return a
	}
	return parseAddr(r.RemoteAddr)
}

// handlerFunc is an API handler returning an error to render.
type handlerFunc func(w http.ResponseWriter, r *http.Request) error

// authedFunc is an API handler for an authenticated user.
type authedFunc func(w http.ResponseWriter, r *http.Request, a *authCtx) error

// authCtx is the authenticated caller.
type authCtx struct {
	User    *store.User
	Session *store.Session
	Token   string
}

func (a *authCtx) ID() string { return a.User.ID }

func (s *Server) public(fn handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.finish(w, r, fn(w, r))
	})
}

func (s *Server) authed(fn authedFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, err := s.authenticate(r)
		if err != nil {
			s.finish(w, r, err)
			return
		}
		*r = *r.WithContext(context.WithValue(r.Context(), ctxUserID, a.ID()))
		if err := s.limit(s.rl.general, a.ID()); err != nil {
			s.finish(w, r, err)
			return
		}
		s.finish(w, r, fn(w, r, a))
	})
}

// bearer extracts the bearer token from the Authorization header.
func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

func (s *Server) authenticate(r *http.Request) (*authCtx, error) {
	tok := bearer(r)
	if !auth.ValidTokenSyntax(tok) {
		return nil, errUnauthorized
	}
	ctx := r.Context()
	h := auth.HashToken(tok)
	sess, err := store.SessionByTokenHash(ctx, s.db.R, h)
	if errors.Is(err, store.ErrNotFound) {
		return nil, s.revokedTokenError(ctx, h)
	}
	if err != nil {
		return nil, err
	}
	u, err := store.GetUser(ctx, s.db.R, sess.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errUnauthorized
	}
	if err != nil {
		return nil, err
	}
	if u.Disabled() {
		// Suspension revokes sessions; this guards against any that slip
		// through (e.g. created concurrently with the suspension).
		return nil, errSessionDisabled
	}
	now := s.now().UnixMilli()
	const touchEvery = 5 * 60 * 1000
	if now-sess.LastSeenAt > touchEvery {
		if err := store.TouchSession(ctx, s.db.W, sess.ID, now, now-touchEvery); err != nil {
			s.log.Warn("session touch failed", "err", err)
		}
		sess.LastSeenAt = now
	}
	return &authCtx{User: u, Session: sess, Token: tok}, nil
}

// revokedTokenError explains a token that matches no session: 401
// account_disabled when it belonged to a session ended by a suspension that
// is still in effect, else the generic 401 unauthorized (unknown, logged-out
// or otherwise revoked tokens learn nothing).
func (s *Server) revokedTokenError(ctx context.Context, tokenHash []byte) error {
	reason, err := store.RevokedReason(ctx, s.db.R, tokenHash)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
			s.log.Warn("revoked session lookup failed", "err", err)
		}
		return errUnauthorized
	}
	if reason == store.RevokedDisabled {
		return errSessionDisabled
	}
	return errUnauthorized
}

// finish renders a handler error (if any).
func (s *Server) finish(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		return
	}
	var ae *Error
	if errors.As(err, &ae) {
		writeError(w, ae)
		return
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		writeError(w, errPayloadTooLarge)
		return
	}
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		return // client went away
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, errNotFound("Not found"))
		return
	}
	s.log.Error("handler error", "route", r.Pattern, "err", err)
	writeError(w, errInternal)
}

// limit consumes a token from l for key, returning a 429 error if exhausted.
func (s *Server) limit(l *ratelimit.Limiter, key string) error {
	if ok, wait := l.Allow(key); !ok {
		return rateLimited(int(wait.Seconds() + 0.999))
	}
	return nil
}

func (s *Server) limitIP(l *ratelimit.Limiter, r *http.Request) error {
	return s.limit(l, ratelimit.IPKey(clientIPFrom(r)))
}
