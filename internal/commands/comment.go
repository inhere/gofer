package commands

import (
	"fmt"
	"strings"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// The comment commands (MCP-05 阶段 A) live in one file rather than next to the
// subcommand literals in job.go / plan.go, because the four of them are one feature
// across two command groups and share their printing: the job and plan GROUPS stay
// where they are, only the runners are here.
//
// Identity follows the MCP tool exactly: when the caller runs inside a job
// (GOFER_JOB_ID set), the comment is sent as THAT job's agent — recorded, and never
// dispatching — while an interactive terminal posts as the authenticated user, which is
// the case that starts work.

// runJobComment implements `gofer job comment <job> <text>`.
func runJobComment(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("job comment requires a <job-id>")
	}
	text := argValue(c, "text")
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("job comment requires a comment body")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	cm, err := cli.PostComment("job", id, text)
	if err != nil {
		return err
	}
	printComment(c, cm)
	return nil
}

// runJobComments implements `gofer job comments <job>`.
func runJobComments(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("job comments requires a <job-id>")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	list, err := cli.ListComments("job", id)
	if err != nil {
		return err
	}
	printComments(c, list)
	return nil
}

// runPlanComment implements `gofer plan comment <plan> <text>` (and, with --todo, the
// checklist item's own thread).
func runPlanComment(c *gcli.Command, _ []string) error {
	planID := argValue(c, "plan-id")
	if planID == "" {
		return fmt.Errorf("plan comment requires a <plan-id>")
	}
	text := argValue(c, "text")
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("plan comment requires a comment body")
	}
	scope, scopeID := "plan", planID
	if todo := strings.TrimSpace(planCommentOpts.todo); todo != "" {
		scope, scopeID = "todo", todo
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	cm, err := cli.PostComment(scope, scopeID, text)
	if err != nil {
		return err
	}
	printComment(c, cm)
	return nil
}

// runPlanComments implements `gofer plan comments <plan>`.
func runPlanComments(c *gcli.Command, _ []string) error {
	planID := argValue(c, "plan-id")
	if planID == "" {
		return fmt.Errorf("plan comments requires a <plan-id>")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	list, err := cli.ListComments("plan", planID)
	if err != nil {
		return err
	}
	printComments(c, list)
	return nil
}

// planCommentOpts is the `plan comment` flag surface. A package-level value (gcli does
// not reset flags between Run calls, so tests clear it), matching the other commands.
var planCommentOpts struct {
	// todo redirects the comment to that checklist item's own thread ("" = the plan's).
	todo string
}

// printComment prints one stored comment and, under it, every job its mentions started
// — the "→ dispatched job <id>" line is the receipt that a comment spent money.
func printComment(c *gcli.Command, cm client.Comment) {
	c.Printf("comment %s on %s %s by %s/%s\n", cm.ID, cm.Scope, cm.ScopeID, cm.Author, cm.AuthorKind)
	if len(cm.Mentions) > 0 {
		c.Printf("mentions:    %s\n", strings.Join(prependAt(cm.Mentions), " "))
	}
	for _, d := range cm.Dispatched {
		c.Printf("→ dispatched job %s (@%s, %s)\n", d.JobID, d.Mention, d.Kind)
	}
	if len(cm.Dispatched) == 0 && cm.TriggeredJobID == "" {
		c.Printf("(no job dispatched)\n")
	}
}

// printComments prints a whole thread, oldest first.
func printComments(c *gcli.Command, list []client.Comment) {
	if len(list) == 0 {
		c.Printf("(no comments)\n")
		return
	}
	for _, cm := range list {
		c.Printf("%s  %s  %s/%s\n", cm.ID, formatUnix(cm.CreatedAt), cm.Author, cm.AuthorKind)
		for _, line := range strings.Split(strings.TrimRight(cm.Body, "\n"), "\n") {
			c.Printf("    %s\n", line)
		}
		if cm.TriggeredJobID != "" {
			c.Printf("    → dispatched job %s\n", cm.TriggeredJobID)
		}
	}
}

// prependAt renders a mention list the way it was written (@omp @reviewer).
func prependAt(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, "@"+n)
	}
	return out
}

// formatUnix renders a unix-seconds timestamp the way the other list commands do.
func formatUnix(sec int64) string {
	if sec <= 0 {
		return "-"
	}
	return fmtServerTime(sec)
}
