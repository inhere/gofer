package commands

import (
	"os"

	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/brief"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/tracker"
)

var briefOpts struct {
	maxLines int
	asJSON   bool
}

// briefOptions binds the brief inputs: the repository tracker (may be nil), the server
// (probed once; unreachable → nil client plus the reason, so the server sections say
// why they are empty) and the project key of the cwd.
func briefOptions(s *tracker.Store) brief.Options {
	opts := brief.Options{Store: s, MaxLines: briefOpts.maxLines}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		opts.ClientNote = err.Error()
		return opts
	}
	bc, note := brief.Connect(cli)
	if bc == nil {
		opts.ClientNote = note
		return opts
	}
	opts.Client = bc
	root, _ := os.Getwd()
	if s != nil && s.RepoRoot() != "" {
		root = s.RepoRoot()
	}
	if _, key, err := primeServerTarget(s, config.InputCfgFile, root); err == nil {
		opts.ProjectKey = serverProjectKey(cli, key, root)
	}
	return opts
}

func printBrief(c *gcli.Command, b brief.Brief) error {
	if briefOpts.asJSON {
		return printTrackerJSON(c, b)
	}
	c.Print(b.Text())
	return nil
}

func bindBriefFlags(c *gcli.Command) {
	bindConfigFlag(c)
	bindServerFlags(c)
	c.IntOpt(&briefOpts.maxLines, "max-lines", "", brief.DefaultMaxLines, "cap the output; sections are cut in order, each says what was cut and how to see it")
	c.BoolOpt(&briefOpts.asJSON, "json", "", false, "print JSON (sections with lines / note)")
}

// newIssueBriefCmd is `gofer issue brief <id>`.
func newIssueBriefCmd(trackerPath *string) *gcli.Command {
	return &gcli.Command{Name: "brief", Desc: "Handoff brief of an issue: the issue, its tree, design docs, related commits / code entries, jobs / plans, applicable memories and takeover hints", Config: func(c *gcli.Command) {
		bindBriefFlags(c)
		c.StrOpt(trackerPath, "tracker", "", "", "explicit .gofer/tracker directory")
		c.AddArg("id", "issue id", true)
	}, Func: func(c *gcli.Command, _ []string) error {
		s, err := tracker.Discover(".", *trackerPath)
		if err != nil {
			return err
		}
		b, err := brief.IssueBrief(c.Arg("id").String(), briefOptions(s))
		if err != nil {
			return err
		}
		return printBrief(c, b)
	}}
}

// newPlanBriefCmd is `gofer plan brief <plan-id>`.
func newPlanBriefCmd() *gcli.Command {
	return &gcli.Command{Name: "brief", Desc: "Handoff brief of a plan: fields, todos (status / deps / acceptance / job outcome), handoff note and the issues it names", Config: func(c *gcli.Command) {
		bindBriefFlags(c)
		c.AddArg("plan-id", "plan id", true)
	}, Func: func(c *gcli.Command, _ []string) error {
		s, _ := tracker.Discover(".", "") // optional: resolves the issues the todos name
		b, err := brief.PlanBrief(c.Arg("plan-id").String(), briefOptions(s))
		if err != nil {
			return err
		}
		return printBrief(c, b)
	}}
}
