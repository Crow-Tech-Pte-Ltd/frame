// Package devseed populates a dev server with demo users, friendships,
// moments (generated colourful JPEGs + a short MP4), reactions and chats.
// It drives the real HTTP handlers in-process, so seeded data is exactly
// what clients would have produced. Seeding is idempotent.
package devseed

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/db"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

//go:embed assets/demo.mp4
var demoMP4 []byte

// Password is the demo password for every seeded user.
const Password = "password123"

// Users are the seeded demo accounts.
var Users = []struct {
	Username, Name, Email, Phone string
	Hue                          float64
}{
	{"alice", "Alice", "alice@example.com", "+15550000001", 340},
	{"bob", "Bob", "bob@example.com", "+15550000002", 200},
	{"carol", "Carol", "carol@example.com", "+15550000003", 45},
}

// Clock is a controllable clock for backdating seeded content.
type Clock struct {
	mu     sync.Mutex
	offset time.Duration
}

// Now returns the (possibly shifted) current time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

func (c *Clock) set(d time.Duration) { c.mu.Lock(); c.offset = d; c.mu.Unlock() }

const seedMarker = "devseed_v1"

type seeder struct {
	h      http.Handler
	clock  *Clock
	tokens map[string]string // username → token
	ids    map[string]string // username → user id
}

