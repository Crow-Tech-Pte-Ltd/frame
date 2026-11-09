package ratelimit

import (
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"
)

func TestTokenBucket(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(10, time.Minute, 0).WithClock(func() time.Time { return now })
	for i := 0; i < 10; i++ {
		if ok, _ := l.Allow("k"); !ok {
			t.Fatalf("denied at %d", i)
		}
	}
	ok, wait := l.Allow("k")
	if ok || wait < 5*time.Second || wait > 7*time.Second {
		t.Fatalf("11th: %v %v", ok, wait)
	}
	if ok, _ := l.Allow("other"); !ok {
		t.Fatal("keys not independent")
	}
	now = now.Add(6 * time.Second)
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("no refill")
	}
	now = now.Add(-time.Hour) // clock steps back: no free tokens, no panic
	if ok, _ := l.Allow("k"); ok {
		t.Fatal("clock skew granted tokens")
	}
}

func TestSweepAndEviction(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(1, time.Second, 1).WithClock(func() time.Time { return now }).WithMaxKeys(100)
	for i := 0; i < 1000; i++ {
		l.Allow(fmt.Sprint(i))
	}
	if n := l.Len(); n > 100 {
		t.Fatalf("map grew to %d", n)
	}
	now = now.Add(2 * time.Second)
	l.Sweep()
	if l.Len() != 0 {
		t.Fatalf("sweep left %d", l.Len())
	}
	var nilL *Limiter
	if ok, _ := nilL.Allow("x"); !ok {
		t.Fatal("nil limiter must allow")
	}
}

func TestConcurrent(t *testing.T) {
	l := New(1000, time.Hour, 0)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if ok, _ := l.Allow("k"); ok {
					mu.Lock()
					allowed++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	if allowed < 1000 || allowed > 1001 {
		t.Fatalf("allowed %d", allowed)
	}
}

func TestIPKey(t *testing.T) {
	if IPKey(netip.MustParseAddr("2001:db8:1:2:3:4:5:6")) != IPKey(netip.MustParseAddr("2001:db8:1:2::9")) {
		t.Fatal("v6 /64 grouping")
	}
	if IPKey(netip.MustParseAddr("::ffff:1.2.3.4")) != "1.2.3.4" {
		t.Fatal("v4-mapped")
	}
}
