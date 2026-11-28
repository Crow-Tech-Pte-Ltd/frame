package api

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ids"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/push"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

// peer resolves the {peer_id} path value to an existing user other than me.
func (s *Server) peer(r *http.Request, me string) (*store.User, error) {
	id := ids.Normalize(r.PathValue("peer_id"))
	if id == "" {
		return nil, errUserNotFound
	}
	if id == me {
		return nil, errBadRequest("You can't chat with yourself")
	}
	u, err := store.GetUser(r.Context(), s.db.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errUserNotFound
	}
	return u, err
}

func (s *Server) handleConversations(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	ctx := r.Context()
	me := a.ID()
	convs, err := store.Conversations(ctx, s.db.R, me, 1000)
	if err != nil {
		return err
	}
	unread, err := store.UnreadCounts(ctx, s.db.R, me)
	if err != nil {
		return err
	}
	var peers, lastIDs []string
	for _, c := range convs {
		peers = append(peers, c.PeerID)
		if c.LastMessageID != nil {
			lastIDs = append(lastIDs, *c.LastMessageID)
		}
	}
	msgs, err := store.GetMessages(ctx, s.db.R, lastIDs)
	if err != nil {
		return err
	}
	rr := s.renderer(ctx, me)
	if err := rr.loadUsers(peers...); err != nil {
		return err
	}
	var list []*store.Message
	for _, id := range lastIDs {
		if m := msgs[id]; m != nil {
			list = append(list, m)
		}
	}
	views, err := rr.messages(list)
	if err != nil {
		return err
	}
	byID := make(map[string]*messageView, len(views))
	for _, v := range views {
		byID[v.ID] = v
	}
	out := make([]conversationView, 0, len(convs))
	for _, c := range convs {
		cv := conversationView{Peer: rr.summary(c.PeerID), UnreadCount: unread[c.PeerID], UpdatedAt: c.UpdatedAt}
		if c.LastMessageID != nil {
			cv.LastMessage = byID[*c.LastMessageID]
		}
		out = append(out, cv)
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": out})
	return nil
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	peer, err := s.peer(r, a.ID())
	if err != nil {
		return err
	}
	limit, err := queryInt(r, "limit", 50, 1, 100)
	if err != nil {
		return err
	}
	before, err := decodeCursor(r.URL.Query().Get("before"))
	if err != nil {
		return errBadRequest("Invalid cursor")
	}
	ctx := r.Context()
	msgs, err := store.MessagesPage(ctx, s.db.R, a.ID(), peer.ID, before, limit+1)
	if err != nil {
		return err
	}
	var next *string
	if len(msgs) > limit {
		msgs = msgs[:limit]
		next = encodeCursor(msgs[len(msgs)-1].ID)
	}
	views, err := s.renderer(ctx, a.ID()).messages(msgs)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": views, "next_cursor": next})
	return nil
}

type sendMessageReq struct {
	ClientID        string  `json:"client_id"`
	Text            string  `json:"text"`
	ReplyToMomentID *string `json:"reply_to_moment_id"`
}

func randomUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// validMessageText allows newlines/tabs but no other control characters.
func validMessageText(t string) bool {
	if !utf8.ValidString(t) || strings.TrimSpace(t) == "" {
		return false
	}
	if n := utf8.RuneCountInString(t); n < 1 || n > 2000 {
		return false
	}
	for _, r := range t {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	me := a.ID()
	if err := s.limit(s.rl.messages, me); err != nil {
		return err
	}
	peer, err := s.peer(r, me)
	if err != nil {
		return err
	}
	var req sendMessageReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.ClientID == "" {
		req.ClientID = randomUUID()
	}
	if !validClientID(req.ClientID) {
		return errBadRequest("client_id must be a UUID")
	}
	if !validMessageText(req.Text) {
		return errBadRequest("text must be 1–2000 characters")
	}
	ctx := r.Context()
	if existing, err := store.MessageByClientID(ctx, s.db.R, me, req.ClientID); err == nil {
		if existing.RecipientID != peer.ID {
			return errConflict("client_id was already used for another conversation")
		}
		return s.writeMessage(w, r, me, existing, http.StatusOK)
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if ok, err := store.AreFriends(ctx, s.db.R, me, peer.ID); err != nil {
		return err
	} else if !ok {
		return errNotFriends
	}
	now := s.now()
	m := &store.Message{ID: ids.At(now), ClientID: req.ClientID, SenderID: me, RecipientID: peer.ID, Text: req.Text, CreatedAt: now.UnixMilli()}
	var replied *store.Moment
	if req.ReplyToMomentID != nil && *req.ReplyToMomentID != "" {
		mid := ids.Normalize(*req.ReplyToMomentID)
		if mid == "" {
			return errMomentNotFound
		}
		mm, err := store.VisibleMoment(ctx, s.db.R, me, mid)
		if errors.Is(err, store.ErrNotFound) {
			return errMomentNotFound
		}
		if err != nil {
			return err
		}
		if ok, err := store.VisibleTo(ctx, s.db.R, peer.ID, mid); err != nil {
			return err
		} else if !ok {
			return errMomentNotFound
		}
		replied = mm
		m.MomentID, m.MomentAuthorID, m.MomentKind = &mm.ID, &mm.AuthorID, &mm.Kind
	}
	if err := store.InsertMessage(ctx, s.db.W, m); err != nil {
		if errors.Is(err, store.ErrDuplicateClient) {
			existing, err := store.MessageByClientID(ctx, s.db.R, me, req.ClientID)
			if err != nil {
				return err
			}
			return s.writeMessage(w, r, me, existing, http.StatusOK)
		}
		return err
	}
	v, err := s.renderer(ctx, me).message(m)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, v)
	s.emit("message.new", map[string]any{"message": v}, me, peer.ID)

	body := push.Truncate(m.Text, 150)
	var thumb *string
	if replied != nil {
		if replied.AuthorID == peer.ID {
			what := "photo"
			if replied.Kind == "video" {
				what = "video"
			}
			body = "replied to your " + what + ": " + push.Truncate(m.Text, 120)
		}
		t := s.media.Signer.URL(replied.ThumbID)
		thumb = &t
	}
	p := push.Payload{Type: "message", Title: a.User.DisplayName, Body: body, PeerID: strp(me), ThumbPath: thumb, CreatedAt: m.CreatedAt}
	if replied != nil {
		p.MomentID = strp(replied.ID)
	}
	s.notify(push.Notification{UserIDs: []string{peer.ID}, Pref: push.PrefMessages, ThreadID: "chat-" + me, Payload: p})
	return nil
}

func (s *Server) writeMessage(w http.ResponseWriter, r *http.Request, me string, m *store.Message, status int) error {
	v, err := s.renderer(r.Context(), me).message(m)
	if err != nil {
		return err
	}
	writeJSON(w, status, v)
	return nil
}

type readReq struct {
	UpTo string `json:"up_to"`
}

func (s *Server) handleRead(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	peer, err := s.peer(r, a.ID())
	if err != nil {
		return err
	}
	var req readReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	upTo := ids.Normalize(req.UpTo)
	if upTo == "" {
		return errBadRequest("up_to must be a message id")
	}
	ctx := r.Context()
	now := s.now().UnixMilli()
	receipts := a.User.ReadReceipts
	n, err := store.MarkRead(ctx, s.db.W, a.ID(), peer.ID, upTo, now, receipts)
	if err != nil {
		return err
	}
	if n > 0 {
		// My other devices update their unread badges.
		s.emit("message.read", map[string]any{"peer_id": peer.ID, "up_to": upTo, "read_at": now}, a.ID())
		if receipts {
			s.emit("message.read", map[string]any{"peer_id": a.ID(), "up_to": upTo, "read_at": now}, peer.ID)
		}
	}
	return noContent(w)
}

func (s *Server) handleTyping(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	if err := s.limit(s.rl.typing, a.ID()); err != nil {
		return err
	}
	peer, err := s.peer(r, a.ID())
	if err != nil {
		return err
	}
	if ok, err := store.AreFriends(r.Context(), s.db.R, a.ID(), peer.ID); err != nil {
		return err
	} else if !ok {
		return errNotFriends
	}
	s.emit("typing", map[string]any{"peer_id": a.ID()}, peer.ID)
	return noContent(w)
}

// participantMessage loads a message the caller is part of.
func (s *Server) participantMessage(r *http.Request, me string) (*store.Message, error) {
	id := ids.Normalize(r.PathValue("id"))
	if id == "" {
		return nil, errMessageNotFound
	}
	m, err := store.GetMessage(r.Context(), s.db.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errMessageNotFound
	}
	if err != nil {
		return nil, err
	}
	if m.SenderID != me && m.RecipientID != me {
		return nil, errMessageNotFound
	}
	return m, nil
}

func other(m *store.Message, me string) string {
	if m.SenderID == me {
		return m.RecipientID
	}
	return m.SenderID
}

func (s *Server) handleDeleteMessage(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	m, err := s.participantMessage(r, a.ID())
	if err != nil {
		return err
	}
	if m.SenderID != a.ID() {
		return errForbidden("Only the sender can unsend a message")
	}
	if err := s.deleteMessage(r.Context(), m); err != nil {
		return err
	}
	return noContent(w)
}

// deleteMessage deletes a message and tells both participants. Used for
// "unsend" and by the admin acting on a report.
func (s *Server) deleteMessage(ctx context.Context, m *store.Message) error {
	if err := store.DeleteMessage(ctx, s.db.W, m); err != nil {
		return err
	}
	s.emit("message.deleted", map[string]any{"id": m.ID, "peer_id": m.RecipientID}, m.SenderID)
	s.emit("message.deleted", map[string]any{"id": m.ID, "peer_id": m.SenderID}, m.RecipientID)
	return nil
}

func (s *Server) handleReactToMessage(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	return s.messageReaction(w, r, a, true)
}

func (s *Server) handleDeleteMessageReaction(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	return s.messageReaction(w, r, a, false)
}

func (s *Server) messageReaction(w http.ResponseWriter, r *http.Request, a *authCtx, set bool) error {
	me := a.ID()
	m, err := s.participantMessage(r, me)
	if err != nil {
		return err
	}
	var emoji any
	var req reactionReq
	if set {
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		if !validEmoji(req.Emoji) {
			return errBadRequest("emoji must be a single emoji")
		}
		emoji = req.Emoji
	}
	ctx := r.Context()
	peerID := other(m, me)
	if ok, err := store.AreFriends(ctx, s.db.R, me, peerID); err != nil {
		return err
	} else if !ok {
		return errNotFriends
	}
	changed := true
	if set {
		err = store.SetMessageReaction(ctx, s.db.W, m.ID, me, req.Emoji, s.now().UnixMilli())
	} else {
		changed, err = store.DeleteMessageReaction(ctx, s.db.W, m.ID, me)
	}
	if err != nil {
		return err
	}
	if changed {
		s.emit("message.reaction", map[string]any{"message_id": m.ID, "peer_id": peerID, "user_id": me, "emoji": emoji}, me)
		s.emit("message.reaction", map[string]any{"message_id": m.ID, "peer_id": me, "user_id": me, "emoji": emoji}, peerID)
	}
	return noContent(w)
}
