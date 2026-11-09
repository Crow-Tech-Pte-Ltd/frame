package media

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/db"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ids"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/storage"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

// Ref is the API's MediaRef object.
type Ref struct {
	URL         string `json:"url"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	ContentType string `json:"content_type"`
	DurationMS  *int64 `json:"duration_ms"`
	Bytes       int64  `json:"bytes"`
}

// Signer creates and verifies signed media URLs:
// /m/<id>?e=<unix seconds>&s=base64url(HMAC-SHA256(key, "<id>.<e>")).
type Signer struct {
	key []byte
	now func() time.Time
}

// NewSigner returns a signer; key must be ≥ 32 bytes.
func NewSigner(key []byte, now func() time.Time) *Signer {
	if now == nil {
		now = time.Now
	}
	return &Signer{key: key, now: now}
}

// Validity is how long a signed URL stays valid (at least).
const Validity = 24 * time.Hour

// expiry rounds up to the next hour so URLs are stable for an hour, which
// keeps HTTP caches and widget timelines effective; validity is 24–25 h.
func (s *Signer) expiry() int64 {
	now := s.now().Unix()
	return (now/3600+1)*3600 + int64(Validity/time.Second)
}

func (s *Signer) sign(id string, e int64) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(id))
	m.Write([]byte{'.'})
	m.Write([]byte(strconv.FormatInt(e, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// URL returns the signed relative URL for a media id.
func (s *Signer) URL(id string) string {
	e := s.expiry()
	return "/m/" + id + "?e=" + strconv.FormatInt(e, 10) + "&s=" + s.sign(id, e)
}

// Verify checks a signature and its expiry.
func (s *Signer) Verify(id, eStr, sig string) bool {
	if !ids.Valid(id) || len(eStr) == 0 || len(eStr) > 12 || len(sig) != 43 {
		return false
	}
	e, err := strconv.ParseInt(eStr, 10, 64)
	if err != nil || strconv.FormatInt(e, 10) != eStr {
		return false
	}
	now := s.now().Unix()
	if e < now || e > now+int64(2*Validity/time.Second) {
		return false
	}
	want := s.sign(id, e)
	return hmac.Equal([]byte(want), []byte(sig))
}

// Service stores media blobs and their metadata.
type Service struct {
	DB      *db.DB
	Storage storage.Storage
	Signer  *Signer
	Log     *slog.Logger
	Now     func() time.Time
}

// Ref renders a media row as a signed MediaRef.
func (s *Service) Ref(m *store.Media) *Ref {
	if m == nil {
		return nil
	}
	return &Ref{URL: s.Signer.URL(m.ID), Width: m.Width, Height: m.Height, ContentType: m.ContentType,
		DurationMS: m.DurationMS, Bytes: m.Bytes}
}

// Put records metadata and stores the blob. The row is committed first so a
// crash mid-upload leaves an unreferenced row that Sweep removes.
func (s *Service) Put(ctx context.Context, owner, contentType string, body io.ReadSeeker, size int64, w, h int, durMS *int64) (*store.Media, error) {
	m := &store.Media{ID: ids.At(s.Now()), OwnerID: &owner, ContentType: contentType, Bytes: size, Width: w, Height: h,
		DurationMS: durMS, CreatedAt: s.Now().UnixMilli()}
	if err := store.InsertMedia(ctx, s.DB.W, m); err != nil {
		return nil, err
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if err := s.Storage.Put(ctx, m.ID, body, size, contentType); err != nil {
		s.Delete(context.WithoutCancel(ctx), []string{m.ID})
		return nil, err
	}
	return m, nil
}

// Delete removes blobs and rows for media that are no longer referenced.
// Errors are logged; anything left behind is retried by Sweep.
func (s *Service) Delete(ctx context.Context, mediaIDs []string) {
	var done []string
	for _, id := range mediaIDs {
		if ref, err := store.IsMediaReferenced(ctx, s.DB.R, id); err != nil || ref {
			continue
		}
		if err := s.Storage.Delete(ctx, id); err != nil {
			s.Log.Warn("media delete failed", "media_id", id, "err", err)
			continue
		}
		done = append(done, id)
	}
	if len(done) > 0 {
		if err := store.DeleteMediaRows(ctx, s.DB.W, done); err != nil {
			s.Log.Warn("media row delete failed", "err", err)
		}
	}
}

// Sweep deletes media unreferenced for longer than grace. Returns how many
// were removed.
func (s *Service) Sweep(ctx context.Context, grace time.Duration) (int, error) {
	total := 0
	for {
		orphans, err := store.OrphanMedia(ctx, s.DB.R, s.Now().Add(-grace).UnixMilli(), 200)
		if err != nil {
			return total, err
		}
		if len(orphans) == 0 {
			return total, nil
		}
		before := total
		var done []string
		for _, id := range orphans {
			if err := s.Storage.Delete(ctx, id); err != nil {
				s.Log.Warn("orphan delete failed", "media_id", id, "err", err)
				continue
			}
			done = append(done, id)
		}
		if err := store.DeleteMediaRows(ctx, s.DB.W, done); err != nil {
			return total, err
		}
		total += len(done)
		if total == before || len(orphans) < 200 {
			return total, nil
		}
	}
}

// Open opens a media blob for serving.
func (s *Service) Open(ctx context.Context, id string) (*store.Media, storage.Object, error) {
	m, err := store.GetMedia(ctx, s.DB.R, id)
	if err != nil {
		return nil, nil, err
	}
	obj, err := s.Storage.Open(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, nil, store.ErrNotFound
		}
		return nil, nil, err
	}
	return m, obj, nil
}