// Seed creates the demo data unless it already exists.
func Seed(ctx context.Context, d *db.DB, h http.Handler, clock *Clock) error {
	if _, err := store.Meta(ctx, d.R, seedMarker); err == nil {
		return ensureAdmin(ctx, d)
	}
	s := &seeder{h: h, clock: clock, tokens: map[string]string{}, ids: map[string]string{}}
	defer clock.set(0)

	clock.set(-50 * time.Hour)
	for _, u := range Users {
		if err := s.ensureUser(ctx, d, u.Username, u.Name, u.Email, u.Phone, u.Hue); err != nil {
			return fmt.Errorf("seed user %s: %w", u.Username, err)
		}
	}
	for _, pair := range [][2]string{{"alice", "bob"}, {"alice", "carol"}, {"bob", "carol"}} {
		if err := s.befriend(pair[0], pair[1]); err != nil {
			return fmt.Errorf("seed friends: %w", err)
		}
	}

	type seedMoment struct {
		author   string
		hoursAgo float64
		pattern  int
		caption  map[string]any
		video    bool
		aud      string
		to       []string
	}
	moments := []seedMoment{
		{author: "carol", hoursAgo: 47, pattern: 0},
		{author: "bob", hoursAgo: 44, pattern: 1, caption: map[string]any{"type": "text", "text": "morning swim 🌊", "style": "classic"}},
		{author: "alice", hoursAgo: 40, pattern: 2},
		{author: "carol", hoursAgo: 30, pattern: 3, caption: map[string]any{"type": "location", "text": "Café Central", "style": "glass", "data": map[string]any{"name": "Café Central"}}},
		{author: "alice", hoursAgo: 26, pattern: 4, caption: map[string]any{"type": "text", "text": "new plant!", "style": "bold"}},
		{author: "bob", hoursAgo: 20, pattern: 5, video: true},
		{author: "carol", hoursAgo: 14, pattern: 6},
		{author: "alice", hoursAgo: 9, pattern: 7, aud: "selected", to: []string{"bob"}, caption: map[string]any{"type": "text", "text": "just for you", "style": "color", "color": "#FFFFFF", "background": "#E0245E"}},
		{author: "bob", hoursAgo: 6, pattern: 8},
		{author: "carol", hoursAgo: 3, pattern: 9, caption: map[string]any{"type": "text", "text": "golden hour", "style": "script"}},
		{author: "bob", hoursAgo: 1.5, pattern: 10},
		{author: "alice", hoursAgo: 0.5, pattern: 11, caption: map[string]any{"type": "text", "text": "sunset 🌅", "style": "classic"}},
	}
	momentIDs := make([]string, len(moments))
	for i, m := range moments {
		clock.set(-time.Duration(m.hoursAgo * float64(time.Hour)))
		var to []string
		for _, u := range m.to {
			to = append(to, s.ids[u])
		}
		id, err := s.postMoment(m.author, fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), m.pattern, m.caption, m.video, m.aud, to)
		if err != nil {
			return fmt.Errorf("seed moment %d: %w", i, err)
		}
		momentIDs[i] = id
		// Some friends see and react shortly after.
		clock.set(-time.Duration((m.hoursAgo - 0.2) * float64(time.Hour)))
		for _, viewer := range []string{"alice", "bob", "carol"} {
			if viewer == m.author || (m.aud == "selected" && viewer != "bob") || i == len(moments)-1 {
				continue
			}
			if _, err := s.do("POST", "/api/v1/moments/views", viewer, map[string]any{"ids": []string{id}}, 204); err != nil {
				return err
			}
		}
	}
	reactions := []struct {
		user   string
		moment int
		emoji  string
	}{{"bob", 2, "😍"}, {"carol", 2, "🔥"}, {"alice", 1, "😂"}, {"alice", 3, "💛"}, {"bob", 4, "🔥"}, {"carol", 5, "😂"}, {"alice", 9, "😍"}, {"bob", 9, "💛"}}
	for _, r := range reactions {
		if _, err := s.do("PUT", "/api/v1/moments/"+momentIDs[r.moment]+"/reaction", r.user, map[string]any{"emoji": r.emoji}, 204); err != nil {
			return err
		}
	}

	msgs := []struct {
		from, to string
		minsAgo  float64
		text     string
		reply    int // index into momentIDs, -1 = none
	}{
		{"bob", "alice", 25 * 60, "that plant is huge 🌱", 4},
		{"alice", "bob", 24*60 + 50, "right?? it was on sale", -1},
		{"bob", "alice", 24*60 + 45, "we should go plant shopping this weekend", -1},
		{"alice", "bob", 24*60 + 40, "yes! saturday?", -1},
		{"bob", "alice", 8 * 60, "aww thanks ❤️", 7},
		{"carol", "alice", 20, "where was that sunset?", 11},
	}
	var lastAliceBob string
	for i, m := range msgs {
		clock.set(-time.Duration(m.minsAgo * float64(time.Minute)))
		body := map[string]any{"client_id": fmt.Sprintf("00000000-0000-4000-9000-%012d", i+1), "text": m.text, "reply_to_moment_id": nil}
		if m.reply >= 0 {
			body["reply_to_moment_id"] = momentIDs[m.reply]
		}
		res, err := s.do("POST", "/api/v1/conversations/"+s.ids[m.to]+"/messages", m.from, body, 201, 200)
		if err != nil {
			return fmt.Errorf("seed message %d: %w", i, err)
		}
		var msg struct{ ID string }
		_ = json.Unmarshal(res, &msg)
		if m.from == "bob" && m.to == "alice" {
			lastAliceBob = msg.ID
		}
		if i == 1 {
			if _, err := s.do("PUT", "/api/v1/messages/"+msg.ID+"/reaction", "bob", map[string]any{"emoji": "🤣"}, 204); err != nil {
				return err
			}
		}
	}
	clock.set(-time.Duration(7 * float64(time.Hour)))
	if lastAliceBob != "" {
		if _, err := s.do("POST", "/api/v1/conversations/"+s.ids["bob"]+"/read", "alice", map[string]any{"up_to": lastAliceBob}, 204); err != nil {
			return err
		}
	}
	clock.set(0)
	if _, err := store.SetMetaIfAbsent(ctx, d.W, seedMarker, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return ensureAdmin(ctx, d)
}

// AdminUser is the demo account that is also an admin of the dev server
// (web panel at /admin), so no first-run setup link is needed in dev.
const AdminUser = "alice"

// ensureAdmin makes the demo admin an admin (idempotent; also upgrades data
// seeded before the admin panel existed).
func ensureAdmin(ctx context.Context, d *db.DB) error {
	_, err := d.W.ExecContext(ctx, `UPDATE users SET is_admin = 1 WHERE username = ? AND is_admin = 0`, AdminUser)
	return err
}

func (s *seeder) do(method, path, user string, body any, want ...int) ([]byte, error) {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("Content-Type", "application/json")
	if tok := s.tokens[user]; tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return s.send(req, want...)
}

