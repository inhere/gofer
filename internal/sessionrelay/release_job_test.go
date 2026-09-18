package sessionrelay

import (
	"context"
	"errors"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

// terminalJob writes the takeover job's terminal row: the auto-release reads the
// job's status from the store to name the reason ("job_done" / "job_failed"), so a
// test that drives the hook has to have one on disk.
func terminalJob(t *testing.T, s *Service, jobID, status string) {
	t.Helper()
	if err := s.store.UpsertJob(jobstore.JobRecord{ID: jobID, ProjectKey: "self", Status: status}); err != nil {
		t.Fatalf("upsert job %s: %v", jobID, err)
	}
}

// TestReleaseTakeoverForJobReleasesHandedOffSession is the HOOK path (SUP-02 R1):
// when the pty job that HOLDS a session reaches a terminal state the server hands
// the session back — WITHOUT cancelling that job (it is already gone; the row only
// names the reason code) — and announces session.takeover_released so an operator
// (and a webhook subscribing to it) learns the original terminal relays again.
func TestReleaseTakeoverForJobReleasesHandedOffSession(t *testing.T) {
	s := newSvc(t)
	to := &fakeTakeoverer{res: TakeoverResult{JobID: "job-t1"}}
	s.SetTakeoverer(to)
	nf := &fakeNotifier{}
	s.SetNotifier(nf)
	takeoverSession(t, s, "sid-auto-release")

	_, err := s.Deliver(context.Background(), "sid-auto-release", "carry on", "alice", true)
	assert.NoErr(t, err)
	handed, ok, err := s.store.GetAgentSession("sid-auto-release")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, jobstore.SessionHandedOff, handed.State)

	terminalJob(t, s, "job-t1", "done")
	released, err := s.ReleaseTakeoverForJob(context.Background(), "job-t1")
	assert.NoErr(t, err)
	assert.True(t, released)

	a, ok, err := s.store.GetAgentSession("sid-auto-release")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, jobstore.SessionIdle, a.State)
	assert.Eq(t, "", a.HandedOffJobID)
	assert.Eq(t, int64(0), a.HandedOffAt)
	// No cancel: the job already ended, and a cancel of a terminal job would only
	// add a failure the release must never depend on.
	assert.Len(t, to.cancels, 0)
	assert.Len(t, nf.released, 1)
	assert.Eq(t, "sid-auto-release:job-t1:job_done", nf.released[0])

	// A second terminal notification for the same job (a retried hook, a duplicate
	// event) finds nothing left to release — and must not announce a phantom release.
	released, err = s.ReleaseTakeoverForJob(context.Background(), "job-t1")
	assert.NoErr(t, err)
	assert.False(t, released)
	assert.Len(t, nf.released, 1)
}

// TestReleaseTakeoverForJobIgnoresOtherJobs pins the hook's narrowness: only the
// job that HOLDS a session releases it. A job that merely finished (every job the
// server runs) changes nothing, and a session a human already released manually is
// left exactly as it is.
func TestReleaseTakeoverForJobIgnoresOtherJobs(t *testing.T) {
	s := newSvc(t)
	to := &fakeTakeoverer{res: TakeoverResult{JobID: "job-holder"}}
	s.SetTakeoverer(to)
	nf := &fakeNotifier{}
	s.SetNotifier(nf)
	takeoverSession(t, s, "sid-other")
	_, err := s.Deliver(context.Background(), "sid-other", "carry on", "alice", true)
	assert.NoErr(t, err)

	released, err := s.ReleaseTakeoverForJob(context.Background(), "job-plain")
	assert.NoErr(t, err)
	assert.False(t, released)
	held, ok, err := s.store.GetAgentSession("sid-other")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, jobstore.SessionHandedOff, held.State)
	assert.Eq(t, "job-holder", held.HandedOffJobID)

	// The human released it first: the terminal hook must not touch it again (nor
	// resurrect the handoff), and nothing is announced.
	_, err = s.ReleaseTakeover(context.Background(), "sid-other")
	assert.NoErr(t, err)
	released, err = s.ReleaseTakeoverForJob(context.Background(), "job-holder")
	assert.NoErr(t, err)
	assert.False(t, released)
	assert.Len(t, nf.released, 0)
}

// TestTakeoverFallbackOnRunnerError pins path B's EXTENDED reach (SUP-02 R1): a
// runner-level failure of the injection path — the dispatch errored, or the script
// itself could not run (any exit code the pane check does not own) — is also worth
// retrying as a takeover, because path A never got to look at a pane. A pane that
// is genuinely BUSY stays a refusal: the human is using that terminal, and moving
// the conversation away from them would be the wrong answer.
func TestTakeoverFallbackOnRunnerError(t *testing.T) {
	s := newSvc(t)
	to := &fakeTakeoverer{res: TakeoverResult{JobID: "job-fb"}}
	s.SetTakeoverer(to)

	// The injector could not even submit the job: no reason code about the pane
	// exists, so A's own report is only "the runner said no".
	s.SetInjector(&fakeInjector{err: errors.New("runner offline")})
	paneSession(t, s, "sid-runner-err", "%11")
	res, err := s.Deliver(context.Background(), "sid-runner-err", "hi", "alice", true)
	assert.NoErr(t, err)
	assert.Eq(t, PathTakeover, res.Path)

	// The same failure as an exit code: the injection script ran but failed for a
	// reason that is not the pane's state.
	s.SetInjector(&fakeInjector{res: InjectResult{JobID: "job-script", ExitCode: 1, Output: "sh: tmux: not found"}})
	paneSession(t, s, "sid-runner-exit", "%12")
	res, err = s.Deliver(context.Background(), "sid-runner-exit", "hi", "alice", true)
	assert.NoErr(t, err)
	assert.Eq(t, PathTakeover, res.Path)
	assert.Len(t, to.reqs, 2)

	// pane_busy is NOT a takeover trigger, even though it also arrives through the
	// injection job. The human has that pane in an editor; the reply is refused.
	s.SetInjector(&fakeInjector{res: InjectResult{JobID: "job-busy", ExitCode: 4, Output: "pane_busy:vim\n"}})
	paneSession(t, s, "sid-busy-pane", "%13")
	_, err = s.Deliver(context.Background(), "sid-busy-pane", "hi", "alice", true)
	assert.True(t, errors.Is(err, ErrUndeliverable))
	assert.Eq(t, "inject_failed:pane_busy:vim", DeliverReason(err))
	assert.Len(t, to.reqs, 2)
}
