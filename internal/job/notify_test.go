package job

import (
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// TestSubmitNotifyRegistersTheTargetWatch: the notification's pending record is the
// target session's WATCH (gofer-3db4) — Submit has to create it, or the finished job
// would have nothing to claim. It is an explicit target here (an env-detected /
// supervisor session), not the job's source.
func TestSubmitNotifyRegistersTheTargetWatch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	svc := newTestService(t, root)
	_, err := svc.meta.UpsertAgentSession(jobstore.AgentSession{
		SessionID: "notify-target", Agent: "claude", ProjectKey: "self",
		Runner: "local", Cwd: root, CallerID: "caller-a",
	})
	assert.NoErr(t, err)

	res, err := svc.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"},
		Cwd: ".", CallerID: "caller-a", Notify: true, NotifySession: "notify-target",
	})
	assert.NoErr(t, err)
	_, ok := svc.Wait(res.ID)
	assert.True(t, ok)
	assert.True(t, res.Notify)
	assert.Eq(t, "notify-target", res.NotifySession)

	watches, err := svc.meta.ListSessionJobWatches("notify-target")
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(watches))
	assert.Eq(t, res.ID, watches[0].JobID)

	// The request rides request_json, so a row read back still knows whom to tell
	// (the persisted read path is what a restarted server notifies from).
	rec, ok, err := svc.meta.GetJob(res.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	rebuilt := fromRecord(rec)
	assert.True(t, rebuilt.Notify)
	assert.Eq(t, "notify-target", rebuilt.NotifySession)
}

// TestSubmitNotifyDefaultsToTheSourceSession: with an authenticated source session
// bound, `--notify` needs no explicit target — the source session's own watch (added
// for every source-bound job) is the pending record.
func TestSubmitNotifyDefaultsToTheSourceSession(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	svc := newTestService(t, root)
	_, err := svc.meta.UpsertAgentSession(jobstore.AgentSession{
		SessionID: "notify-source", Agent: "claude", ProjectKey: "self",
		Runner: "local", Cwd: root, CallerID: "caller-a",
	})
	assert.NoErr(t, err)

	res, err := svc.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"},
		Cwd: ".", CallerID: "caller-a", Notify: true, SourceSessionID: "notify-source",
	})
	assert.NoErr(t, err)
	_, ok := svc.Wait(res.ID)
	assert.True(t, ok)
	watches, err := svc.meta.ListSessionJobWatches("notify-source")
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(watches), "the source watch and the notify record are the same row")
}

// TestSubmitNotifyWithoutASessionIsRefused: gofer cannot guess whom to tell, and a
// silently ignored --notify would look like it fired.
func TestSubmitNotifyWithoutASessionIsRefused(t *testing.T) {
	t.Parallel()
	svc := newTestService(t, t.TempDir())
	_, err := svc.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"},
		Cwd: ".", CallerID: "caller-a", Notify: true,
	})
	assert.Err(t, err)
	jobs, lerr := svc.ListJobs(ListOpts{Project: "self"})
	assert.NoErr(t, lerr)
	assert.Eq(t, 0, len(jobs), "a refused notify must not create a job")
}

// TestSubmitNotifyUnknownSessionIsRefused: an explicit target that this server does
// not know has no address, so the submit is refused instead of accepted-and-lost.
func TestSubmitNotifyUnknownSessionIsRefused(t *testing.T) {
	t.Parallel()
	svc := newTestService(t, t.TempDir())
	_, err := svc.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"},
		Cwd: ".", CallerID: "caller-a", NotifySession: "not-a-session",
	})
	assert.Err(t, err)
}

// TestNotifyTargetOwnedByAnotherCallerIsRefused mirrors the source-session rule: a
// caller may not aim a notice at somebody else's terminal.
func TestNotifyTargetOwnedByAnotherCallerIsRefused(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	svc := newTestService(t, root)
	_, err := svc.meta.UpsertAgentSession(jobstore.AgentSession{
		SessionID: "notify-foreign", Agent: "claude", ProjectKey: "self",
		Runner: "local", Cwd: root, CallerID: "caller-b",
	})
	assert.NoErr(t, err)
	_, err = svc.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"},
		Cwd: ".", CallerID: "caller-a", Notify: true, NotifySession: "notify-foreign",
	})
	assert.Err(t, err)
}

