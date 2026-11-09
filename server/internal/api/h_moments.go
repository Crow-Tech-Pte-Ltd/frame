package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ids"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/media"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/push"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

const (
	maxMetaBytes  = 64 << 10
	maxThumbBytes = 200 << 10
	maxThumbDim   = 512
)

// upload holds the parsed parts of a moment upload.
type upload struct {
	meta      *momentMeta
	caption   *store.Caption
	mediaFile *os.File
	mediaSize int64
	mediaType string // sniffed
	mediaDecl string // part Content-Type
	thumb     []byte
}

func (u *upload) cleanup() {
	if u.mediaFile != nil {
		name := u.mediaFile.Name()
		u.mediaFile.Close()
		os.Remove(name)
	}
}

// readPart reads at most limit bytes; errPayloadTooLarge if more.
func readPart(p *multipart.Part, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(p, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errPayloadTooLarge
	}
	return b, nil
}

func (s *Server) handleCreateMoment(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	if err := s.limit(s.rl.moments, a.ID()); err != nil {
		return err
	}
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		return apiErr(http.StatusUnsupportedMediaType, "unsupported_media", "Expected multipart/form-data")
	}
	limit := s.live().MaxUploadBytes + maxThumbBytes + maxMetaBytes + 64<<10
	if r.ContentLength > limit {
		return errPayloadTooLarge
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	rc := http.NewResponseController(w)
	// The server-wide WriteTimeout (60 s) starts when the headers are read,
	// so a slow upload would lose its response: extend both deadlines.
	_ = rc.SetReadDeadline(time.Now().Add(15 * time.Minute))
	_ = rc.SetWriteDeadline(time.Now().Add(16 * time.Minute))

	ctx := r.Context()
	up := &upload{}
	defer up.cleanup()
	mr := multipart.NewReader(r.Body, params["boundary"])
	for parts := 0; ; parts++ {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return errPayloadTooLarge
			}
			return errBadRequest("Malformed multipart body")
		}
		if parts >= 8 {
			return errBadRequest("Too many parts")
		}
		switch p.FormName() {
		case "meta":
			if up.meta != nil {
				return errBadRequest("Duplicate meta part")
			}
			b, err := readPart(p, maxMetaBytes)
			if err != nil {
				return err
			}
			up.meta, up.caption, err = parseMomentMeta(b, s.live().MaxVideoMS, s.now())
			if err != nil {
				return err
			}
			// Idempotency: a retry of an upload that already succeeded
			// returns the original without reading the rest of the body.
			if existing, err := store.MomentByClientID(ctx, s.db.R, a.ID(), up.meta.ClientID); err == nil {
				return s.writeMoment(w, ctx, a.ID(), existing, http.StatusOK)
			} else if !errors.Is(err, store.ErrNotFound) {
				return err
			}
		case "media":
			if up.mediaFile != nil {
				return errBadRequest("Duplicate media part")
			}
			up.mediaDecl, _, _ = mime.ParseMediaType(p.Header.Get("Content-Type"))
			if err := s.spoolMedia(up, p); err != nil {
				return err
			}
		case "thumb":
			if up.thumb != nil {
				return errBadRequest("Duplicate thumb part")
			}
			if up.thumb, err = readPart(p, maxThumbBytes); err != nil {
				return err
			}
		default:
			if _, err := io.Copy(io.Discard, io.LimitReader(p, maxMetaBytes)); err != nil {
				return errBadRequest("Malformed multipart body")
			}
		}
		p.Close()
	}
	if up.meta == nil || up.mediaFile == nil || up.thumb == nil {
		return errBadRequest("meta, media and thumb parts are required")
	}
	if err := validateUpload(up); err != nil {
		return err
	}
	tw, th, _ := media.JPEGSize(up.thumb)

	// Validate recipients before storing anything.
	me := a.ID()
	if up.meta.Audience == "selected" {
		friends, err := store.FriendIDs(ctx, s.db.R, me)
		if err != nil {
			return err
		}
		set := make(map[string]bool, len(friends))
		for _, f := range friends {
			set[f] = true
		}
		for _, id := range up.meta.RecipientIDs {
			if !set[id] {
				return errNotFriends
			}
		}
	}

	var dur *int64
	if up.meta.Kind == "video" {
		dur = up.meta.DurationMS
	}
	full, err := s.media.Put(ctx, me, up.mediaType, up.mediaFile, up.mediaSize, up.meta.Width, up.meta.Height, dur)
	if err != nil {
		return err
	}
	thumb, err := s.media.Put(ctx, me, media.JPEG, bytes.NewReader(up.thumb), int64(len(up.thumb)), tw, th, nil)
	if err != nil {
		s.media.Delete(context.WithoutCancel(ctx), []string{full.ID})
		return err
	}
	now := s.now()
	m := &store.Moment{ID: ids.At(now), AuthorID: me, ClientID: up.meta.ClientID, Kind: up.meta.Kind,
		Audience: up.meta.Audience, MediaID: full.ID, ThumbID: thumb.ID, LocalDate: up.meta.LocalDate,
		Caption: up.caption, CreatedAt: now.UnixMilli()}
	var recipients []string
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		friends, err := store.FriendIDs(ctx, tx, me)
		if err != nil {
			return err
		}
		switch m.Audience {
		case "all":
			recipients = friends
		case "selected":
			// Drop anyone unfriended since validation.
			set := make(map[string]bool, len(friends))
			for _, f := range friends {
				set[f] = true
			}
			for _, id := range up.meta.RecipientIDs {
				if set[id] {
					recipients = append(recipients, id)
				}
			}
		}
		return store.InsertMoment(ctx, tx, m, recipients)
	})
	if errors.Is(err, store.ErrDuplicateClient) {
		s.media.Delete(context.WithoutCancel(ctx), []string{full.ID, thumb.ID})
		existing, err := store.MomentByClientID(ctx, s.db.R, me, up.meta.ClientID)
		if err != nil {
			return err
		}
		return s.writeMoment(w, ctx, me, existing, http.StatusOK)
	}
	if err != nil {
		s.media.Delete(context.WithoutCancel(ctx), []string{full.ID, thumb.ID})
		return err
	}
	if err := s.writeMoment(w, ctx, me, m, http.StatusCreated); err != nil {
		return err
	}
	s.announceMoment(ctx, a.User, m, recipients)
	return nil
}

