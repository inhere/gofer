package hookrelay

import (
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// A Stop that parked the terminal on the session's watched jobs while sub-agents
// were still running must let go the moment the last one finishes (gofer-7lac):
// otherwise the main agent's "your sub-agent finished" turn cannot reach the
// session until some job ends or the --wait budget runs out. Waiting still holds
// while a sub-agent runs.
func TestStopJobWaitReleasedWhenSubagentsFinish(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			f := newFake()
			sid := "sid-sub-" + agent
			f.sessions[sid] = client.AgentSession{SessionID: sid, RelayMode: client.RelayModeAuto,
				WaitReasonDetail: "supervising 1 jobs", SubagentCount: 1}
			f.watchRows = []client.SessionJobWatch{{JobID: "job-sub", Title: "build", Status: "running"}}
			// First poll: still one sub-agent (the wait must hold); second: finished.
			f.subagentCounts = []int{1, 0}
			var log strings.Builder
			opts := fastOpts(&log)
			opts.StopJobWait = config.StopJobWaitAlways
			start := time.Now()
			res, err := Run(f, stopPayload(t, agent, sid), opts)
			if err != nil {
				t.Fatal(err)
			}
			if res.Blocked {
				t.Fatalf("a finished sub-agent must not block: %+v log=%s", res, log.String())
			}
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Fatalf("released after %s, want well before the --wait budget (3s)", elapsed)
			}
			if !strings.Contains(log.String(), "sub-agent(s) finished, released so the agent can read their results") {
				t.Fatalf("release not logged: %s", log.String())
			}
			if len(f.turns) != 0 {
				t.Fatalf("a job-only wait must not open a relay turn: %+v", f.turns)
			}
			if len(f.watchRows) != 1 {
				t.Fatalf("the pending watch must stay queued for the catch-up: %+v", f.watchRows)
			}
		})
	}
}

// Entering the job-only wait with no sub-agent running changes nothing: the wait
// still runs to its budget and the pending watch is left to the catch-up.
func TestStopJobWaitWithoutRunningSubagentsIsUnchanged(t *testing.T) {
	f := newFake()
	f.sessions["sid-nosub"] = client.AgentSession{SessionID: "sid-nosub", RelayMode: client.RelayModeOff}
	f.watchRows = []client.SessionJobWatch{{JobID: "job-hold", Status: "running"}}
	// A server reporting 0 on every poll (and one reporting nothing at all) must not
	// cut the wait short.
	f.subagentCounts = []int{0}
	var log strings.Builder
	opts := fastOpts(&log)
	opts.StopJobWait = config.StopJobWaitAlways
	opts.Wait = 150 * time.Millisecond
	res, err := Run(f, stopPayload(t, "claude", "sid-nosub"), opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Blocked || len(f.watchRows) != 1 {
		t.Fatalf("res=%+v watches=%+v", res, f.watchRows)
	}
	if strings.Contains(log.String(), "sub-agent(s) finished") {
		t.Fatalf("released on a sub-agent count it never entered with: %s", log.String())
	}
	if !strings.Contains(log.String(), "wait budget exhausted with watched jobs still pending") {
		t.Fatalf("wait did not run to its budget: %s", log.String())
	}
}

// When the last sub-agent finishes in the same round as a watched job, the job's
// notice wins: the main agent is woken with the result instead of being released
// with nothing (its own sub-agent turn already carries the sub-agent result).
func TestStopJobWaitDeliversJobNoticeBeforeSubagentRelease(t *testing.T) {
	f := newFake()
	f.sessions["sid-both"] = client.AgentSession{SessionID: "sid-both", RelayMode: client.RelayModeAuto, SubagentCount: 2}
	f.watchRows = []client.SessionJobWatch{{JobID: "job-both", Title: "tests", Status: "done"}}
	f.subagentCounts = []int{0}
	var log strings.Builder
	opts := fastOpts(&log)
	opts.StopJobWait = config.StopJobWaitAlways
	res, err := Run(f, stopPayload(t, "claude", "sid-both"), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Blocked || !strings.Contains(res.Reason, "[gofer · job 完成] job-both tests status=done") {
		t.Fatalf("result=%+v log=%s", res, log.String())
	}
	if len(f.watchRows) != 0 {
		t.Fatalf("delivered watch must be removed: %+v", f.watchRows)
	}
}
