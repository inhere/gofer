package job

import (
	"context"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/wsproto"
)

// dirWaitingRunner plays a worker whose job parks on its directory lock: it mirrors the
// worker's job.waiting_dir event, waits to be told, then reports the job started.
type dirWaitingRunner struct {
	release chan struct{}
	holder  string
	session bool
}

func (r *dirWaitingRunner) Name() string { return "remote-w1" }

func (r *dirWaitingRunner) Run(_ context.Context, req runner.Request) runner.Result {
	if r.session {
		req.OnSessionStatus(wsproto.Status{Status: StatusWaitingDir, SessionStatus: StatusWaitingDir})
	} else {
		req.OnJobEvent(EventJobWaitingDir, map[string]any{"holder_job": r.holder, "dir": "/work"})
	}
	<-r.release
	req.OnStartedAt(time.Now().Unix())
	return runner.Result{ExitCode: 0}
}

// TestWorkerJobWaitingForDirLockShowsWaitingDir: a job parked on its directory lock on the
// worker is `waiting_dir` on the host row (naming the holder) instead of `queued`, and the
// started report takes it to `running` and clears the holder. A session reports the same
// through its status frame.
func TestWorkerJobWaitingForDirLockShowsWaitingDir(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		session bool
		holder  string
	}{
		{"job.waiting_dir event", false, "job-holder"},
		{"session status frame", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := &dirWaitingRunner{release: make(chan struct{}), holder: tc.holder, session: tc.session}
			s := newWorkerTestService(t, t.TempDir(), run)
			req := JobRequest{ProjectKey: "self", Agent: "exec", Runner: "remote-w1", Cmd: []string{"echo", "x"}, Cwd: ".", TimeoutSec: 30}
			if tc.session {
				req = JobRequest{ProjectKey: "self", Agent: "acpw", Runner: "remote-w1", Prompt: "hi", Cwd: ".", TimeoutSec: 30, Session: true}
			}
			res, err := s.Submit(req)
			if err != nil {
				t.Fatalf("Submit: %v", err)
			}
			wait.For(t, 10*time.Second, "host row shows waiting_dir", func() (bool, any) {
				cur, _ := s.Get(res.ID)
				return cur.Status == StatusWaitingDir && cur.WaitingOnJob == tc.holder, cur.Status
			})
			close(run.release)
			wait.For(t, 10*time.Second, "started report takes it out of waiting_dir", func() (bool, any) {
				cur, _ := s.Get(res.ID)
				return cur.Status != StatusWaitingDir && cur.WaitingOnJob == "", cur.Status
			})
			s.Wait(res.ID)
		})
	}
}
