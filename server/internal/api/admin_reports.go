package api

// Reports in the admin panel (ADMIN.md "Reports"). The list and the
// overview count carry metadata only. GET reports/{id} (and its media/thumb
// routes) is the one deliberate exception to "the admin API never returns
// content": a member chose to show that one item to the admin by reporting
// it, so the detail returns exactly the reported moment, message or profile
// and nothing around it.

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ids"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/media"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

// reportViewAuditEvery: repeated opens of the same report by the same
// admin within this window are logged once.
const reportViewAuditEvery = 30 * time.Minute

var (
	errReportNotFound = errNotFound("Report not found")
	errContentGone    = apiErr(http.StatusConflict, "content_gone", "The reported content no longer exists")
)

type reportUserView struct {
	ID          string  `json:"id"`
	Username    *string `json:"username"`
	DisplayName string  `json:"display_name"`
	HasAvatar   bool    `json:"has_avatar"`
	Suspended   bool    `json:"suspended"`
}

type adminReportView struct {
	ID            string          `json:"id"`
	TargetType    string          `json:"target_type"`
	ContentExists bool            `json:"content_exists"`
	Reason        string          `json:"reason"`
	Note          *string         `json:"note"`
	CreatedAt     int64           `json:"created_at"`
	Status        string          `json:"status"`
	Resolution    *string         `json:"resolution"`
	ResolvedAt    *int64          `json:"resolved_at"`
	ResolvedBy    *userMini       `json:"resolved_by"`
	Reporter      *reportUserView `json:"reporter"`
	ReportedUser  *reportUserView `json:"reported_user"`
}

func reportUserOf(u *store.User) *reportUserView {
	if u == nil {
		return nil
	}
	return &reportUserView{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, HasAvatar: u.AvatarMediaID != nil,
		Suspended: u.Disabled()}
}

// reportViews renders reports with their people (deleted accounts → null).
func (s *Server) reportViews(r *http.Request, reps []*store.Report) ([]adminReportView, error) {
	var uids []string
	for _, rep := range reps {
		for _, p := range []*string{rep.ReporterID, rep.OwnerID, rep.ResolvedBy} {
			if p != nil {
				uids = append(uids, *p)
			}
		}
	}
	users, err := store.GetUsers(r.Context(), s.db.R, uids)
	if err != nil {
		return nil, err
	}
	get := func(p *string) *store.User {
		if p == nil {
			return nil
		}
		return users[*p]
	}
	out := make([]adminReportView, 0, len(reps))
	for _, rep := range reps {
		out = append(out, adminReportView{ID: rep.ID, TargetType: rep.TargetType, ContentExists: rep.Exists, Reason: rep.Reason,
			Note: rep.Note, CreatedAt: rep.CreatedAt, Status: rep.Status, Resolution: rep.Resolution, ResolvedAt: rep.ResolvedAt,
			ResolvedBy: miniOf(get(rep.ResolvedBy)), Reporter: reportUserOf(get(rep.ReporterID)),
			ReportedUser: reportUserOf(get(rep.OwnerID))})
	}
	return out, nil
}

func (s *Server) handleAdminReports(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	page, per := pageParams(r, 25, 100)
	status := r.URL.Query().Get("status")
	switch status {
	case "", "all":
		status = ""
	case store.ReportOpen, store.ReportResolved:
	default:
		return errBadRequest("status must be open, resolved or all")
	}
	ctx := r.Context()
	reps, total, err := store.ListReports(ctx, s.db.R, status, per, (page-1)*per)
	if err != nil {
		return err
	}
	open, err := store.CountOpenReports(ctx, s.db.R)
	if err != nil {
		return err
	}
	views, err := s.reportViews(r, reps)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"reports": views, "total": total, "open": open, "page": page, "per_page": per})
	return nil
}

// adminReport loads the {id} report of an admin route.
func (s *Server) adminReport(r *http.Request) (*store.Report, error) {
	id := ids.Normalize(r.PathValue("id"))
	if id == "" {
		return nil, errReportNotFound
	}
	rep, err := store.GetReport(r.Context(), s.db.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errReportNotFound
	}
	return rep, err
}

