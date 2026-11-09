package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ids"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/push"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

func (s *Server) handleFriends(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	ctx := r.Context()
	fs, err := store.ListFriends(ctx, s.db.R, a.ID())
	if err != nil {
		return err
	}
	var uids []string
	for _, f := range fs {
		uids = append(uids, f.UserID)
	}
	rr := s.renderer(ctx, a.ID())
	if err := rr.loadUsers(uids...); err != nil {
		return err
	}
	out := make([]friendView, 0, len(fs))
	for _, f := range fs {
		out = append(out, friendView{User: rr.summary(f.UserID), Since: f.Since})
	}
	writeJSON(w, http.StatusOK, map[string]any{"friends": out})
	return nil
}

func (s *Server) handleFriendRequests(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	ctx := r.Context()
	in, outReqs, err := store.ListRequests(ctx, s.db.R, a.ID())
	if err != nil {
		return err
	}
	rr := s.renderer(ctx, a.ID())
	inV, err := rr.requests(in)
	if err != nil {
		return err
	}
	outV, err := rr.requests(outReqs)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"incoming": inV, "outgoing": outV})
	return nil
}

// checkFriendLimit enforces FRAME_MAX_FRIENDS for both users.
func (s *Server) checkFriendLimit(ctx context.Context, q store.Q, users ...string) error {
	maxFriends := s.live().MaxFriends
	if maxFriends <= 0 {
		return nil
	}
	for _, u := range users {
		n, err := store.CountFriends(ctx, q, u)
		if err != nil {
			return err
		}
		if n >= maxFriends {
			return apiErr(http.StatusForbidden, "friend_limit", "Friend limit reached on this server")
		}
	}
	return nil
}

type userIDReq struct {
	UserID string `json:"user_id"`
}

func (s *Server) handleCreateFriendRequest(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	var req userIDReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	me := a.ID()
	target := ids.Normalize(req.UserID)
	if target == "" {
		return errBadRequest("user_id is required")
	}
	if target == me {
		return errBadRequest("You can't befriend yourself")
	}
	_, rel, err := s.visibleUser(r, me, target)
	if err != nil {
		return err
	}
	switch rel {
	case store.RelBlocked:
		return errBlocked
	case store.RelFriends:
		return errConflict("You are already friends")
	}
	ctx := r.Context()
	var fr *store.FriendRequest
	var autoAccepted bool
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		// Re-check inside the transaction (state may have changed).
		if b, err := store.BlockedEither(ctx, tx, me, target); err != nil || b {
			if b {
				return errBlocked
			}
			return err
		}
		if _, err := store.RequestBetween(ctx, tx, target, me); err == nil {
			if err := s.checkFriendLimit(ctx, tx, me, target); err != nil {
				return err
			}
			autoAccepted = true
			_, err := store.AddFriendship(ctx, tx, me, target, s.now().UnixMilli())
			return err
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err := s.checkFriendLimit(ctx, tx, me); err != nil {
			return err
		}
		fr, err = store.InsertRequest(ctx, tx, &store.FriendRequest{ID: ids.At(s.now()), FromID: me, ToID: target, CreatedAt: s.now().UnixMilli()})
		return err
	})
	if err != nil {
		return err
	}
	if autoAccepted {
		s.announceFriendship(ctx, me, target)
		s.notify(push.Notification{UserIDs: []string{target}, Pref: push.PrefFriends,
			Payload: push.Payload{Type: "friend_accepted", Title: a.User.DisplayName, Body: "accepted your friend request", PeerID: strp(me)}})
		writeJSON(w, http.StatusOK, map[string]any{"status": "friends", "request": nil})
		return nil
	}
	rr := s.renderer(ctx, me)
	views, err := rr.requests([]store.FriendRequest{*fr})
	if err != nil {
		return err
	}
	ev := map[string]any{"request": views[0]}
	s.emit("friend.request", ev, target, me)
	s.notify(push.Notification{UserIDs: []string{target}, Pref: push.PrefFriends, CollapseID: "friend-" + me,
		Payload: push.Payload{Type: "friend_request", Title: a.User.DisplayName, Body: "wants to be your friend", PeerID: strp(me)}})
	writeJSON(w, http.StatusOK, map[string]any{"status": "requested", "request": views[0]})
	return nil
}

func (s *Server) loadRequest(r *http.Request) (*store.FriendRequest, error) {
	id := ids.Normalize(r.PathValue("id"))
	if id == "" {
		return nil, errNotFound("Friend request not found")
	}
	fr, err := store.GetRequest(r.Context(), s.db.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errNotFound("Friend request not found")
	}
	return fr, err
}

