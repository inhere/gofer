package tunnel

import (
	"errors"
	"testing"
	"time"
)

func stoppableReg(caps ...string) ForwarderRegistration {
	return ForwarderRegistration{
		Worker: "w-hw", Host: "pc", PID: 7, Caps: caps,
		Specs: []ForwardSpec{{Network: "tcp", Bind: "127.0.0.1", LocalPort: 1502, Target: "10.0.0.1:502"}},
	}
}

// TestForwarderRequestStopHeartbeatGone: a stop request is visible on the entry, the
// owner's next heartbeat collects it as ErrForwarderStopRequested, and that same
// heartbeat removes the entry (the forwarder exits instead of re-registering).
func TestForwarderRequestStopHeartbeatGone(t *testing.T) {
	r := NewForwarderRegistry(nil)
	reg := r.Register("alice", stoppableReg(CapStop))

	got, err := r.RequestStop(reg.ID, "alice", false)
	if err != nil {
		t.Fatalf("RequestStop: %v", err)
	}
	if !got.StopRequested || got.StopRequestedAt.IsZero() {
		t.Fatalf("stop request not recorded: %#v", got)
	}
	first := got.StopRequestedAt
	// Idempotent: the first request time is kept.
	again, err := r.RequestStop(reg.ID, "alice", false)
	if err != nil || !again.StopRequestedAt.Equal(first) {
		t.Fatalf("second RequestStop = %#v, %v; want idempotent", again, err)
	}
	if list := r.List(); len(list) != 1 || !list[0].StopRequested {
		t.Fatalf("pending stop must stay listed with StopRequested, got %#v", list)
	}

	if _, err := r.Heartbeat(reg.ID, "alice", nil); !errors.Is(err, ErrForwarderStopRequested) {
		t.Fatalf("heartbeat after stop = %v, want ErrForwarderStopRequested", err)
	}
	if list := r.List(); len(list) != 0 {
		t.Fatalf("the collecting heartbeat must remove the entry, got %#v", list)
	}
	if _, err := r.Heartbeat(reg.ID, "alice", nil); !errors.Is(err, ErrForwarderNotFound) {
		t.Fatalf("heartbeat after removal = %v, want ErrForwarderNotFound", err)
	}
}

// TestForwarderRequestStopOwnership: another caller cannot stop the forwarder unless it
// is an administrator; a foreign heartbeat does not collect (or clear) the stop.
func TestForwarderRequestStopOwnership(t *testing.T) {
	r := NewForwarderRegistry(nil)
	reg := r.Register("alice", stoppableReg(CapStop))

	if _, err := r.RequestStop(reg.ID, "bob", false); !errors.Is(err, ErrForwarderNotOwner) {
		t.Fatalf("foreign RequestStop = %v, want ErrForwarderNotOwner", err)
	}
	if list := r.List(); list[0].StopRequested {
		t.Fatal("a refused request must not mark the entry")
	}
	if _, err := r.RequestStop(reg.ID, "bob", true); err != nil {
		t.Fatalf("admin RequestStop: %v", err)
	}
	if _, err := r.Heartbeat(reg.ID, "bob", nil); !errors.Is(err, ErrForwarderNotOwner) {
		t.Fatalf("foreign heartbeat = %v, want ErrForwarderNotOwner", err)
	}
	if list := r.List(); len(list) != 1 {
		t.Fatal("a foreign heartbeat must not collect the stop")
	}
	if _, err := r.RequestStop("fw-00000000", "alice", true); !errors.Is(err, ErrForwarderNotFound) {
		t.Fatalf("unknown id = %v, want ErrForwarderNotFound", err)
	}
}

// TestForwarderRequestStopNeedsCap: an older forwarder (no CapStop) cannot be stopped
// remotely, and a hosted one goes through its own route.
func TestForwarderRequestStopNeedsCap(t *testing.T) {
	r := NewForwarderRegistry(nil)
	old := r.Register("alice", stoppableReg())
	if _, err := r.RequestStop(old.ID, "alice", false); !errors.Is(err, ErrForwarderStopUnsupported) {
		t.Fatalf("RequestStop without cap = %v, want ErrForwarderStopUnsupported", err)
	}
	if _, err := r.Heartbeat(old.ID, "alice", nil); err != nil {
		t.Fatalf("an unsupported request must leave the entry alive: %v", err)
	}

	hosted := stoppableReg(CapStop)
	hosted.Hosted, hosted.HostedName = true, "demo"
	h := r.Register("server", hosted)
	if _, err := r.RequestStop(h.ID, "server", false); !errors.Is(err, ErrForwarderHosted) {
		t.Fatalf("RequestStop on hosted = %v, want ErrForwarderHosted", err)
	}
}

// TestForwarderStopRequestedExpires: a forwarder that dies before collecting the stop
// still ages out through the ordinary TTL.
func TestForwarderStopRequestedExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	r := NewForwarderRegistry(func() time.Duration { return time.Minute })
	r.now = func() time.Time { return now }
	reg := r.Register("alice", stoppableReg(CapStop))
	if _, err := r.RequestStop(reg.ID, "alice", false); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if list := r.List(); len(list) != 0 {
		t.Fatalf("stop-requested entry must still expire, got %#v", list)
	}
}
