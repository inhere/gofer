package commands

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"

	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/tracker"
)

func trackerActor() string {
	if value := strings.TrimSpace(os.Getenv("GOFER_CALLER")); value != "" {
		return value
	}
	if value, err := exec.Command("git", "config", "user.name").Output(); err == nil && strings.TrimSpace(string(value)) != "" {
		return strings.TrimSpace(string(value))
	}
	if current, err := user.Current(); err == nil && current.Username != "" {
		return current.Username
	}
	return "unknown"
}

func NewIssueCmd() *gcli.Command {
	var trackerPath, createTitle, createType, createParent, createDescription, createOwner string
	var listStatus, listType, listLabel, updateStatus, updateTitle, appendNotes, closeReason string
	var createPriority int
	var createDeps gcli.Strings
	var all, claim, asJSON bool
	bind := func(c *gcli.Command) {
		bindConfigFlag(c)
		c.StrOpt(&trackerPath, "tracker", "", "", "explicit .gofer/tracker directory")
		c.BoolOpt(&asJSON, "json", "", false, "print JSON")
	}
	store := func() (*tracker.Store, error) { return tracker.Discover(".", trackerPath) }
	printIssue := func(c *gcli.Command, item tracker.Issue) error {
		if asJSON {
			return printTrackerJSON(c, item)
		}
		c.Printf("%s [%s] P%d %s\n", item.ID, item.Status, item.Priority, item.Title)
		if item.Assignee != "" {
			c.Printf("assignee: %s\n", item.Assignee)
		}
		if item.Description != "" {
			c.Printf("description: %s\n", item.Description)
		}
		for _, note := range item.Notes {
			c.Printf("note: %s (%s) %s\n", note.Text, note.By, note.At)
		}
		return nil
	}
	printIssues := func(c *gcli.Command, items []tracker.Issue) error {
		if asJSON {
			return printTrackerJSON(c, items)
		}
		for _, item := range items {
			c.Printf("%s [%s] P%d %s\n", item.ID, item.Status, item.Priority, item.Title)
		}
		return nil
	}
	return &gcli.Command{Name: "issue", Desc: "Manage repository-local issues", Subs: []*gcli.Command{
		{Name: "ready", Desc: "List open unblocked issues", Config: bind, Func: func(c *gcli.Command, _ []string) error {
			s, err := store()
			if err != nil {
				return err
			}
			items, err := s.Ready()
			if err != nil {
				return err
			}
			return printIssues(c, items)
		}},
		{Name: "ls", Aliases: []string{"list"}, Desc: "List issues", Config: func(c *gcli.Command) {
			bind(c)
			c.StrOpt(&listStatus, "status", "", "", "filter status")
			c.StrOpt(&listType, "type", "", "", "filter issue type")
			c.StrOpt(&listLabel, "label", "", "", "filter label")
			c.BoolOpt(&all, "all", "", false, "include closed issues")
		}, Func: func(c *gcli.Command, _ []string) error {
			s, err := store()
			if err != nil {
				return err
			}
			items, err := s.ListIssues(tracker.IssueFilter{Status: listStatus, Type: listType, Label: listLabel, All: all})
			if err != nil {
				return err
			}
			return printIssues(c, items)
		}},
		{Name: "show", Desc: "Show an issue", Config: func(c *gcli.Command) { bind(c); c.AddArg("id", "issue id", true) }, Func: func(c *gcli.Command, _ []string) error {
			s, err := store()
			if err != nil {
				return err
			}
			item, err := s.Issue(c.Arg("id").String())
			if err != nil {
				return err
			}
			return printIssue(c, item)
		}},
		{Name: "create", Desc: "Create an issue", Config: func(c *gcli.Command) {
			bind(c)
			c.StrOpt(&createTitle, "title", "t", "", "issue title")
			c.StrOpt(&createType, "type", "", "task", "issue type")
			c.IntOpt(&createPriority, "priority", "", 2, "priority 0..4")
			c.StrOpt(&createParent, "parent", "", "", "parent issue id")
			c.StrOpt(&createDescription, "description", "", "", "description")
			c.StrOpt(&createOwner, "owner", "", "", "owner")
			c.VarOpt(&createDeps, "dep", "", "blocking dependency id (repeatable)")
		}, Func: func(c *gcli.Command, _ []string) error {
			s, err := store()
			if err != nil {
				return err
			}
			item := tracker.Issue{Title: createTitle, Type: createType, Priority: createPriority, Parent: createParent, Description: createDescription, Owner: createOwner, CreatedBy: trackerActor()}
			for _, id := range createDeps {
				item.Deps = append(item.Deps, tracker.Dep{ID: id, Type: "blocks"})
			}
			item, err = s.CreateIssue(item)
			if err != nil {
				return err
			}
			return printIssue(c, item)
		}},
		{Name: "update", Desc: "Update an issue", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("id", "issue id", true)
			c.BoolOpt(&claim, "claim", "", false, "claim and start the issue")
			c.StrOpt(&updateStatus, "status", "", "", "new status")
			c.StrOpt(&updateTitle, "title", "", "", "new title")
			c.StrOpt(&appendNotes, "append-notes", "", "", "append one note")
		}, Func: func(c *gcli.Command, _ []string) error {
			if claim && updateStatus != "" && updateStatus != "in_progress" {
				return fmt.Errorf("--claim conflicts with --status %s", updateStatus)
			}
			s, err := store()
			if err != nil {
				return err
			}
			item, err := s.UpdateIssue(c.Arg("id").String(), tracker.IssuePatch{Title: updateTitle, Status: updateStatus, Claim: claim, AppendNotes: appendNotes, Actor: trackerActor()})
			if err != nil {
				return err
			}
			return printIssue(c, item)
		}},
		{Name: "close", Desc: "Close an issue", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("id", "issue id", true)
			c.StrOpt(&closeReason, "reason", "", "", "close reason")
		}, Func: func(c *gcli.Command, _ []string) error {
			s, err := store()
			if err != nil {
				return err
			}
			item, err := s.CloseIssue(c.Arg("id").String(), closeReason)
			if err != nil {
				return err
			}
			return printIssue(c, item)
		}},
		{Name: "dep", Desc: "Manage issue dependencies", Subs: []*gcli.Command{
			{Name: "add", Desc: "Make an issue wait for another", Config: func(c *gcli.Command) {
				bind(c)
				c.AddArg("id", "issue id", true)
				c.AddArg("on", "blocking issue id", true)
			}, Func: func(c *gcli.Command, _ []string) error {
				s, err := store()
				if err != nil {
					return err
				}
				item, err := s.AddDep(c.Arg("id").String(), c.Arg("on").String())
				if err != nil {
					return err
				}
				return printIssue(c, item)
			}},
		}},
	}}
}
