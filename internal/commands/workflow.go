package commands

import (
	"fmt"
	"os"
	"time"

	"github.com/gookit/gcli/v3"

	yaml "github.com/goccy/go-yaml"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// wfRunOpts / wfShowOpts / wfListOpts / wfCancelOpts hold the per-subcommand
// flags. Each subcommand carries its own --config/--server/--token (same shape
// as the job group) so the bound vars never collide across subcommands.
var wfRunOpts = struct {
	config, server, token string
	watch                 bool
}{}

var wfShowOpts = struct {
	config, server, token string
}{}

var wfListOpts = struct {
	config, server, token string
	status                string
}{}

var wfCancelOpts = struct {
	config, server, token string
}{}

// NewWorkflowCmd builds the `workflow` command group (run/show/list/cancel). It
// wraps the server's /v1/workflows HTTP API so the host can submit and inspect
// job-chains without curl (P3-a). A workflow file is yaml (title + steps[]); the
// CLI parses it into a job.WorkflowSpec and POSTs it.
func NewWorkflowCmd() *gcli.Command {
	return &gcli.Command{
		Name:    "workflow",
		Aliases: []string{"wf"},
		Desc:    "Submit and manage workflows (job chains) via the bridge server",
		Subs: []*gcli.Command{
			{
				Name: "run",
				Desc: "Submit a workflow from a yaml file (title + steps[])",
				Config: func(c *gcli.Command) {
					c.StrOpt(&wfRunOpts.config, "config", "c", "", "path to the bridge config file")
					c.StrOpt(&wfRunOpts.server, "server", "s", "", "server address (overrides config server.addr)")
					c.StrOpt(&wfRunOpts.token, "token", "", "", "bearer token override (prefer config/env)")
					c.BoolOpt(&wfRunOpts.watch, "watch", "w", false, "poll the workflow until it reaches a terminal state, printing each step")
					c.AddArg("file", "path to the workflow yaml file", true)
				},
				Func: runWorkflowRun,
			},
			{
				Name: "show",
				Desc: "Query a workflow's status + step chain",
				Config: func(c *gcli.Command) {
					c.StrOpt(&wfShowOpts.config, "config", "c", "", "path to the bridge config file")
					c.StrOpt(&wfShowOpts.server, "server", "s", "", "server address (overrides config server.addr)")
					c.StrOpt(&wfShowOpts.token, "token", "", "", "bearer token override (prefer config/env)")
					c.AddArg("id", "workflow id", true)
				},
				Func: runWorkflowShow,
			},
			{
				Name: "list",
				Desc: "List workflows with an optional status filter",
				Config: func(c *gcli.Command) {
					c.StrOpt(&wfListOpts.config, "config", "c", "", "path to the bridge config file")
					c.StrOpt(&wfListOpts.server, "server", "s", "", "server address (overrides config server.addr)")
					c.StrOpt(&wfListOpts.token, "token", "", "", "bearer token override (prefer config/env)")
					c.StrOpt(&wfListOpts.status, "status", "", "", "filter by status (running/done/failed/cancelled)")
				},
				Func: runWorkflowList,
			},
			{
				Name: "cancel",
				Desc: "Cancel a running workflow",
				Config: func(c *gcli.Command) {
					c.StrOpt(&wfCancelOpts.config, "config", "c", "", "path to the bridge config file")
					c.StrOpt(&wfCancelOpts.server, "server", "s", "", "server address (overrides config server.addr)")
					c.StrOpt(&wfCancelOpts.token, "token", "", "", "bearer token override (prefer config/env)")
					c.AddArg("id", "workflow id", true)
				},
				Func: runWorkflowCancel,
			},
		},
	}
}

// argFile returns the required <file> positional from the gcli-bound named arg.
func argFile(c *gcli.Command) string {
	if c != nil {
		if a := c.Arg("file"); a != nil {
			return a.String()
		}
	}
	return ""
}

// parseWorkflowFile reads a yaml workflow file and unmarshals it into a
// job.WorkflowSpec. The yaml keys align with the StepSpec/WorkflowSpec yaml tags
// (title + steps[] with project_key/agent/runner/prompt/cmd/cwd/timeout_sec/tags).
// It is extracted so the command and its unit test share one decoder.
func parseWorkflowFile(path string) (job.WorkflowSpec, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return job.WorkflowSpec{}, fmt.Errorf("read workflow file: %w", err)
	}
	var spec job.WorkflowSpec
	if err := yaml.Unmarshal(body, &spec); err != nil {
		return job.WorkflowSpec{}, fmt.Errorf("parse workflow yaml: %w", err)
	}
	if len(spec.Steps) == 0 {
		return job.WorkflowSpec{}, fmt.Errorf("workflow file has no steps")
	}
	return spec, nil
}

// runWorkflowRun submits a yaml workflow file and prints the new workflow id +
// step overview. With --watch it then polls GetWorkflow until the workflow
// reaches a terminal state (workflow-level SSE is left to a follow-up), printing
// the final per-step chain.
func runWorkflowRun(c *gcli.Command, _ []string) error {
	file := argFile(c)
	if file == "" {
		return fmt.Errorf("workflow run requires a <file> argument")
	}
	spec, err := parseWorkflowFile(file)
	if err != nil {
		return err
	}
	cli, err := newClient(wfRunOpts.config, wfRunOpts.server, wfRunOpts.token)
	if err != nil {
		return err
	}
	wf, err := cli.SubmitWorkflow(spec)
	if err != nil {
		return err
	}
	c.Printf("workflow %s submitted: status=%s steps=%d\n", wf.ID, wf.Status, wf.TotalSteps)
	printStepOverview(c, spec)

	if !wfRunOpts.watch {
		return nil
	}
	final, err := watchWorkflow(c, cli, wf.ID)
	if err != nil {
		return err
	}
	c.Printf("workflow %s finished: status=%s\n", final.ID, final.Status)
	printStepChain(c, final.Steps)
	if code := workflowExitCode(final.Status); code != 0 {
		os.Exit(code)
	}
	return nil
}

