package commands

import (
	"fmt"
	"strings"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/stage"
)

func runStageLevel(c *gcli.Command, _ []string) error {
	level := strings.ToLower(strings.TrimSpace(c.Arg("level").String()))
	if level != "record" && level != "remind" {
		return fmt.Errorf("stage level must be record or remind (got %q)", level)
	}
	cli, err := stageClient()
	if err != nil {
		return err
	}
	id, err := stageTarget(cli, "level")
	if err != nil {
		return err
	}
	v, err := cli.StageSetLevel(id, level)
	if err != nil {
		return err
	}
	if stageOpts.asJSON {
		return workPrintJSON(c, v)
	}
	c.Printf("work item %s: level %s\n", v.WorkItemID, v.Level)
	if v.Level == "remind" {
		c.Println("remind: when the next step is the agent's, its current session is sent a continue turn (offline: queued on its seat, else only noted in the journal)")
	} else {
		c.Println("record: step changes only note what would be done; nothing is sent")
	}
	return nil
}

func runStageReport(c *gcli.Command, _ []string) error {
	cli, err := stageClient()
	if err != nil {
		return err
	}
	work := strings.TrimSpace(stageOpts.work)
	project := strings.TrimSpace(stageOpts.project)
	if work != "" {
		if work, err = resolveWorkID(cli, work); err != nil {
			return err
		}
	} else if project == "" {
		project = currentProjectKey(cli)
	}
	rep, err := cli.StageReport(work, project)
	if err != nil {
		return err
	}
	if stageOpts.asJSON {
		return workPrintJSON(c, rep)
	}
	printStageReport(c, rep)
	return nil
}

func ratePct(r float64) string { return fmt.Sprintf("%.0f%%", r*100) }

func printStageReport(c *gcli.Command, rep stage.Report) {
	scope := "all flows"
	switch {
	case rep.WorkItemID != "":
		scope = "work item " + rep.WorkItemID
	case rep.ProjectKey != "":
		scope = "project " + rep.ProjectKey
	}
	c.Printf("stage report (%s): %d recorded, %d not observed yet (window %dh)\n", scope, rep.Total, rep.Pending, rep.WindowHours)
	if rep.Total == 0 {
		c.Println("nothing recorded yet: a step change writes what the level would do")
		return
	}
	c.Printf("recent %d resolved: %d match (%s)\n", rep.RecentN, rep.RecentMatch, ratePct(rep.RecentRate))
	c.Printf("%-18s %-28s %5s %5s %8s %11s %7s %6s\n", "ACTION", "RULE", "TOTAL", "MATCH", "MISMATCH", "NO_FOLLOWUP", "PENDING", "RATE")
	rows := map[string][]stage.ReportRow{}
	for _, r := range rep.ByRule {
		rows[r.Action] = append(rows[r.Action], r)
	}
	for _, a := range rep.ByAction {
		c.Printf("%-18s %-28s %5d %5d %8d %11d %7d %6s\n", a.Action, "(all)", a.Total, a.Match, a.Mismatch, a.NoFollowup, a.Pending, ratePct(a.Rate))
		for _, r := range rows[a.Action] {
			c.Printf("%-18s %-28s %5d %5d %8d %11d %7d %6s\n", "", r.Rule, r.Total, r.Match, r.Mismatch, r.NoFollowup, r.Pending, ratePct(r.Rate))
		}
	}
	for _, a := range rep.Recent {
		obs := a.Observed
		if obs == "" {
			obs = "-"
		}
		c.Printf("  %-12s %-16s %-8s %-12s %s\n", a.WorkItemID, a.Action, a.Step, a.Outcome, obs)
	}
}
