package hookrelay

import (
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/client"
)

func TestPostToolUseRegistersJobWatch(t *testing.T) {
	tests := []struct {
		name   string
		tool   string
		output string
		want   []string
	}{
		{name: "claude submitted", tool: "Bash", output: "gofer job abc123 submitted", want: []string{"abc123"}},
		{name: "codex watch", tool: "shell", output: "gofer job watch abc123", want: []string{"abc123"}},
		{name: "same output deduplicates", tool: "Bash", output: "job abc123 submitted\ngofer job watch abc123", want: []string{"abc123"}},
		{name: "sync terminal is not registered", tool: "Bash", output: "job abc123 submitted\njob abc123 finished: status=done exit=0", want: nil},
		{name: "unrelated output is local no-op", tool: "Bash", output: "echo hello", want: nil},
		{name: "non-shell tool is ignored", tool: "Read", output: "gofer job xyz submitted", want: nil},
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
		p := payload(t, agent, map[string]any{"session_id": "sid-watch", "hook_event_name": "PostToolUse", "tool_name": "Bash", "tool_output": "gofer job abc123 submitted"})
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
	p = payload(t, "claude", map[string]any{"session_id": "sid-watch", "hook_event_name": "PostToolUse", "tool_name": "Bash", "tool_output": "job abc123 submitted\njob abc123 finished: status=done exit=0"})
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
	if !res.Blocked || !strings.Contains(res.Reason, "[gofer job 完成] job-1 compile status=done exit=0") || len(f.watchRows) != 0 {
		t.Fatalf("stop result=%+v watches=%+v", res, f.watchRows)
	}
	terminal := WatchedJob{ID: "job-1", Title: "compile", Status: "done", ExitCode: 0, Duration: 3 * time.Second}
	reason := formatWatchedJobCompletion(terminal)
	if !strings.Contains(reason, "[gofer job 完成] job-1 compile status=done exit=0") {
		t.Fatalf("completion injection = %q", reason)
	}
	if got := mergeWatchedTerminals([]WatchedJob{terminal}); got != reason {
		t.Fatalf("single terminal merge = %q, want %q", got, reason)
	}
}

func TestStopHookMergesSimultaneousFinishes(t *testing.T) {
	// The merge is also exercised through the Stop path after both rows become
	// terminal in the same list response.
	f := newFake()
	f.sessions["sid-merge"] = client.AgentSession{SessionID: "sid-merge", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
	f.watchRows = []client.SessionJobWatch{{JobID: "job-1", Title: "first", Status: "done"}, {JobID: "job-2", Title: "second", Status: "failed", ExitCode: 1}}
	res, err := Run(f, payload(t, "codex", map[string]any{"session_id": "sid-merge", "hook_event_name": "Stop", "last_assistant_message": "done"}), fastOpts(nil))
	if err != nil || !res.Blocked || strings.Count(res.Reason, "[gofer job 完成]") != 2 || len(f.watchRows) != 0 {
		t.Fatalf("stop merge result=%+v watches=%+v err=%v", res, f.watchRows, err)
	}
	got := mergeWatchedTerminals([]WatchedJob{
		{ID: "job-1", Title: "first", Status: "done", ExitCode: 0, Duration: 2 * time.Second},
		{ID: "job-2", Title: "second", Status: "failed", ExitCode: 1, Duration: 4 * time.Second},
	})
	if strings.Count(got, "[gofer job 完成]") != 2 {
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
