package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/hookrelay"
	"github.com/inhere/gofer/internal/tracker"
)

func NewRepoCmd() *gcli.Command {
	var prefix, initTracker, statusTracker string
	var noAgents, noHooks, asJSON bool
	return &gcli.Command{
		Name: "repo", Desc: "Manage this repository's local tracker",
		Subs: []*gcli.Command{
			{
				Name: "prime", Desc: "Print repository tracker context",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.BoolOpt(&asJSON, "hook-json", "", false, "Claude SessionStart JSON output")
				},
				Func: func(c *gcli.Command, _ []string) error {
					s, err := tracker.Discover(".", "")
					if err != nil {
						if asJSON {
							return printTrackerJSON(c, map[string]any{"hookSpecificOutput": map[string]string{"additionalContext": ""}})
						}
						return err
					}
					body, err := s.Prime()
					if err != nil {
						return err
					}
					if asJSON {
						return printTrackerJSON(c, map[string]any{"hookSpecificOutput": map[string]string{"additionalContext": body}})
					}
					c.Print(body)
					return nil
				},
			},
			{
				Name: "init", Desc: "Initialize local issue and memory files",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.StrOpt(&initTracker, "tracker", "", "", "explicit .gofer/tracker directory")
					c.StrOpt(&prefix, "prefix", "", "", "issue id prefix (default: repository directory name)")
					c.BoolOpt(&noAgents, "no-agents-md", "", false, "do not write a managed agent instruction block")
					c.BoolOpt(&noHooks, "no-hooks", "", false, "reserved for P3; hooks are not installed in P2")
				},
				Func: func(c *gcli.Command, _ []string) error {
					root, err := os.Getwd()
					if err != nil {
						return err
					}
					if initTracker != "" {
						if filepath.Base(initTracker) != "tracker" || filepath.Base(filepath.Dir(initTracker)) != ".gofer" {
							return fmt.Errorf("--tracker must name a .gofer/tracker directory")
						}
						root = filepath.Dir(filepath.Dir(initTracker))
					}
					s, beads, err := tracker.Init(root, prefix, noAgents)
					if err != nil {
						return err
					}
					c.Printf("tracker initialized: %s\n", s.Dir)
					if beads {
						c.Println("BEADS integration remains; use gofer repo migrate --from-bd (P3)")
					}
					if !noHooks {
						for _, agent := range []string{hookrelay.AgentClaude, hookrelay.AgentCodex} {
							if _, err := hookrelay.InstallTrackerPrime(agent, root, false); err != nil {
								return err
							}
						}
					}
					return nil
				},
			},
			{
				Name: "status", Desc: "Inspect local tracker status",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.StrOpt(&statusTracker, "tracker", "", "", "explicit .gofer/tracker directory")
					c.BoolOpt(&asJSON, "json", "", false, "print JSON")
				},
				Func: func(c *gcli.Command, _ []string) error {
					s, err := tracker.Discover(".", statusTracker)
					if err != nil {
						return err
					}
					status, err := s.Status()
					if err != nil {
						return err
					}
					root := filepath.Dir(filepath.Dir(s.Dir))
					status.Hooks = fmt.Sprintf("claude=%v codex=%v", hookrelay.HasTrackerPrime(hookrelay.AgentClaude, root), hookrelay.HasTrackerPrime(hookrelay.AgentCodex, root))
					if asJSON {
						return printTrackerJSON(c, status)
					}
					c.Printf("tracker: %s\n", status.Tracker)
					for _, name := range []string{"open", "in_progress", "blocked", "closed"} {
						c.Printf("issues.%s: %d\n", name, status.Issues[name])
					}
					c.Printf("memories: %d\ncommit_policy: %s\nmanaged_block: %v\nhooks: %s\nsync: %s\n", status.Memories, status.CommitPolicy, status.ManagedBlock, status.Hooks, status.Sync)
					return nil
				},
			},
		},
	}
}

func printTrackerJSON(c *gcli.Command, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.Println(string(b))
	return nil
}
