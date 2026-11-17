package worker

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/wsproto"
)

func newDrainClient(t *testing.T, hubProto int) *Client {
	t.Helper()
	cl := New(Config{WorkerID: "w1"}, &stubJobs{})
	cl.serverProto.Store(int64(hubProto))
	return cl
}

func addSession(cl *Client, id, status string, reloadable bool) {
	cl.inflightCreate(id)
	cl.inflightSetSession(id, 2, status, false)
	cl.inflightSetReloadable(id, reloadable)
}

// TestDrainBlockersLeaveIdleReloadableSessionsOut: an upgrade waits for what cannot survive
// the exit: a turn in progress, a session whose agent cannot reload it, and any ordinary job.
// An idle session whose agent can load it again is not waited for, but only when the hub does
// the reload (an older hub fails it, so there every job counts as before).
func TestDrainBlockersLeaveIdleReloadableSessionsOut(t *testing.T) {
	cl := newDrainClient(t, wsproto.HostedMinProtocolVersion)
	addSession(cl, "sess-idle", job.StatusAwaitingInput, true)
	addSession(cl, "sess-busy", job.StatusRunning, true)
	addSession(cl, "sess-noload", job.StatusAwaitingInput, false)
	cl.inflightCreate("plain")
	want := []string{"plain", "sess-busy", "sess-noload"}
	if got := cl.drainBlockers(); !reflect.DeepEqual(got, want) {
		t.Fatalf("blockers = %v, want %v", got, want)
	}

	old := newDrainClient(t, wsproto.HostedMinProtocolVersion-1)
	addSession(old, "sess-idle", job.StatusAwaitingInput, true)
	if got := old.drainBlockers(); !reflect.DeepEqual(got, []string{"sess-idle"}) {
		t.Fatalf("against a hub below v%d the idle session must still block, got %v", wsproto.HostedMinProtocolVersion, got)
	}
}

// TestUpgradeDoesNotWaitForIdleSessionAndNamesWhatHeldItUp: the upgrade goes through with only
// an idle reloadable session in flight; when a running turn holds the drain the abandoned
// upgrade names that job and not the idle one.
func TestUpgradeDoesNotWaitForIdleSessionAndNamesWhatHeldItUp(t *testing.T) {
	cl := newDrainClient(t, wsproto.CurrentProtocolVersion)
	addSession(cl, "sess-idle", job.StatusAwaitingInput, true)

	h := newHarness(t)
	h.writeMarkerOnSpawn()
	h.run.inflight = func() int { return len(cl.drainBlockers()) }
	h.run.blockers = cl.drainBlockers
	h.run.drainLimit = 2 * time.Second
	if err := h.do(context.Background()); err != nil {
		t.Fatalf("upgrade with only an idle reloadable session: %v", err)
	}
	if len(h.spawned) != 1 {
		t.Fatalf("spawned %d replacement processes, want 1", len(h.spawned))
	}

	addSession(cl, "sess-busy", job.StatusRunning, true)
	h2 := newHarness(t)
	h2.run.inflight = func() int { return len(cl.drainBlockers()) }
	h2.run.blockers = cl.drainBlockers
	h2.run.drainLimit = 50 * time.Millisecond
	err := h2.do(context.Background())
	if err == nil || !strings.Contains(err.Error(), "drain timed out") || !strings.Contains(err.Error(), "sess-busy") ||
		strings.Contains(err.Error(), "sess-idle") {
		t.Fatalf("err = %v, want an abandoned drain naming sess-busy only", err)
	}
}
