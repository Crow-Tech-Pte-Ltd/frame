// Package ratelimit implements in-memory token-bucket rate limiting keyed by
// arbitrary strings (IP, user id, identifier), with bounded memory.
package ratelimit

import (
	"math"
	"net/netip"
	"sync"
	"time"
)

// Limiter is a set of token buckets sharing one rate and burst.
type Limiter struct {
	rate  float64 // tokens per second
	burst float64
	max   int
	now   func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// DefaultMaxKeys bounds memory per limiter (~100 bytes per key).
const DefaultMaxKeys = 100_000

// New returns a limiter allowing n events per period with the given burst
// (burst ≤ 0 means burst = n).
func New(n int, period time.Duration, burst int) *Limiter {
	if burst <= 0 {
		burst = n
	}
	return &Limiter{
		rate:    float64(n) / period.Seconds(),
		burst:   float64(burst),
		max:     DefaultMaxKeys,
		now:     time.Now,
		buckets: make(map[string]*bucket),
	}
}

// WithClock overrides the clock (tests).
func (l *Limiter) WithClock(now func() time.Time) *Limiter { l.now = now; return l }

// WithMaxKeys overrides the key cap (tests).
func (l *Limiter) WithMaxKeys(n int) *Limiter { l.max = n; return l }

// Allow consumes one token for key. When denied it returns how long until a
// token becomes available.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	return l.AllowN(key, 1)
}

// AllowN consumes n tokens for key.
func (l *Limiter) AllowN(key string, n float64) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= l.max {
			l.evictLocked(now)
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else {
		if elapsed := now.Sub(b.last); elapsed > 0 { // ignore clocks stepping back
			b.tokens = math.Min(l.burst, b.tokens+elapsed.Seconds()*l.rate)
			b.last = now
		}
	}
	if b.tokens >= n {
		b.tokens -= n
		return true, 0
	}
	wait := time.Duration((n - b.tokens) / l.rate * float64(time.Second))
	return false, wait
}

// Sweep drops buckets that have refilled completely (equivalent to absent).
func (l *Limiter) Sweep() {
	if l == nil {
		return
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)
}

func (l *Limiter) sweepLocked(now time.Time) {
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.rate >= l.burst {
			delete(l.buckets, k)
		}
	}
}

func (l *Limiter) evictLocked(now time.Time) {
	l.sweepLocked(now)
	if len(l.buckets) < l.max {
		return
	}
	// Still full (e.g. a flood of distinct keys): drop ~10% arbitrary
	// entries. Map iteration order is randomised, so this is fair enough.
	drop := max(1, l.max/10)
	for k := range l.buckets {
		delete(l.buckets, k)
		if drop--; drop == 0 {
			break
		}
	}
}

// Len returns the number of tracked keys.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// IPKey returns the limiter key for a client address: the address itself for
// IPv4, the /64 prefix for IPv6 (one subscriber typically owns a /64).
func IPKey(a netip.Addr) string {
	a = a.Unmap()
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.String()
	}
	return a.String()
}
