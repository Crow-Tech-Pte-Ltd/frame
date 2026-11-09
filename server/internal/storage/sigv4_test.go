package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Official AWS Signature V4 test suite (subset), copied from
// github.com/boto/botocore tests/unit/auth/aws4_testsuite (Apache-2.0, see
// testdata/sigv4/LICENSE and NOTICE). Each case has the raw request (.req),
// the expected canonical request (.creq), string to sign (.sts) and
// Authorization header (.authz).
const (
	suiteAccessKey = "AKIDEXAMPLE"
	suiteSecret    = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	suiteRegion    = "us-east-1"
	suiteService   = "service"
)

// parseSuiteRequest parses a test-suite .req file into an http.Request and
// its body.
func parseSuiteRequest(t *testing.T, raw string) (*http.Request, []byte) {
	t.Helper()
	head, body, _ := strings.Cut(raw, "\n\n")
	lines := strings.Split(head, "\n")
	reqLine, ok := strings.CutSuffix(lines[0], " HTTP/1.1")
	if !ok {
		t.Fatalf("bad request line %q", lines[0])
	}
	method, target, ok := strings.Cut(reqLine, " ")
	if !ok {
		t.Fatalf("bad request line %q", lines[0])
	}
	req := &http.Request{Method: method, Header: http.Header{}}
	for _, l := range lines[1:] {
		name, value, ok := strings.Cut(l, ":")
		if !ok {
			t.Fatalf("bad header line %q", l)
		}
		if strings.EqualFold(name, "host") {
			req.Host = value
			continue
		}
		req.Header.Add(name, value)
	}
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse target %q: %v", target, err)
	}
	u.Host = req.Host
	u.Scheme = "https"
	req.URL = u
	return req, []byte(body)
}

func readVector(t *testing.T, dir, name, ext string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name+"."+ext))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSigV4OfficialSuite(t *testing.T) {
	root := filepath.Join("testdata", "sigv4")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	cases := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		dir := filepath.Join(root, name)
		t.Run(name, func(t *testing.T) {
			req, body := parseSuiteRequest(t, readVector(t, dir, name, "req"))
			wantCreq := readVector(t, dir, name, "creq")
			wantSts := readVector(t, dir, name, "sts")
			wantAuthz := readVector(t, dir, name, "authz")

			when, err := time.Parse(amzDateFormat, req.Header.Get("X-Amz-Date"))
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(body)
			payloadHash := hex.EncodeToString(sum[:])

			creq, _ := canonicalRequest(req, payloadHash)
			if creq != wantCreq {
				t.Fatalf("canonical request:\n%s\nwant:\n%s", creq, wantCreq)
			}
			sts := stringToSign(when.Format(amzDateFormat), credentialScope(when, suiteRegion, suiteService), creq)
			if sts != wantSts {
				t.Fatalf("string to sign:\n%s\nwant:\n%s", sts, wantSts)
			}
			signV4(req, payloadHash, suiteAccessKey, suiteSecret, suiteRegion, suiteService, when)
			if got := req.Header.Get("Authorization"); got != wantAuthz {
				t.Fatalf("authorization:\n%s\nwant:\n%s", got, wantAuthz)
			}
		})
		cases++
	}
	if cases < 15 {
		t.Fatalf("only %d test-suite cases found", cases)
	}
}

// From the AWS IAM docs ("derive a signing key") example.
func TestSigV4SigningKey(t *testing.T) {
	when := time.Date(2015, 8, 30, 0, 0, 0, 0, time.UTC)
	got := hex.EncodeToString(signingKey(suiteSecret, when, "us-east-1", "iam"))
	if want := "c4afb1cc5771d871763a393e44b703571b55cc28424d1a5e86da6ed3c154a4b9"; got != want {
		t.Fatalf("signing key = %s, want %s", got, want)
	}
}

