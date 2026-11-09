package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/db"
)

const momentCols = `id, author_id, client_id, kind, audience, media_id, thumb_id, local_date,
	caption_type, caption_text, caption_style, caption_color, caption_background, caption_data, created_at`

func scanMoment(s scanner) (*Moment, error) {
	var m Moment
	var ctype, ctext, cstyle, ccolor, cbg, cdata sql.NullString
	if err := s.Scan(&m.ID, &m.AuthorID, &m.ClientID, &m.Kind, &m.Audience, &m.MediaID, &m.ThumbID, &m.LocalDate,
		&ctype, &ctext, &cstyle, &ccolor, &cbg, &cdata, &m.CreatedAt); err != nil {
		return nil, err
	}
	if ctype.Valid {
		c := &Caption{Type: ctype.String, Text: ctext.String, Style: cstyle.String, Color: nstr(ccolor), Background: nstr(cbg)}
		if cdata.Valid {
			c.Data = json.RawMessage(cdata.String)
		}
		m.Caption = c
	}
	return &m, nil
}

// InsertMoment stores a moment plus feed entries for the author (already
// viewed) and every recipient. ErrDuplicateClient if client_id was used.
func InsertMoment(ctx ctxT, q Q, m *Moment, recipients []string) error {
	var ctype, ctext, cstyle, ccolor, cbg, cdata any
	if c := m.Caption; c != nil {
		ctype, ctext, cstyle, ccolor, cbg = c.Type, c.Text, c.Style, c.Color, c.Background
		if len(c.Data) > 0 {
			cdata = string(c.Data)
		}
	}
	_, err := q.ExecContext(ctx, `INSERT INTO moments(`+momentCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.AuthorID, m.ClientID, m.Kind, m.Audience, m.MediaID, m.ThumbID, m.LocalDate,
		ctype, ctext, cstyle, ccolor, cbg, cdata, m.CreatedAt)
	if err != nil {
		if col, ok := isUnique(err); ok && strings.Contains(col, "client_id") {
			return ErrDuplicateClient
		}
		return err
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO feed_entries(user_id, moment_id, author_id, viewed_at) VALUES (?,?,?,?)`,
		m.AuthorID, m.ID, m.AuthorID, m.CreatedAt); err != nil {
		return err
	}
	for _, r := range dedupe(recipients) {
		if r == m.AuthorID {
			continue
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO feed_entries(user_id, moment_id, author_id) VALUES (?,?,?)`, r, m.ID, m.AuthorID); err != nil {
			return err
		}
	}
	return nil
}

