package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/gookit/gcli/v3"

	yaml "github.com/goccy/go-yaml"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
)

// wfRunOpts / wfListOpts / wfExportOpts / wfEventsOpts hold the per-subcommand
// NON-connection flags. The --server/--token connection flags are bound via the
// SHARED bindServerFlags (into jobConnOpts) so wf gets the SAME GOFER_SERVER_ADDR
// / GOFER_SERVER_TOKEN env defaults as `job` (E38③: don't re-bind per-subcommand
// with empty defaults — that dropped the env fallback). The config path is the
// app-level global -c (config.InputCfgFile), not a per-command flag (P1).
var wfRunOpts = struct {
	watch    bool
	template string
	project  string
	vars     gcli.Strings
}{}

var wfListOpts = struct {
	status string
}{}

var wfExportOpts = struct {
	out    string
	format string
}{}

var wfEventsOpts = struct {
	since int64
}{}

var wfTemplateProject string

// wfTemplateSource selects `template show --source`: print the template's YAML source.
var wfTemplateSource bool

// wfTemplateWriteOpts holds the write-side template flags (`template save` / `template
// rm` / `save-as-template`): the target scope and, for a project template, the project.
var wfTemplateWriteOpts = struct {
	global  bool
	project string
	file    string
}{}

var wfPickOpts struct {
	step          int
	fan           int
	merge         bool
	squash        bool
	cleanupOthers bool
}

// wfRerunOpts are `wf rerun`'s selectors: --failed (only the failed steps/fans) or
// --from <step>, plus the shared --watch polling.
var wfRerunOpts struct {
	failed bool
	from   string
	watch  bool
}

