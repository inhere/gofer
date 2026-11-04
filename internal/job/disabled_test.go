package job

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// 停用语义与配额（配置入库 P1，设计 docs/design/2026-11-03-config-into-db-design.md §停用，
// 计划 T5）：
//
//   - a disabled caller owns no new job, with or without a ceiling, and the refusal is judged
//     BEFORE the ceiling (a caller without one would otherwise be admitted);
//   - only a delivery and its check may finish for a disabled owner (D7);
//   - a job admitted before the disable is re-judged once it is through the queue, fails there
//     and is never retried;
//   - the derived paths (inherit, plan dispatch, hold release) refuse the same owner;
//   - a changed concurrency quota installs a new gate, so it applies without a restart.

// storedTokenHash is a syntactically valid stored caller hash. The job layer never reads it —
// the merge has already held the row to it — and the shape keeps the fixtures honest about what
// a stored caller looks like.
const storedTokenHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// cfgWithDisabledCaller returns a config whose ONLY caller is a disabled stored caller with no
// ceiling at all: the shape whose admission used to read as "no ceiling, nothing to check".
func cfgWithDisabledCaller(id string) *config.Config {
	cfg := &config.Config{}
	cfg.Server.Callers = []config.CallerConfig{{
		ID: id, Source: config.CallerSourceDB, TokenHash: storedTokenHash, Disabled: true,
	}}
	return cfg
}

// disableStoredCaller reloads a RUNNING service with caller id disabled — what the console's
// disable does, without a restart.
func disableStoredCaller(t *testing.T, s *Service, id string) {
	t.Helper()
	cfg := s.Config()
	cfg.Server.Callers = []config.CallerConfig{{
		ID: id, Source: config.CallerSourceDB, TokenHash: storedTokenHash,
		Disabled: true, MaxConcurrentJobs: 1,
	}}
	s.Reload(cfg)
}

// TestAdmitCeilingRefusesDisabledOwnerWithoutCeiling is the reverse-validated rule of plan T5: the
// disable is judged BEFORE the ceiling lookup, so an owner with no ceiling cannot slip through the
// "nothing to check" continue. The control caller (same id, not disabled) is admitted, so a case
// that simply refused everything could not pass.
func TestAdmitCeilingRefusesDisabledOwnerWithoutCeiling(t *testing.T) {
	t.Parallel()
	req := JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, OriginCaller: "dbuser"}

	open := &config.Config{}
	open.Server.Callers = []config.CallerConfig{{ID: "dbuser", Source: config.CallerSourceDB, TokenHash: storedTokenHash}}
	assert.NoErr(t, admitCeiling(open, req), "a caller without a ceiling and not disabled is admitted")

	err := admitCeiling(cfgWithDisabledCaller("dbuser"), req)
	assert.True(t, errors.Is(err, ErrCeilingDenied), "a disabled owner must be refused: %v", err)
	assert.True(t, strings.Contains(err.Error(), "disabled"), "the refusal names the reason: %v", err)

	// An EXTRA owner (a plan item's author) is judged exactly like the request's own.
	extra := req
	extra.OriginCaller = "alice"
	extra.ExtraCeilingOwners = []string{"dbuser"}
	assert.True(t, errors.Is(admitCeiling(cfgWithDisabledCaller("dbuser"), extra), ErrCeilingDenied),
		"a disabled EXTRA owner must be refused too")
}

// TestDisabledOwnerReleasesOnlyTheClosingInternalExecKinds: D7's list — the delivery of finished
// work and the read-only check of it — is released for a disabled owner; everything else an
// internal exec does starts NEW work and is refused.
func TestDisabledOwnerReleasesOnlyTheClosingInternalExecKinds(t *testing.T) {
	t.Parallel()
	cfg := cfgWithDisabledCaller("dbuser")
	base := JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", OriginCaller: "dbuser"}

	for _, kind := range []string{InternalExecDelivery, InternalExecDeliveryCheck} {
		req := base
		req.InternalExec = kind
		assert.NoErr(t, admitCeiling(cfg, req), "%s must be allowed to finish", kind)
	}
	for _, kind := range []string{InternalExecRelay, InternalExecTrackerSync, InternalExecSummarizer, InternalExecWorkflowStep} {
		req := base
		req.InternalExec = kind
		assert.True(t, errors.Is(admitCeiling(cfg, req), ErrCeilingDenied), "%s starts new work and must be refused", kind)
	}
}

