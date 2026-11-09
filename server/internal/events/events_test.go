package events

import (
	"sync"
	"testing"
)

func TestHubFanOutAndClose(t *testing.T) {
	h := NewHub()
	a1, a2, b := h.Subscribe("a", "s1"), h.Subscribe("a", "s2"), h.Subscribe("b", "s3")
	ev, _ := New("typing", map[string]string{"peer_id": "x&y"})
	if string(ev.Encode()) != "event: typing\ndata: {\"peer_id\":\"x&y\"}\n\n" {
		t.Fatalf("encode: %q", ev.Encode())
	}
	h.Publish("a", ev)
	if (<-a1.C).Type != "typing" || (<-a2.C).Type != "typing" {
		t.Fatal("fan-out")
	}
	select {
	case <-b.C:
		t.Fatal("leaked to other user")
	default:
	}
	h.CloseSession("a", "s1")
	<-a1.Done
	if h.Count("a") != 1 {
		t.Fatal("session close")
	}
	h.Unsubscribe(a1) // idempotent after close
	h.CloseUser("b")
	<-b.Done
	h.Close()
	<-a2.Done
	if h.Subscribe("a", "s4") != nil {
		t.Fatal("subscribe after close")
	}
}

func TestSlowConsumerDropped(t *testing.T) {
	h := NewHub()
	h.BufSize = 2
	s := h.Subscribe("u", "s")
	ev, _ := New("x", 1)
	for i := 0; i < 5; i++ {
		h.Publish("u", ev) // never blocks
	}
	<-s.Done
	if h.Count("u") != 0 {
		t.Fatal("slow consumer not dropped")
	}
}

func TestPerUserCap(t *testing.T) {
	h := NewHub()
	h.MaxPer = 3
	var subs []*Sub
	for i := 0; i < 5; i++ {
		subs = append(subs, h.Subscribe("u", "s"))
	}
	if h.Count("u") != 3 {
		t.Fatalf("count %d", h.Count("u"))
	}
}

func TestConcurrentPublish(t *testing.T) {
	h := NewHub()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := h.Subscribe("u", "s")
			ev, _ := New("x", 1)
			for j := 0; j < 100; j++ {
				h.Publish("u", ev)
				select {
				case <-s.C:
				default:
				}
			}
			h.Unsubscribe(s)
		}()
	}
	wg.Wait()
}