// GetMoment loads a moment by id.
func GetMoment(ctx ctxT, q Q, id string) (*Moment, error) {
	m, err := scanMoment(q.QueryRowContext(ctx, `SELECT `+momentCols+` FROM moments WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// MomentByClientID finds an author's moment by its idempotency key.
func MomentByClientID(ctx ctxT, q Q, authorID, clientID string) (*Moment, error) {
	m, err := scanMoment(q.QueryRowContext(ctx, `SELECT `+momentCols+` FROM moments WHERE author_id = ? AND client_id = ?`, authorID, clientID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// GetMoments loads moments keyed by id.
func GetMoments(ctx ctxT, q Q, ids []string) (map[string]*Moment, error) {
	out := make(map[string]*Moment, len(ids))
	ids = dedupe(ids)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT `+momentCols+` FROM moments WHERE id IN (`+placeholders(len(ids))+`)`, db.Args(nil, ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		m, err := scanMoment(rows)
		if err != nil {
			return nil, err
		}
		out[m.ID] = m
	}
	return out, rows.Err()
}

// visibleSQL is true when ?me may see moment m: own moment, or a feed entry
// exists and me is currently friends with the author.
const visibleSQL = `(m.author_id = ?2 OR EXISTS (SELECT 1 FROM feed_entries f
	JOIN friendships fr ON fr.user_id = f.user_id AND fr.friend_id = m.author_id
	WHERE f.user_id = ?2 AND f.moment_id = m.id))`

// VisibleMoment returns the moment if me may see it, else ErrNotFound.
func VisibleMoment(ctx ctxT, q Q, me, id string) (*Moment, error) {
	m, err := scanMoment(q.QueryRowContext(ctx, `SELECT `+momentCols+` FROM moments m WHERE m.id = ?1 AND `+visibleSQL, id, me))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// VisibleTo reports whether user may see moment id.
func VisibleTo(ctx ctxT, q Q, user, id string) (bool, error) {
	var ok bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM moments m WHERE m.id = ?1 AND `+visibleSQL+`)`, id, user).Scan(&ok)
	return ok, err
}

// FeedQuery selects a page of a user's feed.
type FeedQuery struct {
	Me         string
	AuthorID   string // "" = everyone visible
	ExcludeOwn bool
	BeforeID   string // exclusive upper bound ("" = none)
	FromID     string // inclusive lower bound
	ToID       string // exclusive upper bound
	Limit      int
}

func (f FeedQuery) where() (string, []any) {
	var sb strings.Builder
	args := []any{f.Me}
	sb.WriteString(`f.user_id = ?1 AND f.hidden = 0`)
	if f.AuthorID != "" {
		args = append(args, f.AuthorID)
		sb.WriteString(` AND f.author_id = ?2`)
	}
	add := func(cond, v string) {
		args = append(args, v)
		sb.WriteString(" AND " + cond + " ?" + itoa(len(args)))
	}
	if f.ExcludeOwn {
		sb.WriteString(` AND f.author_id != ?1`)
	}
	if f.BeforeID != "" {
		add("f.moment_id <", f.BeforeID)
	}
	if f.FromID != "" {
		add("f.moment_id >=", f.FromID)
	}
	if f.ToID != "" {
		add("f.moment_id <", f.ToID)
	}
	sb.WriteString(` AND (f.author_id = ?1 OR EXISTS (SELECT 1 FROM friendships fr WHERE fr.user_id = ?1 AND fr.friend_id = f.author_id))`)
	return sb.String(), args
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// FeedIDs returns moment ids for a feed page, newest first.
func FeedIDs(ctx ctxT, q Q, f FeedQuery) ([]string, error) {
	where, args := f.where()
	args = append(args, f.Limit)
	rows, err := q.QueryContext(ctx, `SELECT f.moment_id FROM feed_entries f WHERE `+where+
		` ORDER BY f.moment_id DESC LIMIT ?`+itoa(len(args)), args...)
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

// UnseenCount counts visible, unhidden, unviewed friend moments (capped).
func UnseenCount(ctx ctxT, q Q, me, authorID string, capN int) (int, error) {
	args := []any{me, capN}
	cond := ""
	if authorID != "" {
		args = append(args, authorID)
		cond = ` AND f.author_id = ?3`
	}
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM feed_entries f
		WHERE f.user_id = ?1 AND f.viewed_at IS NULL AND f.hidden = 0 AND f.author_id != ?1`+cond+`
		  AND EXISTS (SELECT 1 FROM friendships fr WHERE fr.user_id = ?1 AND fr.friend_id = f.author_id)
		LIMIT ?2)`, args...).Scan(&n)
	return n, err
}

// Reaction to a moment.
type Reaction struct {
	MomentID  string
	UserID    string
	Emoji     string
	CreatedAt int64
}

// ReactionsFor returns reactions on the given moments, oldest first. If
// onlyUser is set, only that user's reactions are returned.
func ReactionsFor(ctx ctxT, q Q, momentIDs []string, onlyUser string) (map[string][]Reaction, error) {
	out := map[string][]Reaction{}
	if len(momentIDs) == 0 {
		return out, nil
	}
	query := `SELECT moment_id, user_id, emoji, created_at FROM reactions WHERE moment_id IN (` + placeholders(len(momentIDs)) + `)`
	args := db.Args(nil, momentIDs)
	if onlyUser != "" {
		query += ` AND user_id = ?`
		args = append(args, onlyUser)
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY created_at, user_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Reaction
		if err := rows.Scan(&r.MomentID, &r.UserID, &r.Emoji, &r.CreatedAt); err != nil {
			return nil, err
		}
		out[r.MomentID] = append(out[r.MomentID], r)
	}
	return out, rows.Err()
}

// Audience is a feed entry of a moment other than the author's own.
type Audience struct {
	MomentID string
	UserID   string
	ViewedAt *int64
}

// AudienceFor returns the recipients of the given moments with view times.
func AudienceFor(ctx ctxT, q Q, momentIDs []string) (map[string][]Audience, error) {
	out := map[string][]Audience{}
	if len(momentIDs) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT moment_id, user_id, viewed_at FROM feed_entries
		WHERE moment_id IN (`+placeholders(len(momentIDs))+`) AND user_id != author_id ORDER BY viewed_at, user_id`, db.Args(nil, momentIDs)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Audience
		var v sql.NullInt64
		if err := rows.Scan(&a.MomentID, &a.UserID, &v); err != nil {
			return nil, err
		}
		a.ViewedAt = nint(v)
		out[a.MomentID] = append(out[a.MomentID], a)
	}
	return out, rows.Err()
}

// ViewedSet reports which of the moments me has viewed.
func ViewedSet(ctx ctxT, q Q, me string, momentIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(momentIDs) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT moment_id FROM feed_entries WHERE user_id = ? AND viewed_at IS NOT NULL AND moment_id IN (`+
		placeholders(len(momentIDs))+`)`, db.Args([]any{me}, momentIDs)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// RecipientIDs returns the non-author audience of a moment.
func RecipientIDs(ctx ctxT, q Q, momentID string) ([]string, error) {
	aud, err := AudienceFor(ctx, q, []string{momentID})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(aud[momentID]))
	for _, a := range aud[momentID] {
		ids = append(ids, a.UserID)
	}
	return ids, nil
}

// NewView is a first-time view recorded by MarkViewed.
type NewView struct {
	MomentID string
	AuthorID string
}

// MarkViewed records first views of visible, non-own moments.
func MarkViewed(ctx ctxT, q Q, me string, momentIDs []string, now int64) ([]NewView, error) {
	var out []NewView
	for _, id := range dedupe(momentIDs) {
		var author string
		err := q.QueryRowContext(ctx, `UPDATE feed_entries SET viewed_at = ?1
			WHERE user_id = ?2 AND moment_id = ?3 AND viewed_at IS NULL AND author_id != ?2
			  AND EXISTS (SELECT 1 FROM friendships fr WHERE fr.user_id = ?2 AND fr.friend_id = feed_entries.author_id)
			RETURNING author_id`, now, me, id).Scan(&author)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, NewView{MomentID: id, AuthorID: author})
	}
	return out, nil
}

