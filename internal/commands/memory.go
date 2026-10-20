package commands

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/tracker"
)

// memorySetFlags are the `memory set` options. Empty means "keep the stored
// value"; "-" clears title / summary / source / when-* fields.
type memorySetFlags struct {
	tags, tagsAlias, title, summary, kind, ttl, source string
	whenKeywords, whenCommands                         string
	whenPaths                                          gcli.Strings
	doctorIgnore                                       string
}

// memorySourceFromEnv is the default `source` of a memory written by the CLI:
// the gofer job it runs in, else the gofer session (design §2.10).
func memorySourceFromEnv() string {
	if id := strings.TrimSpace(os.Getenv("GOFER_JOB_ID")); id != "" {
		return "job:" + id
	}
	if id, _ := client.SessionIDFromEnv(os.Getenv); id != "" {
		return "session:" + id
	}
	return ""
}

func memoryOptString(value string) *string {
	if value == "" {
		return nil
	}
	if value == "-" {
		value = ""
	}
	return &value
}

func memoryOptList(value string) *[]string {
	if value == "" {
		return nil
	}
	list := []string{}
	if value != "-" {
		list = tracker.ParseTags(value)
	}
	return &list
}

// memoryOptPaths turns the repeatable --when-paths into the patch value: not given at all
// (nil) keeps the stored globs, while an empty value or `-` clears them — the explicit way
// to make a memory repo-wide after it was scoped to a subproject.
func memoryOptPaths(values gcli.Strings) *[]string {
	if len(values) == 0 {
		return nil
	}
	joined := strings.TrimSpace(strings.Join(values, ","))
	list := []string{}
	if joined == "" || joined == "-" {
		return &list
	}
	list = tracker.ParseTags(joined)
	return &list
}

func (f memorySetFlags) patch(content string) (tracker.MemoryPatch, error) {
	patch := tracker.MemoryPatch{Content: content, By: trackerActor(), Title: memoryOptString(f.title), Summary: memoryOptString(f.summary), Source: memoryOptString(f.source),
		WhenKeywords: memoryOptList(f.whenKeywords), WhenPaths: memoryOptPaths(f.whenPaths), WhenCommands: memoryOptList(f.whenCommands),
		DoctorIgnore: memoryOptList(f.doctorIgnore), DefaultSource: memorySourceFromEnv()}
	if patch.DoctorIgnore != nil {
		for _, slug := range *patch.DoctorIgnore {
			if !tracker.ValidDoctorSlug(slug) {
				return patch, fmt.Errorf("invalid --doctor-ignore %q (%s)", slug, strings.Join(tracker.DoctorSlugs, "|"))
			}
		}
	}
	if tags := strings.Trim(f.tags+","+f.tagsAlias, ","); tags != "" {
		patch.Tags = tracker.ParseTags(tags)
	}
	if f.kind != "" {
		if !tracker.ValidMemoryKind(f.kind) {
			return patch, fmt.Errorf("invalid --kind %q (rule|note|handoff)", f.kind)
		}
		patch.Kind = &f.kind
	}
	if f.ttl != "" {
		ttl, err := tracker.ParseMemoryTTL(f.ttl)
		if err != nil {
			return patch, err
		}
		patch.TTL = &ttl
	}
	return patch, nil
}

