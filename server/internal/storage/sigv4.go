package storage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// AWS Signature Version 4, header-based authentication.
// https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv-create-signed-request.html
//
// The implementation follows the S3 flavour: the canonical URI is the
// URI-encoded path, encoded exactly once (S3 does not normalise or
// double-encode paths).

const (
	sigV4Algorithm = "AWS4-HMAC-SHA256"
	amzDateFormat  = "20060102T150405Z"
	// emptyPayloadHash is hex(sha256("")).
	emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// headers never included in the signature (hop-by-hop or set by transports
// and proxies after signing).
var unsignedHeaders = map[string]bool{
	"authorization":   true,
	"user-agent":      true,
	"x-amzn-trace-id": true,
	"expect":          true,
	"connection":      true,
	"content-length":  true,
}

// signV4 signs req in place: it sets X-Amz-Date (from t) and Authorization.
// Every header present in req.Header (except the unsigned set above) plus
// Host is signed, so callers set all headers before signing. payloadHash is
// the hex SHA-256 of the body (emptyPayloadHash for no body).
func signV4(req *http.Request, payloadHash, accessKey, secret, region, service string, t time.Time) {
	t = t.UTC()
	amzDate := t.Format(amzDateFormat)
	req.Header.Set("X-Amz-Date", amzDate)
	creq, signed := canonicalRequest(req, payloadHash)
	scope := credentialScope(t, region, service)
	sts := stringToSign(amzDate, scope, creq)
	sig := hex.EncodeToString(hmacSHA256(signingKey(secret, t, region, service), []byte(sts)))
	req.Header.Set("Authorization", sigV4Algorithm+" Credential="+accessKey+"/"+scope+
		", SignedHeaders="+signed+", Signature="+sig)
}

// canonicalRequest returns the SigV4 canonical request for req and the
// semicolon-separated list of signed header names.
func canonicalRequest(req *http.Request, payloadHash string) (creq, signedHeaders string) {
	path := req.URL.Path
	if path == "" {
		path = "/"
	}
	names, canonHeaders := canonicalHeaders(req)
	signedHeaders = strings.Join(names, ";")
	creq = strings.Join([]string{
		req.Method,
		awsURIEncode(path, true),
		canonicalQuery(req.URL.RawQuery),
		canonHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")
	return creq, signedHeaders
}

// canonicalHeaders returns the sorted lowercase header names to sign and the
// canonical header block ("name:value\n" per header).
func canonicalHeaders(req *http.Request) ([]string, string) {
	values := map[string][]string{}
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	values["host"] = []string{host}
	for name, vs := range req.Header {
		lname := strings.ToLower(name)
		if unsignedHeaders[lname] || lname == "host" {
			continue
		}
		values[lname] = append(values[lname], vs...)
	}
	names := make([]string, 0, len(values))
	for n := range values {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		vs := values[n]
		trimmed := make([]string, len(vs))
		for i, v := range vs {
			trimmed[i] = trimHeaderValue(v)
		}
		b.WriteString(n)
		b.WriteByte(':')
		b.WriteString(strings.Join(trimmed, ","))
		b.WriteByte('\n')
	}
	return names, b.String()
}

// trimHeaderValue trims surrounding whitespace and collapses internal runs of
// spaces/tabs to a single space, as SigV4 requires.
func trimHeaderValue(v string) string {
	return strings.Join(strings.Fields(v), " ")
}

// canonicalQuery sorts query parameters by encoded name, then value, and
// re-encodes them with the SigV4 rules. A parameter without "=" gets an empty
// value ("?lifecycle" → "lifecycle=").
func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	type kv struct{ k, v string }
	var pairs []kv
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		pairs = append(pairs, kv{awsURIEncode(unescapeQueryPart(k), false), awsURIEncode(unescapeQueryPart(v), false)})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.k + "=" + p.v
	}
	return strings.Join(out, "&")
}

func unescapeQueryPart(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
}

// awsURIEncode percent-encodes every byte except the unreserved set
// A-Z a-z 0-9 - . _ ~ (and '/' when keepSlash), using uppercase hex.
func awsURIEncode(s string, keepSlash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		case c == '/' && keepSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0x0f])
		}
	}
	return b.String()
}

// credentialScope is "<yyyymmdd>/<region>/<service>/aws4_request".
func credentialScope(t time.Time, region, service string) string {
	return t.UTC().Format("20060102") + "/" + region + "/" + service + "/aws4_request"
}

// stringToSign builds the SigV4 string to sign for a canonical request.
func stringToSign(amzDate, scope, creq string) string {
	sum := sha256.Sum256([]byte(creq))
	return sigV4Algorithm + "\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
}

// signingKey derives the SigV4 signing key for a date, region and service.
func signingKey(secret string, t time.Time, region, service string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), []byte(t.UTC().Format("20060102")))
	k = hmacSHA256(k, []byte(region))
	k = hmacSHA256(k, []byte(service))
	return hmacSHA256(k, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}
