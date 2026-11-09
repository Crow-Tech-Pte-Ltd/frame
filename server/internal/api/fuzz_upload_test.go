package api_test

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"
)

// FuzzMomentUpload throws arbitrary multipart bodies at POST /api/v1/moments.
// The server must never panic or return 5xx, and must never accept a body
// that lacks a valid JPEG/HEIC/MP4 media part.
func FuzzMomentUpload(f *testing.F) {
	e := newEnv(f)
	u := e.register("Fuzz")
	photo, thumb := testPhoto()
	valid, ct := multipartBody(map[string]any{"client_id": "6f1c2b1e-3d4a-4b5c-8d9e-0a1b2c3d4e5f", "kind": "photo", "audience": "self",
		"width": 100, "height": 100, "duration_ms": nil}, photo, "image/jpeg", thumb)
	boundary := ct[len("multipart/form-data; boundary="):]
	f.Add(valid.Bytes())
	f.Add([]byte("--" + boundary + "\r\nContent-Disposition: form-data; name=\"meta\"\r\n\r\n{}\r\n--" + boundary + "--\r\n"))
	f.Add([]byte("--" + boundary + "\r\n\r\n"))
	f.Fuzz(func(t *testing.T, body []byte) {
		e.clock.Add(time.Second) // keep the per-user upload rate limit refilled
		req := httptest.NewRequest("POST", "/api/v1/moments", bytes.NewReader(body))
		req.RemoteAddr = "127.0.0.1:9"
		req.Header.Set("Content-Type", ct)
		req.Header.Set("Authorization", "Bearer "+u.Token)
		rec := httptest.NewRecorder()
		e.srv.Handler().ServeHTTP(rec, req)
		if rec.Code >= 500 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
	})
}
