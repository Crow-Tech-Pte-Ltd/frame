package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/auth"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ids"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ratelimit"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/totp"
)

// ---- First-run setup -------------------------------------------------------

// SetupTokenTTL is how long a first-run setup link stays valid.
const SetupTokenTTL = 24 * time.Hour

const setupMetaKey = "admin_setup"

// ErrSetupDone is returned when an admin already exists.
var ErrSetupDone = errors.New("an admin account already exists; use `frame-server admin user promote <username>` to add more")

var (
	errSetupTokenInvalid = apiErr(http.StatusForbidden, "setup_token_invalid", "This setup link is invalid or has expired. Run `frame-server admin setup-link` on the server for a new one.")
	errSetupDone         = apiErr(http.StatusConflict, "setup_done", "This server already has an admin. Sign in instead.")
)

// NewSetupToken creates a fresh single-use first-run token (replacing any
// previous one) while no admin exists. Only its SHA-256 is stored.
func (s *Server) NewSetupToken(ctx context.Context) (string, time.Time, error) {
	if has, err := store.AnyAdmin(ctx, s.db.R); err != nil {
		return "", time.Time{}, err
	} else if has {
		return "", time.Time{}, ErrSetupDone
	}
	tok := ids.Token(32)
	h := sha256.Sum256([]byte(tok))
	exp := s.now().Add(SetupTokenTTL)
	if err := store.SetMeta(ctx, s.db.W, setupMetaKey, hex.EncodeToString(h[:])+":"+strconv.FormatInt(exp.UnixMilli(), 10)); err != nil {
		return "", time.Time{}, err
	}
	return tok, exp, nil
}

// NeedsSetup reports whether the server has no admin yet.
func (s *Server) NeedsSetup(ctx context.Context) (bool, error) {
	has, err := store.AnyAdmin(ctx, s.db.R)
	return !has, err
}

func (s *Server) validSetupToken(ctx context.Context, q store.Q, tok string) (bool, error) {
	if len(tok) != 43 || !auth.ValidTokenSyntax(tok) {
		return false, nil
	}
	v, err := store.Meta(ctx, q, setupMetaKey)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	hexHash, expStr, ok := strings.Cut(v, ":")
	exp, err := strconv.ParseInt(expStr, 10, 64)
	want, herr := hex.DecodeString(hexHash)
	if !ok || err != nil || herr != nil {
		return false, nil
	}
	got := sha256.Sum256([]byte(tok))
	return subtle.ConstantTimeCompare(want, got[:]) == 1 && s.now().UnixMilli() < exp, nil
}

func (s *Server) handleAdminSetupInfo(w http.ResponseWriter, r *http.Request) error {
	need, err := s.NeedsSetup(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"needed": need, "server_name": s.live().Name})
	return nil
}

type setupTokenReq struct {
	Token string `json:"token"`
}

func (s *Server) handleAdminSetupVerify(w http.ResponseWriter, r *http.Request) error {
	if err := s.limitIP(s.rl.authIP, r); err != nil {
		return err
	}
	var req setupTokenReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	if need, err := s.NeedsSetup(ctx); err != nil {
		return err
	} else if !need {
		return errSetupDone
	}
	ok, err := s.validSetupToken(ctx, s.db.R, req.Token)
	if err != nil {
		return err
	}
	if !ok {
		return errSetupTokenInvalid
	}
	return noContent(w)
}

type setupReq struct {
	Token       string `json:"token"`
	DisplayName string `json:"display_name"`
	Username    string `json:"username"`
	Method      string `json:"method"`
	Identifier  string `json:"identifier"`
	Password    string `json:"password"`
}

