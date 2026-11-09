package store

import (
	"database/sql"
	"errors"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/db"
)

const mediaCols = `id, owner_id, content_type, bytes, width, height, duration_ms, created_at`

func scanMedia(s scanner) (*Media, error) {
	var m Media
	var owner sql.NullString
	var dur sql.NullInt64
	if err := s.Scan(&m.ID, &owner, &m.ContentType, &m.Bytes, &m.Width, &m.Height, &dur, &m.CreatedAt); err != nil {
		return nil, err
	}
	m.OwnerID, m.DurationMS = nstr(owner), nint(dur)
	return &m, nil
}

// InsertMedia records a blob. Insert the row before writing the blob so a
// crash between the two leaves an unreferenced row the sweeper cleans up.
func InsertMedia(ctx ctxT, q Q, m *Media) error {
	_, err := q.ExecContext(ctx, `INSERT INTO media(`+mediaCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		m.ID, m.OwnerID, m.ContentType, m.Bytes, m.Width, m.Height, m.DurationMS, m.CreatedAt)
	return err
}

// GetMedia loads one media row.
func GetMedia(ctx ctxT, q Q, id string) (*Media, error) {
	m, err := scanMedia(q.QueryRowContext(ctx, `SELECT `+mediaCols+` FROM media WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// GetMediaMany loads media rows keyed by id.
func GetMediaMany(ctx ctxT, q Q, ids []string) (map[string]*Media, error) {
	out := make(map[string]*Media, len(ids))
	ids = dedupe(ids)
	for len(ids) > 0 {
		n := min(len(ids), 500)
		rows, err := q.QueryContext(ctx, `SELECT `+mediaCols+` FROM media WHERE id IN (`+db.Placeholders(n)+`)`, db.Args(nil, ids[:n])...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			m, err := scanMedia(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out[m.ID] = m
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		ids = ids[n:]
	}
	return out, nil
}

// DeleteMediaRows deletes media rows (only call after their blobs are gone
// or for rows that are unreferenced).
func DeleteMediaRows(ctx ctxT, q Q, ids []string) error {
	for len(ids) > 0 {
		n := min(len(ids), 500)
		if _, err := q.ExecContext(ctx, `DELETE FROM media WHERE id IN (`+db.Placeholders(n)+`)`, db.Args(nil, ids[:n])...); err != nil {
			return err
		}
		ids = ids[n:]
	}
	return nil
}

// OrphanMedia returns ids of media rows created before olderThan that no
// moment or avatar references.
func OrphanMedia(ctx ctxT, q Q, olderThan int64, limit int) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT m.id FROM media m WHERE m.created_at < ?
		AND NOT EXISTS (SELECT 1 FROM moments WHERE media_id = m.id)
		AND NOT EXISTS (SELECT 1 FROM moments WHERE thumb_id = m.id)
		AND NOT EXISTS (SELECT 1 FROM users WHERE avatar_media_id = m.id)
		LIMIT ?`, olderThan, limit)
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

// IsMediaReferenced reports whether a moment or avatar still uses the media.
func IsMediaReferenced(ctx ctxT, q Q, id string) (bool, error) {
	var ok bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM moments WHERE media_id = ?1 OR thumb_id = ?1)
		OR EXISTS(SELECT 1 FROM users WHERE avatar_media_id = ?1)`, id).Scan(&ok)
	return ok, err
}

// MediaOwnedBy lists all media ids owned by a user.
func MediaOwnedBy(ctx ctxT, q Q, userID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM media WHERE owner_id = ?`, userID)
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
