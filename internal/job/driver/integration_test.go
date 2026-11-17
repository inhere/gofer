package driver

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// newRealService builds a job.Service over real local jobs (the testcmd agents `ok` and
// `boom`) with the plan driver wired in the way core wires it. The service is drained
// before the store closes (G051).
func newRealService(t *testing.T, wire func(*job.Service) job.PlanDriver) *job.Service {
	t.Helper()
	root := t.TempDir()
	bin := testcmd.Path(t)
	agentOf := func(args ...string) config.AgentConfig {
		return config.AgentConfig{Type: agent.TypeCLIAgent, Command: bin, Args: args}
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"ok", "boom"}, AllowedRunners: []string{"local"}},
		},
		Agents: map[string]config.AgentConfig{
			"ok":   agentOf("argv", "{{prompt}}"),
			"boom": agentOf("exit", "1"),
		},
	}
	meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = meta.Close() })
	svc := job.NewService(cfg, project.NewRegistry(cfg, ""), agent.NewRegistry(cfg),
		map[string]runner.Runner{localrunner.Name: localrunner.New()}, meta, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 10*time.Second))
		defer cancel()
		if err := svc.Drain(ctx); err != nil {
			t.Errorf("teardown drain: %v", err)
		}
	})
	if wire != nil {
		svc.SetPlanDriver(wire(svc))
	}
	return svc
}

// runChain seeds a chain whose agents are given in order (A -> B -> C), optionally turns
// the driver on, runs the plan, waits for it to settle (done or blocked) and returns the
// service.
func runChain(t *testing.T, driverOn bool, agents ...string) *job.Service {
	t.Helper()
	var eng *Engine
	svc := newRealService(t, func(s *job.Service) job.PlanDriver {
		eng = NewEngine(s)
		return eng
	})
	assert.NoErr(t, svc.Meta().InsertPlan(jobstore.Plan{PlanID: "plan-it", Title: "it", Status: jobstore.PlanOpen, ProjectKey: "self", CreatedAt: 1, UpdatedAt: 1}))
	prev := ""
	for i, a := range agents {
		td := jobstore.PlanTodo{
			TodoID: fmt.Sprintf("td-%d", i), PlanID: "plan-it", Title: a, Status: jobstore.TodoPending,
			Assignee: a, Auto: true, Sort: (i + 1) * 10, CreatedAt: 1, UpdatedAt: 1,
		}
		if prev != "" {
			td.After = []string{prev}
		}
		prev = td.TodoID
		assert.NoErr(t, svc.Meta().InsertTodo(td))
	}
	if driverOn {
		assert.NoErr(t, svc.EnablePlanDriver("plan-it", "", job.PlanDriverBudget{}))
	}
	_, err := svc.RunPlan("plan-it", "alice")
	assert.NoErr(t, err)
	wait.For(t, 30*time.Second, "plan settled", func() (bool, any) {
		p, _, _ := svc.Meta().GetPlan("plan-it")
		rows, _ := svc.Meta().ListPlanEvents(10)
		return (p.Status == jobstore.PlanDone || p.Status == jobstore.PlanBlocked) && len(rows) == 0, p.Status
	})
	return svc
}

// finalState renders everything the progression decided — plan flags, every todo's status
// and dispatch error, and each todo's jobs (status and failure class) — without ids or
// timestamps, so two runs of the same chain can be compared for equality.
func finalState(t *testing.T, svc *job.Service) string {
	t.Helper()
	p, _, err := svc.Meta().GetPlan("plan-it")
	assert.NoErr(t, err)
	var b strings.Builder
	fmt.Fprintf(&b, "plan status=%s paused=%v blocked_todo=%s\n", p.Status, p.Paused, p.BlockedTodo)
	todos, err := svc.Meta().ListTodosByPlan("plan-it")
	assert.NoErr(t, err)
	for _, td := range todos {
		fmt.Fprintf(&b, "todo %s status=%s done=%v assignee=%s dispatch_error=%q\n", td.TodoID, td.Status, td.Done, td.Assignee, td.DispatchError)
		jobs, err := svc.Meta().ListJobsByTodo(td.TodoID, 20)
		assert.NoErr(t, err)
		for _, j := range jobs {
			fmt.Fprintf(&b, "  job agent=%s status=%s failure_class=%q resumed=%v auto_resumed=%v\n", j.Agent, j.Status, j.FailureClass, j.ResumedFrom != "", j.AutoResumedBy != "")
		}
	}
	return b.String()
}

func actionsOf(t *testing.T, svc *job.Service) map[string]jobstore.PlanDriverAction {
	t.Helper()
	acts, err := svc.Meta().ListPlanDriverActions("plan-it", false)
	assert.NoErr(t, err)
	out := map[string]jobstore.PlanDriverAction{}
	for _, a := range acts {
		out[a.Action+"/"+a.Rule+"/"+a.TodoID] = a
	}
	return out
}

