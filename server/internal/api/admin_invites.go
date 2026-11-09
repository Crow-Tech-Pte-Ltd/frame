package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ids"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

const (
	maxServerInviteUses = 1000
	maxServerInviteDays = 365
	maxInviteNote       = 200
)

const serverInviteAAD = "frame-server-invite-v1"

// CreateServerInvite creates an admin server invite (registration only, no
// friendship). expires nil = never. Used by the panel and the CLI.
func (s *Server) CreateServerInvite(ctx context.Context, createdBy *string, maxUses int, expires *time.Time, note string) (*store.ServerInvite, string, error) {
	if maxUses < 1 || maxUses > maxServerInviteUses {
		return nil, "", errBadRequest("max_uses must be between 1 and 1000")
	}
	code := ids.Token(16) // 128-bit, 22 chars base64url, same format as member invites
	enc, err := seal(s.inviteKey, []byte(code), serverInviteAAD)
	if err != nil {
		return nil, "", err
	}
	now := s.now()
	inv := &store.ServerInvite{ID: ids.At(now), CodeHash: inviteHash(code), CodeEnc: enc, CreatedBy: createdBy,
		Note: cleanNote(note), MaxUses: maxUses, CreatedAt: now.UnixMilli()}
	if expires != nil {
		e := expires.UnixMilli()
		inv.ExpiresAt = &e
	}
	if err := store.InsertServerInvite(ctx, s.db.W, inv); err != nil {
		return nil, "", err
	}
	return inv, code, nil
}

func cleanNote(n string) string {
	n = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, n))
	if utf8.RuneCountInString(n) > maxInviteNote {
		n = string([]rune(n)[:maxInviteNote])
	}
	return n
}

// inviteServerURL is the server address embedded in invite links:
// FRAME_PUBLIC_URL, else the address the request came in on.
func (s *Server) inviteServerURL(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return s.cfg.PublicURL
	}
	return s.publicURL(r)
}

type registeredView struct {
	User *userMini `json:"user"`
	At   int64     `json:"at"`
}

type serverInviteView struct {
	ID         string           `json:"id"`
	Code       *string          `json:"code"`
	URL        *string          `json:"url"`
	Note       string           `json:"note"`
	MaxUses    int              `json:"max_uses"`
	Uses       int              `json:"uses"`
	CreatedAt  int64            `json:"created_at"`
	ExpiresAt  *int64           `json:"expires_at"`
	RevokedAt  *int64           `json:"revoked_at"`
	Status     string           `json:"status"`
	CreatedBy  *userMini        `json:"created_by"`
	Registered []registeredView `json:"registered"`
}

func (s *Server) serverInviteViews(ctx context.Context, r *http.Request, list []*store.ServerInvite) ([]serverInviteView, error) {
	var invIDs, userIDs []string
	for _, i := range list {
		invIDs = append(invIDs, i.ID)
		if i.CreatedBy != nil {
			userIDs = append(userIDs, *i.CreatedBy)
		}
	}
	regs, err := store.RegistrationsFor(ctx, s.db.R, invIDs)
	if err != nil {
		return nil, err
	}
	for _, x := range regs {
		userIDs = append(userIDs, x.UserID)
	}
	users, err := store.GetUsers(ctx, s.db.R, userIDs)
	if err != nil {
		return nil, err
	}
	byInvite := map[string][]registeredView{}
	for _, x := range regs {
		if u := users[x.UserID]; u != nil {
			byInvite[x.InviteID] = append(byInvite[x.InviteID], registeredView{User: miniOf(u), At: x.At})
		}
	}
	now := s.now().UnixMilli()
	base := s.inviteServerURL(r)
	out := make([]serverInviteView, 0, len(list))
	for _, i := range list {
		v := serverInviteView{ID: i.ID, Note: i.Note, MaxUses: i.MaxUses, Uses: i.Uses, CreatedAt: i.CreatedAt,
			ExpiresAt: i.ExpiresAt, RevokedAt: i.RevokedAt, Status: i.Status(now), Registered: byInvite[i.ID]}
		if v.Registered == nil {
			v.Registered = []registeredView{}
		}
		if i.CreatedBy != nil {
			v.CreatedBy = miniOf(users[*i.CreatedBy])
		}
		if v.Status == "active" {
			if code, ok := unseal(s.inviteKey, i.CodeEnc, serverInviteAAD); ok {
				c := string(code)
				link := InviteURL(s.cfg.InviteBase, base, c)
				v.Code, v.URL = &c, &link
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) handleAdminInvites(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	ctx := r.Context()
	activeOnly := r.URL.Query().Get("status") != "all"
	list, err := store.ListServerInvites(ctx, s.db.R, activeOnly, s.now().UnixMilli(), 500)
	if err != nil {
		return err
	}
	views, err := s.serverInviteViews(ctx, r, list)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"invites": views})
	return nil
}

type createServerInviteReq struct {
	MaxUses       *int            `json:"max_uses"`
	ExpiresInDays json.RawMessage `json:"expires_in_days"` // absent → 7, null → never
	Note          string          `json:"note"`
}

func (s *Server) handleAdminCreateInvite(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	var req createServerInviteReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	uses := 1
	if req.MaxUses != nil {
		uses = *req.MaxUses
	}
	days := 7
	never := false
	if raw := strings.TrimSpace(string(req.ExpiresInDays)); raw == "null" {
		never = true
	} else if raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxServerInviteDays {
			return errBadRequest("expires_in_days must be between 1 and 365, or null for never")
		}
		days = n
	}
	var exp *time.Time
	if !never {
		t := s.now().Add(time.Duration(days) * 24 * time.Hour)
		exp = &t
	}
	if utf8.RuneCountInString(req.Note) > maxInviteNote {
		return errBadRequest("note must be at most 200 characters")
	}
	ctx := r.Context()
	creator := a.User.ID
	inv, _, err := s.CreateServerInvite(ctx, &creator, uses, exp, req.Note)
	if err != nil {
		return err
	}
	views, err := s.serverInviteViews(ctx, r, []*store.ServerInvite{inv})
	if err != nil {
		return err
	}
	detail := strconv.Itoa(uses) + " use(s), "
	if never {
		detail += "never expires"
	} else {
		detail += "expires in " + strconv.Itoa(days) + " day(s)"
	}
	if inv.Note != "" {
		detail += ", note: " + inv.Note
	}
	s.audit(ctx, r, a.User, "invite.create", &AuditTarget{Type: "invite", ID: inv.ID, Label: inv.Note}, detail)
	writeJSON(w, http.StatusCreated, map[string]any{"invite": views[0]})
	return nil
}

