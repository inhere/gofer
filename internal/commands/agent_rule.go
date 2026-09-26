package commands

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/rule"
)

// agentRuleCaller is the `updated_by` stamp a LOCAL write records. The server stamps
// its own caller id for HTTP calls; a direct local run has no caller identity, so it
// names the surface that did the write.
const agentRuleCaller = "cli"

// agentRuleOpts holds the flags shared by the `agent rule` subcommands. The sub-group
// lives under `agent` (design §一.1: no new top-level command, G033) — a rule is what
// an agent must obey, so it is found where the agents are.
var agentRuleOpts = struct {
	local bool
	file  string
}{}

// newAgentRuleCmd builds the `agent rule` group: ls/show/set/rm over the JOB-06①
// library (`<config-dir>/rules/<name>.md` + its index).
//
// Dual mode, exactly like `agent skill`: a client node — or any box with no local
// server config of its own, such as the worker/container this sub-group is used from —
// goes over HTTP to the server that owns the library, so one box can manage a fleet's
// rules; --local opens the library here instead.
func newAgentRuleCmd() *gcli.Command {
	bindRuleConn := func(c *gcli.Command) {
		bindConfigFlag(c)
		bindServerFlags(c)
		c.BoolOpt(&agentRuleOpts.local, "local", "", false, "operate on the LOCAL rule library even in client mode")
	}
	return &gcli.Command{
		Name: "rule",
		Desc: "Manage the server-side MANDATORY rule library (list/show/set/rm)",
		Subs: []*gcli.Command{
			{
				Name:    "list",
				Desc:    "List rules with their size, sha256 prefix and description",
				Aliases: []string{"ls"},
				Config:  bindRuleConn,
				Func:    runAgentRuleList,
			},
			{
				Name: "show",
				Desc: "Show one rule: metadata and its text",
				Config: func(c *gcli.Command) {
					bindRuleConn(c)
					c.AddArg("name", "rule name", true)
				},
				Func: runAgentRuleShow,
			},
			{
				Name: "set",
				Desc: "Create or replace a rule from a markdown file",
				Config: func(c *gcli.Command) {
					bindRuleConn(c)
					c.AddArg("name", "rule name (lower-case letters, digits and '-')", true)
					c.StrOpt(&agentRuleOpts.file, "file", "f", "", "read the rule text from this file ('-' = stdin)")
				},
				Func: runAgentRuleSet,
			},
			{
				Name:    "rm",
				Desc:    "Delete a rule from the library",
				Aliases: []string{"remove"},
				Config: func(c *gcli.Command) {
					bindRuleConn(c)
					c.AddArg("name", "rule name", true)
				},
				Func: runAgentRuleRemove,
			},
		},
	}
}

// agentRuleRemote resolves where the rule commands read from, with the shared
// dual-mode rule (useServerAPI): a client node, or any node with no local server
// config, goes over HTTP; --local forces the local copy.
func agentRuleRemote() serverAPIChoice { return useServerAPI(agentRuleOpts.local) }

// openLocalRuleStore opens the library the way the server does: the resolved config,
// its metadata db, and <config-dir>/rules. The returned func closes the db and must
// always be called.
//
// A missing config REFUSES rather than degrading to a fresh empty library — same
// reasoning as openLocalSkillStore: without a config there is no config dir and no
// index, so silently creating one would answer "no rules" to an operator who is
// actually pointed at the wrong box.
func openLocalRuleStore() (*rule.Store, func(), error) {
	cfg, path, err := config.Load(config.InputCfgFile)
	if err != nil {
		return nil, nil, err
	}
	if path == "" {
		return nil, nil, fmt.Errorf(
			"no local gofer config found: --local reads the rule library beside the server's config, " +
				"and this box has none; pass -c/--config to point at one, or drop --local to use the " +
				"server's library over HTTP")
	}
	repo, err := jobstore.Open(cfg.ResolveDBPath())
	if err != nil {
		return nil, nil, err
	}
	closeStore := func() { _ = repo.Close() }
	dir, err := config.ConfigDir()
	if err != nil {
		closeStore()
		return nil, nil, fmt.Errorf("resolve config dir: %w", err)
	}
	st, err := rule.NewStore(filepath.Join(dir, "rules"), repo)
	if err != nil {
		closeStore()
		return nil, nil, err
	}
	return st, closeStore, nil
}

