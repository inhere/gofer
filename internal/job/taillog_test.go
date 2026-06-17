package job

import (
	"strings"
	"testing"

	"dev-agent-bridge/internal/store"
)

func TestTailLogStdout(t *testing.T) {
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("setup: expected done, got %s (err=%s)", final.Status, final.Error)
	}

	// Whole file (maxBytes <= 0).
	out, err := s.TailLog(final.ID, store.StreamStdout, 0)
	if err != nil {
		t.Fatalf("TailLog: %v", err)
	}
	if !strings.Contains(string(out), "go version") {
		t.Fatalf("stdout tail missing output: %q", out)
	}
}

func TestTailLogStderrEmpty(t *testing.T) {
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("setup: expected done")
	}
	// stderr exists (created by the runner) but is empty for `go version`.
	out, err := s.TailLog(final.ID, store.StreamStderr, 0)
	if err != nil {
		t.Fatalf("TailLog stderr: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty stderr, got %q", out)
	}
}

func TestTailLogCapped(t *testing.T) {
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "yes ABCDEFGH | head -c 4096"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("setup: expected done, got %s", final.Status)
	}
	out, err := s.TailLog(final.ID, store.StreamStdout, 100)
	if err != nil {
		t.Fatalf("TailLog: %v", err)
	}
	if len(out) != 100 {
		t.Fatalf("expected exactly 100 tail bytes, got %d", len(out))
	}
}

func TestTailLogUnknownJob(t *testing.T) {
	s := newTestService(t, t.TempDir())
	if _, err := s.TailLog("does-not-exist", store.StreamStdout, 0); err == nil {
		t.Fatalf("expected error for unknown job id")
	}
}