func (s *Server) handleAdminRevokeInvite(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	id := ids.Normalize(r.PathValue("id"))
	if id == "" {
		return errNotFound("Invite not found")
	}
	ctx := r.Context()
	inv, err := store.GetServerInvite(ctx, s.db.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return errNotFound("Invite not found")
	}
	if err != nil {
		return err
	}
	if err := store.RevokeServerInvite(ctx, s.db.W, id, s.now().UnixMilli()); err != nil {
		return err
	}
	s.audit(ctx, r, a.User, "invite.revoke", &AuditTarget{Type: "invite", ID: inv.ID, Label: inv.Note}, "")
	return noContent(w)
}

type friendInviteView struct {
	ID        string    `json:"id"`
	Owner     *userMini `json:"owner"`
	CreatedAt int64     `json:"created_at"`
	ExpiresAt int64     `json:"expires_at"`
	Uses      int       `json:"uses"`
}

// friendInviteViews lists members' invite links (of one user, or all). The
// id is the hex SHA-256 of the code: enough to revoke, useless to redeem.
func (s *Server) friendInviteViews(ctx context.Context, userID string) ([]friendInviteView, error) {
	list, err := store.ListFriendInvites(ctx, s.db.R, userID, s.now().UnixMilli(), 1000)
	if err != nil {
		return nil, err
	}
	var uids []string
	for _, f := range list {
		uids = append(uids, f.UserID)
	}
	users, err := store.GetUsers(ctx, s.db.R, uids)
	if err != nil {
		return nil, err
	}
	out := make([]friendInviteView, 0, len(list))
	for _, f := range list {
		owner := miniOf(users[f.UserID])
		if owner == nil {
			owner = &userMini{ID: f.UserID, DisplayName: "Deleted user"}
		}
		out = append(out, friendInviteView{ID: hex.EncodeToString(f.CodeHash), Owner: owner, CreatedAt: f.CreatedAt,
			ExpiresAt: f.ExpiresAt, Uses: f.Uses})
	}
	return out, nil
}

func (s *Server) handleAdminFriendInvites(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	views, err := s.friendInviteViews(r.Context(), "")
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"invites": views})
	return nil
}

func (s *Server) handleAdminRevokeFriendInvite(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	h, err := hex.DecodeString(r.PathValue("id"))
	if err != nil || len(h) != 32 {
		return errNotFound("Invite not found")
	}
	ctx := r.Context()
	var owner *store.User
	if uid, err := store.FriendInviteOwner(ctx, s.db.R, h); err == nil {
		owner, _ = store.GetUser(ctx, s.db.R, uid)
	}
	ok, err := store.DeleteInviteByHash(ctx, s.db.W, h)
	if err != nil {
		return err
	}
	if !ok {
		return errNotFound("Invite not found")
	}
	var target *AuditTarget
	if owner != nil {
		target = UserTarget(owner)
	}
	s.audit(ctx, r, a.User, "friend_invite.revoke", target, "")
	return noContent(w)
}
