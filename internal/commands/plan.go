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
