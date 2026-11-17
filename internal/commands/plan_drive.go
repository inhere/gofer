package commands

import (
	"fmt"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// `gofer plan drive` (design docs/design/2026-11-16-plan-driver-autonomy-design.md §1,
// plan R4): the switches of a plan's autonomy — on at a level with a budget, off, and the
// state. It lives beside `plan decisions` (the decision channel), which it must not be
// confused with: decisions are questions an agent asks a human, the driver is gofer
// advancing the plan by itself.
//
// This batch accepts the `record` level only (record observes and logs the action it
// WOULD take). `assist` / `auto` are refused by the server with a message that says
// "later version", so the CLI forwards whatever level it is given rather than judging
// levels of its own — one validator, one message.

var planDriveOpts struct {
	level     string
	maxJobs   int
	maxRework int
	maxHours  float64
}

// newPlanDriveCmd builds the `plan drive` group: on / off / status.
func newPlanDriveCmd() *gcli.Command {
	return &gcli.Command{
		Name: "drive",
		Desc: "Turn a plan's driver on or off and show its state (autonomous advance; this version supports --level record only)",
		Subs: []*gcli.Command{
			{
				Name: "on",
				Desc: "Enable the plan's driver: record what it would do, within an optional budget",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("plan-id", "plan id", true)
					c.StrOpt(&planDriveOpts.level, "level", "", job.PlanDriverLevelRecord, "autonomy level: record (the only level this version supports; assist / auto come later)")
					c.IntOpt(&planDriveOpts.maxJobs, "max-jobs", "", 0, "cap on the jobs the plan may start, rework and retries included (0 = unlimited)")
					c.IntOpt(&planDriveOpts.maxRework, "max-rework", "", 0, "cap on the plan's rework rounds (0 = unlimited)")
					c.Float64Opt(&planDriveOpts.maxHours, "max-hours", "", 0, "cap on wall-clock hours since the driver was enabled (0 = unlimited)")
				},
				Func: runPlanDriveOn,
			},
			{
				Name: "off",
				Desc: "Disable the plan's driver (idempotent: a plan that is already off answers the same)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("plan-id", "plan id", true)
				},
				Func: runPlanDriveOff,
			},
			{
				Name: "status",
				Desc: "Show whether the plan's driver is on, at which level and with which budget",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("plan-id", "plan id", true)
				},
				Func: runPlanDriveStatus,
			},
			{
				Name: "report",
				Desc: "Compare what the driver recorded it would do with what actually happened: agreement per action and per rule",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("plan-id", "plan id", true)
				},
				Func: runPlanDriveReport,
			},
		},
	}
}

