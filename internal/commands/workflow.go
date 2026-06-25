package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/gookit/gcli/v3"

	yaml "github.com/goccy/go-yaml"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
)

// wfRunOpts / wfShowOpts / wfListOpts / wfCancelOpts hold the per-subcommand
// flags. Each subcommand carries its own --server/--token (same shape as the
// job group) so the bound vars never collide across subcommands. The config
// path is the app-level global -c (config.InputCfgFile), not a per-command flag (P1).
var wfRunOpts = struct {
	server, token string
	watch         bool
}{}

var wfShowOpts = struct {
	server, token string
}{}

var wfListOpts = struct {
	server, token string
	status        string
}{}

var wfCancelOpts = struct {
	server, token string
}{}

var wfExportOpts = struct {
	server, token string
	out           string
	format        string
}{}

var wfEventsOpts = struct {
	server, token string
	since         int64
}{}

// NewWorkflowCmd builds the `workflow` command group (run/show/list/cancel). It
// wraps the server's /v1/workflows HTTP API so the host can submit and inspect
// job-chains without curl (P3-a). A workflow file is yaml (title + steps[]); the
// CLI parses it into a workflow.Spec and POSTs it.
func NewWorkflowCmd() *gcli.Command {
	return &gcli.Command{
		Name:    "workflow",
		Aliases: []string{"wf"},
		Desc:    "Submit and manage workflows (job chains) via the bridge server",
		Subs: []*gcli.Command{
			{
				Name:    "run",
				Aliases: []string{"add"},
				Desc:    "Submit a workflow from a yaml or json file (title + steps[])",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.StrOpt(&wfRunOpts.server, "server", "s", "", "server address (overrides config server.addr)")
					c.StrOpt(&wfRunOpts.token, "token", "", "", "bearer token override (prefer config/env)")
					c.BoolOpt(&wfRunOpts.watch, "watch", "w", false, "poll the workflow until it reaches a terminal state, printing each step")
					c.AddArg("file", "path to the workflow file (.json => json, else yaml; json also auto-detected by content)", true)
				},
				Func: runWorkflowRun,
			},
			{
				Name: "show",
				Desc: "Query a workflow's status + step chain",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.StrOpt(&wfShowOpts.server, "server", "s", "", "server address (overrides config server.addr)")
					c.StrOpt(&wfShowOpts.token, "token", "", "", "bearer token override (prefer config/env)")
					c.AddArg("id", "workflow id", true)
				},
				Func: runWorkflowShow,
			},
			{
				Name:    "list",
				Desc:    "List workflows with an optional status filter",
				Aliases: []string{"ls"},
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
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
					bindConfigFlag(c)
					c.StrOpt(&wfCancelOpts.server, "server", "s", "", "server address (overrides config server.addr)")
					c.StrOpt(&wfCancelOpts.token, "token", "", "", "bearer token override (prefer config/env)")
					c.AddArg("id", "workflow id", true)
				},
				Func: runWorkflowCancel,
			},
			{
				Name: "export",
				Desc: "Export a workflow's spec (secrets stripped) for re-import; default yaml (= `wf run` format)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.StrOpt(&wfExportOpts.server, "server", "s", "", "server address (overrides config server.addr)")
					c.StrOpt(&wfExportOpts.token, "token", "", "", "bearer token override (prefer config/env)")
					c.StrOpt(&wfExportOpts.out, "out", "o", "", "write the spec to this file instead of stdout")
					c.StrOpt(&wfExportOpts.format, "format", "f", "yaml", "output format: yaml (default, = wf run input) | json")
					c.AddArg("id", "workflow id", true)
				},
				Func: runWorkflowExport,
			},
			{
				Name: "events",
				Desc: "Print a workflow's lifecycle event timeline",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.StrOpt(&wfEventsOpts.server, "server", "s", "", "server address (overrides config server.addr)")
					c.StrOpt(&wfEventsOpts.token, "token", "", "", "bearer token override (prefer config/env)")
					c.Int64Opt(&wfEventsOpts.since, "since", "", 0, "only events with seq strictly greater than this cursor")
					c.AddArg("id", "workflow id", true)
				},
				Func: runWorkflowEvents,
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

