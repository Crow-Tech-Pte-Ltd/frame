package api

// POST /api/v1/reports (API.md "Reports", v1.3): a member flags a moment,
// message or user they can see for the server's admin. Nothing is scanned
// automatically; the admin reviews reports in the panel (admin_reports.go).

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ids"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

// MaxReportNote is the longest report note, in Unicode scalars.
const MaxReportNote = 500

type reportTargetIn struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type reportReq struct {
	Target *reportTargetIn `json:"target"`
	Reason string          `json:"reason"`
	Note   *string         `json:"note"`
	Block  bool            `json:"block"`
}

// parseReportNote trims the note; blank means none. Newlines and tabs are
// allowed, other control characters are not.
func parseReportNote(p *string) (*string, error) {
	if p == nil {
		return nil, nil
	}
	t := strings.TrimSpace(*p)
	if t == "" {
		return nil, nil
	}
	if !utf8.ValidString(t) || utf8.RuneCountInString(t) > MaxReportNote {
		return nil, errBadRequest("note must be at most 500 characters")
	}
	for _, r := range t {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return nil, errBadRequest("note contains invalid characters")
		}
	}
	return &t, nil
}

// reportOwner checks that me may see the target and returns whose it is
// (moment author, message sender, or the user). Anything me can't see is
// the same 404 the rest of the API gives, so reports reveal nothing.
func (s *Server) reportOwner(r *http.Request, me string, t *reportTargetIn) (string, error) {
	ctx := r.Context()
	id := ids.Normalize(t.ID)
	switch t.Type {
	case store.ReportMoment:
		if id == "" {
			return "", errMomentNotFound
		}
		m, err := store.VisibleMoment(ctx, s.db.R, me, id)
		if errors.Is(err, store.ErrNotFound) {
			return "", errMomentNotFound
		}
		if err != nil {
			return "", err
		}
		if m.AuthorID == me {
			return "", errBadRequest("You can't report your own moment")
		}
		return m.AuthorID, nil
	case store.ReportMessage:
		if id == "" {
			return "", errMessageNotFound
		}
		m, err := store.GetMessage(ctx, s.db.R, id)
		if errors.Is(err, store.ErrNotFound) {
			return "", errMessageNotFound
		}
		if err != nil {
			return "", err
		}
		if m.SenderID != me && m.RecipientID != me {
			return "", errMessageNotFound
		}
		if m.SenderID == me {
			return "", errBadRequest("You can't report your own message")
		}
		return m.SenderID, nil
	case store.ReportUser:
		u, _, err := s.visibleUser(r, me, t.ID)
		if err != nil {
			return "", err
		}
		if u.ID == me {
			return "", errBadRequest("You can't report yourself")
		}
		return u.ID, nil
	}
	return "", errBadRequest("target.type must be moment, message or user")
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	me := a.ID()
	if err := s.limit(s.rl.reports, me); err != nil {
		return err
	}
	var req reportReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Target == nil {
		return errBadRequest("target is required")
	}
	if !store.ReportReasons[req.Reason] {
		return errBadRequest("reason must be one of spam, harassment, nudity, violence, self_harm, other")
	}
	note, err := parseReportNote(req.Note)
	if err != nil {
		return err
	}
	owner, err := s.reportOwner(r, me, req.Target)
	if err != nil {
		return err
	}
	ctx := r.Context()
	now := s.now()
	rep := &store.Report{ID: ids.At(now), ReporterID: &me, TargetType: req.Target.Type,
		TargetID: ids.Normalize(req.Target.ID), OwnerID: &owner, Reason: req.Reason, Note: note, CreatedAt: now.UnixMilli()}
	var wereFriends bool
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := store.InsertReport(ctx, tx, rep); err != nil {
			return err
		}
		// A reported moment leaves the reporter's feed and widget, like
		// "Delete for me".
		if rep.TargetType == store.ReportMoment {
			if err := store.HideMoment(ctx, tx, me, rep.TargetID); err != nil {
				return err
			}
		}
		if req.Block {
			var err error
			wereFriends, err = s.blockTx(ctx, tx, me, owner)
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	if req.Block {
		s.afterBlock(me, owner, wereFriends)
	}
	s.log.Info("report received", "report_id", rep.ID, "type", rep.TargetType, "reason", rep.Reason)
	return noContent(w)
}
