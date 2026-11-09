package api

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/auth"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/media"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

func (s *Server) writeMe(w http.ResponseWriter, ctx context.Context, userID string) error {
	u, err := store.GetUser(ctx, s.db.R, userID)
	if err != nil {
		return err
	}
	me, err := s.renderer(ctx, userID).meView(u)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, me)
	return nil
}

func (s *Server) handleGetMe(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	return s.writeMe(w, r.Context(), a.ID())
}

type patchMeReq struct {
	DisplayName  *string `json:"display_name"`
	Username     *string `json:"username"`
	Discoverable *bool   `json:"discoverable"`
	Searchable   *bool   `json:"searchable"`
	ReadReceipts *bool   `json:"read_receipts"`
}

func (s *Server) handlePatchMe(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	var req patchMeReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	upd := store.ProfileUpdate{Discoverable: req.Discoverable, Searchable: req.Searchable, ReadReceipts: req.ReadReceipts}
	profileChanged := false
	if req.DisplayName != nil {
		n, ok := auth.CleanDisplayName(*req.DisplayName)
		if !ok {
			return errBadRequest("Display name must be 1–40 characters")
		}
		upd.DisplayName = &n
		profileChanged = profileChanged || n != a.User.DisplayName
	}
	if req.Username != nil {
		u := strings.ToLower(strings.TrimSpace(*req.Username))
		if msg := auth.UsernameError(u); msg != "" {
			return errBadRequest(msg)
		}
		upd.Username = &u
		profileChanged = profileChanged || a.User.Username == nil || *a.User.Username != u
	}
	ctx := r.Context()
	if err := store.UpdateProfile(ctx, s.db.W, a.ID(), upd, s.now().UnixMilli()); err != nil {
		if errors.Is(err, store.ErrUsernameTaken) {
			return apiErr(http.StatusConflict, "username_taken", "This username is taken")
		}
		return err
	}
	if profileChanged {
		s.announceProfile(ctx, a.ID())
	}
	return s.writeMe(w, ctx, a.ID())
}

// announceProfile sends profile.updated to the user's friends.
func (s *Server) announceProfile(ctx context.Context, userID string) {
	friends, err := store.FriendIDs(ctx, s.db.R, userID)
	if err != nil || len(friends) == 0 {
		return
	}
	rr := s.renderer(ctx, userID)
	if err := rr.loadUsers(userID); err != nil {
		return
	}
	s.emit("profile.updated", map[string]any{"user": rr.summary(userID)}, friends...)
}

const (
	maxAvatarBytes = 2 << 20
	maxAvatarDim   = 1024
)

func (s *Server) handlePutAvatar(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != media.JPEG {
		return errUnsupportedMedia
	}
	if r.ContentLength > maxAvatarBytes {
		return errPayloadTooLarge
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAvatarBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return errPayloadTooLarge
		}
		return err
	}
	if media.Sniff(body) != media.JPEG {
		return errUnsupportedMedia
	}
	wd, ht, ok := media.JPEGSize(body)
	if !ok {
		return apiErr(http.StatusUnsupportedMediaType, "unsupported_media", "Malformed JPEG")
	}
	if wd > maxAvatarDim || ht > maxAvatarDim {
		return errBadRequest("Avatar must be at most 1024×1024")
	}
	ctx := r.Context()
	m, err := s.media.Put(ctx, a.ID(), media.JPEG, bytes.NewReader(body), int64(len(body)), wd, ht, nil)
	if err != nil {
		return err
	}
	old, err := store.SetAvatar(ctx, s.db.W, a.ID(), &m.ID, s.now().UnixMilli())
	if err != nil {
		return err
	}
	if old != nil {
		s.media.Delete(context.WithoutCancel(ctx), []string{*old})
	}
	s.announceProfile(ctx, a.ID())
	return s.writeMe(w, ctx, a.ID())
}

func (s *Server) handleDeleteAvatar(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	ctx := r.Context()
	old, err := store.SetAvatar(ctx, s.db.W, a.ID(), nil, s.now().UnixMilli())
	if err != nil {
		return err
	}
	if old != nil {
		s.media.Delete(context.WithoutCancel(ctx), []string{*old})
		s.announceProfile(ctx, a.ID())
	}
	return s.writeMe(w, ctx, a.ID())
}

type deleteMeReq struct {
	Confirm string `json:"confirm"`
}

func (s *Server) handleDeleteMe(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	var req deleteMeReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Confirm != "DELETE" {
		return errBadRequest(`Send {"confirm":"DELETE"} to delete your account`)
	}
	if err := s.DeleteAccount(context.WithoutCancel(r.Context()), a.ID()); err != nil {
		return err
	}
	return noContent(w)
}

// DeleteAccount irreversibly deletes a user with all their moments, media,
// messages, friendships and sessions (used by the API and the admin CLI).
func (s *Server) DeleteAccount(ctx context.Context, userID string) error {
	friends, err := store.FriendIDs(ctx, s.db.R, userID)
	if err != nil {
		return err
	}
	var mediaIDs []string
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		if mediaIDs, err = store.MediaOwnedBy(ctx, tx, userID); err != nil {
			return err
		}
		return store.DeleteUser(ctx, tx, userID)
	})
	if err != nil {
		return err
	}
	s.hub.CloseUser(userID)
	s.media.Delete(ctx, mediaIDs)
	s.emit("friend.removed", map[string]any{"user_id": userID}, friends...)
	s.log.Info("account deleted", "user", userID, "media", len(mediaIDs))
	return nil
}
