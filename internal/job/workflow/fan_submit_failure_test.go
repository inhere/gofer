package workflow

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	job "github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// fanSubmitFaultOps fails the n-th Submit and records the cancels the engine issues: the
// fault-injection seam for a fan-out step whose submission fails MID-WAY (gofer-ourk: fan 1
// starts, fan 2's submit fails — e.g. its `worktree add` lost a race with another machine).
type fanSubmitFaultOps struct {
	JobOps
	failAt int
	// afterCancel, when set, runs right after a Cancel — the test uses it to fire the
	// cancelled fan's finish hook SYNCHRONOUSLY (a hook that lands before the engine had
	// marked the header terminal would re-start the generation inside the cancel itself).
	afterCancel func()
	mu          sync.Mutex
	// submits counts every Submit call, started holds the ids that really launched.
	submits int
	started []string
	cancels []string
}

func (o *fanSubmitFaultOps) Submit(req job.JobRequest) (job.JobResult, error) {
	o.mu.Lock()
	o.submits++
	n := o.submits
	o.mu.Unlock()
	if n == o.failAt {
		return job.JobResult{}, errors.New("simulated worktree add race")
	}
	res, err := o.JobOps.Submit(req)
	if err == nil {
		o.mu.Lock()
		o.started = append(o.started, res.ID)
		o.mu.Unlock()
	}
	return res, err
}

func (o *fanSubmitFaultOps) Cancel(id string) error {
	o.mu.Lock()
	o.cancels = append(o.cancels, id)
	o.mu.Unlock()
	err := o.JobOps.Cancel(id)
	if o.afterCancel != nil {
		o.afterCancel()
	}
	return err
}

func (o *fanSubmitFaultOps) snapshot() (started, cancels []string, submits int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.started...), append([]string(nil), o.cancels...), o.submits
}

// TestFanSubmitPartialFailureCancelsStartedFans is the regression for gofer-ourk ①: fan 2 of
// a 3-fan step fails to submit, which fails the whole workflow — so fan 1, already running,
// used to keep running as an orphan the operator had to cancel by hand. It must be cancelled
// as part of the failed step instead.
func TestFanSubmitPartialFailureCancelsStartedFans(t *testing.T) {
	t.Parallel()
	bin := testcmd.Path(t) // G053: resolve the helper before any timing starts
	var ops *fanSubmitFaultOps
	e := newTestEngineWrapped(t, t.TempDir(), func(base JobOps) JobOps {
		ops = &fanSubmitFaultOps{JobOps: base, failAt: 2}
		return ops
	})

	st := StepSpec{
		Name: "fan", ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{bin, "sleep", "60s"}, Cwd: ".", TimeoutSec: 60,
		FanOut: 3, Join: "all",
	}
	// The cancelled fan's finish hook fires INSIDE the cancel, i.e. at the earliest moment
	// the engine could re-drive Advance. The header must already be terminal by then, or that
	// re-drive would find a running workflow with 1 of 3 fans and (re)start the generation —
	// the very orphans this fix removes.
	ops.afterCancel = func() {
		if wfs, lerr := e.meta.ListWorkflows(jobstore.WorkflowRunning, 10); lerr == nil {
			for _, w := range wfs {
				e.Advance(w.ID)
			}
		}
	}
	_, err := e.SubmitWorkflow(Spec{Project: "self", Steps: []StepSpec{st}}, "alice")
	if err == nil {
		t.Fatal("want a mid-step fan submit failure to fail the workflow submit")
	}
	if !strings.Contains(err.Error(), "fan 2") {
		t.Fatalf("the error must name the failing fan, got %q", err.Error())
	}

	started, cancels, submits := ops.snapshot()
	if submits != 2 {
		t.Fatalf("Submit calls = %d, want 2 (fan 2 failed, fan 3 never started)", submits)
	}
	if len(started) != 1 {
		t.Fatalf("only fan 1 can have started, got %v", started)
	}
	if len(cancels) != 1 || cancels[0] != started[0] {
		t.Fatalf("started fan %s must be cancelled exactly once, cancels = %v", started[0], cancels)
	}

	// The job really stops: a fan left running is precisely the orphan this fixes.
	first := started[0]
	wait.Until(t, 10*time.Second, "the started fan reaches cancelled", func() bool {
		rec, ok, gerr := e.meta.GetJob(first)
		return gerr == nil && ok && rec.Status == job.StatusCancelled
	})

	// The header is failed and the failed step holds exactly that one (cancelled) job.
	wfs, lerr := e.meta.ListWorkflows(jobstore.WorkflowFailed, 10)
	if lerr != nil {
		t.Fatalf("ListWorkflows: %v", lerr)
	}
	if len(wfs) != 1 {
		t.Fatalf("want exactly 1 failed workflow, got %d", len(wfs))
	}
	jobs, jerr := e.meta.ListWorkflowJobs(wfs[0].ID)
	if jerr != nil {
		t.Fatalf("ListWorkflowJobs: %v", jerr)
	}
	if len(jobs) != 1 || jobs[0].ID != first {
		t.Fatalf("the failed step must hold only the started fan, got %+v", jobs)
	}
	if !strings.Contains(wfs[0].Error, "fan 2") {
		t.Fatalf("the workflow error must name the failing fan, got %q", wfs[0].Error)
	}
}
