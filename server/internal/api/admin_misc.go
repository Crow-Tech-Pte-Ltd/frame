package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ids"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

// ---- Settings -----------------------------------------------------------------

type settingView struct {
	Value   any    `json:"value"`
	Default any    `json:"default"`
	Locked  bool   `json:"locked"`
	Source  string `json:"source"` // default | panel | config
}

var errSettingLocked = apiErr(http.StatusConflict, "setting_locked", "This setting is set in frame.conf (or the environment) and can't be changed here")

func (s *Server) settingsView(ctx context.Context) (map[string]settingView, error) {
	stored, err := store.Settings(ctx, s.db.R)
	if err != nil {
		return nil, err
	}
	live := s.live()
	out := map[string]settingView{}
	for i := range settingDefs {
		d := &settingDefs[i]
		v := settingView{Value: d.get(live), Default: defaultSetting(d), Locked: s.locked(d), Source: "default"}
		if v.Locked {
			v.Source = "config"
		} else if _, ok := stored[d.Key]; ok {
			v.Source = "panel"
		}
		out[d.Key] = v
	}
	return out, nil
}

func (s *Server) handleAdminSettings(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	v, err := s.settingsView(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": v})
	return nil
}

func (s *Server) handleAdminPatchSettings(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	var req map[string]json.RawMessage
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if len(req) == 0 {
		return errBadRequest("Nothing to change")
	}
	type change struct {
		key   string
		value *string // nil = reset to default
	}
	var changes []change
	live := s.live()
	keys := make([]string, 0, len(req))
	for k := range req {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		d := settingDefFor(k)
		if d == nil {
			return errBadRequest("Unknown setting " + k)
		}
		raw := strings.TrimSpace(string(req[k]))
		if raw == "null" {
			if s.locked(d) {
				return errSettingLocked
			}
			changes = append(changes, change{key: k})
			continue
		}
		var v any
		if err := json.Unmarshal(req[k], &v); err != nil {
			return errBadRequest(k + ": invalid value")
		}
		canon, err := d.parse(v)
		if err != nil {
			return errBadRequest(k + ": " + err.Error())
		}
		if s.locked(d) {
			if canon == fmt.Sprint(d.get(live)) {
				continue // unchanged locked value: harmless
			}
			return errSettingLocked
		}
		changes = append(changes, change{key: k, value: &canon})
	}
	ctx := r.Context()
	now := s.now().UnixMilli()
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		for _, c := range changes {
			if err := store.SetSetting(ctx, tx, c.key, c.value, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := s.ReloadSettings(ctx); err != nil {
		return err
	}
	if len(changes) > 0 {
		var parts []string
		for _, c := range changes {
			if c.value == nil {
				parts = append(parts, c.key+" reset to default")
			} else {
				parts = append(parts, c.key+" = "+*c.value)
			}
		}
		s.audit(ctx, r, a.User, "settings.update", nil, strings.Join(parts, ", "))
	}
	return s.handleAdminSettings(w, r, a)
}

// ---- Overview ------------------------------------------------------------------

type relayHealth struct {
	checked  time.Time
	ok       bool
	inflight bool
}

const relayHealthTTL = 5 * time.Minute

// relayStatus returns the cached push relay health; it checks synchronously
// the first time and refreshes in the background when stale.
func (s *Server) relayStatus() (*bool, *int64) {
	if !s.push.Enabled() {
		return nil, nil
	}
	s.relayMu.Lock()
	st := s.relayState
	stale := st.checked.IsZero() || s.now().Sub(st.checked) > relayHealthTTL
	if stale && !st.inflight {
		s.relayState.inflight = true
		s.relayMu.Unlock()
		if st.checked.IsZero() {
			s.checkRelay()
		} else {
			go s.checkRelay()
		}
		s.relayMu.Lock()
		st = s.relayState
	}
	s.relayMu.Unlock()
	if st.checked.IsZero() {
		return nil, nil
	}
	ok, at := st.ok, st.checked.UnixMilli()
	return &ok, &at
}

func (s *Server) checkRelay() {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	ok := false
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.PushRelay+"/healthz", nil)
	if err == nil {
		if res, err := s.relayClient.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
			res.Body.Close()
			ok = res.StatusCode == http.StatusOK
		}
	}
	s.relayMu.Lock()
	s.relayState = relayHealth{checked: s.now(), ok: ok}
	s.relayMu.Unlock()
}

func fileSize(p string) int64 {
	if st, err := os.Stat(p); err == nil {
		return st.Size()
	}
	return 0
}

func (s *Server) handleAdminOverview(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	ctx := r.Context()
	now := s.now()
	o, err := store.AdminOverview(ctx, s.db.R, now.Add(-7*24*time.Hour).UnixMilli())
	if err != nil {
		return err
	}
	recent, _, err := store.AdminListUsers(ctx, s.db.R, store.UserQuery{Sort: "created_at", Desc: true, Limit: 5})
	if err != nil {
		return err
	}
	signups := make([]adminUserView, 0, len(recent))
	for _, u := range recent {
		signups = append(signups, adminUserOf(u))
	}
	actions, _, err := store.ListAudit(ctx, s.db.R, "", 8, 0)
	if err != nil {
		return err
	}
	openReports, err := store.CountOpenReports(ctx, s.db.R)
	if err != nil {
		return err
	}
	live := s.live()
	var publicURL, relayURL *string
	if s.cfg.PublicURL != "" {
		p := s.cfg.PublicURL
		publicURL = &p
	}
	if s.push.Enabled() {
		p := s.cfg.PushRelay
		relayURL = &p
	}
	reachable, checkedAt := s.relayStatus()
	writeJSON(w, http.StatusOK, map[string]any{
		"server": map[string]any{"name": live.Name, "version": s.version, "started_at": s.started.UnixMilli(),
			"registration": live.Registration, "user_invites": live.UserInvites, "storage": s.cfg.Storage,
			"public_url": publicURL, "admin_mode": s.cfg.Admin, "admin_listen": s.cfg.AdminListen != ""},
		"users":    map[string]any{"total": o.Users, "active_7d": o.Active7d, "disabled": o.Disabled, "admins": o.Admins},
		"content":  map[string]any{"moments": o.Moments, "messages": o.Messages},
		"reports":  map[string]any{"open": openReports},
		"storage":  map[string]any{"media_bytes": o.MediaBytes, "media_files": o.MediaFiles},
		"database": map[string]any{"bytes": fileSize(s.db.Path) + fileSize(s.db.Path+"-wal")},
		"push": map[string]any{"configured": s.push.Enabled(), "relay_url": relayURL, "reachable": reachable,
			"checked_at": checkedAt},
		"recent_signups": signups,
		"recent_actions": auditViews(actions),
	})
	return nil
}

// ---- Maintenance -----------------------------------------------------------------

type backupGrant struct {
	sessionID string
	expires   time.Time
}

const backupTokenTTL = 2 * time.Minute

func (s *Server) handleAdminBackupToken(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	var req passwordOnlyReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if err := s.stepUp(r.Context(), r, a.User, req.Password); err != nil {
		return err
	}
	tok := ids.Token(32)
	exp := s.now().Add(backupTokenTTL)
	s.backupMu.Lock()
	for k, g := range s.backupTokens {
		if s.now().After(g.expires) {
			delete(s.backupTokens, k)
		}
	}
	s.backupTokens[tok] = backupGrant{sessionID: a.Session.ID, expires: exp}
	s.backupMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"url": "/admin/api/v1/maintenance/backup/" + tok, "expires_at": exp.UnixMilli()})
	return nil
}

