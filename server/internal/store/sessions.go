package store

import (
	"database/sql"
	"errors"
)

// Session is a row of the sessions table.
type Session struct {
	ID         string
	UserID     string
	DeviceName string
	Platform   string
	CreatedAt  int64
	LastSeenAt int64
}

// InsertSession stores a new session with the SHA-256 of its token.
func InsertSession(ctx ctxT, q Q, s *Session, tokenHash []byte) error {
	_, err := q.ExecContext(ctx, `INSERT INTO sessions(id, user_id, token_hash, device_name, platform, created_at, last_seen_at)
		VALUES (?,?,?,?,?,?,?)`, s.ID, s.UserID, tokenHash, s.DeviceName, s.Platform, s.CreatedAt, s.LastSeenAt)
	return err
}

// SessionByTokenHash resolves a bearer token hash.
func SessionByTokenHash(ctx ctxT, q Q, h []byte) (*Session, error) {
	var s Session
	err := q.QueryRowContext(ctx, `SELECT id, user_id, device_name, platform, created_at, last_seen_at
		FROM sessions WHERE token_hash = ?`, h).Scan(&s.ID, &s.UserID, &s.DeviceName, &s.Platform, &s.CreatedAt, &s.LastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &s, err
}

// SessionAlive reports whether a session still exists (SSE re-validation).
func SessionAlive(ctx ctxT, q Q, id string) (bool, error) {
	var ok bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id = ?)`, id).Scan(&ok)
	return ok, err
}

// UseNonce records a single-use nonce hash until expiresAt. It reports false
// if the nonce was already used.
func UseNonce(ctx ctxT, q Q, hash []byte, expiresAt int64) (bool, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO used_nonces(hash, expires_at) VALUES (?, ?) ON CONFLICT(hash) DO NOTHING`, hash, expiresAt)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeleteExpiredNonces forgets nonces whose tokens have expired.
func DeleteExpiredNonces(ctx ctxT, q Q, now int64) error {
	_, err := q.ExecContext(ctx, `DELETE FROM used_nonces WHERE expires_at < ?`, now)
	return err
}

// ResetAttempt counts a password-reset confirm attempt against the user's
// 24 h budget (independent of how many codes were requested). It reports
// false once max attempts were used in the current window.
func ResetAttempt(ctx ctxT, q Q, userID string, now int64, window int64, max int) (bool, error) {
	if _, err := q.ExecContext(ctx, `INSERT INTO reset_limits(user_id, window_start, attempts) VALUES (?1, ?2, 0)
		ON CONFLICT(user_id) DO UPDATE SET window_start = ?2, attempts = 0 WHERE reset_limits.window_start <= ?2 - ?3`,
		userID, now, window); err != nil {
		return false, err
	}
	res, err := q.ExecContext(ctx, `UPDATE reset_limits SET attempts = attempts + 1 WHERE user_id = ? AND attempts < ?`, userID, max)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// TouchSession updates last_seen_at if it is older than staleBefore.
func TouchSession(ctx ctxT, q Q, id string, now, staleBefore int64) error {
	_, err := q.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id = ? AND last_seen_at < ?`, now, id, staleBefore)
	return err
}

// ListSessions returns the user's sessions, most recently used first.
func ListSessions(ctx ctxT, q Q, userID string) ([]Session, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, user_id, device_name, platform, created_at, last_seen_at
		FROM sessions WHERE user_id = ? ORDER BY last_seen_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.UserID, &s.DeviceName, &s.Platform, &s.CreatedAt, &s.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteSession removes one of the user's sessions (and its device).
func DeleteSession(ctx ctxT, q Q, userID, id string) error {
	res, err := q.ExecContext(ctx, `DELETE FROM sessions WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteUserSessions removes all the user's sessions except keep ("" = all)
// and returns the removed ids.
func DeleteUserSessions(ctx ctxT, q Q, userID, keep string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id != ? RETURNING id`, userID, keep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// RevokedDisabled is the revoked_sessions reason for sessions ended by a
// suspension (reported to the app as 401 account_disabled).
const RevokedDisabled = "account_disabled"

// MarkSessionsRevoked remembers the token hashes of all the user's current
// sessions with reason, before they are deleted.
func MarkSessionsRevoked(ctx ctxT, q Q, userID, reason string, now int64) error {
	_, err := q.ExecContext(ctx, `INSERT INTO revoked_sessions(token_hash, user_id, reason, revoked_at)
		SELECT token_hash, user_id, ?, ? FROM sessions WHERE user_id = ?
		ON CONFLICT(token_hash) DO UPDATE SET reason = excluded.reason, revoked_at = excluded.revoked_at`,
		reason, now, userID)
	return err
}

// DeleteRevokedSessions forgets the user's revoked-session reasons (the user
// was reinstated: old tokens are then plain invalid tokens).
func DeleteRevokedSessions(ctx ctxT, q Q, userID string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM revoked_sessions WHERE user_id = ?`, userID)
	return err
}

// RevokedReason returns why the session with token hash h was revoked, if
// it was revoked by a suspension that is still in effect; ErrNotFound
// otherwise (unknown tokens, other revocations, reinstated users).
func RevokedReason(ctx ctxT, q Q, h []byte) (string, error) {
	var reason string
	err := q.QueryRowContext(ctx, `SELECT r.reason FROM revoked_sessions r
		JOIN users u ON u.id = r.user_id AND u.disabled_at IS NOT NULL
		WHERE r.token_hash = ?`, h).Scan(&reason)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return reason, err
}

// Device is a push device bound to a session.
type Device struct {
	SessionID        string
	UserID           string
	Platform         string
	RelayToken       string
	WidgetRelayToken *string
	PushKey          []byte
	PrefMoments      bool
	PrefMessages     bool
	PrefReactions    bool
	PrefFriends      bool
	UpdatedAt        int64
}

// UpsertDevice replaces the session's device. Any other device row carrying
// the same relay token (e.g. a previous account on the same phone) is
// removed so pushes never reach the wrong account.
func UpsertDevice(ctx ctxT, q Q, d *Device) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM devices WHERE relay_token = ? AND session_id != ?`, d.RelayToken, d.SessionID); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `INSERT INTO devices(session_id, user_id, platform, relay_token, widget_relay_token, push_key,
		pref_moments, pref_messages, pref_reactions, pref_friends, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(session_id) DO UPDATE SET platform = excluded.platform, relay_token = excluded.relay_token,
		widget_relay_token = excluded.widget_relay_token, push_key = excluded.push_key,
		pref_moments = excluded.pref_moments, pref_messages = excluded.pref_messages,
		pref_reactions = excluded.pref_reactions, pref_friends = excluded.pref_friends, updated_at = excluded.updated_at`,
		d.SessionID, d.UserID, d.Platform, d.RelayToken, d.WidgetRelayToken, d.PushKey,
		d.PrefMoments, d.PrefMessages, d.PrefReactions, d.PrefFriends, d.UpdatedAt)
	return err
}

// DeleteDevice removes the session's device.
func DeleteDevice(ctx ctxT, q Q, sessionID string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM devices WHERE session_id = ?`, sessionID)
	return err
}

