package commands

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"

	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/procattr"
	"github.com/inhere/gofer/internal/tracker"
)

func trackerActor() string {
	if value := strings.TrimSpace(os.Getenv("GOFER_CALLER")); value != "" {
		return value
	}
	gitCmd := exec.Command("git", "config", "user.name")
	procattr.Background(gitCmd)
	if value, err := gitCmd.Output(); err == nil && strings.TrimSpace(string(value)) != "" {
		return strings.TrimSpace(string(value))
	}
	if current, err := user.Current(); err == nil && current.Username != "" {
		return current.Username
	}
	return "unknown"
}

// issueFlags holds every option the issue subcommands bind. One struct keeps the
// create/update/ls flag sets (and the bd-style -l/--label aliases) identical.
type issueFlags struct {
	trackerPath string
	asJSON      bool

	title, typ, parent, description, design, acceptance, assignee, owner string
	priority, createPriority, listPriority                               int
	tags, labels                                                         string // comma lists (create/update)
	listTags, listLabels                                                 gcli.Strings
	deps                                                                 gcli.Strings

	status, query, appendNotes, closeReason, reopenReason string
	untag, clear, depType, rmDepType, sortBy              string
	all, claim, reverse                                   bool
	limit                                                 int
}

// mergedTags joins the --tag and -l/--label comma lists.
func (f *issueFlags) mergedTags() []string {
	return tracker.ParseTags(f.tags + "," + f.labels)
}

// mergedListTags joins the repeatable --tag and -l/--label filters.
func (f *issueFlags) mergedListTags() []string {
	out := make([]string, 0, len(f.listTags)+len(f.listLabels))
	for _, group := range []gcli.Strings{f.listTags, f.listLabels} {
		for _, value := range group {
			out = append(out, tracker.ParseTags(value)...)
		}
	}
	return out
}

