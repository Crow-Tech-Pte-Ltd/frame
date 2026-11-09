package push

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

func TestSharedVectors(t *testing.T) {
	b, err := os.ReadFile("../../../docs/test-vectors/push.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Vectors []struct {
			Key       string `json:"key_b64"`
			Nonce     string `json:"nonce_hex"`
			Plaintext string `json:"plaintext_utf8"`
			Payload   string `json:"payload_b64"`
		}
	}
	if err := json.Unmarshal(b, &doc); err != nil || len(doc.Vectors) == 0 {
		t.Fatal(err)
	}
	for i, v := range doc.Vectors {
		key, _ := base64.StdEncoding.DecodeString(v.Key)
		got, err := Open(key, v.Payload)
		if err != nil || string(got) != v.Plaintext {
			t.Errorf("vector %d: %v %q", i, err, got)
		}
		raw, _ := base64.StdEncoding.DecodeString(v.Payload)
		if hex.EncodeToString(raw[:12]) != v.Nonce {
			t.Errorf("vector %d nonce", i)
		}
		// Our sealer produces payloads the same decoder accepts.
		sealed, err := Seal(key, []byte(v.Plaintext))
		if err != nil {
			t.Fatal(err)
		}
		if back, err := Open(key, sealed); err != nil || string(back) != v.Plaintext {
			t.Fatal("round trip")
		}
		bad := []byte(v.Payload)
		bad[20] ^= 1
		if _, err := Open(key, string(bad)); err == nil {
			t.Error("tampered payload accepted")
		}
	}
	if _, err := Seal(make([]byte, 16), nil); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestPayloadShape(t *testing.T) {
	b, _ := json.Marshal(Payload{V: 1, Type: "message", Title: "Bob", Body: "hi", CreatedAt: 1})
	want := `{"v":1,"type":"message","title":"Bob","body":"hi","moment_id":null,"peer_id":null,"thumb_path":null,"created_at":1}`
	if string(b) != want {
		t.Fatalf("%s", b)
	}
	if Truncate("héllo wörld", 5) != "héll…" || Truncate("abc", 5) != "abc" {
		t.Fatal("truncate")
	}
}

type relay struct {
	mu     sync.Mutex
	got    []relayRequest
	status func(n int) int
	n      atomic.Int64
}

func (r *relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var body relayRequest
	json.NewDecoder(req.Body).Decode(&body)
	r.mu.Lock()
	r.got = append(r.got, body)
	r.mu.Unlock()
	n := int(r.n.Add(1))
	st := 200
	r.mu.Lock()
	status := r.status
	r.mu.Unlock()
	if status != nil {
		st = status(n)
	}
	w.WriteHeader(st)
}

func (r *relay) setStatus(f func(int) int) {
	r.mu.Lock()
	r.status = f
	r.mu.Unlock()
}

func (r *relay) requests() []relayRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]relayRequest(nil), r.got...)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timeout")
}

func device(user, token, platform string, widget *string, prefs bool) store.Device {
	key := make([]byte, 32)
	copy(key, token)
	return store.Device{SessionID: "s-" + token, UserID: user, Platform: platform, RelayToken: token, WidgetRelayToken: widget,
		PushKey: key, PrefMoments: prefs, PrefMessages: true, PrefReactions: true, PrefFriends: true}
}