func NewMemoryCmd() *gcli.Command {
	var trackerPath string
	var setFlags memorySetFlags
	var listTags gcli.Strings
	var listKind, archiveReason, promoteKind, promoteSummary, flagReason string
	var scopeMinConfidence, scopeOnly string
	var asJSON, globalScope, listArchived, scopeApply, scopeSuggest bool
	var projectScope string
	bind := func(c *gcli.Command) {
		bindConfigFlag(c)
		bindServerFlags(c)
		c.StrOpt(&trackerPath, "tracker", "", "", "explicit .gofer/tracker directory")
		c.BoolOpt(&asJSON, "json", "", false, "print JSON")
		c.BoolOpt(&globalScope, "global", "", false, "use the global memories (this machine's copy under <config-dir>/tracker, synced with the server)")
		c.StrOpt(&projectScope, "project", "", "", "use server project memory scope")
	}
	scope := func() (string, string, error) {
		if globalScope && projectScope != "" {
			return "", "", fmt.Errorf("--global and --project are mutually exclusive")
		}
		if globalScope {
			return "global", "", nil
		}
		if projectScope != "" {
			return "project", projectScope, nil
		}
		return "", "", nil
	}
	scopedClient := func() (*client.Client, string, string, error) {
		scopeName, scopeKey, err := scope()
		if err != nil {
			return nil, "", "", err
		}
		// Global memories are a local store now (gofer-j6qn); only the project scope
		// is still served by the server alone.
		if scopeName != "project" {
			return nil, "", "", nil
		}
		cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
		if err != nil {
			return nil, "", "", fmt.Errorf("scoped memory requires a reachable server: %w", err)
		}
		return cli, scopeName, scopeKey, nil
	}
	trackerStore := func() (*tracker.Store, error) {
		if globalScope {
			return globalMemoryStore()
		}
		return tracker.Discover(".", trackerPath)
	}
	store := trackerStore
	// localOnly is the store for the local-only subcommands (archive, restore,
	// promote): the repository tracker or the global copy, never the server's project scope.
	localOnly := func() (*tracker.Store, error) {
		if scopeName, _, err := scope(); err != nil || scopeName == "project" {
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("this subcommand works on the repository tracker or --global, not --project")
		}
		return trackerStore()
	}
	// writeStore is the store a change goes to. A job may not change the global
	// memories (they reach every session); it can only flag one.
	writeStore := func() (*tracker.Store, error) {
		if globalScope && jobCredentialEnv() {
			return nil, fmt.Errorf("a job cannot change global memories; report a stale one with `gofer memory flag --global <key> --reason …`")
		}
		return store()
	}
	printMemory := func(c *gcli.Command, item tracker.Memory) error {
		if asJSON {
			return printTrackerJSON(c, item)
		}
		c.Print(tracker.MemoryDetail(item, time.Now()))
		return nil
	}
	return withMemoryCandidateCmds(&gcli.Command{Name: "memory", Desc: "Manage repository-local memories", Subs: []*gcli.Command{
		{Name: "set", Aliases: []string{"remember"}, Desc: "Set a memory (updating keeps every field you do not pass; \"-\" clears title/summary/source/when-*)", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("key", "memory key", true)
			c.AddArg("content", "memory content (full text)", true)
			c.StrOpt(&setFlags.tags, "tag", "", "", "comma-separated tags (the first tag groups the prime index)")
			c.StrOpt(&setFlags.tagsAlias, "tags", "", "", "alias of --tag")
			c.StrOpt(&setFlags.title, "title", "", "", "display name (<= 80 chars, one line); the key stays the id, shown in ls / show / prime index as \"title — summary\"")
			c.StrOpt(&setFlags.summary, "summary", "", "", "one sentence (<= 80 chars): what it covers and when to read it; required for rule/note content > 200 chars")
			c.StrOpt(&setFlags.kind, "kind", "", "", "rule (long-lived convention, full text in prime) | note (default, index line) | handoff (short-lived, expires)")
			c.StrOpt(&setFlags.ttl, "ttl", "", "", "handoff lifetime, e.g. 14d (default), 2w, 36h")
			c.StrOpt(&setFlags.whenKeywords, "when-keywords", "", "", "comma-separated prompt keywords that make it relevant")
			c.VarOpt(&setFlags.whenPaths, "when-paths", "", "repo path globs, comma-separated and/or repeatable, e.g. 'web/**,internal/tunnel/**'; '' or - clears them")
			c.StrOpt(&setFlags.whenCommands, "when-commands", "", "", "comma-separated command prefixes, e.g. 'git push,gofer worker upgrade'")
			c.StrOpt(&setFlags.source, "source", "", "", "origin reference: issue:<id> | plan:<id> | job:<id> | session:<id> (default: job:$GOFER_JOB_ID or session:$GOFER_SESSION_ID when unset)")
			c.StrOpt(&setFlags.doctorIgnore, "doctor-ignore", "", "", "comma-separated `memory doctor` slugs to silence for this memory")
		}, Func: func(c *gcli.Command, _ []string) error {
			key := c.Arg("key").String()
			patch, err := setFlags.patch(c.Arg("content").String())
			if err != nil {
				return err
			}
			if cli, scopeName, scopeKey, err := scopedClient(); scopeName != "" || err != nil {
				if err != nil {
					return err
				}
				// Merge on the client so the summary rule sees the stored fields,
				// then send the full record.
				var existing *tracker.Memory
				if old, getErr := cli.GetScopedMemory(scopeName, scopeKey, key); getErr == nil {
					m := old.TrackerMemory()
					existing = &m
				}
				merged, err := tracker.ApplyMemoryPatch(existing, key, patch, time.Now())
				if err != nil {
					return err
				}
				if err := tracker.ValidateMemoryForWrite(merged); err != nil {
					return err
				}
				item, err := cli.CreateScopedMemory(scopeName, scopeKey, key, merged.Content, merged.Tags, &merged.MemoryMeta)
				if err != nil {
					return fmt.Errorf("set scoped memory: %w", err)
				}
				return printMemoryValue(c, item, asJSON)
			}
			s, err := writeStore()
			if err != nil {
				return err
			}
			note := autoScopeMemoryPaths(s, key, &patch)
			item, err := s.SetMemoryPatch(key, patch, true)
			if err != nil {
				return err
			}
			tryAutoSync(c, s)
			if err := printMemory(c, item); err != nil {
				return err
			}
			if note != "" && !asJSON {
				c.Println(note)
			}
			return nil
		}},
		{Name: "ls", Aliases: []string{"list", "memories"}, Desc: "List memories (kind, age, title, summary), or search key/title/summary/content with a keyword (case-insensitive)", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("kw", "keyword", false)
			c.VarOpt(&listTags, "tag", "", "filter tag (repeatable)")
			c.StrOpt(&listKind, "kind", "", "", "filter kind: rule|note|handoff (the legacy prime tag counts as rule)")
			c.BoolOpt(&listArchived, "archived", "", false, "list / search archived memories (memories-archive.jsonl)")
		}, Func: func(c *gcli.Command, _ []string) error {
			if listKind != "" && !tracker.ValidMemoryKind(listKind) {
				return fmt.Errorf("invalid --kind %q (rule|note|handoff)", listKind)
			}
			now := time.Now()
			if listArchived {
				s, err := localOnly()
				if err != nil {
					return err
				}
				items, err := s.ListArchivedMemories(tracker.MemoryFilter{Keyword: c.Arg("kw").String(), Tags: listTags, Kind: listKind})
				if err != nil {
					return err
				}
				if asJSON {
					return printTrackerJSON(c, items)
				}
				for _, item := range items {
					c.Print(tracker.ArchivedMemoryListLine(item, now))
				}
				return nil
			}
			if cli, scopeName, scopeKey, err := scopedClient(); scopeName != "" || err != nil {
				if err != nil {
					return err
				}
				items, err := cli.ListScopedMemories(client.ScopedMemoryListOpts{Scope: scopeName, ScopeKey: scopeKey, Keyword: c.Arg("kw").String(), Tags: listTags})
				if err != nil {
					return fmt.Errorf("list scoped memories: %w", err)
				}
				kept := items[:0]
				for _, item := range items {
					if tracker.MemoryMatchesFilter(item.TrackerMemory(), tracker.MemoryFilter{Kind: listKind}) {
						kept = append(kept, item)
					}
				}
				if asJSON {
					return printTrackerJSON(c, kept)
				}
				for _, item := range kept {
					c.Print(tracker.MemoryListLine(item.TrackerMemory(), now))
				}
				return nil
			}
			s, err := store()
			if err != nil {
				return err
			}
			items, err := s.ListMemoriesFiltered(tracker.MemoryFilter{Keyword: c.Arg("kw").String(), Tags: listTags, Kind: listKind})
			if err != nil {
				return err
			}
			if asJSON {
				return printTrackerJSON(c, items)
			}
			for _, item := range items {
				c.Print(tracker.MemoryListLine(item, now))
			}
			return nil
		}},
		{Name: "show", Aliases: []string{"recall"}, Desc: "Show one or more memories by key", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("keys", "one or more memory keys", true, true)
		}, Func: func(c *gcli.Command, _ []string) error {
			keys := c.Arg("keys").Strings()
			cli, scopeName, scopeKey, err := scopedClient()
			if err != nil {
				return err
			}
			var store *tracker.Store
			if scopeName == "" {
				if store, err = trackerStore(); err != nil {
					return err
				}
			}
			type shown struct {
				memory tracker.Memory
				raw    any
			}
			found := make([]shown, 0, len(keys))
			var missing []string
			for _, key := range keys {
				if store != nil {
					item, err := store.Memory(key)
					if err != nil {
						missing = append(missing, key)
						continue
					}
					found = append(found, shown{item, item})
					continue
				}
				item, err := cli.GetScopedMemory(scopeName, scopeKey, key)
				if err != nil {
					if len(keys) == 1 {
						return fmt.Errorf("show scoped memory: %w", err)
					}
					missing = append(missing, key)
					continue
				}
				found = append(found, shown{item.TrackerMemory(), item})
			}
			if asJSON {
				// One key keeps the single-object shape; several give an array.
				if len(keys) == 1 && len(found) == 1 {
					_ = printTrackerJSON(c, found[0].raw)
				} else {
					raws := make([]any, 0, len(found))
					for _, item := range found {
						raws = append(raws, item.raw)
					}
					_ = printTrackerJSON(c, raws)
				}
			} else {
				for i, item := range found {
					if i > 0 {
						c.Println()
					}
					c.Print(tracker.MemoryDetail(item.memory, time.Now()))
				}
			}
			if len(missing) > 0 {
				return fmt.Errorf("memory not found: %s", strings.Join(missing, ", "))
			}
			return nil
		}},
		{Name: "rm", Aliases: []string{"forget"}, Desc: "Remove a memory", Config: func(c *gcli.Command) { bind(c); c.AddArg("key", "memory key", true) }, Func: func(c *gcli.Command, _ []string) error {
			if cli, scopeName, scopeKey, err := scopedClient(); scopeName != "" || err != nil {
				if err != nil {
					return err
				}
				key := c.Arg("key").String()
				if err := cli.DeleteScopedMemory(scopeName, scopeKey, key); err != nil {
					return fmt.Errorf("remove scoped memory: %w", err)
				}
				if asJSON {
					return printTrackerJSON(c, map[string]string{"removed": key})
				}
				c.Printf("memory %s removed\n", key)
				return nil
			}
			s, err := writeStore()
			if err != nil {
				return err
			}
			key := c.Arg("key").String()
			if err := s.RemoveMemory(key); err != nil {
				return err
			}
			tryAutoSync(c, s)
			if asJSON {
				return printTrackerJSON(c, map[string]string{"removed": key})
			}
			c.Printf("memory %s removed\n", key)
			return nil
		}},
		{Name: "flag", Desc: "Report a memory as out of date (marks it 「⚠ 待复核」 where it is injected; the rule keeps its full text)", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("key", "memory key", true)
			c.StrOpt(&flagReason, "reason", "", "", "what no longer matches (required)")
		}, Func: func(c *gcli.Command, _ []string) error {
			key := c.Arg("key").String()
			jobID := strings.TrimSpace(os.Getenv("GOFER_JOB_ID"))
			flag, err := tracker.NewMemoryFlag(flagReason, trackerActor(), jobID, time.Now())
			if err != nil {
				return err
			}
			if cli, scopeName, scopeKey, err := scopedClient(); scopeName != "" || err != nil {
				if err != nil {
					return err
				}
				item, err := cli.FlagScopedMemory(scopeName, scopeKey, key, flag.Reason, jobID)
				if err != nil {
					return fmt.Errorf("flag scoped memory: %w", err)
				}
				return printMemoryValue(c, item, asJSON)
			}
			if globalScope && jobCredentialEnv() {
				// A job may flag a global memory, but only through the server (it cannot
				// push the global tracker); the local copy then pulls the flag.
				cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
				if err != nil {
					return fmt.Errorf("flagging a global memory from a job needs the server: %w", err)
				}
				item, err := cli.FlagScopedMemory("global", "", key, flag.Reason, jobID)
				if err != nil {
					return fmt.Errorf("flag global memory: %w", err)
				}
				if s, err := store(); err == nil {
					tryAutoSync(c, s)
				}
				return printMemoryValue(c, item, asJSON)
			}
			s, err := store()
			if err != nil {
				return err
			}
			item, err := s.FlagMemory(key, flag)
			if err != nil {
				return err
			}
			tryAutoSync(c, s)
			return printMemory(c, item)
		}},
		{Name: "unflag", Desc: "Clear the flags of a memory after reviewing it (rewriting the content with `memory set` clears them too)", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("key", "memory key", true)
		}, Func: func(c *gcli.Command, _ []string) error {
			key := c.Arg("key").String()
			if cli, scopeName, scopeKey, err := scopedClient(); scopeName != "" || err != nil {
				if err != nil {
					return err
				}
				item, err := cli.UnflagScopedMemory(scopeName, scopeKey, key)
				if err != nil {
					return fmt.Errorf("unflag scoped memory: %w", err)
				}
				return printMemoryValue(c, item, asJSON)
			}
			s, err := writeStore()
			if err != nil {
				return err
			}
			item, err := s.UnflagMemory(key)
			if err != nil {
				return err
			}
			tryAutoSync(c, s)
			return printMemory(c, item)
		}},
		{Name: "doctor", Desc: "Check memories for staleness: agent flags, expired handoffs, 90-day notes, missing paths / commits, missing summaries, duplicates, 30-day-old pending knowledge candidates (advisory, exit 0; --global / --project check server-scoped memories, without the path / commit checks)", Config: bind, Func: func(c *gcli.Command, _ []string) error {
			now := time.Now()
			var out memoryDoctorOutput
			cli, scopeName, scopeKey, err := scopedClient()
			if err != nil {
				return err
			}
			if scopeName != "" {
				items, err := cli.ListScopedMemories(client.ScopedMemoryListOpts{Scope: scopeName, ScopeKey: scopeKey})
				if err != nil {
					return fmt.Errorf("list scoped memories: %w", err)
				}
				out.DoctorReport = scopedDoctorReport(items, now)
				if scopeName == "project" {
					if out.StaleCandidates, err = pendingStaleCandidates(cli, scopeKey, now); err != nil {
						return fmt.Errorf("list memory candidates: %w", err)
					}
				}
			} else if globalScope {
				s, err := store()
				if err != nil {
					return err
				}
				items, err := s.ReadMemories()
				if err != nil {
					return err
				}
				// No path / commit checks: global memories belong to no repository.
				out.DoctorReport = tracker.DiagnoseMemories(items, tracker.DoctorOptions{Now: now})
			} else {
				s, err := trackerStore()
				if err != nil {
					return err
				}
				if out.DoctorReport, err = s.Doctor(now); err != nil {
					return err
				}
				// Best effort: candidates live on the server, which a repo doctor may not reach.
				if cfg, err := s.ReadConfig(); err == nil && cfg.ProjectKey != "" {
					if cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token); err == nil {
						out.StaleCandidates, _ = pendingStaleCandidates(cli, cfg.ProjectKey, now)
					}
				}
			}
			if asJSON {
				return printTrackerJSON(c, out)
			}
			c.Print(tracker.FormatDoctorReport(out.DoctorReport))
			c.Print(formatStaleCandidates(out.StaleCandidates))
			return nil
		}},
		{Name: "scope", Desc: "Suggest the applicable path of memories that have none (a workspace root holding several checkouts) and optionally write it", Config: func(c *gcli.Command) {
			bind(c)
			c.BoolOpt(&scopeSuggest, "suggest", "", false, "only list the suggestions (default)")
			c.BoolOpt(&scopeApply, "apply", "", false, "write the suggested when.paths (prints them first)")
			c.StrOpt(&scopeMinConfidence, "min-confidence", "", "high", "--apply writes suggestions at least this strong: high|medium")
			c.StrOpt(&scopeOnly, "only", "", "", "only consider the comma-separated memory keys")
		}, Func: func(c *gcli.Command, _ []string) error {
			if scopeSuggest && scopeApply {
				return fmt.Errorf("--suggest and --apply are mutually exclusive")
			}
			if scopeName, _, err := scope(); scopeName != "" || err != nil {
				if err != nil {
					return err
				}
				return fmt.Errorf("memory scope works on the repository tracker, not --global / --project")
			}
			s, err := trackerStore()
			if err != nil {
				return err
			}
			return runMemoryScope(c, s, memoryScopeOptions{Apply: scopeApply, MinConfidence: scopeMinConfidence, Only: scopeOnly}, asJSON)
		}},
		{Name: "archive", Desc: "Move a memory to memories-archive.jsonl (out of prime; `ls --archived` finds it, `restore` brings it back)", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("key", "memory key", true)
			c.StrOpt(&archiveReason, "reason", "", "", "why it is archived")
		}, Func: func(c *gcli.Command, _ []string) error {
			s, err := localOnly()
			if err == nil && globalScope {
				s, err = writeStore()
			}
			if err != nil {
				return err
			}
			item, err := s.ArchiveMemory(c.Arg("key").String(), archiveReason, trackerActor())
			if err != nil {
				return err
			}
			tryAutoSync(c, s)
			if asJSON {
				return printTrackerJSON(c, item)
			}
			c.Printf("memory %s archived\n", item.Key)
			return nil
		}},
		{Name: "restore", Desc: "Move an archived memory back into memories.jsonl", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("key", "archived memory key", true)
		}, Func: func(c *gcli.Command, _ []string) error {
			s, err := localOnly()
			if err == nil && globalScope {
				s, err = writeStore()
			}
			if err != nil {
				return err
			}
			item, err := s.RestoreMemory(c.Arg("key").String(), trackerActor())
			if err != nil {
				return err
			}
			tryAutoSync(c, s)
			return printMemory(c, item)
		}},
		{Name: "promote", Desc: "Turn a memory (typically a handoff) into a long-lived rule or note (clears the expiry, keeps source)", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("key", "memory key", true)
			c.StrOpt(&promoteKind, "kind", "", "", "rule | note (required)")
			c.StrOpt(&promoteSummary, "summary", "", "", "one-sentence summary (required for content > 200 chars without one)")
		}, Func: func(c *gcli.Command, _ []string) error {
			s, err := localOnly()
			if err == nil && globalScope {
				s, err = writeStore()
			}
			if err != nil {
				return err
			}
			item, err := s.PromoteMemory(c.Arg("key").String(), promoteKind, memoryOptString(promoteSummary), trackerActor())
			if err != nil {
				return err
			}
			tryAutoSync(c, s)
			return printMemory(c, item)
		}},
		{Name: "sync", Desc: "Sync this machine's copy of the global memories with the server (--global; a repository syncs with `gofer repo sync`)", Config: bind, Func: func(c *gcli.Command, _ []string) error {
			if !globalScope {
				return fmt.Errorf("memory sync works on the global memories (--global); sync a repository tracker with `gofer repo sync`")
			}
			s, err := store()
			if err != nil {
				return err
			}
			report, err := syncGlobalMemories(s, 20*time.Second)
			if err != nil {
				return err
			}
			if asJSON {
				return printTrackerJSON(c, report)
			}
			items, _ := s.ReadMemories()
			line := fmt.Sprintf("global memories synced: %d on this machine (%s)", len(items), s.Dir)
			if strings.TrimSpace(report.Summary) != "" {
				line += "; " + report.Summary
			}
			c.Println(line)
			return nil
		}},
	}})
}

