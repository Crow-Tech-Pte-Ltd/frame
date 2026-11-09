package api

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/config"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

// liveSettings are the server settings that can change at runtime (admin
// panel → Settings). Handlers read them through s.live(); never cache them
// across requests.
type liveSettings struct {
	Registration   string // open | invite | closed
	UserInvites    bool
	Name           string
	MaxVideoMS     int64
	MaxUploadBytes int64
	MaxFriends     int // 0 = unlimited
}

// settingDef describes one panel-editable setting and its config key.
type settingDef struct {
	Key, Env string
	// parse validates a panel value (JSON-decoded) and returns its canonical
	// string form for the settings table.
	parse func(v any) (string, error)
	// apply stores a canonical value into l.
	apply func(l *liveSettings, v string)
	// get renders the effective value as JSON.
	get func(l *liveSettings) any
}

func parseIntSetting(lo, hi int) func(any) (string, error) {
	return func(v any) (string, error) {
		f, ok := v.(float64)
		if !ok || f != float64(int64(f)) || f < float64(lo) || f > float64(hi) {
			return "", fmt.Errorf("must be a whole number between %d and %d", lo, hi)
		}
		return strconv.FormatInt(int64(f), 10), nil
	}
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

var settingDefs = []settingDef{
	{Key: "registration", Env: "FRAME_REGISTRATION",
		parse: func(v any) (string, error) {
			s, _ := v.(string)
			switch s {
			case "open", "invite", "closed":
				return s, nil
			}
			return "", fmt.Errorf(`must be "open", "invite" or "closed"`)
		},
		apply: func(l *liveSettings, v string) { l.Registration = v },
		get:   func(l *liveSettings) any { return l.Registration }},
	{Key: "user_invites", Env: "FRAME_USER_INVITES",
		parse: func(v any) (string, error) {
			b, ok := v.(bool)
			if !ok {
				return "", fmt.Errorf("must be true or false")
			}
			return strconv.FormatBool(b), nil
		},
		apply: func(l *liveSettings, v string) { l.UserInvites = v == "true" },
		get:   func(l *liveSettings) any { return l.UserInvites }},
	{Key: "name", Env: "FRAME_NAME",
		parse: func(v any) (string, error) {
			s, _ := v.(string)
			s = strings.TrimSpace(strings.Map(func(r rune) rune {
				if r < 0x20 || r == 0x7f {
					return -1
				}
				return r
			}, s))
			if s == "" || len(s) > 64 {
				return "", fmt.Errorf("must be 1–64 bytes")
			}
			return s, nil
		},
		apply: func(l *liveSettings, v string) { l.Name = v },
		get:   func(l *liveSettings) any { return l.Name }},
	{Key: "max_video_seconds", Env: "FRAME_MAX_VIDEO_SECONDS", parse: parseIntSetting(1, 600),
		apply: func(l *liveSettings, v string) { l.MaxVideoMS = int64(atoi(v)) * 1000 },
		get:   func(l *liveSettings) any { return l.MaxVideoMS / 1000 }},
	{Key: "max_upload_mb", Env: "FRAME_MAX_UPLOAD_MB", parse: parseIntSetting(1, 1024),
		apply: func(l *liveSettings, v string) { l.MaxUploadBytes = int64(atoi(v)) << 20 },
		get:   func(l *liveSettings) any { return l.MaxUploadBytes >> 20 }},
	{Key: "max_friends", Env: "FRAME_MAX_FRIENDS", parse: parseIntSetting(0, 1_000_000),
		apply: func(l *liveSettings, v string) { l.MaxFriends = atoi(v) },
		get:   func(l *liveSettings) any { return l.MaxFriends }},
}

func settingDefFor(key string) *settingDef {
	for i := range settingDefs {
		if settingDefs[i].Key == key {
			return &settingDefs[i]
		}
	}
	return nil
}

// fromConfig returns the settings as configured (environment, file or
// defaults) — the base the panel's values are layered on.
func fromConfig(c *config.Config) liveSettings {
	return liveSettings{Registration: c.Registration, UserInvites: c.UserInvites, Name: c.Name,
		MaxVideoMS: c.MaxVideoMS, MaxUploadBytes: c.MaxUploadBytes, MaxFriends: c.MaxFriends}
}

// locked reports whether a setting was set in the environment/config file
// (those win and can't be changed in the panel).
func (s *Server) locked(d *settingDef) bool { return s.cfg.Explicit[d.Env] }

// live returns the current effective settings.
func (s *Server) live() *liveSettings { return s.settings.Load() }

// ReloadSettings recomputes the effective settings from the configuration
// and the panel's settings table. Invalid stored values are ignored.
func (s *Server) ReloadSettings(ctx context.Context) error {
	stored, err := store.Settings(ctx, s.db.R)
	if err != nil {
		return err
	}
	l := fromConfig(s.cfg)
	for i := range settingDefs {
		d := &settingDefs[i]
		v, ok := stored[d.Key]
		if !ok || s.locked(d) {
			continue
		}
		var raw any = v
		switch d.Key {
		case "user_invites":
			raw = v == "true"
			if v != "true" && v != "false" {
				continue
			}
		case "max_video_seconds", "max_upload_mb", "max_friends":
			n, err := strconv.Atoi(v)
			if err != nil {
				continue
			}
			raw = float64(n)
		}
		canon, err := d.parse(raw)
		if err != nil {
			s.log.Warn("ignoring invalid stored setting", "key", d.Key)
			continue
		}
		d.apply(&l, canon)
	}
	s.settings.Store(&l)
	return nil
}

// defaultSetting renders a setting's built-in default.
func defaultSetting(d *settingDef) any {
	var l liveSettings
	c, err := config.Load(config.Source{})
	if err != nil {
		return nil
	}
	l = fromConfig(c)
	return d.get(&l)
}
