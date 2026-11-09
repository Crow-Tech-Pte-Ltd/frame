package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testAccessKey = "AKIDFRAMETEST"
	testSecret    = "s3cr3t/FRAME+test/key"
	testBucket    = "frame-media"
)

func newTestS3(t *testing.T, secret string) (*fakeS3, *S3) {
	t.Helper()
	fake, srv := newFakeS3(t, testAccessKey, testSecret, "auto", testBucket)
	c, err := NewS3(S3Config{
		Endpoint:        srv.URL,
		Bucket:          testBucket,
		AccessKeyID:     testAccessKey,
		SecretAccessKey: secret,
		Prefix:          "frame/",
		HTTPClient:      srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return fake, c
}

func TestS3RoundTrip(t *testing.T) {
	_, c := newTestS3(t, testSecret)
	exerciseStorage(t, c)
}

func TestS3EncryptedRoundTrip(t *testing.T) {
	fake, c := newTestS3(t, testSecret)
	e, err := NewEncrypted(c, testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	exerciseStorage(t, e)

	// The bucket only holds ciphertext with an opaque content type.
	plain := bytes.Repeat([]byte("frame plaintext "), 5000)
	if err := e.Put(context.Background(), "secret", bytes.NewReader(plain), int64(len(plain)), "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	raw := fake.rawObject("frame/secret")
	if int64(len(raw)) != encCiphertextSize(int64(len(plain))) {
		t.Fatalf("stored %d bytes, want %d", len(raw), encCiphertextSize(int64(len(plain))))
	}
	if bytes.Contains(raw, []byte("frame plaintext")) {
		t.Fatal("plaintext visible in bucket")
	}
	fake.mu.Lock()
	ct := fake.objects["frame/secret"].contentType
	fake.mu.Unlock()
	if ct != "application/octet-stream" {
		t.Fatalf("bucket content type = %q", ct)
	}
}

func TestS3ObjectKeyAndContentType(t *testing.T) {
	fake, c := newTestS3(t, testSecret)
	if err := c.Put(context.Background(), "01JC3V9Z4X8Q2M7T5R1N6K0B3H", strings.NewReader("hi"), 2, "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	obj, ok := fake.objects["frame/01JC3V9Z4X8Q2M7T5R1N6K0B3H"]
	fake.mu.Unlock()
	if !ok || string(obj.data) != "hi" || obj.contentType != "image/jpeg" {
		t.Fatalf("object stored as %+v (found %v)", obj, ok)
	}
}

func TestS3WrongSecret(t *testing.T) {
	_, c := newTestS3(t, "not-the-secret")
	err := c.Put(context.Background(), "k", strings.NewReader("x"), 1, "")
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Fatalf("Put with wrong secret = %v, want 403 SignatureDoesNotMatch", err)
	}
	if strings.Contains(err.Error(), "not-the-secret") {
		t.Fatal("error leaks the secret")
	}
	if _, err := c.Stat(context.Background(), "k"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat with wrong secret = %v, want a non-NotFound error", err)
	}
}

func TestS3NotFound(t *testing.T) {
	_, c := newTestS3(t, testSecret)
	ctx := context.Background()
	if _, err := c.Open(ctx, "missing"); err != ErrNotFound {
		t.Fatalf("Open = %v, want ErrNotFound", err)
	}
	if _, err := c.Stat(ctx, "missing"); err != ErrNotFound {
		t.Fatalf("Stat = %v, want ErrNotFound", err)
	}
	if err := c.Delete(ctx, "missing"); err != nil {
		t.Fatalf("Delete = %v, want nil", err)
	}
}

func TestS3ServerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "<Error><Code>InternalError</Code></Error>"+strings.Repeat("x", 1<<20))
	}))
	defer srv.Close()
	c, err := NewS3(S3Config{Endpoint: srv.URL, Bucket: testBucket, AccessKeyID: "a", SecretAccessKey: "b"})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Put(context.Background(), "k", strings.NewReader("x"), 1, "")
	if err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "InternalError") {
		t.Fatalf("Put = %v", err)
	}
	if err := c.Delete(context.Background(), "k"); err == nil {
		t.Fatal("Delete on 500 succeeded")
	}
}

func TestS3ContextCancelled(t *testing.T) {
	_, c := newTestS3(t, testSecret)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Put(ctx, "k", strings.NewReader("x"), 1, ""); err == nil {
		t.Fatal("Put with cancelled context succeeded")
	}
}

