package commands

import (
	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/tracker"
)

func NewMemoryCmd() *gcli.Command {
	var trackerPath, setTags string
	var listTags gcli.Strings
	var asJSON bool
	bind := func(c *gcli.Command) {
		bindConfigFlag(c)
		c.StrOpt(&trackerPath, "tracker", "", "", "explicit .gofer/tracker directory")
		c.BoolOpt(&asJSON, "json", "", false, "print JSON")
	}
	store := func() (*tracker.Store, error) { return tracker.Discover(".", trackerPath) }
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
		{Name: "ls", Aliases: []string{"list"}, Desc: "List memories", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("kw", "keyword", false)
			c.VarOpt(&listTags, "tag", "", "filter tag (repeatable)")
		}, Func: func(c *gcli.Command, _ []string) error {
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
		{Name: "show", Desc: "Show a memory", Config: func(c *gcli.Command) { bind(c); c.AddArg("key", "memory key", true) }, Func: func(c *gcli.Command, _ []string) error {
			s, err := store()
			if err != nil {
				return err
			}
			item, err := s.Memory(c.Arg("key").String())
			if err != nil {
				return err
			}
			return printMemory(c, item)
		}},
		{Name: "rm", Aliases: []string{"forget"}, Desc: "Remove a memory", Config: func(c *gcli.Command) { bind(c); c.AddArg("key", "memory key", true) }, Func: func(c *gcli.Command, _ []string) error {
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
