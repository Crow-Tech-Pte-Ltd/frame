package api

import (
	"context"
	"net/http"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ids"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

// AuditTarget is what an admin action was done to.
type AuditTarget struct {
	Type, ID, Label string
}

func userLabel(u *store.User) string {
	if u.Username != nil {
		return *u.Username
	}
	return u.DisplayName
}

// UserTarget makes an audit target for a user.
func UserTarget(u *store.User) *AuditTarget {
	return &AuditTarget{Type: "user", ID: u.ID, Label: userLabel(u)}
}

// audit records an admin action. It never fails the request; entries must
// never contain passwords, codes or tokens.
func (s *Server) audit(ctx context.Context, r *http.Request, actor *store.User, action string, target *AuditTarget, detail string) {
	e := &store.AuditEntry{ID: ids.At(s.now()), ActorLabel: "unknown", Action: action, IP: "-", CreatedAt: s.now().UnixMilli()}
	if actor != nil {
		id := actor.ID
		e.ActorID, e.ActorLabel = &id, userLabel(actor)
	}
	if r != nil {
		e.IP = clientIPFrom(r).String()
	}
	s.writeAudit(ctx, e, target, detail)
}

// AuditCLI records an action taken with the admin CLI.
func (s *Server) AuditCLI(ctx context.Context, action string, target *AuditTarget, detail string) {
	s.writeAudit(ctx, &store.AuditEntry{ID: ids.At(s.now()), ActorLabel: "CLI", Action: action, IP: "local",
		CreatedAt: s.now().UnixMilli()}, target, detail)
}

func (s *Server) writeAudit(ctx context.Context, e *store.AuditEntry, target *AuditTarget, detail string) {
	if target != nil {
		t, id, l := target.Type, target.ID, target.Label
		e.TargetType, e.TargetID, e.TargetLabel = &t, &id, &l
	}
	if detail != "" {
		e.Detail = &detail
	}
	if err := store.InsertAudit(context.WithoutCancel(ctx), s.db.W, e); err != nil {
		s.log.Warn("audit log write failed", "action", e.Action, "err", err)
	}
}

type auditActorView struct {
	ID    *string `json:"id"`
	Label string  `json:"label"`
}

type auditTargetView struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Label string `json:"label"`
}

type auditView struct {
	ID     string           `json:"id"`
	At     int64            `json:"at"`
	Actor  auditActorView   `json:"actor"`
	Action string           `json:"action"`
	Target *auditTargetView `json:"target"`
	Detail *string          `json:"detail"`
	IP     string           `json:"ip"`
}

func auditViews(es []store.AuditEntry) []auditView {
	out := make([]auditView, 0, len(es))
	for _, e := range es {
		v := auditView{ID: e.ID, At: e.CreatedAt, Actor: auditActorView{ID: e.ActorID, Label: e.ActorLabel}, Action: e.Action,
			Detail: e.Detail, IP: e.IP}
		if e.TargetType != nil {
			v.Target = &auditTargetView{Type: *e.TargetType}
			if e.TargetID != nil {
				v.Target.ID = *e.TargetID
			}
			if e.TargetLabel != nil {
				v.Target.Label = *e.TargetLabel
			}
		}
		out = append(out, v)
	}
	return out
}

func (s *Server) handleAdminAudit(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	page, per := pageParams(r, 50, 200)
	action := r.URL.Query().Get("action")
	if len(action) > 64 {
		action = ""
	}
	es, total, err := store.ListAudit(r.Context(), s.db.R, action, per, (page-1)*per)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": auditViews(es), "total": total, "page": page, "per_page": per})
	return nil
}
