package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strings"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/db"
)

type ctxT = context.Context

const userCols = `id, username, display_name, email, phone, password_hash, apple_sub, google_sub,
	avatar_media_id, discoverable, searchable, read_receipts, created_at, updated_at,
	is_admin, disabled_at, invited_by, server_invite_id`

type scanner interface{ Scan(dest ...any) error }

func scanUser(s scanner) (*User, error) {
	var u User
	var username, email, phone, ph, apple, google, avatar, invitedBy, serverInvite sql.NullString
	var disabled sql.NullInt64
	if err := s.Scan(&u.ID, &username, &u.DisplayName, &email, &phone, &ph, &apple, &google, &avatar,
		&u.Discoverable, &u.Searchable, &u.ReadReceipts, &u.CreatedAt, &u.UpdatedAt,
		&u.IsAdmin, &disabled, &invitedBy, &serverInvite); err != nil {
		return nil, err
	}
	u.Username, u.Email, u.Phone, u.PasswordHash = nstr(username), nstr(email), nstr(phone), nstr(ph)
	u.AppleSub, u.GoogleSub, u.AvatarMediaID = nstr(apple), nstr(google), nstr(avatar)
	u.DisabledAt, u.InvitedBy, u.ServerInviteID = nint(disabled), nstr(invitedBy), nstr(serverInvite)
	return &u, nil
}

func mapUserUnique(err error) error {
	col, ok := isUnique(err)
	if !ok {
		return err
	}
	switch col {
	case "users.username":
		return ErrUsernameTaken
	case "users.email", "users.phone":
		return ErrIdentifierTaken
	case "users.apple_sub":
		return ErrAppleSubTaken
	case "users.google_sub":
		return ErrGoogleSubTaken
	}
	return err
}

// InsertUser creates a user row and its contact hashes.
func InsertUser(ctx ctxT, q Q, u *User) error {
	_, err := q.ExecContext(ctx, `INSERT INTO users(`+userCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		u.ID, u.Username, u.DisplayName, u.Email, u.Phone, u.PasswordHash, u.AppleSub, u.GoogleSub,
		u.AvatarMediaID, u.Discoverable, u.Searchable, u.ReadReceipts, u.CreatedAt, u.UpdatedAt,
		u.IsAdmin, u.DisabledAt, u.InvitedBy, u.ServerInviteID)
	if err != nil {
		return mapUserUnique(err)
	}
	return SyncContactHashes(ctx, q, u.ID)
}