// spoolMedia streams the media part to a temp file (bounded by
// max_upload_bytes) and sniffs its magic bytes.
func (s *Server) spoolMedia(up *upload, p *multipart.Part) error {
	f, err := os.CreateTemp(s.tmpDir, "upload-*")
	if err != nil {
		return err
	}
	up.mediaFile = f
	n, err := io.Copy(f, io.LimitReader(p, s.live().MaxUploadBytes+1))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return errPayloadTooLarge
		}
		return errBadRequest("Upload interrupted")
	}
	if n > s.live().MaxUploadBytes {
		return errPayloadTooLarge
	}
	if n == 0 {
		return errBadRequest("media part is empty")
	}
	up.mediaSize = n
	head := make([]byte, media.SniffLen)
	k, _ := f.ReadAt(head, 0)
	up.mediaType = media.Sniff(head[:k])
	return nil
}

// validateUpload checks magic bytes against the declared kind and the thumb.
func validateUpload(up *upload) error {
	switch up.meta.Kind {
	case "photo":
		if up.mediaType != media.JPEG && up.mediaType != media.HEIC {
			return apiErr(http.StatusUnsupportedMediaType, "unsupported_media", "Photos must be JPEG or HEIC")
		}
		switch up.mediaDecl {
		case "", "application/octet-stream", media.JPEG, media.HEIC, "image/heif":
		default:
			return apiErr(http.StatusUnsupportedMediaType, "unsupported_media", "media Content-Type does not match its contents")
		}
		if (up.mediaDecl == media.JPEG && up.mediaType != media.JPEG) || ((up.mediaDecl == media.HEIC || up.mediaDecl == "image/heif") && up.mediaType != media.HEIC) {
			return apiErr(http.StatusUnsupportedMediaType, "unsupported_media", "media Content-Type does not match its contents")
		}
	case "video":
		if up.mediaType != media.MP4 {
			return apiErr(http.StatusUnsupportedMediaType, "unsupported_media", "Videos must be MP4")
		}
		switch up.mediaDecl {
		case "", "application/octet-stream", media.MP4, "video/quicktime":
		default:
			return apiErr(http.StatusUnsupportedMediaType, "unsupported_media", "media Content-Type does not match its contents")
		}
	}
	if media.Sniff(up.thumb) != media.JPEG {
		return apiErr(http.StatusUnsupportedMediaType, "unsupported_media", "thumb must be a JPEG")
	}
	tw, th, ok := media.JPEGSize(up.thumb)
	if !ok {
		return apiErr(http.StatusUnsupportedMediaType, "unsupported_media", "thumb is not a valid JPEG")
	}
	if tw > maxThumbDim || th > maxThumbDim {
		return errBadRequest("thumb must be at most 512×512")
	}
	return nil
}

