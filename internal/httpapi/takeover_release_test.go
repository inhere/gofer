package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// recordingRelayNotifier captures the relay's outbound notifications, so a test can
// pin the event the auto-release raises (SUP-02 R1) without a real webhook.
type recordingRelayNotifier struct {
	mu       sync.Mutex
	released []string
}

func (n *recordingRelayNotifier) NotifySessionWaiting(_, _, _, _ string, _ int64) {}
func (n *recordingRelayNotifier) NotifySessionAttention(_, _, _, _ string)        {}
func (n *recordingRelayNotifier) NotifySessionHandedOff(_, _, _, _ string)        {}
func (n *recordingRelayNotifier) NotifySessionTakeoverReleased(sid, _, _, jobID, reason string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.released = append(n.released, sid+":"+jobID+":"+reason)
}

func (n *recordingRelayNotifier) releases() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.released...)
}

// waitFile polls for a path the job's command creates, so a test can act while the
// job is provably still running.
func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", path)
}

// TestTakeoverJobEndReleasesSession is the assembled-hub proof of SUP-02 R1: a
// session held by a pty takeover job goes back to idle the moment that job reaches a
// terminal state — no human action, no cancel of an already-dead process — and the
// event naming the reason rides the relay's notifier seam so an operator (or a
// webhook subscribing to session.takeover_released) learns the original terminal
// relays again.
func TestTakeoverJobEndReleasesSession(t *testing.T) {
	s := newTestServer(t, testToken, false)
	nf := &recordingRelayNotifier{}
	s.relay.SetNotifier(nf)

	const sid = "sid-e2e-takeover-release"
	cwd := t.TempDir()
	registerTakeoverSession(t, s, sid, cwd, "")

	// The takeover job itself: an ordinary terminal command carrying path B's tag
	// (that tag is what the assembly keys the release hook on). It drops a marker
	// before sleeping, so the handoff below is written while the job still runs.
	marker := filepath.Join(cwd, "started")
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "append-file-sleep", marker, "up", "2s"),
		Cwd: ".", TimeoutSec: 60, Tags: []string{"relay-takeover"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit status=%d, want 200", resp.StatusCode)
	}
	var submitted job.JobResult
	decode(t, resp, &submitted)
	waitFile(t, marker)

	if _, err := s.jobs.Meta().SetSessionHandedOff(sid, submitted.ID); err != nil {
		t.Fatalf("hand session off to %s: %v", submitted.ID, err)
	}
	held := sessionState(t, s, sid)
	if held.State != "handed_off" || held.HandedOffJobID != submitted.ID {
		t.Fatalf("session before finish = %+v, want handed_off to %s", held, submitted.ID)
	}

	final := waitDone(t, s, submitted.ID)
	if final.Status != job.StatusDone {
		t.Fatalf("takeover job status = %s (err=%s), want done", final.Status, final.Error)
	}

	// The release runs on the finish path, asynchronously: poll for it.
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := sessionState(t, s, sid)
		ev := nf.releases()
		if got.State == "idle" && got.HandedOffJobID == "" && len(ev) == 1 {
			if ev[0] != sid+":"+submitted.ID+":job_done" {
				t.Fatalf("release event = %q, want %s:%s:job_done", ev[0], sid, submitted.ID)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session=%+v events=%v, want idle + one release", got, ev)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
