package storage

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeS3 is a path-style S3 server for tests. It verifies every request's
// SigV4 Authorization header with its own canonicalisation (written
// independently of the client's signer, from the SignedHeaders list the
// client declares) and checks x-amz-content-sha256 against the body.
type fakeS3 struct {
	t         *testing.T
	accessKey string
	secret    string
	region    string
	bucket    string

	mu       sync.Mutex
	objects  map[string]fakeObject
	requests map[string]int // method → count
	gets     []string       // Range headers of GET requests
}

type fakeObject struct {
	data        []byte
	contentType string
}

func newFakeS3(t *testing.T, accessKey, secret, region, bucket string) (*fakeS3, *httptest.Server) {
	f := &fakeS3{t: t, accessKey: accessKey, secret: secret, region: region, bucket: bucket,
		objects: map[string]fakeObject{}, requests: map[string]int{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeS3) fail(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<?xml version=\"1.0\"?><Error><Code>%s</Code><Message>fake</Message></Error>", code)
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		f.fail(w, 400, "IncompleteBody")
		return
	}
	if code := f.verify(r, body); code != "" {
		f.fail(w, http.StatusForbidden, code)
		return
	}
	f.mu.Lock()
	f.requests[r.Method]++
	if r.Method == http.MethodGet {
		f.gets = append(f.gets, r.Header.Get("Range"))
	}
	f.mu.Unlock()

	prefix := "/" + f.bucket + "/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		f.fail(w, 404, "NoSuchBucket")
		return
	}
	key := strings.TrimPrefix(r.URL.Path, prefix)

	switch r.Method {
	case http.MethodPut:
		if r.ContentLength != int64(len(body)) {
			f.fail(w, 400, "IncompleteBody")
			return
		}
		f.mu.Lock()
		f.objects[key] = fakeObject{data: body, contentType: r.Header.Get("Content-Type")}
		f.mu.Unlock()
		w.Header().Set("ETag", `"fake"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodGet, http.MethodHead:
		f.mu.Lock()
		obj, ok := f.objects[key]
		f.mu.Unlock()
		if !ok {
			f.fail(w, 404, "NoSuchKey")
			return
		}
		if obj.contentType != "" {
			w.Header().Set("Content-Type", obj.contentType)
		}
		// ServeContent implements Range (206 + Content-Range) like S3.
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(obj.data))
	case http.MethodDelete:
		f.mu.Lock()
		delete(f.objects, key)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		f.fail(w, 405, "MethodNotAllowed")
	}
}

// verify returns an S3 error code, or "" if the signature is valid.
func (f *fakeS3) verify(r *http.Request, body []byte) string {
	authz := r.Header.Get("Authorization")
	rest, ok := strings.CutPrefix(authz, "AWS4-HMAC-SHA256 ")
	if !ok {
		return "AccessDenied"
	}
	fields := map[string]string{}
	for _, part := range strings.Split(rest, ", ") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return "AuthorizationHeaderMalformed"
		}
		fields[k] = v
	}
	cred := strings.Split(fields["Credential"], "/")
	if len(cred) != 5 || cred[0] != f.accessKey || cred[2] != f.region || cred[3] != "s3" || cred[4] != "aws4_request" {
		return "InvalidAccessKeyId"
	}
	amzDate := r.Header.Get("X-Amz-Date")
	when, err := time.Parse("20060102T150405Z", amzDate)
	if err != nil || when.Format("20060102") != cred[1] {
		return "AuthorizationHeaderMalformed"
	}
	payloadHash := r.Header.Get("X-Amz-Content-Sha256")
	sum := sha256.Sum256(body)
	if payloadHash != hex.EncodeToString(sum[:]) {
		return "XAmzContentSHA256Mismatch"
	}
	signed := strings.Split(fields["SignedHeaders"], ";")
	if !sort.StringsAreSorted(signed) {
		return "AuthorizationHeaderMalformed"
	}
	must := map[string]bool{"host": false, "x-amz-date": false, "x-amz-content-sha256": false}
	var hdrs strings.Builder
	for _, name := range signed {
		if _, ok := must[name]; ok {
			must[name] = true
		}
		var vals []string
		if name == "host" {
			vals = []string{r.Host}
		} else {
			vals = r.Header.Values(name)
		}
		for i, v := range vals {
			vals[i] = strings.Join(strings.Fields(v), " ")
		}
		hdrs.WriteString(name + ":" + strings.Join(vals, ",") + "\n")
	}
	for name, present := range must {
		if !present {
			return "AccessDenied: unsigned " + name
		}
	}
	// Range must be signed when present, so it cannot be altered in transit.
	if r.Header.Get("Range") != "" && !strings.Contains(";"+fields["SignedHeaders"]+";", ";range;") {
		return "AccessDenied"
	}
	creq := strings.Join([]string{
		r.Method,
		fakeEncodePath(r.URL.Path),
		fakeCanonicalQuery(r),
		hdrs.String(),
		fields["SignedHeaders"],
		payloadHash,
	}, "\n")
	creqSum := sha256.Sum256([]byte(creq))
	scope := cred[1] + "/" + cred[2] + "/s3/aws4_request"
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(creqSum[:])
	mac := func(k []byte, s string) []byte { m := hmac.New(sha256.New, k); m.Write([]byte(s)); return m.Sum(nil) }
	key := mac(mac(mac(mac([]byte("AWS4"+f.secret), cred[1]), cred[2]), "s3"), "aws4_request")
	want := hex.EncodeToString(mac(key, sts))
	if !hmac.Equal([]byte(want), []byte(fields["Signature"])) {
		return "SignatureDoesNotMatch"
	}
	return ""
}

// fakeEncodePath re-encodes the decoded request path the way S3 does.
func fakeEncodePath(p string) string { return fakeEncode(p, "-._~/") }

func fakeEncode(p, safe string) string {
	var b strings.Builder
	for _, c := range []byte(p) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.IndexByte(safe, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func fakeCanonicalQuery(r *http.Request) string {
	q := r.URL.Query()
	var parts []string
	for k, vs := range q {
		for _, v := range vs {
			parts = append(parts, fakeEncode(k, "-._~")+"="+fakeEncode(v, "-._~"))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "&")
}

func (f *fakeS3) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[method]
}

func (f *fakeS3) rawObject(key string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.objects[key].data
}

func (f *fakeS3) resetCounts() {
	f.mu.Lock()
	f.requests = map[string]int{}
	f.gets = nil
	f.mu.Unlock()
}
