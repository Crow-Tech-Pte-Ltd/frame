package store

import (
	"database/sql"
	"errors"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/db"
)

func placeholders(n int) string { return db.Placeholders(n) }

// Friendship states as seen by "me".
const (
	RelSelf     = "self"
	RelFriends  = "friends"
	RelIncoming = "incoming"
	RelOutgoing = "outgoing"
	RelNone     = "none"
	RelBlocked  = "blocked" // I blocked them
)

// Relation computes how other relates to me. blockedMe reports whether other
// has blocked me (callers usually treat that as "not found").
func Relation(ctx ctxT, q Q, me, other string) (rel string, blockedMe bool, err error) {
	if me == other {
		return RelSelf, false, nil
	}
	var iBlocked, theyBlocked, friends, outgoing, incoming bool
	err = q.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM blocks WHERE blocker_id = ?1 AND blocked_id = ?2),
		EXISTS(SELECT 1 FROM blocks WHERE blocker_id = ?2 AND blocked_id = ?1),
		EXISTS(SELECT 1 FROM friendships WHERE user_id = ?1 AND friend_id = ?2),
		EXISTS(SELECT 1 FROM friend_requests WHERE from_id = ?1 AND to_id = ?2),
		EXISTS(SELECT 1 FROM friend_requests WHERE from_id = ?2 AND to_id = ?1)`, me, other).
		Scan(&iBlocked, &theyBlocked, &friends, &outgoing, &incoming)
	if err != nil {
		return "", false, err
	}
	switch {
	case iBlocked:
		return RelBlocked, theyBlocked, nil
	case theyBlocked:
		return RelNone, true, nil
	case friends:
		return RelFriends, false, nil
	case incoming:
		return RelIncoming, false, nil
	case outgoing:
		return RelOutgoing, false, nil
	}
	return RelNone, false, nil
}

// Relations computes Relation for many users at once (blocked-me users get
// RelNone; callers filter them separately where needed).
func Relations(ctx ctxT, q Q, me string, others []string) (map[string]string, error) {
	out := make(map[string]string, len(others))
	others = dedupe(others)
	for _, o := range others {
		out[o] = RelNone
		if o == me {
			out[o] = RelSelf
		}
	}
	if len(others) == 0 {
		return out, nil
	}
	args := db.Args([]any{me}, others)
	ph := placeholders(len(others))
	type pass struct {
		sql string
		rel string
	}
	// Later passes override earlier ones, mirroring Relation's precedence.
	for _, p := range []pass{
		{`SELECT from_id FROM friend_requests WHERE to_id = ?1 AND from_id IN (` + ph + `)`, ""},
		{`SELECT to_id FROM friend_requests WHERE from_id = ?1 AND to_id IN (` + ph + `)`, RelOutgoing},
		{`SELECT friend_id FROM friendships WHERE user_id = ?1 AND friend_id IN (` + ph + `)`, RelFriends},
		{`SELECT blocked_id FROM blocks WHERE blocker_id = ?1 AND blocked_id IN (` + ph + `)`, RelBlocked},
	} {
		rel := p.rel
		if rel == "" {
			rel = RelIncoming
		}
		rows, err := q.QueryContext(ctx, p.sql, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = rel
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	// Precedence fix-up: an incoming request loses to an outgoing one only
	// if both exist, which cannot happen (auto-accept), so order is fine.
	return out, nil
}

// AreFriends reports whether a and b are friends.
func AreFriends(ctx ctxT, q Q, a, b string) (bool, error) {
	var ok bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM friendships WHERE user_id = ? AND friend_id = ?)`, a, b).Scan(&ok)
	return ok, err
}

// BlockedEither reports whether a blocked b or b blocked a.
func BlockedEither(ctx ctxT, q Q, a, b string) (bool, error) {
	var ok bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM blocks WHERE (blocker_id = ?1 AND blocked_id = ?2) OR (blocker_id = ?2 AND blocked_id = ?1))`, a, b).Scan(&ok)
	return ok, err
}

// Friend is one entry of a friend list.
type Friend struct {
	UserID string
	Since  int64
}

// ListFriends returns my friends, newest friendship first.
func ListFriends(ctx ctxT, q Q, me string) ([]Friend, error) {
	rows, err := q.QueryContext(ctx, `SELECT friend_id, created_at FROM friendships WHERE user_id = ? ORDER BY created_at DESC, friend_id`, me)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Friend
	for rows.Next() {
		var f Friend
		if err := rows.Scan(&f.UserID, &f.Since); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FriendIDs returns the ids of my friends.
func FriendIDs(ctx ctxT, q Q, me string) ([]string, error) {
	fs, err := ListFriends(ctx, q, me)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(fs))
	for i, f := range fs {
		ids[i] = f.UserID
	}
	return ids, nil
}

// CountFriends returns how many friends the user has.
func CountFriends(ctx ctxT, q Q, me string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM friendships WHERE user_id = ?`, me).Scan(&n)
	return n, err
}