// runAgentRuleList lists the library's entries, one line each.
func runAgentRuleList(c *gcli.Command, _ []string) error {
	var list []rule.Rule
	if ch := agentRuleRemote(); ch.remote {
		cli, err := ch.client()
		if err != nil {
			return err
		}
		if list, err = cli.RuleList(); err != nil {
			return ch.wrap(err)
		}
	} else {
		st, closeStore, err := openLocalRuleStore()
		if err != nil {
			return err
		}
		defer closeStore()
		if list, err = st.List(); err != nil {
			return err
		}
	}
	if len(list) == 0 {
		c.Println("(no rules)")
		return nil
	}
	for _, r := range list {
		c.Printf("%-24s %-9s %-12s %s\n", r.Name, humanBytes(r.Size), shortSkillVersion(r.SHA256), orDash(r.Description))
	}
	return nil
}

// runAgentRuleShow prints one rule: metadata plus the stored text.
func runAgentRuleShow(c *gcli.Command, args []string) error {
	name := toolArg(c, args, 0, "name")
	if name == "" {
		return fmt.Errorf("agent rule show requires a <name> argument")
	}
	var view client.RuleView
	if ch := agentRuleRemote(); ch.remote {
		cli, err := ch.client()
		if err != nil {
			return err
		}
		if view, err = cli.RuleShow(name); err != nil {
			return ch.wrap(err)
		}
	} else {
		st, closeStore, err := openLocalRuleStore()
		if err != nil {
			return err
		}
		defer closeStore()
		r, ok, err := st.Get(name)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: rule %q", rule.ErrNotFound, name)
		}
		content, err := st.Read(name)
		if err != nil {
			return err
		}
		view = client.RuleView{Rule: r, Content: content}
	}
	printRuleView(c, view)
	return nil
}

// printRuleView renders `rule show`.
func printRuleView(c *gcli.Command, v client.RuleView) {
	c.Printf("name:          %s\n", v.Name)
	c.Printf("description:   %s\n", orDash(v.Description))
	c.Printf("size:          %s\n", humanBytes(v.Size))
	c.Printf("sha256:        %s\n", orDash(v.SHA256))
	c.Printf("updated_at:    %s\n", fmtServerTime(v.UpdatedAt))
	c.Printf("updated_by:    %s\n", orDash(v.UpdatedBy))
	c.Println("--- content ---")
	if strings.TrimSpace(v.Content) == "" {
		c.Println("(no content)")
		return
	}
	c.Println(strings.TrimRight(v.Content, "\n"))
}

// runAgentRuleSet creates or replaces one rule from -f/--file ('-' reads stdin).
func runAgentRuleSet(c *gcli.Command, args []string) error {
	name := toolArg(c, args, 0, "name")
	if name == "" {
		return fmt.Errorf("agent rule set requires a <name> argument")
	}
	src := strings.TrimSpace(agentRuleOpts.file)
	if src == "" {
		return fmt.Errorf("agent rule set requires -f/--file (the rule text; '-' reads stdin)")
	}
	var (
		content []byte
		err     error
	)
	if src == "-" {
		content, err = io.ReadAll(os.Stdin)
	} else {
		content, err = os.ReadFile(src)
	}
	if err != nil {
		return fmt.Errorf("read rule source %s: %w", src, err)
	}
	if len(strings.TrimSpace(string(content))) == 0 {
		return fmt.Errorf("rule source %s is empty", src)
	}

	if ch := agentRuleRemote(); ch.remote {
		cli, cerr := ch.client()
		if cerr != nil {
			return cerr
		}
		r, serr := cli.RuleSet(name, content)
		if serr != nil {
			return ch.wrap(serr)
		}
		c.Printf("wrote rule %s (%s, sha256 %s)\n", r.Name, humanBytes(r.Size), shortSkillVersion(r.SHA256))
		return nil
	}
	st, closeStore, err := openLocalRuleStore()
	if err != nil {
		return err
	}
	defer closeStore()
	r, err := st.Set(name, content, agentRuleCaller)
	if err != nil {
		return err
	}
	c.Printf("wrote rule %s (%s, sha256 %s)\n", r.Name, humanBytes(r.Size), shortSkillVersion(r.SHA256))
	return nil
}

// runAgentRuleRemove deletes a rule and its file.
func runAgentRuleRemove(c *gcli.Command, args []string) error {
	name := toolArg(c, args, 0, "name")
	if name == "" {
		return fmt.Errorf("agent rule rm requires a <name> argument")
	}
	if ch := agentRuleRemote(); ch.remote {
		cli, err := ch.client()
		if err != nil {
			return err
		}
		if err := cli.RuleRemove(name); err != nil {
			return ch.wrap(err)
		}
		c.Printf("removed rule %s\n", name)
		return nil
	}
	st, closeStore, err := openLocalRuleStore()
	if err != nil {
		return err
	}
	defer closeStore()
	if err := st.Remove(name); err != nil {
		return err
	}
	c.Printf("removed rule %s\n", name)
	return nil
}
