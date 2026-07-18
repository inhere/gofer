package commands

import (
	"fmt"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

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
	undone bool
	status string
	note   string
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
					c.StrOpt(&planAddTodoOpts.note, "note", "", "", "short remark for the todo (optional)")
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
					c.StrOpt(&planSetTodoOpts.note, "note", "", "", "set the todo note (kept unchanged when omitted)")
				},
				Func: runPlanSetTodo,
			},
		},
	}
}

func runPlanCreate(c *gcli.Command, _ []string) error {
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
	c.Printf("%-30s %-10s %-24s %s\n", "PLAN ID", "STATUS", "TITLE", "CREATED")
	for _, p := range plans {
		c.Printf("%-30s %-10s %-24s %s\n",
			p.PlanID, p.Status, truncate(p.Title, 24), formatStarted(p.CreatedAt))
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

func runPlanSetTodo(c *gcli.Command, _ []string) error {
	todoID := argValue(c, "todo-id")
	if todoID == "" {
		return fmt.Errorf("plan set-todo requires a <todo-id>")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	// --status wins; bare invocation keeps the legacy semantics (done, or
	// pending with --undone). --note alone leaves the status untouched.
	status := planSetTodoOpts.status
	if status == "" && planSetTodoOpts.note == "" {
		status = "done"
		if planSetTodoOpts.undone {
			status = "pending"
		}
	} else if status == "" && planSetTodoOpts.undone {
		status = "pending"
	}
	var note *string
	if planSetTodoOpts.note != "" {
		note = &planSetTodoOpts.note
	}
	t, err := cli.UpdateTodoStatus(todoID, status, note)
	if err != nil {
		return err
	}
	c.Printf("todo %s status=%s\n", t.TodoID, t.Status)
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
	if p.Progress > 0 {
		c.Printf("progress:    %d\n", p.Progress)
	}
	c.Println("jobs:")
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
			c.Printf("      note: %s\n", t.Note)
		}
	}
}
