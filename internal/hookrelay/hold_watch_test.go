package hookrelay

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
)

// TestStopHookHeldJobRejectedReason (gofer-9b1b): a held job's submit lines register a
// watch (and never read as finished); once it is rejected the completion notice carries
// the reason, so the agent learns the job never ran and why.
func TestStopHookHeldJobRejectedReason(t *testing.T) {
	out := "job job-h submitted: status=awaiting_approval expires_at=2026-10-11T08:00:00Z result_dir=/r/job-h\n" +
		"awaiting approval: https://gofer.example/jobs/job-h\n"
	if got := extractJobWatchCandidates("Bash", out); strings.Join(got, ",") != "job-h" {
		t.Fatalf("held submit output registers %v, want job-h", got)
	}

	f := newFake()
	f.sessions["sid-hold"] = client.AgentSession{SessionID: "sid-hold", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
	f.watchRows = []client.SessionJobWatch{{JobID: "job-h", Title: "push", Status: "cancelled", Error: "hold rejected by alice: wrong branch"}}
	res, err := Run(f, payload(t, "claude", map[string]any{"session_id": "sid-hold", "hook_event_name": "Stop", "last_assistant_message": "done"}), fastOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Blocked || !strings.Contains(res.Reason, "status=cancelled") || !strings.Contains(res.Reason, "reason=hold rejected by alice: wrong branch") {
		t.Fatalf("stop result = %+v", res)
	}

	expired := formatWatchedJobCompletion(WatchedJob{ID: "job-e", Status: "cancelled", Error: "hold expired"})
	if !strings.HasSuffix(expired, " reason=hold expired") {
		t.Fatalf("expired notice = %q", expired)
	}
	if done := formatWatchedJobCompletion(WatchedJob{ID: "job-d", Status: "failed", Error: "exit 1"}); strings.Contains(done, "reason=") {
		t.Fatalf("only a cancelled job carries the reason: %q", done)
	}
}
