package api_test

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// TestLogsNeverContainSecrets runs a typical session at debug level and
// checks that tokens, passwords, captions, message text, media signatures and
// invite codes never reach the logs.
func TestLogsNeverContainSecrets(t *testing.T) {
	buf := &lockedBuf{}
	e := newEnv(t, withLogger(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	a, b := e.register("Alice"), e.register("Bob")
	e.befriend(a, b)
	m := e.postMoment(a, momentOpts{caption: map[string]any{"text": "SECRETCAPTION"}})
	e.sendMessage(b, a, "SECRETMESSAGE", m.ID)
	e.must(e.request("GET", m.Media.URL, "", nil, ""), 200)
	inv := e.must(e.do("POST", "/api/v1/invites", a.Token, nil), 200).m(t)["code"].(string)
	e.must(e.do("GET", "/api/v1/invites/"+inv, "", nil), 200)
	e.do("POST", "/api/v1/auth/login", "", map[string]any{"method": "email", "identifier": a.Email, "password": "WRONGPASSWORD"})
	e.must(e.do("PUT", "/api/v1/devices/current", a.Token, map[string]any{"platform": "ios", "relay_token": "RELAYTOKENSECRET",
		"push_key": "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="}), 204)
	logs := buf.String()
	if !strings.Contains(logs, `"route":"POST /api/v1/moments"`) {
		t.Fatalf("access log missing: %s", logs)
	}
	sig := m.Media.URL[strings.Index(m.Media.URL, "s=")+2:]
	for _, secret := range []string{a.Token, b.Token, "password123", "WRONGPASSWORD", "SECRETCAPTION", "SECRETMESSAGE", sig, inv, "RELAYTOKENSECRET", "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"} {
		if strings.Contains(logs, secret) {
			t.Errorf("log contains %q", secret)
		}
	}
}
