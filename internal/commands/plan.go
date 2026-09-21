package commands

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

func validPlanID(id string) bool {
	if len(id) < jobstore.PlanIDMinLength {
		return false
	}
	for _, r := range id {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == ':' || r == '-' {
			continue
		}
		return false
	}
	return true
}

var planCreateOpts = struct {
	planID  string
	title   string
	desc    string
	project string
}{}

var planListOpts = struct {
	status string
}{}

var planAddTodoOpts = struct {
	job  string
	note string
	todoDispatchFlags
}{}

var planSetTodoOpts = struct {
	undone     bool
	status     string
	note       optionalString
	appendNote string
	todoDispatchFlags
}{}

// todoDispatchFlags are the PLAN-02 P2 dispatch flags, shared by `plan add-todo` and
// `plan set-todo` so the two commands describe an item the same way (and so a caller can
// move from "describe" to "run" with one command). An empty value means the flag was not
// given: patch() leaves that field alone.
type todoDispatchFlags struct {
	assign   string
	project  string
	template string
	vars     gcli.Strings // repeatable: --var k=v
	verify   string
	review   bool
	runner   string
	cwd      string
	timeout  int
}

func (f *todoDispatchFlags) bind(c *gcli.Command) {
	c.StrOpt(&f.assign, "assign", "", "", "agent key that runs this item; a ready item with an assignee is dispatched at once")
	c.StrOpt(&f.project, "project", "", "", "project this item runs in (default: the plan's project)")
	c.StrOpt(&f.template, "template", "", "", "task-book template to render this item's prompt from (default: the plan/todo prompt)")
	c.VarOpt(&f.vars, "var", "", "template variable k=v (repeatable; requires --template)")
	c.StrOpt(&f.verify, "verify", "", "", "command run after the agent finishes (shell-words, no shell); a non-zero exit fails the job")
	c.BoolOpt(&f.review, "review", "", false, "require human review: on a normal completion the run parks in needs_review until someone accepts or rejects it")
	c.StrOpt(&f.runner, "runner", "", "", "runner key for the job (default: the server's built-in local runner)")
	c.StrOpt(&f.cwd, "cwd", "", "", "working dir within the project (default: the project root)")
	c.IntOpt(&f.timeout, "timeout", "", 0, "job timeout in seconds (0 = the server default)")
}

// patch builds the update/create patch from the flags that were given. --var without
// --template is refused: it would be a value nobody renders.
func (f *todoDispatchFlags) patch() (jobstore.TodoPatch, error) {
	var p jobstore.TodoPatch
	if f.assign != "" {
		p.Assignee = &f.assign
	}
	if f.project != "" {
		p.ProjectKey = &f.project
	}
	if f.template != "" {
		p.Template = &f.template
	}
	if len(f.vars) > 0 {
		if f.template == "" {
			return jobstore.TodoPatch{}, fmt.Errorf("--var requires --template")
		}
		vars, err := parseVarFlags(f.vars)
		if err != nil {
			return jobstore.TodoPatch{}, err
		}
		p.Vars = vars
	}
	if strings.TrimSpace(f.verify) != "" {
		words, err := splitShellWords(f.verify)
		if err != nil {
			return jobstore.TodoPatch{}, err
		}
		if len(words) == 0 {
			return jobstore.TodoPatch{}, fmt.Errorf("--verify is empty")
		}
		p.Verify = words
	}
	if f.review {
		p.Review = &f.review
	}
	if f.runner != "" {
		p.Runner = &f.runner
	}
	if f.cwd != "" {
		p.Cwd = &f.cwd
	}
	if f.timeout > 0 {
		p.TimeoutSec = &f.timeout
	}
	return p, nil
}

var planAskOpts = struct {
	plan     string
	title    string
	question string
	options  gcli.Strings // repeatable: --option
	timeout  string
}{}

var planDecisionsOpts = struct {
	state string
	plan  string
}{}

var planAnswerOpts = struct {
	answer string
}{}