func (s *Server) handleAcceptRequest(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	fr, err := s.loadRequest(r)
	if err != nil {
		return err
	}
	if fr.ToID != a.ID() {
		return errNotFound("Friend request not found")
	}
	ctx := r.Context()
	var since int64
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := store.GetRequest(ctx, tx, fr.ID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return errNotFound("Friend request not found")
			}
			return err
		}
		if err := s.checkFriendLimit(ctx, tx, fr.FromID, fr.ToID); err != nil {
			return err
		}
		since, err = store.AddFriendship(ctx, tx, fr.FromID, fr.ToID, s.now().UnixMilli())
		return err
	})
	if err != nil {
		return err
	}
	s.announceFriendship(ctx, fr.FromID, fr.ToID)
	s.notify(push.Notification{UserIDs: []string{fr.FromID}, Pref: push.PrefFriends,
		Payload: push.Payload{Type: "friend_accepted", Title: a.User.DisplayName, Body: "accepted your friend request", PeerID: strp(a.ID())}})
	rr := s.renderer(ctx, a.ID())
	if err := rr.loadUsers(fr.FromID); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"friend": friendView{User: rr.summary(fr.FromID), Since: since}})
	return nil
}

func (s *Server) handleDeclineRequest(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	fr, err := s.loadRequest(r)
	if err != nil {
		return err
	}
	if fr.ToID != a.ID() {
		return errNotFound("Friend request not found")
	}
	if err := store.DeleteRequest(r.Context(), s.db.W, fr.ID); err != nil {
		return err
	}
	return noContent(w)
}

func (s *Server) handleCancelRequest(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	fr, err := s.loadRequest(r)
	if err != nil {
		return err
	}
	if fr.FromID != a.ID() {
		return errNotFound("Friend request not found")
	}
	if err := store.DeleteRequest(r.Context(), s.db.W, fr.ID); err != nil {
		return err
	}
	return noContent(w)
}

func (s *Server) handleUnfriend(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	other := ids.Normalize(r.PathValue("user_id"))
	if other == "" {
		return errUserNotFound
	}
	ctx := r.Context()
	var removed bool
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		if removed, err = store.RemoveFriendship(ctx, tx, a.ID(), other); err != nil || !removed {
			return err
		}
		// Old invite links of mine must not silently re-befriend them.
		return store.RecordRemoval(ctx, tx, a.ID(), other, s.now().UnixMilli())
	})
	if err != nil {
		return err
	}
	if removed {
		s.emit("friend.removed", map[string]any{"user_id": a.ID()}, other)
		s.emit("friend.removed", map[string]any{"user_id": other}, a.ID())
	}
	return noContent(w)
}

func (s *Server) handleBlocks(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	ctx := r.Context()
	blocked, err := store.ListBlocked(ctx, s.db.R, a.ID())
	if err != nil {
		return err
	}
	rr := s.renderer(ctx, a.ID())
	if err := rr.loadUsers(blocked...); err != nil {
		return err
	}
	out := make([]*userSummary, 0, len(blocked))
	for _, id := range blocked {
		out = append(out, rr.summary(id))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
	return nil
}

func (s *Server) handleBlock(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	var req userIDReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	me := a.ID()
	target := ids.Normalize(req.UserID)
	if target == "" {
		return errBadRequest("user_id is required")
	}
	if target == me {
		return errBadRequest("You can't block yourself")
	}
	ctx := r.Context()
	if _, err := store.GetUser(ctx, s.db.R, target); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errUserNotFound
		}
		return err
	}
	var wereFriends bool
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		wereFriends, err = s.blockTx(ctx, tx, me, target)
		return err
	})
	if err != nil {
		return err
	}
	s.afterBlock(me, target, wereFriends)
	return noContent(w)
}

// blockTx blocks target for me inside tx: it also unfriends them, remembers
// the removal (so old invite links don't re-befriend) and drops pending
// requests. Call afterBlock once the transaction has committed.
func (s *Server) blockTx(ctx context.Context, tx *sql.Tx, me, target string) (wereFriends bool, err error) {
	if err := store.InsertBlock(ctx, tx, me, target, s.now().UnixMilli()); err != nil {
		return false, err
	}
	if wereFriends, err = store.RemoveFriendship(ctx, tx, me, target); err != nil {
		return false, err
	}
	if err := store.RecordRemoval(ctx, tx, me, target, s.now().UnixMilli()); err != nil {
		return false, err
	}
	return wereFriends, store.DeleteRequestsBetween(ctx, tx, me, target)
}

// afterBlock tells both sides about a friendship a block ended.
func (s *Server) afterBlock(me, target string, wereFriends bool) {
	if wereFriends {
		s.emit("friend.removed", map[string]any{"user_id": me}, target)
		s.emit("friend.removed", map[string]any{"user_id": target}, me)
	}
}

func (s *Server) handleUnblock(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	other := ids.Normalize(r.PathValue("user_id"))
	if other == "" {
		return errUserNotFound
	}
	if err := store.DeleteBlock(r.Context(), s.db.W, a.ID(), other); err != nil {
		return err
	}
	return noContent(w)
}