// DeleteDeviceByToken removes devices using a relay token the relay reported
// as unregistered (alert or widget token).
func DeleteDeviceByToken(ctx ctxT, q Q, token string) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM devices WHERE relay_token = ?`, token); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `UPDATE devices SET widget_relay_token = NULL WHERE widget_relay_token = ?`, token)
	return err
}

// DevicesFor returns all push devices of the given users.
func DevicesFor(ctx ctxT, q Q, userIDs []string) ([]Device, error) {
	var out []Device
	userIDs = dedupe(userIDs)
	for len(userIDs) > 0 {
		n := min(len(userIDs), 500)
		args := make([]any, n)
		for i, id := range userIDs[:n] {
			args[i] = id
		}
		rows, err := q.QueryContext(ctx, `SELECT session_id, user_id, platform, relay_token, widget_relay_token, push_key,
			pref_moments, pref_messages, pref_reactions, pref_friends, updated_at
			FROM devices WHERE user_id IN (`+placeholders(n)+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var d Device
			var w sql.NullString
			if err := rows.Scan(&d.SessionID, &d.UserID, &d.Platform, &d.RelayToken, &w, &d.PushKey,
				&d.PrefMoments, &d.PrefMessages, &d.PrefReactions, &d.PrefFriends, &d.UpdatedAt); err != nil {
				rows.Close()
				return nil, err
			}
			d.WidgetRelayToken = nstr(w)
			out = append(out, d)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		userIDs = userIDs[n:]
	}
	return out, nil
}

// SetPasswordReset stores a reset code hash for the user.
func SetPasswordReset(ctx ctxT, q Q, userID string, codeHash []byte, expiresAt int64) error {
	_, err := q.ExecContext(ctx, `INSERT INTO password_resets(user_id, code_hash, expires_at, attempts) VALUES (?,?,?,0)
		ON CONFLICT(user_id) DO UPDATE SET code_hash = excluded.code_hash, expires_at = excluded.expires_at, attempts = 0`,
		userID, codeHash, expiresAt)
	return err
}

// PasswordReset loads a pending reset and counts an attempt.
func PasswordReset(ctx ctxT, q Q, userID string, now int64) (codeHash []byte, err error) {
	var exp, attempts int64
	err = q.QueryRowContext(ctx, `SELECT code_hash, expires_at, attempts FROM password_resets WHERE user_id = ?`, userID).Scan(&codeHash, &exp, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if exp < now || attempts >= 5 {
		_, _ = q.ExecContext(ctx, `DELETE FROM password_resets WHERE user_id = ?`, userID)
		return nil, ErrNotFound
	}
	_, err = q.ExecContext(ctx, `UPDATE password_resets SET attempts = attempts + 1 WHERE user_id = ?`, userID)
	return codeHash, err
}

// DeletePasswordReset removes a pending reset.
func DeletePasswordReset(ctx ctxT, q Q, userID string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM password_resets WHERE user_id = ?`, userID)
	return err
}