// NewPlanCmd builds the `plan` command group for lightweight job grouping.
func NewPlanCmd() *gcli.Command {
	return &gcli.Command{
		Name: "plan",
		Desc: "Create and inspect job grouping plans",
		Subs: []*gcli.Command{
			{
				Name: "create",
				Desc: "Create a plan",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&planCreateOpts.planID, "plan-id", "", "", "plan id (optional; server generates when empty)")
					c.StrOpt(&planCreateOpts.title, "title", "", "", "plan title")
					c.StrOpt(&planCreateOpts.desc, "desc", "", "", "plan description")
					c.StrOpt(&planCreateOpts.project, "project", "", "", "project the plan's items run in (PLAN-02: an item may still override it)")
				},
				Func: runPlanCreate,
			},
			{
				Name:    "list",
				Aliases: []string{"ls"},
				Desc:    "List plans",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&planListOpts.status, "status", "", "", "filter by status (open/active/done/archived)")
				},
				Func: runPlanList,
			},
			{
				Name: "show",
				Desc: "Show a plan and its jobs",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("id", "plan id", true)
				},
				Func: runPlanShow,
			},
			{
				Name: "attach",
				Desc: "Attach an existing job to a plan",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("plan-id", "plan id", true)
					c.AddArg("job-id", "job id", true)
				},
				Func: runPlanAttach,
			},
			{
				Name: "set-status",
				Desc: "Set a plan status",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("plan-id", "plan id", true)
					c.AddArg("status", "status (open/active/done/archived)", true)
				},
				Func: runPlanSetStatus,
			},
			{
				Name: "archive",
				Desc: "Archive a plan",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("plan-id", "plan id", true)
				},
				Func: runPlanArchive,
			},
			{
				Name:    "add-todo",
				Aliases: []string{"todo-add"},
				Desc:    "Add a todo to a plan (the item is created pending; --assign describes who runs it once it is ready)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("plan-id", "plan id", true)
					c.AddArg("title", "todo title", true)
					c.StrOpt(&planAddTodoOpts.job, "job", "", "", "bind the todo to a job id (optional)")
					c.StrOpt(&planAddTodoOpts.note, "note", "", "", "short remark for the todo (optional); append later with gofer plan set-todo <todo-id> --append-note \"...\"")
					planAddTodoOpts.todoDispatchFlags.bind(c)
				},
				Func: runPlanAddTodo,
			},
			{
				Name:    "set-todo",
				Aliases: []string{"todo-done"},
				Desc:    "Update a todo: --status pending|ready|doing|done|skipped and/or --note, and/or its dispatch fields; bare = done, --undone = pending. A ready item with an assignee is dispatched at once",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("todo-id", "todo id", true)
					c.BoolOpt(&planSetTodoOpts.undone, "undone", "", false, "mark the todo not done (= --status pending)")
					c.StrOpt(&planSetTodoOpts.status, "status", "", "", "lifecycle status: pending|ready|doing|done|skipped (wins over --undone)")
					c.VarOpt(&planSetTodoOpts.note, "note", "", "set the todo note; --note \"\" clears it (kept unchanged when omitted)")
					c.StrOpt(&planSetTodoOpts.appendNote, "append-note", "", "", "append a line to the todo note (mutually exclusive with --note)")
					planSetTodoOpts.todoDispatchFlags.bind(c)
				},
				Func: runPlanSetTodo,
			},
			{
				Name: "dispatch",
				Desc: "Dispatch a todo's assigned agent now, whatever its status (the explicit fallback to \"ready + assigned\")",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("todo-id", "todo id", true)
				},
				Func: runPlanDispatch,
			},
			{
				Name: "ask",
				Desc: "Raise a decision question for a human (does NOT wait; blocking wait is the MCP gofer_ask_human semantics)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&planAskOpts.plan, "plan", "", "", "plan id to attach the decision to (optional; empty = global question)")
					c.StrOpt(&planAskOpts.title, "title", "", "", "decision title (required)")
					c.StrOpt(&planAskOpts.question, "question", "", "", "the question for the human (required)")
					c.VarOpt(&planAskOpts.options, "option", "", "answer option (repeatable; omit all for free-text answer)")
					c.StrOpt(&planAskOpts.timeout, "timeout", "", "", "answer timeout, e.g. 30m (default 30m; clamped server-side to [2s,24h])")
				},
				Func: runPlanAsk,
			},
			{
				Name: "decisions",
				Desc: "List decisions, optionally filtered by state/plan",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&planDecisionsOpts.state, "state", "", "", "filter by state (OPEN/ANSWERED/EXPIRED)")
					c.StrOpt(&planDecisionsOpts.plan, "plan", "", "", "filter by plan id")
				},
				Func: runPlanDecisions,
			},
			{
				Name: "answer",
				Desc: "Answer an OPEN decision",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("decision-id", "decision id", true)
					c.StrOpt(&planAnswerOpts.answer, "answer", "", "", "the answer text (required)")
				},
				Func: runPlanAnswer,
			},
		},
	}
}

