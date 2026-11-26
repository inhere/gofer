package commands

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/stage"
	"github.com/inhere/gofer/internal/tracker"
)

// `gofer stage` (plan 2026-11-26-flow-progression-plan.md, F2): one flow rides on a work
// item — design -> review -> plan -> doing -> verify -> done for `feature`, doing -> verify
// -> done for `small`. Only an explicit action moves it: `done` when the current step's
// gate passes, `back` / `blocked` with a reason, or a person's `approve`. The gates read
// stored facts and git; what they want is printed, never guessed.
//
// With no `--work`, every subcommand acts on the flow of the work item the current session
// is on (the same association the server uses for a session's flow), so an agent inside a
// session never has to look the id up.

type stageOptions struct {
	asJSON                      bool
	size, work, project, reason string
}

var stageOpts stageOptions

// stageGateExit is the exit code of `stage done` when the gate did not pass: the flow is
// fine, it is simply not there yet, so a script can branch on it without parsing text.
const stageGateExit = 2

// NewStageCmd builds the `stage` command group.
func NewStageCmd() *gcli.Command {
	bind := func(c *gcli.Command) {
		bindConfigFlag(c)
		bindServerFlags(c)
		c.BoolOpt(&stageOpts.asJSON, "json", "", false, "print JSON")
	}
	return &gcli.Command{
		Name: "stage",
		Desc: "Flow progression of a work item: design -> review -> plan -> doing -> verify -> done (start / status / done / back / blocked / approve / drop / level / report / ls)",
		Subs: []*gcli.Command{
			{
				Name: "start", Desc: "Start a flow on the work item of <issue> (the linked one, else a new one); this session becomes a current session of it",
				Config: func(c *gcli.Command) {
					bind(c)
					c.AddArg("issue", "tracker issue id the work is for", false)
					c.StrOpt(&stageOpts.size, "size", "", "", "template: feature (design->review->plan->doing->verify->done, default) | small (doing->verify->done)")
					c.StrOpt(&stageOpts.work, "work", "", "", "work item id (instead of / along with the issue)")
					c.StrOpt(&stageOpts.project, "project", "p", "", "project key (only when a new work item is created; default: the project of this directory)")
				},
				Func: runStageStart,
			},
			{
				Name: "status", Aliases: []string{"show"},
				Desc: "Show the flow: current step, the steps line and what the current gate is still waiting for",
				Config: func(c *gcli.Command) {
					bind(c)
					c.StrOpt(&stageOpts.work, "work", "", "", "work item id (default: the flow of this session's work item)")
				},
				Func: runStageStatus,
			},
			{
				Name: "done", Desc: "Evaluate the current step's gate and move on when it passes (exit code 2 when it does not; the gate's missing items are printed)",
				Config: func(c *gcli.Command) {
					bind(c)
					c.StrOpt(&stageOpts.work, "work", "", "", "work item id (default: the flow of this session's work item)")
				},
				Func: runStageDone,
			},
			{
				Name: "back", Desc: "Send the flow one step back (use when a review sends the work back)",
				Config: func(c *gcli.Command) {
					bind(c)
					c.StrOpt(&stageOpts.work, "work", "", "", "work item id (default: the flow of this session's work item)")
					c.StrOpt(&stageOpts.reason, "reason", "", "", "why it goes back (required; goes to the journal)")
				},
				Func: runStageBack,
			},
			{
				Name: "blocked", Desc: "Mark the flow blocked on its current step (a later `stage done` lifts it)",
				Config: func(c *gcli.Command) {
					bind(c)
					c.StrOpt(&stageOpts.work, "work", "", "", "work item id (default: the flow of this session's work item)")
					c.StrOpt(&stageOpts.reason, "reason", "", "", "what it is blocked on (required)")
				},
				Func: runStageBlocked,
			},
			{
				Name: "approve", Desc: "A person's approval of the current step: passes the review gate and waives a gate this server cannot judge (refused inside a session)",
				Config: func(c *gcli.Command) {
					bind(c)
					c.StrOpt(&stageOpts.work, "work", "", "", "work item id (default: the flow of this session's work item)")
				},
				Func: runStageApprove,
			},
			{
				Name: "drop", Desc: "Abandon the flow (the work item itself is untouched)",
				Config: func(c *gcli.Command) {
					bind(c)
					c.StrOpt(&stageOpts.work, "work", "", "", "work item id (default: the flow of this session's work item)")
					c.StrOpt(&stageOpts.reason, "reason", "", "", "why it is abandoned (optional)")
				},
				Func: runStageDrop,
			},
			{
				Name: "level", Desc: "Set how much the flow may do for itself: record (only note what it would do) | remind (also send the session a continue turn when the next step is the agent's); a person's command, refused inside a session",
				Config: func(c *gcli.Command) {
					bind(c)
					c.AddArg("level", "record | remind", true)
					c.StrOpt(&stageOpts.work, "work", "", "", "work item id (default: the flow of this session's work item)")
				},
				Func: runStageLevel,
			},
			{
				Name: "report", Desc: "How often what a level would have done matched what happened next (per action and per rule; newest 20 resolved)",
				Config: func(c *gcli.Command) {
					bind(c)
					c.StrOpt(&stageOpts.work, "work", "", "", "work item id (default: every flow of the project)")
					c.StrOpt(&stageOpts.project, "project", "p", "", "project key (default: the project of this directory)")
				},
				Func: runStageReport,
			},
			{
				Name: "ls", Aliases: []string{"list"},
				Desc: "List the flows in progress",
				Config: func(c *gcli.Command) {
					bind(c)
					c.StrOpt(&stageOpts.project, "project", "p", "", "only this project key")
				},
				Func: runStageList,
			},
		},
	}
}