func (s *Server) handleAdminSetup(w http.ResponseWriter, r *http.Request) error {
	if err := s.limitIP(s.rl.authIP, r); err != nil {
		return err
	}
	var req setupReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	if need, err := s.NeedsSetup(ctx); err != nil {
		return err
	} else if !need {
		return errSetupDone
	}
	// Cheap token check before the expensive password hash; re-checked in
	// the transaction.
	if ok, err := s.validSetupToken(ctx, s.db.R, req.Token); err != nil {
		return err
	} else if !ok {
		return errSetupTokenInvalid
	}
	u, err := s.newUserFromAdminInput(req.DisplayName, &req.Username, true, req.Method, req.Identifier)
	if err != nil {
		return err
	}
	if !auth.ValidPassword(req.Password) {
		return errBadRequest("Password must be 8–128 characters")
	}
	hash, err := auth.HashPassword(ctx, req.Password)
	if err != nil {
		return err
	}
	u.PasswordHash = &hash
	u.IsAdmin = true
	var token string
	var sess *store.AdminSession
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		if has, err := store.AnyAdmin(ctx, tx); err != nil {
			return err
		} else if has {
			return errSetupDone
		}
		if ok, err := s.validSetupToken(ctx, tx, req.Token); err != nil {
			return err
		} else if !ok {
			return errSetupTokenInvalid
		}
		if err := store.InsertUser(ctx, tx, u); err != nil {
			return err
		}
		if err := store.DeleteMeta(ctx, tx, setupMetaKey); err != nil {
			return err
		}
		token, sess, err = s.newAdminSession(ctx, tx, r, u.ID)
		return err
	})
	if err := mapUserErr(err, req.Method); err != nil {
		return err
	}
	s.audit(ctx, r, u, "setup.complete", nil, "")
	s.setAdminCookie(w, r, token)
	return s.writeAdminSession(w, r, u, sess)
}

// mapUserErr turns unique-constraint errors into API errors.
func mapUserErr(err error, method string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrIdentifierTaken):
		return apiErr(http.StatusConflict, "identifier_taken", "An account with this "+method+" already exists")
	case errors.Is(err, store.ErrUsernameTaken):
		return apiErr(http.StatusConflict, "username_taken", "This username is taken")
	}
	return err
}

// newUserFromAdminInput validates the fields shared by setup and "New user".
func (s *Server) newUserFromAdminInput(displayName string, username *string, requireUsername bool, method, identifier string) (*store.User, error) {
	name, ok := auth.CleanDisplayName(displayName)
	if !ok {
		return nil, errBadRequest("Display name must be 1–40 characters")
	}
	if method != "email" && method != "phone" {
		return nil, errBadRequest(`method must be "email" or "phone"`)
	}
	ident, ok := auth.NormalizeIdentifier(method, identifier)
	if !ok {
		if method == "phone" {
			return nil, errBadRequest("Phone number must be in international E.164 format, e.g. +34600111222")
		}
		return nil, errBadRequest("Invalid email address")
	}
	now := s.now().UnixMilli()
	u := &store.User{ID: ids.At(s.now()), DisplayName: name, Discoverable: true, Searchable: true, ReadReceipts: true,
		CreatedAt: now, UpdatedAt: now}
	if method == "email" {
		u.Email = &ident
	} else {
		u.Phone = &ident
	}
	if username != nil && strings.TrimSpace(*username) != "" {
		un := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(*username), "@"))
		if msg := auth.UsernameError(un); msg != "" {
			return nil, errBadRequest("Username: " + msg)
		}
		u.Username = &un
	} else if requireUsername {
		return nil, errBadRequest("Username is required")
	}
	return u, nil
}

// ---- Sessions ----------------------------------------------------------------

func (s *Server) newAdminSession(ctx context.Context, q store.Q, r *http.Request, userID string) (string, *store.AdminSession, error) {
	tok, hash := auth.NewSessionToken()
	now := s.now().UnixMilli()
	ua := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, r.UserAgent())
	if utf8.RuneCountInString(ua) > 200 {
		ua = string([]rune(ua)[:200])
	}
	sess := &store.AdminSession{ID: ids.At(s.now()), UserID: userID, IP: clientIPFrom(r).String(), UserAgent: ua, CreatedAt: now, LastSeenAt: now}
	if err := store.InsertAdminSession(ctx, q, sess, hash); err != nil {
		return "", nil, err
	}
	return tok, sess, nil
}