func (s *seeder) send(req *http.Request, want ...int) ([]byte, error) {
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	for _, w := range want {
		if rec.Code == w {
			return rec.Body.Bytes(), nil
		}
	}
	return nil, fmt.Errorf("%s %s: status %d: %s", req.Method, req.URL.Path, rec.Code, rec.Body.String())
}

func (s *seeder) ensureUser(ctx context.Context, d *db.DB, username, name, email, phone string, hue float64) error {
	dev := map[string]any{"name": "Seeder", "platform": "ios"}
	res, err := s.do("POST", "/api/v1/auth/login", "", map[string]any{"method": "email", "identifier": email, "password": Password, "device": dev}, 200, 401)
	if err != nil {
		return err
	}
	var ar struct {
		Token string `json:"token"`
		User  struct {
			ID       string  `json:"id"`
			Username *string `json:"username"`
			Avatar   any     `json:"avatar"`
			Phone    *string `json:"phone"`
		} `json:"user"`
	}
	_ = json.Unmarshal(res, &ar)
	if ar.Token == "" {
		res, err = s.do("POST", "/api/v1/auth/register", "", map[string]any{"method": "email", "identifier": email, "password": Password,
			"display_name": name, "invite_code": nil, "device": dev}, 200)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(res, &ar); err != nil {
			return err
		}
	}
	s.tokens[username], s.ids[username] = ar.Token, ar.User.ID
	if ar.User.Username == nil {
		if _, err := s.do("PATCH", "/api/v1/me", username, map[string]any{"username": username}, 200); err != nil {
			return err
		}
	}
	if ar.User.Phone == nil {
		err := d.Tx(ctx, func(tx *sql.Tx) error {
			return store.SetIdentifier(ctx, tx, ar.User.ID, "phone", phone, time.Now().UnixMilli())
		})
		if err != nil {
			return err
		}
	}
	if ar.User.Avatar == nil {
		req := httptest.NewRequest("PUT", "/api/v1/me/avatar", bytes.NewReader(avatarJPEG(hue)))
		req.RemoteAddr = "127.0.0.1:1"
		req.Header.Set("Content-Type", "image/jpeg")
		req.Header.Set("Authorization", "Bearer "+ar.Token)
		if _, err := s.send(req, 200); err != nil {
			return err
		}
	}
	return nil
}

func (s *seeder) befriend(a, b string) error {
	res, err := s.do("POST", "/api/v1/friends/requests", a, map[string]any{"user_id": s.ids[b]}, 200, 409)
	if err != nil {
		return err
	}
	var fr struct {
		Status  string `json:"status"`
		Request *struct {
			ID string `json:"id"`
		} `json:"request"`
	}
	_ = json.Unmarshal(res, &fr)
	if fr.Request != nil {
		_, err = s.do("POST", "/api/v1/friends/requests/"+fr.Request.ID+"/accept", b, nil, 200)
	}
	return err
}

func (s *seeder) postMoment(author, clientID string, pattern int, caption map[string]any, video bool, audience string, to []string) (string, error) {
	if audience == "" {
		audience = "all"
	}
	kind, mediaType, mediaBytes := "photo", "image/jpeg", photoJPEG(pattern, 1080)
	var dur any
	w, h := 1080, 1080
	if video {
		kind, mediaType, mediaBytes = "video", "video/mp4", demoMP4
		dur, w, h = 4000, 480, 480
	}
	meta := map[string]any{"client_id": clientID, "kind": kind, "caption": caption, "audience": audience, "recipient_ids": to,
		"width": w, "height": h, "duration_ms": dur, "local_date": s.clock.Now().Format(time.DateOnly)}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	add := func(name, ctype string, data []byte) {
		hdr := textproto.MIMEHeader{}
		hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, name, name))
		hdr.Set("Content-Type", ctype)
		pw, _ := mw.CreatePart(hdr)
		pw.Write(data)
	}
	mb, _ := json.Marshal(meta)
	add("meta", "application/json", mb)
	add("media", mediaType, mediaBytes)
	add("thumb", "image/jpeg", photoJPEG(pattern, 512))
	mw.Close()
	req := httptest.NewRequest("POST", "/api/v1/moments", &buf)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+s.tokens[author])
	res, err := s.send(req, 201, 200)
	if err != nil {
		return "", err
	}
	var mv struct{ ID string }
	if err := json.Unmarshal(res, &mv); err != nil {
		return "", err
	}
	return mv.ID, nil
}

