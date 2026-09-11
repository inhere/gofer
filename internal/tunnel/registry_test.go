package tunnel

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRegistryBeginConsume(t *testing.T) {
	r := NewRegistry()
	b := Binding{WorkerID: "w"}
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		p, n := r.Begin(b, time.Minute)
		if !strings.HasPrefix(p.TunnelID(), "t-") || len(p.TunnelID()) != 14 {
			t.Fatal(p.TunnelID())
		}
		if len(n) != 64 {
			t.Fatal(len(n))
		}
		if seen[n] {
			t.Fatal("duplicate")
		}
		seen[n] = true
		got, ok := r.Consume(n, time.Now())
		if !ok || got.WorkerID != "w" {
			t.Fatal(ok, got)
		}
		if _, ok = r.Consume(n, time.Now()); ok {
			t.Fatal("reconsume")
		}
	}
}

func TestRegistryExpiryAndWait(t *testing.T) {
	r := NewRegistry()
	p, n := r.Begin(Binding{}, time.Millisecond)
	if _, ok := r.Consume(n, time.Now().Add(time.Second)); ok {
		t.Fatal("expired")
	}
	if _, err := p.Wait(context.Background(), time.Millisecond); err != ErrRendezvousTimeout {
		t.Fatal(err)
	}
	if _, ok := r.Deliver(p.TunnelID(), Arrival{}); ok {
		t.Fatal("deliver after timeout")
	}
	p2, _ := r.Begin(Binding{}, time.Minute)
	c, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p2.Wait(c, time.Second); err == nil {
		t.Fatal("expected ctx")
	}
	if _, ok := r.Deliver(p2.TunnelID(), Arrival{}); ok {
		t.Fatal("deliver canceled")
	}
}

func TestRegistryDeliverFinishActivate(t *testing.T) {
	r := NewRegistry()
	p, _ := r.Begin(Binding{}, time.Minute)
	if _, ok := r.Deliver(p.TunnelID(), Arrival{}); !ok {
		t.Fatal("deliver")
	}
	if _, ok := r.Deliver(p.TunnelID(), Arrival{}); ok {
		t.Fatal("double deliver")
	}
	p.Finish()
	select {
	case <-p.done:
	case <-time.After(time.Second):
		t.Fatal("not done")
	}
	p.Cancel()
	upd, rm := r.Activate(Info{ID: "x"})
	upd(10, 5)
	upd(30, 8)
	l := r.List()
	if len(l) != 1 || l[0].BytesUp != 30 || l[0].BytesDown != 8 {
		t.Fatal(l)
	}
	rm()
	if len(r.List()) != 0 {
		t.Fatal("not removed")
	}
}

func TestRegistryConcurrent(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, n := r.Begin(Binding{}, time.Second)
			r.Consume(n, time.Now())
			r.Deliver(p.TunnelID(), Arrival{})
			p.Cancel()
			_ = r.List()
		}()
	}
	wg.Wait()
}
