package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// gofer-dzv3 (docs/design/2026-10-14-update-wait-idle-design.md): the one-click update
// waits for the machine to go idle before it restarts the server, parking the jobs that
// arrive meanwhile instead of refusing them.

// setApplyWaitTuning lowers the wait-for-idle bounds and poll interval for a test.
func setApplyWaitTuning(t *testing.T, minSec int, poll time.Duration) {
	t.Helper()
	oldMin, oldPoll := applyWaitIdleMinSec, applyWaitIdlePoll
	applyWaitIdleMinSec, applyWaitIdlePoll = minSec, poll
	t.Cleanup(func() { applyWaitIdleMinSec, applyWaitIdlePoll = oldMin, oldPoll })
}

// newWaitIdleServer is a managed-looking server over the shared "self" project (exec on
// the local runner), with the release mirror checked and a managed-upgrade stub that
// records the handover.
func newWaitIdleServer(t *testing.T) (*Server, <-chan string) {
	t.Helper()
	s := newTestServer(t, testToken, false)
	c, _ := releaseMirror(t)
	s.SetUpdateChecker(c)
	stubBinaryVersion(t, "0.160.0")
	handed := make(chan string, 4)
	s.SetUpdateApply(t.TempDir(), func(_ context.Context, binary string) error {
		handed <- binary
		return nil
	})
	return s, handed
}

// submitApply posts an apply with the given wait window and returns the status code.
func submitApply(t *testing.T, s *Server, waitIdleSec int) int {
	t.Helper()
	body, err := json.Marshal(updateApplyRequest{WaitIdleSec: waitIdleSec})
	if err != nil {
		t.Fatal(err)
	}
	resp := rawDo(t, s, http.MethodPost, "/v1/update/apply", testToken, body)
	defer resp.Body.Close()
	return resp.StatusCode
}

// applyPhase polls GET /v1/update for the apply's phase.
func applyPhase(t *testing.T, s *Server) string {
	t.Helper()
	v := getApply(t, s)
	if v.Apply == nil {
		return ""
	}
	return v.Apply.Phase
}

