package store

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/db"
)

const messageCols = `id, client_id, sender_id, recipient_id, text, moment_id, moment_author_id, moment_kind, created_at, read_at, receipt_at`

func scanMessage(s scanner) (*Message, error) {
	var m Message
	var mid, mauthor, mkind sql.NullString
	var readAt, receiptAt sql.NullInt64
	if err := s.Scan(&m.ID, &m.ClientID, &m.SenderID, &m.RecipientID, &m.Text, &mid, &mauthor, &mkind,
		&m.CreatedAt, &readAt, &receiptAt); err != nil {
		return nil, err
	}
	m.MomentID, m.MomentAuthorID, m.MomentKind = nstr(mid), nstr(mauthor), nstr(mkind)
	m.ReadAt, m.ReceiptAt = nint(readAt), nint(receiptAt)
	return &m, nil
}

// InsertMessage stores a message and bumps both conversation rows.
func InsertMessage(ctx ctxT, q Q, m *Message) error {
	_, err := q.ExecContext(ctx, `INSERT INTO messages(id, client_id, sender_id, recipient_id, conv, text, moment_id, moment_author_id, moment_kind, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, m.ID, m.ClientID, m.SenderID, m.RecipientID, ConvKey(m.SenderID, m.RecipientID), m.Text,
		m.MomentID, m.MomentAuthorID, m.MomentKind, m.CreatedAt)
	if err != nil {
		if col, ok := isUnique(err); ok && strings.Contains(col, "client_id") {
			return ErrDuplicateClient
		}
		return err
	}
	for _, p := range [][2]string{{m.SenderID, m.RecipientID}, {m.RecipientID, m.SenderID}} {
		if _, err := q.ExecContext(ctx, `INSERT INTO conversations(user_id, peer_id, last_message_id, updated_at) VALUES (?,?,?,?)
			ON CONFLICT(user_id, peer_id) DO UPDATE SET last_message_id = excluded.last_message_id, updated_at = excluded.updated_at`,
			p[0], p[1], m.ID, m.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

// GetMessage loads a message.
func GetMessage(ctx ctxT, q Q, id string) (*Message, error) {
	m, err := scanMessage(q.QueryRowContext(ctx, `SELECT `+messageCols+` FROM messages WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// MessageByClientID finds a sender's message by idempotency key.
func MessageByClientID(ctx ctxT, q Q, senderID, clientID string) (*Message, error) {
	m, err := scanMessage(q.QueryRowContext(ctx, `SELECT `+messageCols+` FROM messages WHERE sender_id = ? AND client_id = ?`, senderID, clientID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// GetMessages loads messages keyed by id.
func GetMessages(ctx ctxT, q Q, ids []string) (map[string]*Message, error) {
	out := map[string]*Message{}
	ids = dedupe(ids)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT `+messageCols+` FROM messages WHERE id IN (`+db.Placeholders(len(ids))+`)`, db.Args(nil, ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out[m.ID] = m
	}
	return out, rows.Err()
}

// MessagesPage returns messages between a and b, newest first.
func MessagesPage(ctx ctxT, q Q, a, b, beforeID string, limit int) ([]*Message, error) {
	args := []any{ConvKey(a, b)}
	cond := ""
	if beforeID != "" {
		cond = ` AND id < ?`
		args = append(args, beforeID)
	}
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, `SELECT `+messageCols+` FROM messages WHERE conv = ?`+cond+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteMessage removes a message and repoints both conversation rows.
func DeleteMessage(ctx ctxT, q Q, m *Message) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM messages WHERE id = ?`, m.ID); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `UPDATE conversations SET last_message_id =
		(SELECT id FROM messages WHERE conv = ?1 ORDER BY id DESC LIMIT 1)
		WHERE (user_id = ?2 AND peer_id = ?3) OR (user_id = ?3 AND peer_id = ?2)`, ConvKey(m.SenderID, m.RecipientID), m.SenderID, m.RecipientID)
	return err
}

// MarkRead marks peer's messages to me up to (and including) upTo as read.
// receipt controls whether the sender-visible receipt is set too.
func MarkRead(ctx ctxT, q Q, me, peer, upTo string, now int64, receipt bool) (int64, error) {
	var receiptAt any
	if receipt {
		receiptAt = now
	}
	res, err := q.ExecContext(ctx, `UPDATE messages SET read_at = ?, receipt_at = ?
		WHERE recipient_id = ? AND sender_id = ? AND read_at IS NULL AND id <= ?`, now, receiptAt, me, peer, upTo)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Conversation is one of my conversation index rows.
type Conversation struct {
	PeerID        string
	LastMessageID *string
	UpdatedAt     int64
}

// Conversations lists my conversations, most recently updated first.
func Conversations(ctx ctxT, q Q, me string, limit int) ([]Conversation, error) {
	rows, err := q.QueryContext(ctx, `SELECT peer_id, last_message_id, updated_at FROM conversations
		WHERE user_id = ? ORDER BY updated_at DESC, peer_id LIMIT ?`, me, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Conversation
	for rows.Next() {
		var c Conversation
		var last sql.NullString
		if err := rows.Scan(&c.PeerID, &last, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.LastMessageID = nstr(last)
		out = append(out, c)
	}
	return out, rows.Err()
}

// UnreadCounts returns, per sender, how many messages to me are unread.
func UnreadCounts(ctx ctxT, q Q, me string) (map[string]int, error) {
	rows, err := q.QueryContext(ctx, `SELECT sender_id, COUNT(*) FROM messages WHERE recipient_id = ? AND read_at IS NULL GROUP BY sender_id`, me)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// MessageReaction is a reaction on a chat message.
type MessageReaction struct {
	MessageID string
	UserID    string
	Emoji     string
}

// MessageReactionsFor returns reactions for the given messages.
func MessageReactionsFor(ctx ctxT, q Q, ids []string) (map[string][]MessageReaction, error) {
	out := map[string][]MessageReaction{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT message_id, user_id, emoji FROM message_reactions WHERE message_id IN (`+
		db.Placeholders(len(ids))+`) ORDER BY created_at, user_id`, db.Args(nil, ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r MessageReaction
		if err := rows.Scan(&r.MessageID, &r.UserID, &r.Emoji); err != nil {
			return nil, err
		}
		out[r.MessageID] = append(out[r.MessageID], r)
	}
	return out, rows.Err()
}

// SetMessageReaction upserts a reaction on a message.
func SetMessageReaction(ctx ctxT, q Q, messageID, userID, emoji string, now int64) error {
	_, err := q.ExecContext(ctx, `INSERT INTO message_reactions(message_id, user_id, emoji, created_at) VALUES (?,?,?,?)
		ON CONFLICT(message_id, user_id) DO UPDATE SET emoji = excluded.emoji, created_at = excluded.created_at`, messageID, userID, emoji, now)
	return err
}

// DeleteMessageReaction removes a reaction; reports whether one existed.
func DeleteMessageReaction(ctx ctxT, q Q, messageID, userID string) (bool, error) {
	res, err := q.ExecContext(ctx, `DELETE FROM message_reactions WHERE message_id = ? AND user_id = ?`, messageID, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