// HideMoment hides a moment from my feed ("delete for me").
func HideMoment(ctx ctxT, q Q, me, momentID string) error {
	_, err := q.ExecContext(ctx, `UPDATE feed_entries SET hidden = 1 WHERE user_id = ? AND moment_id = ? AND author_id != user_id`, me, momentID)
	return err
}

// SetReaction upserts my reaction on a moment.
func SetReaction(ctx ctxT, q Q, momentID, userID, emoji string, now int64) error {
	_, err := q.ExecContext(ctx, `INSERT INTO reactions(moment_id, user_id, emoji, created_at) VALUES (?,?,?,?)
		ON CONFLICT(moment_id, user_id) DO UPDATE SET emoji = excluded.emoji, created_at = excluded.created_at`, momentID, userID, emoji, now)
	return err
}

// DeleteReaction removes my reaction; reports whether one existed.
func DeleteReaction(ctx ctxT, q Q, momentID, userID string) (bool, error) {
	res, err := q.ExecContext(ctx, `DELETE FROM reactions WHERE moment_id = ? AND user_id = ?`, momentID, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteMoment deletes a moment (feed entries and reactions cascade) and
// returns its media ids, which become orphans for the caller to delete.
func DeleteMoment(ctx ctxT, q Q, id string) (mediaIDs []string, err error) {
	var media, thumb string
	err = q.QueryRowContext(ctx, `DELETE FROM moments WHERE id = ? RETURNING media_id, thumb_id`, id).Scan(&media, &thumb)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return []string{media, thumb}, nil
}

// Streak computes the posting streak: consecutive local dates with at least
// one own moment, ending at the most recent posting day.
func Streak(ctx ctxT, q Q, userID string) (count int, last string, err error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT local_date FROM moments WHERE author_id = ? ORDER BY local_date DESC LIMIT 4000`, userID)
	if err != nil {
		return 0, "", err
	}
	defer rows.Close()
	var prev time.Time
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return 0, "", err
		}
		d, perr := time.Parse(time.DateOnly, s)
		if perr != nil {
			continue
		}
		if count == 0 {
			count, last, prev = 1, s, d
			continue
		}
		if prev.AddDate(0, 0, -1).Equal(d) {
			count++
			prev = d
			continue
		}
		break
	}
	return count, last, rows.Err()
}
