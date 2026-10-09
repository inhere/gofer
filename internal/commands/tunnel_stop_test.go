package commands

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/client"
)

// fakeForwarderHub scripts heartbeat answers and records every call.
type fakeForwarderHub struct {
	mu          sync.Mutex
	registers   []client.TunnelForwarderRegistration
	heartbeats  int
	unregisters []string
	beat        func(n int) error
}

func (f *fakeForwarderHub) RegisterTunnelForwarder(r client.TunnelForwarderRegistration) (client.TunnelForwarder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registers = append(f.registers, r)
	return client.TunnelForwarder{ID: fmt.Sprintf("fw-%08d", len(f.registers))}, nil
}

func (f *fakeForwarderHub) HeartbeatTunnelForwarder(string, []client.TunnelSpecView) (client.TunnelForwarder, error) {
	f.mu.Lock()
	f.heartbeats++
	n := f.heartbeats
	f.mu.Unlock()
	return client.TunnelForwarder{}, f.beat(n)
}

func (f *fakeForwarderHub) UnregisterTunnelForwarder(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unregisters = append(f.unregisters, id)
	return nil
}

type syncLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *syncLog) printf(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(&l.b, format, a...)
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestRegisterForwarderStopOn410: a 410 heartbeat cancels the forward's context and
// neither re-registers nor unregisters (the hub already dropped the entry); the
// registration advertises the stop capability.
func TestRegisterForwarderStopOn410(t *testing.T) {
	hub := &fakeForwarderHub{beat: func(n int) error {
		if n == 1 {
			return nil // one ordinary renewal first
		}
		return &client.StatusError{Status: http.StatusGone, Msg: "stop requested"}
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var log syncLog
	unregister := registerForwarder(ctx, log.printf, hub, "w-hw", nil, 5*time.Millisecond, cancel)

	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a 410 heartbeat must cancel the forward context")
	}
	unregister()

	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.registers) != 1 {
		t.Fatalf("registers = %d, want 1 (no re-register on 410)", len(hub.registers))
	}
	if caps := hub.registers[0].Caps; len(caps) != 1 || caps[0] != client.ForwarderCapStop {
		t.Fatalf("registration caps = %v, want [stop]", caps)
	}
	if len(hub.unregisters) != 0 {
		t.Fatalf("unregisters = %v, want none after 410", hub.unregisters)
	}
	if !strings.Contains(log.String(), "stop requested from the console") {
		t.Fatalf("output = %q, want the stop notice", log.String())
	}
}

// TestRegisterForwarderReRegistersOn404: the 404 path is unchanged — re-register and
// keep going; a normal exit unregisters the latest id.
func TestRegisterForwarderReRegistersOn404(t *testing.T) {
	hub := &fakeForwarderHub{beat: func(n int) error {
		if n == 1 {
			return &client.StatusError{Status: http.StatusNotFound, Msg: "gone"}
		}
		return nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var log syncLog
	unregister := registerForwarder(ctx, log.printf, hub, "w-hw", nil, 5*time.Millisecond, func() { t.Error("cancel must not be called on 404") })
	deadline := time.Now().Add(5 * time.Second)
	for {
		hub.mu.Lock()
		n := len(hub.registers)
		hub.mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("404 must re-register")
		}
		time.Sleep(time.Millisecond)
	}
	unregister()
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.unregisters) != 1 || hub.unregisters[0] != "fw-00000002" {
		t.Fatalf("unregisters = %v, want the re-registered id", hub.unregisters)
	}
}

func TestForwarderStopLabel(t *testing.T) {
	for _, tc := range []struct {
		f    client.TunnelForwarder
		want string
	}{
		{client.TunnelForwarder{Caps: []string{"stop"}, StopRequested: true}, "stopping"},
		{client.TunnelForwarder{Hosted: true}, "hosted"},
		{client.TunnelForwarder{Caps: []string{"stop"}}, "remote"},
		{client.TunnelForwarder{}, "ctrl+c-only"},
	} {
		if got := forwarderStopLabel(tc.f); got != tc.want {
			t.Errorf("forwarderStopLabel(%#v) = %q, want %q", tc.f, got, tc.want)
		}
	}
}
