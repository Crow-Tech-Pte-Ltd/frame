package api

import (
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ids"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

// visibleUser loads a user unless they blocked me (treated as not found).
func (s *Server) visibleUser(r *http.Request, me, id string) (*store.User, string, error) {
	id = ids.Normalize(id)
	if id == "" {
		return nil, "", errUserNotFound
	}
	u, err := store.GetUser(r.Context(), s.db.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, "", errUserNotFound
	}
	if err != nil {
		return nil, "", err
	}
	rel, blockedMe, err := store.Relation(r.Context(), s.db.R, me, id)
	if err != nil {
		return nil, "", err
	}
	if blockedMe && rel != store.RelBlocked {
		return nil, "", errUserNotFound
	}
	return u, rel, nil
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	u, rel, err := s.visibleUser(r, a.ID(), r.PathValue("id"))
	if err != nil {
		return err
	}
	rr := s.renderer(r.Context(), a.ID())
	rr.addUser(u)
	if err := rr.loadUsers(u.ID); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, userRelView{User: rr.summary(u.ID), Friendship: rel})
	return nil
}

func validUsernameChars(q string) bool {
	for i := 0; i < len(q); i++ {
		c := q[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_') {
			return false
		}
	}
	return true
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	q = strings.TrimPrefix(q, "@")
	if len([]rune(q)) < 2 {
		return errBadRequest("q must be at least 2 characters")
	}
	out := []userRelView{}
	if len(q) <= 20 && validUsernameChars(q) {
		ctx := r.Context()
		users, err := store.SearchUsers(ctx, s.db.R, a.ID(), q, 20)
		if err != nil {
			return err
		}
		rr := s.renderer(ctx, a.ID())
		var idsList []string
		for _, u := range users {
			rr.addUser(u)
			idsList = append(idsList, u.ID)
		}
		if err := rr.loadUsers(idsList...); err != nil {
			return err
		}
		rels, err := store.Relations(ctx, s.db.R, a.ID(), idsList)
		if err != nil {
			return err
		}
		for _, u := range users {
			out = append(out, userRelView{User: rr.summary(u.ID), Friendship: rels[u.ID]})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
	return nil
}

type contactsReq struct {
	Hashes []string `json:"hashes"`
}

type contactMatchView struct {
	Hash       string       `json:"hash"`
	User       *userSummary `json:"user"`
	Friendship string       `json:"friendship"`
}

func (s *Server) handleContactsMatch(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	if err := s.limit(s.rl.contacts, a.ID()); err != nil {
		return err
	}
	var req contactsReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if len(req.Hashes) > 2000 {
		return errBadRequest("At most 2000 hashes per request")
	}
	hashes := make([][]byte, 0, len(req.Hashes))
	seen := map[string]bool{}
	for _, h := range req.Hashes {
		h = strings.ToLower(h)
		b, err := hex.DecodeString(h)
		if err != nil || len(b) != 32 {
			return errBadRequest("Each hash must be 64 hex characters")
		}
		if !seen[h] {
			seen[h] = true
			hashes = append(hashes, b)
		}
	}
	ctx := r.Context()
	matches, err := store.MatchContacts(ctx, s.db.R, a.ID(), hashes)
	if err != nil {
		return err
	}
	var uids []string
	for _, m := range matches {
		uids = append(uids, m.UserID)
	}
	rr := s.renderer(ctx, a.ID())
	if err := rr.loadUsers(uids...); err != nil {
		return err
	}
	rels, err := store.Relations(ctx, s.db.R, a.ID(), uids)
	if err != nil {
		return err
	}
	out := make([]contactMatchView, 0, len(matches))
	for _, m := range matches {
		out = append(out, contactMatchView{Hash: hex.EncodeToString(m.Hash), User: rr.summary(m.UserID), Friendship: rels[m.UserID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"matches": out})
	return nil
}
