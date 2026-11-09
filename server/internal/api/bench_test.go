package api_test

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ids"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

// bigFeed builds a realistic large dataset: the viewer U has 200 friends who
// posted 50,000 moments in total (each to U plus ~3 other friends, so the
// feed table holds ~250k rows), with captions, reactions and views.
type bigFeed struct {
	e      *env
	u      *user
	cursor string // a cursor ~100 pages deep
}

func setupBigFeed(tb testing.TB) *bigFeed {
	e := newEnv(tb)
	u := e.register("Viewer")
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(1, 2))
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	const nFriends, nMoments = 200, 50_000
	friends := make([]string, nFriends)
	err := e.db.Tx(ctx, func(tx *sql.Tx) error {
		now := start.UnixMilli()
		for i := range friends {
			id := ids.At(start.Add(time.Duration(i) * time.Millisecond))
			friends[i] = id
			name := fmt.Sprintf("friend%03d", i)
			var avatar any
			if i%2 == 0 {
				avatar = ids.At(start.Add(time.Duration(i) * time.Millisecond))
				if _, err := tx.Exec(`INSERT INTO media(id, owner_id, content_type, bytes, width, height, created_at) VALUES (?,NULL,'image/jpeg',9000,512,512,?)`, avatar, now); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(`INSERT INTO users(id, username, display_name, avatar_media_id, created_at, updated_at) VALUES (?,?,?,?,?,?)`, id, name, name, avatar, now, now); err != nil {
				return err
			}
			if _, err := store.AddFriendship(ctx, tx, u.ID, id, now); err != nil {
				return err
			}
		}
		insMedia, _ := tx.Prepare(`INSERT INTO media(id, owner_id, content_type, bytes, width, height, created_at) VALUES (?,?,'image/jpeg',?,?,?,?)`)
		insMoment, _ := tx.Prepare(`INSERT INTO moments(id, author_id, client_id, kind, audience, media_id, thumb_id, local_date, caption_type, caption_text, caption_style, created_at)
			VALUES (?,?,?,'photo','all',?,?,?,?,?,?,?)`)
		insFeed, _ := tx.Prepare(`INSERT OR IGNORE INTO feed_entries(user_id, moment_id, author_id, viewed_at) VALUES (?,?,?,?)`)
		insReaction, _ := tx.Prepare(`INSERT INTO reactions(moment_id, user_id, emoji, created_at) VALUES (?,?,?,?)`)
		for i := 0; i < nMoments; i++ {
			at := start.Add(time.Duration(i) * 10 * time.Minute)
			ms := at.UnixMilli()
			author := friends[rng.IntN(nFriends)]
			mid, tid, id := ids.At(at), ids.At(at), ids.At(at)
			if _, err := insMedia.Exec(mid, author, 300_000, 1440, 1440, ms); err != nil {
				return err
			}
			if _, err := insMedia.Exec(tid, author, 30_000, 512, 512, ms); err != nil {
				return err
			}
			var ctype, ctext, cstyle any
			if i%3 == 0 {
				ctype, ctext, cstyle = "text", "caption number "+fmt.Sprint(i), "classic"
			}
			if _, err := insMoment.Exec(id, author, fmt.Sprintf("c-%d", i), mid, tid, at.Format(time.DateOnly), ctype, ctext, cstyle, ms); err != nil {
				return err
			}
			if _, err := insFeed.Exec(author, id, author, ms); err != nil {
				return err
			}
			var viewed any
			if rng.IntN(2) == 0 {
				viewed = ms + 1000
			}
			if _, err := insFeed.Exec(u.ID, id, author, viewed); err != nil {
				return err
			}
			for k := 0; k < 3; k++ {
				if _, err := insFeed.Exec(friends[rng.IntN(nFriends)], id, author, nil); err != nil {
					return err
				}
			}
			if i%10 == 0 {
				if _, err := insReaction.Exec(id, u.ID, "🔥", ms+2000); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		tb.Fatal(err)
	}
	e.db.W.Exec("ANALYZE")
	bf := &bigFeed{e: e, u: u}
	// Walk 100 pages deep to get a realistic deep cursor.
	q := store.FeedQuery{Me: u.ID, Limit: 3000}
	page, err := store.FeedIDs(ctx, e.db.R, q)
	if err != nil || len(page) != 3000 {
		tb.Fatalf("feed ids: %d %v", len(page), err)
	}
	bf.cursor = *encodeCursorForTest(page[len(page)-1])
	return bf
}

func encodeCursorForTest(id string) *string {
	// Mirrors api.encodeCursor (opaque to clients).
	s := base64URL("v1:" + id)
	return &s
}

func (bf *bigFeed) get(tb testing.TB, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("Authorization", "Bearer "+bf.u.Token)
	rec := httptest.NewRecorder()
	bf.e.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		tb.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
	}
	return rec
}

var sharedBigFeed *bigFeed

// persistentTB outlives a single benchmark run (benchmarks are invoked
// several times with growing b.N); its cleanups run at the end of TestMain.
type persistentTB struct{ testing.TB }

func (p persistentTB) Cleanup(f func()) { suiteCleanups = append(suiteCleanups, f) }
func (p persistentTB) TempDir() string {
	d, err := os.MkdirTemp("", "frame-bench-")
	if err != nil {
		p.Fatal(err)
	}
	suiteCleanups = append(suiteCleanups, func() { os.RemoveAll(d) })
	return d
}

func getBigFeed(b *testing.B) *bigFeed {
	if sharedBigFeed == nil {
		sharedBigFeed = setupBigFeed(persistentTB{b})
	}
	return sharedBigFeed
}

func BenchmarkFeedPage30(b *testing.B) {
	bf := getBigFeed(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bf.get(b, "/api/v1/moments?limit=30")
	}
}

func BenchmarkFeedPage30Deep(b *testing.B) {
	bf := getBigFeed(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bf.get(b, "/api/v1/moments?limit=30&before="+bf.cursor)
	}
}

func BenchmarkFeedPage30ByFriend(b *testing.B) {
	bf := getBigFeed(b)
	var friend string
	bf.e.db.R.QueryRow(`SELECT friend_id FROM friendships WHERE user_id = ? LIMIT 1`, bf.u.ID).Scan(&friend)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bf.get(b, "/api/v1/moments?limit=30&user_id="+friend)
	}
}

func BenchmarkLatestWithUnseen(b *testing.B) {
	bf := getBigFeed(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bf.get(b, "/api/v1/moments/latest")
	}
}

func BenchmarkFeedIDsQuery(b *testing.B) {
	bf := getBigFeed(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.FeedIDs(ctx, bf.e.db.R, store.FeedQuery{Me: bf.u.ID, Limit: 31}); err != nil {
			b.Fatal(err)
		}
	}
}

// TestFeedLatencyBudget enforces the < 10 ms budget for a 30-moment feed
// page (full handler: auth, queries, rendering, JSON) on the big dataset.
func TestFeedLatencyBudget(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("latency budget is measured without -short/-race")
	}
	bf := setupBigFeed(t)
	for _, path := range []string{"/api/v1/moments?limit=30", "/api/v1/moments?limit=30&before=" + bf.cursor, "/api/v1/moments/latest"} {
		var ds []time.Duration
		for i := 0; i < 200; i++ {
			t0 := time.Now()
			rec := bf.get(t, path)
			ds = append(ds, time.Since(t0))
			if i == 0 && path != "/api/v1/moments/latest" && len(rec.Body.Bytes()) < 1000 {
				t.Fatalf("suspiciously small page: %s", rec.Body)
			}
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		p50, p99 := ds[len(ds)/2], ds[len(ds)*99/100]
		t.Logf("%-60s p50 %v  p99 %v", path[:min(len(path), 60)], p50, p99)
		if p50 > 10*time.Millisecond {
			t.Errorf("%s p50 %v exceeds 10 ms", path, p50)
		}
	}
	// The planner must use the feed indexes (no full scans).
	rows, _ := bf.e.db.R.Query(`EXPLAIN QUERY PLAN SELECT f.moment_id FROM feed_entries f WHERE f.user_id = ?1 AND f.hidden = 0
		AND (f.author_id = ?1 OR EXISTS (SELECT 1 FROM friendships fr WHERE fr.user_id = ?1 AND fr.friend_id = f.author_id))
		ORDER BY f.moment_id DESC LIMIT 31`, bf.u.ID)
	defer rows.Close()
	for rows.Next() {
		var id, parent, nu int
		var detail string
		rows.Scan(&id, &parent, &nu, &detail)
		t.Logf("plan: %s", detail)
		if detail == "SCAN f" || detail == "USE TEMP B-TREE FOR ORDER BY" {
			t.Errorf("feed query plan regressed: %s", detail)
		}
	}
}
