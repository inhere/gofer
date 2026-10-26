package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/bdmigrate"
	"github.com/inhere/gofer/internal/brief"
	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/hookrelay"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/tracker"
)

func NewRepoCmd() *gcli.Command {
	var statusChanged bool
	var prefix, initTracker, statusTracker, syncServer, primeAgent, primeKeywords, syncRemote, commitPolicy string
	var primeSession, primeSource string
	var syncTimeout time.Duration
	var noAgents, noHooks, asJSON, fromBD, applyMigration, forceMigration bool
	return &gcli.Command{
		Name: "repo", Desc: "Manage this repository's local tracker",
		Subs: []*gcli.Command{
			newRepoMirrorCmd(),
			{
				Name: "sync", Desc: "Synchronize the local tracker with a server mirror",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.StrOpt(&syncServer, "server", "", "", "tracker mirror URL override")
					c.DurationOpt(&syncTimeout, "timeout", "", time.Minute, "request timeout for this manual sync (automatic sync after write commands keeps its short timeout)")
					c.StrOpt(&syncRemote, "remote", "", "", "ask the server to run the sync for this tracker_id (a hidden exec job in that repo's directory) instead of syncing here")
				},
				Func: func(c *gcli.Command, _ []string) error {
					if strings.TrimSpace(syncRemote) != "" {
						cli, err := newClient(config.InputCfgFile, syncServerOrEnv(syncServer), os.Getenv("GOFER_SERVER_TOKEN"))
						if err != nil {
							return err
						}
						res, err := cli.SyncTrackerRepo(strings.TrimSpace(syncRemote))
						if err != nil {
							return err
						}
						c.Printf("sync dispatched: job=%s project=%s runner=%s cwd=%s\nfollow it with: gofer job logs %s\n", res.JobID, res.ProjectKey, res.Runner, res.Cwd, res.JobID)
						return nil
					}
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
					report, err := tracker.SyncHTTPWithToken(ctx, s, cli.BaseURL(), cli.Token())
					if err != nil {
						return err
					}
					cfg, _ := s.ReadConfig()
					c.Printf("sync: tracker_id=%s server=%s\n", cfg.TrackerID, cli.BaseURL())
					if report.Summary != "" {
						c.Printf("sync: %s\n", report.Summary)
					}
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
					report, err := bdmigrate.Run(bdmigrate.Options{Root: root, Apply: applyMigration, Force: forceMigration, Language: managedBlockLanguage(root)})
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
							keyNotes = append(keyNotes, installMergeDriverNotes(root)...)
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
					c.StrOpt(&primeKeywords, "keywords", "", "", "comma-separated subsystems / languages / modules this session is about: the matching memories are injected in full and ranked first (default: $GOFER_PRIME_KEYWORDS)")
					c.StrOpt(&primeSession, "session", "", "", "session id whose own tasks are re-injected after a compact / resume (default: the SessionStart hook's session_id on stdin)")
					c.StrOpt(&primeSource, "source", "", "", "SessionStart source: compact | resume re-inject this session's own tasks; startup | clear do not (default: the hook's source on stdin)")
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
					if asJSON {
						// A SessionStart hook gets its payload on stdin (Claude Code and Codex both
						// send session_id + source there), so prime reads it itself: the installed
						// `gofer repo prime --hook-json` command needs no re-install and no shell
						// quoting to pass the values. Flags win over stdin.
						if sid, source := readPrimeHookStdin(os.Stdin); sid != "" || source != "" {
							if primeSession == "" {
								primeSession = sid
							}
							if primeSource == "" {
								primeSource = source
							}
						}
					}
					body, err := primeWithServerContext(s, config.InputCfgFile, primeAgent, primeKeywordsFromFlag(primeKeywords))
					if err != nil {
						if asJSON {
							return printTrackerJSON(c, map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": "SessionStart", "additionalContext": ""}})
						}
						return err
					}
					if section := primeRefocusSection(s, config.InputCfgFile, primeSession, primeSource, tracker.PrimeMaxBytes-len(body)); section != "" {
						body = tracker.AppendPrimeSections(body, section)
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
					c.StrOpt(&commitPolicy, "commit-policy", "", "", "set commit_policy: local-commit | ask | none (default: keep the current value; a new tracker gets local-commit)")
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
					s, beads, err := tracker.InitWith(root, tracker.InitOptions{Prefix: prefix, NoAgentsMD: noAgents, CommitPolicy: commitPolicy, Language: managedBlockLanguage(root)})
					if err != nil {
						return err
					}
					c.Printf("tracker initialized: %s\n", s.Dir)
					if cfg, err := s.ReadConfig(); err == nil {
						c.Printf("commit_policy: %s (change with --commit-policy local-commit|ask|none)\n", cfg.CommitPolicy)
					}
					if beads {
						c.Println("BEADS integration remains; use gofer repo migrate --from-bd")
					}
					if !noHooks {
						for _, agent := range []string{hookrelay.AgentClaude, hookrelay.AgentCodex} {
							if _, err := hookrelay.InstallTrackerPrime(agent, root); err != nil {
								return err
							}
							if _, err := hookrelay.InstallCommandMemory(agent, root); err != nil {
								return err
							}
						}
					}
					printNotes(c, bindTrackerProjectKey(s, root))
					printNotes(c, installMergeDriverNotes(root))
					tryAutoSync(c, s)
					return nil
				},
			},
			newRepoMergeDriverCmd(),
			newRepoChangelogMergeCmd(),
			{
				Name: "status", Desc: "Inspect local tracker status",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.StrOpt(&statusTracker, "tracker", "", "", "explicit .gofer/tracker directory")
					c.BoolOpt(&asJSON, "json", "", false, "print JSON")
					c.BoolOpt(&statusChanged, "changed", "", false, "list issue/memory changes vs git HEAD")
				},
				Func: func(c *gcli.Command, _ []string) error {
					s, err := tracker.Discover(".", statusTracker)
					if err != nil {
						return err
					}
					if statusChanged {
						rep, err := s.ChangedSinceHEAD()
						if err != nil {
							return err
						}
						if asJSON {
							return printTrackerJSON(c, rep)
						}
						for _, line := range rep.Lines() {
							c.Println(line)
						}
						return nil
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

// primeRefocusSource reports whether a SessionStart source asks for the session's own
// tasks back, and whether to word it as a resume (false = a compact).
func primeRefocusSource(source string) (enabled, resumed bool) {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "compact":
		return true, false
	case "resume":
		return true, true
	}
	return false, false
}

// primeHookStdinTimeout bounds how long prime waits for a hook payload on stdin. A
// real hook writes it and closes at once; a stdin that never answers (an unusual
// wrapper) must not delay the session start.
const primeHookStdinTimeout = 300 * time.Millisecond

// readPrimeHookStdin reads the SessionStart payload when stdin is a pipe (a hook).
// A terminal or an unreadable stdin yields empty values.
func readPrimeHookStdin(f *os.File) (sid, source string) {
	if f == nil {
		return "", ""
	}
	fi, err := f.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice != 0 {
		return "", ""
	}
	type found struct{ sid, source string }
	ch := make(chan found, 1)
	go func() {
		data, _ := io.ReadAll(io.LimitReader(f, 1<<20))
		sid, source := parsePrimeHookStdin(data)
		ch <- found{sid: sid, source: source}
	}()
	select {
	case got := <-ch:
		return got.sid, got.source
	case <-time.After(primeHookStdinTimeout):
		return "", ""
	}
}

// parsePrimeHookStdin reads session_id and source out of a SessionStart hook's stdin
// JSON (Claude Code and Codex both send them). Unknown or malformed input is "" —
// prime then simply skips the re-injection.
func parsePrimeHookStdin(data []byte) (sid, source string) {
	var raw struct {
		SessionID string `json:"session_id"`
		Source    string `json:"source"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return "", ""
	}
	return strings.TrimSpace(raw.SessionID), strings.TrimSpace(raw.Source)
}

// primeRefocusSection renders 「本会话当前任务」for a compact / resume, or "".
// Server unreachable, an unknown source and a session with nothing in flight all
// yield "" — the rest of the prime is never affected.
func primeRefocusSection(s *tracker.Store, configPath, session, source string, budget int) string {
	enabled, resumed := primeRefocusSource(source)
	if !enabled {
		return ""
	}
	session = strings.TrimSpace(session)
	if session == "" {
		return ""
	}
	root, err := os.Getwd()
	if err != nil {
		return ""
	}
	addr, _, err := primeServerTarget(s, configPath, root)
	if err != nil || addr == "" {
		return ""
	}
	focus, err := client.NewWithTimeout(addr, os.Getenv("GOFER_SERVER_TOKEN"), primeClientTimeout).SessionFocus(session)
	if err != nil {
		return ""
	}
	r := brief.Refocus{Topic: focus.Topic}
	for _, p := range focus.Plans {
		r.Plans = append(r.Plans, brief.RefocusPlan{PlanID: p.PlanID, Title: p.Title, Status: p.Status,
			Progress: p.Progress, Total: p.Total, Next: p.Next, Blocked: p.Blocked})
	}
	for _, j := range focus.Jobs {
		r.Jobs = append(r.Jobs, brief.RefocusJob{JobID: j.JobID, Title: j.Title, Status: j.Status})
	}
	for _, m := range focus.Mail {
		r.Mail = append(r.Mail, brief.RefocusMail{ID: m.ID, From: m.From, Subject: m.Subject})
	}
	r.Claims = refocusClaims(s, focus)
	return brief.RenderRefocus(r, resumed, budget)
}

// refocusClaimsLimit caps how many claimed issues the section reminds about.
const refocusClaimsLimit = 3

// refocusClaims finds the issues this session claimed. The tracker records no
// session id on a claim, so an issue whose assignee is the session's caller is a
// direct hit and an in_progress issue touched after the session started is only a
// probable one (Inferred, rendered as 「可能」).
func refocusClaims(s *tracker.Store, focus client.SessionFocus) []brief.RefocusClaim {
	if s == nil {
		return nil
	}
	issues, err := s.ReadIssues()
	if err != nil {
		return nil
	}
	caller := strings.TrimSpace(focus.Caller)
	out := make([]brief.RefocusClaim, 0, refocusClaimsLimit)
	for _, issue := range issues {
		if issue.Status != "in_progress" {
			continue
		}
		inferred := true
		if caller != "" && strings.EqualFold(strings.TrimSpace(issue.Assignee), caller) {
			inferred = false
		} else if focus.StartedAt > 0 {
			t, err := time.Parse(time.RFC3339, issue.UpdatedAt)
			if err != nil || t.Unix() < focus.StartedAt {
				continue
			}
		} else {
			continue
		}
		out = append(out, brief.RefocusClaim{ID: issue.ID, Title: issue.Title, Status: issue.Status, Inferred: inferred})
		if len(out) >= refocusClaimsLimit {
			break
		}
	}
	return out
}

func primeWithServerHandoffs(s *tracker.Store, configPath string) (string, error) {
	return primeWithServerContext(s, configPath, "", nil)
}

// managedBlockLanguage is the language of the instruction block a repository gets
// (`repo init` / `repo migrate`). It is the repository's project language, else
// `server.language`, else the deployment default (design 2026-10-27-i18n-design.md
// §3). The machine's locale is deliberately not a source: the block is written for
// the repository, so running init on an English-locale machine must not flip a
// deployment that names another language (see i18n.FromEnvVar).
func managedBlockLanguage(root string) i18n.Lang {
	cfg, _, err := config.Load(config.InputCfgFile)
	if err != nil || cfg == nil {
		return i18n.Default
	}
	if key, ok := cfg.ProjectForPath(root); ok {
		return cfg.LanguageFor(key)
	}
	return cfg.Language()
}

// primeWithServerContext renders the whole session-opening prime: the local tracker
// prime (selected by the cwd and, since gofer-11tz, by keywords) plus the server-backed
// scoped memories and open plans. keywords names the subsystems / languages / modules
// the session is about (`repo prime --keywords`, else $GOFER_PRIME_KEYWORDS); nil keeps
// the plain cwd-scoped material.
func primeWithServerContext(s *tracker.Store, configPath, agentName string, keywords []string) (string, error) {
	base := ""
	primeCfg := tracker.PrimeConfig{}
	primeLang := i18n.FromEnv()
	if s != nil {
		localCfg, err := s.ReadConfig()
		if err != nil {
			return "", err
		}
		primeCfg = localCfg.Prime
		cwd, _ := os.Getwd()
		lang := primeLang
		if appCfg, _, cfgErr := config.Load(configPath); cfgErr == nil {
			if key, ok := appCfg.ProjectForPath(cwd); ok {
				lang = appCfg.LanguageFor(key)
			} else {
				lang = appCfg.Language()
			}
		}
		primeLang = lang
		opts := tracker.PrimeOptions{Cwd: cwd, Now: time.Now(), Language: lang, Keywords: keywords}
		workHint := make(chan bool, 1)
		go func() { workHint <- primeWorkHint(s, configPath, cwd) }()
		if primeCfg.FocusEnabled() {
			opts.Focus = primeFocus(s, configPath, opts.Now, lang)
		}
		opts.WorkHint = <-workHint
		if base, err = s.PrimeWith(opts); err != nil {
			return "", err
		}
	}
	if !primeCfg.ScopedMemoryEnabled() && !primeCfg.HandoffEnabled() {
		return base, nil
	}
	// The server budget starts after the local part: the focus line alone may take
	// up to primeFocusTimeout on a slow machine, which used to leave nothing for the
	// server sections and drop them silently.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	// gofer-j6qn: the global memories come from this machine's copy, so they are
	// in the prime whether or not the server answers. Within the same budget the copy
	// is synced first (a pull of what changed elsewhere); a copy that never synced and
	// holds nothing falls back to the server's list.
	var globalStore *tracker.Store
	globalSynced := make(chan struct{})
	if primeCfg.ScopedMemoryEnabled() {
		if gs, err := openGlobalMemoryStore(); err == nil {
			globalStore = gs
			go func() {
				defer close(globalSynced)
				_, _ = syncGlobalMemories(gs, primeGlobalSyncTimeout)
			}()
		}
	}
	if globalStore == nil {
		close(globalSynced)
	}
	fetch := func(ctx context.Context) (string, error) {
		root, err := os.Getwd()
		if err != nil {
			return "", err
		}
		addr, projectKey, err := primeServerTarget(s, configPath, root)
		if err != nil {
			return "", err
		}
		cli := client.NewWithTimeout(addr, os.Getenv("GOFER_SERVER_TOKEN"), primeClientTimeout)
		if addr == "" {
			return "", nil
		}
		projectKey = serverProjectKey(cli, projectKey, root)
		global := []client.ScopedMemory(nil)
		var globalErr error
		project := []client.ScopedMemory(nil)
		if primeCfg.ScopedMemoryEnabled() {
			if globalStore == nil {
				global, globalErr = cli.ListScopedMemories(client.ScopedMemoryListOpts{Scope: "global"})
			}
			if projectKey != "" {
				project, _ = cli.ListScopedMemories(client.ScopedMemoryListOpts{Scope: "project", ScopeKey: projectKey})
			}
		}
		var out strings.Builder
		if primeCfg.ScopedMemoryEnabled() {
			opts := tracker.ScopedPrimeOptions{AgentName: agentName, Now: time.Now(), Language: primeLang, SummaryLimit: primeCfg.SummaryLimit(), Budget: scopedPrimeBudget(base), Selection: tracker.KeywordsOnly(keywords)}
			if s != nil {
				opts.CwdRel, opts.CwdKnown = s.CwdRel(root)
			}
			if globalStore == nil {
				opts.LsHint = "gofer memory ls --global"
				out.WriteString(tracker.RenderScopedPrimeSection(i18n.T(primeLang, "prime.memory.scoped_global_title"), scopedTrackerMemories(global), opts))
			}
			if projectKey != "" {
				opts.LsHint = "gofer memory ls --project " + projectKey
				out.WriteString(tracker.RenderScopedPrimeSection(i18n.T(primeLang, "prime.memory.scoped_project_title"), scopedTrackerMemories(project), opts))
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
		var issues []tracker.Issue
		if s != nil {
			issues, _ = s.ReadIssues()
		}
		var planOut strings.Builder
		for i, plan := range plans.Plans {
			if i >= clientPlanPrimeLimit {
				break
			}
			// The doing / ready todos and the issues they name (design 2026-10-10 §一);
			// a plan whose detail cannot be read still shows its line.
			if full, getErr := cli.GetPlan(plan.PlanID); getErr == nil {
				plan = full
			}
			for _, line := range brief.PrimePlanLines(plan, issues) {
				planOut.WriteString(line + "\n")
			}
			if h, getErr := cli.GetPlanHandoff(plan.PlanID, 0); getErr == nil && h.Version > 0 {
				planOut.WriteString(i18n.T(primeLang, "prime.header.handoff_line", "version", h.Version, "by", h.By, "at", h.At) + "\n" + h.Body + "\n")
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			default:
			}
		}
		if planOut.Len() > 0 {
			out.WriteString("\n" + i18n.T(primeLang, "prime.header.plans_heading") + "\n\n")
			out.WriteString(planOut.String())
		}
		return out.String(), nil
	}
	result := make(chan string, 1)
	go func() { section, _ := fetch(ctx); result <- section }()
	var serverSection string
	select {
	case serverSection = <-result:
	case <-ctx.Done():
	}
	// The local global section goes first, as the server's used to.
	if globalStore != nil {
		select {
		case <-globalSynced:
		case <-ctx.Done():
		}
		if section := primeGlobalSection(globalStore, base, agentName, keywords, primeCfg, s, primeLang); section != "" {
			serverSection = section + serverSection
		}
	}
	if strings.TrimSpace(serverSection) == "" {
		return base, nil
	}
	return tracker.AppendPrimeSectionsIn(primeLang, base, serverSection), nil
}

// primeGlobalSyncTimeout bounds the global copy's sync inside the prime budget.
const primeGlobalSyncTimeout = 1200 * time.Millisecond

// primeGlobalSection renders the 「全局记忆」 section from the local copy. A copy that
// never synced and holds nothing asks the server (short timeout) instead.
func primeGlobalSection(gs *tracker.Store, base, agentName string, keywords []string, primeCfg tracker.PrimeConfig, s *tracker.Store, lang i18n.Lang) string {
	items, err := localGlobalMemoriesFrom(gs)
	if errors.Is(err, tracker.ErrGlobalNotSynced) {
		if cli, cerr := trackerClient(); cerr == nil {
			rows, lerr := client.NewWithTimeout(cli.BaseURL(), cli.Token(), primeClientTimeout).ListScopedMemories(client.ScopedMemoryListOpts{Scope: "global"})
			if lerr == nil {
				items, err = scopedTrackerMemories(rows), nil
			}
		}
	}
	if err != nil {
		return ""
	}
	opts := tracker.ScopedPrimeOptions{AgentName: agentName, Now: time.Now(), Language: lang, SummaryLimit: primeCfg.SummaryLimit(), Budget: scopedPrimeBudget(base), LsHint: "gofer memory ls --global", Selection: tracker.KeywordsOnly(keywords)}
	if s != nil {
		if root, werr := os.Getwd(); werr == nil {
			opts.CwdRel, opts.CwdKnown = s.CwdRel(root)
		}
	}
	return tracker.RenderScopedPrimeSection(i18n.T(lang, "prime.memory.scoped_global_title"), items, opts)
}

// primeClientTimeout bounds every server request prime makes.
const primeClientTimeout = 250 * time.Millisecond

// primeServerTarget resolves the server address prime talks to ("" = none) and
// the project key of root (tracker config first, then the gofer config mapping).
func primeServerTarget(s *tracker.Store, configPath, root string) (addr, projectKey string, err error) {
	cfg, _, err := config.Load(configPath)
	if err != nil {
		return "", "", err
	}
	if s != nil {
		if localCfg, cfgErr := s.ReadConfig(); cfgErr == nil {
			projectKey = strings.TrimSpace(localCfg.ProjectKey)
		}
	}
	if projectKey == "" {
		projectKey, _ = cfg.ProjectForPath(root)
	}
	addr = strings.TrimSpace(cfg.Server.Addr)
	envAddr := strings.TrimSpace(os.Getenv("GOFER_SERVER_ADDR"))
	if addr == config.DefaultAddr {
		addr = envAddr
	} else if strings.TrimSpace(configPath) == "" && envAddr != "" {
		addr = envAddr
	}
	if addr == "" {
		addr = envAddr
	}
	return addr, projectKey, nil
}

// scopedPrimeBudget splits what the local prime left between the two scoped
// sections, keeping room for the plan handoff section (design §2.3.1 「余量」).
func scopedPrimeBudget(base string) int {
	remaining := tracker.PrimeMaxBytes - len(base)
	if remaining <= 0 {
		return 0
	}
	return remaining / 3
}

func scopedTrackerMemories(items []client.ScopedMemory) []tracker.Memory {
	out := make([]tracker.Memory, 0, len(items))
	for _, item := range items {
		out = append(out, item.TrackerMemory())
	}
	return out
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