// runWorkflowRun submits a yaml workflow file and prints the new workflow id +
// step overview. With --watch it then polls GetWorkflow until the workflow
// reaches a terminal state (workflow-level SSE is left to a follow-up), printing
// the final per-step chain.
func runWorkflowRun(c *gcli.Command, _ []string) error {
	file := argFile(c)
	if file == "" {
		return fmt.Errorf("workflow run requires a <file> argument")
	}
	spec, err := workflow.ParseWorkflowFile(file)
	if err != nil {
		return err
	}
	cli, err := newClient(config.InputCfgFile, wfRunOpts.server, wfRunOpts.token)
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
func printStepOverview(c *gcli.Command, spec workflow.Spec) {
	for i, st := range spec.Steps {
		name := st.Name
		if name == "" {
			name = "-"
		}
		c.Printf("  step %d: %-16s %s/%s\n", i+1, name, st.ProjectKey, st.Agent)
	}
}

// watchWorkflow polls the workflow until it reaches a terminal state, printing
// each step's status as it transitions. The poll state-machine lives in
// client.WatchWorkflow (BP6); this keeps only the per-step printing. Workflow-level
// SSE is a follow-up; v1 uses a simple poll loop (plan §P3-a range note).
func watchWorkflow(c *gcli.Command, cli *client.Client, id string) (client.Workflow, error) {
	return cli.WatchWorkflow(context.Background(), id, func(st client.WorkflowStep) {
		c.Printf(">> step %d (%s): %s\n", st.StepIndex, stepName(st), st.Status)
	})
}

func runWorkflowShow(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("workflow show requires an <id> argument")
	}
	cli, err := newClient(config.InputCfgFile, wfShowOpts.server, wfShowOpts.token)
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
// (STEP/ATT/FAN/NAME/JOB ID/STATUS, T4.3). A step not yet reached has no row (the
// chain is strictly serial), so an empty chain prints a friendly hint. A retried
// step contributes one row per attempt and a fan-out step one row per fan, so the
// ATT/FAN columns expose the v2 dimensions; both render "-" for a v1 single-job step.
func printStepChain(c *gcli.Command, steps []client.WorkflowStep) {
	if len(steps) == 0 {
		c.Println("  (no steps started yet)")
		return
	}
	c.Printf("  %-5s %-4s %-4s %-18s %-26s %s\n", "STEP", "ATT", "FAN", "NAME", "JOB ID", "STATUS")
	for _, st := range steps {
		c.Printf("  %-5d %-4s %-4s %-18s %-26s %s\n",
			st.StepIndex, attemptCol(st.Attempt), fanCol(st.FanIndex),
			stepName(st), st.JobID, st.Status)
	}
}

// attemptCol renders a step's attempt for the chain table: an attempt of 0/1 (a v1
// single run) shows "-" to keep the common case quiet; a retried run (>=2) shows the
// number so the retry history stands out.
func attemptCol(att int) string {
	if att <= 1 {
		return "-"
	}
	return fmt.Sprintf("%d", att)
}

// fanCol renders a step's fan index for the chain table: 0 (a non-fan single job)
// shows "-"; a fan-out job (>=1) shows its 1-based parallel index.
func fanCol(fan int) string {
	if fan <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", fan)
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
	cli, err := newClient(config.InputCfgFile, wfListOpts.server, wfListOpts.token)
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
	cli, err := newClient(config.InputCfgFile, wfCancelOpts.server, wfCancelOpts.token)
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

// runWorkflowExport fetches a workflow's reconstructed spec (secrets stripped,
// T4.1) and writes it as indented JSON to stdout or, with -o, to a file. A
// redacted export prints a stderr warning so the operator knows a placeholder must
// be replaced before re-running it.
func runWorkflowExport(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("workflow export requires an <id> argument")
	}
	cli, err := newClient(config.InputCfgFile, wfExportOpts.server, wfExportOpts.token)
	if err != nil {
		return err
	}
	spec, redacted, err := cli.ExportWorkflow(id)
	if err != nil {
		return err
	}
	out, err := marshalWorkflowSpec(spec, wfExportOpts.format)
	if err != nil {
		return err
	}
	if wfExportOpts.out != "" {
		if err := os.WriteFile(wfExportOpts.out, append(out, '\n'), 0o600); err != nil {
			return fmt.Errorf("write %q: %w", wfExportOpts.out, err)
		}
		c.Printf("workflow %s exported to %s\n", id, wfExportOpts.out)
	} else {
		c.Println(string(out))
	}
	if redacted {
		fmt.Fprintf(os.Stderr, "warning: secret-looking values were redacted to %q; replace them before re-running\n", "***REDACTED***")
	}
	return nil
}

// marshalWorkflowSpec encodes an exported spec for `workflow export`. The default
// (empty/"yaml") emits YAML — the SAME shape `wf run` consumes, so an export
// round-trips straight back in (StepSpec carries matching yaml tags); "json" emits
// the indented JSON dump. The output is normalised to no trailing newline so the
// caller's file-write/println paths add exactly one. An unknown format is rejected.
func marshalWorkflowSpec(spec workflow.Spec, format string) ([]byte, error) {
	var (
		out []byte
		err error
	)
	switch strings.ToLower(format) {
	case "", "yaml", "yml":
		if out, err = yaml.Marshal(spec); err != nil {
			return nil, fmt.Errorf("encode spec yaml: %w", err)
		}
	case "json":
		if out, err = json.MarshalIndent(spec, "", "  "); err != nil {
			return nil, fmt.Errorf("encode spec json: %w", err)
		}
	default:
		return nil, fmt.Errorf("unknown --format %q: want yaml or json", format)
	}
	return []byte(strings.TrimRight(string(out), "\n")), nil
}

// runWorkflowEvents prints a workflow's append-only lifecycle event timeline (P1
// workflow_events via the events API, T4.3). Each row is SEQ/TIME/TYPE/DETAIL so the
// fan-out / retry / sub-workflow milestones are visible from the CLI.
func runWorkflowEvents(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("workflow events requires an <id> argument")
	}
	cli, err := newClient(config.InputCfgFile, wfEventsOpts.server, wfEventsOpts.token)
	if err != nil {
		return err
	}
	events, err := cli.ListWorkflowEvents(id, wfEventsOpts.since)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		c.Println("no events for this workflow")
		return nil
	}
	c.Printf("%-6s %-20s %-22s %s\n", "SEQ", "TIME", "TYPE", "DETAIL")
	for _, ev := range events {
		c.Printf("%-6d %-20s %-22s %s\n",
			ev.Seq, formatStarted(ev.At), ev.Type, ev.Detail)
	}
	return nil
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
