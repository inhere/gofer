package commands

import (
	"fmt"
	"strings"

	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/tracker"
)

func NewMemoryCmd() *gcli.Command {
	var trackerPath, setTags string
	var listTags gcli.Strings
	var asJSON, globalScope bool
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
	printMemory := func(c *gcli.Command, item tracker.Memory) error {
		if asJSON {
			return printTrackerJSON(c, item)
		}
		c.Printf("%s: %s\n", item.Key, item.Content)
		return nil
	}
	return &gcli.Command{Name: "memory", Desc: "Manage repository-local memories", Subs: []*gcli.Command{
		{Name: "set", Aliases: []string{"remember"}, Desc: "Set a memory", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("key", "memory key", true)
			c.AddArg("content", "memory content", true)
			c.StrOpt(&setTags, "tag", "", "", "comma-separated tags")
		}, Func: func(c *gcli.Command, _ []string) error {
			if cli, scopeName, scopeKey, err := scopedClient(); scopeName != "" || err != nil {
				if err != nil {
					return err
				}
				item, err := cli.CreateScopedMemory(scopeName, scopeKey, c.Arg("key").String(), c.Arg("content").String(), tracker.ParseTags(setTags))
				if err != nil {
					return fmt.Errorf("set scoped memory: %w", err)
				}
				return printMemoryValue(c, item, asJSON)
			}
			s, err := store()
			if err != nil {
				return err
			}
			item, err := s.SetMemory(c.Arg("key").String(), c.Arg("content").String(), trackerActor(), tracker.ParseTags(setTags)...)
			if err != nil {
				return err
			}
			tryAutoSync(c, s)
			return printMemory(c, item)
		}},
		{Name: "ls", Aliases: []string{"list", "memories"}, Desc: "List memories, or search key and content with a keyword (case-insensitive)", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("kw", "keyword", false)
			c.VarOpt(&listTags, "tag", "", "filter tag (repeatable)")
		}, Func: func(c *gcli.Command, _ []string) error {
			if cli, scopeName, scopeKey, err := scopedClient(); scopeName != "" || err != nil {
				if err != nil {
					return err
				}
				items, err := cli.ListScopedMemories(client.ScopedMemoryListOpts{Scope: scopeName, ScopeKey: scopeKey, Keyword: c.Arg("kw").String(), Tags: listTags})
				if err != nil {
					return fmt.Errorf("list scoped memories: %w", err)
				}
				if asJSON {
					return printTrackerJSON(c, items)
				}
				for _, item := range items {
					c.Printf("%s: %s\n", item.Key, item.Content)
				}
				return nil
			}
			s, err := store()
			if err != nil {
				return err
			}
			items, err := s.ListMemories(c.Arg("kw").String(), listTags...)
			if err != nil {
				return err
			}
			if asJSON {
				return printTrackerJSON(c, items)
			}
			for _, item := range items {
				c.Printf("%s: %s\n", item.Key, item.Content)
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
				key, content string
				raw          any
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
					found = append(found, shown{item.Key, item.Content, item})
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
				found = append(found, shown{item.Key, item.Content, item})
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
				for _, item := range found {
					c.Printf("%s: %s\n", item.key, item.content)
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
	}}
}

func printMemoryValue(c *gcli.Command, item client.ScopedMemory, asJSON bool) error {
	if asJSON {
		return printTrackerJSON(c, item)
	}
	c.Printf("%s: %s\n", item.Key, item.Content)
	return nil
}