// printStepOverview prints the planned steps from the spec (before any job ids
// exist) so `run` shows what was submitted even without --watch.
func printStepOverview(c *gcli.Command, spec job.WorkflowSpec) {
	for i, st := range spec.Steps {
		name := st.Name
		if name == "" {
			name = "-"
		}
		c.Printf("  step %d: %-16s %s/%s\n", i+1, name, st.ProjectKey, st.Agent)
	}
}

// watchWorkflow polls GetWorkflow until the workflow reaches a terminal state,
// printing each step's status as it transitions. Workflow-level SSE is a
// follow-up; v1 uses a simple poll loop (plan §P3-a range note).
func watchWorkflow(c *gcli.Command, cli *client.Client, id string) (client.Workflow, error) {
	deadline := time.Now().Add(2 * time.Hour)
	// lastStep tracks the last printed status per step index so we only print on change.
	lastStep := map[int]string{}
	for time.Now().Before(deadline) {
		wf, err := cli.GetWorkflow(id)
		if err != nil {
			return client.Workflow{}, err
		}
		for _, st := range wf.Steps {
			if lastStep[st.StepIndex] != st.Status {
				lastStep[st.StepIndex] = st.Status
				c.Printf(">> step %d (%s): %s\n", st.StepIndex, stepName(st), st.Status)
			}
		}
		if isWorkflowTerminal(wf.Status) {
			return wf, nil
		}
		time.Sleep(2 * time.Second)
	}
	return client.Workflow{}, fmt.Errorf("workflow %s did not finish within the watch window", id)
}

func runWorkflowShow(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("workflow show requires an <id> argument")
	}
	cli, err := newClient(wfShowOpts.config, wfShowOpts.server, wfShowOpts.token)
	if err != nil {
		return err
	}
	wf, err := cli.GetWorkflow(id)
	if err != nil {
		return err
	}
	c.Printf("id:           %s\n", wf.ID)
	if wf.Title != "" {
		c.Printf("title:        %s\n", wf.Title)
	}
	c.Printf("status:       %s\n", wf.Status)
	c.Printf("current_step: %d/%d\n", wf.CurrentStep, wf.TotalSteps)
	if wf.Error != "" {
		c.Printf("error:        %s\n", wf.Error)
	}
	c.Println("steps:")
	printStepChain(c, wf.Steps)
	return nil
}

// printStepChain renders a workflow's started step-jobs as a table
// (STEP/NAME/JOB ID/STATUS). A step not yet reached has no row (the chain is
// strictly serial), so an empty chain prints a friendly hint.
func printStepChain(c *gcli.Command, steps []client.WorkflowStep) {
	if len(steps) == 0 {
		c.Println("  (no steps started yet)")
		return
	}
	c.Printf("  %-5s %-18s %-26s %s\n", "STEP", "NAME", "JOB ID", "STATUS")
	for _, st := range steps {
		c.Printf("  %-5d %-18s %-26s %s\n", st.StepIndex, stepName(st), st.JobID, st.Status)
	}
}

// stepName returns a step's display name, falling back to "-" when unnamed.
func stepName(st client.WorkflowStep) string {
	if st.Name == "" {
		return "-"
	}
	return st.Name
}

// runWorkflowList queries GET /v1/workflows with an optional --status filter and
// prints a fixed-width table. An empty result prints a friendly hint.
func runWorkflowList(c *gcli.Command, _ []string) error {
	cli, err := newClient(wfListOpts.config, wfListOpts.server, wfListOpts.token)
	if err != nil {
		return err
	}
	list, err := cli.ListWorkflows(wfListOpts.status)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		c.Println("no workflows matched the given filter")
		return nil
	}
	c.Printf("%-26s %-10s %-8s %-24s %s\n", "ID", "STATUS", "STEP", "TITLE", "CREATED")
	for _, wf := range list {
		c.Printf("%-26s %-10s %-8s %-24s %s\n",
			wf.ID, wf.Status, fmt.Sprintf("%d/%d", wf.CurrentStep, wf.TotalSteps),
			truncate(wf.Title, 24), formatStarted(wf.CreatedAt))
	}
	return nil
}

// truncate caps a string at max runes, appending an ellipsis when cut, so the
// list table stays aligned. An empty title renders as "-".
func truncate(s string, max int) string {
	if s == "" {
		return "-"
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

func runWorkflowCancel(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("workflow cancel requires an <id> argument")
	}
	cli, err := newClient(wfCancelOpts.config, wfCancelOpts.server, wfCancelOpts.token)
	if err != nil {
		return err
	}
	wf, err := cli.CancelWorkflow(id)
	if err != nil {
		return err
	}
	c.Printf("workflow %s cancel requested: status=%s\n", wf.ID, wf.Status)
	return nil
}

// isWorkflowTerminal reports whether a workflow status is terminal (not running).
func isWorkflowTerminal(status string) bool {
	switch status {
	case jobstore.WorkflowDone, jobstore.WorkflowFailed, jobstore.WorkflowCancelled:
		return true
	default:
		return false
	}
}

// workflowExitCode maps a workflow terminal status to a process exit code:
// done=0, cancelled=130 (SIGINT convention), failed/other=1. Mirrors the job
// watch exit-code mapping so `workflow run --watch` is scriptable.
func workflowExitCode(status string) int {
	switch status {
	case jobstore.WorkflowDone:
		return 0
	case jobstore.WorkflowCancelled:
		return 130
	default:
		return 1
	}
}