func (s *Server) writeMoment(w http.ResponseWriter, ctx context.Context, me string, m *store.Moment, status int) error {
	v, err := s.renderer(ctx, me).moment(m)
	if err != nil {
		return err
	}
	writeJSON(w, status, v)
	return nil
}

// announceMoment sends moment.new to the author's other devices and every
// recipient, plus alert/widget pushes.
func (s *Server) announceMoment(ctx context.Context, author *store.User, m *store.Moment, recipients []string) {
	if v, err := s.renderer(ctx, author.ID).moment(m); err == nil {
		s.emit("moment.new", map[string]any{"moment": v}, author.ID)
	}
	if len(recipients) == 0 {
		return
	}
	// Recipients all see the same view at creation (no reactions/views yet),
	// so render it once from the first recipient's perspective.
	if v, err := s.renderer(ctx, recipients[0]).moment(m); err == nil {
		s.emit("moment.new", map[string]any{"moment": v}, recipients...)
	}
	body := "sent a new photo"
	if m.Kind == "video" {
		body = "sent a new video"
	}
	if m.Caption != nil {
		body += `: "` + m.Caption.Text + `"`
	}
	thumb := s.media.Signer.URL(m.ThumbID)
	s.notify(push.Notification{
		UserIDs: recipients, Pref: push.PrefMoments, Widget: true, CollapseID: "moment-" + m.ID,
		Payload: push.Payload{Type: "moment", Title: author.DisplayName, Body: body, MomentID: strp(m.ID),
			PeerID: strp(author.ID), ThumbPath: &thumb, CreatedAt: m.CreatedAt},
	})
}

func queryInt(r *http.Request, key string, def, lo, hi int) (int, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return 0, errBadRequest(key + " must be an integer between " + strconv.Itoa(lo) + " and " + strconv.Itoa(hi))
	}
	return n, nil
}

func queryMillis(r *http.Request, key string) (int64, bool, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return 0, false, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 || n > 1<<47 {
		return 0, false, errBadRequest(key + " must be a timestamp in milliseconds")
	}
	return n, true, nil
}

func (s *Server) handleListMoments(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	limit, err := queryInt(r, "limit", 30, 1, 100)
	if err != nil {
		return err
	}
	before, err := decodeCursor(r.URL.Query().Get("before"))
	if err != nil {
		return errBadRequest("Invalid cursor")
	}
	me := a.ID()
	ctx := r.Context()
	fq := store.FeedQuery{Me: me, BeforeID: before, Limit: limit + 1}
	empty := func() error {
		writeJSON(w, http.StatusOK, map[string]any{"moments": []any{}, "next_cursor": nil})
		return nil
	}
	if uid := r.URL.Query().Get("user_id"); uid != "" {
		uid = ids.Normalize(uid)
		if uid == "" {
			return errBadRequest("Invalid user_id")
		}
		if uid != me {
			ok, err := store.AreFriends(ctx, s.db.R, me, uid)
			if err != nil {
				return err
			}
			if !ok {
				return empty()
			}
		}
		fq.AuthorID = uid
	}
	if from, ok, err := queryMillis(r, "from"); err != nil {
		return err
	} else if ok {
		fq.FromID = ids.Floor(time.UnixMilli(from))
	}
	if to, ok, err := queryMillis(r, "to"); err != nil {
		return err
	} else if ok {
		fq.ToID = ids.Floor(time.UnixMilli(to))
	}
	idsPage, err := store.FeedIDs(ctx, s.db.R, fq)
	if err != nil {
		return err
	}
	var next *string
	if len(idsPage) > limit {
		idsPage = idsPage[:limit]
		next = encodeCursor(idsPage[len(idsPage)-1])
	}
	views, err := s.renderMomentIDs(ctx, me, idsPage)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"moments": views, "next_cursor": next})
	return nil
}