type adminMeView struct {
	ID                string  `json:"id"`
	Username          *string `json:"username"`
	DisplayName       string  `json:"display_name"`
	Email             *string `json:"email"`
	Phone             *string `json:"phone"`
	TOTPEnabled       bool    `json:"totp_enabled"`
	RecoveryCodesLeft int     `json:"recovery_codes_left"`
}

type adminSessionInfo struct {
	ID            string `json:"id"`
	CreatedAt     int64  `json:"created_at"`
	ExpiresAt     int64  `json:"expires_at"`
	IdleTimeoutMS int64  `json:"idle_timeout_ms"`
}

type adminServerInfo struct {
	Name      string  `json:"name"`
	Version   string  `json:"version"`
	PublicURL *string `json:"public_url"`
}

type adminSessionView struct {
	Admin   adminMeView      `json:"admin"`
	Server  adminServerInfo  `json:"server"`
	Session adminSessionInfo `json:"session"`
}

func (s *Server) writeAdminSession(w http.ResponseWriter, r *http.Request, u *store.User, sess *store.AdminSession) error {
	ctx := r.Context()
	enabled, err := store.TOTPEnabled(ctx, s.db.R, u.ID)
	if err != nil {
		return err
	}
	left, err := store.RecoveryCodesLeft(ctx, s.db.R, u.ID)
	if err != nil {
		return err
	}
	v := adminSessionView{
		Admin: adminMeView{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Email: u.Email, Phone: u.Phone,
			TOTPEnabled: enabled, RecoveryCodesLeft: left},
		Server: adminServerInfo{Name: s.live().Name, Version: s.version},
		Session: adminSessionInfo{ID: sess.ID, CreatedAt: sess.CreatedAt, ExpiresAt: sess.CreatedAt + adminMaxAge.Milliseconds(),
			IdleTimeoutMS: adminIdleTimeout.Milliseconds()},
	}
	if s.cfg.PublicURL != "" {
		p := s.cfg.PublicURL
		v.Server.PublicURL = &p
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

func (s *Server) handleAdminSession(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	return s.writeAdminSession(w, r, a.User, a.Session)
}

type adminLoginReq struct {
	Identifier string  `json:"identifier"`
	Password   string  `json:"password"`
	TOTPCode   *string `json:"totp_code"`
}

// lookupLoginUser resolves an e-mail, phone or username.
func lookupLoginUser(ctx context.Context, q store.Q, ident string) (*store.User, string) {
	ident = strings.TrimSpace(ident)
	var col, val string
	switch {
	case strings.HasPrefix(ident, "+"):
		v, ok := auth.NormalizePhone(ident)
		if !ok {
			return nil, ""
		}
		col, val = "phone", v
	case strings.Contains(ident, "@") && !strings.HasPrefix(ident, "@"):
		v, ok := auth.NormalizeEmail(ident)
		if !ok {
			return nil, ""
		}
		col, val = "email", v
	default:
		v := strings.ToLower(strings.TrimPrefix(ident, "@"))
		if auth.UsernameError(v) != "" {
			return nil, ""
		}
		col, val = "username", v
	}
	u, err := store.UserBy(ctx, q, col, val)
	if err != nil {
		return nil, col + ":" + val
	}
	return u, col + ":" + val
}

func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) error {
	if err := s.limitIP(s.rl.authIP, r); err != nil {
		return err
	}
	var req adminLoginReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	if len(req.Password) > 1024 || len(req.Identifier) > 320 {
		auth.BurnTime(ctx, req.Password)
		return errInvalidCredentials
	}
	u, key := lookupLoginUser(ctx, s.db.R, req.Identifier)
	if key != "" {
		if err := s.limitIdentifier(r, "admin:"+key); err != nil {
			return err
		}
	}
	if u == nil || u.PasswordHash == nil {
		auth.BurnTime(ctx, req.Password)
		return errInvalidCredentials
	}
	if err := auth.VerifyPassword(ctx, req.Password, *u.PasswordHash); err != nil {
		if errors.Is(err, auth.ErrMismatch) {
			if u.IsAdmin {
				s.audit(ctx, r, u, "login.failed", nil, "wrong password")
			}
			return errInvalidCredentials
		}
		return err
	}
	// Non-admins and suspended admins look exactly like a wrong password.
	if !u.IsAdmin || u.Disabled() {
		return errInvalidCredentials
	}
	if err := s.checkSecondFactor(ctx, r, u, req.TOTPCode); err != nil {
		if errors.Is(err, errTOTPInvalid) {
			s.audit(ctx, r, u, "login.failed", nil, "wrong two-factor code")
		}
		return err
	}
	token, sess, err := s.newAdminSession(ctx, s.db.W, r, u.ID)
	if err != nil {
		return err
	}
	s.audit(ctx, r, u, "login", nil, "")
	s.setAdminCookie(w, r, token)
	return s.writeAdminSession(w, r, u, sess)
}

// checkSecondFactor verifies a TOTP or recovery code when the admin has
// two-factor authentication enabled (codes are single-use).
func (s *Server) checkSecondFactor(ctx context.Context, r *http.Request, u *store.User, code *string) error {
	t, err := store.GetTOTP(ctx, s.db.R, u.ID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && t.EnabledAt == nil) {
		return nil
	}
	if err != nil {
		return err
	}
	if code == nil || strings.TrimSpace(*code) == "" {
		return errTOTPRequired
	}
	return s.verifySecondFactor(ctx, u.ID, t, *code)
}

// verifySecondFactor checks a TOTP or recovery code. Attempts are limited
// per account (s.rl.secondFactor), so codes can't be guessed by spreading
// tries over identifiers and IPs.
func (s *Server) verifySecondFactor(ctx context.Context, userID string, t *store.TOTP, code string) error {
	if err := s.limit(s.rl.secondFactor, userID); err != nil {
		return err
	}
	if c := totp.NormalizeCode(code); c != "" {
		secret, ok := unseal(s.totpKey, t.SecretEnc, "frame-totp-v1:"+userID)
		if !ok {
			return errors.New("totp secret can't be decrypted (secrets.json changed?)")
		}
		step, ok := totp.Verify(secret, c, s.now())
		if !ok {
			return errTOTPInvalid
		}
		fresh, err := store.UseTOTPStep(ctx, s.db.W, userID, step)
		if err != nil {
			return err
		}
		if !fresh {
			return errTOTPInvalid // replayed code
		}
		return nil
	}
	if rc := totp.NormalizeRecoveryCode(code); rc != "" {
		ok, err := store.UseRecoveryCode(ctx, s.db.W, userID, s.recoveryHash(userID, rc), s.now().UnixMilli())
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return errTOTPInvalid
}

func (s *Server) recoveryHash(userID, normalized string) []byte {
	m := hmac.New(sha256.New, s.recKey)
	m.Write([]byte(userID + ":" + normalized))
	return m.Sum(nil)
}

func (s *Server) handleAdminLogout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(adminCookie); err == nil && auth.ValidTokenSyntax(c.Value) {
		ctx := r.Context()
		if sess, err := store.AdminSessionByTokenHash(ctx, s.db.R, auth.HashToken(c.Value)); err == nil {
			_ = store.DeleteAdminSession(ctx, s.db.W, sess.UserID, sess.ID)
		}
	}
	s.clearAdminCookie(w, r)
	return noContent(w)
}

// ---- Security page -----------------------------------------------------------

type adminSessionRow struct {
	ID         string `json:"id"`
	IP         string `json:"ip"`
	UserAgent  string `json:"user_agent"`
	CreatedAt  int64  `json:"created_at"`
	LastSeenAt int64  `json:"last_seen_at"`
	Current    bool   `json:"current"`
}

func (s *Server) handleAdminSecurity(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	ctx := r.Context()
	enabled, err := store.TOTPEnabled(ctx, s.db.R, a.User.ID)
	if err != nil {
		return err
	}
	left, err := store.RecoveryCodesLeft(ctx, s.db.R, a.User.ID)
	if err != nil {
		return err
	}
	list, err := store.ListAdminSessions(ctx, s.db.R, a.User.ID)
	if err != nil {
		return err
	}
	rows := make([]adminSessionRow, 0, len(list))
	now := s.now()
	for _, x := range list {
		if now.Sub(time.UnixMilli(x.LastSeenAt)) > adminIdleTimeout || now.Sub(time.UnixMilli(x.CreatedAt)) > adminMaxAge {
			continue // expired, swept by maintenance
		}
		rows = append(rows, adminSessionRow{ID: x.ID, IP: x.IP, UserAgent: x.UserAgent, CreatedAt: x.CreatedAt,
			LastSeenAt: x.LastSeenAt, Current: x.ID == a.Session.ID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"totp_enabled": enabled, "recovery_codes_left": left, "sessions": rows})
	return nil
}

func (s *Server) handleAdminRevokeAdminSession(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	id := ids.Normalize(r.PathValue("id"))
	if id == "" {
		return errNotFound("Session not found")
	}
	if err := store.DeleteAdminSession(r.Context(), s.db.W, a.User.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errNotFound("Session not found")
		}
		return err
	}
	if id == a.Session.ID {
		s.clearAdminCookie(w, r)
	}
	s.audit(r.Context(), r, a.User, "admin_session.revoke", nil, "")
	return noContent(w)
}

// stepUp re-checks the admin's password before sensitive actions.
func (s *Server) stepUp(ctx context.Context, r *http.Request, u *store.User, password string) error {
	if err := s.limit(s.rl.authID, "stepup:"+u.ID+"|"+ratelimit.IPKey(clientIPFrom(r))); err != nil {
		return err
	}
	if u.PasswordHash == nil || len(password) > 1024 {
		return errInvalidCredentials
	}
	if err := auth.VerifyPassword(ctx, password, *u.PasswordHash); err != nil {
		if errors.Is(err, auth.ErrMismatch) {
			return apiErr(http.StatusUnauthorized, "invalid_credentials", "Wrong password")
		}
		return err
	}
	return nil
}

type adminPasswordReq struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (s *Server) handleAdminChangePassword(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	var req adminPasswordReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	if err := s.stepUp(ctx, r, a.User, req.CurrentPassword); err != nil {
		return err
	}
	if !auth.ValidPassword(req.NewPassword) {
		return errBadRequest("Password must be 8–128 characters")
	}
	hash, err := auth.HashPassword(ctx, req.NewPassword)
	if err != nil {
		return err
	}
	var revoked []string
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := store.SetPassword(ctx, tx, a.User.ID, hash, s.now().UnixMilli()); err != nil {
			return err
		}
		if err := store.DeleteAdminSessions(ctx, tx, a.User.ID, a.Session.ID); err != nil {
			return err
		}
		revoked, err = store.DeleteUserSessions(ctx, tx, a.User.ID, "")
		return err
	})
	if err != nil {
		return err
	}
	for _, sid := range revoked {
		s.hub.CloseSession(a.User.ID, sid)
	}
	s.audit(ctx, r, a.User, "password.change", nil, "")
	return noContent(w)
}

