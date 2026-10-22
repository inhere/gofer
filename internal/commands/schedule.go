package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	yaml "github.com/goccy/go-yaml"
	"github.com/gookit/cliui/show/table"
	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

var scheduleOpts = struct {
	name    string
	cron    string
	delay   string
	at      string
	catchUp bool
	project string
	webhook bool
	// eventsLimit / eventsBefore page `schedule events` (newest first, like a plan's stream).
	eventsLimit  int
	eventsBefore int64
	// paramDefs / paramsFile declare a schedule's parameters (design 2026-10-22 §D6) on
	// add / edit; paramBindings are the run-now overrides (`schedule run --param K=V`).
	paramDefs     gcli.Strings
	paramsFile    string
	paramBindings gcli.Strings
	// editCron / editAt / editTitle / editTimeout / editEnable patch an existing schedule.
	editCron    string
	editAt      string
	editTitle   string
	editTimeout string
	editEnable  bool
	editDisable bool
}{}

// NewScheduleCmd builds the `schedule` command group. It talks to the running
// serve HTTP API and reuses job run request flags for the scheduled JobRequest.
func NewScheduleCmd() *gcli.Command {
	return &gcli.Command{
		Name:    "schedule",
		Aliases: []string{"sch"},
		Desc:    "Manage schedules via the bridge server",
		Subs: []*gcli.Command{
			{
				Name: "add",
				Desc: "Create a schedule from a job request",
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
				Desc:    "List schedules",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&scheduleOpts.project, "project", "p", "", "filter by project key")
				},
				Func: runScheduleList,
			},
			{
				Name: "show",
				Desc: "Show a schedule",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("id", "schedule id", true)
				},
				Func: runScheduleShow,
			},
			{
				Name: "events",
				Desc: "List a schedule's own events (schedule.run_failed…), newest first",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.IntOpt(&scheduleOpts.eventsLimit, "limit", "", 50, "max events (default 50, server caps at 200)")
					c.Int64Opt(&scheduleOpts.eventsBefore, "before", "", 0, "only events older than this seq")
					c.AddArg("id", "schedule id", true)
				},
				Func: runScheduleEvents,
			},
			{
				Name: "enable",
				Desc: "Enable a schedule",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("id", "schedule id", true)
				},
				Func: func(c *gcli.Command, args []string) error { return runScheduleSetEnabled(c, true) },
			},
			{
				Name: "disable",
				Desc: "Disable a schedule",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("id", "schedule id", true)
				},
				Func: func(c *gcli.Command, args []string) error { return runScheduleSetEnabled(c, false) },
			},
			{
				Name: "edit",
				Desc: "Edit a schedule in place: cron/at, title, params, enabled, timeout (the command, agent, runner and project cannot change)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					bindScheduleParamFlags(c)
					c.StrOpt(&scheduleOpts.editCron, "cron", "", "", "new cron expression (recomputes the next run)")
					c.StrOpt(&scheduleOpts.editAt, "at", "", "", "new one-shot run time (RFC3339 or unix seconds)")
					c.StrOpt(&scheduleOpts.editTitle, "title", "", "", "new title")
					c.StrOpt(&scheduleOpts.editTimeout, "timeout", "", "", "new job timeout in seconds (0 = server default)")
					c.BoolOpt(&scheduleOpts.editEnable, "enable", "", false, "turn the schedule on")
					c.BoolOpt(&scheduleOpts.editDisable, "disable", "", false, "turn the schedule off")
					c.AddArg("id", "schedule id", true)
				},
				Func: runScheduleEdit,
			},
			{
				Name: "run",
				Desc: "Run a schedule now",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.VarOpt(&scheduleOpts.paramBindings, "param", "", "override a declared param for this run, NAME=VALUE (repeatable)")
					c.AddArg("id", "schedule id", true)
				},
				Func: runScheduleRun,
			},
			{
				Name: "rotate-token",
				Desc: "Mint a fresh webhook trigger token for a schedule (also enables the webhook on one created without --webhook)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("id", "schedule id", true)
				},
				Func: runScheduleRotateToken,
			},
			{
				Name: "rm",
				Desc: "Delete a schedule",
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
	c.StrOpt(&scheduleOpts.cron, "cron", "", "", "standard 5-field cron expression")
	c.StrOpt(&scheduleOpts.delay, "delay", "", "", "create a one-shot schedule after a duration, e.g. 30s/5m")
	c.StrOpt(&scheduleOpts.at, "at", "", "", "create a one-shot schedule at RFC3339 or unix seconds")
	c.BoolOpt(&scheduleOpts.catchUp, "catch-up", "", true, "run once after a missed tick within the server grace window")
	c.BoolOpt(&scheduleOpts.webhook, "webhook", "", false, "enable the external trigger endpoint: the server mints a trigger_token for POST /v1/schedules/{id}/trigger")
	c.StrOpt(&jobRunOpts.project, "project", "p", "", "project key (required)")
	c.StrOpt(&jobRunOpts.agent, "agent", "a", "", "agent key (required)")
	c.StrOpt(&jobRunOpts.runner, "runner", "", config.BuiltinLocalRunner, "runner key (server and local are the same built-in runner)")
	c.StrOpt(&jobRunOpts.cwd, "cwd", "", ".", "working dir within the project")
	c.StrOpt(&jobRunOpts.prompt, "prompt", "", "", "prompt text for cli-agent (use -- <argv...> for exec)")
	c.IntOpt(&jobRunOpts.timeout, "timeout", "", 0, "job timeout in seconds (0 = server default)")
	c.StrOpt(&jobRunOpts.title, "title", "", "", "optional job title")
	c.StrOpt(&jobRunOpts.tags, "tags", "", "", "comma-separated free-form tags for the job")
	c.StrOpt(&jobRunOpts.plan, "plan", "", "", "attach the job to a plan (grouping key)")
	c.StrOpt(&jobRunOpts.role, "role", "", "", "role preset: fills agent/system_prompt/project/tags when unset")
	c.StrOpt(&jobRunOpts.systemPrompt, "system-prompt", "", "", "resident system prompt injected via the agent")
	bindScheduleParamFlags(c)
	c.AddArg("cmd", "raw command for exec agent (after --)", false, true)
}

