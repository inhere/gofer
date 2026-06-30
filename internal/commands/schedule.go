package commands

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gookit/cliui/show/table"
	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

var scheduleOpts = struct {
	name    string
	cron    string
	catchUp bool
	project string
}{}

// NewScheduleCmd builds the `schedule` command group. It talks to the running
// serve HTTP API and reuses job run request flags for the scheduled JobRequest.
func NewScheduleCmd() *gcli.Command {
	return &gcli.Command{
		Name:    "schedule",
		Aliases: []string{"sch"},
		Desc:    "Manage cron schedules via the bridge server",
		Subs: []*gcli.Command{
			{
				Name: "add",
				Desc: "Create a cron schedule from a job request",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					bindScheduleAddFlags(c)
				},
				Func: runScheduleAdd,
			},
			{
				Name:    "list",
				Aliases: []string{"ls"},
				Desc:    "List cron schedules",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&scheduleOpts.project, "project", "p", "", "filter by project key")
				},
				Func: runScheduleList,
			},
			{
				Name: "show",
				Desc: "Show a cron schedule",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("id", "schedule id", true)
				},
				Func: runScheduleShow,
			},
			{
				Name: "enable",
				Desc: "Enable a cron schedule",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("id", "schedule id", true)
				},
				Func: func(c *gcli.Command, args []string) error { return runScheduleSetEnabled(c, true) },
			},
			{
				Name: "disable",
				Desc: "Disable a cron schedule",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("id", "schedule id", true)
				},
				Func: func(c *gcli.Command, args []string) error { return runScheduleSetEnabled(c, false) },
			},
			{
				Name: "run",
				Desc: "Run a cron schedule now",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("id", "schedule id", true)
				},
				Func: runScheduleRun,
			},
			{
				Name: "rm",
				Desc: "Delete a cron schedule",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("id", "schedule id", true)
				},
				Func: runScheduleDelete,
			},
		},
	}
}

func bindScheduleAddFlags(c *gcli.Command) {
	c.StrOpt(&scheduleOpts.name, "name", "", "", "schedule name (required)")
	c.StrOpt(&scheduleOpts.cron, "cron", "", "", "standard 5-field cron expression (required)")
	c.BoolOpt(&scheduleOpts.catchUp, "catch-up", "", true, "run once after a missed tick within the server grace window")
	c.StrOpt(&jobRunOpts.project, "project", "p", "", "project key (required)")
	c.StrOpt(&jobRunOpts.agent, "agent", "a", "", "agent key (required)")
	c.StrOpt(&jobRunOpts.runner, "runner", "", "local", "runner key")
	c.StrOpt(&jobRunOpts.cwd, "cwd", "", ".", "working dir within the project")
	c.StrOpt(&jobRunOpts.prompt, "prompt", "", "", "prompt text for cli-agent (use -- <argv...> for exec)")
	c.IntOpt(&jobRunOpts.timeout, "timeout", "", 0, "job timeout in seconds (0 = server default)")
	c.StrOpt(&jobRunOpts.title, "title", "", "", "optional job title")
	c.StrOpt(&jobRunOpts.tags, "tags", "", "", "comma-separated free-form tags for the job")
	c.StrOpt(&jobRunOpts.role, "role", "", "", "role preset: fills agent/system_prompt/project/tags when unset")
	c.StrOpt(&jobRunOpts.systemPrompt, "system-prompt", "", "", "resident system prompt injected via the agent")
	c.AddArg("cmd", "raw command for exec agent (after --)", false, true)
}

func runScheduleAdd(c *gcli.Command, _ []string) error {
	if strings.TrimSpace(scheduleOpts.name) == "" {
		return fmt.Errorf("--name is required")
	}
	if strings.TrimSpace(scheduleOpts.cron) == "" {
		return fmt.Errorf("--cron is required")
	}
	autoDetectJobProject(c)
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	req, err := buildJobRunRequest(c, cli)
	if err != nil {
		return err
	}
	out, err := cli.CreateSchedule(client.CreateScheduleRequest{
		Name:    scheduleOpts.name,
		Cron:    scheduleOpts.cron,
		Request: req,
		CatchUp: &scheduleOpts.catchUp,
	})
	if err != nil {
		return err
	}
	c.Printf("schedule %s created: name=%s cron=%q next_run=%s enabled=%s\n",
		out.ID, out.Name, out.Cron, formatScheduleTime(out.NextRunAt), enabledText(out.Enabled))
	return nil
}

func runScheduleList(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	list, err := cli.ListSchedules(scheduleOpts.project)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		c.Println("no schedules matched the given filter")
		return nil
	}
	tb := table.New("", table.WithColMaxWidth(30))
	tb.SetHeads("ID", "NAME", "CRON", "NEXT_RUN", "ENABLED", "LAST_JOB")
	for _, s := range list {
		tb.AddRow(s.ID, s.Name, s.Cron, formatScheduleListTime(s.NextRunAt), enabledText(s.Enabled), emptyDash(s.LastJobID))
	}
	c.Print(tb.Render())
	return nil
}

func runScheduleShow(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("schedule show requires an <id> argument")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	s, err := cli.GetSchedule(id)
	if err != nil {
		return err
	}
	c.Printf("id:          %s\n", s.ID)
	c.Printf("name:        %s\n", s.Name)
	c.Printf("cron:        %s\n", s.Cron)
	c.Printf("project:     %s\n", s.ProjectKey)
	c.Printf("enabled:     %s\n", enabledText(s.Enabled))
	c.Printf("catch_up:    %s\n", enabledText(s.CatchUp))
	c.Printf("next_run:    %s\n", formatScheduleTime(s.NextRunAt))
	c.Printf("last_run:    %s\n", formatScheduleTime(s.LastRunAt))
	c.Printf("last_job:    %s\n", emptyDash(s.LastJobID))
	c.Println("request:")
	body, err := json.MarshalIndent(s.Request, "  ", "  ")
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	c.Println("  " + strings.ReplaceAll(string(body), "\n", "\n  "))
	return nil
}

func runScheduleSetEnabled(c *gcli.Command, enabled bool) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("schedule enable/disable requires an <id> argument")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	s, err := cli.SetScheduleEnabled(id, enabled)
	if err != nil {
		return err
	}
	c.Printf("schedule %s enabled=%s\n", s.ID, enabledText(s.Enabled))
	return nil
}

func runScheduleRun(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("schedule run requires an <id> argument")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	res, err := cli.RunSchedule(id)
	if err != nil {
		return err
	}
	c.Printf("schedule %s submitted job %s: status=%s\n", id, res.ID, res.Status)
	return nil
}

func runScheduleDelete(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("schedule rm requires an <id> argument")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	if err := cli.DeleteSchedule(id); err != nil {
		return err
	}
	c.Printf("schedule %s deleted\n", id)
	return nil
}

func formatScheduleListTime(sec int64) string {
	if sec <= 0 {
		return "-"
	}
	t := time.Unix(sec, 0)
	if time.Since(t) < 24*time.Hour && time.Until(t) < 24*time.Hour {
		return t.Format("15:04:05")
	}
	return t.Format("2006-01-02 15:04")
}

func formatScheduleTime(sec int64) string {
	if sec <= 0 {
		return "-"
	}
	return time.Unix(sec, 0).Format("2006-01-02 15:04:05")
}

func enabledText(v int) string {
	if v != 0 {
		return "true"
	}
	return "false"
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
