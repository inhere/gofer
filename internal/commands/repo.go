package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/bdmigrate"
	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/hookrelay"
	"github.com/inhere/gofer/internal/tracker"
)

func NewRepoCmd() *gcli.Command {
	var prefix, initTracker, statusTracker, syncServer, primeAgent string
	var syncTimeout time.Duration
	var noAgents, noHooks, asJSON, fromBD, applyMigration, forceMigration bool
	return &gcli.Command{
		Name: "repo", Desc: "Manage this repository's local tracker",
		Subs: []*gcli.Command{
			{
				Name: "sync", Desc: "Synchronize the local tracker with a server mirror",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.StrOpt(&syncServer, "server", "", "", "tracker mirror URL override")
					c.DurationOpt(&syncTimeout, "timeout", "", time.Minute, "request timeout for this manual sync (automatic sync after write commands keeps its short timeout)")
				},
				Func: func(c *gcli.Command, _ []string) error {
					s, err := tracker.Discover(".", "")
					if err != nil {
						return err
					}
					cli, err := newClient(config.InputCfgFile, syncServerOrEnv(syncServer), os.Getenv("GOFER_SERVER_TOKEN"))
					if err != nil {
						return err
					}
					ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
					defer cancel()
					if _, err := tracker.SyncHTTPWithToken(ctx, s, cli.BaseURL(), cli.Token()); err != nil {
						return err
					}
					cfg, _ := s.ReadConfig()
					c.Printf("sync: tracker_id=%s server=%s\n", cfg.TrackerID, cli.BaseURL())
					return nil
				},
			},
			{
				Name: "migrate", Desc: "Migrate a bd (beads) repository to the gofer tracker (dry-run unless --apply)",
				Help: "Reads the live bd database (`bd export`, read-only; .beads/issues.jsonl only as a fallback) and\n" +
					"imports issues + memories into .gofer/tracker, then swaps the bd integration points: the BEADS\n" +
					"blocks in AGENTS.md/CLAUDE.md become the gofer block, `bd prime` / `bd codex-hook` hooks become\n" +
					"`gofer repo prime --hook-json`, and core.hooksPath is unset when it points at .beads/hooks and\n" +
					"that directory holds only bd's own hooks. .beads/ is kept. Anything else that mentions bd is\n" +
					"listed for a human. --apply refuses while bd looks active (locks, processes, writes in the last\n" +
					"minutes, unexpired claim leases); --force overrides.",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.BoolOpt(&fromBD, "from-bd", "", false, "migrate from bd (required)")
					c.BoolOpt(&applyMigration, "apply", "", false, "apply changes (default: dry-run)")
					c.BoolOpt(&forceMigration, "force", "", false, "apply even though bd looks active")
					c.BoolOpt(&asJSON, "json", "", false, "print the report as JSON")
				},
				Func: func(c *gcli.Command, _ []string) error {
					if !fromBD {
						return fmt.Errorf("specify --from-bd")
					}
					root, err := os.Getwd()
					if err != nil {
						return err
					}
					report, err := bdmigrate.Run(bdmigrate.Options{Root: root, Apply: applyMigration, Force: forceMigration})
					if err != nil {
						if asJSON {
							_ = printTrackerJSON(c, report)
						} else if report.Root != "" {
							c.Print(report.Format())
						}
						return err
					}
					var keyNotes []string
					if applyMigration {
						if s, discoverErr := tracker.Discover(root, ""); discoverErr == nil {
							keyNotes = bindTrackerProjectKey(s, root)
						}
					}
					if asJSON {
						return printTrackerJSON(c, report)
					}
					c.Print(report.Format())
					printNotes(c, keyNotes)
					return nil
				},
			},
			{
				Name: "prime", Desc: "Print repository tracker context",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.StrOpt(&primeAgent, "agent", "", "", "agent name for server memory injection")
					c.BoolOpt(&asJSON, "hook-json", "", false, "Claude SessionStart JSON output")
				},
				Func: func(c *gcli.Command, _ []string) error {
					s, err := tracker.Discover(".", "")
					if err != nil && !strings.Contains(err.Error(), "no tracker found") {
						return err
					}
					if err != nil {
						s = nil
					}
					body, err := primeWithServerContext(s, config.InputCfgFile, primeAgent)
					if err != nil {
						if asJSON {
							return printTrackerJSON(c, map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": "SessionStart", "additionalContext": ""}})
						}
						return err
					}
					if asJSON {
						return printTrackerJSON(c, map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": "SessionStart", "additionalContext": body}})
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
					c.BoolOpt(&noHooks, "no-hooks", "", false, "reserved; hooks are not installed in this mode")
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
						c.Println("BEADS integration remains; use gofer repo migrate --from-bd")
					}
					if !noHooks {
						for _, agent := range []string{hookrelay.AgentClaude, hookrelay.AgentCodex} {
							if _, err := hookrelay.InstallTrackerPrime(agent, root); err != nil {
								return err
							}
						}
					}
					printNotes(c, bindTrackerProjectKey(s, root))
					tryAutoSync(c, s)
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
					c.Printf("memories: %d\ncommit_policy: %s\nmanaged_block: %v\nhooks: %s\nsync: %s\npending_sync: %d\nlast_sync_at: %s\nsync_summary: %s\n", status.Memories, status.CommitPolicy, status.ManagedBlock, status.Hooks, status.Sync, status.PendingSync, status.LastSyncAt, status.SyncSummary)
					c.Printf("prime_bytes: %d\nprime_truncated: %v\n", status.PrimeBytes, status.PrimeTruncated)
					if strings.TrimSpace(status.ProjectKey) == "" {
						c.Println("project_key: 未填写；可在 .gofer/tracker/config.yaml 填写 project_key")
					} else {
						c.Printf("project_key: %s\n", status.ProjectKey)
					}
					return nil
				},
			},
		},
	}
}