func TestNewS3Validation(t *testing.T) {
	ok := S3Config{Endpoint: "https://acct.r2.cloudflarestorage.com", Bucket: "frame", AccessKeyID: "a", SecretAccessKey: "b", Prefix: "frame/"}
	if c, err := NewS3(ok); err != nil || c.region != "auto" {
		t.Fatalf("NewS3(valid) = %v, %v", c, err)
	}
	bad := []func(*S3Config){
		func(c *S3Config) { c.Endpoint = "" },
		func(c *S3Config) { c.Endpoint = "ftp://x" },
		func(c *S3Config) { c.Endpoint = "https://user:pw@host" },
		func(c *S3Config) { c.Bucket = "" },
		func(c *S3Config) { c.Bucket = "a/b" },
		func(c *S3Config) { c.Prefix = "../x/" },
		func(c *S3Config) { c.Prefix = "/abs/" },
		func(c *S3Config) { c.AccessKeyID = "" },
		func(c *S3Config) { c.SecretAccessKey = "" },
	}
	for i, mut := range bad {
		cfg := ok
		mut(&cfg)
		if _, err := NewS3(cfg); err == nil {
			t.Errorf("case %d: NewS3 accepted invalid config", i)
		}
	}
}

// Sequential reads of an S3 object (raw or encrypted) use a single GET;
// seeking elsewhere opens exactly one more.
func TestS3LazyRangedReads(t *testing.T) {
	fake, c := newTestS3(t, testSecret)
	e, _ := NewEncrypted(c, testMasterKey)
	ctx := context.Background()
	want := randomBytes(99, 5*encChunkSize+123)
	for _, s := range []Storage{c, e} {
		if err := s.Put(ctx, "lazy", bytes.NewReader(want), int64(len(want)), ""); err != nil {
			t.Fatal(err)
		}
		fake.resetCounts()
		obj, err := s.Open(ctx, "lazy")
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(obj)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("ReadAll: %v (equal=%v)", err, bytes.Equal(got, want))
		}
		if n := fake.count("GET"); n != 1 {
			t.Fatalf("%T: sequential read issued %d GETs, want 1", s, n)
		}
		if _, err := obj.Seek(3*encChunkSize+7, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 100)
		if _, err := io.ReadFull(obj, buf); err != nil || !bytes.Equal(buf, want[3*encChunkSize+7:][:100]) {
			t.Fatalf("read after seek: %v", err)
		}
		if n := fake.count("GET"); n != 2 {
			t.Fatalf("%T: after one seek %d GETs, want 2", s, n)
		}
		obj.Close()
	}
}

// http.ServeContent over S3 objects (raw and encrypted) serves exact ranges.
func TestS3ServeContentRanges(t *testing.T) {
	_, c := newTestS3(t, testSecret)
	e, _ := NewEncrypted(c, testMasterKey)
	ctx := context.Background()
	want := randomBytes(7, 3*encChunkSize+5)
	if err := e.Put(ctx, "video", bytes.NewReader(want), int64(len(want)), "video/mp4"); err != nil {
		t.Fatal(err)
	}
	if err := c.Put(ctx, "plainvideo", bytes.NewReader(want), int64(len(want)), "video/mp4"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		store Storage
		key   string
	}{{e, "video"}, {c, "plainvideo"}} {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			obj, err := tc.store.Open(r.Context(), tc.key)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			defer obj.Close()
			w.Header().Set("Content-Type", "video/mp4")
			http.ServeContent(w, r, "", time.Time{}, obj)
		})
		n := len(want)
		ranges := []struct {
			hdr        string
			start, end int // inclusive
		}{
			{"bytes=0-9", 0, 9},
			{"bytes=70000-140000", 70000, 140000},
			{"bytes=65530-65545", 65530, 65545},
			{fmt.Sprintf("bytes=%d-", n-10), n - 10, n - 1},
			{"bytes=-7", n - 7, n - 1},
			{fmt.Sprintf("bytes=%d-%d", 2*encChunkSize, 3*encChunkSize+4), 2 * encChunkSize, n - 1},
		}
		for _, rg := range ranges {
			req := httptest.NewRequest("GET", "/m/x", nil)
			req.Header.Set("Range", rg.hdr)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusPartialContent {
				t.Fatalf("%s %s: status %d: %s", tc.key, rg.hdr, rec.Code, rec.Body.String())
			}
			if wantCR := fmt.Sprintf("bytes %d-%d/%d", rg.start, rg.end, n); rec.Header().Get("Content-Range") != wantCR {
				t.Fatalf("%s %s: Content-Range %q, want %q", tc.key, rg.hdr, rec.Header().Get("Content-Range"), wantCR)
			}
			if !bytes.Equal(rec.Body.Bytes(), want[rg.start:rg.end+1]) {
				t.Fatalf("%s %s: body mismatch", tc.key, rg.hdr)
			}
		}
		// Whole object.
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/m/x", nil))
		if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), want) {
			t.Fatalf("%s full GET: status %d, equal=%v", tc.key, rec.Code, bytes.Equal(rec.Body.Bytes(), want))
		}
	}
}
