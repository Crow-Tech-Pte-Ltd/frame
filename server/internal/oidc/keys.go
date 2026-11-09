package oidc

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxJWKSSize  = 1 << 20
	maxJWKSKeys  = 100
	minKeyBits   = 2048
	maxKeyBits   = 8192
	minCacheTTL  = 5 * time.Minute
	maxCacheTTL  = 24 * time.Hour
	defCacheTTL  = time.Hour
	fetchTimeout = 15 * time.Second
)

// keySet is one successfully fetched JWKS document.
type keySet struct {
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
	ttl       time.Duration
}

func (s *keySet) fresh(now time.Time) bool {
	return s != nil && now.Before(s.fetchedAt.Add(s.ttl))
}

// usable reports whether the set may still be served when a refresh fails:
// up to twice its TTL.
func (s *keySet) usable(now time.Time) bool {
	return s != nil && now.Before(s.fetchedAt.Add(2*s.ttl))
}

type fetchCall struct{ done chan struct{} }

// publicKey resolves kid. A fresh cached key is returned immediately.
// Otherwise (cold cache, expired set or unknown kid) one fetch is started —
// single-flight, and at most once per minRefetch — and the lookup is
// retried. When no fetch is possible, keys from a set younger than 2×TTL
// are still served.
func (v *Verifier) publicKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	waited := false
	for {
		v.mu.Lock()
		now := v.now()
		set := v.set
		var key *rsa.PublicKey
		if set != nil {
			key = set.keys[kid]
		}
		if key != nil && set.fresh(now) {
			v.mu.Unlock()
			return key, nil
		}
		if !waited {
			call := v.inflight
			if call == nil && (v.lastAttempt.IsZero() || now.Sub(v.lastAttempt) >= v.minRefetch) {
				call = v.startFetchLocked(ctx, now)
			}
			if call != nil {
				v.mu.Unlock()
				select {
				case <-call.done:
				case <-ctx.Done():
					return nil, fmt.Errorf("%w: %w", ErrInvalidToken, ctx.Err())
				}
				waited = true
				continue
			}
		}
		lastErr := v.lastErr
		v.mu.Unlock()

		if key != nil && set.usable(now) {
			return key, nil
		}
		if lastErr != nil {
			return nil, fmt.Errorf("%w: %w: %v", ErrInvalidToken, ErrKeysUnavailable, lastErr)
		}
		return nil, invalid("unknown signing key")
	}
}

// startFetchLocked starts a background fetch; v.mu must be held. The fetch
// is detached from the caller's cancellation (other callers may be waiting
// on it) but bounded by fetchTimeout; callers still honour their own ctx
// while waiting.
func (v *Verifier) startFetchLocked(ctx context.Context, now time.Time) *fetchCall {
	call := &fetchCall{done: make(chan struct{})}
	v.inflight = call
	v.lastAttempt = now
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
	go func() {
		defer cancel()
		set, err := v.fetch(fctx)
		v.mu.Lock()
		if err == nil {
			v.set = set
		}
		v.lastErr = err
		v.inflight = nil
		v.mu.Unlock()
		close(call.done)
	}()
	return call
}

func (v *Verifier) fetch(ctx context.Context) (*keySet, error) {
	u, err := url.Parse(v.provider.JWKSURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("jwks url must be https")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jwks fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks fetch: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSSize+1))
	if err != nil {
		return nil, fmt.Errorf("jwks read: %w", err)
	}
	if len(body) > maxJWKSSize {
		return nil, errors.New("jwks document too large")
	}
	keys, err := parseJWKS(body)
	if err != nil {
		return nil, err
	}
	return &keySet{keys: keys, fetchedAt: v.now(), ttl: cacheTTL(resp.Header)}, nil
}

// parseJWKS extracts usable RS256 signing keys. Keys that are not RSA, not
// for signatures, for another algorithm, shorter than 2048 bits or
// malformed are skipped; a document without any usable key is an error.
func parseJWKS(body []byte) (map[string]*rsa.PublicKey, error) {
	var doc struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, errors.New("malformed jwks")
	}
	keys := make(map[string]*rsa.PublicKey)
	for i, raw := range doc.Keys {
		if i >= maxJWKSKeys {
			break
		}
		var k struct {
			Kty string `json:"kty"`
			Use string `json:"use"`
			Alg string `json:"alg"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		}
		if json.Unmarshal(raw, &k) != nil {
			continue
		}
		if k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != "RS256") || k.Kid == "" {
			continue
		}
		if _, dup := keys[k.Kid]; dup {
			continue
		}
		pub, ok := rsaKey(k.N, k.E)
		if !ok {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("jwks has no usable RS256 keys")
	}
	return keys, nil
}

func rsaKey(nStr, eStr string) (*rsa.PublicKey, bool) {
	nb, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(nStr, "="))
	if err != nil || len(nb) == 0 {
		return nil, false
	}
	eb, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(eStr, "="))
	if err != nil || len(eb) == 0 || len(eb) > 4 {
		return nil, false
	}
	n := new(big.Int).SetBytes(nb)
	if bits := n.BitLen(); bits < minKeyBits || bits > maxKeyBits {
		return nil, false
	}
	var e int64
	for _, b := range eb {
		e = e<<8 | int64(b)
	}
	if e < 3 || e > 1<<31-1 || e%2 == 0 {
		return nil, false
	}
	return &rsa.PublicKey{N: n, E: int(e)}, true
}

// cacheTTL derives the key-set lifetime from Cache-Control max-age, clamped
// to [5 min, 24 h]; 1 h when absent, unparsable, no-store or no-cache.
func cacheTTL(h http.Header) time.Duration {
	ttl := time.Duration(-1)
	for _, line := range h.Values("Cache-Control") {
		for _, d := range strings.Split(line, ",") {
			d = strings.ToLower(strings.TrimSpace(d))
			switch {
			case d == "no-store" || d == "no-cache":
				return defCacheTTL
			case strings.HasPrefix(d, "max-age="):
				secs, err := strconv.ParseInt(strings.Trim(d[len("max-age="):], `"`), 10, 64)
				if err == nil && secs >= 0 && ttl < 0 {
					if secs > int64(maxCacheTTL/time.Second) {
						secs = int64(maxCacheTTL / time.Second)
					}
					ttl = time.Duration(secs) * time.Second
				}
			}
		}
	}
	switch {
	case ttl < 0:
		return defCacheTTL
	case ttl < minCacheTTL:
		return minCacheTTL
	case ttl > maxCacheTTL:
		return maxCacheTTL
	}
	return ttl
}

// httpsOnlyRedirects refuses redirects away from https and long chains.
func httpsOnlyRedirects(req *http.Request, via []*http.Request) error {
	if len(via) >= 3 {
		return errors.New("too many redirects")
	}
	if req.URL.Scheme != "https" {
		return errors.New("refusing non-https redirect")
	}
	return nil
}
