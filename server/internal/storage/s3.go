package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// S3Config configures an S3-compatible bucket (AWS S3, Cloudflare R2, MinIO…).
type S3Config struct {
	// Endpoint is the service base URL, e.g.
	// https://<accountid>.r2.cloudflarestorage.com.
	Endpoint string
	Bucket   string
	// Region is the signing region; "" or "auto" signs with "auto" (R2).
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	// Prefix is prepended to every key, e.g. "frame/".
	Prefix string
	// HTTPClient defaults to a client with a 60 s timeout.
	HTTPClient *http.Client
	// Now defaults to time.Now (used for request signing).
	Now func() time.Time
}

// S3 is a minimal S3 client: PUT, GET (ranged), HEAD and DELETE of single
// objects, path-style (<endpoint>/<bucket>/<prefix><key>), signed with SigV4.
type S3 struct {
	scheme, host, basePath string
	bucket, prefix         string
	region                 string
	accessKey, secret      string
	client                 *http.Client
	now                    func() time.Time
}

var _ Storage = (*S3)(nil)

var (
	bucketRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{1,62}$`)
	prefixRe = regexp.MustCompile(`^[A-Za-z0-9._/-]{0,256}$`)
)

// maxErrorBody bounds how much of an error response is read.
const maxErrorBody = 4 << 10

// NewS3 validates cfg and returns a client.
func NewS3(cfg S3Config) (*S3, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("storage: s3 endpoint must be an http(s) URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("storage: s3 endpoint must not contain credentials, query or fragment")
	}
	if !bucketRe.MatchString(cfg.Bucket) || strings.Contains(cfg.Bucket, "..") {
		return nil, errors.New("storage: invalid s3 bucket name")
	}
	if !prefixRe.MatchString(cfg.Prefix) || strings.HasPrefix(cfg.Prefix, "/") || strings.Contains(cfg.Prefix, "..") || strings.Contains(cfg.Prefix, "//") {
		return nil, errors.New("storage: invalid s3 prefix")
	}
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, errors.New("storage: s3 access key id and secret are required")
	}
	region := cfg.Region
	if region == "" {
		region = "auto"
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &S3{
		scheme:    u.Scheme,
		host:      u.Host,
		basePath:  strings.TrimSuffix(u.Path, "/"),
		bucket:    cfg.Bucket,
		prefix:    cfg.Prefix,
		region:    region,
		accessKey: cfg.AccessKeyID,
		secret:    cfg.SecretAccessKey,
		client:    client,
		now:       now,
	}, nil
}

// objectURL is the path-style URL for key. The path is pre-encoded with the
// SigV4 rules so the bytes on the wire match the canonical URI.
func (c *S3) objectURL(key string) string {
	path := c.basePath + "/" + c.bucket + "/" + c.prefix + key
	return c.scheme + "://" + c.host + awsURIEncode(path, true)
}

// do signs and sends one request. body (may be nil) is hashed, rewound and
// streamed with an exact Content-Length.
func (c *S3) do(ctx context.Context, method, key string, body io.ReadSeeker, size int64, hdr http.Header) (*http.Response, error) {
	if !ValidKey(key) {
		return nil, ErrInvalidKey
	}
	payloadHash := emptyPayloadHash
	if body != nil {
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("storage: rewind body: %w", err)
		}
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(ctxReader{ctx, body}, size+1))
		if err != nil {
			return nil, fmt.Errorf("storage: hash body: %w", err)
		}
		if n != size {
			return nil, fmt.Errorf("storage: body is %d bytes, want %d", n, size)
		}
		payloadHash = hex.EncodeToString(h.Sum(nil))
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("storage: rewind body: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.objectURL(key), nil)
	if err != nil {
		return nil, fmt.Errorf("storage: build request: %w", err)
	}
	if body != nil {
		if size == 0 {
			req.Body = http.NoBody
		} else {
			req.Body = io.NopCloser(io.LimitReader(body, size))
			req.GetBody = func() (io.ReadCloser, error) {
				if _, err := body.Seek(0, io.SeekStart); err != nil {
					return nil, err
				}
				return io.NopCloser(io.LimitReader(body, size)), nil
			}
		}
		req.ContentLength = size
	}
	for k, vs := range hdr {
		req.Header[k] = vs
	}
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	signV4(req, payloadHash, c.accessKey, c.secret, c.region, "s3", c.now())
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("storage: s3 %s %s: %w", method, key, err)
	}
	return resp, nil
}

// s3Error drains (bounded) and closes resp, and describes the failure. It
// includes the S3 error code when present but never credentials.
func s3Error(method, key string, resp *http.Response) error {
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		return ErrNotFound
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	code := ""
	if i := strings.Index(string(b), "<Code>"); i >= 0 {
		rest := string(b[i+len("<Code>"):])
		if j := strings.Index(rest, "</Code>"); j >= 0 && j <= 64 {
			code = rest[:j]
		}
	}
	if code != "" {
		return fmt.Errorf("storage: s3 %s %s: status %d (%s)", method, key, resp.StatusCode, code)
	}
	return fmt.Errorf("storage: s3 %s %s: status %d", method, key, resp.StatusCode)
}

func drainClose(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
	resp.Body.Close()
}

// Put uploads body with a single PUT.
func (c *S3) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, contentType string) error {
	if size < 0 {
		return errors.New("storage: negative size")
	}
	hdr := http.Header{}
	if contentType != "" {
		hdr.Set("Content-Type", contentType)
	}
	resp, err := c.do(ctx, http.MethodPut, key, body, size, hdr)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		if err := s3Error("PUT", key, resp); err != ErrNotFound {
			return err
		}
		return fmt.Errorf("storage: s3 PUT %s: status 404 (bucket missing?)", key)
	}
	drainClose(resp)
	return nil
}

// Stat issues a HEAD request.
func (c *S3) Stat(ctx context.Context, key string) (int64, error) {
	resp, err := c.do(ctx, http.MethodHead, key, nil, 0, nil)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, s3Error("HEAD", key, resp)
	}
	drainClose(resp)
	if resp.ContentLength < 0 {
		return 0, fmt.Errorf("storage: s3 HEAD %s: missing Content-Length", key)
	}
	return resp.ContentLength, nil
}

// Delete removes key; 404 is treated as success.
func (c *S3) Delete(ctx context.Context, key string) error {
	resp, err := c.do(ctx, http.MethodDelete, key, nil, 0, nil)
	if err != nil {
		return err
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusAccepted, http.StatusNotFound:
		drainClose(resp)
		return nil
	}
	return s3Error("DELETE", key, resp)
}

// Open returns a lazy object: it HEADs for the size now and issues ranged
// GETs on Read (from the current position to the end of the object).
func (c *S3) Open(ctx context.Context, key string) (Object, error) {
	size, err := c.Stat(ctx, key)
	if err != nil {
		return nil, err
	}
	return &s3Object{c: c, ctx: ctx, key: key, size: size}, nil
}

// s3Object streams an object with ranged GETs. Seeking only moves the
// position; the next Read opens a new GET if the stream is elsewhere.
type s3Object struct {
	c    *S3
	ctx  context.Context
	key  string
	size int64

	pos     int64         // logical read position
	body    io.ReadCloser // current GET body (nil if none)
	bodyPos int64         // object offset of the next byte in body
	closed  bool
}

func (o *s3Object) Size() int64 { return o.size }

func (o *s3Object) Read(p []byte) (int, error) {
	if o.closed {
		return 0, errors.New("storage: read on closed object")
	}
	if o.pos >= o.size {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	if o.body == nil || o.bodyPos != o.pos {
		if err := o.openAt(o.pos); err != nil {
			return 0, err
		}
	}
	n, err := o.body.Read(p)
	o.pos += int64(n)
	o.bodyPos += int64(n)
	if err == io.EOF {
		o.closeBody()
		if o.pos < o.size {
			if n > 0 {
				return n, nil
			}
			return 0, io.ErrUnexpectedEOF
		}
		return n, io.EOF
	}
	if err != nil {
		o.closeBody()
	}
	return n, err
}

// openAt starts a GET for bytes [off, size).
func (o *s3Object) openAt(off int64) error {
	o.closeBody()
	hdr := http.Header{}
	hdr.Set("Range", "bytes="+strconv.FormatInt(off, 10)+"-"+strconv.FormatInt(o.size-1, 10))
	resp, err := o.c.do(o.ctx, http.MethodGet, o.key, nil, 0, hdr)
	if err != nil {
		return err
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		start, ok := contentRangeStart(resp.Header.Get("Content-Range"))
		if !ok || start != off {
			drainClose(resp)
			return fmt.Errorf("storage: s3 GET %s: unexpected Content-Range", o.key)
		}
	case http.StatusOK:
		// Some servers ignore Range for a whole-object request.
		if off != 0 {
			drainClose(resp)
			return fmt.Errorf("storage: s3 GET %s: server ignored Range", o.key)
		}
	default:
		return s3Error("GET", o.key, resp)
	}
	o.body = resp.Body
	o.bodyPos = off
	return nil
}

// contentRangeStart parses the first byte offset of "bytes a-b/total".
func contentRangeStart(v string) (int64, bool) {
	rest, ok := strings.CutPrefix(v, "bytes ")
	if !ok {
		return 0, false
	}
	a, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(a, 10, 64)
	return n, err == nil && n >= 0
}

func (o *s3Object) closeBody() {
	if o.body != nil {
		o.body.Close()
		o.body = nil
	}
}

func (o *s3Object) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = o.pos + offset
	case io.SeekEnd:
		abs = o.size + offset
	default:
		return 0, errors.New("storage: invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("storage: negative position")
	}
	o.pos = abs
	return abs, nil
}

func (o *s3Object) Close() error {
	o.closeBody()
	o.closed = true
	return nil
}
