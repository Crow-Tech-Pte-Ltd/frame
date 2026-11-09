// Package push fans out end-to-end encrypted notifications to the push relay
// (docs/PUSH.md). Request handlers call Notify, which never blocks: a bounded
// fan-out queue resolves recipients' devices, seals one payload per device
// with that device's AES-256-GCM push_key, and a bounded send queue POSTs to
// the relay with retries (3 attempts, exponential backoff). Devices the relay
// reports as unregistered (410) are deleted.
package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

// Payload is the plaintext JSON sealed for the device.
type Payload struct {
	V         int     `json:"v"`
	Type      string  `json:"type"` // moment | message | reaction | friend_request | friend_accepted
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	MomentID  *string `json:"moment_id"`
	PeerID    *string `json:"peer_id"`
	ThumbPath *string `json:"thumb_path"`
	CreatedAt int64   `json:"created_at"`
}

// Seal encrypts plaintext with a 32-byte key: base64(nonce12 || ct || tag16).
func Seal(key, plaintext []byte) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts a Seal output (used by tests and the shared test vectors).
func Open(key []byte, payloadB64 string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(payloadB64)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("push: payload too short")
	}
	return gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("push: key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Truncate shortens s to at most n runes, adding an ellipsis when cut.
func Truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// Pref names a per-device notification preference.
type Pref string

const (
	PrefMoments   Pref = "moments"
	PrefMessages  Pref = "messages"
	PrefReactions Pref = "reactions"
	PrefFriends   Pref = "friends"
)

func allows(d *store.Device, p Pref) bool {
	switch p {
	case PrefMoments:
		return d.PrefMoments
	case PrefMessages:
		return d.PrefMessages
	case PrefReactions:
		return d.PrefReactions
	case PrefFriends:
		return d.PrefFriends
	}
	return false
}

// Notification is a push-worthy event for a set of users.
type Notification struct {
	UserIDs    []string
	Pref       Pref
	Payload    Payload
	CollapseID string
	ThreadID   string
	// Widget also sends widget-refresh pushes (new moments) — to every device
	// with a widget token, and to Android devices that muted moment alerts.
	Widget bool
}

type job struct {
	token    string
	typ      string // alert | widget
	payload  string
	collapse string
	thread   string
	attempt  int
}

// Options configures a Dispatcher.
type Options struct {
	RelayURL       string // "" disables push entirely
	HTTPClient     *http.Client
	Devices        func(ctx context.Context, userIDs []string) ([]store.Device, error)
	OnUnregistered func(ctx context.Context, token string)
	Log            *slog.Logger
	SendWorkers    int           // default 4
	QueueSize      int           // default 4096
	BaseBackoff    time.Duration // default 1s
	MaxAttempts    int           // default 3
}

// Dispatcher owns the fan-out and send queues.
type Dispatcher struct {
	opt      Options
	fanout   chan Notification
	sends    chan job
	fanoutWG sync.WaitGroup
	sendWG   sync.WaitGroup
	mu       sync.RWMutex // guards closing fanout vs. Notify
	stopped  atomic.Bool
	quit     chan struct{}

	Sent, Failed, Dropped atomic.Int64
}

// NewDispatcher creates a dispatcher; call Start to run workers.
func NewDispatcher(opt Options) *Dispatcher {
	if opt.HTTPClient == nil {
		opt.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	if opt.SendWorkers <= 0 {
		opt.SendWorkers = 4
	}
	if opt.QueueSize <= 0 {
		opt.QueueSize = 4096
	}
	if opt.BaseBackoff <= 0 {
		opt.BaseBackoff = time.Second
	}
	if opt.MaxAttempts <= 0 {
		opt.MaxAttempts = 3
	}
	return &Dispatcher{opt: opt, fanout: make(chan Notification, 1024), sends: make(chan job, opt.QueueSize), quit: make(chan struct{})}
}

// Enabled reports whether a relay is configured.
func (d *Dispatcher) Enabled() bool { return d != nil && d.opt.RelayURL != "" }

// Start launches the workers.
func (d *Dispatcher) Start() {
	if !d.Enabled() {
		return
	}
	for i := 0; i < 2; i++ {
		d.fanoutWG.Add(1)
		go d.fanoutWorker()
	}
	for i := 0; i < d.opt.SendWorkers; i++ {
		d.sendWG.Add(1)
		go d.sendWorker()
	}
}

// Notify queues a notification without blocking; it is dropped (and counted)
// if the queue is full or push is disabled.
func (d *Dispatcher) Notify(n Notification) {
	if !d.Enabled() || len(n.UserIDs) == 0 {
		return
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.stopped.Load() {
		return
	}
	select {
	case d.fanout <- n:
	default:
		d.drop("fan-out")
	}
}

// Stop stops accepting work and waits (until ctx is done) for queued pushes.
func (d *Dispatcher) Stop(ctx context.Context) {
	if !d.Enabled() {
		return
	}
	d.mu.Lock()
	if d.stopped.Swap(true) {
		d.mu.Unlock()
		return
	}
	close(d.fanout)
	d.mu.Unlock()
	done := make(chan struct{})
	go func() {
		d.fanoutWG.Wait() // all notifications expanded into sends
		close(d.quit)     // send workers drain the queue and exit
		d.sendWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// drop counts a dropped push, logging the 1st and every 1000th.
func (d *Dispatcher) drop(queue string) {
	if n := d.Dropped.Add(1); n == 1 || n%1000 == 0 {
		d.opt.Log.Warn("push queue full; dropping", "queue", queue, "dropped_total", n)
	}
}

func (d *Dispatcher) fanoutWorker() {
	defer d.fanoutWG.Done()
	for n := range d.fanout {
		d.expand(n)
	}
}

func (d *Dispatcher) expand(n Notification) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	devs, err := d.opt.Devices(ctx, n.UserIDs)
	if err != nil {
		d.opt.Log.Error("push: loading devices failed", "err", err)
		return
	}
	plain, err := json.Marshal(n.Payload)
	if err != nil {
		return
	}
	for i := range devs {
		dev := &devs[i]
		alert := allows(dev, n.Pref)
		var widgetToken string
		if n.Widget {
			if dev.WidgetRelayToken != nil && *dev.WidgetRelayToken != "" {
				widgetToken = *dev.WidgetRelayToken
			} else if dev.Platform == "android" && !alert {
				widgetToken = dev.RelayToken
			}
		}
		if !alert && widgetToken == "" {
			continue
		}
		sealed, err := Seal(dev.PushKey, plain)
		if err != nil {
			d.opt.Log.Warn("push: sealing failed", "err", err)
			continue
		}
		if alert {
			d.enqueue(job{token: dev.RelayToken, typ: "alert", payload: sealed, collapse: n.CollapseID, thread: n.ThreadID})
		}
		if widgetToken != "" {
			d.enqueue(job{token: widgetToken, typ: "widget", payload: sealed, collapse: n.CollapseID})
		}
	}
}

func (d *Dispatcher) enqueue(j job) {
	select {
	case d.sends <- j:
	default:
		d.drop("send")
	}
}

func (d *Dispatcher) sendWorker() {
	defer d.sendWG.Done()
	for {
		select {
		case j := <-d.sends:
			d.send(j)
		case <-d.quit:
			// Drain what is already queued, then exit.
			for {
				select {
				case j := <-d.sends:
					d.send(j)
				default:
					return
				}
			}
		}
	}
}

type relayRequest struct {
	RelayToken string  `json:"relay_token"`
	Type       string  `json:"type"`
	Payload    string  `json:"payload"`
	CollapseID *string `json:"collapse_id"`
	ThreadID   *string `json:"thread_id"`
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// errGone marks a 410 response.
var errGone = errors.New("push: device unregistered")

type permanentError struct{ status int }

func (e permanentError) Error() string {
	return fmt.Sprintf("push: relay rejected request (%d)", e.status)
}

func (d *Dispatcher) send(j job) {
	err := d.post(j)
	switch {
	case err == nil:
		d.Sent.Add(1)
	case errors.Is(err, errGone):
		if d.opt.OnUnregistered != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			d.opt.OnUnregistered(ctx, j.token)
			cancel()
		}
	default:
		var perm permanentError
		if errors.As(err, &perm) || j.attempt+1 >= d.opt.MaxAttempts || d.stopped.Load() {
			d.Failed.Add(1)
			d.opt.Log.Warn("push failed", "err", err, "attempts", j.attempt+1)
			return
		}
		j.attempt++
		backoff := d.opt.BaseBackoff * time.Duration(1<<(2*(j.attempt-1))) // 1×, 4×, 16× …
		backoff += time.Duration(mrand.Int64N(int64(backoff/4) + 1))
		time.AfterFunc(backoff, func() {
			if !d.stopped.Load() {
				d.enqueue(j)
			}
		})
	}
}

func (d *Dispatcher) post(j job) error {
	body, _ := json.Marshal(relayRequest{RelayToken: j.token, Type: j.typ, Payload: j.payload,
		CollapseID: strPtr(j.collapse), ThreadID: strPtr(j.thread)})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.opt.RelayURL+"/v1/push", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "frame-server")
	resp, err := d.opt.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusGone:
		return errGone
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return fmt.Errorf("push: relay status %d", resp.StatusCode)
	default:
		return permanentError{resp.StatusCode}
	}
}
