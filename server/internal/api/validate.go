package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ids"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

// captionIn is the Caption object as sent by clients.
type captionIn struct {
	Type       string          `json:"type"`
	Text       string          `json:"text"`
	Style      string          `json:"style"`
	Color      *string         `json:"color"`
	Background *string         `json:"background"`
	Data       json.RawMessage `json:"data"`
}

// momentMeta is the "meta" part of POST /api/v1/moments.
type momentMeta struct {
	ClientID     string     `json:"client_id"`
	Kind         string     `json:"kind"`
	Caption      *captionIn `json:"caption"`
	Audience     string     `json:"audience"`
	RecipientIDs []string   `json:"recipient_ids"`
	Width        int        `json:"width"`
	Height       int        `json:"height"`
	DurationMS   *int64     `json:"duration_ms"`
	LocalDate    string     `json:"local_date"`
}

var (
	identRE   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,15}$`)
	styleRE   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,23}$`)
	hexRGBRE  = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	clientIDS = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// validClientID accepts UUIDs (what clients send) and other short tokens.
func validClientID(s string) bool { return clientIDS.MatchString(s) }

const maxCaptionData = 1024

// parseCaption validates and normalises a caption.
func parseCaption(c *captionIn, maxLen int) (*store.Caption, error) {
	if c == nil {
		return nil, nil
	}
	out := &store.Caption{Type: c.Type, Style: c.Style}
	if out.Type == "" {
		out.Type = "text"
	}
	if !identRE.MatchString(out.Type) {
		return nil, errBadRequest("caption.type must be a short lowercase identifier")
	}
	if out.Style == "" {
		out.Style = "classic"
	}
	if !styleRE.MatchString(out.Style) {
		return nil, errBadRequest("caption.style must be a short lowercase identifier")
	}
	text := strings.TrimSpace(c.Text)
	n := utf8.RuneCountInString(text)
	if !utf8.ValidString(text) || n < 1 || n > maxLen {
		return nil, errBadRequest("caption.text must be 1–64 characters")
	}
	for _, r := range text {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return nil, errBadRequest("caption.text must be a single line")
		}
	}
	out.Text = text
	for _, p := range []struct {
		in  *string
		out **string
	}{{c.Color, &out.Color}, {c.Background, &out.Background}} {
		if p.in == nil {
			continue
		}
		if !hexRGBRE.MatchString(*p.in) {
			return nil, errBadRequest("caption colors must be #RRGGBB")
		}
		v := strings.ToUpper(*p.in)
		*p.out = &v
	}
	if d := bytes.TrimSpace(c.Data); len(d) > 0 && !bytes.Equal(d, []byte("null")) {
		if d[0] != '{' {
			return nil, errBadRequest("caption.data must be a JSON object")
		}
		var buf bytes.Buffer
		if err := json.Compact(&buf, d); err != nil {
			return nil, errBadRequest("caption.data must be a JSON object")
		}
		if buf.Len() > maxCaptionData {
			return nil, errBadRequest("caption.data must be at most 1 KiB")
		}
		out.Data = json.RawMessage(buf.Bytes())
	}
	return out, nil
}

