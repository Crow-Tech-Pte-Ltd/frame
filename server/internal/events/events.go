// Package events is the in-memory realtime hub behind GET /api/v1/events
// (Server-Sent Events). Each user may have several connections (devices);
// every event for a user fans out to all of them. Slow consumers are
// disconnected rather than allowed to block publishers; clients reconnect and
// re-fetch state (there is no replay).
package events

import (
	"bytes"
	"encoding/json"
	"sync"
)

// Event is one encoded SSE event.
type Event struct {
	Type string
	Data []byte // JSON
}

// Encode renders the event in text/event-stream framing.
func (e Event) Encode() []byte {
	b := make([]byte, 0, len(e.Type)+len(e.Data)+16)
	b = append(b, "event: "...)
	b = append(b, e.Type...)
	b = append(b, "\ndata: "...)
	b = append(b, e.Data...)
	b = append(b, "\n\n"...)
	return b
}

// New builds an event from a JSON-marshalable payload.
func New(typ string, data any) (Event, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(data); err != nil {
		return Event{}, err
	}
	return Event{Type: typ, Data: bytes.TrimRight(buf.Bytes(), "\n")}, nil
}

// Sub is a single SSE connection.
type Sub struct {
	UserID    string
	SessionID string
	C         chan Event    // events to write
	Done      chan struct{} // closed when the hub drops this subscriber
	once      sync.Once
}

func (s *Sub) close() { s.once.Do(func() { close(s.Done) }) }

// Hub routes events to subscribers.
type Hub struct {
	mu      sync.Mutex
	users   map[string]map[*Sub]struct{}
	closed  bool
	MaxPer  int // max connections per user (oldest dropped); default 8
	BufSize int // per-connection buffer; default 64
}

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{users: map[string]map[*Sub]struct{}{}, MaxPer: 8, BufSize: 64}
}

// Subscribe registers a connection. The caller must call Unsubscribe.
// Returns nil if the hub is shut down.
func (h *Hub) Subscribe(userID, sessionID string) *Sub {
	s := &Sub{UserID: userID, SessionID: sessionID, C: make(chan Event, h.BufSize), Done: make(chan struct{})}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	set := h.users[userID]
	if set == nil {
		set = map[*Sub]struct{}{}
		h.users[userID] = set
	}
	if len(set) >= h.MaxPer {
		// Drop an arbitrary existing connection to stay within the cap.
		for old := range set {
			delete(set, old)
			old.close()
			break
		}
	}
	set[s] = struct{}{}
	return s
}

// Unsubscribe removes a connection.
func (h *Hub) Unsubscribe(s *Sub) {
	if s == nil {
		return
	}
	h.mu.Lock()
	if set := h.users[s.UserID]; set != nil {
		delete(set, s)
		if len(set) == 0 {
			delete(h.users, s.UserID)
		}
	}
	h.mu.Unlock()
	s.close()
}

// Publish delivers ev to every connection of userID without blocking. A
// connection whose buffer is full is dropped.
func (h *Hub) Publish(userID string, ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.users[userID] {
		select {
		case s.C <- ev:
		default:
			delete(h.users[userID], s)
			s.close()
		}
	}
	if set := h.users[userID]; set != nil && len(set) == 0 {
		delete(h.users, userID)
	}
}

// PublishMany delivers the same event to several users.
func (h *Hub) PublishMany(userIDs []string, ev Event) {
	for _, u := range userIDs {
		h.Publish(u, ev)
	}
}

// CloseSession disconnects every connection belonging to a session (logout,
// revocation).
func (h *Hub) CloseSession(userID, sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.users[userID] {
		if s.SessionID == sessionID {
			delete(h.users[userID], s)
			s.close()
		}
	}
}

// CloseUser disconnects all of a user's connections (account deletion).
func (h *Hub) CloseUser(userID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.users[userID] {
		s.close()
	}
	delete(h.users, userID)
}

// Close disconnects everyone and rejects new subscriptions (shutdown).
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for u, set := range h.users {
		for s := range set {
			s.close()
		}
		delete(h.users, u)
	}
}

// Count returns the number of open connections for a user.
func (h *Hub) Count(userID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.users[userID])
}
