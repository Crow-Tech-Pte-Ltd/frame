package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/auth"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ids"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/oidc"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/push"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ratelimit"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

type deviceIn struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

func (d *deviceIn) clean() (name, platform string, err error) {
	if d == nil {
		return "Unknown device", "unknown", nil
	}
	switch d.Platform {
	case "ios", "android":
		platform = d.Platform
	case "":
		platform = "unknown"
	default:
		return "", "", errBadRequest(`device.platform must be "ios" or "android"`)
	}
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, d.Name))
	if name == "" {
		name = "Unknown device"
	}
	if utf8.RuneCountInString(name) > 64 {
		name = string([]rune(name)[:64])
	}
	return name, platform, nil
}

type authResponse struct {
	Token string  `json:"token"`
	User  *meView `json:"user"`
	IsNew bool    `json:"is_new"`
}

// newSession creates a session row and returns its bearer token.
func (s *Server) newSession(ctx context.Context, q store.Q, userID string, dev *deviceIn) (string, error) {
	name, platform, err := dev.clean()
	if err != nil {
		return "", err
	}
	tok, hash := auth.NewSessionToken()
	now := s.now().UnixMilli()
	sess := &store.Session{ID: ids.At(s.now()), UserID: userID, DeviceName: name, Platform: platform, CreatedAt: now, LastSeenAt: now}
	if err := store.InsertSession(ctx, q, sess, hash); err != nil {
		return "", err
	}
	return tok, nil
}

func (s *Server) respondAuth(w http.ResponseWriter, ctx context.Context, userID, token string, isNew bool) error {
	u, err := store.GetUser(ctx, s.db.R, userID)
	if err != nil {
		return err
	}
	me, err := s.renderer(ctx, userID).meView(u)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, authResponse{Token: token, User: me, IsNew: isNew})
	return nil
}

func inviteHash(code string) []byte {
	h := sha256.Sum256([]byte(code))
	return h[:]
}

