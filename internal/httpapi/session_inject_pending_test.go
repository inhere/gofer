package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/sessionrelay"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// gofer-qzv5: the delivery seam's answer when gofer's wait window expires. The bug this
// pins: a delivery job parked by an update waiting for idle was reported as
// 「失败 · exit code -1」, while the job ran 27 minutes later and delivered the text — the
// web kept showing a failure the user could have "retried" into a double delivery.

// injectProbeFile is the file the testcmd helper writes when its job really runs: the
// evidence that a delivery job did (or did not) get to do its work.
func injectProbeFile(t *testing.T, s *Server, name string) string {
	t.Helper()
	return filepath.Join(s.projects.Config().Projects["self"].HostPath, name)
}

// TestInjectSessionGivesUpOnAnUnstartedJob: a delivery job that had not STARTED when the
// wait window elapsed is cancelled and reported with the status that held it, so nothing
// arrives late and the caller can retry safely.
func TestInjectSessionGivesUpOnAnUnstartedJob(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	bin := testcmd.Path(t)
	probe := injectProbeFile(t, s, "parked-delivery.txt")

	// An update waiting for idle parks new jobs (gofer-dzv3).
	s.jobs.PauseStartsForUpdate("apply-qzv5")
	res, err := s.msgr.InjectSession(context.Background(), sessionrelay.InjectRequest{
		ProjectKey: "self", Runner: "local", Cwd: ".", Title: "relay deliver → sid-parked",
		Cmd: []string{bin, "write-files", probe, "ran"}, TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("InjectSession(parked): %v", err)
	}
	if res.Running {
		t.Fatalf("res.Running = true for a job that never started: %+v", res)
	}
	if res.Status != job.StatusPausedForUpdate {
		t.Fatalf("res.Status = %q, want %q", res.Status, job.StatusPausedForUpdate)
	}
	if !strings.HasPrefix(res.StatusDetail, job.StatusLabelZH(job.StatusPausedForUpdate)) {
		t.Fatalf("res.StatusDetail = %q, want the status in the human's language", res.StatusDetail)
	}
	// Withdrawn: nothing can arrive later.
	got, ok := s.jobs.Get(res.JobID)
	if !ok || got.Status != job.StatusCancelled {
		t.Fatalf("parked delivery job = %+v, %v, want cancelled", got.Status, ok)
	}
	if _, err := os.Stat(probe); err == nil {
		t.Fatalf("the cancelled delivery job ran after all")
	}
	// The relay's probe agrees, so a 「送达中」 record of such a job settles as unresolved
	// rather than staying pending for ever.
	state, err := s.msgr.InjectJob(res.JobID)
	if err != nil || !state.Known || !state.Done || state.Status != job.StatusCancelled {
		t.Fatalf("InjectJob(cancelled) = %+v, %v", state, err)
	}

	// The same request marked as gofer's own control-plane work (the relay's flag) runs
	// through the pause: a delivery must not be parked, and here it finishes in time.
	ok2 := injectProbeFile(t, s, "delivery-ran.txt")
	res, err = s.msgr.InjectSession(context.Background(), sessionrelay.InjectRequest{
		ProjectKey: "self", Runner: "local", Cwd: ".", Title: "relay deliver → sid-live",
		Cmd: []string{bin, "write-files", ok2, "ran"}, TimeoutSec: 30, UpdateExempt: true,
	})
	if err != nil {
		t.Fatalf("InjectSession(exempt): %v", err)
	}
	if res.Status != "" || res.ExitCode != 0 {
		t.Fatalf("exempt delivery = %+v, want a job that ran to completion", res)
	}
	if _, err := os.Stat(ok2); err != nil {
		t.Fatalf("the exempt delivery did not run through the pause: %v", err)
	}
	// The one-shot messenger is the same control-plane delivery (SubmitMessenger carries
	// the same flag), so it must not park either.
	ok3 := injectProbeFile(t, s, "messenger-ran.txt")
	mid, err := s.msgr.SubmitMessenger("self", "local", ".",
		[]string{bin, "write-files", ok3, "ran"}, "session messenger · peer-x", "alice")
	if err != nil {
		t.Fatalf("SubmitMessenger while paused: %v", err)
	}
	if final, ok := s.jobs.Wait(mid); !ok || final.Status != job.StatusDone {
		t.Fatalf("messenger job = %s (%s), %v, want done", final.Status, final.Error, ok)
	}
	if _, err := os.Stat(ok3); err != nil {
		t.Fatalf("the messenger job did not run through the pause: %v", err)
	}
	if n, err := s.jobs.CountUpdatePaused(); err != nil || n != 0 {
		t.Fatalf("CountUpdatePaused = %d, %v, want 0", n, err)
	}
}

// TestInjectSessionGivesUpOnARunningJob: a job that DID start is reported as still
// running (never as a failure), because the text may still land — and the same job
// ending first is reported with its real result.
func TestInjectSessionGivesUpOnARunningJob(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	bin := testcmd.Path(t)

	// A delivery that takes a while: 5 lines, one per second.
	running, err := s.jobs.Submit(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{bin, "stdout-lines", "tick", "5", "1s"}, Cwd: ".", TimeoutSec: 60,
		Tags: []string{sessionrelay.TagRelayDeliver}, UpdateExempt: true,
	})
	if err != nil {
		t.Fatalf("Submit(slow delivery): %v", err)
	}
	wait.For(t, wait.Timeout(t, 10*time.Second), "the delivery job to start running", func() (bool, any) {
		got, ok := s.jobs.Get(running.ID)
		return ok && got.Status == job.StatusRunning, got.Status
	})
	snap, _ := s.jobs.Get(running.ID)
	res := s.msgr.gaveUp(snap)
	if !res.Running || res.Status != job.StatusRunning {
		t.Fatalf("gaveUp(running) = %+v, want Running with status running", res)
	}
	if res.ExitCode == 0 {
		t.Fatalf("gaveUp(running).ExitCode = 0: a job that has not finished must not read as delivered")
	}
	if err := s.jobs.Cancel(running.ID); err != nil {
		t.Fatalf("Cancel(slow delivery): %v", err)
	}

	// A job that ended inside the window is reported with its own result, whatever the
	// snapshot SubmitSync handed over looked like.
	done, err := s.jobs.Submit(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{bin, "printf", "pane_busy:vim"}, Cwd: ".", TimeoutSec: 30,
		Tags: []string{sessionrelay.TagRelayInject}, UpdateExempt: true,
	})
	if err != nil {
		t.Fatalf("Submit(quick delivery): %v", err)
	}
	final, ok := s.jobs.Wait(done.ID)
	if !ok {
		t.Fatalf("Wait(quick delivery) = not found")
	}
	res = s.msgr.gaveUp(final)
	if res.Running || res.Status != "" {
		t.Fatalf("gaveUp(finished) = %+v, want the job's own result", res)
	}
	if res.ExitCode != 0 || res.Output != "pane_busy:vim" {
		t.Fatalf("gaveUp(finished) = exit %d output %q, want the job's own exit code and output", res.ExitCode, res.Output)
	}
}
