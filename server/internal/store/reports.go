package store

import (
	"database/sql"
	"errors"
)

// Report statuses, target types, reasons and resolutions (migration 0005).
const (
	ReportOpen     = "open"
	ReportResolved = "resolved"

	ReportMoment  = "moment"
	ReportMessage = "message"
	ReportUser    = "user"

	ResolutionDeleted   = "deleted_content"
	ResolutionSuspended = "suspended_user"
	ResolutionDismissed = "dismissed"
)

// ReportReasons is the closed set of reasons a member can pick.
var ReportReasons = map[string]bool{
	"spam": true, "harassment": true, "nudity": true, "violence": true, "self_harm": true, "other": true,
}

// Report is a row of the reports table.
type Report struct {
	ID         string
	ReporterID *string // nil once the reporter's account is deleted
	TargetType string
	TargetID   string
	OwnerID    *string // author / sender / reported user; nil once deleted
	Reason     string
	Note       *string
	CreatedAt  int64
	Status     string
	Resolution *string
	ResolvedBy *string
	ResolvedAt *int64
	// Exists reports whether the target still exists (list/get only).
	Exists bool
}

const reportCols = `r.id, r.reporter_id, r.target_type, r.target_id, r.owner_id, r.reason, r.note, r.created_at,
	r.status, r.resolution, r.resolved_by, r.resolved_at,
	CASE r.target_type
		WHEN 'moment' THEN EXISTS(SELECT 1 FROM moments WHERE id = r.target_id)
		WHEN 'message' THEN EXISTS(SELECT 1 FROM messages WHERE id = r.target_id)
		ELSE EXISTS(SELECT 1 FROM users WHERE id = r.target_id) END`

func scanReport(s scanner) (*Report, error) {
	var r Report
	var reporter, owner, note, resolution, by sql.NullString
	var at sql.NullInt64
	if err := s.Scan(&r.ID, &reporter, &r.TargetType, &r.TargetID, &owner, &r.Reason, &note, &r.CreatedAt,
		&r.Status, &resolution, &by, &at, &r.Exists); err != nil {
		return nil, err
	}
	r.ReporterID, r.OwnerID, r.Note, r.Resolution, r.ResolvedBy = nstr(reporter), nstr(owner), nstr(note), nstr(resolution), nstr(by)
	r.ResolvedAt = nint(at)
	return &r, nil
}

// InsertReport stores a new open report. A second open report by the same
// reporter on the same target is ignored (inserted == false).
func InsertReport(ctx ctxT, q Q, r *Report) (inserted bool, err error) {
	res, err := q.ExecContext(ctx, `INSERT INTO reports(id, reporter_id, target_type, target_id, owner_id, reason, note, created_at, status)
		VALUES (?,?,?,?,?,?,?,?,'open') ON CONFLICT DO NOTHING`,
		r.ID, r.ReporterID, r.TargetType, r.TargetID, r.OwnerID, r.Reason, r.Note, r.CreatedAt)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// GetReport loads one report.
func GetReport(ctx ctxT, q Q, id string) (*Report, error) {
	r, err := scanReport(q.QueryRowContext(ctx, `SELECT `+reportCols+` FROM reports r WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// ListReports returns a page of reports — open ones first, then newest
// first — and the total for the filter. status: "open", "resolved" or ""
// (all).
func ListReports(ctx ctxT, q Q, status string, limit, offset int) ([]*Report, int, error) {
	where, args := "", []any{}
	if status == ReportOpen || status == ReportResolved {
		where, args = ` WHERE r.status = ?`, append(args, status)
	}
	var total int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM reports r`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := q.QueryContext(ctx, `SELECT `+reportCols+` FROM reports r`+where+`
		ORDER BY CASE r.status WHEN 'open' THEN 0 ELSE 1 END, r.created_at DESC, r.id DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Report{}
	for rows.Next() {
		r, err := scanReport(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// CountOpenReports returns how many reports wait for the admin.
func CountOpenReports(ctx ctxT, q Q) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM reports WHERE status = 'open'`).Scan(&n)
	return n, err
}

// ResolveReports marks report id — and every other open report on the same
// target — as resolved with the given resolution. It returns how many rows
// changed.
func ResolveReports(ctx ctxT, q Q, r *Report, resolution, by string, now int64) (int, error) {
	res, err := q.ExecContext(ctx, `UPDATE reports SET status = 'resolved', resolution = ?, resolved_by = ?, resolved_at = ?
		WHERE id = ? OR (target_type = ? AND target_id = ? AND status = 'open')`,
		resolution, by, now, r.ID, r.TargetType, r.TargetID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
