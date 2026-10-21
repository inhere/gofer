package workflow

import (
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// TestWorkflowStepsFanName pins gofer-j5xc: every fan row of the detail view carries the
// fan's own declared name (fan_name) next to the step name, so the CLI table and the web
// result table show WHICH fan a row is instead of repeating the step name.
func TestWorkflowStepsFanName(t *testing.T) {
	t.Parallel()
	e := newTestEngine(t, t.TempDir())
	wf, err := e.SubmitWorkflow(Spec{
		Project: "self",
		Steps: []StepSpec{
			{
				Name: "checks", Agent: "exec", Runner: "local", Join: joinAll,
				Fan: []FanSpec{
					{Name: "linux-race", Cmd: testcmd.Cmd(t, "exit", "0")},
					{Name: "web", Cmd: testcmd.Cmd(t, "exit", "0")},
				},
			},
			{Name: "single", Agent: "exec", Runner: "local", Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: "."},
		},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("workflow = %s (err=%s), want done", final.Status, final.Error)
	}
	steps, err := e.WorkflowSteps(wf.ID)
	if err != nil {
		t.Fatalf("WorkflowSteps: %v", err)
	}
	want := []struct {
		stepIndex int
		fanIndex  int
		name      string
		fanName   string
	}{
		{1, 1, "checks", "linux-race"},
		{1, 2, "checks", "web"},
		{2, 0, "single", ""},
	}
	if len(steps) != len(want) {
		t.Fatalf("steps = %d, want %d: %+v", len(steps), len(want), steps)
	}
	// Fan rows may be listed in completion order, so look each one up by step/fan.
	for _, w := range want {
		var row *Step
		for i := range steps {
			if steps[i].StepIndex == w.stepIndex && steps[i].FanIndex == w.fanIndex {
				row = &steps[i]
				break
			}
		}
		if row == nil {
			t.Fatalf("no row for step %d fan %d: %+v", w.stepIndex, w.fanIndex, steps)
		}
		if row.Name != w.name {
			t.Fatalf("step %d fan %d name = %q, want %q (a fan row must keep the step name)", w.stepIndex, w.fanIndex, row.Name, w.name)
		}
		if row.FanName != w.fanName {
			t.Fatalf("step %d fan %d fan_name = %q, want %q", w.stepIndex, w.fanIndex, row.FanName, w.fanName)
		}
	}
}

// TestWorkflowStepsRunLocation pins gofer-plkx: a detail row reports WHERE and WHEN it
// ran — the resolved cwd, its start and end as unix seconds — so the CLI/web detail view
// can show them without a second job lookup per row.
func TestWorkflowStepsRunLocation(t *testing.T) {
	t.Parallel()
	e := newTestEngine(t, t.TempDir())
	wf, err := e.SubmitWorkflow(Spec{
		Project: "self",
		Steps: []StepSpec{
			{Name: "one", Agent: "exec", Runner: "local", Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: "."},
			{Name: "two", Agent: "exec", Runner: "local", Cmd: testcmd.Cmd(t, "exit", "0")},
		},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	if final := waitWorkflow(t, e, wf.ID); final.Status != jobstore.WorkflowDone {
		t.Fatalf("workflow = %s (err=%s), want done", final.Status, final.Error)
	}
	steps, err := e.WorkflowSteps(wf.ID)
	if err != nil {
		t.Fatalf("WorkflowSteps: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("steps = %d, want 2: %+v", len(steps), steps)
	}
	for _, st := range steps {
		if st.Cwd == "" {
			t.Fatalf("step %d cwd is empty, want the directory it ran in", st.StepIndex)
		}
		if st.StartedAt <= 0 {
			t.Fatalf("step %d started_at = %d, want a unix timestamp", st.StepIndex, st.StartedAt)
		}
		if st.FinishedAt < st.StartedAt {
			t.Fatalf("step %d finished_at = %d before started_at = %d", st.StepIndex, st.FinishedAt, st.StartedAt)
		}
		if st.Ref != "" {
			t.Fatalf("step %d ref = %q, want empty for a step without ref:", st.StepIndex, st.Ref)
		}
	}
}