// TestDisabledRecheckOwnersFollowsAdmission: execute re-judges exactly the owners admission judged,
// and none for a system job or a released internal exec.
func TestDisabledRecheckOwnersFollowsAdmission(t *testing.T) {
	t.Parallel()
	req := JobRequest{OriginCaller: "dbuser", ExtraCeilingOwners: []string{"alice", "dbuser"}}
	assert.Eq(t, []string{"dbuser", "alice"}, disabledRecheckOwners(req))

	assert.Eq(t, 0, len(disabledRecheckOwners(JobRequest{SystemJob: SystemJobCleanup})), "a system job carries no owner")
	release := req
	release.InternalExec = InternalExecDelivery
	assert.Eq(t, 0, len(disabledRecheckOwners(release)), "a released internal exec is not re-judged")
	pending := req
	pending.InternalExec = InternalExecWorkflowStep
	assert.Eq(t, []string{"dbuser", "alice"}, disabledRecheckOwners(pending), "a workflow step starts new work")
}

// TestInheritCeilingOwnerRefusesDisabledChain: a derivative whose source row is gone is refused
// while the chain still names a DISABLED caller, even one with no ceiling.
func TestInheritCeilingOwnerRefusesDisabledChain(t *testing.T) {
	t.Parallel()
	if _, err := inheritCeilingOwner(cfgWithDisabledCaller("dbuser"), nil, "dbuser"); !errors.Is(err, ErrCeilingDenied) {
		t.Fatalf("disabled chain caller: err = %v, want ErrCeilingDenied", err)
	}
	open := &config.Config{}
	open.Server.Callers = []config.CallerConfig{{ID: "dbuser"}}
	if got, err := inheritCeilingOwner(open, nil, "dbuser"); err != nil || got != "" {
		t.Fatalf("enabled unrestricted chain caller: %q %v, want empty/nil", got, err)
	}
}

// TestCallerSemaphoreFollowsTheConfiguredLimit is the reverse-validated quota rule of plan T5: a
// changed limit installs a NEW gate instead of keeping the capacity frozen at creation, while a job
// that captured the old one releases that one (so no slot is leaked or freed twice).
func TestCallerSemaphoreFollowsTheConfiguredLimit(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())

	first := s.callerSemaphore("dbuser", 1)
	assert.Eq(t, 1, cap(first))
	assert.Eq(t, first, s.callerSemaphore("dbuser", 1), "the same limit keeps the same gate")
	first <- struct{}{} // an in-flight job holds the caller's only slot

	second := s.callerSemaphore("dbuser", 2)
	assert.Eq(t, 2, cap(second), "the new quota is the new capacity")
	assert.True(t, first != second, "a changed limit must install a new gate")
	assert.Eq(t, 0, len(second), "the new gate starts empty")

	// The in-flight job releases the gate it captured.
	<-first
	assert.Eq(t, 0, len(first))
	assert.Eq(t, 0, len(second), "releasing the old gate must not touch the new one")

	assert.True(t, s.callerSemaphore("dbuser", 0) == nil, "an unlimited quota is ungated")
}

// TestFailureDecisionClassifiesDisabledCaller: a job refused for a disabled owner is its own
// failure class and is taken over by nobody.
func TestFailureDecisionClassifiesDisabledCaller(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())
	dec := s.failureDecision(JobResult{Status: StatusFailed, Error: errCallerDisabledFor("dbuser").Error()})
	assert.Eq(t, FailureClassCallerDisabled, dec.Class)
	assert.False(t, dec.AutoResume, "a disabled caller's job is not continued")
	assert.Eq(t, "", dec.Fallback, "a disabled caller's job is not handed to another agent")
}