func (s *Server) renderMomentIDs(ctx context.Context, me string, idList []string) ([]*momentView, error) {
	byID, err := store.GetMoments(ctx, s.db.R, idList)
	if err != nil {
		return nil, err
	}
	ms := make([]*store.Moment, 0, len(idList))
	for _, id := range idList {
		if m := byID[id]; m != nil {
			ms = append(ms, m)
		}
	}
	return s.renderer(ctx, me).moments(ms)
}

// UnseenCap caps unseen_count (widget badges show "99+" anyway).
const UnseenCap = 999

func (s *Server) handleLatestMoment(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	me := a.ID()
	ctx := r.Context()
	fq := store.FeedQuery{Me: me, ExcludeOwn: true, Limit: 1}
	author := ""
	if uid := r.URL.Query().Get("user_id"); uid != "" {
		author = ids.Normalize(uid)
		if author == "" {
			return errBadRequest("Invalid user_id")
		}
		if author == me {
			fq.ExcludeOwn = false
		} else if ok, err := store.AreFriends(ctx, s.db.R, me, author); err != nil {
			return err
		} else if !ok {
			writeJSON(w, http.StatusOK, map[string]any{"moment": nil, "unseen_count": 0})
			return nil
		}
		fq.AuthorID = author
	}
	idsPage, err := store.FeedIDs(ctx, s.db.R, fq)
	if err != nil {
		return err
	}
	unseen := 0
	if author != me {
		if unseen, err = store.UnseenCount(ctx, s.db.R, me, author, UnseenCap); err != nil {
			return err
		}
	}
	var latest *store.Moment
	if len(idsPage) > 0 {
		if latest, err = store.GetMoment(ctx, s.db.R, idsPage[0]); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	// Widgets poll this endpoint: answer 304 when nothing they show changed.
	etag := latestETag(me, author, latest, unseen, s.media.Signer.URL(""))
	w.Header().Set("ETag", etag)
	if etagMatch(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	var mv *momentView
	if latest != nil {
		if mv, err = s.renderer(ctx, me).moment(latest); err != nil {
			return err
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"moment": mv, "unseen_count": unseen})
	return nil
}

// latestETag is a weak validator over (caller, user_id param, moment id,
// caption, unseen_count) plus the signed-URL expiry bucket, so a 304 never
// leaves a client holding media URLs that have expired.
func latestETag(me, userParam string, m *store.Moment, unseen int, signedProbe string) string {
	h := sha256.New()
	fmt.Fprintf(h, "v1|%s|%s|%d|", me, userParam, unseen)
	if m != nil {
		h.Write([]byte(m.ID))
		if c := m.Caption; c != nil {
			fmt.Fprintf(h, "|%s|%s|%s|%v|%v|%s", c.Type, c.Text, c.Style, deref(c.Color), deref(c.Background), c.Data)
		}
	}
	if i := strings.Index(signedProbe, "?e="); i >= 0 {
		e := signedProbe[i+3:]
		if j := strings.IndexByte(e, '&'); j >= 0 {
			e = e[:j]
		}
		h.Write([]byte("|" + e))
	}
	return `W/"` + base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:18]) + `"`
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// etagMatch implements weak comparison against an If-None-Match header.
func etagMatch(header, etag string) bool {
	if header == "" {
		return false
	}
	want := strings.TrimPrefix(etag, "W/")
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || strings.TrimPrefix(part, "W/") == want {
			return true
		}
	}
	return false
}

func (s *Server) visibleMoment(r *http.Request, me string) (*store.Moment, error) {
	id := ids.Normalize(r.PathValue("id"))
	if id == "" {
		return nil, errMomentNotFound
	}
	m, err := store.VisibleMoment(r.Context(), s.db.R, me, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errMomentNotFound
	}
	return m, err
}

func (s *Server) handleGetMoment(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	m, err := s.visibleMoment(r, a.ID())
	if err != nil {
		return err
	}
	return s.writeMoment(w, r.Context(), a.ID(), m, http.StatusOK)
}

func (s *Server) handleDeleteMoment(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	m, err := s.visibleMoment(r, a.ID())
	if err != nil {
		return err
	}
	if m.AuthorID != a.ID() {
		return errForbidden("Only the author can delete a moment (use hide instead)")
	}
	if err := s.deleteMoment(r.Context(), m); err != nil {
		return err
	}
	return noContent(w)
}