// reportContent returns exactly the reported item, or nil once it is gone.
func (s *Server) reportContent(r *http.Request, rep *store.Report) (map[string]any, error) {
	ctx := r.Context()
	base := adminAPIPrefix + "reports/" + rep.ID
	switch rep.TargetType {
	case store.ReportMoment:
		m, err := store.GetMoment(ctx, s.db.R, rep.TargetID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		md, err := store.GetMedia(ctx, s.db.R, m.MediaID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		var caption any
		if m.Caption != nil {
			caption = map[string]any{"type": m.Caption.Type, "text": m.Caption.Text}
		}
		out := map[string]any{"type": rep.TargetType, "id": m.ID, "kind": m.Kind, "created_at": m.CreatedAt, "caption": caption,
			"media": nil, "thumb": map[string]any{"url": base + "/thumb"}}
		if md != nil {
			out["media"] = map[string]any{"url": base + "/media", "content_type": md.ContentType, "width": md.Width,
				"height": md.Height, "duration_ms": md.DurationMS}
		}
		return out, nil
	case store.ReportMessage:
		m, err := store.GetMessage(ctx, s.db.R, rep.TargetID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": rep.TargetType, "id": m.ID, "created_at": m.CreatedAt, "text": m.Text}, nil
	case store.ReportUser:
		u, err := store.GetUser(ctx, s.db.R, rep.TargetID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": rep.TargetType, "id": u.ID, "created_at": u.CreatedAt, "user": reportUserOf(u)}, nil
	}
	return nil, nil
}

// adminAPIPrefix is the admin API path prefix used in URLs the panel follows.
const adminAPIPrefix = "/admin/api/v1/"

func (s *Server) handleAdminReport(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	rep, err := s.adminReport(r)
	if err != nil {
		return err
	}
	views, err := s.reportViews(r, []*store.Report{rep})
	if err != nil {
		return err
	}
	content, err := s.reportContent(r, rep)
	if err != nil {
		return err
	}
	if content != nil && rep.TargetType != store.ReportUser {
		if err := s.auditReportView(r, a, rep); err != nil {
			return err
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": views[0], "content": content})
	return nil
}

// auditReportView records that the admin opened a reported moment or
// message (its detail or its media), once per admin and report per half
// hour, so the content exception stays visible in the audit log.
func (s *Server) auditReportView(r *http.Request, a *adminCtx, rep *store.Report) error {
	seen, err := store.AuditedSince(r.Context(), s.db.R, a.User.ID, "report.view", rep.ID,
		s.now().Add(-reportViewAuditEvery).UnixMilli())
	if err != nil {
		return err
	}
	if !seen {
		s.audit(r.Context(), r, a.User, "report.view", s.reportTarget(r, rep), "")
	}
	return nil
}

func (s *Server) handleAdminReportMedia(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	return s.serveReportMedia(w, r, a, false)
}

func (s *Server) handleAdminReportThumb(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	return s.serveReportMedia(w, r, a, true)
}

// serveReportMedia streams the reported moment's photo/video or thumbnail
// through the admin API (so it works on a separate FRAME_ADMIN_LISTEN
// address and under the panel's CSP, and never as a shareable URL). Like
// the detail, streaming it is audit-logged as report.view.
func (s *Server) serveReportMedia(w http.ResponseWriter, r *http.Request, a *adminCtx, thumb bool) error {
	rep, err := s.adminReport(r)
	if err != nil {
		return err
	}
	if rep.TargetType != store.ReportMoment {
		return errNotFound("This report has no media")
	}
	ctx := r.Context()
	m, err := store.GetMoment(ctx, s.db.R, rep.TargetID)
	if errors.Is(err, store.ErrNotFound) {
		return errContentGone
	}
	if err != nil {
		return err
	}
	id := m.MediaID
	if thumb {
		id = m.ThumbID
	}
	md, obj, err := s.media.Open(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return errContentGone
	}
	if err != nil {
		return err
	}
	defer obj.Close()
	if md.ContentType != media.JPEG && md.ContentType != media.HEIC && md.ContentType != media.MP4 {
		return errNotFound("No media")
	}
	if err := s.auditReportView(r, a, rep); err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", md.ContentType)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Accept-Ranges", "bytes")
	h.Set("Content-Disposition", "inline")
	singleRangeOnly(r)
	http.ServeContent(w, r, "", time.Time{}, obj)
	return nil
}

// reportTarget is the audit target for a report: "moment by alice".
func (s *Server) reportTarget(r *http.Request, rep *store.Report) *AuditTarget {
	label := rep.TargetType
	if rep.OwnerID != nil {
		if u, err := store.GetUser(r.Context(), s.db.R, *rep.OwnerID); err == nil {
			if rep.TargetType == store.ReportUser {
				label = "user " + userLabel(u)
			} else {
				label += " by " + userLabel(u)
			}
		}
	}
	return &AuditTarget{Type: "report", ID: rep.ID, Label: label}
}

func resolvedDetail(rep *store.Report, n int) string {
	d := "reason: " + rep.Reason
	if n > 1 {
		d += fmt.Sprintf("; resolved %d reports on the same %s", n, rep.TargetType)
	}
	return d
}

// handleAdminReportDelete deletes the reported moment or message (exactly
// as if its author had) and resolves every open report on it.
func (s *Server) handleAdminReportDelete(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	rep, err := s.adminReport(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	switch rep.TargetType {
	case store.ReportMoment:
		m, err := store.GetMoment(ctx, s.db.R, rep.TargetID)
		if errors.Is(err, store.ErrNotFound) {
			return errContentGone
		}
		if err != nil {
			return err
		}
		if err := s.deleteMoment(ctx, m); err != nil {
			if errors.Is(err, errMomentNotFound) {
				return errContentGone
			}
			return err
		}
	case store.ReportMessage:
		m, err := store.GetMessage(ctx, s.db.R, rep.TargetID)
		if errors.Is(err, store.ErrNotFound) {
			return errContentGone
		}
		if err != nil {
			return err
		}
		if err := s.deleteMessage(ctx, m); err != nil {
			return err
		}
	default:
		return errBadRequest("A user report has no content to delete; suspend the account instead")
	}
	return s.resolveReport(w, r, a, rep, store.ResolutionDeleted, "report.delete_content")
}

// handleAdminReportSuspend suspends the reported content's author (the
// same suspension as Users → Suspend) and resolves the report.
func (s *Server) handleAdminReportSuspend(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	rep, err := s.adminReport(r)
	if err != nil {
		return err
	}
	if rep.OwnerID == nil {
		return errUserNotFound
	}
	ctx := r.Context()
	u, err := store.GetUser(ctx, s.db.R, *rep.OwnerID)
	if errors.Is(err, store.ErrNotFound) {
		return errUserNotFound
	}
	if err != nil {
		return err
	}
	if u.ID == a.User.ID {
		return errSelfAction
	}
	if !u.Disabled() {
		if err := s.SuspendUser(ctx, u.ID, true); err != nil {
			return err
		}
		s.audit(ctx, r, a.User, "user.disable", UserTarget(u), "report "+rep.ID)
	}
	return s.resolveReport(w, r, a, rep, store.ResolutionSuspended, "report.suspend_user")
}

func (s *Server) handleAdminReportDismiss(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	rep, err := s.adminReport(r)
	if err != nil {
		return err
	}
	return s.resolveReport(w, r, a, rep, store.ResolutionDismissed, "report.dismiss")
}

func (s *Server) resolveReport(w http.ResponseWriter, r *http.Request, a *adminCtx, rep *store.Report, resolution, action string) error {
	ctx := r.Context()
	n, err := store.ResolveReports(ctx, s.db.W, rep, resolution, a.User.ID, s.now().UnixMilli())
	if err != nil {
		return err
	}
	s.audit(ctx, r, a.User, action, s.reportTarget(r, rep), resolvedDetail(rep, n))
	writeJSON(w, http.StatusOK, map[string]any{"resolved": n})
	return nil
}
