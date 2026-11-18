package worker

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/wsproto"
)

// TestTerminalLogTailReplayedBeforeResult: a job that wrote output and finished while the
// connection was down is replayed on reconnect as the log tail FROM the hub's durable
// offset (from the resume ack), then the Result — never the Result alone, and never the
// bytes the hub already has.
//
// Falsification: drop the replayLogTail call in replayJobState -> the hub sees the Result
// but no log frame for the job.
func TestTerminalLogTailReplayedBeforeResult(t *testing.T) {
	jobs := newRunningJobs(t)
	jobs.finish(job.JobResult{ID: "local-1", Status: job.StatusDone, ExitCode: 0})
	// The job's output, all written while the worker could not reach the hub. The hub
	// already persisted the first 6 bytes of stdout ("start\n") before the blip.
	logDir := filepath.Join(filepath.Dir(jobs.dir), "local-1")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logDir, store.StdoutFile), []byte("start\nend-of-job\n"), 0o600); err != nil {
		t.Fatalf("write stdout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logDir, store.StderrFile), []byte("warn\n"), 0o600); err != nil {
		t.Fatalf("write stderr: %v", err)
	}

	hub := newRecoverHub(t)
	hub.setResume(func(reg wsproto.Register) []wsproto.ResumeJob {
		for _, f := range reg.Inflight {
			if f.JobID == "job-x" {
				return []wsproto.ResumeJob{{JobID: "job-x", StdoutOff: 6}}
			}
		}
		return nil
	})
	cl := New(Config{
		WorkerID: "w1", Token: "t", URLs: []string{hub.wsURL()},
		InitialBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond,
	}, jobs)
	cl.pollInterval = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	disconnected := make(chan struct{})
	var once sync.Once
	cl.onSession = func(event string) {
		if event == "disconnected" {
			once.Do(func() { close(disconnected) })
		}
	}
	StartClient(t, ctx, cl)
	hub.waitConnections(t, 1, 5*time.Second)

	gate := make(chan struct{}, 1)
	hub.setGate(gate)
	hub.dropConn()
	select {
	case <-disconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never observed the disconnect")
	}
	cl.handleDispatch(ctx, hub.wsURL(), wsproto.Dispatch{
		JobID: "job-x", ProjectKey: "alpha", Agent: "exec", Runner: builtinLocalRunner, Cmd: []string{"echo", "hi"},
	})
	if _, ok := cl.inflightResult("job-x"); !ok {
		t.Fatal("the terminal Result was NOT cached while the connection was down")
	}

	gate <- struct{}{}
	hub.waitResult(t, "job-x", 5*time.Second)

	out, errOut := "", ""
	resultAt, lastLogAt := -1, -1
	for i, env := range hub.frames() {
		if env.JobID != "job-x" {
			continue
		}
		switch env.Type {
		case wsproto.TypeLog:
			l, _ := wsproto.As[wsproto.Log](env)
			if l.Stream == string(store.StreamStderr) {
				errOut += l.Text
			} else {
				out += l.Text
			}
			lastLogAt = i
		case wsproto.TypeResult:
			resultAt = i
		}
	}
	if out != "end-of-job\n" {
		t.Errorf("stdout replayed = %q, want only the tail past the hub's offset %q", out, "end-of-job\n")
	}
	if errOut != "warn\n" {
		t.Errorf("stderr replayed = %q, want %q", errOut, "warn\n")
	}
	if lastLogAt < 0 || resultAt < lastLogAt {
		t.Errorf("Result at frame %d came before the last log frame at %d", resultAt, lastLogAt)
	}
}