type passwordOnlyReq struct {
	Password string `json:"password"`
}

func (s *Server) handleAdminTOTPSetup(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	var req passwordOnlyReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	if err := s.stepUp(ctx, r, a.User, req.Password); err != nil {
		return err
	}
	if on, err := store.TOTPEnabled(ctx, s.db.R, a.User.ID); err != nil {
		return err
	} else if on {
		return errConflict("Two-factor authentication is already on; turn it off first")
	}
	secret := totp.NewSecret()
	enc, err := seal(s.totpKey, secret, "frame-totp-v1:"+a.User.ID)
	if err != nil {
		return err
	}
	if err := store.SetPendingTOTP(ctx, s.db.W, a.User.ID, enc); err != nil {
		return err
	}
	account := a.User.DisplayName
	switch {
	case a.User.Username != nil:
		account = *a.User.Username
	case a.User.Email != nil:
		account = *a.User.Email
	}
	issuer := s.live().Name
	if issuer == "" {
		issuer = "Frame"
	}
	writeJSON(w, http.StatusOK, map[string]any{"secret": totp.EncodeSecret(secret), "uri": totp.URI(issuer, account, secret)})
	return nil
}

type codeReq struct {
	Code string `json:"code"`
}

func (s *Server) newRecoveryCodes(ctx context.Context, q store.Q, userID string) ([]string, error) {
	codes := totp.NewRecoveryCodes(totp.RecoveryCodeCount)
	hashes := make([][]byte, len(codes))
	for i, c := range codes {
		hashes[i] = s.recoveryHash(userID, totp.NormalizeRecoveryCode(c))
	}
	return codes, store.ReplaceRecoveryCodes(ctx, q, userID, hashes)
}

