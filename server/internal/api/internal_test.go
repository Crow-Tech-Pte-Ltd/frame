package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/config"
	_ "github.com/Crow-Tech-Pte-Ltd/frame/server/internal/testutil"
)

func TestClientIP(t *testing.T) {
	mk := func(proxies string) *Server {
		cfg, err := config.Load(config.Source{Overrides: map[string]string{"FRAME_TRUSTED_PROXIES": proxies}})
		if err != nil {
			t.Fatal(err)
		}
		return &Server{cfg: cfg}
	}
	cases := []struct {
		proxies, remote, xff, cf, want string
	}{
		{"", "203.0.113.9:1234", "1.2.3.4", "5.6.7.8", "203.0.113.9"},                     // nothing trusted
		{"10.0.0.0/8", "203.0.113.9:1234", "1.2.3.4", "", "203.0.113.9"},                  // untrusted peer: XFF ignored
		{"10.0.0.0/8", "10.1.2.3:1234", "1.2.3.4", "", "1.2.3.4"},                         // trusted peer
		{"10.0.0.0/8", "10.1.2.3:1234", "6.6.6.6, 1.2.3.4, 10.9.9.9", "", "1.2.3.4"},      // rightmost untrusted hop, spoofed left ignored
		{"10.0.0.0/8,192.168.1.1", "192.168.1.1:80", "10.0.0.5", "", "10.0.0.5"},          // all hops trusted → leftmost
		{"10.0.0.0/8", "10.1.2.3:1234", "garbage", "", "10.1.2.3"},                        // unparsable XFF
		{"cloudflare", "127.0.0.1:5555", "", "198.51.100.7", "198.51.100.7"},              // cloudflared on localhost
		{"cloudflare", "203.0.113.9:1234", "", "198.51.100.7", "203.0.113.9"},             // CF header from the internet ignored
		{"cloudflare", "172.18.0.2:1234", "", "2001:db8::1", "172.18.0.2"},                // private peer is NOT trusted implicitly
		{"cloudflare,172.18.0.0/16", "172.18.0.2:1234", "", "2001:db8::1", "2001:db8::1"}, // explicitly listed cloudflared network
		{"", "[2001:db8::5]:443", "", "", "2001:db8::5"},                                  // v6 peer
		{"10.0.0.0/8", "[::ffff:10.0.0.1]:80", "1.2.3.4", "", "1.2.3.4"},                  // v4-mapped peer
	}
	for _, c := range cases {
		s := mk(c.proxies)
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if c.cf != "" {
			r.Header.Set("CF-Connecting-IP", c.cf)
		}
		if got := s.clientIP(r); got != netip.MustParseAddr(c.want) {
			t.Errorf("%+v → %s", c, got)
		}
	}
}

func TestETagMatch(t *testing.T) {
	tag := `W/"abc"`
	for h, want := range map[string]bool{`W/"abc"`: true, `"abc"`: true, `"x", W/"abc"`: true, `*`: true, `W/"abd"`: false, ``: false} {
		if etagMatch(h, tag) != want {
			t.Errorf("etagMatch(%q) != %v", h, want)
		}
	}
}

func TestEmoji(t *testing.T) {
	good := []string{"❤️", "❤", "😂", "👍🏽", "🇸🇬", "#️⃣", "👩‍💻", "🏳️‍🌈", "🫶", "é"}
	bad := []string{"", " ", "ab", "😀😀", "🇸", "🇸🇬🇸", "‍😀", "😀‍", "\x00", "a\n", "\xff", strings.Repeat("😀", 5)}
	for _, s := range good {
		if !validEmoji(s) {
			t.Errorf("valid emoji rejected: %q", s)
		}
	}
	for _, s := range bad {
		if validEmoji(s) {
			t.Errorf("invalid emoji accepted: %q", s)
		}
	}
}

