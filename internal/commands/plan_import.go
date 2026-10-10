package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// planImportFlags holds `plan import` flags (gofer-3nxa.5).
type planImportFlags struct {
	fromJob string
	file    string
	assign  string
	dryRun  bool
}

var planImportOpts planImportFlags

// planImportReportBytes is how much of a planner job's report is read: a plan is longer
// than a review summary, so the client's largest default window.
const planImportReportBytes = 256 * 1024

func planImportCmd() *gcli.Command {
	return &gcli.Command{
		Name: "import",
		Desc: "Create a todo chain from a plan's ```gofer-todos``` block (a planner job's report or a file): one item per step chained by `after`, each `check` an exec item gating the next steps",
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.AddArg("plan-id", "plan id", true)
			c.StrOpt(&planImportOpts.fromJob, "from-job", "", "", "read the block from this job's report (stdout)")
			c.StrOpt(&planImportOpts.file, "file", "f", "", "read the block from this file")
			c.StrOpt(&planImportOpts.assign, "assign", "", "", "agent key the steps are assigned to (checks are always exec items)")
			c.BoolOpt(&planImportOpts.dryRun, "dry-run", "", false, "print the todos that would be created, create nothing")
		},
		Func: runPlanImport,
	}
}

func runPlanImport(c *gcli.Command, _ []string) error {
	o := planImportOpts
	planID := argValue(c, "plan-id")
	if planID == "" {
		return fmt.Errorf("plan import requires <plan-id>")
	}
	if (o.fromJob == "") == (o.file == "") {
		return fmt.Errorf("give exactly one of --from-job <job-id> or -f <file>")
	}
	connect := func() (*client.Client, error) {
		return newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	}
	var text string
	if o.file != "" {
		b, err := os.ReadFile(o.file)
		if err != nil {
			return err
		}
		text = string(b)
	} else {
		cli, err := connect()
		if err != nil {
			return err
		}
		if text, err = cli.GetLogsTail(o.fromJob, "stdout", planImportReportBytes); err != nil {
			return fmt.Errorf("read the report of job %s: %w", o.fromJob, err)
		}
	}
	specs, err := job.ParsePlanTodos(text)
	if err != nil {
		return err
	}
	items, err := job.BuildTodoImport(specs, o.assign)
	if err != nil {
		return err
	}
	argv := make([][]string, len(items))
	for i, it := range items {
		if it.Cmd == "" {
			continue
		}
		words, err := splitShellWords(it.Cmd)
		if err != nil || len(words) == 0 {
			return fmt.Errorf("step %d check %q: not a command line: %v", it.Step, it.Cmd, err)
		}
		argv[i] = words
	}
	if o.dryRun {
		c.Printf("plan %s: %d todo(s) from %d step(s) (dry run, nothing created)\n", planID, len(items), len(specs))
		for i, it := range items {
			printImportTodo(c, fmt.Sprintf("#%d", i+1), it, func(dep int) string { return fmt.Sprintf("#%d", dep+1) })
		}
		return nil
	}
	cli, err := connect()
	if err != nil {
		return err
	}
	ids := make([]string, len(items))
	for i, it := range items {
		patch := jobstore.TodoPatch{}
		if it.Assignee != "" {
			assignee := it.Assignee
			patch.Assignee = &assignee
		}
		if it.Acceptance != "" {
			acceptance := it.Acceptance
			patch.Acceptance = &acceptance
		}
		if len(it.Scope) > 0 {
			scope := it.Scope
			patch.Scope = &scope
		}
		if argv[i] != nil {
			cmd := argv[i]
			patch.Cmd = &cmd
		}
		if len(it.After) > 0 {
			after := make([]string, 0, len(it.After))
			for _, dep := range it.After {
				after = append(after, ids[dep])
			}
			patch.After = &after
		}
		t, err := cli.AddTodo(planID, it.Title, "", "", patch)
		if err != nil {
			return fmt.Errorf("create %q (%d of %d created before it): %w", it.Title, i, len(items), err)
		}
		ids[i] = t.TodoID
		printImportTodo(c, t.TodoID, it, func(dep int) string { return ids[dep] })
	}
	c.Printf("%d todo(s) added to plan %s; start the chain with `gofer plan run %s`\n", len(items), planID, planID)
	return nil
}

// printImportTodo prints one created (or would-be) todo: its handle, title, assignee,
// dependencies and the fields it carries.
func printImportTodo(c *gcli.Command, handle string, it job.ImportTodo, dep func(int) string) {
	line := handle + " " + it.Title
	if it.Assignee != "" {
		line += "  [" + it.Assignee + "]"
	}
	if len(it.After) > 0 {
		deps := make([]string, 0, len(it.After))
		for _, d := range it.After {
			deps = append(deps, dep(d))
		}
		line += "  after " + strings.Join(deps, ",")
	}
	c.Println(line)
	if it.Cmd != "" {
		c.Printf("    cmd: %s\n", it.Cmd)
	}
	if len(it.Scope) > 0 {
		c.Printf("    scope: %s\n", strings.Join(it.Scope, ", "))
	}
	if it.Acceptance != "" {
		c.Printf("    acceptance: %s\n", strings.ReplaceAll(it.Acceptance, "\n", " / "))
	}
}