// GetUser loads one user.
func GetUser(ctx ctxT, q Q, id string) (*User, error) {
	u, err := scanUser(q.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// GetUsers loads many users keyed by id (missing ids are absent).
func GetUsers(ctx ctxT, q Q, ids []string) (map[string]*User, error) {
	out := make(map[string]*User, len(ids))
	ids = dedupe(ids)
	for len(ids) > 0 {
		n := min(len(ids), 500)
		rows, err := q.QueryContext(ctx, `SELECT `+userCols+` FROM users WHERE id IN (`+db.Placeholders(n)+`)`, db.Args(nil, ids[:n])...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			u, err := scanUser(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out[u.ID] = u
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		ids = ids[n:]
	}
	return out, nil
}

// UserBy looks a user up by a unique column: username, email, phone,
// apple_sub or google_sub.
func UserBy(ctx ctxT, q Q, column, value string) (*User, error) {
	switch column {
	case "username", "email", "phone", "apple_sub", "google_sub":
	default:
		return nil, errors.New("store: invalid lookup column")
	}
	u, err := scanUser(q.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE `+column+` = ?`, value))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// ProfileUpdate holds optional profile changes (nil = unchanged).
type ProfileUpdate struct {
	DisplayName  *string
	Username     *string
	Discoverable *bool
	Searchable   *bool
	ReadReceipts *bool
}

// UpdateProfile applies a ProfileUpdate.
func UpdateProfile(ctx ctxT, q Q, id string, p ProfileUpdate, now int64) error {
	_, err := q.ExecContext(ctx, `UPDATE users SET
		display_name  = COALESCE(?, display_name),
		username      = COALESCE(?, username),
		discoverable  = COALESCE(?, discoverable),
		searchable    = COALESCE(?, searchable),
		read_receipts = COALESCE(?, read_receipts),
		updated_at    = ?
		WHERE id = ?`, p.DisplayName, p.Username, p.Discoverable, p.Searchable, p.ReadReceipts, now, id)
	if err != nil {
		return mapUserUnique(err)
	}
	if p.Discoverable != nil {
		return SyncContactHashes(ctx, q, id)
	}
	return nil
}

// SetPassword stores a new password hash.
func SetPassword(ctx ctxT, q Q, id, hash string, now int64) error {
	_, err := q.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, hash, now, id)
	return err
}

// SetIdentifier sets the user's email or phone.
func SetIdentifier(ctx ctxT, q Q, id, method, value string, now int64) error {
	col := "email"
	if method == "phone" {
		col = "phone"
	}
	if _, err := q.ExecContext(ctx, `UPDATE users SET `+col+` = ?, updated_at = ? WHERE id = ?`, value, now, id); err != nil {
		return mapUserUnique(err)
	}
	return SyncContactHashes(ctx, q, id)
}

// LinkSub links an Apple or Google subject to a user.
func LinkSub(ctx ctxT, q Q, id, provider, sub string, now int64) error {
	col := "apple_sub"
	if provider == "google" {
		col = "google_sub"
	}
	_, err := q.ExecContext(ctx, `UPDATE users SET `+col+` = ?, updated_at = ? WHERE id = ?`, sub, now, id)
	return mapUserUnique(err)
}

// UnlinkSub removes the user's Apple or Google identity; it reports whether
// one was linked.
func UnlinkSub(ctx ctxT, q Q, id, provider string, now int64) (bool, error) {
	col := "apple_sub"
	if provider == "google" {
		col = "google_sub"
	}
	res, err := q.ExecContext(ctx, `UPDATE users SET `+col+` = NULL, updated_at = ? WHERE id = ? AND `+col+` IS NOT NULL`, now, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// SetAvatar points the user at a new avatar (or none) and returns the old one.
func SetAvatar(ctx ctxT, q Q, id string, mediaID *string, now int64) (*string, error) {
	var old sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT avatar_media_id FROM users WHERE id = ?`, id).Scan(&old); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if _, err := q.ExecContext(ctx, `UPDATE users SET avatar_media_id = ?, updated_at = ? WHERE id = ?`, mediaID, now, id); err != nil {
		return nil, err
	}
	return nstr(old), nil
}

// DeleteUser removes the user; ON DELETE CASCADE removes sessions, devices,
// friendships, requests, blocks, invites, moments, feed entries, reactions,
// messages and conversations. Media rows are detached (owner_id NULL) and
// must be deleted by the caller (see MediaOwnedBy) or the orphan sweeper.
func DeleteUser(ctx ctxT, q Q, id string) error {
	res, err := q.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListUsers returns all users ordered by creation (admin CLI).
func ListUsers(ctx ctxT, q Q) ([]*User, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SearchUsers returns searchable users whose username starts with prefix,
// excluding me and anyone in a block relationship with me. prefix must be
// lowercase [a-z0-9._]. Exact match first, then alphabetical.
func SearchUsers(ctx ctxT, q Q, me, prefix string, limit int) ([]*User, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+userCols+` FROM users u
		WHERE u.username >= ?1 AND u.username < ?1 || '~'
		  AND u.searchable = 1 AND u.disabled_at IS NULL AND u.id != ?2
		  AND NOT EXISTS (SELECT 1 FROM blocks b WHERE (b.blocker_id = ?2 AND b.blocked_id = u.id) OR (b.blocker_id = u.id AND b.blocked_id = ?2))
		ORDER BY (u.username = ?1) DESC, u.username
		LIMIT ?3`, prefix, me, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ContactHash computes the contact discovery hash of a normalized identifier.
func ContactHash(normalized string) []byte {
	h := sha256.Sum256([]byte("frame-contact-v1:" + normalized))
	return h[:]
}

// SyncContactHashes rewrites the user's identifier hashes: present only when
// the user is discoverable.
func SyncContactHashes(ctx ctxT, q Q, id string) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM contact_hashes WHERE user_id = ?`, id); err != nil {
		return err
	}
	var email, phone sql.NullString
	var disc bool
	if err := q.QueryRowContext(ctx, `SELECT email, phone, discoverable FROM users WHERE id = ?`, id).Scan(&email, &phone, &disc); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if !disc {
		return nil
	}
	for _, v := range []sql.NullString{email, phone} {
		if v.Valid && v.String != "" {
			if _, err := q.ExecContext(ctx, `INSERT OR IGNORE INTO contact_hashes(hash, user_id) VALUES (?, ?)`,
				ContactHash(strings.ToLower(strings.TrimSpace(v.String))), id); err != nil {
				return err
			}
		}
	}
	return nil
}

// ContactMatch is one matched uploaded hash.
type ContactMatch struct {
	Hash   []byte
	UserID string
}

// MatchContacts returns users (other than me, not in a block relationship
// with me) owning any of the hashes. The hashes are only used for lookup.
func MatchContacts(ctx ctxT, q Q, me string, hashes [][]byte) ([]ContactMatch, error) {
	var out []ContactMatch
	for len(hashes) > 0 {
		n := min(len(hashes), 500)
		args := []any{me}
		for _, h := range hashes[:n] {
			args = append(args, h)
		}
		rows, err := q.QueryContext(ctx, `SELECT c.hash, c.user_id FROM contact_hashes c
			JOIN users u ON u.id = c.user_id AND u.discoverable = 1 AND u.disabled_at IS NULL
			WHERE c.user_id != ?1 AND c.hash IN (`+db.Placeholders(n)+`)
			  AND NOT EXISTS (SELECT 1 FROM blocks b WHERE (b.blocker_id = ?1 AND b.blocked_id = c.user_id) OR (b.blocker_id = c.user_id AND b.blocked_id = ?1))`,
			args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var m ContactMatch
			if err := rows.Scan(&m.Hash, &m.UserID); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, m)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		hashes = hashes[n:]
	}
	return out, nil
}

func dedupe(ss []string) []string {
	seen := make(map[string]struct{}, len(ss))
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