// TestQueuedJobOfDisabledCallerFailsAndIsNotRetried: a job admitted while its caller was enabled
// waits for the caller's concurrency slot; the caller is disabled in between; the job gets the slot
// and is refused there — it never runs, and the refusal is permanent (no retry row).
func TestQueuedJobOfDisabledCallerFailsAndIsNotRetried(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newHoldService(t, root, func(cfg *config.Config) {
		cfg.Server.Callers = []config.CallerConfig{{
			ID: "dbuser", Source: config.CallerSourceDB, TokenHash: storedTokenHash, MaxConcurrentJobs: 1,
		}}
	})
	sem := s.callerSemaphore("dbuser", 1)
	sem <- struct{}{} // the slot a job already running holds

	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{testcmd.Path(t), "write-files", filepath.Join(root, "probe-queued.txt"), "ran"},
		Cwd: ".", TimeoutSec: 60, CallerID: "dbuser", OriginCaller: "dbuser",
		Retry: &RetryPolicy{MaxAttempts: 3},
	})
	assert.NoErr(t, err)
	wait.For(t, 10*time.Second, "the job queues on the caller's only slot", func() (bool, any) {
		n := s.semWaitersOn(sem)
		return n == 1, n
	})

	// The operator disables the caller while its job is queued; the slot then frees.
	disableStoredCaller(t, s, "dbuser")
	<-sem

	final, ok := s.Wait(res.ID)
	assert.True(t, ok, "the queued job must end")
	assert.Eq(t, StatusFailed, final.Status, "a job of a disabled caller fails instead of running")
	assert.Eq(t, FailureClassCallerDisabled, final.FailureClass, "the refusal is its own failure class")
	assert.True(t, strings.Contains(final.Error, "caller disabled"), "the error names the reason: %q", final.Error)
	// The class is what stops maybeRetryJob; the timeline must show no scheduled retry either.
	assert.Eq(t, 0, countEvents(eventTypes(t, s, res.ID), EventJobRetryScheduled), "a disabled caller's job is not retried")
}

// TestDispatchTodoOfDisabledCallerIsRefused drives the PLAN DISPATCH entry: a ready item dispatched
// for a disabled caller starts nothing and stores the refusal as its dispatch error.
func TestDispatchTodoOfDisabledCallerIsRefused(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newDispatchService(t, root)
	seedDispatchPlan(t, s,
		jobstore.Plan{PlanID: "pl-dis", Title: "plan", ProjectKey: "self"},
		jobstore.PlanTodo{TodoID: "td-dis", Title: "item", Assignee: "omp", Status: jobstore.TodoReady})
	disableStoredCaller(t, s, "ci-bot")

	disp, err := s.DispatchTodo("td-dis", "ci-bot")
	assert.NoErr(t, err)
	assert.True(t, disp.Job == nil, "a refused dispatch must start no job")
	rec, ok, err := s.Meta().GetTodo("td-dis")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.True(t, strings.Contains(rec.DispatchError, "disabled"), "dispatch_error = %q", rec.DispatchError)
	assert.Eq(t, jobstore.TodoReady, rec.Status, "the item keeps its status for a later dispatch")
	if jobs, lerr := s.Meta().ListJobs(jobstore.ListQuery{}); lerr != nil {
		t.Fatalf("ListJobs: %v", lerr)
	} else {
		assert.Eq(t, 0, len(jobs), "a refused dispatch created a job")
	}
}

// TestApproveHeldJobOfDisabledCallerIsRefused drives the HOLD RELEASE entry: the approval
// re-submits the held request through admission, and a caller disabled in between is refused there
// — the held work never runs.
func TestApproveHeldJobOfDisabledCallerIsRefused(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	s := newHoldService(t, root, nil)
	probe := filepath.Join(root, "probe-disabled.txt")
	req := probeRequest(bin, probe)
	req.CallerID, req.OriginCaller = "dbuser", "dbuser"
	held := mustHold(t, s, req, probe)

	disableStoredCaller(t, s, "dbuser")
	if _, err := s.ApproveJob(held.ID, "root", "go"); !errors.Is(err, ErrCeilingDenied) {
		t.Fatalf("ApproveJob of a disabled caller's hold: err = %v, want ErrCeilingDenied", err)
	}
	assertNoProbe(t, probe)
}