// wfInitOpts are `wf init`'s flags: where to write the starter file and whether an
// existing one may be replaced.
var wfInitOpts struct {
	dir   string
	force bool
}

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
					bindServerFlags(c)
					c.BoolOpt(&wfRunOpts.watch, "watch", "w", false, "poll the workflow until it reaches a terminal state, printing each step")
					c.StrOpt(&wfRunOpts.template, "template", "t", "", "workflow template name (project .gofer/workflows, config-dir workflows, or built-in)")
					c.StrOpt(&wfRunOpts.project, "project", "p", "", "project key: the workflow's default project (a step without project_key runs there)")
					c.VarOpt(&wfRunOpts.vars, "var", "", "workflow template variable k=v (repeatable)")
					c.AddArg("file", "path to the workflow file (.json => json, else yaml; json also auto-detected by content)", false)
				},
				Func: runWorkflowRun,
			},
			{
				Name: "pick",
				Desc: "Select a fan of a join=pick step and continue the workflow",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.IntOpt(&wfPickOpts.step, "step", "", 0, "1-based workflow step")
					c.IntOpt(&wfPickOpts.fan, "fan", "", 0, "1-based fan index")
					c.BoolOpt(&wfPickOpts.merge, "merge", "", false, "after picking, merge the picked fan's worktree branch into the project main checkout (local runner only)")
					c.BoolOpt(&wfPickOpts.squash, "squash", "", false, "with --merge: squash into one commit")
					c.BoolOpt(&wfPickOpts.cleanupOthers, "cleanup-others", "", false, "with --merge: remove the other fans' worktrees and branches")
					c.AddArg("id", "workflow id", true)
					c.AddArg("step", "1-based workflow step (or use --step)", false)
					c.AddArg("fan", "1-based fan index (or use --fan)", false)
				},
				Func: runWorkflowPick,
			},
			{
				Name: "template",
				Desc: "List, show, save or remove workflow templates",
				Subs: []*gcli.Command{
					{
						Name: "ls", Aliases: []string{"list"},
						Config: func(c *gcli.Command) {
							bindConfigFlag(c)
							bindServerFlags(c)
							c.StrOpt(&wfTemplateProject, "project", "p", "", "also search this project's .gofer/workflows")
						},
						Func: runWorkflowTemplateList,
					},
					{
						Name: "show",
						Config: func(c *gcli.Command) {
							bindConfigFlag(c)
							bindServerFlags(c)
							c.StrOpt(&wfTemplateProject, "project", "p", "", "also search this project's .gofer/workflows")
							c.BoolOpt(&wfTemplateSource, "source", "", false, "print the template's own YAML source (and where it comes from) instead of the parsed spec")
							c.AddArg("name", "workflow template name", true)
						},
						Func: runWorkflowTemplateShow,
					},
					{
						Name: "save",
						Desc: "Save a template file to the global scope or a project workspace (validated first)",
						Config: func(c *gcli.Command) {
							bindTemplateWriteFlags(c)
							c.StrOpt(&wfTemplateWriteOpts.file, "file", "f", "", "template YAML file to read (default: stdin)")
							c.AddArg("name", "workflow template name (A-Z a-z 0-9 . _ -)", true)
						},
						Func: runWorkflowTemplateSave,
					},
					{
						Name: "rm", Aliases: []string{"remove", "delete"},
						Desc: "Remove a global or project template",
						Config: func(c *gcli.Command) {
							bindTemplateWriteFlags(c)
							c.AddArg("name", "workflow template name", true)
						},
						Func: runWorkflowTemplateRemove,
					},
				},
			},
			{
				Name: "init",
				Desc: "Write a starter workflow file (.gofer/workflows/verify.yaml or release.yaml) for a repository",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.StrOpt(&wfInitOpts.dir, "dir", "", "", "repository directory (default: the current directory)")
					c.BoolOpt(&wfInitOpts.force, "force", "", false, "overwrite the file when it already exists")
					c.AddArg("kind", "which starter to write: verify or release", true)
				},
				Func: runWorkflowInit,
			},
			{
				Name: "save-as-template",
				Desc: "Save a workflow's exported spec as a named template",
				Config: func(c *gcli.Command) {
					bindTemplateWriteFlags(c)
					c.AddArg("id", "workflow id", true)
					c.AddArg("name", "template name to create", true)
				},
				Func: runWorkflowSaveAsTemplate,
			},
			{
				Name: "rerun",
				Desc: "Re-run a finished workflow as a new one: only its failed steps, or from a step on",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.BoolOpt(&wfRerunOpts.failed, "failed", "", false, "keep only the failed steps (and, inside a failed fan-out step, only the failed fans)")
					c.StrOpt(&wfRerunOpts.from, "from", "", "", "keep this step and every later one (1-based number or step name)")
					c.BoolOpt(&wfRerunOpts.watch, "watch", "w", false, "poll the new workflow until it reaches a terminal state")
					c.AddArg("id", "workflow id to re-run", true)
				},
				Func: runWorkflowRerun,
			},
			{
				Name: "show",
				Desc: "Query a workflow's status + step chain",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
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
					bindServerFlags(c)
					c.StrOpt(&wfListOpts.status, "status", "", "", "filter by status (running/done/failed/cancelled)")
				},
				Func: runWorkflowList,
			},
			{
				Name: "cancel",
				Desc: "Cancel a running workflow",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("id", "workflow id", true)
				},
				Func: runWorkflowCancel,
			},
			{
				Name: "export",
				Desc: "Export a workflow's spec (secrets stripped) for re-import; default yaml (= `wf run` format)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
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
					bindServerFlags(c)
					c.Int64Opt(&wfEventsOpts.since, "since", "", 0, "only events with seq strictly greater than this cursor")
					c.AddArg("id", "workflow id", true)
				},
				Func: runWorkflowEvents,
			},
		},
	}
}

func runWorkflowTemplateList(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	templates, err := cli.ListWorkflowTemplates("", wfTemplateProject)
	if err != nil {
		return err
	}
	for _, tpl := range templates {
		c.Printf("%-20s %-8s %s\n", tpl.Name, tpl.Source, tpl.Desc)
	}
	return nil
}

func runWorkflowTemplateShow(c *gcli.Command, _ []string) error {
	name := c.Arg("name").String()
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	if wfTemplateSource {
		src, err := cli.GetWorkflowTemplateSource(name, wfTemplateProject)
		if err != nil {
			return err
		}
		where := src.Source
		if src.Path != "" {
			where += " (" + src.Path + ")"
		}
		c.Printf("# %s source: %s\n", src.Name, where)
		c.Println(src.Content)
		return nil
	}
	templates, err := cli.ListWorkflowTemplates(name, wfTemplateProject)
	if err != nil {
		return err
	}
	if len(templates) == 0 {
		return fmt.Errorf("workflow template %q not found", name)
	}
	b, err := json.MarshalIndent(templates[0], "", "  ")
	if err != nil {
		return err
	}
	c.Println(string(b))
	return nil
}