func TestDispatcherFanOutRetryAndGone(t *testing.T) {
	rl := &relay{status: func(n int) int {
		if n == 1 {
			return 503 // first attempt fails, retried
		}
		return 200
	}}
	ts := httptest.NewServer(rl)
	defer ts.Close()
	w := "widget-1"
	devs := []store.Device{device("u1", "ios-1", "ios", &w, true), device("u2", "and-1", "android", nil, false), device("u2", "and-2", "android", nil, true)}
	var gone []string
	var mu sync.Mutex
	d := NewDispatcher(Options{RelayURL: ts.URL, BaseBackoff: 5 * time.Millisecond,
		Devices:        func(ctx context.Context, ids []string) ([]store.Device, error) { return devs, nil },
		OnUnregistered: func(ctx context.Context, tok string) { mu.Lock(); gone = append(gone, tok); mu.Unlock() }})
	d.Start()
	d.Notify(Notification{UserIDs: []string{"u1", "u2"}, Pref: PrefMoments, Widget: true, CollapseID: "moment-1",
		Payload: Payload{V: 1, Type: "moment", Title: "A", Body: "sent a new photo"}})
	// ios-1 alert + widget-1 widget + and-1 widget (moments muted) + and-2 alert, plus one retry.
	waitFor(t, func() bool { return len(rl.requests()) >= 5 })
	seen := map[string]bool{}
	for _, r := range rl.requests() {
		seen[r.RelayToken+"/"+r.Type] = true
		if r.Type == "alert" && (r.CollapseID == nil || *r.CollapseID != "moment-1") {
			t.Errorf("collapse id: %+v", r)
		}
	}
	for _, k := range []string{"ios-1/alert", "widget-1/widget", "and-1/widget", "and-2/alert"} {
		if !seen[k] {
			t.Errorf("missing %s in %v", k, seen)
		}
	}
	if seen["and-1/alert"] {
		t.Error("muted device got an alert")
	}
	// Each device's payload is sealed with its own key.
	for _, r := range rl.requests() {
		if r.RelayToken == "ios-1" && r.Type == "alert" {
			if _, err := Open(devs[0].PushKey, r.Payload); err != nil {
				t.Fatal("ios payload not sealed with its key")
			}
			if _, err := Open(devs[2].PushKey, r.Payload); err == nil {
				t.Fatal("decryptable with another key")
			}
		}
	}

	rl.setStatus(func(int) int { return 410 })
	d.Notify(Notification{UserIDs: []string{"u1"}, Pref: PrefMessages, Payload: Payload{V: 1, Type: "message"}})
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(gone) >= 1 })

	rl.setStatus(func(int) int { return 400 }) // permanent: no retry
	before := len(rl.requests())
	d.Notify(Notification{UserIDs: []string{"u1"}, Pref: PrefMessages, Payload: Payload{V: 1, Type: "message"}})
	waitFor(t, func() bool { return d.Failed.Load() >= 1 })
	time.Sleep(30 * time.Millisecond)
	if n := len(rl.requests()) - before; n != 3 { // 3 devices allow messages, one attempt each
		t.Errorf("400 retried: %d requests", n)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	d.Stop(ctx)
	d.Notify(Notification{UserIDs: []string{"u1"}, Pref: PrefMessages}) // no panic after stop
}

func TestRetriesExhausted(t *testing.T) {
	rl := &relay{status: func(int) int { return 500 }}
	ts := httptest.NewServer(rl)
	defer ts.Close()
	d := NewDispatcher(Options{RelayURL: ts.URL, BaseBackoff: time.Millisecond,
		Devices: func(ctx context.Context, ids []string) ([]store.Device, error) {
			return []store.Device{device("u", "t", "ios", nil, true)}, nil
		}})
	d.Start()
	d.Notify(Notification{UserIDs: []string{"u"}, Pref: PrefFriends, Payload: Payload{V: 1}})
	waitFor(t, func() bool { return d.Failed.Load() == 1 })
	if n := len(rl.requests()); n != 3 {
		t.Fatalf("attempts = %d, want 3", n)
	}
	d.Stop(context.Background())
}

func TestDisabledAndFullQueue(t *testing.T) {
	off := NewDispatcher(Options{})
	off.Start()
	off.Notify(Notification{UserIDs: []string{"u"}}) // no-op
	if off.Enabled() {
		t.Fatal("enabled without relay")
	}
	block := make(chan struct{})
	d := NewDispatcher(Options{RelayURL: "http://127.0.0.1:1", Devices: func(ctx context.Context, ids []string) ([]store.Device, error) {
		<-block
		return nil, nil
	}})
	d.Start()
	for i := 0; i < 2000; i++ {
		d.Notify(Notification{UserIDs: []string{"u"}}) // never blocks
	}
	if d.Dropped.Load() == 0 {
		t.Fatal("queue overflow not counted")
	}
	close(block)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	d.Stop(ctx)
}
