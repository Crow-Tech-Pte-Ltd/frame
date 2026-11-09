// Package store contains every SQL query frame-server runs. Functions take a
// db.Querier so they compose inside transactions. All SQL is parameterised;
// the only string concatenation is for generated "?,?,?" placeholder lists
// and whitelisted column names.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/db"
)

// Q is the query interface used by all store functions.
type Q = db.Querier

var (
	ErrNotFound         = errors.New("store: not found")
	ErrUsernameTaken    = errors.New("store: username taken")
	ErrIdentifierTaken  = errors.New("store: identifier taken")
	ErrDuplicateClient  = errors.New("store: duplicate client_id")
	ErrAppleSubTaken    = errors.New("store: apple account already linked")
	ErrGoogleSubTaken   = errors.New("store: google account already linked")
	errUniqueConstraint = errors.New("store: unique constraint")
)

// User is a row of the users table.
type User struct {
	ID            string
	Username      *string
	DisplayName   string
	Email         *string
	Phone         *string
	PasswordHash  *string
	AppleSub      *string
	GoogleSub     *string
	AvatarMediaID *string
	Discoverable  bool
	Searchable    bool
	ReadReceipts  bool
	CreatedAt     int64
	UpdatedAt     int64
	// Admin panel (migration 0003).
	IsAdmin        bool
	DisabledAt     *int64  // suspended since (ms) or nil
	InvitedBy      *string // member whose invite link they registered with
	ServerInviteID *string // admin server invite they registered with
}

// Disabled reports whether the account is suspended.
func (u *User) Disabled() bool { return u.DisabledAt != nil }

// Media is a row of the media table.
type Media struct {
	ID          string
	OwnerID     *string
	ContentType string
	Bytes       int64
	Width       int
	Height      int
	DurationMS  *int64
	CreatedAt   int64
}

// Caption of a moment.
type Caption struct {
	Type       string
	Text       string
	Style      string
	Color      *string
	Background *string
	Data       json.RawMessage // compact JSON object or nil
}

// Moment is a row of the moments table.
type Moment struct {
	ID        string
	AuthorID  string
	ClientID  string
	Kind      string
	Audience  string
	MediaID   string
	ThumbID   string
	LocalDate string
	Caption   *Caption
	CreatedAt int64
}

// Message is a row of the messages table.
type Message struct {
	ID             string
	ClientID       string
	SenderID       string
	RecipientID    string
	Text           string
	MomentID       *string
	MomentAuthorID *string
	MomentKind     *string
	CreatedAt      int64
	ReadAt         *int64
	ReceiptAt      *int64
}

func nstr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	s := ns.String
	return &s
}

func nint(ni sql.NullInt64) *int64 {
	if !ni.Valid {
		return nil
	}
	v := ni.Int64
	return &v
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// isUnique reports whether err is a SQLite UNIQUE/PRIMARY KEY violation and,
// if so, which column ("users.email" etc.) it was about.
func isUnique(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	msg := err.Error()
	i := strings.Index(msg, "UNIQUE constraint failed: ")
	if i < 0 {
		if strings.Contains(msg, "PRIMARY KEY") || strings.Contains(msg, "constraint failed: PRIMARY") {
			return "", true
		}
		return "", false
	}
	col := msg[i+len("UNIQUE constraint failed: "):]
	if j := strings.LastIndex(col, " ("); j >= 0 && strings.HasSuffix(col, ")") { // modernc appends " (2067)"
		col = col[:j]
	}
	return strings.TrimSpace(col), true
}

// ConvKey returns the canonical conversation key for two users.
func ConvKey(a, b string) string {
	if a < b {
		return a + ":" + b
	}
	return b + ":" + a
}

// Meta reads a key from the meta table.
func Meta(ctx ctxT, q Q, key string) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// SetMetaIfAbsent stores key=value unless key already exists, returning the
// stored value.
func SetMetaIfAbsent(ctx ctxT, q Q, key, value string) (string, error) {
	if _, err := q.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING`, key, value); err != nil {
		return "", err
	}
	return Meta(ctx, q, key)
}

// SetMeta stores key=value, replacing any previous value.
func SetMeta(ctx ctxT, q Q, key, value string) error {
	_, err := q.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// DeleteMeta removes a key from the meta table.
func DeleteMeta(ctx ctxT, q Q, key string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM meta WHERE key = ?`, key)
	return err
}
