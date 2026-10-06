package hookrelay

import (
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/client"
)

func stopPayload(t *testing.T, agent, sid string) Payload {
	return payload(t, agent, map[string]any{"session_id": sid, "hook_event_name": "Stop", "last_assistant_message": "done"})
}

// Relay off (auto released by the SUP-01 D gate, or off) with a watched running
// job: the Stop must block for the job event and deliver it, opening no turn.
func TestStopRelayOffWaitsForWatchedJob(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "omp"} {
		t.Run(agent, func(t *testing.T) {
			f := newFake()
			sid := "sid-off-" + agent
			f.sessions[sid] = client.AgentSession{SessionID: sid, RelayMode: client.RelayModeAuto, WaitReasonDetail: "supervising 1 jobs"}
			f.watchRows = []client.SessionJobWatch{{JobID: "job-9", Title: "build", Status: "running"}}
			time.AfterFunc(60*time.Millisecond, func() {
				f.mu.Lock()
				f.watchRows[0].Status, f.watchRows[0].ExitCode = "done", 0
				f.mu.Unlock()
			})
			var log strings.Builder
			res, err := Run(f, stopPayload(t, agent, sid), fastOpts(&log))
			if err != nil {
				t.Fatal(err)
			}
			if !res.Blocked || !strings.Contains(res.Reason, "[gofer job 完成] job-9 build status=done exit=0") {
				t.Fatalf("result=%+v log=%s", res, log.String())
			}
			if len(f.turns) != 0 {
				t.Fatalf("a job-only wait must not open a relay turn: %+v", f.turns)
			}
			if len(f.watchRows) != 0 {
				t.Fatalf("delivered watch must be removed: %+v", f.watchRows)
			}
			if !strings.Contains(log.String(), "waiting up to") || !strings.Contains(log.String(), "supervising 1 jobs") {
				t.Fatalf("wait path not logged: %s", log.String())
			}
		})
	}
}

func TestStopRelayOffNoJobsReleasesImmediately(t *testing.T) {
	f := newFake()
	f.sessions["sid-nojob"] = client.AgentSession{SessionID: "sid-nojob", RelayMode: client.RelayModeOff}
	var log strings.Builder
	start := time.Now()
	res, err := Run(f, stopPayload(t, "claude", "sid-nojob"), fastOpts(&log))
	if err != nil || res.Blocked || time.Since(start) > time.Second {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if !strings.Contains(log.String(), "relay off, released") {
		t.Fatalf("log=%s", log.String())
	}
}

func TestStopSupervisingWithoutWatchLogsReason(t *testing.T) {
	f := newFake()
	f.sessions["sid-nw"] = client.AgentSession{SessionID: "sid-nw", RelayMode: client.RelayModeAuto, WaitReasonDetail: "supervising 2 jobs"}
	var log strings.Builder
	res, _ := Run(f, stopPayload(t, "claude", "sid-nw"), fastOpts(&log))
	if res.Blocked || !strings.Contains(log.String(), "supervising 2 jobs but no watched job") {
		t.Fatalf("res=%+v log=%s", res, log.String())
	}
}

func TestStopRelayOffWaitBudgetExhausted(t *testing.T) {
	f := newFake()
	f.sessions["sid-bud"] = client.AgentSession{SessionID: "sid-bud", RelayMode: client.RelayModeOff}
	f.watchRows = []client.SessionJobWatch{{JobID: "job-1", Status: "running"}}
	opts := fastOpts(nil)
	opts.Wait = 150 * time.Millisecond
	res, _ := Run(f, stopPayload(t, "claude", "sid-bud"), opts)
	if res.Blocked || len(f.watchRows) != 1 {
		t.Fatalf("res=%+v watches=%+v", res, f.watchRows)
	}
}

// A completed-but-undelivered notice is caught up on the next UserPromptSubmit /
// SessionStart exactly once, and only for agents whose hook output reaches the model.
func TestCatchUpUndeliveredJobNotice(t *testing.T) {
	f := newFake()
	f.sessions["sid-cu"] = client.AgentSession{SessionID: "sid-cu", RelayMode: client.RelayModeAuto}
	f.watchRows = []client.SessionJobWatch{{JobID: "job-7", Title: "t", Status: "failed", ExitCode: 2}}
	p := payload(t, "claude", map[string]any{"session_id": "sid-cu", "hook_event_name": "UserPromptSubmit", "prompt": "hello"})
	res, err := Run(f, p, fastOpts(nil))
	if err != nil || !strings.Contains(res.Context, "[gofer job 完成] job-7 t status=failed exit=2") {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	res, _ = Run(f, p, fastOpts(nil))
	if res.Context != "" {
		t.Fatalf("second prompt re-delivered: %q", res.Context)
	}

	f.watchRows = []client.SessionJobWatch{{JobID: "job-8", Status: "done"}}
	ss := payload(t, "codex", map[string]any{"session_id": "sid-cu", "hook_event_name": "SessionStart", "source": "resume"})
	res, _ = Run(f, ss, fastOpts(nil))
	if !strings.Contains(res.Context, "job-8") || len(f.watchRows) != 0 {
		t.Fatalf("SessionStart catch-up res=%+v", res)
	}

	// omp / jcode never see hook output: the notice must stay queued.
	f.watchRows = []client.SessionJobWatch{{JobID: "job-9", Status: "done"}}
	p = payload(t, "omp", map[string]any{"session_id": "sid-cu", "hook_event_name": "UserPromptSubmit", "prompt": "hi"})
	res, _ = Run(f, p, fastOpts(nil))
	if res.Context != "" || len(f.watchRows) != 1 {
		t.Fatalf("omp must not consume: res=%+v watches=%+v", res, f.watchRows)
	}
	// still-running jobs are not injected
	f.watchRows = []client.SessionJobWatch{{JobID: "job-r", Status: "running"}}
	p = payload(t, "claude", map[string]any{"session_id": "sid-cu", "hook_event_name": "UserPromptSubmit", "prompt": "hi"})
	res, _ = Run(f, p, fastOpts(nil))
	if res.Context != "" || len(f.watchRows) != 1 {
		t.Fatalf("running job injected: %+v", res)
	}
}
