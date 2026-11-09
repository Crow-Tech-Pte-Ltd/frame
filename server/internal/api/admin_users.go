package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/auth"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ids"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/media"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

// Admin views of users. Privacy by design: only profile metadata and
// counts — never moments, media URLs, captions or messages.

type userMini struct {
	ID          string  `json:"id"`
	Username    *string `json:"username"`
	DisplayName string  `json:"display_name"`
}

func miniOf(u *store.User) *userMini {
	if u == nil {
		return nil
	}
	return &userMini{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName}
}

type adminUserView struct {
	ID           string   `json:"id"`
	Username     *string  `json:"username"`
	DisplayName  string   `json:"display_name"`
	HasAvatar    bool     `json:"has_avatar"`
	EmailMasked  *string  `json:"email_masked"`
	PhoneMasked  *string  `json:"phone_masked"`
	AuthMethods  []string `json:"auth_methods"`
	IsAdmin      bool     `json:"is_admin"`
	DisabledAt   *int64   `json:"disabled_at"`
	CreatedAt    int64    `json:"created_at"`
	LastSeenAt   *int64   `json:"last_seen_at"`
	Moments      int      `json:"moments"`
	StorageBytes int64    `json:"storage_bytes"`
	Friends      int      `json:"friends"`
}

type serverInviteRef struct {
	ID   string `json:"id"`
	Note string `json:"note"`
}

type adminUserDetailView struct {
	adminUserView
	Email        *string          `json:"email"`
	Phone        *string          `json:"phone"`
	Discoverable bool             `json:"discoverable"`
	Searchable   bool             `json:"searchable"`
	TOTPEnabled  bool             `json:"totp_enabled"`
	InvitedBy    *userMini        `json:"invited_by"`
	ServerInvite *serverInviteRef `json:"server_invite"`
}

type appSessionView struct {
	ID         string `json:"id"`
	DeviceName string `json:"device_name"`
	Platform   string `json:"platform"`
	CreatedAt  int64  `json:"created_at"`
	LastSeenAt int64  `json:"last_seen_at"`
	Push       bool   `json:"push"`
}

// maskEmail keeps the first and last character of the local part:
// alice@example.com → a•••e@example.com.
func maskEmail(e string) string {
	local, domain, ok := strings.Cut(e, "@")
	if !ok {
		return "•••"
	}
	r := []rune(local)
	switch {
	case len(r) <= 1:
		local = "•"
	case len(r) == 2:
		local = string(r[0]) + "•"
	default:
		local = string(r[0]) + "•••" + string(r[len(r)-1])
	}
	return local + "@" + domain
}

// maskPhone keeps the country prefix and the last three digits:
// +34600111222 → +34•••••••222.
func maskPhone(p string) string {
	if len(p) <= 6 {
		return "•••"
	}
	return p[:3] + strings.Repeat("•", len(p)-6) + p[len(p)-3:]
}

func authMethods(u *store.User) []string {
	m := []string{}
	if u.PasswordHash != nil {
		m = append(m, "password")
	}
	if u.AppleSub != nil {
		m = append(m, "apple")
	}
	if u.GoogleSub != nil {
		m = append(m, "google")
	}
	return m
}