func NewIssueCmd() *gcli.Command {
	var f issueFlags
	bind := func(c *gcli.Command) {
		bindConfigFlag(c)
		c.StrOpt(&f.trackerPath, "tracker", "", "", "explicit .gofer/tracker directory")
		c.BoolOpt(&f.asJSON, "json", "", false, "print JSON")
	}
	// bindFields binds the field options create and update share.
	bindFields := func(c *gcli.Command, priority *int, defaultPriority int) {
		c.StrOpt(&f.typ, "type", "", "", "issue type (task|bug|feature|epic|chore|decision, free-form)")
		c.IntOpt(priority, "priority", "p", defaultPriority, "priority 0 (highest)..4")
		c.StrOpt(&f.parent, "parent", "", "", "parent issue id")
		c.StrOpt(&f.description, "description", "d", "", "description")
		c.StrOpt(&f.design, "design", "", "", "design text")
		c.StrOpt(&f.acceptance, "acceptance", "", "", "acceptance criteria")
		c.StrOpt(&f.assignee, "assignee", "a", "", "assignee")
		c.StrOpt(&f.owner, "owner", "", "", "owner")
		c.StrOpt(&f.tags, "tag", "", "", "comma-separated tags (added)")
		c.StrOpt(&f.labels, "label", "l", "", "bd-style alias of --tag")
	}
	store := func() (*tracker.Store, error) { return tracker.Discover(".", f.trackerPath) }
	printIssue := func(c *gcli.Command, item tracker.Issue) error {
		if f.asJSON {
			return printTrackerJSON(c, item)
		}
		c.Print(formatIssueBrief(item))
		return nil
	}
	printIssues := func(c *gcli.Command, items []tracker.Issue) error {
		if f.asJSON {
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
		{Name: "ls", Aliases: []string{"list"}, Desc: "List issues (closed hidden unless --all or --status)", Config: func(c *gcli.Command) {
			bind(c)
			c.StrOpt(&f.status, "status", "", "", "filter status (open|in_progress|blocked|closed)")
			c.StrOpt(&f.typ, "type", "", "", "filter issue type")
			c.VarOpt(&f.listTags, "tag", "", "filter tag (repeatable or comma-separated, all required)")
			c.VarOpt(&f.listLabels, "label", "l", "bd-style alias of --tag")
			c.StrOpt(&f.assignee, "assignee", "a", "", "filter assignee")
			c.IntOpt(&f.listPriority, "priority", "p", -1, "filter priority 0..4")
			c.StrOpt(&f.query, "query", "q", "", "search title and description")
			c.StrOpt(&f.sortBy, "sort", "", "id", "sort by id|priority|created|updated (updated = newest first)")
			c.BoolOpt(&f.reverse, "reverse", "r", false, "reverse the sort order")
			c.IntOpt(&f.limit, "limit", "n", 0, "show at most N issues (0 = all)")
			c.BoolOpt(&f.all, "all", "", false, "include closed issues")
		}, Func: func(c *gcli.Command, _ []string) error {
			s, err := store()
			if err != nil {
				return err
			}
			filter := tracker.IssueFilter{Status: f.status, Type: f.typ, Tags: f.mergedListTags(), Assignee: f.assignee, Query: f.query, All: f.all, Sort: f.sortBy, Reverse: f.reverse, Limit: f.limit}
			if f.listPriority >= 0 {
				p := f.listPriority
				filter.Priority = &p
			}
			items, err := s.ListIssues(filter)
			if err != nil {
				return err
			}
			return printIssues(c, items)
		}},
		{Name: "show", Desc: "Show one or more issues with notes, comments, parent/children and dependencies", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("ids", "one or more issue ids", true, true)
		}, Func: func(c *gcli.Command, _ []string) error {
			s, err := store()
			if err != nil {
				return err
			}
			ids := c.Arg("ids").Strings()
			details := make([]issueDetail, 0, len(ids))
			for _, id := range ids {
				item, rel, err := s.Relations(id)
				if err != nil {
					return err
				}
				details = append(details, issueDetail{Issue: item, Relations: rel})
			}
			if f.asJSON {
				if len(details) == 1 {
					return printTrackerJSON(c, details[0])
				}
				return printTrackerJSON(c, details)
			}
			for i, d := range details {
				if i > 0 {
					c.Println("")
				}
				c.Print(formatIssueDetail(d))
			}
			return nil
		}},
		{Name: "create", Desc: "Create an issue (title as the argument or --title)", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("title", "issue title (alternative to --title)", false)
			c.StrOpt(&f.title, "title", "t", "", "issue title")
			bindFields(c, &f.createPriority, 2)
			c.VarOpt(&f.deps, "dep", "", "blocking dependency id (repeatable)")
		}, Func: func(c *gcli.Command, _ []string) error {
			title := f.title
			if title == "" {
				title = c.Arg("title").String()
			}
			s, err := store()
			if err != nil {
				return err
			}
			typ := f.typ
			if typ == "" {
				typ = "task"
			}
			item := tracker.Issue{Title: title, Type: typ, Priority: f.createPriority, Parent: f.parent, Description: f.description, Design: f.design, AcceptanceCriteria: f.acceptance, Assignee: f.assignee, Owner: f.owner, Tags: f.mergedTags(), CreatedBy: trackerActor()}
			for _, id := range f.deps {
				item.Deps = append(item.Deps, tracker.Dep{ID: id, Type: "blocks"})
			}
			item, err = s.CreateIssue(item)
			if err != nil {
				return err
			}
			tryAutoSync(c, s)
			return printIssue(c, item)
		}},
		{Name: "update", Desc: "Update issue fields (only the flags you pass change; --clear empties a field)", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("ids", "one or more issue ids (the same patch applies to each)", true, true)
			c.BoolOpt(&f.claim, "claim", "", false, "claim and start the issue")
			c.StrOpt(&f.status, "status", "", "", "new status (open|in_progress|blocked|closed)")
			c.StrOpt(&f.title, "title", "t", "", "new title")
			bindFields(c, &f.priority, -1)
			c.StrOpt(&f.clear, "clear", "", "", "comma-separated fields to empty: "+strings.Join(tracker.ClearableFields, ","))
			c.StrOpt(&f.appendNotes, "append-notes", "", "", "append one note")
			c.StrOpt(&f.untag, "untag", "", "", "remove comma-separated tags")
		}, Func: func(c *gcli.Command, _ []string) error {
			if f.claim && f.status != "" && f.status != "in_progress" {
				return fmt.Errorf("--claim conflicts with --status %s", f.status)
			}
			s, err := store()
			if err != nil {
				return err
			}
			patch := tracker.IssuePatch{Title: f.title, Status: f.status, Type: f.typ, Claim: f.claim, AppendNotes: f.appendNotes, Actor: trackerActor(), Tags: f.mergedTags(), Untag: tracker.ParseTags(f.untag), Clear: tracker.ParseTags(f.clear)}
			if f.priority >= 0 {
				p := f.priority
				patch.Priority = &p
			}
			for _, field := range []struct {
				value string
				dst   **string
			}{{f.description, &patch.Description}, {f.design, &patch.Design}, {f.acceptance, &patch.Acceptance}, {f.assignee, &patch.Assignee}, {f.owner, &patch.Owner}, {f.parent, &patch.Parent}} {
				if field.value != "" {
					v := field.value
					*field.dst = &v
				}
			}
			return applyEach(c, s, c.Arg("ids").Strings(), f.asJSON, func(id string) (tracker.Issue, error) {
				return s.UpdateIssue(id, patch)
			})
		}},
		{Name: "comment", Desc: "Append a comment to an issue", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("id", "issue id", true)
			c.AddArg("text", "comment text (several words are joined with spaces)", true, true)
		}, Func: func(c *gcli.Command, _ []string) error {
			s, err := store()
			if err != nil {
				return err
			}
			item, err := s.AddComment(c.Arg("id").String(), strings.Join(c.Arg("text").Strings(), " "), trackerActor())
			if err != nil {
				return err
			}
			tryAutoSync(c, s)
			return printIssue(c, item)
		}},
		{Name: "close", Desc: "Close one or more issues (same --reason for each)", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("ids", "one or more issue ids", true, true)
			c.StrOpt(&f.closeReason, "reason", "", "", "close reason")
		}, Func: func(c *gcli.Command, _ []string) error {
			s, err := store()
			if err != nil {
				return err
			}
			return applyEach(c, s, c.Arg("ids").Strings(), f.asJSON, func(id string) (tracker.Issue, error) {
				return s.CloseIssue(id, f.closeReason)
			})
		}},
		{Name: "reopen", Desc: "Reopen a closed issue (clears closed_at and close_reason)", Config: func(c *gcli.Command) {
			bind(c)
			c.AddArg("id", "issue id", true)
			c.StrOpt(&f.reopenReason, "reason", "", "", "why it is reopened (recorded as a comment)")
		}, Func: func(c *gcli.Command, _ []string) error {
			s, err := store()
			if err != nil {
				return err
			}
			item, err := s.ReopenIssue(c.Arg("id").String(), f.reopenReason, trackerActor())
			if err != nil {
				return err
			}
			tryAutoSync(c, s)
			return printIssue(c, item)
		}},
		{Name: "dep", Desc: "Manage issue dependencies", Subs: []*gcli.Command{
			{Name: "add", Desc: "Make an issue depend on another (default kind: blocks)", Config: func(c *gcli.Command) {
				bind(c)
				c.AddArg("id", "issue id", true)
				c.AddArg("on", "issue it depends on", true)
				c.StrOpt(&f.depType, "type", "", "blocks", "kind: "+strings.Join(tracker.DepTypes, "|")+" (only blocks gates ready)")
			}, Func: func(c *gcli.Command, _ []string) error {
				s, err := store()
				if err != nil {
					return err
				}
				item, err := s.AddDepType(c.Arg("id").String(), c.Arg("on").String(), f.depType)
				if err != nil {
					return err
				}
				tryAutoSync(c, s)
				return printIssue(c, item)
			}},
			{Name: "rm", Aliases: []string{"remove"}, Desc: "Remove a dependency", Config: func(c *gcli.Command) {
				bind(c)
				c.AddArg("id", "issue id", true)
				c.AddArg("on", "issue it depends on", true)
				c.StrOpt(&f.rmDepType, "type", "", "", "only remove this kind (default: every kind)")
			}, Func: func(c *gcli.Command, _ []string) error {
				s, err := store()
				if err != nil {
					return err
				}
				item, err := s.RemoveDep(c.Arg("id").String(), c.Arg("on").String(), f.rmDepType)
				if err != nil {
					return err
				}
				tryAutoSync(c, s)
				return printIssue(c, item)
			}},
			{Name: "ls", Aliases: []string{"list"}, Desc: "Show what an issue waits on and what waits on it", Config: func(c *gcli.Command) {
				bind(c)
				c.AddArg("id", "issue id", true)
			}, Func: func(c *gcli.Command, _ []string) error {
				s, err := store()
				if err != nil {
					return err
				}
				_, rel, err := s.Relations(c.Arg("id").String())
				if err != nil {
					return err
				}
				if f.asJSON {
					return printTrackerJSON(c, rel)
				}
				c.Print(formatRelations(rel))
				return nil
			}},
		}},
	}}
}

