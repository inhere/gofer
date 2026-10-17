package hookrelay

import (
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/client"
)

// TestStopWaitHonoursAWatchAddedAfterTheStopBegan (gofer-fxbu): the relay wait used
// the watch count from the Stop's own heartbeat, so a job that became watched while
// the turn was open (a held job parked or approved from elsewhere) could end without
// ever releasing the wait. Every poll now re-reads the watches.
func TestStopWaitHonoursAWatchAddedAfterTheStopBegan(t *testing.T) {
	f := newFake()
	const sid = "sid-late-watch"
	f.sessions[sid] = client.AgentSession{SessionID: sid, RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
	time.AfterFunc(150*time.Millisecond, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.watchRows = append(f.watchRows, client.SessionJobWatch{JobID: "20261017-214236-7dfedcab", Title: "held", Status: "done"})
	})
	var log strings.Builder
	opts := fastOpts(&log)
	opts.Wait = 10 * time.Second
	res, err := Run(f, stopPayload(t, "claude", sid), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Blocked || !strings.Contains(res.Reason, "20261017-214236-7dfedcab") {
		t.Fatalf("result=%+v, want the late-watched job's notice; log=%s", res, log.String())
	}
}

// TestStopAutoRelayOffFallsThroughToJobsOnlyWait (gofer-fxbu): in auto mode the relay
// turns itself off once an approved job is running (the caller is "supervising"). The
// hook used to exit silently there, leaving the job's end notice with nobody to take it;
// a session that still has watched jobs now keeps waiting for job events only.
func TestStopAutoRelayOffFallsThroughToJobsOnlyWait(t *testing.T) {
	f := newFake()
	const sid = "sid-auto-off"
	f.sessions[sid] = client.AgentSession{SessionID: sid, RelayMode: client.RelayModeAuto, WaitReason: client.WaitModeOn}
	f.relayOffAfter = 2
	f.watchRows = []client.SessionJobWatch{{JobID: "20261017-214236-7dfedcab", Title: "held", Status: "running"}}
	time.AfterFunc(300*time.Millisecond, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.watchRows[0].Status = "done"
	})
	var log strings.Builder
	opts := fastOpts(&log)
	opts.Wait = 10 * time.Second
	res, err := Run(f, stopPayload(t, "claude", sid), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Blocked || !strings.Contains(res.Reason, "20261017-214236-7dfedcab") {
		t.Fatalf("result=%+v, want the job's notice after the relay went off; log=%s", res, log.String())
	}
	if !strings.Contains(log.String(), "waiting for job events only") {
		t.Fatalf("job-only fall-through not logged: %s", log.String())
	}
}