// The worked examples from the Amazon S3 API reference ("Signature
// Calculations for the Authorization Header: Transferring Payload in a Single
// Chunk", examplebucket, 20130524T000000Z).
func TestSigV4S3DocumentationExamples(t *testing.T) {
	const (
		accessKey = "AKIAIOSFODNN7EXAMPLE"
		secret    = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
		host      = "examplebucket.s3.amazonaws.com"
	)
	when := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	putBody := "Welcome to Amazon S3."
	putSum := sha256.Sum256([]byte(putBody))
	putHash := hex.EncodeToString(putSum[:])

	tests := []struct {
		name        string
		method      string
		target      string
		headers     [][2]string
		payloadHash string
		wantCreq    string // optional
		wantSig     string
	}{
		{
			name:        "GET object with Range",
			method:      "GET",
			target:      "/test.txt",
			headers:     [][2]string{{"Range", "bytes=0-9"}},
			payloadHash: emptyPayloadHash,
			wantCreq: "GET\n/test.txt\n\nhost:examplebucket.s3.amazonaws.com\nrange:bytes=0-9\n" +
				"x-amz-content-sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\n" +
				"x-amz-date:20130524T000000Z\n\nhost;range;x-amz-content-sha256;x-amz-date\n" +
				"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			wantSig: "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
		},
		{
			name:   "PUT object",
			method: "PUT",
			target: "/test$file.text",
			headers: [][2]string{
				{"Date", "Fri, 24 May 2013 00:00:00 GMT"},
				{"X-Amz-Storage-Class", "REDUCED_REDUNDANCY"},
			},
			payloadHash: putHash,
			wantCreq: "PUT\n/test%24file.text\n\ndate:Fri, 24 May 2013 00:00:00 GMT\n" +
				"host:examplebucket.s3.amazonaws.com\n" +
				"x-amz-content-sha256:44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072\n" +
				"x-amz-date:20130524T000000Z\nx-amz-storage-class:REDUCED_REDUNDANCY\n\n" +
				"date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class\n" +
				"44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072",
			wantSig: "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd",
		},
		{
			name:        "GET bucket lifecycle",
			method:      "GET",
			target:      "/?lifecycle",
			payloadHash: emptyPayloadHash,
			wantCreq: "GET\n/\nlifecycle=\nhost:examplebucket.s3.amazonaws.com\n" +
				"x-amz-content-sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\n" +
				"x-amz-date:20130524T000000Z\n\nhost;x-amz-content-sha256;x-amz-date\n" +
				"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			wantSig: "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543",
		},
		{
			name:        "GET bucket (list objects)",
			method:      "GET",
			target:      "/?max-keys=2&prefix=J",
			payloadHash: emptyPayloadHash,
			wantSig:     "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse("https://" + host + tc.target)
			if err != nil {
				t.Fatal(err)
			}
			req := &http.Request{Method: tc.method, URL: u, Host: host, Header: http.Header{}}
			for _, h := range tc.headers {
				req.Header.Set(h[0], h[1])
			}
			req.Header.Set("X-Amz-Content-Sha256", tc.payloadHash)
			req.Header.Set("X-Amz-Date", when.Format(amzDateFormat))
			if tc.wantCreq != "" {
				if creq, _ := canonicalRequest(req, tc.payloadHash); creq != tc.wantCreq {
					t.Fatalf("canonical request:\n%s\nwant:\n%s", creq, tc.wantCreq)
				}
			}
			signV4(req, tc.payloadHash, accessKey, secret, "us-east-1", "s3", when)
			authz := req.Header.Get("Authorization")
			if !strings.HasPrefix(authz, "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=") {
				t.Fatalf("unexpected authorization prefix: %s", authz)
			}
			if !strings.HasSuffix(authz, ", Signature="+tc.wantSig) {
				t.Fatalf("authorization = %s\nwant signature %s", authz, tc.wantSig)
			}
		})
	}
}

func TestAWSURIEncode(t *testing.T) {
	tests := []struct {
		in        string
		keepSlash bool
		want      string
	}{
		{"/a b/c", true, "/a%20b/c"},
		{"/a b/c", false, "%2Fa%20b%2Fc"},
		{"-._~AZaz09", false, "-._~AZaz09"},
		{"$&+,:;=?@", false, "%24%26%2B%2C%3A%3B%3D%3F%40"},
		{"ሴ", false, "%E1%88%B4"},
	}
	for _, tc := range tests {
		if got := awsURIEncode(tc.in, tc.keepSlash); got != tc.want {
			t.Errorf("awsURIEncode(%q, %v) = %q, want %q", tc.in, tc.keepSlash, got, tc.want)
		}
	}
}