func printMemoryValue(c *gcli.Command, item client.ScopedMemory, asJSON bool) error {
	if asJSON {
		return printTrackerJSON(c, item)
	}
	c.Print(tracker.MemoryDetail(item.TrackerMemory(), time.Now()))
	return nil
}

// autoScopeMemoryPaths narrows a `memory set` to the nested repository the process cwd
// sits in (gofer-opv5): a workspace root holding several checkouts (each its own git
// repository) shares one tracker, and without a scope the memories of one checkout are
// injected into the jobs of every other one. It fills patch.WhenPaths only for a NEW
// memory and only when cwd is inside a nested checkout; an update keeps the stored
// `when`, and an explicit --when-paths (an empty value to clear, or '**') always wins.
// The returned line is the CLI note shown after the memory ("" = nothing).
func autoScopeMemoryPaths(s *tracker.Store, key string, patch *tracker.MemoryPatch) string {
	if patch.WhenPaths != nil {
		return ""
	}
	if _, err := s.Memory(key); err == nil {
		return "" // an update keeps the stored when
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	scope, ok := s.SubrepoScope(cwd)
	if !ok {
		return ""
	}
	paths := []string{scope}
	patch.WhenPaths = &paths
	return fmt.Sprintf("适用路径已设为 %s（整仓通用用 --when-paths '**'，工作区通用请写成项目记忆 --project）", scope)
}