func primeWithServerHandoffs(s *tracker.Store, configPath string) (string, error) {
	return primeWithServerContext(s, configPath, "")
}

func primeWithServerContext(s *tracker.Store, configPath, agentName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	base := ""
	primeCfg := tracker.PrimeConfig{}
	if s != nil {
		var err error
		base, err = s.Prime()
		if err != nil {
			return "", err
		}
		localCfg, err := s.ReadConfig()
		if err != nil {
			return "", err
		}
		primeCfg = localCfg.Prime
	}
	if !primeCfg.ScopedMemoryEnabled() && !primeCfg.HandoffEnabled() {
		return base, nil
	}
	fetch := func(ctx context.Context) (string, error) {
		root, err := os.Getwd()
		if err != nil {
			return "", err
		}
		cfg, _, err := config.Load(configPath)
		if err != nil {
			return "", err
		}
		projectKey := ""
		if s != nil {
			if localCfg, cfgErr := s.ReadConfig(); cfgErr == nil {
				projectKey = strings.TrimSpace(localCfg.ProjectKey)
			}
		}
		if projectKey == "" {
			projectKey, _ = cfg.ProjectForPath(root)
		}
		addr := strings.TrimSpace(cfg.Server.Addr)
		envAddr := strings.TrimSpace(os.Getenv("GOFER_SERVER_ADDR"))
		if addr == config.DefaultAddr {
			addr = envAddr
		} else if strings.TrimSpace(configPath) == "" && envAddr != "" {
			addr = envAddr
		}
		if addr == "" {
			addr = strings.TrimSpace(os.Getenv("GOFER_SERVER_ADDR"))
		}
		cli := client.NewWithTimeout(addr, os.Getenv("GOFER_SERVER_TOKEN"), 250*time.Millisecond)
		if addr == "" {
			return "", nil
		}
		global := []client.ScopedMemory(nil)
		var globalErr error
		project := []client.ScopedMemory(nil)
		if primeCfg.ScopedMemoryEnabled() {
			global, globalErr = cli.ListScopedMemories(client.ScopedMemoryListOpts{Scope: "global"})
			if projectKey != "" {
				project, _ = cli.ListScopedMemories(client.ScopedMemoryListOpts{Scope: "project", ScopeKey: projectKey})
			}
		}
		var out strings.Builder
		if primeCfg.ScopedMemoryEnabled() {
			out.WriteString(scopedPrimeSection("## 全局记忆\n\n", global, agentName, primeCfg.SummaryLimit()))
			if projectKey != "" {
				out.WriteString(scopedPrimeSection("## 项目记忆\n\n", project, agentName, primeCfg.SummaryLimit()))
			}
		}
		if !primeCfg.HandoffEnabled() {
			return out.String(), nil
		}
		plans, err := cli.ListPlans(client.PlanListOpts{Status: "open", Project: projectKey, Limit: clientPlanPrimeLimit})
		if err != nil {
			if globalErr != nil {
				return "", nil
			}
			return out.String(), nil
		}
		sort.SliceStable(plans.Plans, func(i, j int) bool {
			if plans.Plans[i].UpdatedAt != plans.Plans[j].UpdatedAt {
				return plans.Plans[i].UpdatedAt > plans.Plans[j].UpdatedAt
			}
			return plans.Plans[i].PlanID < plans.Plans[j].PlanID
		})
		out.WriteString("\n## 进行中 plan 的交接说明\n\n")
		for i, plan := range plans.Plans {
			if i >= clientPlanPrimeLimit {
				break
			}
			h, getErr := cli.GetPlanHandoff(plan.PlanID, 0)
			if getErr != nil || h.Version == 0 {
				continue
			}
			out.WriteString(fmt.Sprintf("- %s v%d (%s, %d)\n%s\n", plan.PlanID, h.Version, h.By, h.At, h.Body))
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			default:
			}
		}
		return out.String(), nil
	}
	result := make(chan string, 1)
	go func() { section, _ := fetch(ctx); result <- section }()
	var serverSection string
	select {
	case serverSection = <-result:
	case <-ctx.Done():
		return base, nil
	}
	if strings.TrimSpace(serverSection) == "" {
		return base, nil
	}
	return tracker.AppendPrimeSections(base, serverSection), nil
}

func scopedPrimeSection(heading string, items []client.ScopedMemory, agentName string, summaryLimit int) string {
	var out strings.Builder
	out.WriteString(heading)
	summaries := 0
	for _, item := range items {
		if !memoryForAgent(item.Tags, agentName) {
			continue
		}
		full := tracker.PrimeMemoryFull(item.Tags, agentName)
		if !full && summaryLimit >= 0 && summaries >= summaryLimit {
			continue
		}
		out.WriteString(tracker.PrimeMemoryLine(item.Key, item.Content, item.Tags, agentName))
		if !full {
			summaries++
		}
	}
	if summaries > 0 {
		out.WriteString("全文：`gofer memory show <key>`\n")
	}
	return out.String()
}

func memoryForAgent(tags []string, agentName string) bool {
	matched := false
	for _, tag := range tags {
		if strings.HasPrefix(tag, "agent:") {
			matched = true
			if strings.TrimPrefix(tag, "agent:") == strings.TrimSpace(agentName) {
				return true
			}
		}
	}
	return !matched
}

func syncServerOrEnv(flag string) string {
	if strings.TrimSpace(flag) != "" {
		return flag
	}
	return os.Getenv("GOFER_SERVER_ADDR")
}

const clientPlanPrimeLimit = 3

func printTrackerJSON(c *gcli.Command, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.Println(string(b))
	return nil
}