func validInviteCode(code string) bool {
	if len(code) != 22 {
		return false
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

var (
	errInviteRequired     = apiErr(http.StatusForbidden, "invite_required", "This server is invite-only: you need an invite link from a member or the admin")
	errInviteInvalid      = apiErr(http.StatusForbidden, "invite_invalid", "This invite link is invalid or has expired")
	errRegistrationClosed = apiErr(http.StatusForbidden, "registration_closed", "Registration is closed on this server")
	errAccountDisabled    = apiErr(http.StatusForbidden, "account_disabled", "This account has been suspended by the server's admin")
)

// inviteGrant is what an invite code allows at registration: a member's
// friend invite (befriend inviter) or an admin's server invite (consume a
// use, no friendship).
type inviteGrant struct {
	inviter        string // friend invite owner
	friendCodeHash []byte
	serverInviteID string
}

// lookupGrant resolves an invite code against server invites, then members'
// invites. It returns nil (no error) for unknown, expired, revoked or used-up
// codes and for invites of suspended members.
func (s *Server) lookupGrant(ctx context.Context, code string) (*inviteGrant, error) {
	if !validInviteCode(code) {
		return nil, nil
	}
	h := inviteHash(code)
	now := s.now().UnixMilli()
	si, err := store.ServerInviteByHash(ctx, s.db.R, h)
	if err == nil {
		if si.Status(now) != "active" {
			return nil, nil
		}
		return &inviteGrant{serverInviteID: si.ID}, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	inv, err := store.InviteByHash(ctx, s.db.R, h, now)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &inviteGrant{inviter: inv.UserID, friendCodeHash: h}, nil
}

// canRegisterWith reports whether a grant lets a new account register in
// registration mode set (members' links need user_invites on invite-only
// servers; nothing registers on closed servers).
func canRegisterWith(set *liveSettings, g *inviteGrant) bool {
	switch set.Registration {
	case "open":
		return true
	case "invite":
		return g != nil && (g.serverInviteID != "" || set.UserInvites)
	}
	return false
}

// checkRegistration enforces the registration mode (docs/API.md
// "Registration gate") for a new account and returns the invite to apply.
func (s *Server) checkRegistration(ctx context.Context, code *string) (*inviteGrant, error) {
	set := s.live()
	if set.Registration == "closed" {
		return nil, errRegistrationClosed
	}
	c := ""
	if code != nil {
		c = strings.TrimSpace(*code)
	}
	if c == "" {
		if set.Registration == "invite" {
			return nil, errInviteRequired
		}
		return &inviteGrant{}, nil
	}
	g, err := s.lookupGrant(ctx, c)
	if err != nil {
		return nil, err
	}
	if !canRegisterWith(set, g) {
		return nil, errInviteInvalid
	}
	if g == nil { // open server: an unusable code is ignored
		g = &inviteGrant{}
	}
	return g, nil
}

// createAccount inserts u (plus the invite bookkeeping and a session) in one
// transaction. A server invite use is consumed atomically: if it was used up
// or revoked meanwhile, registration fails with invite_invalid (on invite
// servers) instead of overshooting max_uses.
func (s *Server) createAccount(ctx context.Context, u *store.User, g *inviteGrant, dev *deviceIn) (token string, befriended bool, err error) {
	if g == nil {
		g = &inviteGrant{}
	}
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		if g.serverInviteID != "" {
			ok, err := store.ConsumeServerInvite(ctx, tx, g.serverInviteID, s.now().UnixMilli())
			if err != nil {
				return err
			}
			if ok {
				id := g.serverInviteID
				u.ServerInviteID = &id
			} else if s.live().Registration == "invite" {
				return errInviteInvalid
			}
		}
		if g.inviter != "" {
			inviter := g.inviter
			u.InvitedBy = &inviter
		}
		if err := store.InsertUser(ctx, tx, u); err != nil {
			return err
		}
		if g.inviter != "" {
			if err := store.CountInviteUse(ctx, tx, g.friendCodeHash); err != nil {
				return err
			}
		}
		var err error
		if befriended, err = s.befriendInviter(ctx, tx, u.ID, g.inviter); err != nil {
			return err
		}
		token, err = s.newSession(ctx, tx, u.ID, dev)
		return err
	})
	return token, befriended, err
}

// befriendInviter links a new account with its inviter inside tx (skipped
// silently if the inviter is at the friend limit).
func (s *Server) befriendInviter(ctx context.Context, tx *sql.Tx, userID, inviter string) (bool, error) {
	if inviter == "" {
		return false, nil
	}
	if maxFriends := s.live().MaxFriends; maxFriends > 0 {
		n, err := store.CountFriends(ctx, tx, inviter)
		if err != nil {
			return false, err
		}
		if n >= maxFriends {
			return false, nil
		}
	}
	_, err := store.AddFriendship(ctx, tx, userID, inviter, s.now().UnixMilli())
	return err == nil, err
}

// announceInviteFriendship emits friend.accepted to both sides after a new
// account was linked to its inviter.
func (s *Server) announceFriendship(ctx context.Context, a, b string) {
	since, err := store.FriendSince(ctx, s.db.R, a, b)
	if err != nil {
		return
	}
	rr := s.renderer(ctx, a)
	if err := rr.loadUsers(a, b); err != nil {
		return
	}
	s.emit("friend.accepted", map[string]any{"friend": friendView{User: rr.summary(b), Since: since}}, a)
	s.emit("friend.accepted", map[string]any{"friend": friendView{User: rr.summary(a), Since: since}}, b)
}

// limitIdentifier applies the per-(identifier, client IP) limit (5/min) and
// the looser per-identifier global limit (30/min).
func (s *Server) limitIdentifier(r *http.Request, key string) error {
	if err := s.limit(s.rl.authID, key+"|"+ratelimit.IPKey(clientIPFrom(r))); err != nil {
		return err
	}
	return s.limit(s.rl.authIDGlobal, key)
}

type registerReq struct {
	Method      string    `json:"method"`
	Identifier  string    `json:"identifier"`
	Password    string    `json:"password"`
	DisplayName *string   `json:"display_name"`
	InviteCode  *string   `json:"invite_code"`
	Device      *deviceIn `json:"device"`
}

func (s *Server) methodEnabled(method string) bool {
	switch method {
	case "email":
		return s.cfg.AuthEmail
	case "phone":
		return s.cfg.AuthPhone
	}
	return false
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) error {
	if err := s.limitIP(s.rl.authIP, r); err != nil {
		return err
	}
	var req registerReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Method != "email" && req.Method != "phone" {
		return errBadRequest(`method must be "email" or "phone"`)
	}
	if !s.methodEnabled(req.Method) {
		return errForbidden("This sign-up method is disabled on this server")
	}
	ident, ok := auth.NormalizeIdentifier(req.Method, req.Identifier)
	if !ok {
		if req.Method == "phone" {
			return errBadRequest("Phone number must be in international E.164 format, e.g. +34600111222")
		}
		return errBadRequest("Invalid email address")
	}
	if err := s.limitIdentifier(r, "id:"+ident); err != nil {
		return err
	}
	if !auth.ValidPassword(req.Password) {
		return errBadRequest("Password must be 8–128 characters")
	}
	name := defaultName(ident, req.Method)
	if req.DisplayName != nil && strings.TrimSpace(*req.DisplayName) != "" {
		n, ok := auth.CleanDisplayName(*req.DisplayName)
		if !ok {
			return errBadRequest("Display name must be 1–40 characters")
		}
		name = n
	}
	if _, _, err := req.Device.clean(); err != nil {
		return err
	}
	ctx := r.Context()
	grant, err := s.checkRegistration(ctx, req.InviteCode)
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(ctx, req.Password)
	if err != nil {
		return err
	}
	now := s.now().UnixMilli()
	u := &store.User{ID: ids.At(s.now()), DisplayName: name, PasswordHash: &hash, Discoverable: true, Searchable: true,
		ReadReceipts: true, CreatedAt: now, UpdatedAt: now}
	if req.Method == "email" {
		u.Email = &ident
	} else {
		u.Phone = &ident
	}
	token, befriended, err := s.createAccount(ctx, u, grant, req.Device)
	if errors.Is(err, store.ErrIdentifierTaken) {
		return apiErr(http.StatusConflict, "identifier_taken", "An account with this "+req.Method+" already exists")
	}
	if err != nil {
		return err
	}
	if befriended {
		inviter := grant.inviter
		s.announceFriendship(ctx, u.ID, inviter)
		s.notify(push.Notification{UserIDs: []string{inviter}, Pref: push.PrefFriends,
			Payload: push.Payload{Type: "friend_accepted", Title: u.DisplayName, Body: "accepted your friend request", PeerID: strp(u.ID)}})
	}
	return s.respondAuth(w, ctx, u.ID, token, true)
}

func defaultName(ident, method string) string {
	if method == "email" {
		if local, _, ok := strings.Cut(ident, "@"); ok && local != "" {
			if n, ok := auth.CleanDisplayName(local); ok {
				return n
			}
		}
	}
	return "Frame user"
}

type loginReq struct {
	Method     string    `json:"method"`
	Identifier string    `json:"identifier"`
	Password   string    `json:"password"`
	Device     *deviceIn `json:"device"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) error {
	if err := s.limitIP(s.rl.authIP, r); err != nil {
		return err
	}
	var req loginReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Method != "email" && req.Method != "phone" {
		return errBadRequest(`method must be "email" or "phone"`)
	}
	if _, _, err := req.Device.clean(); err != nil {
		return err
	}
	ctx := r.Context()
	ident, ok := auth.NormalizeIdentifier(req.Method, req.Identifier)
	if !ok || len(req.Password) > 1024 || !s.methodEnabled(req.Method) {
		auth.BurnTime(ctx, req.Password)
		return errInvalidCredentials
	}
	if err := s.limitIdentifier(r, "id:"+ident); err != nil {
		return err
	}
	u, err := store.UserBy(ctx, s.db.R, req.Method, ident)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if u == nil || u.PasswordHash == nil {
		auth.BurnTime(ctx, req.Password)
		return errInvalidCredentials
	}
	if err := auth.VerifyPassword(ctx, req.Password, *u.PasswordHash); err != nil {
		if errors.Is(err, auth.ErrMismatch) {
			return errInvalidCredentials
		}
		return err
	}
	// Only revealed after a correct password (no enumeration).
	if u.Disabled() {
		return errAccountDisabled
	}
	token, err := s.newSession(ctx, s.db.W, u.ID, req.Device)
	if err != nil {
		return err
	}
	return s.respondAuth(w, ctx, u.ID, token, false)
}

type oidcReq struct {
	IDToken     string    `json:"id_token"`
	RawNonce    *string   `json:"raw_nonce"`
	DisplayName *string   `json:"display_name"`
	InviteCode  *string   `json:"invite_code"`
	Device      *deviceIn `json:"device"`
}

func (s *Server) handleApple(w http.ResponseWriter, r *http.Request) error {
	return s.oidcLogin(w, r, "apple", s.apple, true)
}

func (s *Server) handleGoogle(w http.ResponseWriter, r *http.Request) error {
	return s.oidcLogin(w, r, "google", s.google, true)
}

// validServerNonce checks the server-bound raw nonce
// "frame1:<server_host>:<base64url(32 random bytes)>": <server_host> (between
// the prefix and the last ':') must be one of this server's configured
// public hosts — never the request Host header. Dev mode accepts any host.
func (s *Server) validServerNonce(raw string) bool {
	rest, ok := strings.CutPrefix(raw, "frame1:")
	if !ok || len(raw) > 512 {
		return false
	}
	i := strings.LastIndexByte(rest, ':')
	if i <= 0 {
		return false
	}
	host, rnd := rest[:i], rest[i+1:]
	if b, err := base64.RawURLEncoding.DecodeString(rnd); err != nil || len(b) != 32 {
		return false
	}
	if s.cfg.Dev {
		return true
	}
	for _, h := range s.cfg.PublicHosts {
		if subtle.ConstantTimeCompare([]byte(h), []byte(host)) == 1 {
			return true
		}
	}
	return false
}

// oidcLogin signs in (or up) with a verified Apple/Google ID token. When the
// request carries a valid session, the identity is linked to that account
// instead (explicit linking; a matching email is never auto-linked).
func (s *Server) oidcLogin(w http.ResponseWriter, r *http.Request, provider string, v *oidc.Verifier, requireNonce bool) error {
	if err := s.limitIP(s.rl.authIP, r); err != nil {
		return err
	}
	var req oidcReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if !s.oidcEnabled(v) {
		return errForbidden("This sign-in method is not enabled on this server")
	}
	if req.IDToken == "" {
		return errBadRequest("id_token is required")
	}
	if _, _, err := req.Device.clean(); err != nil {
		return err
	}
	raw := ""
	if req.RawNonce != nil {
		raw = *req.RawNonce
	}
	// The nonce binds the token to this server (prevents a malicious Frame
	// server replaying a user's token here) and is single-use.
	if !s.validServerNonce(raw) {
		return errInvalidCredentials
	}
	ctx := r.Context()
	claims, err := v.Verify(ctx, req.IDToken, raw, requireNonce)
	if err != nil {
		if errors.Is(err, oidc.ErrKeysUnavailable) {
			s.log.Warn("oidc keys unavailable", "provider", provider, "err", err)
			return apiErr(http.StatusServiceUnavailable, "unavailable", "Sign-in provider keys are temporarily unavailable, try again")
		}
		return errInvalidCredentials
	}
	nh := sha256.Sum256([]byte(raw))
	if fresh, err := store.UseNonce(ctx, s.db.W, nh[:], claims.ExpiresAt.Add(5*time.Minute).UnixMilli()); err != nil {
		return err
	} else if !fresh {
		return errInvalidCredentials
	}
	col := provider + "_sub"
	existing, err := store.UserBy(ctx, s.db.R, col, claims.Subject)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}

	// Explicit linking from a signed-in session.
	if bearer(r) != "" {
		a, err := s.authenticate(r)
		if err != nil {
			return err
		}
		if existing != nil && existing.ID != a.ID() {
			return apiErr(http.StatusConflict, "identifier_taken", "This "+provider+" account is linked to another user")
		}
		if existing == nil {
			cur := a.User.AppleSub
			if provider == "google" {
				cur = a.User.GoogleSub
			}
			if cur != nil {
				return errConflict("A different " + provider + " account is already linked")
			}
			if err := store.LinkSub(ctx, s.db.W, a.ID(), provider, claims.Subject, s.now().UnixMilli()); err != nil {
				if errors.Is(err, store.ErrAppleSubTaken) || errors.Is(err, store.ErrGoogleSubTaken) {
					return apiErr(http.StatusConflict, "identifier_taken", "This "+provider+" account is linked to another user")
				}
				return err
			}
		}
		return s.respondAuth(w, ctx, a.ID(), a.Token, false)
	}

	if existing != nil {
		if existing.Disabled() {
			return errAccountDisabled
		}
		token, err := s.newSession(ctx, s.db.W, existing.ID, req.Device)
		if err != nil {
			return err
		}
		return s.respondAuth(w, ctx, existing.ID, token, false)
	}

	grant, err := s.checkRegistration(ctx, req.InviteCode)
	if err != nil {
		return err
	}
	name := "Frame user"
	if req.DisplayName != nil && strings.TrimSpace(*req.DisplayName) != "" {
		if n, ok := auth.CleanDisplayName(*req.DisplayName); ok {
			name = n
		}
	}
	now := s.now().UnixMilli()
	sub := claims.Subject
	u := &store.User{ID: ids.At(s.now()), DisplayName: name, Discoverable: true, Searchable: true, ReadReceipts: true, CreatedAt: now, UpdatedAt: now}
	if provider == "apple" {
		u.AppleSub = &sub
	} else {
		u.GoogleSub = &sub
	}
	token, befriended, err := s.createAccount(ctx, u, grant, req.Device)
	if errors.Is(err, store.ErrAppleSubTaken) || errors.Is(err, store.ErrGoogleSubTaken) {
		// Lost a race with a concurrent first sign-in: log into that account.
		existing, err := store.UserBy(ctx, s.db.R, col, sub)
		if err != nil {
			return err
		}
		if existing.Disabled() {
			return errAccountDisabled
		}
		token, err := s.newSession(ctx, s.db.W, existing.ID, req.Device)
		if err != nil {
			return err
		}
		return s.respondAuth(w, ctx, existing.ID, token, false)
	}
	if err != nil {
		return err
	}
	if befriended {
		s.announceFriendship(ctx, u.ID, grant.inviter)
	}
	return s.respondAuth(w, ctx, u.ID, token, true)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	if err := store.DeleteSession(r.Context(), s.db.W, a.ID(), a.Session.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	s.hub.CloseSession(a.ID(), a.Session.ID)
	return noContent(w)
}

type sessionView struct {
	ID         string `json:"id"`
	DeviceName string `json:"device_name"`
	Platform   string `json:"platform"`
	CreatedAt  int64  `json:"created_at"`
	LastSeenAt int64  `json:"last_seen_at"`
	Current    bool   `json:"current"`
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	list, err := store.ListSessions(r.Context(), s.db.R, a.ID())
	if err != nil {
		return err
	}
	out := make([]sessionView, 0, len(list))
	for _, x := range list {
		out = append(out, sessionView{ID: x.ID, DeviceName: x.DeviceName, Platform: x.Platform, CreatedAt: x.CreatedAt,
			LastSeenAt: x.LastSeenAt, Current: x.ID == a.Session.ID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
	return nil
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	id := ids.Normalize(r.PathValue("id"))
	if id == "" {
		return errNotFound("Session not found")
	}
	if err := store.DeleteSession(r.Context(), s.db.W, a.ID(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errNotFound("Session not found")
		}
		return err
	}
	s.hub.CloseSession(a.ID(), id)
	return noContent(w)
}

type identifierIn struct {
	Method     string `json:"method"`
	Identifier string `json:"identifier"`
}

type passwordReq struct {
	CurrentPassword *string       `json:"current_password"`
	NewPassword     string        `json:"new_password"`
	Identifier      *identifierIn `json:"identifier"`
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	if err := s.limit(s.rl.authID, "pw:"+a.ID()); err != nil {
		return err
	}
	var req passwordReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if !auth.ValidPassword(req.NewPassword) {
		return errBadRequest("Password must be 8–128 characters")
	}
	ctx := r.Context()
	u := a.User
	var setIdent *identifierIn
	if u.PasswordHash != nil {
		if req.CurrentPassword == nil {
			return errBadRequest("current_password is required")
		}
		if err := auth.VerifyPassword(ctx, *req.CurrentPassword, *u.PasswordHash); err != nil {
			if errors.Is(err, auth.ErrMismatch) {
				return errInvalidCredentials
			}
			return err
		}
	} else {
		if req.CurrentPassword != nil && *req.CurrentPassword != "" {
			return errBadRequest("This account has no password yet; send current_password: null")
		}
		if u.Email == nil && u.Phone == nil {
			if req.Identifier == nil {
				return errBadRequest("identifier is required to add password sign-in")
			}
			if !s.methodEnabled(req.Identifier.Method) {
				return errBadRequest(`identifier.method must be an enabled method ("email" or "phone")`)
			}
			v, ok := auth.NormalizeIdentifier(req.Identifier.Method, req.Identifier.Identifier)
			if !ok {
				return errBadRequest("Invalid identifier")
			}
			setIdent = &identifierIn{Method: req.Identifier.Method, Identifier: v}
		}
	}
	hash, err := auth.HashPassword(ctx, req.NewPassword)
	if err != nil {
		return err
	}
	now := s.now().UnixMilli()
	var revoked []string
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		if setIdent != nil {
			if err := store.SetIdentifier(ctx, tx, u.ID, setIdent.Method, setIdent.Identifier, now); err != nil {
				return err
			}
		}
		if err := store.SetPassword(ctx, tx, u.ID, hash, now); err != nil {
			return err
		}
		// Revoke every other session (their push devices cascade).
		var err error
		revoked, err = store.DeleteUserSessions(ctx, tx, u.ID, a.Session.ID)
		return err
	})
	if errors.Is(err, store.ErrIdentifierTaken) {
		return apiErr(http.StatusConflict, "identifier_taken", "An account with this "+setIdent.Method+" already exists")
	}
	if err != nil {
		return err
	}
	for _, sid := range revoked {
		s.hub.CloseSession(u.ID, sid)
	}
	return noContent(w)
}

type resetRequestReq struct {
	Email string `json:"email"`
}

const (
	resetCodeTTL     = 15 * time.Minute
	resetMaxAttempts = 10 // per user per 24 h
)

func resetCodeHash(userID, code string) []byte {
	h := sha256.Sum256([]byte("frame-reset-v1:" + userID + ":" + code))
	return h[:]
}

func (s *Server) handleResetRequest(w http.ResponseWriter, r *http.Request) error {
	if s.mailer == nil {
		return errNotFound("Password reset by email is not available on this server")
	}
	if err := s.limitIP(s.rl.authIP, r); err != nil {
		return err
	}
	var req resetRequestReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	email, ok := auth.NormalizeEmail(req.Email)
	if !ok {
		return noContent(w)
	}
	if err := s.limitIdentifier(r, "reset:"+email); err != nil {
		return err
	}
	ctx := r.Context()
	u, err := store.UserBy(ctx, s.db.R, "email", email)
	if errors.Is(err, store.ErrNotFound) {
		return noContent(w) // no enumeration
	}
	if err != nil {
		return err
	}
	n, err := rand.Int(rand.Reader, big.NewInt(100_000_000))
	if err != nil {
		return err
	}
	code := fmt.Sprintf("%08d", n.Int64())
	if err := store.SetPasswordReset(ctx, s.db.W, u.ID, resetCodeHash(u.ID, code), s.now().Add(resetCodeTTL).UnixMilli()); err != nil {
		return err
	}
	body := fmt.Sprintf("Your %s password reset code is: %s\n\nIt expires in 15 minutes. If you did not ask for this, ignore this email.\n", s.live().Name, code)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.mailer.Send(ctx, email, s.live().Name+" password reset", body); err != nil {
			s.log.Warn("sending reset email failed", "err", err)
		}
	}()
	return noContent(w)
}

type resetConfirmReq struct {
	Email       string `json:"email"`
	Code        string `json:"code"`
	NewPassword string `json:"new_password"`
}

func (s *Server) handleResetConfirm(w http.ResponseWriter, r *http.Request) error {
	if s.mailer == nil {
		return errNotFound("Password reset by email is not available on this server")
	}
	if err := s.limitIP(s.rl.authIP, r); err != nil {
		return err
	}
	var req resetConfirmReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if !auth.ValidPassword(req.NewPassword) {
		return errBadRequest("Password must be 8–128 characters")
	}
	email, ok := auth.NormalizeEmail(req.Email)
	if !ok {
		return errInvalidCredentials
	}
	if err := s.limitIdentifier(r, "reset:"+email); err != nil {
		return err
	}
	ctx := r.Context()
	u, err := store.UserBy(ctx, s.db.R, "email", email)
	if errors.Is(err, store.ErrNotFound) {
		return errInvalidCredentials
	}
	if err != nil {
		return err
	}
	// At most resetMaxAttempts confirmations per user per 24 h, however many
	// codes were requested.
	if ok, err := store.ResetAttempt(ctx, s.db.W, u.ID, s.now().UnixMilli(), (24 * time.Hour).Milliseconds(), resetMaxAttempts); err != nil {
		return err
	} else if !ok {
		return errInvalidCredentials
	}
	want, err := store.PasswordReset(ctx, s.db.W, u.ID, s.now().UnixMilli())
	if errors.Is(err, store.ErrNotFound) {
		return errInvalidCredentials
	}
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(want, resetCodeHash(u.ID, strings.TrimSpace(req.Code))) != 1 {
		return errInvalidCredentials
	}
	hash, err := auth.HashPassword(ctx, req.NewPassword)
	if err != nil {
		return err
	}
	var revoked []string
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := store.SetPassword(ctx, tx, u.ID, hash, s.now().UnixMilli()); err != nil {
			return err
		}
		if err := store.DeletePasswordReset(ctx, tx, u.ID); err != nil {
			return err
		}
		revoked, err = store.DeleteUserSessions(ctx, tx, u.ID, "")
		return err
	})
	if err != nil {
		return err
	}
	for _, sid := range revoked {
		s.hub.CloseSession(u.ID, sid)
	}
	return noContent(w)
}
