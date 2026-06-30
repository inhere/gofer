package commands

import (
	"reflect"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/job"
)

func TestScheduleSubcommandsRegistered(t *testing.T) {
	app := NewApp("test")
	schCmd := app.GetCommand("schedule")
	if schCmd == nil {
		t.Fatal("schedule command not registered")
	}
	for _, sub := range []string{"add", "list", "show", "enable", "disable", "run", "rm"} {
		if schCmd.GetCommand(sub) == nil {
			t.Fatalf("schedule subcommand %q not registered", sub)
		}
	}
	if !app.IsAlias("sch") || app.ResolveAlias("sch") != "schedule" {
		t.Fatalf("`sch` alias should resolve to schedule, got %q", app.ResolveAlias("sch"))
	}
	if !schCmd.IsAlias("ls") || schCmd.ResolveAlias("ls") != "list" {
		t.Fatalf("`ls` should resolve to schedule list, got %q", schCmd.ResolveAlias("ls"))
	}
}

func TestScheduleAddBuildsJobRequestFromRunFlags(t *testing.T) {
	resetScheduleTestState()

	app := NewApp("test")
	addCmd := app.GetCommand("schedule").GetCommand("add")
	var got client.CreateScheduleRequest
	addCmd.Func = func(c *gcli.Command, _ []string) error {
		req, err := buildJobRunRequest(c, &client.Client{})
		if err != nil {
			return err
		}
		got = client.CreateScheduleRequest{
			Name:    scheduleOpts.name,
			Cron:    scheduleOpts.cron,
			Request: req,
			CatchUp: &scheduleOpts.catchUp,
		}
		return nil
	}

	args := []string{
		"schedule", "add", "--name", "nightly", "--cron", "*/5 * * * *",
		"-p", "self", "-a", "exec", "--runner", "local", "--cwd", ".",
		"--timeout", "30", "--title", "smoke", "--tags", "cron,smoke",
		"--", "go", "version",
	}
	if code := app.Run(args); code != 0 {
		t.Fatalf("app.Run exit code=%d for args %v", code, args)
	}
	if got.Name != "nightly" || got.Cron != "*/5 * * * *" {
		t.Fatalf("schedule fields mismatch: %+v", got)
	}
	if got.CatchUp == nil || !*got.CatchUp {
		t.Fatalf("catch_up default should be true, got %+v", got.CatchUp)
	}
	wantReq := job.JobRequest{
		ProjectKey: "self",
		Agent:      "exec",
		Runner:     "local",
		Cmd:        []string{"go", "version"},
		Cwd:        ".",
		TimeoutSec: 30,
		Title:      "smoke",
		Tags:       []string{"cron", "smoke"},
		Channel:    "cli",
		Client:     got.Request.Client,
	}
	if !reflect.DeepEqual(got.Request, wantReq) {
		t.Fatalf("JobRequest mismatch:\n got=%+v\nwant=%+v", got.Request, wantReq)
	}
	if got.Request.Client == "" {
		t.Fatal("JobRequest.Client should be stamped from cliHostname")
	}
}

func TestScheduleAddPromptAndRoleFlags(t *testing.T) {
	resetScheduleTestState()

	app := NewApp("test")
	addCmd := app.GetCommand("schedule").GetCommand("add")
	var got job.JobRequest
	addCmd.Func = func(c *gcli.Command, _ []string) error {
		req, err := buildJobRunRequest(c, &client.Client{})
		if err != nil {
			return err
		}
		got = req
		return nil
	}

	args := []string{
		"sch", "add", "--name", "review", "--cron", "0 9 * * *",
		"--role", "reviewer", "--prompt", "check this", "--system-prompt", "be strict",
	}
	if code := app.Run(args); code != 0 {
		t.Fatalf("app.Run exit code=%d for args %v", code, args)
	}
	if got.Role != "reviewer" || got.Prompt != "check this" || got.SystemPrompt != "be strict" {
		t.Fatalf("role/prompt flags not mapped: %+v", got)
	}
	if got.ProjectKey != "" || got.Agent != "" {
		t.Fatalf("role path should not require project/agent client-side, got %+v", got)
	}
}

func resetScheduleTestState() {
	scheduleOpts.name, scheduleOpts.cron, scheduleOpts.project = "", "", ""
	scheduleOpts.catchUp = false
	jobRunOpts.project, jobRunOpts.agent, jobRunOpts.runner = "", "", ""
	jobRunOpts.cwd, jobRunOpts.prompt, jobRunOpts.title, jobRunOpts.tags = "", "", "", ""
	jobRunOpts.role, jobRunOpts.systemPrompt = "", ""
	jobRunOpts.timeout = 0
	jobRunOpts.channel = "cli"
}