func (s *Server) handleAdminTOTPEnable(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	if err := s.limit(s.rl.authID, "totp:"+a.User.ID); err != nil {
		return err
	}
	var req codeReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	t, err := store.GetTOTP(ctx, s.db.R, a.User.ID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && t.EnabledAt != nil) {
		return errConflict("Start two-factor setup first")
	}
	if err != nil {
		return err
	}
	secret, ok := unseal(s.totpKey, t.SecretEnc, "frame-totp-v1:"+a.User.ID)
	if !ok {
		return errors.New("totp secret can't be decrypted")
	}
	step, ok := totp.Verify(secret, req.Code, s.now())
	if !ok {
		return errTOTPInvalid
	}
	var codes []string
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		ok, err := store.EnableTOTP(ctx, tx, a.User.ID, s.now().UnixMilli(), step)
		if err != nil {
			return err
		}
		if !ok {
			return errConflict("Two-factor authentication is already on")
		}
		codes, err = s.newRecoveryCodes(ctx, tx, a.User.ID)
		return err
	})
	if err != nil {
		return err
	}
	s.audit(ctx, r, a.User, "totp.enable", nil, "")
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
	return nil
}

type totpDisableReq struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

func (s *Server) handleAdminTOTPDisable(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	var req totpDisableReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	if err := s.stepUp(ctx, r, a.User, req.Password); err != nil {
		return err
	}
	t, err := store.GetTOTP(ctx, s.db.R, a.User.ID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && t.EnabledAt == nil) {
		// Nothing enabled: drop any pending enrolment.
		if err := store.DeleteTOTP(ctx, s.db.W, a.User.ID); err != nil {
			return err
		}
		return noContent(w)
	}
	if err != nil {
		return err
	}
	if err := s.verifySecondFactor(ctx, a.User.ID, t, req.Code); err != nil {
		return err
	}
	if err := store.DeleteTOTP(ctx, s.db.W, a.User.ID); err != nil {
		return err
	}
	s.audit(ctx, r, a.User, "totp.disable", nil, "")
	return noContent(w)
}

func (s *Server) handleAdminRecoveryCodes(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	var req passwordOnlyReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	if err := s.stepUp(ctx, r, a.User, req.Password); err != nil {
		return err
	}
	if on, err := store.TOTPEnabled(ctx, s.db.R, a.User.ID); err != nil {
		return err
	} else if !on {
		return errConflict("Turn on two-factor authentication first")
	}
	var codes []string
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		codes, err = s.newRecoveryCodes(ctx, tx, a.User.ID)
		return err
	})
	if err != nil {
		return err
	}
	s.audit(ctx, r, a.User, "recovery_codes.regenerate", nil, "")
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
	return nil
}
