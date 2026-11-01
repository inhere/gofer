package commands

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/memorysynthesis"
)

// memorySynthesisFlags holds the flags of `memory synthesis ls | show | run`.
type memorySynthesisFlags struct {
	project string
	day     string
	state   string
	limit   int
	force   bool
	asJSON  bool
}

var memorySynthesisOpts memorySynthesisFlags

// withMemorySynthesisCmds appends the `memory synthesis` group. The runs live in the
// server's store, so every subcommand talks to the server.
func withMemorySynthesisCmds(cmd *gcli.Command) *gcli.Command {
	o := &memorySynthesisOpts
	bind := func(c *gcli.Command) {
		bindConfigFlag(c)
		bindServerFlags(c)
		c.BoolOpt(&o.asJSON, "json", "", false, "print JSON")
	}
	cmd.Subs = append(cmd.Subs, &gcli.Command{Name: "synthesis", Desc: cliText("cli.memory.synthesis.desc"), Subs: []*gcli.Command{
		{Name: "ls", Desc: cliText("cli.memory.synthesis.ls.desc"), Config: func(c *gcli.Command) {
			bind(c)
			c.StrOpt(&o.project, "project", "p", "", "only this project's runs")
			c.StrOpt(&o.day, "day", "", "", "only this day (YYYY-MM-DD)")
			c.StrOpt(&o.state, "state", "", "", "running | succeeded | failed | skipped | all")
			c.IntOpt(&o.limit, "limit", "n", 0, "max rows (default 100)")
		}, Func: runMemorySynthesisLs},
		{Name: "run", Desc: cliText("cli.memory.synthesis.run.desc"), Config: func(c *gcli.Command) {
			bind(c)
			c.StrOpt(&o.project, "project", "p", "${GOFER_PROJECT}", "project to synthesize (default: $GOFER_PROJECT)")
			c.StrOpt(&o.day, "day", "", "", "business day YYYY-MM-DD in the server time zone (default: today)")
			c.BoolOpt(&o.force, "force", "", false, "run the model again even when this day's input is unchanged")
		}, Func: runMemorySynthesisRun},
		{Name: "show", Desc: cliText("cli.memory.synthesis.show.desc"), Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("id", "synthesis run id (from `memory synthesis ls`)", true)
		}, Func: runMemorySynthesisShow},
	}})
	return cmd
}

func runMemorySynthesisLs(c *gcli.Command, _ []string) error {
	o := &memorySynthesisOpts
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	runs, err := cli.ListMemorySyntheses(client.MemorySynthesisListOpts{Project: o.project, Day: o.day, State: o.state, Limit: o.limit})
	if err != nil {
		return fmt.Errorf("list memory syntheses: %w", err)
	}
	if o.asJSON {
		return printTrackerJSON(c, runs)
	}
	if len(runs) == 0 {
		c.Println(cliText("cli.memory.synthesis.empty"))
		return nil
	}
	for _, r := range runs {
		c.Println(memorySynthesisLine(r))
	}
	return nil
}

func runMemorySynthesisRun(c *gcli.Command, _ []string) error {
	o := &memorySynthesisOpts
	project := strings.TrimSpace(o.project)
	if project == "" {
		return fmt.Errorf("%s", cliText("cli.memory.synthesis.run.no_project"))
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	res, err := cli.RunMemorySynthesis(client.MemorySynthesisRunOpts{Project: project, Day: o.day, Force: o.force})
	if err != nil {
		return fmt.Errorf("run memory synthesis: %w", err)
	}
	if o.asJSON {
		return printTrackerJSON(c, res)
	}
	c.Println(cliText("cli.memory.synthesis.run.outcome."+res.Outcome, "id", res.Run.ID, "day", res.Run.Day, "project", project, "error", res.Run.Error))
	if res.Run.ResultJSON != "" {
		c.Println(memorySynthesisResultText(res.Run.ResultJSON))
	}
	return nil
}

// memorySynthesisResultText renders the stored result for a person: the summary, then the
// highlights with their source refs and the open questions. Anything that does not parse is
// printed as stored.
func memorySynthesisResultText(raw string) string {
	var r memorysynthesis.Result
	if json.Unmarshal([]byte(raw), &r) != nil || r.Summary == "" {
		return raw
	}
	var b strings.Builder
	b.WriteString(r.Summary)
	for _, h := range r.Highlights {
		b.WriteString("\n- " + h.Text)
		if len(h.SourceRefs) > 0 {
			b.WriteString(" [" + strings.Join(h.SourceRefs, ", ") + "]")
		}
	}
	for _, q := range r.OpenQuestions {
		b.WriteString("\n? " + q)
	}
	return b.String()
}

func runMemorySynthesisShow(c *gcli.Command, _ []string) error {
	o := &memorySynthesisOpts
	id := strings.TrimSpace(c.Arg("id").String())
	if id == "" {
		return fmt.Errorf("synthesis run id is required")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	r, err := cli.GetMemorySynthesis(id)
	if err != nil {
		return fmt.Errorf("get memory synthesis: %w", err)
	}
	if o.asJSON {
		return printTrackerJSON(c, r)
	}
	c.Print(memorySynthesisDetail(r))
	return nil
}

func shortDigest(d string) string {
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

func unixText(sec int64) string {
	if sec <= 0 {
		return "-"
	}
	return time.Unix(sec, 0).Format("2006-01-02 15:04:05")
}

// memorySynthesisLine is one list row.
func memorySynthesisLine(r jobstore.MemorySynthesisRun) string {
	return cliText("cli.memory.synthesis.line", "id", r.ID, "day", r.Day, "project", r.ProjectKey, "status", r.Status,
		"attempt", r.Attempt, "digest", shortDigest(r.InputDigest), "checked", unixText(r.LastCheckedAt))
}

// memorySynthesisDetail renders one run; the result is printed as stored and the material
// snapshot only by presence (it is available with --json for a person with access).
func memorySynthesisDetail(r jobstore.MemorySynthesisRun) string {
	var b strings.Builder
	row := func(label, value string) {
		if strings.TrimSpace(value) != "" {
			fmt.Fprintf(&b, "%s: %s\n", label, value)
		}
	}
	row("id", r.ID)
	row("project", r.ProjectKey)
	row("day", r.Day)
	row("status", r.Status)
	row("attempt", fmt.Sprint(r.Attempt))
	row("input_digest", r.InputDigest)
	row("schema", r.MaterialSchema)
	row("materials", r.MaterialCountsJSON)
	row("omitted", r.OmittedJSON)
	row("agent", r.Agent)
	row("model", r.Model)
	row("started", unixText(r.StartedAt))
	row("finished", unixText(r.FinishedAt))
	row("last_checked", unixText(r.LastCheckedAt))
	row("error", r.Error)
	row("result", r.ResultJSON)
	if r.MaterialSnapshot != "" {
		b.WriteString(cliText("cli.memory.synthesis.snapshot_kept") + "\n")
	}
	return b.String()
}