func adminUserOf(au *store.AdminUser) adminUserView {
	u := au.User
	v := adminUserView{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, HasAvatar: u.AvatarMediaID != nil,
		AuthMethods: authMethods(u), IsAdmin: u.IsAdmin, DisabledAt: u.DisabledAt, CreatedAt: u.CreatedAt,
		LastSeenAt: au.LastSeenAt, Moments: au.Moments, StorageBytes: au.StorageBytes, Friends: au.Friends}
	if u.Email != nil {
		m := maskEmail(*u.Email)
		v.EmailMasked = &m
	}
	if u.Phone != nil {
		m := maskPhone(*u.Phone)
		v.PhoneMasked = &m
	}
	return v
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	q := r.URL.Query()
	page, per := pageParams(r, 25, 100)
	f := store.UserQuery{Search: q.Get("q"), Status: q.Get("status"), Sort: q.Get("sort"), Desc: q.Get("order") != "asc",
		Limit: per, Offset: (page - 1) * per}
	if len(f.Search) > 100 {
		return errBadRequest("Search is too long")
	}
	list, total, err := store.AdminListUsers(r.Context(), s.db.R, f)
	if err != nil {
		return err
	}
	out := make([]adminUserView, 0, len(list))
	for _, u := range list {
		out = append(out, adminUserOf(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out, "total": total, "page": page, "per_page": per})
	return nil
}

// adminTarget loads the {id} user of an admin route.
func (s *Server) adminTarget(r *http.Request) (*store.User, error) {
	id := ids.Normalize(r.PathValue("id"))
	if id == "" {
		return nil, errUserNotFound
	}
	u, err := store.GetUser(r.Context(), s.db.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errUserNotFound
	}
	return u, err
}

func (s *Server) handleAdminUser(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	ctx := r.Context()
	id := ids.Normalize(r.PathValue("id"))
	if id == "" {
		return errUserNotFound
	}
	au, err := store.AdminGetUser(ctx, s.db.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return errUserNotFound
	}
	if err != nil {
		return err
	}
	u := au.User
	d := adminUserDetailView{adminUserView: adminUserOf(au), Email: u.Email, Phone: u.Phone,
		Discoverable: u.Discoverable, Searchable: u.Searchable}
	if d.TOTPEnabled, err = store.TOTPEnabled(ctx, s.db.R, u.ID); err != nil {
		return err
	}
	if u.InvitedBy != nil {
		if inv, err := store.GetUser(ctx, s.db.R, *u.InvitedBy); err == nil {
			d.InvitedBy = miniOf(inv)
		}
	}
	if u.ServerInviteID != nil {
		if si, err := store.GetServerInvite(ctx, s.db.R, *u.ServerInviteID); err == nil {
			d.ServerInvite = &serverInviteRef{ID: si.ID, Note: si.Note}
		}
	}
	sessions, err := store.ListSessions(ctx, s.db.R, u.ID)
	if err != nil {
		return err
	}
	push, err := store.DeviceSessionIDs(ctx, s.db.R, u.ID)
	if err != nil {
		return err
	}
	sv := make([]appSessionView, 0, len(sessions))
	for _, x := range sessions {
		sv = append(sv, appSessionView{ID: x.ID, DeviceName: x.DeviceName, Platform: x.Platform, CreatedAt: x.CreatedAt,
			LastSeenAt: x.LastSeenAt, Push: push[x.ID]})
	}
	invites, err := s.friendInviteViews(ctx, u.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": d, "sessions": sv, "invites": invites})
	return nil
}

// handleAdminAvatar serves a user's profile picture (the only media the
// admin API exposes; it is public to every member anyway).
func (s *Server) handleAdminAvatar(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	u, err := s.adminTarget(r)
	if err != nil {
		return err
	}
	if u.AvatarMediaID == nil {
		return errNotFound("No avatar")
	}
	ctx := r.Context()
	m, obj, err := s.media.Open(ctx, *u.AvatarMediaID)
	if err != nil {
		return err
	}
	defer obj.Close()
	if m.ContentType != media.JPEG {
		return errNotFound("No avatar")
	}
	w.Header().Set("Content-Type", m.ContentType)
	w.Header().Set("Cache-Control", "private, max-age=300")
	singleRangeOnly(r)
	http.ServeContent(w, r, "", time.Time{}, obj)
	return nil
}

type adminCreateUserReq struct {
	DisplayName string  `json:"display_name"`
	Username    *string `json:"username"`
	Method      string  `json:"method"`
	Identifier  string  `json:"identifier"`
	Admin       bool    `json:"admin"`
}

func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	var req adminCreateUserReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	u, err := s.newUserFromAdminInput(req.DisplayName, req.Username, false, req.Method, req.Identifier)
	if err != nil {
		return err
	}
	ctx := r.Context()
	pw := auth.RandomPassword()
	hash, err := auth.HashPassword(ctx, pw)
	if err != nil {
		return err
	}
	u.PasswordHash = &hash
	u.IsAdmin = req.Admin
	if err := mapUserErr(s.db.Tx(ctx, func(tx *sql.Tx) error { return store.InsertUser(ctx, tx, u) }), req.Method); err != nil {
		return err
	}
	detail := ""
	if u.IsAdmin {
		detail = "as admin"
	}
	s.audit(ctx, r, a.User, "user.create", UserTarget(u), detail)
	au := &store.AdminUser{User: u}
	writeJSON(w, http.StatusCreated, map[string]any{"user": adminUserOf(au), "password": pw})
	return nil
}

func (s *Server) handleAdminResetPassword(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	u, err := s.adminTarget(r)
	if err != nil {
		return err
	}
	if u.ID == a.User.ID {
		return apiErr(http.StatusConflict, "self_action", "Change your own password under Security")
	}
	if u.Email == nil && u.Phone == nil {
		return errBadRequest("This account signs in with Apple/Google only and has no e-mail or phone to sign in with")
	}
	ctx := r.Context()
	pw := auth.RandomPassword()
	hash, err := auth.HashPassword(ctx, pw)
	if err != nil {
		return err
	}
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := store.SetPassword(ctx, tx, u.ID, hash, s.now().UnixMilli()); err != nil {
			return err
		}
		if err := store.DeleteAdminSessions(ctx, tx, u.ID, ""); err != nil {
			return err
		}
		_, err := store.DeleteUserSessions(ctx, tx, u.ID, "")
		return err
	})
	if err != nil {
		return err
	}
	s.hub.CloseUser(u.ID)
	s.audit(ctx, r, a.User, "user.reset_password", UserTarget(u), "")
	writeJSON(w, http.StatusOK, map[string]any{"password": pw})
	return nil
}