// FriendSince returns when a and b became friends (ErrNotFound if not).
func FriendSince(ctx ctxT, q Q, a, b string) (int64, error) {
	var at int64
	err := q.QueryRowContext(ctx, `SELECT created_at FROM friendships WHERE user_id = ? AND friend_id = ?`, a, b).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return at, err
}

// AddFriendship makes a and b friends (idempotent) and clears pending
// requests between them. Returns the friendship timestamp.
func AddFriendship(ctx ctxT, q Q, a, b string, now int64) (int64, error) {
	for _, p := range [][2]string{{a, b}, {b, a}} {
		if _, err := q.ExecContext(ctx, `INSERT OR IGNORE INTO friendships(user_id, friend_id, created_at) VALUES (?,?,?)`, p[0], p[1], now); err != nil {
			return 0, err
		}
	}
	if err := DeleteRequestsBetween(ctx, q, a, b); err != nil {
		return 0, err
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM friend_removals WHERE (remover_id = ?1 AND removed_id = ?2) OR (remover_id = ?2 AND removed_id = ?1)`, a, b); err != nil {
		return 0, err
	}
	return FriendSince(ctx, q, a, b)
}

// RecordRemoval notes that remover unfriended or blocked removed, so the
// remover's invite links no longer befriend removed directly.
func RecordRemoval(ctx ctxT, q Q, remover, removed string, now int64) error {
	_, err := q.ExecContext(ctx, `INSERT INTO friend_removals(remover_id, removed_id, created_at) VALUES (?,?,?)
		ON CONFLICT(remover_id, removed_id) DO UPDATE SET created_at = excluded.created_at`, remover, removed, now)
	return err
}

// RemovedBy reports whether remover previously unfriended or blocked removed.
func RemovedBy(ctx ctxT, q Q, remover, removed string) (bool, error) {
	var ok bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM friend_removals WHERE remover_id = ? AND removed_id = ?)`, remover, removed).Scan(&ok)
	return ok, err
}