func stageClient() (*client.Client, error) {
	return newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
}

// stageSessionID is the session this command runs in: the agent's own, resolved to its
// full id when the server knows it. "" outside a session (a person's shell).
func stageSessionID(cli *client.Client) string {
	sid, _ := client.SessionIDFromEnv(os.Getenv)
	if strings.TrimSpace(sid) == "" {
		return ""
	}
	if full, err := resolveSessionID(cli, sid); err == nil {
		return full
	}
	return sid
}

// stageTarget is the work item a subcommand acts on: --work when given, else the work item
// of this session that carries a flow in progress.
func stageTarget(cli *client.Client, verb string) (string, error) {
	if w := strings.TrimSpace(stageOpts.work); w != "" {
		return resolveWorkID(cli, w)
	}
	sid := stageSessionID(cli)
	if sid == "" {
		return "", fmt.Errorf("stage %s: no session here; name the work item with --work <id>", verb)
	}
	id, ok, err := stageSessionWorkItem(cli, sid)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("stage %s: this session's work item has no flow in progress; start one with `gofer stage start <issue>` (or name one with --work <id>)", verb)
	}
	return id, nil
}

// stageSessionWorkItem finds the item of a session that has a flow in progress. The flows
// in progress name the candidates; the session's own items say which of them is its.
func stageSessionWorkItem(cli *client.Client, sid string) (string, bool, error) {
	open, err := cli.ListOpenStages("")
	if err != nil {
		return "", false, err
	}
	if len(open) == 0 {
		return "", false, nil
	}
	has := make(map[string]bool, len(open))
	for _, v := range open {
		has[v.WorkItemID] = true
	}
	l, err := cli.ListWorkItems(client.WorkListOpts{Session: sid, Limit: 50})
	if err != nil {
		return "", false, err
	}
	for _, w := range l.Items {
		if has[w.ID] {
			return w.ID, true, nil
		}
	}
	return "", false, nil
}

