package hookrelay

import (
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/i18n"
)

func TestPostToolUseRegistersJobWatch(t *testing.T) {
	tests := []struct {
		name   string
		tool   string
		output string
		want   []string
	}{
		{name: "claude submitted", tool: "Bash", output: "gofer job 20261017-214236-7dfedcab submitted", want: []string{"20261017-214236-7dfedcab"}},
		{name: "prefixed job id", tool: "Bash", output: "job job-20261021-212412-6a715d34 submitted: status=queued result_dir=/r/job-h", want: []string{"job-20261021-212412-6a715d34"}},
		{name: "prefixed and legacy ids both register", tool: "Bash", output: "job job-20261021-212412-6a715d34 submitted\njob 20261017-214236-7dfedcab submitted", want: []string{"20261017-214236-7dfedcab", "job-20261021-212412-6a715d34"}},
		{name: "prose is not a job id", tool: "Bash", output: "the job was submitted to the queue", want: nil},
		{name: "codex watch", tool: "shell", output: "gofer job watch abc123", want: []string{"abc123"}},
		{name: "same output deduplicates", tool: "Bash", output: "job 20261017-214236-7dfedcab submitted\ngofer job watch 20261017-214236-7dfedcab", want: []string{"20261017-214236-7dfedcab"}},
		{name: "sync terminal is not registered", tool: "Bash", output: "job 20261017-214236-7dfedcab submitted\njob 20261017-214236-7dfedcab finished: status=done exit=0", want: nil},
		{name: "unrelated output is local no-op", tool: "Bash", output: "echo hello", want: nil},
		{name: "non-shell tool is ignored", tool: "Read", output: "gofer job 20261017-214236-7dfedcab submitted", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJobWatchCandidates(tt.tool, tt.output)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("extractJobWatchCandidates(%q, %q) = %v, want %v", tt.tool, tt.output, got, tt.want)
			}
		})
	}
	f := newFake()
	for _, agent := range []string{"claude", "codex"} {
		p := payload(t, agent, map[string]any{"session_id": "sid-watch", "hook_event_name": "PostToolUse", "tool_name": "Bash", "tool_output": "gofer job 20261017-214236-7dfedcab submitted"})
		if _, err := Run(f, p, fastOpts(nil)); err != nil {
			t.Fatal(err)
		}
	}
	p := payload(t, "claude", map[string]any{"session_id": "sid-watch", "hook_event_name": "PostToolUse", "tool_name": "Bash", "tool_output": "echo hello"})
	if _, err := Run(f, p, fastOpts(nil)); err != nil {
		t.Fatal(err)
	}
	if got := len(f.jobWatches); got != 2 {
		t.Fatalf("registered watches=%v, want one per Claude/Codex payload", f.jobWatches)
	}
	p = payload(t, "claude", map[string]any{"session_id": "sid-watch", "hook_event_name": "PostToolUse", "tool_name": "Bash", "tool_output": "job 20261017-214236-7dfedcab submitted\njob 20261017-214236-7dfedcab finished: status=done exit=0"})
	if _, err := Run(f, p, fastOpts(nil)); err != nil {
		t.Fatal(err)
	}
	if got := len(f.jobWatches); got != 2 {
		t.Fatalf("sync terminal registered a watch: %v", f.jobWatches)
	}
}