// bindScheduleParamFlags binds the parameter declarations shared by `schedule add` and
// `schedule edit` (design 2026-10-22 §D6): --param-def for short declarations and
// --params-file when a choice param's value list needs YAML.
func bindScheduleParamFlags(c *gcli.Command) {
	c.VarOpt(&scheduleOpts.paramDefs, "param-def", "", "declare a param as NAME[:type]=default[#desc] (repeatable; type: string|int|bool)")
	c.StrOpt(&scheduleOpts.paramsFile, "params-file", "", "", "YAML file with the full param declaration list (name/type/default/desc/choices)")
}

func runScheduleAdd(c *gcli.Command, _ []string) error {
	if strings.TrimSpace(scheduleOpts.name) == "" {
		return fmt.Errorf("--name is required")
	}
	autoDetectJobProject(c)
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	req, err := buildScheduleCreateRequest(c, cli)
	if err != nil {
		return err
	}
	out, err := cli.CreateSchedule(req)
	if err != nil {
		return err
	}
	c.Printf("schedule %s created: name=%s type=%s cron=%q next_run=%s enabled=%s\n",
		out.ID, out.Name, scheduleTypeText(out.Type), out.Cron, formatScheduleTime(out.NextRunAt), enabledText(out.Enabled))
	if out.TriggerToken != "" {
		c.Printf("webhook token: %s\n  trigger with: curl -X POST '<server>/v1/schedules/%s/trigger?token=%s'\n",
			out.TriggerToken, out.ID, out.TriggerToken)
	}
	return nil
}

func buildScheduleCreateRequest(c *gcli.Command, cli *client.Client) (client.CreateScheduleRequest, error) {
	cronExpr := strings.TrimSpace(scheduleOpts.cron)
	delayRaw := strings.TrimSpace(scheduleOpts.delay)
	atRaw := strings.TrimSpace(scheduleOpts.at)
	provided := 0
	for _, v := range []string{cronExpr, delayRaw, atRaw} {
		if v != "" {
			provided++
		}
	}
	if provided == 0 {
		return client.CreateScheduleRequest{}, fmt.Errorf("one of --cron, --delay, or --at is required")
	}
	if provided > 1 {
		return client.CreateScheduleRequest{}, fmt.Errorf("--cron, --delay, and --at are mutually exclusive")
	}

	req, err := buildJobRunRequest(c, cli)
	if err != nil {
		return client.CreateScheduleRequest{}, err
	}
	out := client.CreateScheduleRequest{
		Name:    strings.TrimSpace(scheduleOpts.name),
		Cron:    cronExpr,
		Request: req,
		CatchUp: &scheduleOpts.catchUp,
		Webhook: scheduleOpts.webhook,
	}
	params, err := scheduleParamsFromFlags()
	if err != nil {
		return client.CreateScheduleRequest{}, err
	}
	out.Params = params
	if delayRaw != "" {
		d, err := time.ParseDuration(delayRaw)
		if err != nil {
			return client.CreateScheduleRequest{}, fmt.Errorf("parse --delay: %w", err)
		}
		if d <= 0 {
			return client.CreateScheduleRequest{}, fmt.Errorf("--delay must be > 0")
		}
		out.Type = "once"
		out.Cron = ""
		out.DelaySec = int64(d / time.Second)
		if out.DelaySec <= 0 {
			return client.CreateScheduleRequest{}, fmt.Errorf("--delay must be at least 1s")
		}
	}
	if atRaw != "" {
		runAt, err := parseScheduleAt(atRaw)
		if err != nil {
			return client.CreateScheduleRequest{}, err
		}
		out.Type = "once"
		out.Cron = ""
		out.RunAt = runAt
	}
	return out, nil
}