// SuspendUser suspends or reinstates a user. Suspending signs out every
// app and admin session (closing SSE streams); content stays. The revoked
// app tokens then get 401 account_disabled until the user is reinstated.
func (s *Server) SuspendUser(ctx context.Context, userID string, suspend bool) error {
	now := s.now().UnixMilli()
	var at *int64
	if suspend {
		at = &now
	}
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := store.SetDisabled(ctx, tx, userID, at, now); err != nil {
			return err
		}
		if !suspend {
			// Old tokens become plain invalid tokens (401 unauthorized).
			return store.DeleteRevokedSessions(ctx, tx, userID)
		}
		if err := store.DeleteAdminSessions(ctx, tx, userID, ""); err != nil {
			return err
		}
		// Remember why the app sessions end, so their next request gets
		// 401 account_disabled and the app can say so (API v1.2.1).
		if err := store.MarkSessionsRevoked(ctx, tx, userID, store.RevokedDisabled, now); err != nil {
			return err
		}
		_, err := store.DeleteUserSessions(ctx, tx, userID, "")
		return err
	})
	if err != nil {
		return err
	}
	if suspend {
		s.hub.CloseUser(userID)
	}
	return nil
}

func (s *Server) handleAdminDisable(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	return s.adminSetDisabled(w, r, a, true)
}

func (s *Server) handleAdminEnable(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	return s.adminSetDisabled(w, r, a, false)
}

func (s *Server) adminSetDisabled(w http.ResponseWriter, r *http.Request, a *adminCtx, suspend bool) error {
	u, err := s.adminTarget(r)
	if err != nil {
		return err
	}
	if u.ID == a.User.ID {
		return errSelfAction
	}
	ctx := r.Context()
	if suspend == u.Disabled() {
		return noContent(w)
	}
	if err := s.SuspendUser(ctx, u.ID, suspend); err != nil {
		return err
	}
	action := "user.enable"
	if suspend {
		action = "user.disable"
	}
	s.audit(ctx, r, a.User, action, UserTarget(u), "")
	return noContent(w)
}