// RemoveFriendship unfriends a and b; reports whether they were friends.
func RemoveFriendship(ctx ctxT, q Q, a, b string) (bool, error) {
	res, err := q.ExecContext(ctx, `DELETE FROM friendships WHERE (user_id = ?1 AND friend_id = ?2) OR (user_id = ?2 AND friend_id = ?1)`, a, b)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// FriendRequest is a row of friend_requests.
type FriendRequest struct {
	ID        string
	FromID    string
	ToID      string
	CreatedAt int64
}

// InsertRequest creates a friend request (idempotent: returns the existing
// one if from→to already exists).
func InsertRequest(ctx ctxT, q Q, r *FriendRequest) (*FriendRequest, error) {
	if _, err := q.ExecContext(ctx, `INSERT OR IGNORE INTO friend_requests(id, from_id, to_id, created_at) VALUES (?,?,?,?)`,
		r.ID, r.FromID, r.ToID, r.CreatedAt); err != nil {
		return nil, err
	}
	return RequestBetween(ctx, q, r.FromID, r.ToID)
}

// RequestBetween returns the pending request from→to.
func RequestBetween(ctx ctxT, q Q, from, to string) (*FriendRequest, error) {
	var r FriendRequest
	err := q.QueryRowContext(ctx, `SELECT id, from_id, to_id, created_at FROM friend_requests WHERE from_id = ? AND to_id = ?`, from, to).
		Scan(&r.ID, &r.FromID, &r.ToID, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &r, err
}

// GetRequest loads a request by id.
func GetRequest(ctx ctxT, q Q, id string) (*FriendRequest, error) {
	var r FriendRequest
	err := q.QueryRowContext(ctx, `SELECT id, from_id, to_id, created_at FROM friend_requests WHERE id = ?`, id).
		Scan(&r.ID, &r.FromID, &r.ToID, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &r, err
}

// ListRequests returns my incoming and outgoing requests, newest first.
func ListRequests(ctx ctxT, q Q, me string) (incoming, outgoing []FriendRequest, err error) {
	rows, err := q.QueryContext(ctx, `SELECT id, from_id, to_id, created_at FROM friend_requests
		WHERE to_id = ?1 OR from_id = ?1 ORDER BY id DESC`, me)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r FriendRequest
		if err := rows.Scan(&r.ID, &r.FromID, &r.ToID, &r.CreatedAt); err != nil {
			return nil, nil, err
		}
		if r.ToID == me {
			incoming = append(incoming, r)
		} else {
			outgoing = append(outgoing, r)
		}
	}
	return incoming, outgoing, rows.Err()
}

// DeleteRequest removes a request by id.
func DeleteRequest(ctx ctxT, q Q, id string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM friend_requests WHERE id = ?`, id)
	return err
}

// DeleteRequestsBetween removes requests in both directions.
func DeleteRequestsBetween(ctx ctxT, q Q, a, b string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM friend_requests WHERE (from_id = ?1 AND to_id = ?2) OR (from_id = ?2 AND to_id = ?1)`, a, b)
	return err
}

// InsertBlock records that blocker blocked blocked (idempotent).
func InsertBlock(ctx ctxT, q Q, blocker, blocked string, now int64) error {
	_, err := q.ExecContext(ctx, `INSERT OR IGNORE INTO blocks(blocker_id, blocked_id, created_at) VALUES (?,?,?)`, blocker, blocked, now)
	return err
}

// DeleteBlock removes a block.
func DeleteBlock(ctx ctxT, q Q, blocker, blocked string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM blocks WHERE blocker_id = ? AND blocked_id = ?`, blocker, blocked)
	return err
}

// ListBlocked returns the ids I blocked, newest first.
func ListBlocked(ctx ctxT, q Q, me string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT blocked_id FROM blocks WHERE blocker_id = ? ORDER BY created_at DESC`, me)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Invite is a row of invites.
type Invite struct {
	UserID    string
	CodeEnc   []byte
	CreatedAt int64
	ExpiresAt int64
	Uses      int
}

// InsertInvite stores an invite: code hash for lookup, encrypted code for
// listing.
func InsertInvite(ctx ctxT, q Q, codeHash, codeEnc []byte, userID string, now, expires int64) error {
	_, err := q.ExecContext(ctx, `INSERT INTO invites(code_hash, code_enc, user_id, created_at, expires_at) VALUES (?,?,?,?,?)`,
		codeHash, codeEnc, userID, now, expires)
	return err
}

// InviteByHash returns a non-expired invite whose owner isn't suspended.
func InviteByHash(ctx ctxT, q Q, codeHash []byte, now int64) (*Invite, error) {
	var inv Invite
	err := q.QueryRowContext(ctx, `SELECT i.user_id, i.code_enc, i.created_at, i.expires_at, i.uses FROM invites i
		JOIN users u ON u.id = i.user_id AND u.disabled_at IS NULL
		WHERE i.code_hash = ? AND i.expires_at > ?`, codeHash, now).
		Scan(&inv.UserID, &inv.CodeEnc, &inv.CreatedAt, &inv.ExpiresAt, &inv.Uses)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &inv, err
}

// ListInvites returns the user's unexpired invites, newest first.
func ListInvites(ctx ctxT, q Q, userID string, now int64) ([]Invite, error) {
	rows, err := q.QueryContext(ctx, `SELECT user_id, code_enc, created_at, expires_at, uses FROM invites
		WHERE user_id = ? AND expires_at > ? ORDER BY created_at DESC`, userID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		var inv Invite
		if err := rows.Scan(&inv.UserID, &inv.CodeEnc, &inv.CreatedAt, &inv.ExpiresAt, &inv.Uses); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// DeleteInvite revokes one of the user's invites; reports whether it existed.
func DeleteInvite(ctx ctxT, q Q, userID string, codeHash []byte) (bool, error) {
	res, err := q.ExecContext(ctx, `DELETE FROM invites WHERE code_hash = ? AND user_id = ?`, codeHash, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CountInviteUse increments an invite's use counter.
func CountInviteUse(ctx ctxT, q Q, codeHash []byte) error {
	_, err := q.ExecContext(ctx, `UPDATE invites SET uses = uses + 1 WHERE code_hash = ?`, codeHash)
	return err
}

// DeleteExpiredInvites removes invites past their expiry.
func DeleteExpiredInvites(ctx ctxT, q Q, now int64) (int64, error) {
	res, err := q.ExecContext(ctx, `DELETE FROM invites WHERE expires_at <= ?`, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