func runPlanCreate(c *gcli.Command, _ []string) error {
	if planCreateOpts.planID != "" && !validPlanID(planCreateOpts.planID) {
		return fmt.Errorf("invalid plan id: must be at least 9 characters (short ids are too easy to collide) and contain only A-Z, a-z, 0-9, '.', '_', ':' or '-'")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	p, err := cli.CreatePlan(planCreateOpts.planID, planCreateOpts.title, planCreateOpts.desc, planCreateOpts.project)
	if err != nil {
		return err
	}
	c.Printf("plan %s created: status=%s\n", p.PlanID, p.Status)
	return nil
}

func runPlanList(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	plans, err := cli.ListPlans(planListOpts.status)
	if err != nil {
		return err
	}
	if len(plans) == 0 {
		c.Println("no plans matched the given filter")
		return nil
	}
	c.Printf("%-30s %-10s %-14s %-24s %s\n", "PLAN ID", "STATUS", "PROGRESS", "TITLE", "CREATED")
	for _, p := range plans {
		c.Printf("%-30s %-10s %-14s %-24s %s\n",
			p.PlanID, p.Status, formatCompletionShort(p), truncate(p.Title, 24), formatStarted(p.CreatedAt))
	}
	return nil
}

func runPlanShow(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("plan show requires an <id> argument")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	p, err := cli.GetPlan(id)
	if err != nil {
		return err
	}
	printPlan(c, p)
	printPlanJobs(c, p.Jobs)
	printPlanTodos(c, p.Todos)
	return nil
}

func runPlanAttach(c *gcli.Command, _ []string) error {
	planID, jobID := argValue(c, "plan-id"), argValue(c, "job-id")
	if planID == "" || jobID == "" {
		return fmt.Errorf("plan attach requires <plan-id> and <job-id>")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	if _, err := cli.AttachJob(planID, jobID); err != nil {
		return err
	}
	c.Printf("job %s attached to plan %s\n", jobID, planID)
	return nil
}

func runPlanSetStatus(c *gcli.Command, _ []string) error {
	return setPlanStatus(c, argValue(c, "plan-id"), argValue(c, "status"))
}

func runPlanArchive(c *gcli.Command, _ []string) error {
	return setPlanStatus(c, argValue(c, "plan-id"), "archived")
}

func setPlanStatus(c *gcli.Command, planID, status string) error {
	if planID == "" || status == "" {
		return fmt.Errorf("plan set-status requires <plan-id> and <status>")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	p, err := cli.UpdatePlan(planID, status, nil)
	if err != nil {
		return err
	}
	c.Printf("plan %s -> %s\n", p.PlanID, p.Status)
	return nil
}

func runPlanAddTodo(c *gcli.Command, _ []string) error {
	planID, title := argValue(c, "plan-id"), argValue(c, "title")
	if planID == "" || title == "" {
		return fmt.Errorf("plan add-todo requires <plan-id> and <title>")
	}
	patch, err := planAddTodoOpts.todoDispatchFlags.patch()
	if err != nil {
		return err
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	t, err := cli.AddTodo(planID, title, planAddTodoOpts.job, planAddTodoOpts.note, patch)
	if err != nil {
		return err
	}
	c.Printf("todo %s added to plan %s\n", t.TodoID, planID)
	return nil
}

// runPlanDispatch is the explicit fallback (PLAN-02 P2): it dispatches the item's
// assignee regardless of the item's status, and reports BOTH outcomes — the job it
// started, or why nothing was started (the item already has a live job is not an error:
// the caller asked, and the answer is "it is already running").
func runPlanDispatch(c *gcli.Command, _ []string) error {
	todoID := argValue(c, "todo-id")
	if todoID == "" {
		return fmt.Errorf("plan dispatch requires a <todo-id>")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	d, err := cli.DispatchTodo(todoID)
	if err != nil {
		return err
	}
	if d.Job == nil {
		c.Printf("todo %s not dispatched: %s\n", d.Todo.TodoID, d.Reason)
		return nil
	}
	c.Printf("todo %s dispatched: job %s (agent=%s, project=%s)\n",
		d.Todo.TodoID, d.Job.ID, d.Job.Agent, d.Job.ProjectKey)
	return nil
}

// optionalString is a string flag that remembers whether it was given at all, so
// an explicit empty value (`--note ""`, which clears the note) is distinguishable
// from an omitted flag (note kept unchanged).
type optionalString struct {
	val string
	set bool
}

// Set implements flag.Value.
func (o *optionalString) Set(s string) error {
	o.val, o.set = s, true
	return nil
}

// String implements flag.Value.
func (o *optionalString) String() string { return o.val }

// resolveTodoUpdate maps the set-todo flags onto one update. --status wins; a
// bare call keeps the legacy meaning (done, or pending with --undone); --note or
// --append-note or a dispatch field alone leaves the status untouched.
func resolveTodoUpdate(status string, undone bool, note optionalString, appendNote string, patch jobstore.TodoPatch) (client.TodoUpdate, error) {
	if note.set && appendNote != "" {
		return client.TodoUpdate{}, fmt.Errorf("plan set-todo: --note and --append-note are mutually exclusive")
	}
	if status == "" {
		switch {
		case undone:
			status = "pending"
		// The legacy bare form means "done" — but only when the caller asked for nothing
		// else: `--assign omp` on its own must not also complete the item.
		case !note.set && appendNote == "" && patch.Empty():
			status = "done"
		}
	}
	u := client.TodoUpdate{Status: status, AppendNote: appendNote, TodoPatch: patch}
	if note.set {
		v := note.val
		u.Note = &v
	}
	return u, nil
}

func runPlanSetTodo(c *gcli.Command, _ []string) error {
	todoID := argValue(c, "todo-id")
	if todoID == "" {
		return fmt.Errorf("plan set-todo requires a <todo-id>")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	patch, err := planSetTodoOpts.todoDispatchFlags.patch()
	if err != nil {
		return err
	}
	upd, err := resolveTodoUpdate(planSetTodoOpts.status, planSetTodoOpts.undone,
		planSetTodoOpts.note, planSetTodoOpts.appendNote, patch)
	if err != nil {
		return err
	}
	// ONE request carries the lifecycle fields, the appended line and the dispatch
	// fields, so a failure never leaves a half-applied update.
	t, err := cli.PatchTodo(todoID, upd)
	if err != nil {
		return err
	}
	c.Printf("todo %s status=%s\n", t.TodoID, t.Status)
	if t.DispatchError != "" {
		// A dispatch the update triggered was refused: say so here rather than leaving
		// the caller to discover it on the plan page.
		c.Printf("  dispatch_error: %s\n", t.DispatchError)
	} else if t.JobID != "" && t.Status == "doing" {
		c.Printf("  dispatched: job %s (agent=%s)\n", t.JobID, t.Assignee)
	}
	return nil
}

// runPlanAsk raises a decision and prints its id. It does NOT block waiting
// for the answer (plan D5): blocking wait is the MCP gofer_ask_human semantics;
// a human follows up with `plan decisions --state OPEN` / `plan answer`.
// The timeout clamp is owned by the store (HIGH-2) — the CLI passes the parsed
// seconds through unchanged.
func runPlanAsk(c *gcli.Command, _ []string) error {
	if planAskOpts.title == "" || planAskOpts.question == "" {
		return fmt.Errorf("plan ask requires --title and --question")
	}
	var timeoutSec int64
	if planAskOpts.timeout != "" {
		dur, err := time.ParseDuration(planAskOpts.timeout)
		if err != nil {
			return fmt.Errorf("invalid --timeout %q: %w", planAskOpts.timeout, err)
		}
		timeoutSec = int64(dur / time.Second)
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	d, err := cli.AskDecision(planAskOpts.plan, planAskOpts.title, planAskOpts.question,
		[]string(planAskOpts.options), timeoutSec)
	if err != nil {
		return err
	}
	c.Printf("decision %s asked: state=%s timeout=%ds\n", d.ID, d.State, d.TimeoutSec)
	return nil
}

func runPlanDecisions(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	list, err := cli.ListDecisions(planDecisionsOpts.state, planDecisionsOpts.plan)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		c.Println("no decisions matched the given filter")
		return nil
	}
	c.Printf("%-32s %-10s %-24s %s\n", "DECISION ID", "STATE", "TITLE", "ASKED")
	for _, d := range list {
		c.Printf("%-32s %-10s %-24s %s\n",
			d.ID, d.State, truncate(d.Title, 24), formatStarted(d.AskedAt))
	}
	return nil
}

func runPlanAnswer(c *gcli.Command, _ []string) error {
	id := argValue(c, "decision-id")
	if id == "" || planAnswerOpts.answer == "" {
		return fmt.Errorf("plan answer requires <decision-id> and --answer")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	d, err := cli.AnswerDecision(id, planAnswerOpts.answer)
	if err != nil {
		return err
	}
	c.Printf("decision %s answered: state=%s\n", d.ID, d.State)
	return nil
}

func argValue(c *gcli.Command, name string) string {
	if c != nil {
		if a := c.Arg(name); a != nil {
			return a.String()
		}
	}
	return ""
}

func printPlan(c *gcli.Command, p client.Plan) {
	c.Printf("id:          %s\n", p.PlanID)
	if p.Title != "" {
		c.Printf("title:       %s\n", p.Title)
	}
	if p.Description != "" {
		c.Printf("description: %s\n", p.Description)
	}
	c.Printf("status:      %s\n", p.Status)
	if p.Project != "" {
		c.Printf("project:     %s\n", p.Project)
	}
	if p.Owner != "" {
		c.Printf("owner:       %s\n", p.Owner)
	}
	if s := formatCompletion(p); s != "" {
		c.Printf("progress:    %s\n", s)
	}
	// PLAN-02 P2: what the plan has burned so far, by agent — the question a reader
	// asks before adding more work to it.
	if s := formatPlanUsage(p.Usage); s != "" {
		c.Printf("usage:       %s\n", s)
	}
	c.Println("jobs:")
}

// formatPlanUsage renders a plan's roll-up as `total 1.2M tokens / $3.45 (omp 5 jobs,
// codex 2 jobs)`. Agents are listed by job count (most first, ties by key) — the
// interesting fact is who is doing the work, not the map's order. "" when the plan has
// no jobs at all (an older server sends no usage either).
func formatPlanUsage(u *client.PlanUsage) string {
	if u == nil || u.Jobs == 0 {
		return ""
	}
	s := fmt.Sprintf("total %s tokens / $%.2f", formatTokenCount(u.TotalTokens), u.CostUSD)
	keys := make([]string, 0, len(u.ByAgent))
	for k := range u.ByAgent {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := u.ByAgent[keys[i]], u.ByAgent[keys[j]]
		if a.Jobs != b.Jobs {
			return a.Jobs > b.Jobs
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		name := k
		if name == "" {
			name = "(unknown)"
		}
		parts = append(parts, fmt.Sprintf("%s %d jobs", name, u.ByAgent[k].Jobs))
	}
	if len(parts) > 0 {
		s += " (" + strings.Join(parts, ", ") + ")"
	}
	return s
}

// formatTokenCount shortens a token count for a one-line summary: 1234 → "1.2k",
// 1234567 → "1.2M" (the exact numbers stay in `job show`/the web).
func formatTokenCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	}
	return strconv.FormatInt(n, 10)
}

// formatCompletion renders a plan's progress for `plan show`: the server's
// completion (todos first, jobs as fallback) plus the other dimension, e.g.
// "5/6 todos (83%) · jobs: 10 done / 1 failed / 11 total". An older server sends
// no completion; then the legacy manual progress (if set) is shown as before.
func formatCompletion(p client.Plan) string {
	c := p.Completion
	if c == nil {
		if p.Progress > 0 {
			return fmt.Sprintf("%d", p.Progress)
		}
		return ""
	}
	if c.Percent == nil {
		return "—"
	}
	s := fmt.Sprintf("%d/%d %s (%d%%)", c.Done, c.Total, c.Basis, *c.Percent)
	j := p.Counts
	switch {
	case j != nil && j.Total > 0 && c.Basis == jobstore.CompletionTodos:
		s += fmt.Sprintf(" · jobs: %d done / %d failed / %d total", j.Done, j.Failed, j.Total)
	case j != nil && c.Basis == jobstore.CompletionJobs:
		var extra []string
		if j.Failed > 0 {
			extra = append(extra, fmt.Sprintf("%d failed", j.Failed))
		}
		if j.Running > 0 {
			extra = append(extra, fmt.Sprintf("%d running", j.Running))
		}
		if j.Queued > 0 {
			extra = append(extra, fmt.Sprintf("%d queued", j.Queued))
		}
		if len(extra) > 0 {
			s += " · " + strings.Join(extra, ", ")
		}
	}
	return s
}

// formatCompletionShort is the PROGRESS column of `plan list`: "5/6 todos",
// "10/11 jobs", or "—" when there is nothing to measure (or an older server).
func formatCompletionShort(p client.Plan) string {
	c := p.Completion
	if c == nil || c.Percent == nil {
		return "—"
	}
	return fmt.Sprintf("%d/%d %s", c.Done, c.Total, c.Basis)
}

func printPlanJobs(c *gcli.Command, jobs []job.JobResult) {
	if len(jobs) == 0 {
		c.Println("  (no jobs attached yet)")
		return
	}
	c.Printf("  %-26s %-12s %-16s %s\n", "JOB ID", "STATUS", "AGENT", "STARTED")
	for _, j := range jobs {
		c.Printf("  %-26s %-12s %-16s %s\n", j.ID, j.Status, j.Agent, formatStarted(j.StartedAt))
	}
}

func printPlanTodos(c *gcli.Command, todos []client.Todo) {
	c.Println("todos:")
	if len(todos) == 0 {
		c.Println("  (no todos yet)")
		return
	}
	for _, t := range todos {
		// Older servers may not send status yet — fall back to the done flag.
		status := t.Status
		if status == "" && t.Done {
			status = "done"
		}
		box := "[ ]"
		switch status {
		case "done":
			box = "[x]"
		case "doing":
			box = "[~]"
		case "skipped":
			box = "[-]"
		case "ready":
			// PLAN-02 P2: queued for dispatch (assigned → it runs immediately, so a
			// ready item on screen means it is waiting for an assignee or for its
			// previous job to end).
			box = "[>]"
		}
		// PLAN-02 P2: who runs this item — the one fact a reader needs to see the
		// assignment at a glance.
		assign := ""
		if t.Assignee != "" {
			assign = "  (assignee=" + t.Assignee + ")"
		}
		bind := ""
		if t.JobID != "" {
			bind = "  (job=" + t.JobID + ")"
		}
		c.Printf("  %s %-26s %s%s%s\n", box, t.TodoID, t.Title, assign, bind)
		// A dispatch that was refused: the item says WHY here rather than leaving the
		// failure to be discovered as "it never ran".
		if t.DispatchError != "" {
			c.Printf("      dispatch_error: %s\n", t.DispatchError)
		}
		// SUP-01 C：挂接在该 todo 上的 job（新→旧，最多 10 条，服务端已限流）——
		// "这一项被谁跑过、结果如何、跑了多久"。完整历史用 `job list --todo`。
		if len(t.Jobs) > 0 {
			parts := make([]string, 0, len(t.Jobs))
			for _, j := range t.Jobs {
				parts = append(parts, fmt.Sprintf("%s %s %s %ds", j.ID, j.Status, j.Agent, j.DurationSec))
			}
			c.Printf("      jobs: %s\n", strings.Join(parts, " · "))
		}
		if t.Note != "" {
			for i, line := range strings.Split(t.Note, "\n") {
				if i == 0 {
					c.Printf("      note: %s\n", line)
				} else {
					c.Printf("            %s\n", line)
				}
			}
		}
	}
}
