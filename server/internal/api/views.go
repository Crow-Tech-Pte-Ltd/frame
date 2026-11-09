package api

import (
	"context"
	"encoding/json"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/db"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/media"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

// JSON views of API objects (docs/API.md). Optional fields are pointers or
// nil slices rendered as null — never omitted.

type userSummary struct {
	ID          string     `json:"id"`
	Username    *string    `json:"username"`
	DisplayName string     `json:"display_name"`
	Avatar      *media.Ref `json:"avatar"`
}

type streakView struct {
	Count    int     `json:"count"`
	LastDate *string `json:"last_date"`
}

type meView struct {
	userSummary
	Email        *string    `json:"email"`
	Phone        *string    `json:"phone"`
	AuthMethods  []string   `json:"auth_methods"`
	Discoverable bool       `json:"discoverable"`
	Searchable   bool       `json:"searchable"`
	ReadReceipts bool       `json:"read_receipts"`
	Streak       streakView `json:"streak"`
	Stats        meStats    `json:"stats"`
	IsAdmin      bool       `json:"is_admin"`
	CreatedAt    int64      `json:"created_at"`
}

type meStats struct {
	Moments int `json:"moments"`
	Friends int `json:"friends"`
}

type captionView struct {
	Type       string          `json:"type"`
	Text       string          `json:"text"`
	Style      string          `json:"style"`
	Color      *string         `json:"color"`
	Background *string         `json:"background"`
	Data       json.RawMessage `json:"data"`
}

type reactionView struct {
	User      *userSummary `json:"user"`
	Emoji     string       `json:"emoji"`
	CreatedAt int64        `json:"created_at"`
}

type viewerView struct {
	User     *userSummary `json:"user"`
	ViewedAt int64        `json:"viewed_at"`
}

type momentView struct {
	ID           string         `json:"id"`
	Author       *userSummary   `json:"author"`
	Kind         string         `json:"kind"`
	CreatedAt    int64          `json:"created_at"`
	Caption      *captionView   `json:"caption"`
	Media        *media.Ref     `json:"media"`
	Thumb        *media.Ref     `json:"thumb"`
	Audience     string         `json:"audience"`
	RecipientIDs []string       `json:"recipient_ids"`
	LocalDate    string         `json:"local_date"`
	Reactions    []reactionView `json:"reactions"`
	Viewers      []viewerView   `json:"viewers"`
	Viewed       bool           `json:"viewed"`
}

type momentRefView struct {
	ID       string       `json:"id"`
	AuthorID string       `json:"author_id"`
	Kind     string       `json:"kind"`
	Thumb    *media.Ref   `json:"thumb"`
	Caption  *captionView `json:"caption"`
	Deleted  bool         `json:"deleted"`
}

type msgReactionView struct {
	UserID string `json:"user_id"`
	Emoji  string `json:"emoji"`
}

type messageView struct {
	ID          string            `json:"id"`
	ClientID    string            `json:"client_id"`
	SenderID    string            `json:"sender_id"`
	RecipientID string            `json:"recipient_id"`
	Text        string            `json:"text"`
	CreatedAt   int64             `json:"created_at"`
	Moment      *momentRefView    `json:"moment"`
	Reactions   []msgReactionView `json:"reactions"`
	ReadAt      *int64            `json:"read_at"`
}

type conversationView struct {
	Peer        *userSummary `json:"peer"`
	LastMessage *messageView `json:"last_message"`
	UnreadCount int          `json:"unread_count"`
	UpdatedAt   int64        `json:"updated_at"`
}

type friendRequestView struct {
	ID        string       `json:"id"`
	From      *userSummary `json:"from"`
	To        *userSummary `json:"to"`
	CreatedAt int64        `json:"created_at"`
}

type friendView struct {
	User  *userSummary `json:"user"`
	Since int64        `json:"since"`
}

type userRelView struct {
	User       *userSummary `json:"user"`
	Friendship string       `json:"friendship"`
}

func captionOf(c *store.Caption) *captionView {
	if c == nil {
		return nil
	}
	return &captionView{Type: c.Type, Text: c.Text, Style: c.Style, Color: c.Color, Background: c.Background, Data: c.Data}
}

// renderer batches lookups (users, avatars, media) for one response so
// lists render with a constant number of queries.
type renderer struct {
	s     *Server
	ctx   context.Context
	q     db.Querier
	me    string
	users map[string]*store.User
	media map[string]*store.Media
}

func (s *Server) renderer(ctx context.Context, me string) *renderer {
	return &renderer{s: s, ctx: ctx, q: s.db.R, me: me, users: map[string]*store.User{}, media: map[string]*store.Media{}}
}

// withQ makes the renderer read through q (e.g. a transaction).
func (r *renderer) withQ(q db.Querier) *renderer { r.q = q; return r }

func (r *renderer) addUser(u *store.User) {
	if u != nil {
		r.users[u.ID] = u
	}
}

// loadUsers fetches users (and their avatar media) not yet cached.
func (r *renderer) loadUsers(ids ...string) error {
	var missing []string
	for _, id := range ids {
		if _, ok := r.users[id]; !ok && id != "" {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		us, err := store.GetUsers(r.ctx, r.q, missing)
		if err != nil {
			return err
		}
		for id, u := range us {
			r.users[id] = u
		}
	}
	var avatars []string
	for _, id := range ids {
		if u := r.users[id]; u != nil && u.AvatarMediaID != nil {
			avatars = append(avatars, *u.AvatarMediaID)
		}
	}
	return r.loadMedia(avatars...)
}

func (r *renderer) loadMedia(ids ...string) error {
	var missing []string
	for _, id := range ids {
		if _, ok := r.media[id]; !ok && id != "" {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	ms, err := store.GetMediaMany(r.ctx, r.q, missing)
	if err != nil {
		return err
	}
	for id, m := range ms {
		r.media[id] = m
	}
	return nil
}

func (r *renderer) ref(id string) *media.Ref {
	return r.s.media.Ref(r.media[id])
}

// summary renders a cached user (call loadUsers first). Unknown users (e.g.
// deleted) render as a placeholder so lists never break.
func (r *renderer) summary(id string) *userSummary {
	u := r.users[id]
	if u == nil {
		return &userSummary{ID: id, DisplayName: "Deleted user"}
	}
	us := &userSummary{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName}
	if u.AvatarMediaID != nil {
		us.Avatar = r.ref(*u.AvatarMediaID)
	}
	return us
}

func (r *renderer) meView(u *store.User) (*meView, error) {
	r.addUser(u)
	if err := r.loadUsers(u.ID); err != nil {
		return nil, err
	}
	methods := []string{}
	if u.PasswordHash != nil {
		methods = append(methods, "password")
	}
	if u.AppleSub != nil {
		methods = append(methods, "apple")
	}
	if u.GoogleSub != nil {
		methods = append(methods, "google")
	}
	count, last, err := store.Streak(r.ctx, r.q, u.ID)
	if err != nil {
		return nil, err
	}
	sv := streakView{Count: count}
	if last != "" {
		sv.LastDate = &last
	}
	var st meStats
	if st.Moments, err = store.CountMoments(r.ctx, r.q, u.ID); err != nil {
		return nil, err
	}
	if st.Friends, err = store.CountFriends(r.ctx, r.q, u.ID); err != nil {
		return nil, err
	}
	return &meView{userSummary: *r.summary(u.ID), Email: u.Email, Phone: u.Phone, AuthMethods: methods,
		Discoverable: u.Discoverable, Searchable: u.Searchable, ReadReceipts: u.ReadReceipts, Streak: sv,
		Stats: st, IsAdmin: u.IsAdmin, CreatedAt: u.CreatedAt}, nil
}

// moments renders moments as seen by r.me.
func (r *renderer) moments(ms []*store.Moment) ([]*momentView, error) {
	var own, others, all, mediaIDs, userIDs []string
	for _, m := range ms {
		all = append(all, m.ID)
		mediaIDs = append(mediaIDs, m.MediaID, m.ThumbID)
		userIDs = append(userIDs, m.AuthorID)
		if m.AuthorID == r.me {
			own = append(own, m.ID)
		} else {
			others = append(others, m.ID)
		}
	}
	if err := r.loadMedia(mediaIDs...); err != nil {
		return nil, err
	}
	audience, err := store.AudienceFor(r.ctx, r.q, own)
	if err != nil {
		return nil, err
	}
	ownReactions, err := store.ReactionsFor(r.ctx, r.q, own, "")
	if err != nil {
		return nil, err
	}
	myReactions, err := store.ReactionsFor(r.ctx, r.q, others, r.me)
	if err != nil {
		return nil, err
	}
	viewed, err := store.ViewedSet(r.ctx, r.q, r.me, others)
	if err != nil {
		return nil, err
	}
	for _, list := range audience {
		for _, a := range list {
			if a.ViewedAt != nil {
				userIDs = append(userIDs, a.UserID)
			}
		}
	}
	for _, list := range ownReactions {
		for _, x := range list {
			userIDs = append(userIDs, x.UserID)
		}
	}
	userIDs = append(userIDs, r.me)
	if err := r.loadUsers(userIDs...); err != nil {
		return nil, err
	}
	out := make([]*momentView, 0, len(ms))
	for _, m := range ms {
		v := &momentView{ID: m.ID, Author: r.summary(m.AuthorID), Kind: m.Kind, CreatedAt: m.CreatedAt,
			Caption: captionOf(m.Caption), Media: r.ref(m.MediaID), Thumb: r.ref(m.ThumbID), Audience: m.Audience,
			LocalDate: m.LocalDate, Reactions: []reactionView{}}
		reactions := myReactions[m.ID]
		if m.AuthorID == r.me {
			v.Viewed = true
			v.RecipientIDs = []string{}
			v.Viewers = []viewerView{}
			for _, a := range audience[m.ID] {
				v.RecipientIDs = append(v.RecipientIDs, a.UserID)
				if a.ViewedAt != nil {
					v.Viewers = append(v.Viewers, viewerView{User: r.summary(a.UserID), ViewedAt: *a.ViewedAt})
				}
			}
			reactions = ownReactions[m.ID]
		} else {
			v.Viewed = viewed[m.ID]
		}
		for _, x := range reactions {
			v.Reactions = append(v.Reactions, reactionView{User: r.summary(x.UserID), Emoji: x.Emoji, CreatedAt: x.CreatedAt})
		}
		out = append(out, v)
	}
	return out, nil
}

func (r *renderer) moment(m *store.Moment) (*momentView, error) {
	vs, err := r.moments([]*store.Moment{m})
	if err != nil {
		return nil, err
	}
	return vs[0], nil
}

// messages renders chat messages as seen by r.me.
func (r *renderer) messages(ms []*store.Message) ([]*messageView, error) {
	var ids, momentIDs []string
	for _, m := range ms {
		ids = append(ids, m.ID)
		if m.MomentID != nil {
			momentIDs = append(momentIDs, *m.MomentID)
		}
	}
	reactions, err := store.MessageReactionsFor(r.ctx, r.q, ids)
	if err != nil {
		return nil, err
	}
	visible := map[string]*store.Moment{}
	for _, mid := range momentIDs {
		if _, done := visible[mid]; done {
			continue
		}
		mm, err := store.VisibleMoment(r.ctx, r.q, r.me, mid)
		if err != nil && err != store.ErrNotFound {
			return nil, err
		}
		visible[mid] = mm
		if mm != nil {
			if err := r.loadMedia(mm.ThumbID); err != nil {
				return nil, err
			}
		}
	}
	out := make([]*messageView, 0, len(ms))
	for _, m := range ms {
		v := &messageView{ID: m.ID, ClientID: m.ClientID, SenderID: m.SenderID, RecipientID: m.RecipientID,
			Text: m.Text, CreatedAt: m.CreatedAt, Reactions: []msgReactionView{}}
		// The recipient sees when they read it; the sender only sees the
		// receipt (absent when the reader disabled read receipts).
		if r.me == m.RecipientID {
			v.ReadAt = m.ReadAt
		} else {
			v.ReadAt = m.ReceiptAt
		}
		if m.MomentID != nil {
			ref := &momentRefView{ID: *m.MomentID, Deleted: true}
			if m.MomentAuthorID != nil {
				ref.AuthorID = *m.MomentAuthorID
			}
			if m.MomentKind != nil {
				ref.Kind = *m.MomentKind
			}
			if mm := visible[*m.MomentID]; mm != nil {
				ref.Deleted = false
				ref.Thumb = r.ref(mm.ThumbID)
				ref.Caption = captionOf(mm.Caption)
			}
			v.Moment = ref
		}
		for _, x := range reactions[m.ID] {
			v.Reactions = append(v.Reactions, msgReactionView{UserID: x.UserID, Emoji: x.Emoji})
		}
		out = append(out, v)
	}
	return out, nil
}

func (r *renderer) message(m *store.Message) (*messageView, error) {
	vs, err := r.messages([]*store.Message{m})
	if err != nil {
		return nil, err
	}
	return vs[0], nil
}

func (r *renderer) requests(rs []store.FriendRequest) ([]friendRequestView, error) {
	var uids []string
	for _, x := range rs {
		uids = append(uids, x.FromID, x.ToID)
	}
	if err := r.loadUsers(uids...); err != nil {
		return nil, err
	}
	out := make([]friendRequestView, 0, len(rs))
	for _, x := range rs {
		out = append(out, friendRequestView{ID: x.ID, From: r.summary(x.FromID), To: r.summary(x.ToID), CreatedAt: x.CreatedAt})
	}
	return out, nil
}