// parseMomentMeta decodes and validates the meta JSON of a moment upload.
func parseMomentMeta(b []byte, maxVideoMS int64, now time.Time) (*momentMeta, *store.Caption, error) {
	var m momentMeta
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&m); err != nil {
		return nil, nil, errBadRequest("meta must be a JSON object")
	}
	if dec.More() {
		return nil, nil, errBadRequest("meta must be a single JSON object")
	}
	if !validClientID(m.ClientID) {
		return nil, nil, errBadRequest("meta.client_id must be a UUID")
	}
	switch m.Kind {
	case "photo":
		if m.DurationMS != nil {
			return nil, nil, errBadRequest("meta.duration_ms must be null for photos")
		}
	case "video":
		if m.DurationMS == nil || *m.DurationMS <= 0 {
			return nil, nil, errBadRequest("meta.duration_ms is required for videos")
		}
		// Small tolerance: encoders round up a few frames.
		if *m.DurationMS > maxVideoMS+500 {
			return nil, nil, errBadRequest("Video is longer than this server allows")
		}
	default:
		return nil, nil, errBadRequest(`meta.kind must be "photo" or "video"`)
	}
	if m.Width < 16 || m.Height < 16 || m.Width > 4096 || m.Height > 4096 {
		return nil, nil, errBadRequest("meta.width/height must be between 16 and 4096")
	}
	switch m.Audience {
	case "", "all":
		m.Audience = "all"
		m.RecipientIDs = nil
	case "self":
		m.RecipientIDs = nil
	case "selected":
		if len(m.RecipientIDs) == 0 {
			return nil, nil, errBadRequest("meta.recipient_ids must not be empty for audience \"selected\"")
		}
		if len(m.RecipientIDs) > 5000 {
			return nil, nil, errBadRequest("Too many recipients")
		}
		for i, id := range m.RecipientIDs {
			n := ids.Normalize(id)
			if n == "" {
				return nil, nil, errBadRequest("meta.recipient_ids contains an invalid id")
			}
			m.RecipientIDs[i] = n
		}
	default:
		return nil, nil, errBadRequest(`meta.audience must be "all", "selected" or "self"`)
	}
	if m.LocalDate == "" {
		m.LocalDate = now.UTC().Format(time.DateOnly)
	} else {
		d, err := time.Parse(time.DateOnly, m.LocalDate)
		if err != nil || d.Year() < 2000 || d.After(now.Add(48*time.Hour)) {
			return nil, nil, errBadRequest("meta.local_date must be YYYY-MM-DD")
		}
	}
	cap, err := parseCaption(m.Caption, MaxCaptionLength)
	if err != nil {
		return nil, nil, err
	}
	return &m, cap, nil
}

// validEmoji checks a reaction: 1–16 bytes of UTF-8 forming a single
// grapheme cluster (base + modifiers/variation selectors/ZWJ sequences, a
// flag pair, or a keycap). It is a conservative approximation of UAX #29 for
// emoji, which is all reactions need.
func validEmoji(s string) bool {
	if len(s) == 0 || len(s) > 16 || !utf8.ValidString(s) {
		return false
	}
	rs := []rune(s)
	isRI := func(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }
	isExt := func(r rune) bool {
		return r == 0xFE0F || r == 0xFE0E || r == 0x20E3 ||
			(r >= 0x1F3FB && r <= 0x1F3FF) || // skin tones
			(r >= 0xE0020 && r <= 0xE007F) || // tags (subdivision flags)
			unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r)
	}
	isBase := func(r rune) bool {
		return !unicode.IsControl(r) && !unicode.IsSpace(r) && !isExt(r) && r != 0x200D &&
			!unicode.Is(unicode.Cf, r) && !unicode.Is(unicode.Cs, r) && !unicode.Is(unicode.Co, r)
	}
	if isRI(rs[0]) {
		return len(rs) == 2 && isRI(rs[1])
	}
	if !isBase(rs[0]) {
		return false
	}
	for i := 1; i < len(rs); i++ {
		r := rs[i]
		switch {
		case isExt(r):
		case r == 0x200D:
			if i+1 >= len(rs) || !isBase(rs[i+1]) || isRI(rs[i+1]) {
				return false
			}
			i++
		default:
			return false
		}
	}
	return true
}

// cursor encoding: opaque base64url of "v1:" + id.

func encodeCursor(id string) *string {
	c := base64.RawURLEncoding.EncodeToString([]byte("v1:" + id))
	return &c
}

var errBadCursor = errors.New("bad cursor")

func decodeCursor(c string) (string, error) {
	if c == "" {
		return "", nil
	}
	if len(c) > 64 {
		return "", errBadCursor
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil || !bytes.HasPrefix(b, []byte("v1:")) {
		return "", errBadCursor
	}
	id := ids.Normalize(string(b[3:]))
	if id == "" {
		return "", errBadCursor
	}
	return id, nil
}