// submitCmdJob submits an exec job for the "self" project without waiting for it and
// returns the snapshot the server answered with (202 when the job did not start yet).
func submitCmdJob(t *testing.T, s *Server, cmd []string) job.JobResult {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: cmd, Cwd: ".", TimeoutSec: 60,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		t.Fatalf("submit job status=%d", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	if created.ID == "" {
		t.Fatal("submit answered no job id")
	}
	return created
}

// waitJobStatus waits for a job to reach a status (read through the API, so it sees
// exactly what a person would).
func waitJobStatus(t *testing.T, s *Server, id, want string) {
	t.Helper()
	wait.Until(t, wait.Timeout(t, 10*time.Second), "job "+id+" to reach "+want, func() bool {
		resp := do(t, s, http.MethodGet, "/v1/jobs/"+id, testToken, nil)
		defer resp.Body.Close()
		var jr job.JobResult
		_ = json.NewDecoder(resp.Body).Decode(&jr)
		return jr.Status == want
	})
}

// TestUpdateApplyPausesBeforePublishingWaitingIdle closes the transition window: the
// seam runs after the public phase is persisted, so a job submitted there must already
// be parked. With the old ordering this submission was queued instead.
func TestUpdateApplyPausesBeforePublishingWaitingIdle(t *testing.T) {
	s, _ := newWaitIdleServer(t)
	setApplyWaitTuning(t, 3600, 20*time.Millisecond)
	bin := testcmd.Path(t)

	blocker := submitCmdJob(t, s, []string{bin, "stdout-lines", "tick", "100", "100ms"})
	waitJobStatus(t, s, blocker.ID, job.StatusRunning)

	published := make(chan struct{})
	release := make(chan struct{})
	oldSeam := applyWaitingIdlePublished
	applyWaitingIdlePublished = func() {
		close(published)
		<-release
	}
	var releaseOnce sync.Once
	stopApply := func() {
		releaseOnce.Do(func() { close(release) })
		wait.Until(t, wait.Timeout(t, 10*time.Second), "the apply waiter to install its cancel function", func() bool {
			s.applyMu.Lock()
			defer s.applyMu.Unlock()
			return s.applyWaitCancel != nil
		})
		resp := rawDo(t, s, http.MethodDelete, "/v1/update/apply", testToken, nil)
		if resp.StatusCode != http.StatusAccepted {
			t.Errorf("cancel status=%d, want %d", resp.StatusCode, http.StatusAccepted)
		}
		resp.Body.Close()
		waitApplyPhase(t, s, applyCancelled)
	}
	t.Cleanup(func() {
		if applyPhase(t, s) == applyWaitingIdle {
			stopApply()
		}
		// The apply goroutine has exited before this write, so the seam cannot race with
		// its next invocation. This cleanup runs before setApplyWaitTuning's cleanup.
		applyWaitingIdlePublished = oldSeam
	})

	if code := submitApply(t, s, 3600); code != http.StatusAccepted {
		t.Fatalf("apply status=%d", code)
	}
	select {
	case <-published:
	case <-time.After(wait.Timeout(t, 10*time.Second)):
		t.Fatal("the apply did not publish waiting_idle")
	}

	parked := submitCmdJob(t, s, []string{bin, "write-files", filepath.Join(t.TempDir(), "parked.txt"), "ran"})
	if parked.Status != job.StatusPausedForUpdate {
		t.Fatalf("job submitted after waiting_idle was published = %s, want %s", parked.Status, job.StatusPausedForUpdate)
	}
	stopApply()
	if err := s.jobs.Cancel(blocker.ID); err != nil {
		t.Fatalf("cancel the blocking job: %v", err)
	}
}

// TestUpdateApplyWaitsForIdleThenRestarts: a running local job keeps the update in
// waiting_idle (naming the job in its progress); when that job ends the update hands the
// server over. New jobs submitted meanwhile are parked, and the update releases them
// after the restart.
func TestUpdateApplyWaitsForIdleThenRestarts(t *testing.T) {
	s, handed := newWaitIdleServer(t)
	setApplyWaitTuning(t, 3600, 20*time.Millisecond)
	bin := testcmd.Path(t)

	blocker := submitCmdJob(t, s, []string{bin, "stdout-lines", "tick", "100", "100ms"})
	waitJobStatus(t, s, blocker.ID, job.StatusRunning)

	if code := submitApply(t, s, 3600); code != http.StatusAccepted {
		t.Fatalf("apply status=%d", code)
	}
	wait.Until(t, wait.Timeout(t, 10*time.Second), "the apply to wait for idle", func() bool {
		return applyPhase(t, s) == applyWaitingIdle
	})
	// Nothing was handed over while a local job was running, and the progress names it.
	v := getApply(t, s)
	if len(v.Apply.Waiting) != 1 || v.Apply.Waiting[0].ID != blocker.ID || v.Apply.Waiting[0].Runner != "local" {
		t.Fatalf("waiting list = %+v", v.Apply.Waiting)
	}
	if v.Apply.WaitDeadline == 0 || v.Apply.WaitTimeoutSec != 3600 {
		t.Fatalf("wait window = %d/%d", v.Apply.WaitDeadline, v.Apply.WaitTimeoutSec)
	}
	select {
	case got := <-handed:
		t.Fatalf("the server was handed over (%s) while %s was running", got, blocker.ID)
	default:
	}
	// A job that arrives during the wait is parked, not refused.
	parked := submitCmdJob(t, s, []string{bin, "write-files", filepath.Join(t.TempDir(), "later.txt"), "ran"})
	if parked.Status != job.StatusPausedForUpdate {
		t.Fatalf("job submitted during the wait = %s, want %s", parked.Status, job.StatusPausedForUpdate)
	}
	if v := getApply(t, s); v.Apply.Paused != 1 {
		t.Fatalf("parked count = %d, want 1", v.Apply.Paused)
	}

	// The blocking job ends (here: cancelled — either way it leaves the census).
	if err := s.jobs.Cancel(blocker.ID); err != nil {
		t.Fatalf("cancel the blocking job: %v", err)
	}
	select {
	case <-handed:
	case <-time.After(wait.Timeout(t, 15*time.Second)):
		t.Fatalf("the update did not hand the server over after the last local job ended (phase %s)", applyPhase(t, s))
	}
	if got := applyPhase(t, s); got != applyRestarting {
		t.Fatalf("phase after the handover = %s", got)
	}
	// The restart is the new server's business: it releases the parked job first.
	s.SetBuildInfo(buildinfo.Info{Version: "0.160.0", GitCommit: "abc1234"})
	s.ResumeUpdateApply(context.Background())
	waitJobStatus(t, s, parked.ID, job.StatusDone)
	if got := applyPhase(t, s); got != applyDone {
		t.Fatalf("phase after the resume = %s", got)
	}
	if owner := s.jobs.UpdatePauseOwner(); owner != "" {
		t.Fatalf("the pause survived the restart: %q", owner)
	}
}

// TestUpdateApplySkipsJobsAWorkerRuns: a job a worker is running survives the restart
// (the next serve adopts it), so the update does not wait for it.
func TestUpdateApplySkipsJobsAWorkerRuns(t *testing.T) {
	s, handed := newWaitIdleServer(t)
	setApplyWaitTuning(t, 3600, 20*time.Millisecond)
	// A worker job: the worker_id marks it as taken over (the census skips a running
	// one; only a queued worker job would block).
	seedJobRow(t, s, jobstore.JobRecord{
		ID: "job-on-worker", ProjectKey: "self", Agent: "exec", Runner: "w1", WorkerID: "w1",
		Status: job.StatusRunning, Cwd: ".", ResultDir: filepath.Join(t.TempDir(), "job-on-worker"),
		StartedAt: time.Now().Unix(),
	})
	if code := submitApply(t, s, 3600); code != http.StatusAccepted {
		t.Fatalf("apply status=%d", code)
	}
	select {
	case <-handed:
	case <-time.After(wait.Timeout(t, 10*time.Second)):
		t.Fatal("the update waited for a job a worker was running")
	}
	if got := applyPhase(t, s); got != applyRestarting {
		t.Fatalf("phase = %s", got)
	}
}

// TestUpdateApplyIdleWaitTimeoutFailsAndReleases: when the machine never goes idle the
// update gives up with a reason a person can act on, and the jobs it parked run again.
func TestUpdateApplyIdleWaitTimeoutFailsAndReleases(t *testing.T) {
	s, handed := newWaitIdleServer(t)
	setApplyWaitTuning(t, 1, 20*time.Millisecond)
	bin := testcmd.Path(t)

	blocker := submitCmdJob(t, s, []string{bin, "stdout-lines", "tick", "200", "100ms"})
	waitJobStatus(t, s, blocker.ID, job.StatusRunning)
	// One second of waiting: the minimum the console offers, shortened by the tuning
	// above (the blocker runs for ~20s, so the deadline is what ends this wait).
	if code := submitApply(t, s, 1); code != http.StatusAccepted {
		t.Fatalf("apply status=%d", code)
	}
	wait.Until(t, wait.Timeout(t, 10*time.Second), "the apply to wait for idle", func() bool {
		return applyPhase(t, s) == applyWaitingIdle
	})
	parked := submitCmdJob(t, s, []string{bin, "write-files", filepath.Join(t.TempDir(), "parked.txt"), "ran"})
	if parked.Status != job.StatusPausedForUpdate {
		t.Fatalf("job submitted during the wait = %s", parked.Status)
	}

	v := waitApplyPhase(t, s, applyFailed)
	if v.WaitDeadline != 0 {
		t.Fatalf("the failed apply kept a deadline: %d", v.WaitDeadline)
	}
	if !strings.Contains(v.Error, "gave up") || !strings.Contains(v.Error, blocker.ID) {
		t.Fatalf("timeout reason = %q, want the job it was waiting for", v.Error)
	}
	select {
	case <-handed:
		t.Fatal("the update restarted the server despite never going idle")
	default:
	}
	// Released: the parked job runs, the pause is off, and the blocker can also finish
	// on its own.
	if err := s.jobs.Cancel(blocker.ID); err != nil {
		t.Fatalf("cancel the blocking job: %v", err)
	}
	waitJobStatus(t, s, parked.ID, job.StatusDone)
	if owner := s.jobs.UpdatePauseOwner(); owner != "" {
		t.Fatalf("the pause outlived the failed apply: %q", owner)
	}
}

// TestUpdateApplyWaitCancelledByAdmin: DELETE /v1/update/apply stops a wait, records the
// apply as cancelled, and releases the jobs it parked; with nothing waiting it is a 409.
func TestUpdateApplyWaitCancelledByAdmin(t *testing.T) {
	s, handed := newWaitIdleServer(t)
	setApplyWaitTuning(t, 3600, 20*time.Millisecond)
	bin := testcmd.Path(t)

	blocker := submitCmdJob(t, s, []string{bin, "stdout-lines", "tick", "200", "100ms"})
	waitJobStatus(t, s, blocker.ID, job.StatusRunning)
	if code := submitApply(t, s, 3600); code != http.StatusAccepted {
		t.Fatalf("apply status=%d", code)
	}
	wait.Until(t, wait.Timeout(t, 10*time.Second), "the apply to wait for idle", func() bool {
		return applyPhase(t, s) == applyWaitingIdle
	})
	parked := submitCmdJob(t, s, []string{bin, "write-files", filepath.Join(t.TempDir(), "parked.txt"), "ran"})

	resp := rawDo(t, s, http.MethodDelete, "/v1/update/apply", testToken, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("cancel status=%d", resp.StatusCode)
	}
	v := waitApplyPhase(t, s, applyCancelled)
	if v.Error == "" {
		t.Fatalf("cancelled apply carries no reason: %+v", v)
	}
	select {
	case <-handed:
		t.Fatal("the server was handed over after the wait was cancelled")
	default:
	}
	if err := s.jobs.Cancel(blocker.ID); err != nil {
		t.Fatalf("cancel the blocking job: %v", err)
	}
	waitJobStatus(t, s, parked.ID, job.StatusDone)
	// A second cancel has nothing to stop (and must not panic on a closed channel).
	if resp := rawDo(t, s, http.MethodDelete, "/v1/update/apply", testToken, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("second cancel status=%d, want 409", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
}

// TestUpdateApplyResumeReleasesJobsTheDeadUpdateParked: a server that starts up while an
// apply was still waiting for idle must not leave those jobs parked — the wait died with
// its process, so it releases them and fails that apply with a reason.
func TestUpdateApplyResumeReleasesJobsTheDeadUpdateParked(t *testing.T) {
	s := newTestServer(t, testToken, false)
	c, _ := releaseMirror(t)
	s.SetUpdateChecker(c)
	dir := t.TempDir()
	s.SetUpdateApply(dir, nil)
	bin := testcmd.Path(t)

	// What the previous process left behind: the pause (as it had it) plus a parked job
	// and an apply.json in its waiting phase.
	s.jobs.PauseStartsForUpdate("apply-dead")
	parked := submitCmdJob(t, s, []string{bin, "write-files", filepath.Join(t.TempDir(), "parked.txt"), "ran"})
	if parked.Status != job.StatusPausedForUpdate {
		t.Fatalf("parked job = %s", parked.Status)
	}
	now := time.Now().Unix()
	s.saveApply(&applyFile{ApplyState: ApplyState{
		ID: "apply-dead", From: "0.153.1", Version: "0.160.0", Phase: applyWaitingIdle,
		StartedAt: now, WaitDeadline: now + 3600, WaitTimeoutSec: 3600,
	}, WaitStartedAt: now})

	s.ResumeUpdateApply(context.Background())

	v := getApply(t, s)
	if v.Apply.Phase != applyFailed || !strings.Contains(v.Apply.Error, "restarted while the update was waiting") {
		t.Fatalf("apply after the restart = %+v", v.Apply)
	}
	if owner := s.jobs.UpdatePauseOwner(); owner != "" {
		t.Fatalf("the pause was inherited by the new server: %q", owner)
	}
	waitJobStatus(t, s, parked.ID, job.StatusDone)
	if n, err := s.jobs.CountUpdatePaused(); err != nil || n != 0 {
		t.Fatalf("parked jobs left behind: %d, %v", n, err)
	}
}

// waitApplyPhase polls until the apply reaches want and returns it.
func waitApplyPhase(t *testing.T, s *Server, want string) ApplyState {
	t.Helper()
	var last ApplyState
	wait.Until(t, wait.Timeout(t, 15*time.Second), "the apply to reach "+want, func() bool {
		v := getApply(t, s)
		if v.Apply == nil {
			return false
		}
		last = *v.Apply
		return last.Phase == want
	})
	return last
}

// seedJobRow inserts a job row directly (a job some other process owns).
func seedJobRow(t *testing.T, s *Server, rec jobstore.JobRecord) {
	t.Helper()
	if err := s.jobs.Meta().UpsertJob(rec); err != nil {
		t.Fatalf("seed job %s: %v", rec.ID, err)
	}
}