func parseScheduleAt(raw string) (int64, error) {
	if sec, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return sec, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return 0, fmt.Errorf("parse --at as unix seconds or RFC3339: %w", err)
	}
	return t.Unix(), nil
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
	tb.SetHeads("ID", "NAME", "TYPE", "CRON", "NEXT_RUN", "ENABLED", "LAST_JOB")
	for _, s := range list {
		tb.AddRow(s.ID, s.Name, scheduleTypeText(s.Type), emptyDash(s.Cron), formatScheduleListTime(s.NextRunAt), enabledText(s.Enabled), emptyDash(s.LastJobID))
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
	c.Printf("type:        %s\n", scheduleTypeText(s.Type))
	c.Printf("cron:        %s\n", s.Cron)
	c.Printf("project:     %s\n", s.ProjectKey)
	c.Printf("enabled:     %s\n", enabledText(s.Enabled))
	c.Printf("catch_up:    %s\n", enabledText(s.CatchUp))
	c.Printf("next_run:    %s\n", formatScheduleTime(s.NextRunAt))
	c.Printf("last_run:    %s\n", formatScheduleTime(s.LastRunAt))
	c.Printf("last_job:    %s\n", emptyDash(s.LastJobID))
	c.Printf("webhook:     %s\n", webhookText(s))
	if len(s.Params) == 0 {
		c.Println("params:      (none declared)")
	} else {
		c.Println("params:")
		for _, p := range s.Params {
			c.Printf("  %-24s %-6s default=%q", p.Name, p.TypeOrString(), p.Default)
			if len(p.Choices) > 0 {
				c.Printf(" choices=%q", p.Choices)
			}
			if p.Desc != "" {
				c.Printf("  # %s", p.Desc)
			}
			c.Println("")
		}
	}
	c.Println("request:")
	body, err := json.MarshalIndent(s.Request, "  ", "  ")
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	c.Println("  " + strings.ReplaceAll(string(body), "\n", "\n  "))
	return nil
}

// runScheduleEvents prints a schedule's own event stream (GET /v1/schedules/{id}/events): the
// scheduler's refusals (schedule.run_failed, e.g. a ceiling that no longer admits the run) live
// here rather than on a job that was never created.
func runScheduleEvents(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("schedule events requires an <id> argument")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	events, err := cli.ListScheduleEvents(id, scheduleOpts.eventsLimit, scheduleOpts.eventsBefore)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		c.Printf("schedule %s has no events\n", id)
		return nil
	}
	tb := table.New("", table.WithColMaxWidth(60))
	tb.SetHeads("SEQ", "AT", "TYPE", "DETAIL")
	for _, ev := range events {
		tb.AddRow(strconv.FormatInt(ev.Seq, 10), formatScheduleTime(ev.At), ev.Type, emptyDash(ev.Detail))
	}
	c.Print(tb.Render())
	return nil
}

// runScheduleRotateToken mints a fresh webhook secret (AUTO-02b). The token is printed
// once here — that is the point of the command — and stays readable on `schedule show`
// for an authenticated caller.
func runScheduleRotateToken(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("schedule rotate-token requires an <id> argument")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	s, err := cli.RotateScheduleToken(id)
	if err != nil {
		return err
	}
	c.Printf("schedule %s webhook token rotated\n", s.ID)
	c.Printf("webhook token: %s\n", s.TriggerToken)
	return nil
}