// applyEach runs op on every id, keeps going past failures, syncs once and
// prints each success (JSON: one object for a single id, an array otherwise).
// Any failure makes the command fail after the successful ids were applied.
func applyEach(c *gcli.Command, s *tracker.Store, ids []string, asJSON bool, op func(id string) (tracker.Issue, error)) error {
	done := make([]tracker.Issue, 0, len(ids))
	var failed []string
	for _, id := range ids {
		item, err := op(id)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		done = append(done, item)
	}
	if len(done) > 0 {
		tryAutoSync(c, s)
		switch {
		case asJSON && len(ids) == 1:
			if err := printTrackerJSON(c, done[0]); err != nil {
				return err
			}
		case asJSON:
			if err := printTrackerJSON(c, done); err != nil {
				return err
			}
		default:
			for _, item := range done {
				c.Print(formatIssueBrief(item))
			}
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d of %d failed: %s", len(failed), len(ids), strings.Join(failed, "; "))
	}
	return nil
}

// issueDetail is `issue show --json`: the issue plus its dependency neighbourhood.
type issueDetail struct {
	tracker.Issue
	Relations tracker.Relations `json:"relations"`
}

func formatIssueBrief(item tracker.Issue) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%s] P%d %s\n", item.ID, item.Status, item.Priority, item.Title)
	if item.Assignee != "" {
		fmt.Fprintf(&b, "assignee: %s\n", item.Assignee)
	}
	return b.String()
}