// bindTemplateWriteFlags binds the scope flags shared by `template save`, `template rm` and
// `save-as-template`: --global for <config-dir>/workflows, --project <p> for that project's
// workspace. Without either, the global scope is the default.
func bindTemplateWriteFlags(c *gcli.Command) {
	bindConfigFlag(c)
	bindServerFlags(c)
	c.BoolOpt(&wfTemplateWriteOpts.global, "global", "", false, "write the global template (<config-dir>/workflows)")
	c.StrOpt(&wfTemplateWriteOpts.project, "project", "p", "", "write the template into this project's workspace (.gofer/workflows)")
}

// templateWriteScope resolves --global / --project into the (scope, project) pair the API
// takes. --global is the default; naming both is refused because they are two places.
func templateWriteScope() (scope, project string, err error) {
	if wfTemplateWriteOpts.global && wfTemplateWriteOpts.project != "" {
		return "", "", fmt.Errorf("--global and --project are mutually exclusive")
	}
	if wfTemplateWriteOpts.project != "" {
		return "project", wfTemplateWriteOpts.project, nil
	}
	return "global", "", nil
}

// runWorkflowTemplateSave reads a template YAML from --file (or stdin) and saves it.
func runWorkflowTemplateSave(c *gcli.Command, _ []string) error {
	name := c.Arg("name").String()
	var content []byte
	var err error
	if wfTemplateWriteOpts.file != "" {
		content, err = os.ReadFile(wfTemplateWriteOpts.file)
	} else {
		content, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		return err
	}
	scope, project, err := templateWriteScope()
	if err != nil {
		return err
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	res, err := cli.SaveWorkflowTemplate(name, string(content), scope, project)
	if err != nil {
		return err
	}
	printTemplateWrite(c, res, "saved")
	return nil
}

// runWorkflowTemplateRemove deletes a global or project template.
func runWorkflowTemplateRemove(c *gcli.Command, _ []string) error {
	name := c.Arg("name").String()
	scope, project, err := templateWriteScope()
	if err != nil {
		return err
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	res, err := cli.DeleteWorkflowTemplate(name, scope, project)
	if err != nil {
		return err
	}
	printTemplateWrite(c, res, "removed")
	return nil
}

// runWorkflowSaveAsTemplate saves a workflow's exported spec as a template.
func runWorkflowSaveAsTemplate(c *gcli.Command, _ []string) error {
	id, name := c.Arg("id").String(), c.Arg("name").String()
	scope, project, err := templateWriteScope()
	if err != nil {
		return err
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	res, err := cli.SaveWorkflowAsTemplate(id, name, scope, project)
	if err != nil {
		return err
	}
	printTemplateWrite(c, res, "saved")
	return nil
}

// printTemplateWrite reports a template write, including the two things a caller needs to
// know: that the name now overrides a builtin template (or, after a delete, which source it
// falls back to), and that a project template is an uncommitted change (gofer never commits
// for the user).
func printTemplateWrite(c *gcli.Command, res workflow.TemplateWriteResult, verb string) {
	c.Printf("template %s %s (%s) %s\n", res.Name, verb, res.Scope, res.Path)
	if res.OverridesBuiltin {
		c.Printf("note: this overrides the builtin template %q (the builtin itself stays read-only)\n", res.Name)
	}
	if res.FallsBackTo != "" {
		c.Printf("note: %q now resolves to the %s template\n", res.Name, res.FallsBackTo)
	}
	if res.Uncommitted {
		c.Println("note: this is an uncommitted change in the project workspace; commit it so the repository keeps it:")
		c.Println("  " + res.CommitCommand)
	}
}

// runWorkflowInit writes a starter workflow file (verify / release) into a repository,
// pre-filled from what the repository contains (workflow.GenerateWorkflowInit). It
// talks to no server: the file only takes effect once it is committed, because the
// server reads project templates from the project workspace.
func runWorkflowInit(c *gcli.Command, _ []string) error {
	kind := c.Arg("kind").String()
	dir := wfInitOpts.dir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		dir = wd
	}
	path, err := workflow.WriteWorkflowInit(kind, dir, wfInitOpts.force)
	if err != nil {
		return err
	}
	c.Printf("wrote %s\n", path)
	c.Printf("run it: gofer wf run --template %s -p <project> --var %s=<value>\n", kind, initKindVar(kind))
	c.Printf("the server reads it from the project workspace, so commit it first: git add %s\n", path)
	return nil
}

// initKindVar names the one required variable a starter of kind carries, for the run
// hint printed after writing it.
func initKindVar(kind string) string {
	if kind == workflow.InitRelease {
		return "version"
	}
	return "commit"
}

func runWorkflowPick(c *gcli.Command, _ []string) error {
	id := argID(c)
	step, fan := wfPickOpts.step, wfPickOpts.fan
	if a := c.Arg("step"); a != nil && a.String() != "" {
		step, _ = strconv.Atoi(a.String())
	}
	if a := c.Arg("fan"); a != nil && a.String() != "" {
		fan, _ = strconv.Atoi(a.String())
	}
	if id == "" || step < 1 || fan < 1 {
		return fmt.Errorf("workflow pick requires <id>, --step and --fan")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	wf, err := cli.PickWorkflowFan(id, step, fan)
	if err != nil {
		return err
	}
	c.Printf("workflow %s picked step=%d fan=%d status=%s\n", wf.ID, step, fan, wf.Status)
	if !wfPickOpts.merge {
		return nil
	}
	cur, err := cli.GetWorkflow(id)
	if err != nil {
		return err
	}
	jobID := ""
	for _, st := range cur.Steps {
		if st.StepIndex == step && st.FanIndex == fan {
			jobID = st.JobID // the latest attempt comes last
		}
	}
	if jobID == "" {
		return fmt.Errorf("picked fan %d of step %d has no job to merge", fan, step)
	}
	res, err := cli.MergeJobWorktree(jobID, job.MergeOptions{Squash: wfPickOpts.squash, CleanupOthers: wfPickOpts.cleanupOthers})
	if err != nil {
		return fmt.Errorf("picked, but merge failed (retry with `gofer job worktree merge %s`): %w", jobID, err)
	}
	c.Printf("merged worktree %s into main checkout\n", res.Branch)
	for _, other := range res.Cleaned {
		c.Printf("cleaned sibling worktree of job %s\n", other)
	}
	return nil
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
	if file == "" && wfRunOpts.template == "" {
		return fmt.Errorf("workflow run requires a <file> argument")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	var wf client.Workflow
	var spec workflow.Spec
	if wfRunOpts.template != "" {
		vars, verr := parseVarFlags(wfRunOpts.vars)
		if verr != nil {
			return verr
		}
		// -p is the project the template is resolved against (its own
		// .gofer/workflows, then the global ones) and the workflow's default project. It
		// travels as the body's project, not as a template variable: a template that does
		// not declare `project` would refuse the unknown variable (the server fills a
		// declared one from it).
		wf, err = cli.SubmitWorkflowTemplate(wfRunOpts.template, wfRunOpts.project, vars)
	} else {
		spec, err = workflow.ParseWorkflowFile(file)
		if err == nil {
			if wfRunOpts.project != "" {
				spec.Project = wfRunOpts.project
			}
			wf, err = cli.SubmitWorkflow(spec)
		}
	}
	if err != nil {
		return err
	}
	c.Printf("workflow %s submitted: status=%s steps=%d\n", wf.ID, wf.Status, wf.TotalSteps)
	if wfRunOpts.template == "" {
		printStepOverview(c, spec)
	}

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
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
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
	c.Printf("current_step: %s\n", workflowStepText(wf.Status, wf.CurrentStep, wf.TotalSteps))
	if wf.Error != "" {
		c.Printf("error:        %s\n", wf.Error)
	}
	c.Println("steps:")
	printStepChain(c, wf.Steps)
	return nil
}

// runWorkflowRerun re-runs a finished workflow as a NEW workflow (design §一·7). The
// selectors are exclusive: --failed keeps only the failed steps (a failed fan-out step
// only its failed fans), --from <step> keeps that step and everything after it. The
// original workflow is never modified; the new one records rerun_of.
func runWorkflowRerun(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("workflow rerun requires an <id> argument")
	}
	if wfRerunOpts.failed == (wfRerunOpts.from != "") {
		return fmt.Errorf("workflow rerun needs exactly one of --failed or --from <step>")
	}
	mode, step := workflow.RerunModeFailed, ""
	if !wfRerunOpts.failed {
		mode, step = workflow.RerunModeFrom, wfRerunOpts.from
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	wf, err := cli.RerunWorkflow(id, mode, step)
	if err != nil {
		return err
	}
	c.Printf("workflow %s rerun of %s: status=%s steps=%d\n", wf.ID, id, wf.Status, wf.TotalSteps)
	if !wfRerunOpts.watch {
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

// printStepChain renders a workflow's started step-jobs as a table
// (STEP/ATT/FAN/NAME/RUNNER/STATUS/EXIT/TIME, design §一·6). A step not yet reached has
// no row (the chain is strictly serial), so an empty chain prints a friendly hint. A
// retried step contributes one row per attempt and a fan-out step one row per fan, so
// the ATT/FAN columns expose the v2 dimensions; both render "-" for a v1 single-job
// step. The failure excerpts follow the table, since they are multi-line.
func printStepChain(c *gcli.Command, steps []client.WorkflowStep) {
	if len(steps) == 0 {
		c.Println("  (no steps started yet)")
		return
	}
	c.Printf("  %-5s %-4s %-4s %-18s %-10s %-10s %-5s %s\n", "STEP", "ATT", "FAN", "NAME", "RUNNER", "STATUS", "EXIT", "TIME")
	var excerpts []client.WorkflowStep
	for _, st := range steps {
		c.Printf("  %-5d %-4s %-4s %-18s %-10s %-10s %-5s %s\n",
			st.StepIndex, attemptCol(st.Attempt), fanCol(st.FanIndex),
			stepName(st), runnerCol(st.Runner), st.Status, exitCol(st.ExitCode), durationCol(st.DurationSec))
		if st.FailureExcerpt != "" {
			excerpts = append(excerpts, st)
		}
	}
	for _, st := range excerpts {
		c.Printf("\nfailed %s (%s):\n", stepRowLabel(st), st.Status)
		for _, line := range strings.Split(st.FailureExcerpt, "\n") {
			c.Printf("  | %s\n", line)
		}
	}
}

// stepRowLabel names a table row for the excerpt heading: the step and, when it is a
// fan / retry row, its fan and attempt.
func stepRowLabel(st client.WorkflowStep) string {
	label := fmt.Sprintf("step %d", st.StepIndex)
	if st.FanIndex > 0 {
		label += fmt.Sprintf(" fan %d", st.FanIndex)
	}
	if st.Attempt > 1 {
		label += fmt.Sprintf(" attempt %d", st.Attempt)
	}
	if st.Name != "" {
		label += " " + st.Name
	}
	return label
}

// runnerCol renders the runner a row ran on, "-" when the row never started.
func runnerCol(runner string) string {
	if runner == "" {
		return "-"
	}
	return runner
}

// exitCol renders a row's exit code, "-" when it has none (not started / not a process).
func exitCol(code *int) string {
	if code == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *code)
}

// durationCol renders a row's runtime as <n>s (<m>m<n>s past a minute), "-" when unknown.
func durationCol(sec int64) string {
	if sec <= 0 {
		return "-"
	}
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	return fmt.Sprintf("%dm%ds", sec/60, sec%60)
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
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
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
	c.Printf("%-26s %-10s %-11s %-24s %s\n", "ID", "STATUS", "STEP", "TITLE", "CREATED")
	for _, wf := range list {
		c.Printf("%-26s %-10s %-11s %-24s %s\n",
			wf.ID, wf.Status, workflowStepText(wf.Status, wf.CurrentStep, wf.TotalSteps),
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
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
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
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
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
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
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

// workflowStepText renders the step progress: once a workflow is done the stored
// current_step points one past the last step (2/1), so it is clamped to the total
// and marked "(done)".
func workflowStepText(status string, current, total int) string {
	if total > 0 && current > total {
		current = total
	}
	text := fmt.Sprintf("%d/%d", current, total)
	if status == "done" {
		text += " (done)"
	}
	return text
}
