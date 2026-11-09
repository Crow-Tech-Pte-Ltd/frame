package api

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ids"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/push"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

// InviteValidity is how long invite codes stay valid.
const InviteValidity = 30 * 24 * time.Hour

// publicURL is FRAME_PUBLIC_URL, or derived from the request.
func (s *Server) publicURL(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return s.cfg.PublicURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if p := r.Header.Get("X-Forwarded-Proto"); (p == "https" || p == "http") && s.trusted(parseAddr(r.RemoteAddr)) {
		scheme = p
	} else if s.cfg.TrustCloudflare && r.Header.Get("CF-Visitor") != "" && strings.Contains(r.Header.Get("CF-Visitor"), "https") {
		scheme = "https"
	}
	host := r.Host
	if host == "" || strings.ContainsAny(host, "/\\@ ") {
		host = "localhost"
	}
	return scheme + "://" + host
}

// InviteURL builds the shareable link for a code.
func InviteURL(base, server, code string) string {
	return base + "#s=" + url.QueryEscape(server) + "&c=" + code
}

// sealInviteCode encrypts an invite code at rest (AES-256-GCM, key derived
// from secrets.json) so the owner can list it while a database-only leak
// reveals nothing usable; lookups use the SHA-256 code hash.
func (s *Server) sealInviteCode(code string) ([]byte, error) {
	gcm, err := s.inviteAEAD()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(code), []byte("frame-invite-v1")), nil
}

func (s *Server) openInviteCode(b []byte) (string, bool) {
	gcm, err := s.inviteAEAD()
	if err != nil || len(b) < gcm.NonceSize() {
		return "", false
	}
	pt, err := gcm.Open(nil, b[:gcm.NonceSize()], b[gcm.NonceSize():], []byte("frame-invite-v1"))
	return string(pt), err == nil
}

func (s *Server) inviteAEAD() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.inviteKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// CreateInvite stores a new invite for userID and returns the code.
func (s *Server) CreateInvite(ctx context.Context, userID string) (code string, expires int64, err error) {
	code = ids.Token(16) // 128-bit, 22 chars base64url
	now := s.now()
	expires = now.Add(InviteValidity).UnixMilli()
	enc, err := s.sealInviteCode(code)
	if err != nil {
		return "", 0, err
	}
	err = store.InsertInvite(ctx, s.db.W, inviteHash(code), enc, userID, now.UnixMilli(), expires)
	return code, expires, err
}

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	code, expires, err := s.CreateInvite(r.Context(), a.ID())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code": code, "url": InviteURL(s.cfg.InviteBase, s.publicURL(r), code), "expires_at": expires,
	})
	return nil
}

