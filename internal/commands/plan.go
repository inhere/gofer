package commands

import (
	"fmt"
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
	planID string
	title  string
	desc   string
}{}

var planListOpts = struct {
	status string
}{}

var planAddTodoOpts = struct {
	job  string
	note string
}{}

var planSetTodoOpts = struct {
	undone     bool
	status     string
	note       optionalString
	appendNote string
}{}

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
				Desc:    "Add a todo to a plan",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("plan-id", "plan id", true)
					c.AddArg("title", "todo title", true)
					c.StrOpt(&planAddTodoOpts.job, "job", "", "", "bind the todo to a job id (optional)")
					c.StrOpt(&planAddTodoOpts.note, "note", "", "", "short remark for the todo (optional); append later with gofer plan set-todo <todo-id> --append-note \"...\"")
				},
				Func: runPlanAddTodo,
			},
			{
				Name:    "set-todo",
				Aliases: []string{"todo-done"},
				Desc:    "Update a todo: --status pending|doing|done|skipped and/or --note; bare = done, --undone = pending",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("todo-id", "todo id", true)
					c.BoolOpt(&planSetTodoOpts.undone, "undone", "", false, "mark the todo not done (= --status pending)")
					c.StrOpt(&planSetTodoOpts.status, "status", "", "", "lifecycle status: pending|doing|done|skipped (wins over --undone)")
					c.VarOpt(&planSetTodoOpts.note, "note", "", "set the todo note; --note \"\" clears it (kept unchanged when omitted)")
					c.StrOpt(&planSetTodoOpts.appendNote, "append-note", "", "", "append a line to the todo note (mutually exclusive with --note)")
				},
				Func: runPlanSetTodo,
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
	p, err := cli.CreatePlan(planCreateOpts.planID, planCreateOpts.title, planCreateOpts.desc)
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
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	t, err := cli.AddTodo(planID, title, planAddTodoOpts.job, planAddTodoOpts.note)
	if err != nil {
		return err
	}
	c.Printf("todo %s added to plan %s\n", t.TodoID, planID)
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

// todoUpdate is the single request `plan set-todo` sends: Status "" leaves the
// status alone, Note nil leaves the note alone, AppendNote "" appends nothing.
type todoUpdate struct {
	Status     string
	Note       *string
	AppendNote string
}

// resolveTodoUpdate maps the set-todo flags onto one update. --status wins; a
// bare call keeps the legacy meaning (done, or pending with --undone); --note or
// --append-note alone leaves the status untouched.
func resolveTodoUpdate(status string, undone bool, note optionalString, appendNote string) (todoUpdate, error) {
	if note.set && appendNote != "" {
		return todoUpdate{}, fmt.Errorf("plan set-todo: --note and --append-note are mutually exclusive")
	}
	if status == "" {
		switch {
		case undone:
			status = "pending"
		case !note.set && appendNote == "":
			status = "done"
		}
	}
	u := todoUpdate{Status: status, AppendNote: appendNote}
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
	upd, err := resolveTodoUpdate(planSetTodoOpts.status, planSetTodoOpts.undone,
		planSetTodoOpts.note, planSetTodoOpts.appendNote)
	if err != nil {
		return err
	}
	var t client.Todo
	if upd.AppendNote != "" {
		// status (if any) and the appended line travel in ONE request, so a
		// failure never leaves a half-applied update.
		t, err = cli.UpdateTodoStatusAppend(todoID, upd.Status, upd.AppendNote)
	} else {
		t, err = cli.UpdateTodoStatus(todoID, upd.Status, upd.Note)
	}
	if err != nil {
		return err
	}
	c.Printf("todo %s status=%s\n", t.TodoID, t.Status)
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
	if p.Owner != "" {
		c.Printf("owner:       %s\n", p.Owner)
	}
	if s := formatCompletion(p); s != "" {
		c.Printf("progress:    %s\n", s)
	}
	c.Println("jobs:")
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
		}
		bind := ""
		if t.JobID != "" {
			bind = "  (job=" + t.JobID + ")"
		}
		c.Printf("  %s %-26s %s%s\n", box, t.TodoID, t.Title, bind)
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
