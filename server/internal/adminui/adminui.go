// Package adminui embeds the web admin panel (built from admin-ui/ with
// `bun run build`, output committed in dist/) so `go build` needs no
// Node/Bun toolchain, and serves it as a single-page app under /admin/.
package adminui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

//go:embed all:dist
var distFS embed.FS

type file struct {
	data  []byte
	ctype string
	etag  string
}

var (
	loadOnce sync.Once
	files    map[string]*file
)

var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json",
	".webmanifest": "application/manifest+json",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".jpg":         "image/jpeg",
	".ico":         "image/x-icon",
	".webp":        "image/webp",
	".woff2":       "font/woff2",
	".woff":        "font/woff",
	".txt":         "text/plain; charset=utf-8",
	".map":         "application/json",
}

func load() {
	files = map[string]*file{}
	_ = fs.WalkDir(distFS, "dist", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := distFS.ReadFile(p)
		if err != nil {
			return nil
		}
		ct, ok := contentTypes[strings.ToLower(path.Ext(p))]
		if !ok {
			ct = "application/octet-stream"
		}
		sum := sha256.Sum256(b)
		files[strings.TrimPrefix(p, "dist/")] = &file{data: b, ctype: ct, etag: `"` + base64.RawURLEncoding.EncodeToString(sum[:12]) + `"`}
		return nil
	})
}

// Has reports whether the embedded build contains rel (e.g. "index.html").
func Has(rel string) bool {
	loadOnce.Do(load)
	_, ok := files[rel]
	return ok
}

// Handler serves /admin/…: content-hashed /admin/assets/* immutably,
// other files with revalidation, and index.html (never cached) for every
// other path so client-side routes work on reload.
func Handler() http.Handler {
	loadOnce.Do(load)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/admin/")
		if rel != "" {
			rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
		}
		f, ok := files[rel]
		spaRoute := rel == "" || rel == "index.html" ||
			(!ok && !strings.HasPrefix(rel, "assets/") && !strings.Contains(path.Base(rel), "."))
		switch {
		case spaRoute:
			f = files["index.html"]
			if f == nil {
				http.Error(w, "admin panel not built", http.StatusNotFound)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
		case ok:
			if strings.HasPrefix(rel, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			w.Header().Set("ETag", f.etag)
		default:
			// A missing file, not a client-side route.
			http.Error(w, "404 not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", f.ctype)
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.data))
	})
}