// TestInternalSubmitDegradesAnUnusableNotify instead of refusing it (gofer-3db4): a
// scheduled run / plan dispatch / continuation asks for a notification nobody can
// address — the work must still run, and the job's timeline must say why the notice
// was dropped. The same target from a CALLER is a 400 (see
// TestNotifyTargetOwnedByAnotherCallerIsRefused).
func TestInternalSubmitDegradesAnUnusableNotify(t *testing.T) {
	t.Parallel()
	t.Run("no target at all", func(t *testing.T) {
		t.Parallel()
		svc := newTestService(t, t.TempDir())
		res, err := svc.Submit(JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"},
			Cwd: ".", CallerID: "caller-a", Notify: true, NotifyDegradable: true,
		})
		assert.NoErr(t, err)
		_, ok := svc.Wait(res.ID)
		assert.True(t, ok)
		assert.False(t, res.Notify)
		assert.Eq(t, "", res.NotifySession)
		assertNotifyDroppedReason(t, svc, res.ID, "no session to tell")
	})

	t.Run("a target owned by another caller", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		svc := newTestService(t, root)
		_, err := svc.meta.UpsertAgentSession(jobstore.AgentSession{
			SessionID: "internal-notify-foreign", Agent: "claude", ProjectKey: "self",
			Runner: "local", Cwd: root, CallerID: "caller-b",
		})
		assert.NoErr(t, err)
		res, err := svc.Submit(JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"},
			Cwd: ".", CallerID: "caller-a", Notify: true, NotifySession: "internal-notify-foreign",
			NotifyDegradable: true,
		})
		assert.NoErr(t, err)
		_, ok := svc.Wait(res.ID)
		assert.True(t, ok)
		assert.False(t, res.Notify)
		assertNotifyDroppedReason(t, svc, res.ID, "belongs to another caller")
	})
}

// assertNotifyDroppedReason fails unless jobID carries a job.notify_dropped event whose
// reason contains want.
func assertNotifyDroppedReason(t *testing.T, svc *Service, jobID, want string) {
	t.Helper()
	events, err := svc.ListJobEvents(jobID, 0)
	assert.NoErr(t, err)
	for _, e := range events {
		if e.Type != EventNotifyDropped {
			continue
		}
		if !strings.Contains(e.Detail, want) {
			t.Fatalf("notify_dropped detail = %s, want %q", e.Detail, want)
		}
		return
	}
	t.Fatalf("no %s event on job %s (%d events)", EventNotifyDropped, jobID, len(events))
}

// TestJobEndedHookFiresForTerminalAndParkedJobs pins the seam's meaning: a job that
// ends (including one PARKED for review, which the terminal hook deliberately does
// not see) reports its snapshot to the ended-hooks exactly once per ending.
func TestJobEndedHookFiresForTerminalAndParkedJobs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	svc := newTestService(t, root)
	ended := make(chan JobResult, 8)
	svc.OnJobEnded(func(r JobResult) { ended <- r })

	res, err := svc.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"},
		Cwd: ".", CallerID: "caller-a",
	})
	assert.NoErr(t, err)
	_, ok := svc.Wait(res.ID)
	assert.True(t, ok)
	wait.Until(t, 10*time.Second, "the done job reaches the ended-hook", func() bool {
		return drainEndedUntil(ended, res.ID)
	})

	parked, err := svc.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"},
		Cwd: ".", CallerID: "caller-a", Review: true,
	})
	assert.NoErr(t, err)
	_, ok = svc.Wait(parked.ID)
	assert.True(t, ok)
	rec, ok, err := svc.meta.GetJob(parked.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, StatusNeedsReview, rec.Status)
	wait.Until(t, 10*time.Second, "the review-parked job reaches the ended-hook", func() bool {
		return drainEndedUntil(ended, parked.ID)
	})
}

// drainEndedUntil reports whether the hook reported jobID, discarding the others
// (the two jobs above run in the same service).
func drainEndedUntil(ch chan JobResult, jobID string) bool {
	for {
		select {
		case got := <-ch:
			if got.ID == jobID {
				return true
			}
		default:
			return false
		}
	}
}