func runPlanDriveOn(c *gcli.Command, _ []string) error {
	id := argValue(c, "plan-id")
	if id == "" {
		return fmt.Errorf("plan drive on requires a <plan-id>")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	st, err := cli.SetPlanDriver(id, planDriveOpts.level, job.PlanDriverBudget{
		MaxJobs: planDriveOpts.maxJobs, MaxRework: planDriveOpts.maxRework, MaxHours: planDriveOpts.maxHours,
	})
	if err != nil {
		return err
	}
	printPlanDriverStatus(c, st)
	return nil
}

func runPlanDriveOff(c *gcli.Command, _ []string) error {
	id := argValue(c, "plan-id")
	if id == "" {
		return fmt.Errorf("plan drive off requires a <plan-id>")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	st, err := cli.DisablePlanDriver(id)
	if err != nil {
		return err
	}
	printPlanDriverStatus(c, st)
	return nil
}

func runPlanDriveStatus(c *gcli.Command, _ []string) error {
	id := argValue(c, "plan-id")
	if id == "" {
		return fmt.Errorf("plan drive status requires a <plan-id>")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	st, err := cli.GetPlanDriver(id)
	if err != nil {
		return err
	}
	printPlanDriverStatus(c, st)
	return nil
}

func runPlanDriveReport(c *gcli.Command, _ []string) error {
	id := argValue(c, "plan-id")
	if id == "" {
		return fmt.Errorf("plan drive report requires a <plan-id>")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	rep, err := cli.GetPlanDriverReport(id)
	if err != nil {
		return err
	}
	printPlanDriverReport(c, rep)
	return nil
}

// printPlanDriverReport prints the report: the driver line, the budget against its spend,
// then one line per action and per rule. A rate is shown only when something was resolved,
// and the upgrade verdict names the condition so the number is not mistaken for a decision.
func printPlanDriverReport(c *gcli.Command, rep job.PlanDriverReport) {
	c.Printf("plan:    %s\n", rep.PlanID)
	if rep.Enabled {
		c.Printf("driver:  on (level=%s)\n", rep.Level)
	} else {
		c.Println("driver:  off")
	}
	c.Printf("budget:  %s\n", formatPlanDriverBudget(rep.Budget))
	c.Printf("spent:   jobs %d / rework %d / hours %.2f\n", rep.Spent.Jobs, rep.Spent.Rework, rep.Spent.Hours)
	c.Printf("actions: %d recorded, %d not observed yet (an action is observed for %dh)\n", rep.Total, rep.Pending, rep.WindowHours)
	if rep.Total == 0 {
		return
	}
	row := func(r job.PlanDriverReportRow) string {
		rate := "-"
		if r.Match+r.Mismatch+r.NoFollowup > 0 {
			rate = fmt.Sprintf("%.0f%%", r.Rate*100)
		}
		return fmt.Sprintf("total %d  match %d  mismatch %d  no_followup %d  pending %d  agreement %s",
			r.Total, r.Match, r.Mismatch, r.NoFollowup, r.Pending, rate)
	}
	c.Println("\nby action:")
	for _, r := range rep.ByAction {
		c.Printf("  %-18s %s\n", r.Action, row(r))
		verdict := "not yet"
		if r.UpgradeSuggested {
			verdict = "REACHED"
		}
		c.Printf("  %-18s last %d resolved: %d match (%.0f%%); upgrade condition (%d in a row at >= %.0f%%): %s\n", "",
			r.RecentN, r.RecentMatch, r.RecentRate*100, job.PlanDriverUpgradeWindow, job.PlanDriverUpgradeRate*100, verdict)
	}
	c.Println("\nby rule:")
	for _, r := range rep.ByRule {
		c.Printf("  %-18s %-28s %s\n", r.Action, r.Rule, row(r))
	}
}

// printPlanDriverStatus prints one driver state: the switch, the level, the budget and the
// accounting window. An off driver prints one line — the budget of a driver that is not
// running is not a fact worth reading.
func printPlanDriverStatus(c *gcli.Command, st job.PlanDriverStatus) {
	c.Printf("plan:    %s\n", st.PlanID)
	if !st.Enabled {
		c.Println("driver:  off")
		return
	}
	c.Printf("driver:  on (level=%s)\n", st.Level)
	c.Printf("budget:  %s\n", formatPlanDriverBudget(st.Budget))
	if st.EnabledAt > 0 {
		c.Printf("since:   %s\n", time.Unix(st.EnabledAt, 0).Format("2006-01-02 15:04:05"))
	}
}

// formatPlanDriverBudget renders a budget as `jobs 12 / rework 3 / hours 6.5`, naming the
// unlimited ceilings explicitly — "0" would read as "nothing allowed".
func formatPlanDriverBudget(b job.PlanDriverBudget) string {
	part := func(name string, v float64, unlimited bool) string {
		if unlimited {
			return name + " unlimited"
		}
		if v == float64(int64(v)) {
			return fmt.Sprintf("%s %d", name, int64(v))
		}
		return fmt.Sprintf("%s %g", name, v)
	}
	return part("jobs", float64(b.MaxJobs), b.MaxJobs == 0) + " / " +
		part("rework", float64(b.MaxRework), b.MaxRework == 0) + " / " +
		part("hours", b.MaxHours, b.MaxHours == 0)
}