func TestStopHookReleasesOnWatchedJobTerminal(t *testing.T) {
	f := newFake()
	f.sessions["sid-stop"] = client.AgentSession{SessionID: "sid-stop", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
	f.watchRows = []client.SessionJobWatch{{JobID: "job-1", Title: "compile", Status: "done", ExitCode: 0, Duration: 3}}
	res, err := Run(f, payload(t, "claude", map[string]any{"session_id": "sid-stop", "hook_event_name": "Stop", "last_assistant_message": "done"}), fastOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Blocked || !strings.Contains(res.Reason, "[gofer · job 完成] job-1 compile status=done exit=0") || len(f.watchRows) != 0 {
		t.Fatalf("stop result=%+v watches=%+v", res, f.watchRows)
	}
	terminal := WatchedJob{ID: "job-1", Title: "compile", Status: "done", ExitCode: 0, Duration: 3 * time.Second}
	reason := formatWatchedJobCompletion(terminal, i18n.Default)
	if !strings.Contains(reason, "[gofer · job 完成] job-1 compile status=done exit=0") {
		t.Fatalf("completion injection = %q", reason)
	}
	if got := mergeWatchedTerminals([]WatchedJob{terminal}, i18n.Default); got != reason {
		t.Fatalf("single terminal merge = %q, want %q", got, reason)
	}
}

// A watched job that stops at needs_review (run with review) wakes the supervisor like a
// terminal one: it is the supervisor's turn to verify and accept or reject.
func TestStopHookReleasesOnWatchedJobNeedsReview(t *testing.T) {
	f := newFake()
	f.sessions["sid-review"] = client.AgentSession{SessionID: "sid-review", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
	f.watchRows = []client.SessionJobWatch{{JobID: "job-r", Title: "impl", Status: "needs_review", Duration: 60}}
	res, err := Run(f, payload(t, "claude", map[string]any{"session_id": "sid-review", "hook_event_name": "Stop", "last_assistant_message": "waiting"}), fastOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Blocked || !strings.Contains(res.Reason, "job-r impl status=needs_review") || !strings.Contains(res.Reason, "gofer job review job-r") || len(f.watchRows) != 0 {
		t.Fatalf("stop result=%+v watches=%+v", res, f.watchRows)
	}
}

func TestStopHookMergesSimultaneousFinishes(t *testing.T) {
	// The merge is also exercised through the Stop path after both rows become
	// terminal in the same list response.
	f := newFake()
	f.sessions["sid-merge"] = client.AgentSession{SessionID: "sid-merge", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
	f.watchRows = []client.SessionJobWatch{{JobID: "job-1", Title: "first", Status: "done"}, {JobID: "job-2", Title: "second", Status: "failed", ExitCode: 1}}
	res, err := Run(f, payload(t, "codex", map[string]any{"session_id": "sid-merge", "hook_event_name": "Stop", "last_assistant_message": "done"}), fastOpts(nil))
	if err != nil || !res.Blocked || strings.Count(res.Reason, "[gofer · job 完成]") != 2 || len(f.watchRows) != 0 {
		t.Fatalf("stop merge result=%+v watches=%+v err=%v", res, f.watchRows, err)
	}
	got := mergeWatchedTerminals([]WatchedJob{
		{ID: "job-1", Title: "first", Status: "done", ExitCode: 0, Duration: 2 * time.Second},
		{ID: "job-2", Title: "second", Status: "failed", ExitCode: 1, Duration: 4 * time.Second},
	}, i18n.Default)
	if strings.Count(got, "[gofer · job 完成]") != 2 {
		t.Fatalf("merged injection = %q, want two completion entries", got)
	}
	if strings.Index(got, "job-1") > strings.Index(got, "job-2") {
		t.Fatalf("merged injection order = %q, want stable job order", got)
	}
	mixed := newFake()
	mixed.sessions["sid-mixed"] = client.AgentSession{SessionID: "sid-mixed", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
	mixed.watchRows = []client.SessionJobWatch{{JobID: "job-1", Status: "done"}, {JobID: "job-running", Status: "running"}}
	res, err = Run(mixed, payload(t, "claude", map[string]any{"session_id": "sid-mixed", "hook_event_name": "Stop", "last_assistant_message": "done"}), fastOpts(nil))
	if err != nil || !res.Blocked || strings.Contains(res.Reason, "job-running") || len(mixed.watchRows) != 1 {
		t.Fatalf("mixed terminal result=%+v watches=%+v err=%v", res, mixed.watchRows, err)
	}
}
