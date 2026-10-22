package hookrelay

import (
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// stopJobWaitFake builds a session with a watched running job that finishes after
// 60ms, i.e. the shape the Stop hook used to park on (gofer-rhux).
func stopJobWaitFake(t *testing.T, sid, relayMode, serverValue string) *fakeAPI {
	t.Helper()
	f := newFake()
	f.sessions[sid] = client.AgentSession{
		SessionID: sid, RelayMode: relayMode, StopJobWait: serverValue,
		// The SUP-01 D gate: the caller has live jobs, so the relay's auto rules stay
		// out of the way and the hook is the one holding the watched job.
		WaitReasonDetail: "supervising 1 jobs",
	}
	f.watchRows = []client.SessionJobWatch{{JobID: "job-9", Title: "build", Status: "running"}}
	time.AfterFunc(60*time.Millisecond, func() {
		f.mu.Lock()
		f.watchRows[0].Status, f.watchRows[0].ExitCode = "done", 0
		f.mu.Unlock()
	})
	return f
}

// TestStopJobWaitMatrix is the whole of session.stop_job_wait: three values × the three
// keyboard readings (at the keyboard / away / unreadable), plus which of the two
// sources (this machine's file, the server's session value) decides.
func TestStopJobWaitMatrix(t *testing.T) {
	const threshold = 300
	cases := []struct {
		name      string
		local     string // this machine's session.stop_job_wait
		server    string // the server's value, carried on the session
		relayMode string
		idle      int64
		want      bool // does the Stop block on the watched job?
	}{
		{name: "away at the keyboard releases", local: config.StopJobWaitAway, relayMode: client.RelayModeAuto, idle: 10, want: false},
		{name: "away, person gone, blocks", local: config.StopJobWaitAway, relayMode: client.RelayModeAuto, idle: 600, want: true},
		{name: "away, unknown reading releases", local: config.StopJobWaitAway, relayMode: client.RelayModeAuto, idle: -1, want: false},
		{name: "away, relay off releases", local: config.StopJobWaitAway, relayMode: client.RelayModeOff, idle: 600, want: false},
		{name: "away, relay on blocks", local: config.StopJobWaitAway, relayMode: client.RelayModeOn, idle: 10, want: true},
		{name: "always blocks while present", local: config.StopJobWaitAlways, relayMode: client.RelayModeAuto, idle: 10, want: true},
		{name: "always blocks while away", local: config.StopJobWaitAlways, relayMode: client.RelayModeAuto, idle: 600, want: true},
		{name: "always blocks on an unknown reading", local: config.StopJobWaitAlways, relayMode: client.RelayModeAuto, idle: -1, want: true},
		{name: "never releases while away", local: config.StopJobWaitNever, relayMode: client.RelayModeAuto, idle: 600, want: false},
		{name: "never releases while present", local: config.StopJobWaitNever, relayMode: client.RelayModeAuto, idle: 10, want: false},
		{name: "never wins over the server value", local: config.StopJobWaitNever, server: config.StopJobWaitAlways, relayMode: client.RelayModeAuto, idle: 10, want: false},
		{name: "server value decides when the file is silent", local: "", server: config.StopJobWaitAlways, relayMode: client.RelayModeAuto, idle: 10, want: true},
		{name: "server never is honoured", local: "", server: config.StopJobWaitNever, relayMode: client.RelayModeAuto, idle: 600, want: false},
		{name: "neither source set falls back to away", local: "", server: "", relayMode: client.RelayModeAuto, idle: 600, want: true},
		{name: "an unknown value falls through to the server", local: "sometimes", server: config.StopJobWaitAlways, relayMode: client.RelayModeAuto, idle: 10, want: true},
		{name: "an unknown value in both falls back to away", local: "sometimes", server: "maybe", relayMode: client.RelayModeAuto, idle: 10, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			injectIdleProbe(t, tc.idle)
			sid := "sid-sjw"
			f := stopJobWaitFake(t, sid, tc.relayMode, tc.server)
			opts := fastOpts(nil)
			opts.StopJobWait = tc.local
			opts.AutoRelayIdleSec = threshold
			var log strings.Builder
			opts.Log = &log

			res, err := Run(f, stopPayload(t, "claude", sid), opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := res.Blocked; got != tc.want {
				t.Fatalf("blocked=%v, want %v (log=%s)", got, tc.want, log.String())
			}
			if tc.want {
				if !strings.Contains(res.Reason, "[gofer · job 完成] job-9") {
					t.Fatalf("block reason=%q, want the job notice", res.Reason)
				}
				if !strings.Contains(log.String(), "waiting up to") {
					t.Fatalf("wait path not logged: %s", log.String())
				}
				return
			}
			// Released: the Stop must return at once, open no turn, and LEAVE the watch
			// for the catch-up (deleting it here would lose the notice entirely).
			if len(f.turns) != 0 {
				t.Fatalf("a released Stop must not open a turn: %+v", f.turns)
			}
			if !strings.Contains(log.String(), "keeps the terminal free") {
				t.Fatalf("release not explained in the log: %s", log.String())
			}
		})
	}
}

// TestStopJobWaitReleasedNoticeIsCaughtUp: releasing the Stop is not losing the notice —
// the next UserPromptSubmit injects the terminal job's completion.
func TestStopJobWaitReleasedNoticeIsCaughtUp(t *testing.T) {
	injectIdleProbe(t, 5) // the person is right here
	sid := "sid-catchup"
	f := newFake()
	f.sessions[sid] = client.AgentSession{SessionID: sid, RelayMode: client.RelayModeAuto}
	f.watchRows = []client.SessionJobWatch{{JobID: "job-5", Title: "tests", Status: "done"}}
	opts := fastOpts(nil)
	opts.StopJobWait = config.StopJobWaitAway
	opts.AutoRelayIdleSec = 300

	res, err := Run(f, stopPayload(t, "claude", sid), opts)
	if err != nil || res.Blocked {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(f.watchRows) != 1 {
		t.Fatalf("the watch must stay for the catch-up: %+v", f.watchRows)
	}
	p := payload(t, "claude", map[string]any{"session_id": sid, "hook_event_name": "UserPromptSubmit", "prompt": "next"})
	res, err = Run(f, p, opts)
	if err != nil || !strings.Contains(res.Context, "[gofer · job 完成] job-5 tests status=done") {
		t.Fatalf("catch-up res=%+v err=%v", res, err)
	}
	if len(f.watchRows) != 0 {
		t.Fatalf("the caught-up notice must consume the watch: %+v", f.watchRows)
	}
}

// TestStopJobWaitDoesNotGovernTheRelayWait: the knob is about JOB blocking. A session
// whose relay is explicitly on still waits for a web reply under `never` — that wait is
// the human's own request, not a job parking the terminal.
func TestStopJobWaitDoesNotGovernTheRelayWait(t *testing.T) {
	injectIdleProbe(t, 5)
	sid := "sid-relay-on"
	f := newFake()
	f.sessions[sid] = client.AgentSession{SessionID: sid, RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
	f.watchRows = []client.SessionJobWatch{{JobID: "job-3", Status: "running"}}
	f.answerAfter, f.answer = 1, "go on"

	opts := fastOpts(nil)
	opts.StopJobWait = config.StopJobWaitNever
	res, err := Run(f, stopPayload(t, "claude", sid), opts)
	if err != nil || !res.Blocked || !strings.Contains(res.Reason, "go on") {
		t.Fatalf("relay wait must survive StopJobWait=never: res=%+v err=%v", res, err)
	}
	if len(f.turns) != 1 {
		t.Fatalf("the relay turn must still be opened: %+v", f.turns)
	}
}
