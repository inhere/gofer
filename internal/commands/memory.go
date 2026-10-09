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
// value"; "-" clears summary / source / when-* fields.
type memorySetFlags struct {
	tags, tagsAlias, summary, kind, ttl, source string
	whenKeywords, whenPaths, whenCommands       string
	doctorIgnore                                string
}

// memorySourceFromEnv is the default `source` of a memory written by the CLI:
// the gofer job it runs in, else the gofer session (design §2.10).
func memorySourceFromEnv() string {
	if id := strings.TrimSpace(os.Getenv("GOFER_JOB_ID")); id != "" {
		return "job:" + id
	}
	if id := strings.TrimSpace(os.Getenv("GOFER_SESSION_ID")); id != "" {
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

func (f memorySetFlags) patch(content string) (tracker.MemoryPatch, error) {
	patch := tracker.MemoryPatch{Content: content, By: trackerActor(), Summary: memoryOptString(f.summary), Source: memoryOptString(f.source),
		WhenKeywords: memoryOptList(f.whenKeywords), WhenPaths: memoryOptList(f.whenPaths), WhenCommands: memoryOptList(f.whenCommands),
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
	var listKind, archiveReason, promoteKind, promoteSummary string
	var asJSON, globalScope, listArchived bool
	var projectScope string
	bind := func(c *gcli.Command) {
		bindConfigFlag(c)
		bindServerFlags(c)
		c.StrOpt(&trackerPath, "tracker", "", "", "explicit .gofer/tracker directory")
		c.BoolOpt(&asJSON, "json", "", false, "print JSON")
		c.BoolOpt(&globalScope, "global", "", false, "use server global memory scope")
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
		if scopeName == "" {
			return nil, "", "", nil
		}
		cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
		if err != nil {
			return nil, "", "", fmt.Errorf("scoped memory requires a reachable server: %w", err)
		}
		return cli, scopeName, scopeKey, nil
	}
	trackerStore := func() (*tracker.Store, error) { return tracker.Discover(".", trackerPath) }
	store := trackerStore
	// localOnly is the store for the repository-only subcommands (doctor,
	// archive, restore, promote): they have no server-scope counterpart.
	localOnly := func() (*tracker.Store, error) {
		if scopeName, _, err := scope(); err != nil || scopeName != "" {
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("this subcommand only works on the repository tracker (no --global / --project)")
		}
		return trackerStore()
	}
	printMemory := func(c *gcli.Command, item tracker.Memory) error {
		if asJSON {
			return printTrackerJSON(c, item)
		}
		c.Print(tracker.MemoryDetail(item, time.Now()))
		return nil
	}
	return &gcli.Command{Name: "memory", Desc: "Manage repository-local memories", Subs: []*gcli.Command{
		{Name: "set", Aliases: []string{"remember"}, Desc: "Set a memory (updating keeps every field you do not pass; \"-\" clears summary/source/when-*)", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("key", "memory key", true)
			c.AddArg("content", "memory content (full text)", true)
			c.StrOpt(&setFlags.tags, "tag", "", "", "comma-separated tags (the first tag groups the prime index)")
			c.StrOpt(&setFlags.tagsAlias, "tags", "", "", "alias of --tag")
			c.StrOpt(&setFlags.summary, "summary", "", "", "one sentence (<= 80 chars): what it covers and when to read it; required for rule/note content > 200 chars")
			c.StrOpt(&setFlags.kind, "kind", "", "", "rule (long-lived convention, full text in prime) | note (default, index line) | handoff (short-lived, expires)")
			c.StrOpt(&setFlags.ttl, "ttl", "", "", "handoff lifetime, e.g. 14d (default), 2w, 36h")
			c.StrOpt(&setFlags.whenKeywords, "when-keywords", "", "", "comma-separated prompt keywords that make it relevant")
			c.StrOpt(&setFlags.whenPaths, "when-paths", "", "", "comma-separated repo path globs, e.g. 'web/**,internal/tunnel/**'")
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
			s, err := store()
			if err != nil {
				return err
			}
			item, err := s.SetMemoryPatch(key, patch, true)
			if err != nil {
				return err
			}
			tryAutoSync(c, s)
			return printMemory(c, item)
		}},
		{Name: "ls", Aliases: []string{"list", "memories"}, Desc: "List memories (kind, age, summary), or search key/summary/content with a keyword (case-insensitive)", Config: func(c *gcli.Command) {
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
			s, err := store()
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
		{Name: "doctor", Desc: "Check memories for staleness: expired handoffs, 90-day notes, missing paths / commits, missing summaries, duplicates (advisory, exit 0)", Config: bind, Func: func(c *gcli.Command, _ []string) error {
			s, err := localOnly()
			if err != nil {
				return err
			}
			report, err := s.Doctor(time.Now())
			if err != nil {
				return err
			}
			if asJSON {
				return printTrackerJSON(c, report)
			}
			c.Print(tracker.FormatDoctorReport(report))
			return nil
		}},
		{Name: "archive", Desc: "Move a memory to memories-archive.jsonl (out of prime; `ls --archived` finds it, `restore` brings it back)", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("key", "memory key", true)
			c.StrOpt(&archiveReason, "reason", "", "", "why it is archived")
		}, Func: func(c *gcli.Command, _ []string) error {
			s, err := localOnly()
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
	}}
}

func printMemoryValue(c *gcli.Command, item client.ScopedMemory, asJSON bool) error {
	if asJSON {
		return printTrackerJSON(c, item)
	}
	c.Print(tracker.MemoryDetail(item.TrackerMemory(), time.Now()))
	return nil
}