// deleteMoment deletes a moment and its media and tells the author and every
// recipient. Used by the author and by the admin acting on a report.
func (s *Server) deleteMoment(ctx context.Context, m *store.Moment) error {
	var recipients, mediaIDs []string
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		if recipients, err = store.RecipientIDs(ctx, tx, m.ID); err != nil {
			return err
		}
		mediaIDs, err = store.DeleteMoment(ctx, tx, m.ID)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errMomentNotFound
		}
		return err
	}
	s.media.Delete(context.WithoutCancel(ctx), mediaIDs)
	s.emit("moment.deleted", map[string]any{"id": m.ID}, append(recipients, m.AuthorID)...)
	return nil
}

func (s *Server) handleHideMoment(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	m, err := s.visibleMoment(r, a.ID())
	if err != nil {
		return err
	}
	if m.AuthorID == a.ID() {
		return errBadRequest("You can't hide your own moment; delete it instead")
	}
	if err := store.HideMoment(r.Context(), s.db.W, a.ID(), m.ID); err != nil {
		return err
	}
	return noContent(w)
}

type viewsReq struct {
	IDs []string `json:"ids"`
}

func (s *Server) handleMomentViews(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	var req viewsReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if len(req.IDs) > 100 {
		return errBadRequest("At most 100 ids")
	}
	var valid []string
	for _, id := range req.IDs {
		if n := ids.Normalize(id); n != "" {
			valid = append(valid, n)
		}
	}
	if len(valid) == 0 {
		return noContent(w)
	}
	ctx := r.Context()
	now := s.now().UnixMilli()
	var fresh []store.NewView
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		fresh, err = store.MarkViewed(ctx, tx, a.ID(), valid, now)
		return err
	})
	if err != nil {
		return err
	}
	if len(fresh) > 0 {
		rr := s.renderer(ctx, a.ID())
		if err := rr.loadUsers(a.ID()); err == nil {
			me := rr.summary(a.ID())
			for _, v := range fresh {
				s.emit("moment.viewed", map[string]any{"moment_id": v.MomentID, "user": me, "viewed_at": now}, v.AuthorID)
			}
		}
	}
	return noContent(w)
}

type reactionReq struct {
	Emoji string `json:"emoji"`
}

func (s *Server) handlePutReaction(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	m, err := s.visibleMoment(r, a.ID())
	if err != nil {
		return err
	}
	var req reactionReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if !validEmoji(req.Emoji) {
		return errBadRequest("emoji must be a single emoji")
	}
	if m.AuthorID == a.ID() {
		return errForbidden("You can't react to your own moment")
	}
	ctx := r.Context()
	if err := store.SetReaction(ctx, s.db.W, m.ID, a.ID(), req.Emoji, s.now().UnixMilli()); err != nil {
		return err
	}
	rr := s.renderer(ctx, a.ID())
	if err := rr.loadUsers(a.ID()); err == nil {
		s.emit("moment.reaction", map[string]any{"moment_id": m.ID, "user": rr.summary(a.ID()), "emoji": req.Emoji}, m.AuthorID)
	}
	what := "photo"
	if m.Kind == "video" {
		what = "video"
	}
	thumb := s.media.Signer.URL(m.ThumbID)
	s.notify(push.Notification{UserIDs: []string{m.AuthorID}, Pref: push.PrefReactions, CollapseID: "reaction-" + m.ID + "-" + a.ID(),
		Payload: push.Payload{Type: "reaction", Title: a.User.DisplayName, Body: "reacted " + req.Emoji + " to your " + what,
			MomentID: strp(m.ID), PeerID: strp(a.ID()), ThumbPath: &thumb}})
	return noContent(w)
}

func (s *Server) handleDeleteReaction(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	m, err := s.visibleMoment(r, a.ID())
	if err != nil {
		return err
	}
	ctx := r.Context()
	existed, err := store.DeleteReaction(ctx, s.db.W, m.ID, a.ID())
	if err != nil {
		return err
	}
	if existed {
		rr := s.renderer(ctx, a.ID())
		if err := rr.loadUsers(a.ID()); err == nil {
			s.emit("moment.reaction", map[string]any{"moment_id": m.ID, "user": rr.summary(a.ID()), "emoji": nil}, m.AuthorID)
		}
	}
	return noContent(w)
}