// ---- image generation (encode only; uploads are never decoded) ----

func hsv(h, s, v float64) color.RGBA {
	h = math.Mod(h, 360)
	if h < 0 {
		h += 360
	}
	c := v * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := v - c
	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return color.RGBA{uint8((r + m) * 255), uint8((g + m) * 255), uint8((b + m) * 255), 255}
}

func mix(a, b color.RGBA, t float64) color.RGBA {
	t = math.Max(0, math.Min(1, t))
	l := func(x, y uint8) uint8 { return uint8(float64(x)*(1-t) + float64(y)*t) }
	return color.RGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), 255}
}

func encodeJPEG(img image.Image, q int) []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// photoJPEG renders one of 12 colourful square patterns at size×size.
//
// Dev only: when FRAME_DEV_PHOTOS names a directory containing "<pattern>-<size>.jpg"
// (e.g. 03-1080.jpg and 03-512.jpg), that file is used instead, so App Store
// screenshots can show real-looking photos. Missing files fall back to the pattern.
func photoJPEG(pattern, size int) []byte {
	if dir := os.Getenv("FRAME_DEV_PHOTOS"); dir != "" {
		if b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("%02d-%d.jpg", pattern, size))); err == nil {
			return b
		}
	}
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	base := float64(pattern) * 30
	top, bottom := hsv(base, 0.65, 0.95), hsv(base+50, 0.8, 0.55)
	accent := hsv(base+180, 0.7, 1)
	n := float64(size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			fx, fy := float64(x)/n, float64(y)/n
			c := mix(top, bottom, fy)
			var t float64
			switch pattern % 6 {
			case 0: // sun
				d := math.Hypot(fx-0.5, fy-0.42)
				t = 1 - smooth(0.2, 0.22, d)
			case 1: // waves
				t = smooth(0.0, 0.04, math.Sin(fx*18+math.Sin(fy*6)*2)*0.5+0.5-fy*0.9)
			case 2: // rings
				t = 0.6 * smooth(0.45, 0.5, math.Mod(math.Hypot(fx-0.5, fy-0.5)*10, 1))
			case 3: // diagonal stripes
				t = 0.5 * smooth(0.45, 0.55, math.Mod((fx+fy)*6, 1))
			case 4: // dots
				gx, gy := math.Mod(fx*6, 1)-0.5, math.Mod(fy*6, 1)-0.5
				t = 1 - smooth(0.18, 0.22, math.Hypot(gx, gy))
			case 5: // hills
				t = smooth(-0.01, 0.01, fy-(0.65+0.08*math.Sin(fx*7+float64(pattern))))
			}
			img.SetRGBA(x, y, mix(c, accent, t))
		}
	}
	return encodeJPEG(img, 82)
}

func smooth(a, b, x float64) float64 {
	t := math.Max(0, math.Min(1, (x-a)/(b-a)))
	return t * t * (3 - 2*t)
}

// avatarJPEG renders a 512×512 avatar in the given hue.
func avatarJPEG(hue float64) []byte {
	const size = 512
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	bg, fg := hsv(hue, 0.55, 0.95), hsv(hue+25, 0.85, 0.6)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			fx, fy := float64(x)/size, float64(y)/size
			c := mix(bg, hsv(hue-20, 0.5, 1), fy)
			head := 1 - smooth(0.17, 0.18, math.Hypot(fx-0.5, fy-0.4))
			body := 1 - smooth(0.3, 0.31, math.Hypot((fx-0.5)*0.9, fy-1.02))
			img.SetRGBA(x, y, mix(c, fg, math.Max(head, body)))
		}
	}
	return encodeJPEG(img, 85)
}

// PhotoJPEG exposes the generator for tests and fixtures.
func PhotoJPEG(pattern, size int) []byte { return photoJPEG(pattern, size) }

// DemoMP4 returns the embedded demo video.
func DemoMP4() []byte { return demoMP4 }

// AvatarJPEG exposes the avatar generator for tests and fixtures.
func AvatarJPEG(hue float64) []byte { return avatarJPEG(hue) }
