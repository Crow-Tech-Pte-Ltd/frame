package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ids"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/media"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

// PingInterval is how often SSE connections get a ": ping" comment.
var PingInterval = 25 * time.Second

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	rc := http.NewResponseController(w)
	// SSE is long-lived: lift the server-wide deadlines; each write below
	// sets its own short deadline to detect dead peers.
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})

	sub := s.hub.Subscribe(a.ID(), a.Session.ID)
	if sub == nil {
		return apiErr(http.StatusServiceUnavailable, "unavailable", "Server is shutting down")
	}
	defer s.hub.Unsubscribe(sub)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	write := func(b []byte) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if _, err := w.Write(b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !write([]byte(": connected\nretry: 3000\n\n")) {
		return nil
	}
	ping := time.NewTicker(PingInterval)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-sub.Done:
			return nil
		case ev := <-sub.C:
			// Never deliver to a revoked session — including revocations
			// made by another process (admin CLI) that this hub never saw.
			// One indexed primary-key lookup per event; no caching, so there
			// is no window after revocation.
			if !s.sessionAlive(r.Context(), a.Session.ID) {
				return nil
			}
			if !write(ev.Encode()) {
				return nil
			}
		case <-ping.C:
			// Idle streams of revoked sessions end within one ping interval.
			if !s.sessionAlive(r.Context(), a.Session.ID) {
				return nil
			}
			if !write([]byte(": ping\n\n")) {
				return nil
			}
		}
	}
}

// sessionAlive reports whether the session still exists; on a database
// error it fails closed.
func (s *Server) sessionAlive(ctx context.Context, sessionID string) bool {
	alive, err := store.SessionAlive(ctx, s.db.R, sessionID)
	return err == nil && alive
}

type deviceReq struct {
	Platform         string  `json:"platform"`
	RelayToken       string  `json:"relay_token"`
	WidgetRelayToken *string `json:"widget_relay_token"`
	PushKey          string  `json:"push_key"`
	Prefs            *struct {
		Moments   *bool `json:"moments"`
		Messages  *bool `json:"messages"`
		Reactions *bool `json:"reactions"`
		Friends   *bool `json:"friends"`
	} `json:"prefs"`
}

func decodePushKey(s string) ([]byte, bool) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == 32 {
			return b, true
		}
	}
	return nil, false
}

func validRelayToken(t string) bool {
	if len(t) == 0 || len(t) > 2048 {
		return false
	}
	for i := 0; i < len(t); i++ {
		if t[i] <= ' ' || t[i] > '~' {
			return false
		}
	}
	return true
}

func (s *Server) handlePutDevice(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	var req deviceReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Platform != "ios" && req.Platform != "android" {
		return errBadRequest(`platform must be "ios" or "android"`)
	}
	if !validRelayToken(req.RelayToken) {
		return errBadRequest("relay_token is required")
	}
	if req.WidgetRelayToken != nil && *req.WidgetRelayToken == "" {
		req.WidgetRelayToken = nil
	}
	if req.WidgetRelayToken != nil && !validRelayToken(*req.WidgetRelayToken) {
		return errBadRequest("widget_relay_token is invalid")
	}
	key, ok := decodePushKey(req.PushKey)
	if !ok {
		return errBadRequest("push_key must be 32 bytes, base64")
	}
	d := &store.Device{SessionID: a.Session.ID, UserID: a.ID(), Platform: req.Platform, RelayToken: req.RelayToken,
		WidgetRelayToken: req.WidgetRelayToken, PushKey: key, PrefMoments: true, PrefMessages: true, PrefReactions: true,
		PrefFriends: true, UpdatedAt: s.now().UnixMilli()}
	if p := req.Prefs; p != nil {
		set := func(dst *bool, v *bool) {
			if v != nil {
				*dst = *v
			}
		}
		set(&d.PrefMoments, p.Moments)
		set(&d.PrefMessages, p.Messages)
		set(&d.PrefReactions, p.Reactions)
		set(&d.PrefFriends, p.Friends)
	}
	if err := store.UpsertDevice(r.Context(), s.db.W, d); err != nil {
		return err
	}
	return noContent(w)
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	if err := store.DeleteDevice(r.Context(), s.db.W, a.Session.ID); err != nil {
		return err
	}
	return noContent(w)
}

// handleMedia serves signed media: GET /m/{id}?e=&s= with Range support.
func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	if err := s.limitIP(s.rl.mediaIP, r); err != nil {
		s.finish(w, r, err)
		return
	}
	id := r.PathValue("id")
	q := r.URL.Query()
	if !s.media.Signer.Verify(id, q.Get("e"), q.Get("s")) {
		writeError(w, errForbidden("Invalid or expired media signature"))
		return
	}
	m, obj, err := s.media.Open(r.Context(), ids.Normalize(id))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, errNotFound("Media not found"))
			return
		}
		s.log.Error("media open failed", "media_id", id, "err", err)
		writeError(w, errInternal)
		return
	}
	defer obj.Close()
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Minute))
	h := w.Header()
	h.Set("Content-Type", m.ContentType)
	h.Set("Cache-Control", "private, max-age=86400, immutable")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Accept-Ranges", "bytes")
	h.Set("ETag", `"`+m.ID+`"`)
	if m.ContentType != media.JPEG && m.ContentType != media.HEIC && m.ContentType != media.MP4 {
		h.Set("Content-Disposition", "attachment")
	}
	singleRangeOnly(r)
	http.ServeContent(w, r, "", time.UnixMilli(m.CreatedAt), obj)
}

// singleRangeOnly drops multi-range requests ("Range: bytes=0-0,9-9,…")
// before http.ServeContent sees them, so the whole object is served instead
// (RFC 9110 lets a server ignore Range). Media players only ever ask for one
// range, while every extra range would be a separate backend fetch (a ranged
// GET against S3/R2): up to 200 per request, from anyone holding one signed
// URL.
func singleRangeOnly(r *http.Request) {
	if strings.Contains(r.Header.Get("Range"), ",") {
		r.Header.Del("Range")
	}
}