// SetAdmin grants or removes admin rights, refusing to remove the last
// active admin. Removing rights signs out the user's admin sessions.
func (s *Server) SetAdmin(ctx context.Context, userID string, admin bool) error {
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		if !admin {
			u, err := store.GetUser(ctx, tx, userID)
			if err != nil {
				return err
			}
			if u.IsAdmin && !u.Disabled() {
				n, err := store.CountActiveAdmins(ctx, tx)
				if err != nil {
					return err
				}
				if n <= 1 {
					return errLastAdmin
				}
			}
			if err := store.DeleteAdminSessions(ctx, tx, userID, ""); err != nil {
				return err
			}
		}
		return store.SetAdmin(ctx, tx, userID, admin, s.now().UnixMilli())
	})
}

func (s *Server) handleAdminPromote(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	return s.adminSetAdmin(w, r, a, true)
}

func (s *Server) handleAdminDemote(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	return s.adminSetAdmin(w, r, a, false)
}

func (s *Server) adminSetAdmin(w http.ResponseWriter, r *http.Request, a *adminCtx, admin bool) error {
	u, err := s.adminTarget(r)
	if err != nil {
		return err
	}
	if u.ID == a.User.ID {
		return errSelfAction
	}
	if admin && u.Disabled() {
		return errBadRequest("Unsuspend this account first")
	}
	if u.IsAdmin == admin {
		return noContent(w)
	}
	ctx := r.Context()
	if err := s.SetAdmin(ctx, u.ID, admin); err != nil {
		return err
	}
	action := "user.demote"
	if admin {
		action = "user.promote"
	}
	s.audit(ctx, r, a.User, action, UserTarget(u), "")
	return noContent(w)
}

func (s *Server) handleAdminRevokeSession(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	u, err := s.adminTarget(r)
	if err != nil {
		return err
	}
	sid := ids.Normalize(r.PathValue("sid"))
	ctx := r.Context()
	if sid == "" {
		return errNotFound("Session not found")
	}
	if err := store.DeleteSession(ctx, s.db.W, u.ID, sid); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errNotFound("Session not found")
		}
		return err
	}
	s.hub.CloseSession(u.ID, sid)
	s.audit(ctx, r, a.User, "user.session_revoke", UserTarget(u), "")
	return noContent(w)
}

func (s *Server) handleAdminRevokeSessions(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	u, err := s.adminTarget(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	revoked, err := store.DeleteUserSessions(ctx, s.db.W, u.ID, "")
	if err != nil {
		return err
	}
	for _, sid := range revoked {
		s.hub.CloseSession(u.ID, sid)
	}
	s.audit(ctx, r, a.User, "user.sessions_revoke", UserTarget(u), "")
	return noContent(w)
}

type adminDeleteReq struct {
	Confirm string `json:"confirm"`
}

func (s *Server) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	u, err := s.adminTarget(r)
	if err != nil {
		return err
	}
	var req adminDeleteReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	want := u.ID
	if u.Username != nil {
		want = *u.Username
	}
	if strings.TrimPrefix(strings.TrimSpace(req.Confirm), "@") != want {
		return apiErr(http.StatusBadRequest, "confirm_mismatch", "Type the username exactly to confirm")
	}
	if u.ID == a.User.ID {
		return errSelfAction
	}
	ctx := context.WithoutCancel(r.Context())
	if u.IsAdmin && !u.Disabled() {
		if n, err := store.CountActiveAdmins(ctx, s.db.R); err != nil {
			return err
		} else if n <= 1 {
			return errLastAdmin
		}
	}
	target := UserTarget(u)
	if err := s.DeleteAccount(ctx, u.ID); err != nil {
		return err
	}
	s.audit(ctx, r, a.User, "user.delete", target, "")
	return noContent(w)
}
