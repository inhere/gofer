package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/hookrelay"
	"github.com/inhere/gofer/internal/tracker"
)

func NewRepoCmd() *gcli.Command {
	var prefix, initTracker, statusTracker, syncServer string
	var noAgents, noHooks, asJSON, fromBD, applyMigration bool
	return &gcli.Command{
		Name: "repo", Desc: "Manage this repository's local tracker",
		Subs: []*gcli.Command{
			{
				Name: "sync", Desc: "Synchronize the local tracker with a server mirror",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.StrOpt(&syncServer, "server", "", "", "tracker mirror URL (required; no live fallback)")
				},
				Func: func(c *gcli.Command, _ []string) error {
					if strings.TrimSpace(syncServer) == "" {
						return fmt.Errorf("--server is required; offline local writes remain available")
					}
					s, err := tracker.Discover(".", "")
					if err != nil {
						return err
					}
					cfg, err := s.ReadConfig()
					if err != nil {
						return err
					}
					issues, err := s.ReadIssues()
					if err != nil {
						return err
					}
					memories, err := s.ReadMemories()
					if err != nil {
						return err
					}
					payload := map[string]any{"tracker_id": cfg.TrackerID, "project_key": cfg.ProjectKey, "prefix": cfg.Prefix, "issues": issues, "memories": memories}
					body, err := json.Marshal(payload)
					if err != nil {
						return err
					}
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(syncServer, "/")+"/v1/tracker/sync", bytes.NewReader(body))
					if err != nil {
						return err
					}
					req.Header.Set("Content-Type", "application/json")
					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						c.Printf("sync warning: %v\n", err)
						return nil
					}
					defer resp.Body.Close()
					if resp.StatusCode >= 300 {
						c.Printf("sync warning: server returned %s\n", resp.Status)
						return nil
					}
					c.Printf("sync: tracker_id=%s server=%s\n", cfg.TrackerID, syncServer)
					return nil
				},
			},
			{
				Name: "migrate", Desc: "Migrate local bd issues and memories",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.BoolOpt(&fromBD, "from-bd", "", false, "read .beads/issues.jsonl")
					c.BoolOpt(&applyMigration, "apply", "", false, "apply changes (default: dry-run)")
				},
				Func: func(c *gcli.Command, _ []string) error {
					if !fromBD {
						return fmt.Errorf("specify --from-bd")
					}
					root, err := os.Getwd()
					if err != nil {
						return err
					}
					mode := "dry-run"
					if applyMigration {
						mode = "apply"
					}
					for _, step := range []string{"1. import .beads/issues.jsonl", "2. import bd memories --json if available", "3. replace BEADS managed blocks", "4. replace bd SessionStart hooks", "5. unset core.hooksPath when it points to .beads/hooks", "6. preserve .beads/"} {
						c.Printf("%s: %s\n", mode, step)
					}
					report, err := tracker.MigrateFromBD(root, applyMigration)
					if err != nil {
						return err
					}
					if !applyMigration {
						c.Printf("dry-run: %d issues; no files written\n", report.Issues)
						return nil
					}
					for _, agent := range []string{hookrelay.AgentClaude, hookrelay.AgentCodex} {
						changed, err := hookrelay.InstallTrackerPrime(agent, root, true)
						if err != nil {
							return err
						}
						if changed {
							path, _ := hookrelay.ConfigFileFor(agent, root)
							report.Files = append(report.Files, path)
						}
					}
					hooksPath, err := exec.Command("git", "-C", root, "config", "--local", "--get", "core.hooksPath").Output()
					oldHooksPath := strings.TrimSpace(string(hooksPath))
					if err == nil && strings.HasSuffix(strings.ReplaceAll(oldHooksPath, "\\", "/"), ".beads/hooks") {
						cmd := exec.Command("git", "-C", root, "config", "--local", "--unset", "core.hooksPath")
						if output, err := cmd.CombinedOutput(); err != nil {
							return fmt.Errorf("unset core.hooksPath: %w: %s", err, output)
						}
					}
					c.Printf("imported issues=%d memories=%d; files=%s; core.hooksPath original=%q\n", report.Issues, report.Memories, strings.Join(report.Files, ", "), oldHooksPath)
					for _, note := range report.Notes {
						c.Println(note)
					}
					return nil
				},
			},
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
							return printTrackerJSON(c, map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": "SessionStart", "additionalContext": ""}})
						}
						return err
					}
					body, err := primeWithServerHandoffs(s, config.InputCfgFile)
					if err != nil {
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

func primeWithServerHandoffs(s *tracker.Store, configPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.PrimeWithHandoffFetch(ctx, func(ctx context.Context) (string, error) {
		root, err := os.Getwd()
		if err != nil {
			return "", err
		}
		cfg, _, err := config.Load(configPath)
		if err != nil {
			return "", err
		}
		projectKey, ok := cfg.ProjectForPath(root)
		if !ok {
			return "", nil
		}
		cli, err := newClient(configPath, "", "")
		if err != nil {
			return "", err
		}
		plans, err := cli.ListPlans(client.PlanListOpts{Status: "open", Project: projectKey, Limit: clientPlanPrimeLimit})
		if err != nil {
			return "", err
		}
		sort.SliceStable(plans.Plans, func(i, j int) bool {
			if plans.Plans[i].UpdatedAt != plans.Plans[j].UpdatedAt {
				return plans.Plans[i].UpdatedAt > plans.Plans[j].UpdatedAt
			}
			return plans.Plans[i].PlanID < plans.Plans[j].PlanID
		})
		var out strings.Builder
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
	})
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
