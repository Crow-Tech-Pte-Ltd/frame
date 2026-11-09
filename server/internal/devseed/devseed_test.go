package devseed

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/api"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/config"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/db"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/media"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/storage"
)

func TestSeedIdempotent(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.Load(config.Source{Overrides: map[string]string{"FRAME_DATA_DIR": dir, "FRAME_PUSH_RELAY": "", "FRAME_REGISTRATION": "open"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Dev = true
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(dir, "frame.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	st, _ := storage.NewLocal(filepath.Join(dir, "media"))
	clock := &Clock{}
	srv, err := api.New(api.Deps{Config: cfg, DB: d, Storage: st, MediaKey: bytes.Repeat([]byte{1}, 32), Now: clock.Now,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if err := Seed(ctx, d, srv.Handler(), clock); err != nil {
		t.Fatal(err)
	}
	count := func(table string) int {
		var n int
		d.R.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n)
		return n
	}
	want := map[string]int{"users": 3, "moments": 12, "friendships": 6, "messages": 6}
	for tbl, n := range want {
		if got := count(tbl); got != n {
			t.Errorf("%s = %d, want %d", tbl, got, n)
		}
	}
	if count("reactions") < 5 || count("media") != 27 { // 12×2 + 3 avatars
		t.Errorf("reactions %d media %d", count("reactions"), count("media"))
	}
	if err := Seed(ctx, d, srv.Handler(), clock); err != nil {
		t.Fatal(err)
	}
	for tbl, n := range want {
		if got := count(tbl); got != n {
			t.Errorf("after reseed %s = %d", tbl, got)
		}
	}
	var phone string
	d.R.QueryRow("SELECT phone FROM users WHERE username = 'bob'").Scan(&phone)
	if phone != "+15550000002" {
		t.Fatal(phone)
	}
	// alice is the dev server's admin (and only alice).
	var admins string
	d.R.QueryRow("SELECT group_concat(username) FROM users WHERE is_admin = 1").Scan(&admins)
	if admins != AdminUser {
		t.Fatalf("admins = %q", admins)
	}
}

func TestGeneratedImages(t *testing.T) {
	for i := 0; i < 12; i++ {
		b := PhotoJPEG(i, 64)
		if media.Sniff(b) != media.JPEG {
			t.Fatal("not a jpeg")
		}
		if w, h, ok := media.JPEGSize(b); !ok || w != 64 || h != 64 {
			t.Fatal("size")
		}
	}
	if media.Sniff(DemoMP4()) != media.MP4 {
		t.Fatal("demo mp4")
	}
}