func TestCursorRoundTrip(t *testing.T) {
	id := "01JC3V9Z4X8Q2M7T5R1N6K0B3H"
	got, err := decodeCursor(*encodeCursor(id))
	if err != nil || got != id {
		t.Fatalf("%q %v", got, err)
	}
	for _, bad := range []string{"x", "djE6", "djI6MDFKQzNWOVo0WDhRMk03VDVSMU42SzBCM0g", strings.Repeat("A", 100)} {
		if _, err := decodeCursor(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestParseMomentMeta(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	m, c, err := parseMomentMeta([]byte(`{"client_id":"6f1c2b1e-3d4a-4b5c-8d9e-0a1b2c3d4e5f","kind":"photo","caption":{"text":"hi","data":{"a": 1}},"audience":"selected","recipient_ids":["01jc3v9z4x8q2m7t5r1n6k0b3h"],"width":1440,"height":1440,"duration_ms":null}`), 15000, now)
	if err != nil {
		t.Fatal(err)
	}
	if m.LocalDate != "2026-11-07" || m.RecipientIDs[0] != "01JC3V9Z4X8Q2M7T5R1N6K0B3H" || c.Type != "text" || c.Style != "classic" || string(c.Data) != `{"a":1}` {
		t.Fatalf("%+v %+v", m, c)
	}
	m, _, err = parseMomentMeta([]byte(`{"client_id":"x","kind":"video","audience":"all","recipient_ids":["junk"],"width":480,"height":480,"duration_ms":15400}`), 15000, now)
	if err != nil || m.RecipientIDs != nil {
		t.Fatalf("all ignores recipient_ids: %v %+v", err, m)
	}
	if _, _, err := parseMomentMeta([]byte(`{"client_id":"x","kind":"photo","width":100,"height":100,"local_date":"2026-11-10"}`), 15000, now); err == nil {
		t.Fatal("future local_date accepted")
	}
}

func FuzzParseMomentMeta(f *testing.F) {
	f.Add([]byte(`{"client_id":"6f1c2b1e-3d4a-4b5c-8d9e-0a1b2c3d4e5f","kind":"photo","caption":{"type":"location","text":"x","style":"color","color":"#FFFFFF","background":"#000000","data":{"name":"y"}},"audience":"selected","recipient_ids":["01JC3V9Z4X8Q2M7T5R1N6K0B3H"],"width":1440,"height":1440,"duration_ms":null,"local_date":"2026-11-07"}`))
	f.Add([]byte(`{"kind":"video","duration_ms":-1}`))
	f.Add([]byte(`[]`))
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, b []byte) {
		m, c, err := parseMomentMeta(b, 15000, now)
		if err != nil {
			return
		}
		if m.Kind != "photo" && m.Kind != "video" || m.Width < 16 || m.Width > 4096 {
			t.Fatalf("invalid meta accepted: %+v", m)
		}
		if c != nil && (c.Text == "" || len(c.Data) > maxCaptionData) {
			t.Fatalf("invalid caption accepted: %+v", c)
		}
	})
}

func FuzzEmoji(f *testing.F) {
	for _, s := range []string{"❤️", "👩‍💻", "🇸🇬", "ab", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if validEmoji(s) && (len(s) == 0 || len(s) > 16) {
			t.Fatalf("accepted %q", s)
		}
	})
}

func FuzzDecodeCursor(f *testing.F) {
	f.Add(*encodeCursor("01JC3V9Z4X8Q2M7T5R1N6K0B3H"))
	f.Add("garbage")
	f.Fuzz(func(t *testing.T, c string) {
		id, err := decodeCursor(c)
		if err == nil && id != "" && len(id) != 26 {
			t.Fatalf("bad id %q", id)
		}
	})
}

func TestPanicRecovery(t *testing.T) {
	var logs strings.Builder
	cfg, _ := config.Load(config.Source{})
	s := &Server{cfg: cfg, log: slog.New(slog.NewJSONHandler(&logs, nil))}
	h := s.outer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("db password is hunter2") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/me", nil))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "hunter2") || !strings.Contains(rec.Body.String(), `"internal"`) {
		t.Fatalf("panic response: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(logs.String(), "panic") {
		t.Fatal("panic not logged")
	}
}
