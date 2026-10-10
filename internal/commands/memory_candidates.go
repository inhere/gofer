package commands

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// memoryCandidateFlags holds the flags of `memory candidates | accept | reject`
// (gofer-3nxa.2).
type memoryCandidateFlags struct {
	project string
	job     string
	all     bool
	asJSON  bool
	key     string
	kind    string
	summary string
	global  bool
}

var memoryCandidateOpts memoryCandidateFlags

// withMemoryCandidateCmds appends the knowledge-candidate subcommands to the `memory`
// group. They always talk to the server: candidates live in its store, and accepting
// one writes a server-scoped memory.
func withMemoryCandidateCmds(cmd *gcli.Command) *gcli.Command {
	o := &memoryCandidateOpts
	bind := func(c *gcli.Command) {
		bindConfigFlag(c)
		bindServerFlags(c)
		c.BoolOpt(&o.asJSON, "json", "", false, "print JSON")
	}
	cmd.Subs = append(cmd.Subs,
		&gcli.Command{Name: "candidates", Desc: "List knowledge candidates: the 「## 可复用经验」 items of delivered jobs, waiting for accept / reject", Config: func(c *gcli.Command) {
			bind(c)
			c.StrOpt(&o.project, "project", "p", "", "only this project's candidates")
			c.StrOpt(&o.job, "job", "", "", "only this job's candidates")
			c.BoolOpt(&o.all, "all", "", false, "include accepted and rejected candidates")
		}, Func: runMemoryCandidates},
		&gcli.Command{Name: "accept", Desc: "Accept a knowledge candidate: write it as a server memory (default scope: the job's project; source job:<id>)", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("id", "candidate id (from `memory candidates`)", true)
			c.StrOpt(&o.key, "key", "", "", "memory key (required)")
			c.StrOpt(&o.kind, "kind", "", "note", "rule | note")
			c.StrOpt(&o.summary, "summary", "", "", "one sentence (<= 80 chars); required for content > 200 chars")
			c.BoolOpt(&o.global, "global", "", false, "write to the global scope instead of the job's project")
			c.StrOpt(&o.project, "project", "", "", "write to this project's scope instead of the job's project")
		}, Func: runMemoryAccept},
		&gcli.Command{Name: "reject", Desc: "Reject a knowledge candidate (nothing is written)", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("id", "candidate id (from `memory candidates`)", true)
		}, Func: runMemoryReject},
	)
	return cmd
}

func memoryCandidateID(c *gcli.Command) (int64, error) {
	raw := strings.TrimPrefix(strings.TrimSpace(c.Arg("id").String()), "#")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid candidate id %q", c.Arg("id").String())
	}
	return id, nil
}

func runMemoryCandidates(c *gcli.Command, _ []string) error {
	o := &memoryCandidateOpts
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	status := ""
	if o.all {
		status = "all"
	}
	items, err := cli.ListMemoryCandidates(client.MemoryCandidateListOpts{JobID: o.job, Project: o.project, Status: status})
	if err != nil {
		return fmt.Errorf("list memory candidates: %w", err)
	}
	if o.asJSON {
		return printTrackerJSON(c, items)
	}
	if len(items) == 0 {
		c.Println("no knowledge candidates")
		return nil
	}
	for _, m := range items {
		c.Println(memoryCandidateLine(m, time.Now()))
	}
	if !o.all {
		c.Println("accept: gofer memory accept <id> --key <k> [--kind rule|note]   reject: gofer memory reject <id>")
	}
	return nil
}

// memoryCandidateLine is one list row: id, status, project, job, age, text (and the
// memory key once accepted).
func memoryCandidateLine(m jobstore.MemoryCandidate, now time.Time) string {
	age := now.Sub(time.Unix(m.CreatedAt, 0)).Round(time.Hour)
	line := fmt.Sprintf("#%d [%s] %s %s (%s ago) %s", m.ID, m.Status, m.ProjectKey, m.JobID, age, m.Text)
	if m.MemoryKey != "" {
		line += " -> " + m.MemoryKey
	}
	return line
}

func runMemoryAccept(c *gcli.Command, _ []string) error {
	o := &memoryCandidateOpts
	id, err := memoryCandidateID(c)
	if err != nil {
		return err
	}
	if strings.TrimSpace(o.key) == "" {
		return fmt.Errorf("--key is required")
	}
	if o.global && o.project != "" {
		return fmt.Errorf("--global and --project are mutually exclusive")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	out, err := cli.AcceptMemoryCandidate(id, job.AcceptMemoryCandidateInput{Key: o.key, Kind: o.kind, Summary: o.summary, Global: o.global, Project: o.project})
	if err != nil {
		return fmt.Errorf("accept candidate #%d: %w", id, err)
	}
	if o.asJSON {
		return printTrackerJSON(c, out)
	}
	if out.Memory != nil {
		where := out.Memory.Scope
		if out.Memory.ScopeKey != "" {
			where += ":" + out.Memory.ScopeKey
		}
		c.Printf("candidate #%d accepted -> memory %s (%s, %s)\n", id, out.Memory.Key, where, out.Memory.Kind)
	}
	return nil
}

func runMemoryReject(c *gcli.Command, _ []string) error {
	id, err := memoryCandidateID(c)
	if err != nil {
		return err
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	cand, err := cli.RejectMemoryCandidate(id)
	if err != nil {
		return fmt.Errorf("reject candidate #%d: %w", id, err)
	}
	if memoryCandidateOpts.asJSON {
		return printTrackerJSON(c, cand)
	}
	c.Printf("candidate #%d rejected\n", id)
	return nil
}