// stageStepsLine draws the template's steps with the current one marked:
// `design ✓ → review ▶ → plan → doing → verify → done`.
func stageStepsLine(v stage.View) string {
	parts := make([]string, 0, len(v.Steps))
	for i, s := range v.Steps {
		switch {
		case v.Status == jobstore.StageDone:
			s += " ✓"
		case i < v.StepIndex:
			s += " ✓"
		case i == v.StepIndex && v.Status == jobstore.StageBlocked:
			s += " ⏸"
		case i == v.StepIndex:
			s += " ▶"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " → ")
}

// printStageGate prints what the current step still waits for, one item per line, and says
// who can act on it.
func printStageGate(c *gcli.Command, g stage.Gate) {
	if g.OK {
		c.Printf("gate %s: passed — `gofer stage done` moves on\n", g.Step)
		return
	}
	c.Printf("gate %s: not passed yet\n", g.Step)
	for _, m := range g.Missing {
		c.Printf("  - %s\n", m)
	}
	if g.NeedsHuman {
		c.Println("  needs a person: approve it in the web console or from outside the session (`gofer stage approve` is refused inside a session)")
	}
}

// printStage prints one flow: where it is, the steps line and the gate in front of it.
func printStage(c *gcli.Command, v stage.View) {
	c.Printf("stage %s  work item %s", v.Template, v.WorkItemID)
	if v.ProjectKey != "" {
		c.Printf("  project %s", v.ProjectKey)
	}
	c.Printf("  status %s\n", v.Status)
	c.Printf("  %s\n", stageStepsLine(v))
	if v.BlockedReason != "" {
		c.Printf("blocked: %s\n", v.BlockedReason)
	}
	if v.Gate != nil {
		printStageGate(c, *v.Gate)
	}
	switch v.Status {
	case jobstore.StageDone:
		c.Println("the flow is finished; the work item and the issue are untouched — close them yourself")
	case jobstore.StageDropped:
		c.Println("the flow was abandoned; the work item itself is untouched")
	}
}

func runStageStart(c *gcli.Command, _ []string) error {
	cli, err := stageClient()
	if err != nil {
		return err
	}
	issue := strings.TrimSpace(c.Arg("issue").String())
	work := strings.TrimSpace(stageOpts.work)
	if issue == "" && work == "" {
		return fmt.Errorf("stage start needs an issue id (or --work <work item id>)")
	}
	tpl := strings.ToLower(strings.TrimSpace(stageOpts.size))
	if tpl != "" {
		if _, ok := stage.TemplateByName(tpl); !ok {
			return fmt.Errorf("--size must be one of %s (got %q)", strings.Join(stage.TemplateNames(), "|"), stageOpts.size)
		}
	}
	if err := checkStageIssue(issue); err != nil {
		return err
	}
	sid := stageSessionID(cli)
	var v stage.View
	if work != "" {
		id, rerr := resolveWorkID(cli, work)
		if rerr != nil {
			return rerr
		}
		v, err = cli.StageStartOn(id, issue, tpl, sid)
	} else {
		project := strings.TrimSpace(stageOpts.project)
		if project == "" {
			project = currentProjectKey(cli)
		}
		v, err = cli.StageStartByIssue(issue, tpl, project, "", sid)
	}
	if err != nil {
		return err
	}
	if stageOpts.asJSON {
		return workPrintJSON(c, v)
	}
	printStage(c, v)
	if slices.Contains(v.Steps, stage.StepDesign) || slices.Contains(v.Steps, stage.StepPlan) {
		c.Println("link the documents the gates look for: `gofer work link <work item> --doc <repo-relative path> --role design|plan`")
	}
	return nil
}

// checkStageIssue refuses an issue id this repository's tracker does not have, so a typo does
// not start a flow on an issue nobody can find. Outside a tracked repository (or when the
// tracker cannot be read) it does not judge and lets the server take the id as given.
func checkStageIssue(issue string) error {
	if issue == "" {
		return nil
	}
	store, err := tracker.Discover(".", "")
	if err != nil {
		return nil
	}
	items, err := store.ReadIssues()
	if err != nil {
		return nil
	}
	for _, it := range items {
		if it.ID == issue {
			return nil
		}
	}
	return fmt.Errorf("issue %s is not in this repository's tracker (%s); check the id with `gofer issue show`", issue, store.Dir)
}

func runStageStatus(c *gcli.Command, _ []string) error {
	cli, err := stageClient()
	if err != nil {
		return err
	}
	id, err := stageTarget(cli, "status")
	if err != nil {
		return err
	}
	v, err := cli.GetStage(id)
	if err != nil {
		return err
	}
	if stageOpts.asJSON {
		return workPrintJSON(c, v)
	}
	printStage(c, v)
	return nil
}

func runStageDone(c *gcli.Command, _ []string) error {
	cli, err := stageClient()
	if err != nil {
		return err
	}
	id, err := stageTarget(cli, "done")
	if err != nil {
		return err
	}
	res, err := cli.StageDone(id)
	if err != nil {
		return err
	}
	if stageOpts.asJSON {
		return workPrintJSON(c, res)
	}
	if !res.Advanced && !res.Moved {
		printStage(c, res.Stage)
		if res.Stage.Gate == nil {
			printStageGate(c, res.Gate)
		}
		return errorx.Failf(stageGateExit, "stage done: the %s gate did not pass", res.Gate.Step)
	}
	printStage(c, res.Stage)
	if res.Moved {
		c.Printf("nothing was written: another call moved the flow first (now at %s)\n", res.Stage.Step)
		return nil
	}
	c.Printf("advanced: %s > %s\n", res.From, res.Stage.Step)
	return nil
}

func runStageBack(c *gcli.Command, _ []string) error {
	return runStageReason(c, "back")
}

func runStageBlocked(c *gcli.Command, _ []string) error {
	return runStageReason(c, "blocked")
}

// runStageReason runs `back` / `blocked`, both of which need a reason.
func runStageReason(c *gcli.Command, verb string) error {
	reason := strings.TrimSpace(stageOpts.reason)
	if reason == "" {
		return fmt.Errorf("stage %s needs --reason (it goes to the work item's journal)", verb)
	}
	cli, err := stageClient()
	if err != nil {
		return err
	}
	id, err := stageTarget(cli, verb)
	if err != nil {
		return err
	}
	var v stage.View
	if verb == "back" {
		v, err = cli.StageBack(id, reason)
	} else {
		v, err = cli.StageBlocked(id, reason)
	}
	if err != nil {
		return err
	}
	if stageOpts.asJSON {
		return workPrintJSON(c, v)
	}
	printStage(c, v)
	if verb == "blocked" {
		c.Println("blocked: a later `gofer stage done` lifts it on the same step")
	}
	return nil
}

func runStageApprove(c *gcli.Command, _ []string) error {
	cli, err := stageClient()
	if err != nil {
		return err
	}
	id, err := stageTarget(cli, "approve")
	if err != nil {
		return err
	}
	res, err := cli.StageApprove(id)
	if err != nil {
		return err
	}
	if stageOpts.asJSON {
		return workPrintJSON(c, res)
	}
	printStage(c, res.Stage)
	if res.Advanced {
		c.Printf("approved and advanced: %s > %s\n", res.From, res.Stage.Step)
	} else if res.Moved {
		c.Printf("nothing was written: another call moved the flow first (now at %s)\n", res.Stage.Step)
	} else {
		c.Println("approved; the step does not pass yet (see the items above)")
	}
	return nil
}

func runStageDrop(c *gcli.Command, _ []string) error {
	cli, err := stageClient()
	if err != nil {
		return err
	}
	id, err := stageTarget(cli, "drop")
	if err != nil {
		return err
	}
	v, err := cli.StageDrop(id, stageOpts.reason)
	if err != nil {
		return err
	}
	if stageOpts.asJSON {
		return workPrintJSON(c, v)
	}
	printStage(c, v)
	return nil
}

func runStageList(c *gcli.Command, _ []string) error {
	cli, err := stageClient()
	if err != nil {
		return err
	}
	list, err := cli.ListOpenStages(stageOpts.project)
	if err != nil {
		return err
	}
	if stageOpts.asJSON {
		return workPrintJSON(c, map[string]any{"stages": list})
	}
	if len(list) == 0 {
		c.Println("no flow in progress (start one with `gofer stage start <issue>`)")
		return nil
	}
	c.Printf("%-14s %-8s %-28s %s\n", "WORK ITEM", "TEMPLATE", "STEPS", "STATUS")
	for _, v := range list {
		status := v.Status
		if v.BlockedReason != "" {
			status += "  " + oneLine(v.BlockedReason, 60)
		}
		c.Printf("%-14s %-8s %-28s %s\n", v.WorkItemID, v.Template, stageStepsLine(v), status)
	}
	return nil
}