// TestDriverOnDoesNotChangeASuccessfulChain: the same three-item chain, run without and
// with the driver on, ends in the same plan / todo / job state; the driver only logged.
func TestDriverOnDoesNotChangeASuccessfulChain(t *testing.T) {
	t.Parallel()
	off := runChain(t, false, "ok", "ok", "ok")
	on := runChain(t, true, "ok", "ok", "ok")
	assert.Eq(t, finalState(t, off), finalState(t, on))
	assert.StrContains(t, finalState(t, on), "plan status=done")

	acts := actionsOf(t, on)
	for _, k := range []string{
		"complete_todo/job_done_complete/td-0", "complete_todo/job_done_complete/td-1", "complete_todo/job_done_complete/td-2",
		"dispatch_todo/dispatch_ready/td-1", "dispatch_todo/dispatch_ready/td-2",
	} {
		a, ok := acts[k]
		if !ok {
			t.Fatalf("missing action %s in %v", k, acts)
		}
		assert.Eq(t, OutcomeMatch, a.Outcome, k)
	}
	none, _ := off.Meta().ListPlanDriverActions("plan-it", false)
	assert.Eq(t, 0, len(none), "a plan without a driver records nothing")
}

// TestDriverOnDoesNotChangeAFailingChain: a failing middle item parks the plan the same
// way with the driver on, and the driver records the escalate the rules would raise.
func TestDriverOnDoesNotChangeAFailingChain(t *testing.T) {
	t.Parallel()
	off := runChain(t, false, "ok", "boom", "ok")
	on := runChain(t, true, "ok", "boom", "ok")
	assert.Eq(t, finalState(t, off), finalState(t, on))
	assert.StrContains(t, finalState(t, on), "plan status=blocked")

	acts := actionsOf(t, on)
	a, ok := acts["escalate/failure_other_escalate/td-1"]
	if !ok {
		t.Fatalf("missing escalate for the failed item in %v", acts)
	}
	assert.Eq(t, OutcomeMatch, a.Outcome, "the host parked the plan, which is what an escalate asks for")
	_, started := acts["dispatch_todo/dispatch_ready/td-2"]
	assert.False(t, started, "the item behind a failure is never due")
}

// panicDriver is a plan driver that fails in every way a driver can.
type panicDriver struct{}

func (panicDriver) BeforePlanEvent(context.Context, job.PlanEventView) any {
	return "ticket"
}
func (panicDriver) OnPlanEvent(context.Context, job.PlanEventView, any) { panic("driver bug") }
func (panicDriver) PlanDriverReport(context.Context, string) (job.PlanDriverReport, error) {
	return job.PlanDriverReport{}, nil
}

// panicBeforeDriver panics before the event, with the plan's advance still to run.
type panicBeforeDriver struct{ panicDriver }

func (panicBeforeDriver) BeforePlanEvent(context.Context, job.PlanEventView) any { panic("driver bug") }

// TestDriverFailureNeverAffectsAdvance: a driver that panics before or after every event
// leaves the plan's progression exactly as a run without any driver.
func TestDriverFailureNeverAffectsAdvance(t *testing.T) {
	t.Parallel()
	off := runChain(t, false, "ok", "boom", "ok")
	want := finalState(t, off)
	for name, d := range map[string]job.PlanDriver{"after": panicDriver{}, "before": panicBeforeDriver{}} {
		t.Run(name, func(t *testing.T) {
			svc := newRealService(t, func(*job.Service) job.PlanDriver { return d })
			assert.NoErr(t, svc.Meta().InsertPlan(jobstore.Plan{PlanID: "plan-it", Title: "it", Status: jobstore.PlanOpen, ProjectKey: "self", CreatedAt: 1, UpdatedAt: 1}))
			for i, a := range []string{"ok", "boom", "ok"} {
				td := jobstore.PlanTodo{TodoID: fmt.Sprintf("td-%d", i), PlanID: "plan-it", Title: a, Status: jobstore.TodoPending,
					Assignee: a, Auto: true, Sort: (i + 1) * 10, CreatedAt: 1, UpdatedAt: 1}
				if i > 0 {
					td.After = []string{fmt.Sprintf("td-%d", i-1)}
				}
				assert.NoErr(t, svc.Meta().InsertTodo(td))
			}
			_, err := svc.RunPlan("plan-it", "alice")
			assert.NoErr(t, err)
			wait.For(t, 30*time.Second, "plan blocked", func() (bool, any) {
				p, _, _ := svc.Meta().GetPlan("plan-it")
				rows, _ := svc.Meta().ListPlanEvents(10)
				return p.Status == jobstore.PlanBlocked && len(rows) == 0, p.Status
			})
			assert.Eq(t, want, finalState(t, svc))
		})
	}
}