func formatIssueDetail(d issueDetail) string {
	item := d.Issue
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%s] P%d %s %s\n", item.ID, item.Status, item.Priority, item.Type, item.Title)
	line := func(label, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%s: %s\n", label, value)
		}
	}
	line("assignee", item.Assignee)
	line("owner", item.Owner)
	line("tags", strings.Join(item.Tags, ", "))
	line("external_ref", item.ExternalRef)
	line("spec_id", item.SpecID)
	line("created", joinNonEmpty(item.CreatedAt, "by "+item.CreatedBy))
	line("updated", item.UpdatedAt)
	line("started", item.StartedAt)
	line("closed", item.ClosedAt)
	line("close_reason", item.CloseReason)
	b.WriteString(formatRelations(d.Relations))
	block := func(label, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%s:\n%s\n", label, indent(value))
		}
	}
	block("description", item.Description)
	block("design", item.Design)
	block("acceptance", item.AcceptanceCriteria)
	if len(item.Notes) > 0 {
		b.WriteString("notes:\n")
		for _, n := range item.Notes {
			fmt.Fprintf(&b, "  - %s (%s)\n%s\n", n.At, n.By, indent2(n.Text))
		}
	}
	if len(item.Comments) > 0 {
		b.WriteString("comments:\n")
		for _, cm := range item.Comments {
			fmt.Fprintf(&b, "  - %s (%s)\n%s\n", cm.At, cm.By, indent2(cm.Text))
		}
	}
	return b.String()
}

func formatRelations(rel tracker.Relations) string {
	var b strings.Builder
	list := func(label string, ids []string) {
		if len(ids) > 0 {
			fmt.Fprintf(&b, "%s: %s\n", label, strings.Join(ids, ", "))
		}
	}
	depList := func(deps []tracker.Dep) []string {
		out := make([]string, 0, len(deps))
		for _, d := range deps {
			out = append(out, d.ID+" ("+d.Type+")")
		}
		return out
	}
	if rel.Parent != "" {
		fmt.Fprintf(&b, "parent: %s\n", rel.Parent)
	}
	list("children", rel.Children)
	list("blocked-by", depIDs(rel.BlockedBy))
	list("blocks", rel.Blocks)
	list("depends-on", depList(rel.DependsOn))
	list("linked-from", depList(rel.Linked))
	return b.String()
}

func depIDs(deps []tracker.Dep) []string {
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		out = append(out, d.ID)
	}
	return out
}

func joinNonEmpty(parts ...string) string {
	kept := parts[:0:0]
	for _, p := range parts {
		if p != "" && p != "by " {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " ")
}

func indent(text string) string  { return prefixLines(text, "  ") }
func indent2(text string) string { return prefixLines(text, "    ") }

func prefixLines(text, prefix string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