type inviteView struct {
	Code      string `json:"code"`
	URL       string `json:"url"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
	Uses      int    `json:"uses"`
}

func (s *Server) handleListInvites(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	list, err := store.ListInvites(r.Context(), s.db.R, a.ID(), s.now().UnixMilli())
	if err != nil {
		return err
	}
	out := make([]inviteView, 0, len(list))
	base := s.publicURL(r)
	for _, inv := range list {
		code, ok := s.openInviteCode(inv.CodeEnc)
		if !ok {
			continue // pre-v1.1 rows or a rotated key: still redeemable, not listable
		}
		out = append(out, inviteView{Code: code, URL: InviteURL(s.cfg.InviteBase, base, code), CreatedAt: inv.CreatedAt,
			ExpiresAt: inv.ExpiresAt, Uses: inv.Uses})
	}
	writeJSON(w, http.StatusOK, map[string]any{"invites": out})
	return nil
}

func (s *Server) handleDeleteInvite(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	code := r.PathValue("code")
	if !validInviteCode(code) {
		return errNotFound("Invite not found")
	}
	ok, err := store.DeleteInvite(r.Context(), s.db.W, a.ID(), inviteHash(code))
	if err != nil {
		return err
	}
	if !ok {
		return errNotFound("Invite not found")
	}
	return noContent(w)
}

var errInviteNotFound = errNotFound("Invite not found or expired")

// lookupInvite resolves a member's (friend) invite from the path.
func (s *Server) lookupInvite(r *http.Request) (*store.Invite, error) {
	code := r.PathValue("code")
	if !validInviteCode(code) {
		return nil, errInviteNotFound
	}
	inv, err := store.InviteByHash(r.Context(), s.db.R, inviteHash(code), s.now().UnixMilli())
	if errors.Is(err, store.ErrNotFound) {
		return nil, errInviteNotFound
	}
	return inv, err
}

type inviteLookupView struct {
	Kind        string       `json:"kind"` // friend | server
	Inviter     *userSummary `json:"inviter"`
	ServerName  string       `json:"server_name"`
	CanRegister bool         `json:"can_register"`
}

func (s *Server) handleGetInvite(w http.ResponseWriter, r *http.Request) error {
	if err := s.limitIP(s.rl.invites, r); err != nil {
		return err
	}
	ctx := r.Context()
	code := r.PathValue("code")
	g, err := s.lookupGrant(ctx, code)
	if err != nil {
		return err
	}
	if g == nil {
		return errNotFound("Invite not found or expired")
	}
	set := s.live()
	v := inviteLookupView{Kind: "server", ServerName: set.Name, CanRegister: canRegisterWith(set, g)}
	if g.inviter != "" {
		rr := s.renderer(ctx, "")
		if err := rr.loadUsers(g.inviter); err != nil {
			return err
		}
		v.Kind, v.Inviter = "friend", rr.summary(g.inviter)
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

// handleRedeemInvite befriends the inviter — unless the inviter previously
// unfriended or blocked the redeemer, in which case it only creates a normal
// friend request the inviter must accept (an old link can't undo a removal).
func (s *Server) handleRedeemInvite(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	if err := s.limit(s.rl.invites, "redeem:"+a.ID()); err != nil {
		return err
	}
	inv, err := s.lookupInvite(r)
	if errors.Is(err, errInviteNotFound) {
		// A server invite only lets new people register; there is nobody to
		// befriend.
		if g, gerr := s.lookupGrant(r.Context(), r.PathValue("code")); gerr == nil && g != nil && g.serverInviteID != "" {
			return errBadRequest("This invite is for joining the server, and you're already a member")
		}
	}
	if err != nil {
		return err
	}
	me, inviter := a.ID(), inv.UserID
	if inviter == me {
		return errBadRequest("This is your own invite")
	}
	ctx := r.Context()
	var since int64
	var created, autoAccepted bool
	var fr *store.FriendRequest
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		if b, err := store.BlockedEither(ctx, tx, me, inviter); err != nil || b {
			if b {
				return errBlocked
			}
			return err
		}
		if at, err := store.FriendSince(ctx, tx, me, inviter); err == nil {
			since = at
			return nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		removed, err := store.RemovedBy(ctx, tx, inviter, me)
		if err != nil {
			return err
		}
		if removed {
			// Behave exactly like POST /friends/requests.
			if _, err := store.RequestBetween(ctx, tx, inviter, me); err == nil {
				autoAccepted = true
			} else if !errors.Is(err, store.ErrNotFound) {
				return err
			} else {
				if err := s.checkFriendLimit(ctx, tx, me); err != nil {
					return err
				}
				fr, err = store.InsertRequest(ctx, tx, &store.FriendRequest{ID: ids.At(s.now()), FromID: me, ToID: inviter, CreatedAt: s.now().UnixMilli()})
				if err != nil {
					return err
				}
				return store.CountInviteUse(ctx, tx, inviteHash(r.PathValue("code")))
			}
		}
		if err := s.checkFriendLimit(ctx, tx, me, inviter); err != nil {
			return err
		}
		created = true
		if since, err = store.AddFriendship(ctx, tx, me, inviter, s.now().UnixMilli()); err != nil {
			return err
		}
		return store.CountInviteUse(ctx, tx, inviteHash(r.PathValue("code")))
	})
	if err != nil {
		return err
	}
	if fr != nil {
		views, err := s.renderer(ctx, me).requests([]store.FriendRequest{*fr})
		if err != nil {
			return err
		}
		s.emit("friend.request", map[string]any{"request": views[0]}, inviter, me)
		s.notify(push.Notification{UserIDs: []string{inviter}, Pref: push.PrefFriends, CollapseID: "friend-" + me,
			Payload: push.Payload{Type: "friend_request", Title: a.User.DisplayName, Body: "wants to be your friend", PeerID: strp(me)}})
		writeJSON(w, http.StatusOK, map[string]any{"status": "requested", "friend": nil, "request": views[0]})
		return nil
	}
	if created || autoAccepted {
		s.announceFriendship(ctx, me, inviter)
		s.notify(push.Notification{UserIDs: []string{inviter}, Pref: push.PrefFriends,
			Payload: push.Payload{Type: "friend_accepted", Title: a.User.DisplayName, Body: "accepted your friend request", PeerID: strp(me)}})
	}
	rr := s.renderer(ctx, me)
	if err := rr.loadUsers(inviter); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "friends", "friend": friendView{User: rr.summary(inviter), Since: since}, "request": nil})
	return nil
}
