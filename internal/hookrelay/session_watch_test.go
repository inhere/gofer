package hookrelay

import (
	"strings"
	"testing"
	"time"
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
}

func TestStopHookReleasesOnWatchedJobTerminal(t *testing.T) {
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
}