// webhookText renders a schedule's webhook state: the token when enabled, an explicit
// "disabled" otherwise (an empty line would read as "not shown").
func webhookText(s client.Schedule) string {
	if s.TriggerToken == "" {
		return "disabled (enable with --webhook or `schedule rotate-token`)"
	}
	return s.TriggerToken + "  (POST /v1/schedules/" + s.ID + "/trigger)"
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
	params, err := job.ParseParamBindings(scheduleOpts.paramBindings)
	if err != nil {
		return err
	}
	res, err := cli.RunSchedule(id, params)
	if err != nil {
		return err
	}
	c.Printf("schedule %s submitted job %s: status=%s\n", id, res.ID, res.Status)
	return nil
}

// runScheduleEdit patches a schedule in place (design 2026-10-22 §D4/D6): only the
// schedule's own settings — the command / agent / runner / project stay put, since
// changing them is a different task (delete and recreate).
func runScheduleEdit(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("schedule edit requires an <id> argument")
	}
	var req client.PatchScheduleRequest
	touched := false
	if v := strings.TrimSpace(scheduleOpts.editCron); v != "" {
		req.Cron = &v
		touched = true
	}
	if v := strings.TrimSpace(scheduleOpts.editAt); v != "" {
		runAt, err := parseScheduleAt(v)
		if err != nil {
			return err
		}
		req.At = &runAt
		touched = true
	}
	if req.Cron != nil && req.At != nil {
		return fmt.Errorf("--cron and --at are mutually exclusive")
	}
	if v := strings.TrimSpace(scheduleOpts.editTitle); v != "" {
		req.Title = &v
		touched = true
	}
	if v := strings.TrimSpace(scheduleOpts.editTimeout); v != "" {
		sec, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("parse --timeout: %w", err)
		}
		req.TimeoutSec = &sec
		touched = true
	}
	if scheduleOpts.editEnable && scheduleOpts.editDisable {
		return fmt.Errorf("--enable and --disable are mutually exclusive")
	}
	if scheduleOpts.editEnable || scheduleOpts.editDisable {
		enabled := scheduleOpts.editEnable
		req.Enabled = &enabled
		touched = true
	}
	if len(scheduleOpts.paramDefs) > 0 || strings.TrimSpace(scheduleOpts.paramsFile) != "" {
		params, err := scheduleParamsFromFlags()
		if err != nil {
			return err
		}
		req.Params = &params
		touched = true
	}
	if !touched {
		return fmt.Errorf("nothing to change: give at least one of --cron, --at, --title, --param-def, --params-file, --timeout, --enable, --disable")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	s, err := cli.PatchSchedule(id, req)
	if err != nil {
		return err
	}
	c.Printf("schedule %s updated: name=%s type=%s cron=%q next_run=%s enabled=%s params=%d\n",
		s.ID, s.Name, scheduleTypeText(s.Type), s.Cron, formatScheduleTime(s.NextRunAt), enabledText(s.Enabled), len(s.Params))
	return nil
}

// scheduleParamsFromFlags builds the declaration list from --param-def and --params-file
// (design §D6). The file carries the whole list (choice params live there); the two are
// mutually exclusive so there is exactly one source of truth per invocation.
func scheduleParamsFromFlags() ([]job.ScheduleParam, error) {
	file := strings.TrimSpace(scheduleOpts.paramsFile)
	if file == "" {
		return job.ParamDefsToParams(scheduleOpts.paramDefs)
	}
	if len(scheduleOpts.paramDefs) > 0 {
		return nil, fmt.Errorf("--param-def and --params-file are mutually exclusive")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read --params-file: %w", err)
	}
	var params []job.ScheduleParam
	if err := yaml.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("parse --params-file %s: %w", file, err)
	}
	if err := job.ValidateScheduleParams(params); err != nil {
		return nil, err
	}
	return params, nil
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

// formatScheduleListTime renders a schedule's next/last run: the time of day when it
// is within a day, the full stamp otherwise — both in the SERVER's zone (bd
// h-aii-tnua), which is the clock the schedule actually fires on.
func formatScheduleListTime(sec int64) string {
	if sec <= 0 {
		return "-"
	}
	t := time.Unix(sec, 0)
	if time.Since(t) < 24*time.Hour && time.Until(t) < 24*time.Hour {
		return fmtServerClock(sec)
	}
	return fmtServerTime(sec)
}

func formatScheduleTime(sec int64) string {
	return fmtServerTime(sec)
}

func enabledText(v int) string {
	if v != 0 {
		return "true"
	}
	return "false"
}

func scheduleTypeText(v string) string {
	if v == "" {
		return "cron"
	}
	return v
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
