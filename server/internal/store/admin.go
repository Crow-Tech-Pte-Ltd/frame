package store

import (
	"database/sql"
	"errors"
	"strings"
)

// ---- Admin flags ----------------------------------------------------------

// CountActiveAdmins returns how many non-suspended admins exist.
func CountActiveAdmins(ctx ctxT, q Q) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin = 1 AND disabled_at IS NULL`).Scan(&n)
	return n, err
}

// AnyAdmin reports whether at least one admin account exists (suspended or
// not): while none does, the first-run setup link is active.
func AnyAdmin(ctx ctxT, q Q) (bool, error) {
	var ok bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE is_admin = 1)`).Scan(&ok)
	return ok, err
}

// SetAdmin grants or removes the admin flag.
func SetAdmin(ctx ctxT, q Q, id string, admin bool, now int64) error {
	res, err := q.ExecContext(ctx, `UPDATE users SET is_admin = ?, updated_at = ? WHERE id = ?`, admin, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetDisabled suspends (at != nil) or reinstates (nil) a user.
func SetDisabled(ctx ctxT, q Q, id string, at *int64, now int64) error {
	res, err := q.ExecContext(ctx, `UPDATE users SET disabled_at = ?, updated_at = ? WHERE id = ?`, at, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountMoments returns how many moments the user authored (all audiences).
func CountMoments(ctx ctxT, q Q, userID string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM moments WHERE author_id = ?`, userID).Scan(&n)
	return n, err
}

// ---- Server invites -------------------------------------------------------

// ServerInvite is a row of server_invites.
type ServerInvite struct {
	ID        string
	CodeHash  []byte
	CodeEnc   []byte
	CreatedBy *string
	Note      string
	MaxUses   int
	Uses      int
	CreatedAt int64
	ExpiresAt *int64
	RevokedAt *int64
}

// Status classifies an invite at time now.
func (i *ServerInvite) Status(now int64) string {
	switch {
	case i.RevokedAt != nil:
		return "revoked"
	case i.ExpiresAt != nil && *i.ExpiresAt <= now:
		return "expired"
	case i.Uses >= i.MaxUses:
		return "used_up"
	}
	return "active"
}

const serverInviteCols = `id, code_hash, code_enc, created_by, note, max_uses, uses, created_at, expires_at, revoked_at`

func scanServerInvite(s scanner) (*ServerInvite, error) {
	var i ServerInvite
	var by sql.NullString
	var exp, rev sql.NullInt64
	if err := s.Scan(&i.ID, &i.CodeHash, &i.CodeEnc, &by, &i.Note, &i.MaxUses, &i.Uses, &i.CreatedAt, &exp, &rev); err != nil {
		return nil, err
	}
	i.CreatedBy, i.ExpiresAt, i.RevokedAt = nstr(by), nint(exp), nint(rev)
	return &i, nil
}

// InsertServerInvite stores a new server invite.
func InsertServerInvite(ctx ctxT, q Q, i *ServerInvite) error {
	_, err := q.ExecContext(ctx, `INSERT INTO server_invites(`+serverInviteCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		i.ID, i.CodeHash, i.CodeEnc, i.CreatedBy, i.Note, i.MaxUses, i.Uses, i.CreatedAt, i.ExpiresAt, i.RevokedAt)
	return err
}

// ServerInviteByHash returns the invite with this code hash (any status).
func ServerInviteByHash(ctx ctxT, q Q, codeHash []byte) (*ServerInvite, error) {
	i, err := scanServerInvite(q.QueryRowContext(ctx, `SELECT `+serverInviteCols+` FROM server_invites WHERE code_hash = ?`, codeHash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return i, err
}

// GetServerInvite loads one server invite by id.
func GetServerInvite(ctx ctxT, q Q, id string) (*ServerInvite, error) {
	i, err := scanServerInvite(q.QueryRowContext(ctx, `SELECT `+serverInviteCols+` FROM server_invites WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return i, err
}

// ConsumeServerInvite atomically counts one use of a still-valid invite. It
// reports false when the invite was revoked, expired or used up meanwhile.
func ConsumeServerInvite(ctx ctxT, q Q, id string, now int64) (bool, error) {
	res, err := q.ExecContext(ctx, `UPDATE server_invites SET uses = uses + 1
		WHERE id = ? AND revoked_at IS NULL AND uses < max_uses AND (expires_at IS NULL OR expires_at > ?)`, id, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ListServerInvites returns server invites, newest first; activeOnly limits
// the list to usable ones.
func ListServerInvites(ctx ctxT, q Q, activeOnly bool, now int64, limit int) ([]*ServerInvite, error) {
	where := ""
	args := []any{}
	if activeOnly {
		where = ` WHERE revoked_at IS NULL AND uses < max_uses AND (expires_at IS NULL OR expires_at > ?)`
		args = append(args, now)
	}
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, `SELECT `+serverInviteCols+` FROM server_invites`+where+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ServerInvite
	for rows.Next() {
		i, err := scanServerInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// RevokeServerInvite marks an invite revoked (no-op if it already was).
func RevokeServerInvite(ctx ctxT, q Q, id string, now int64) error {
	res, err := q.ExecContext(ctx, `UPDATE server_invites SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?`, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Registration is a user who joined with a server invite.
type Registration struct {
	InviteID string
	UserID   string
	At       int64
}

// RegistrationsFor returns the users who registered with the given invites.
func RegistrationsFor(ctx ctxT, q Q, inviteIDs []string) ([]Registration, error) {
	var out []Registration
	inviteIDs = dedupe(inviteIDs)
	for len(inviteIDs) > 0 {
		n := min(len(inviteIDs), 500)
		rows, err := q.QueryContext(ctx, `SELECT server_invite_id, id, created_at FROM users
			WHERE server_invite_id IN (`+placeholders(n)+`) ORDER BY created_at`, argsOf(inviteIDs[:n])...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r Registration
			if err := rows.Scan(&r.InviteID, &r.UserID, &r.At); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, r)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		inviteIDs = inviteIDs[n:]
	}
	return out, nil
}

func argsOf(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// ---- Members' friend invites (admin view) ----------------------------------

// FriendInvite is a member's invite as listed to admins (no code).
type FriendInvite struct {
	CodeHash  []byte
	UserID    string
	CreatedAt int64
	ExpiresAt int64
	Uses      int
}

// ListFriendInvites returns all unexpired member invites (optionally of one
// user), newest first.
func ListFriendInvites(ctx ctxT, q Q, userID string, now int64, limit int) ([]FriendInvite, error) {
	query := `SELECT code_hash, user_id, created_at, expires_at, uses FROM invites WHERE expires_at > ?`
	args := []any{now}
	if userID != "" {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FriendInvite
	for rows.Next() {
		var f FriendInvite
		if err := rows.Scan(&f.CodeHash, &f.UserID, &f.CreatedAt, &f.ExpiresAt, &f.Uses); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// DeleteInviteByHash revokes any member's invite (admin).
func DeleteInviteByHash(ctx ctxT, q Q, codeHash []byte) (bool, error) {
	res, err := q.ExecContext(ctx, `DELETE FROM invites WHERE code_hash = ?`, codeHash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ---- Admin sessions ---------------------------------------------------------

// AdminSession is a row of admin_sessions.
type AdminSession struct {
	ID         string
	UserID     string
	IP         string
	UserAgent  string
	CreatedAt  int64
	LastSeenAt int64
}

// InsertAdminSession stores a new admin session with the SHA-256 of its token.
func InsertAdminSession(ctx ctxT, q Q, s *AdminSession, tokenHash []byte) error {
	_, err := q.ExecContext(ctx, `INSERT INTO admin_sessions(id, user_id, token_hash, ip, user_agent, created_at, last_seen_at)
		VALUES (?,?,?,?,?,?,?)`, s.ID, s.UserID, tokenHash, s.IP, s.UserAgent, s.CreatedAt, s.LastSeenAt)
	return err
}

// AdminSessionByTokenHash resolves an admin cookie.
func AdminSessionByTokenHash(ctx ctxT, q Q, h []byte) (*AdminSession, error) {
	var s AdminSession
	err := q.QueryRowContext(ctx, `SELECT id, user_id, ip, user_agent, created_at, last_seen_at FROM admin_sessions WHERE token_hash = ?`, h).
		Scan(&s.ID, &s.UserID, &s.IP, &s.UserAgent, &s.CreatedAt, &s.LastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &s, err
}

// TouchAdminSession records activity.
func TouchAdminSession(ctx ctxT, q Q, id string, now int64) error {
	_, err := q.ExecContext(ctx, `UPDATE admin_sessions SET last_seen_at = ? WHERE id = ?`, now, id)
	return err
}

// ListAdminSessions returns the user's admin sessions, most recent first.
func ListAdminSessions(ctx ctxT, q Q, userID string) ([]AdminSession, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, user_id, ip, user_agent, created_at, last_seen_at FROM admin_sessions
		WHERE user_id = ? ORDER BY last_seen_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminSession
	for rows.Next() {
		var s AdminSession
		if err := rows.Scan(&s.ID, &s.UserID, &s.IP, &s.UserAgent, &s.CreatedAt, &s.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteAdminSession removes one of the user's admin sessions.
func DeleteAdminSession(ctx ctxT, q Q, userID, id string) error {
	res, err := q.ExecContext(ctx, `DELETE FROM admin_sessions WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAdminSessions removes all of the user's admin sessions except keep.
func DeleteAdminSessions(ctx ctxT, q Q, userID, keep string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM admin_sessions WHERE user_id = ? AND id != ?`, userID, keep)
	return err
}

// DeleteExpiredAdminSessions removes sessions idle since before idleBefore
// or created before createdBefore.
func DeleteExpiredAdminSessions(ctx ctxT, q Q, idleBefore, createdBefore int64) error {
	_, err := q.ExecContext(ctx, `DELETE FROM admin_sessions WHERE last_seen_at < ? OR created_at < ?`, idleBefore, createdBefore)
	return err
}

// ---- TOTP & recovery codes --------------------------------------------------

// TOTP is a row of admin_totp.
type TOTP struct {
	SecretEnc []byte
	EnabledAt *int64
	LastStep  int64
}

// GetTOTP loads the user's TOTP state.
func GetTOTP(ctx ctxT, q Q, userID string) (*TOTP, error) {
	var t TOTP
	var en sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT secret_enc, enabled_at, last_step FROM admin_totp WHERE user_id = ?`, userID).Scan(&t.SecretEnc, &en, &t.LastStep)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	t.EnabledAt = nint(en)
	return &t, err
}

// TOTPEnabled reports whether the user has active two-factor authentication.
func TOTPEnabled(ctx ctxT, q Q, userID string) (bool, error) {
	var ok bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admin_totp WHERE user_id = ? AND enabled_at IS NOT NULL)`, userID).Scan(&ok)
	return ok, err
}

// SetPendingTOTP stores a new, not yet enabled secret, replacing a pending
// one. An enabled secret is left untouched (callers check first).
func SetPendingTOTP(ctx ctxT, q Q, userID string, secretEnc []byte) error {
	_, err := q.ExecContext(ctx, `INSERT INTO admin_totp(user_id, secret_enc, enabled_at, last_step) VALUES (?, ?, NULL, 0)
		ON CONFLICT(user_id) DO UPDATE SET secret_enc = excluded.secret_enc, enabled_at = NULL, last_step = 0
		WHERE admin_totp.enabled_at IS NULL`, userID, secretEnc)
	return err
}

// EnableTOTP activates a pending secret, recording the step just used.
func EnableTOTP(ctx ctxT, q Q, userID string, now, step int64) (bool, error) {
	res, err := q.ExecContext(ctx, `UPDATE admin_totp SET enabled_at = ?, last_step = ? WHERE user_id = ? AND enabled_at IS NULL`, now, step, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// UseTOTPStep records a successfully verified step; it reports false if that
// step (or a later one) was already used — a replayed code.
func UseTOTPStep(ctx ctxT, q Q, userID string, step int64) (bool, error) {
	res, err := q.ExecContext(ctx, `UPDATE admin_totp SET last_step = ? WHERE user_id = ? AND last_step < ?`, step, userID, step)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeleteTOTP turns two-factor authentication off and drops recovery codes.
func DeleteTOTP(ctx ctxT, q Q, userID string) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM admin_totp WHERE user_id = ?`, userID); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `DELETE FROM admin_recovery_codes WHERE user_id = ?`, userID)
	return err
}

// ReplaceRecoveryCodes stores a fresh set of recovery code hashes.
func ReplaceRecoveryCodes(ctx ctxT, q Q, userID string, hashes [][]byte) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM admin_recovery_codes WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := q.ExecContext(ctx, `INSERT INTO admin_recovery_codes(user_id, code_hash) VALUES (?, ?)`, userID, h); err != nil {
			return err
		}
	}
	return nil
}

// UseRecoveryCode consumes an unused recovery code.
func UseRecoveryCode(ctx ctxT, q Q, userID string, hash []byte, now int64) (bool, error) {
	res, err := q.ExecContext(ctx, `UPDATE admin_recovery_codes SET used_at = ? WHERE user_id = ? AND code_hash = ? AND used_at IS NULL`, now, userID, hash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// RecoveryCodesLeft counts unused recovery codes.
func RecoveryCodesLeft(ctx ctxT, q Q, userID string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_recovery_codes WHERE user_id = ? AND used_at IS NULL`, userID).Scan(&n)
	return n, err
}

// ---- Settings ---------------------------------------------------------------

// Settings returns the panel-set settings.
func Settings(ctx ctxT, q Q) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetSetting stores (value != nil) or clears (nil) a panel setting.
func SetSetting(ctx ctxT, q Q, key string, value *string, now int64) error {
	if value == nil {
		_, err := q.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
		return err
	}
	_, err := q.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, *value, now)
	return err
}

// ---- Audit log --------------------------------------------------------------

// AuditEntry is a row of admin_audit.
type AuditEntry struct {
	ID          string
	ActorID     *string
	ActorLabel  string
	Action      string
	TargetType  *string
	TargetID    *string
	TargetLabel *string
	Detail      *string
	IP          string
	CreatedAt   int64
}

// InsertAudit appends an audit entry.
func InsertAudit(ctx ctxT, q Q, e *AuditEntry) error {
	_, err := q.ExecContext(ctx, `INSERT INTO admin_audit(id, actor_id, actor_label, action, target_type, target_id, target_label, detail, ip, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, e.ID, e.ActorID, e.ActorLabel, e.Action, e.TargetType, e.TargetID, e.TargetLabel, e.Detail, e.IP, e.CreatedAt)
	return err
}

// AuditedSince reports whether actor already has an entry with action on
// targetID at or after since (used to log repeated views only once).
func AuditedSince(ctx ctxT, q Q, actorID, action, targetID string, since int64) (bool, error) {
	var ok bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admin_audit WHERE actor_id = ? AND action = ? AND target_id = ? AND created_at >= ?)`,
		actorID, action, targetID, since).Scan(&ok)
	return ok, err
}

// ListAudit returns a page of audit entries, newest first, and the total.
func ListAudit(ctx ctxT, q Q, action string, limit, offset int) ([]AuditEntry, int, error) {
	where, args := "", []any{}
	if action != "" {
		where = ` WHERE action = ? OR action LIKE ? ESCAPE '\'`
		args = append(args, action, likeEscape(action)+".%")
	}
	var total int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_audit`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := q.QueryContext(ctx, `SELECT id, actor_id, actor_label, action, target_type, target_id, target_label, detail, ip, created_at
		FROM admin_audit`+where+` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var actor, tt, tid, tl, det sql.NullString
		if err := rows.Scan(&e.ID, &actor, &e.ActorLabel, &e.Action, &tt, &tid, &tl, &det, &e.IP, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		e.ActorID, e.TargetType, e.TargetID, e.TargetLabel, e.Detail = nstr(actor), nstr(tt), nstr(tid), nstr(tl), nstr(det)
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// likeEscape escapes LIKE wildcards; queries using it add ESCAPE '\'.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ---- Admin user listing & statistics -----------------------------------------

// UserStats are the per-user numbers the admin panel shows.
type UserStats struct {
	LastSeenAt   *int64
	Moments      int
	StorageBytes int64
	Friends      int
}

// AdminUser is a user plus statistics.
type AdminUser struct {
	*User
	UserStats
}

// UserQuery filters and orders the admin user list.
type UserQuery struct {
	Search string // matches display name, username, email, phone
	Status string // all | active | disabled | admins
	Sort   string // created_at | last_seen_at | display_name | moments | storage_bytes | friends
	Desc   bool
	Limit  int
	Offset int
}

const statsCols = `(SELECT MAX(s.last_seen_at) FROM sessions s WHERE s.user_id = u.id) AS last_seen,
	(SELECT COUNT(*) FROM moments m WHERE m.author_id = u.id) AS n_moments,
	(SELECT COALESCE(SUM(x.bytes), 0) FROM media x WHERE x.owner_id = u.id) AS n_storage,
	(SELECT COUNT(*) FROM friendships f WHERE f.user_id = u.id) AS n_friends`

var prefixedUserCols = func() string {
	parts := strings.Split(userCols, ",")
	for i, p := range parts {
		parts[i] = "u." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}()

func scanAdminUser(rows *sql.Rows) (*AdminUser, error) {
	var u User
	var username, email, phone, ph, apple, google, avatar, invitedBy, serverInvite sql.NullString
	var disabled, lastSeen sql.NullInt64
	var st UserStats
	if err := rows.Scan(&u.ID, &username, &u.DisplayName, &email, &phone, &ph, &apple, &google, &avatar,
		&u.Discoverable, &u.Searchable, &u.ReadReceipts, &u.CreatedAt, &u.UpdatedAt,
		&u.IsAdmin, &disabled, &invitedBy, &serverInvite,
		&lastSeen, &st.Moments, &st.StorageBytes, &st.Friends); err != nil {
		return nil, err
	}
	u.Username, u.Email, u.Phone, u.PasswordHash = nstr(username), nstr(email), nstr(phone), nstr(ph)
	u.AppleSub, u.GoogleSub, u.AvatarMediaID = nstr(apple), nstr(google), nstr(avatar)
	u.DisabledAt, u.InvitedBy, u.ServerInviteID = nint(disabled), nstr(invitedBy), nstr(serverInvite)
	st.LastSeenAt = nint(lastSeen)
	return &AdminUser{User: &u, UserStats: st}, nil
}

// AdminListUsers returns one page of users with statistics, and the total
// number of matches.
func AdminListUsers(ctx ctxT, q Q, f UserQuery) ([]*AdminUser, int, error) {
	var conds []string
	var args []any
	switch f.Status {
	case "active":
		conds = append(conds, "u.disabled_at IS NULL")
	case "disabled":
		conds = append(conds, "u.disabled_at IS NOT NULL")
	case "admins":
		conds = append(conds, "u.is_admin = 1")
	}
	if s := strings.ToLower(strings.TrimSpace(f.Search)); s != "" {
		pat := "%" + likeEscape(s) + "%"
		conds = append(conds, `(lower(u.display_name) LIKE ? ESCAPE '\' OR u.username LIKE ? ESCAPE '\' OR u.email LIKE ? ESCAPE '\' OR u.phone LIKE ? ESCAPE '\')`)
		args = append(args, pat, pat, pat, pat)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users u`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := map[string]string{
		"created_at": "u.created_at", "last_seen_at": "COALESCE(last_seen, 0)", "display_name": "lower(u.display_name)",
		"moments": "n_moments", "storage_bytes": "n_storage", "friends": "n_friends",
	}[f.Sort]
	if order == "" {
		order = "u.created_at"
	}
	dir := "ASC"
	if f.Desc {
		dir = "DESC"
	}
	if f.Limit <= 0 {
		f.Limit = 25
	}
	rows, err := q.QueryContext(ctx, `SELECT `+prefixedUserCols+`, `+statsCols+` FROM users u`+where+
		` ORDER BY `+order+` `+dir+`, u.id `+dir+` LIMIT ? OFFSET ?`, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*AdminUser
	for rows.Next() {
		u, err := scanAdminUser(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	return out, total, rows.Err()
}

// AdminGetUser loads one user with statistics.
func AdminGetUser(ctx ctxT, q Q, id string) (*AdminUser, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+prefixedUserCols+`, `+statsCols+` FROM users u WHERE u.id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, ErrNotFound
	}
	return scanAdminUser(rows)
}

// Overview holds server-wide counts for the admin dashboard.
type Overview struct {
	Users, Active7d, Disabled, Admins int
	Moments, Messages                 int
	MediaBytes                        int64
	MediaFiles                        int
}

// AdminOverview computes the dashboard counters (all cheap aggregate
// queries over indexed tables).
func AdminOverview(ctx ctxT, q Q, activeSince int64) (*Overview, error) {
	var o Overview
	err := q.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM users),
		(SELECT COUNT(DISTINCT user_id) FROM sessions WHERE last_seen_at >= ?),
		(SELECT COUNT(*) FROM users WHERE disabled_at IS NOT NULL),
		(SELECT COUNT(*) FROM users WHERE is_admin = 1),
		(SELECT COUNT(*) FROM moments),
		(SELECT COUNT(*) FROM messages),
		(SELECT COALESCE(SUM(bytes), 0) FROM media),
		(SELECT COUNT(*) FROM media)`, activeSince).
		Scan(&o.Users, &o.Active7d, &o.Disabled, &o.Admins, &o.Moments, &o.Messages, &o.MediaBytes, &o.MediaFiles)
	return &o, err
}

// DeviceSessionIDs returns the ids of the user's sessions that have a push
// device registered.
func DeviceSessionIDs(ctx ctxT, q Q, userID string) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT session_id FROM devices WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// FriendInviteOwner returns the owner of a member invite by code hash.
func FriendInviteOwner(ctx ctxT, q Q, codeHash []byte) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, `SELECT user_id FROM invites WHERE code_hash = ?`, codeHash).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}