func (s *Server) handleAdminBackupDownload(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	tok := r.PathValue("token")
	s.backupMu.Lock()
	g, ok := s.backupTokens[tok]
	delete(s.backupTokens, tok) // single use
	s.backupMu.Unlock()
	if !ok || g.sessionID != a.Session.ID || s.now().After(g.expires) {
		return errNotFound("This download link has expired; start the backup again")
	}
	ctx := r.Context()
	tmp := filepath.Join(s.tmpDir, "backup-"+ids.Token(9)+".db")
	defer os.Remove(tmp)
	if err := s.db.Backup(ctx, tmp); err != nil {
		return err
	}
	f, err := os.Open(tmp)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Minute))
	name := "frame-backup-" + s.now().UTC().Format("20060102-150405") + ".db"
	h := w.Header()
	h.Set("Content-Type", "application/vnd.sqlite3")
	h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	h.Set("Content-Length", fmt.Sprint(st.Size()))
	s.audit(ctx, r, a.User, "maintenance.backup", nil, fmt.Sprintf("%d bytes", st.Size()))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
	return nil
}

func (s *Server) handleAdminCleanup(w http.ResponseWriter, r *http.Request, a *adminCtx) error {
	ctx := context.WithoutCancel(r.Context())
	n, err := s.media.Sweep(ctx, OrphanGrace)
	if err != nil {
		return err
	}
	m, err := store.DeleteExpiredInvites(ctx, s.db.W, s.now().UnixMilli())
	if err != nil {
		return err
	}
	if err := store.DeleteExpiredNonces(ctx, s.db.W, s.now().UnixMilli()); err != nil {
		return err
	}
	s.audit(ctx, r, a.User, "maintenance.cleanup", nil, fmt.Sprintf("%d orphaned media, %d expired invites removed", n, m))
	writeJSON(w, http.StatusOK, map[string]any{"orphan_media_removed": n, "expired_invites_removed": m})
	return nil
}
