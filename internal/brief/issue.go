package brief

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/tracker"
)

const (
	// commentsMax is how many recent comments the issue section shows.
	commentsMax = 5
	// commentLines is how many lines of an ordinary comment the issue section shows.
	commentLines = 6
	// planCommentLines bounds the plan-like comment shown in full.
	planCommentLines = 60
	// jobScanLimit is how many recent jobs are scanned for issue_id.
	jobScanLimit = 200
	// planScanMax caps the open plans fetched in full to find the issue in todos.
	planScanMax = 8
)

// IssueBrief assembles the brief of one repository issue (design §一, sections in
// order: issue → 上下文树 → 设计稿 → 相关提交 → 相关 job / plan → 适用记忆 → 接手提示).
func IssueBrief(id string, opts Options) (Brief, error) {
	if opts.Store == nil {
		return Brief{}, errors.New("issue brief needs the repository tracker")
	}
	issues, err := opts.Store.ReadIssues()
	if err != nil {
		return Brief{}, err
	}
	byID := make(map[string]tracker.Issue, len(issues))
	for _, it := range issues {
		byID[it.ID] = it
	}
	item, ok := byID[id]
	if !ok {
		return Brief{}, fmt.Errorf("issue %s not found", id)
	}
	_, rel, err := opts.Store.Relations(id)
	if err != nil {
		return Brief{}, err
	}
	root := opts.root()
	lang := opts.lang()

	siblingReasons := map[string]string{}
	if item.Parent != "" {
		for _, it := range issues {
			if it.Parent == item.Parent && it.ID != id && it.Status == "closed" && it.CloseReason != "" {
				siblingReasons[it.ID] = it.CloseReason
			}
		}
	}
	commits, isGit := relatedCommits(lang, root, id, item.Parent, siblingReasons)
	fromCommits := codeEntries(root, commits)
	mentioned := mentionedFiles(root, item)
	files := mergeEntries(mentioned, fromCommits)

	b := Brief{Kind: "issue", ID: id, Lang: lang}
	b.Sections = append(b.Sections,
		issueSection(lang, item),
		treeSection(lang, item, rel, issues, byID),
		designSection(lang, root, item),
		commitSection(lang, id, item.Parent, commits, mentioned, fromCommits, keySymbols(root, commits, files), isGit),
		workSection(opts, id),
		verifySection(lang, root, verifyRules(opts.Store), files),
		memorySection(opts, newMemoryTarget(item, files)),
		hintSection(lang, opts.Store, id),
	)
	b.Sections = fit(b.Sections, opts.maxLines())
	return b, nil
}

// verifyRules reads the repository's `brief.verify` rules (none when unreadable).
func verifyRules(store *tracker.Store) []tracker.BriefVerifyRule {
	if store == nil {
		return nil
	}
	cfg, err := store.ReadConfig()
	if err != nil {
		return nil
	}
	return cfg.Brief.Verify
}

func issueHeadLine(it tracker.Issue) string {
	return strings.TrimSpace(fmt.Sprintf("%s [%s] P%d %s %s", it.ID, it.Status, it.Priority, it.Type, it.Title))
}

func issueSection(lang i18n.Lang, it tracker.Issue) Section {
	sec := Section{Title: briefText(lang, "brief.issue.title"), More: "gofer issue show " + it.ID}
	sec.Lines = append(sec.Lines, issueHeadLine(it))
	var facts []string
	if len(it.Tags) > 0 {
		facts = append(facts, briefText(lang, "brief.field.tags", "tags", strings.Join(it.Tags, ", ")))
	}
	if it.Assignee != "" {
		facts = append(facts, briefText(lang, "brief.field.assignee", "assignee", it.Assignee))
	}
	if it.UpdatedAt != "" {
		facts = append(facts, briefText(lang, "brief.field.updated", "time", shortTime(it.UpdatedAt)))
	}
	if len(facts) > 0 {
		sec.Lines = append(sec.Lines, strings.Join(facts, " · "))
	}
	block := func(label, text string) {
		if body := indentBlock(text, "  "); len(body) > 0 {
			sec.Lines = append(sec.Lines, briefText(lang, "brief.field.block", "label", label))
			sec.Lines = append(sec.Lines, body...)
		}
	}
	plan := planComment(it.Comments)
	if plan >= 0 {
		c := it.Comments[plan]
		lines := indentBlock(c.Text, "  ")
		sec.Lines = append(sec.Lines, briefText(lang, "brief.issue.plan_comment",
			"time", shortTime(c.At), "by", c.By, "n", len(lines)))
		if len(lines) > planCommentLines {
			sec.Lines = append(sec.Lines, lines[:planCommentLines]...)
			sec.Lines = append(sec.Lines, "  "+briefText(lang, "brief.issue.more_lines",
				"n", len(lines)-planCommentLines, "id", it.ID))
		} else {
			sec.Lines = append(sec.Lines, lines...)
		}
	}
	block(briefText(lang, "brief.field.description"), it.Description)
	block("design", it.Design)
	if strings.TrimSpace(it.AcceptanceCriteria) == "" {
		sec.Lines = append(sec.Lines, briefText(lang, "brief.issue.no_acceptance"),
			"  "+briefText(lang, "brief.issue.acceptance_hint", "id", it.ID))
	} else {
		block(briefText(lang, "brief.field.acceptance"), it.AcceptanceCriteria)
	}
	if n := len(it.Comments); n > 0 {
		from := 0
		if n > commentsMax {
			from = n - commentsMax
		}
		sec.Lines = append(sec.Lines, briefText(lang, "brief.issue.comments", "shown", n-from, "total", n))
		for i := from; i < n; i++ {
			c := it.Comments[i]
			sec.Lines = append(sec.Lines, fmt.Sprintf("  - %s %s:", shortTime(c.At), c.By))
			if i == plan {
				sec.Lines = append(sec.Lines, "    "+briefText(lang, "brief.issue.plan_comment_above"))
				continue
			}
			lines := indentBlock(c.Text, "    ")
			if len(lines) > commentLines {
				sec.Lines = append(sec.Lines, lines[:commentLines]...)
				sec.Lines = append(sec.Lines, "    "+briefText(lang, "brief.issue.more_lines",
					"n", len(lines)-commentLines, "id", it.ID))
			} else {
				sec.Lines = append(sec.Lines, lines...)
			}
		}
	}
	return sec
}

// planPrefix matches a comment that opens like a plan.
var planPrefix = regexp.MustCompile(`(?i)^[\s#*>\-]*(实施方案|实现方案|方案|计划|plan\b|implementation plan\b)`)

// planComment picks the comment to show in full, or -1: the latest one that starts
// like a plan (any age), else the longest of the recent window when it is longer
// than an ordinary comment is shown.
func planComment(comments []tracker.Comment) int {
	for i := len(comments) - 1; i >= 0; i-- {
		if planPrefix.MatchString(comments[i].Text) {
			return i
		}
	}
	from := 0
	if len(comments) > commentsMax {
		from = len(comments) - commentsMax
	}
	best, bestLines := -1, commentLines
	for i := from; i < len(comments); i++ {
		if n := len(indentBlock(comments[i].Text, "")); n > bestLines {
			best, bestLines = i, n
		}
	}
	return best
}

func issueRefLine(lang i18n.Lang, prefix string, it tracker.Issue) string {
	line := prefix + issueHeadLine(it)
	if it.Status == "closed" && it.CloseReason != "" {
		line += " " + briefText(lang, "brief.issue.close_reason", "reason", firstLine(it.CloseReason, 120))
	}
	return line
}

func treeSection(lang i18n.Lang, it tracker.Issue, rel tracker.Relations, issues []tracker.Issue, byID map[string]tracker.Issue) Section {
	sec := Section{Title: briefText(lang, "brief.tree.title"), More: "gofer issue show " + it.ID}
	ref := func(prefix, id string) string {
		if other, ok := byID[id]; ok {
			return issueRefLine(lang, prefix, other)
		}
		return prefix + id + briefText(lang, "brief.tree.not_in_tracker")
	}
	if it.Parent != "" {
		var head string
		if other, ok := byID[it.Parent]; ok {
			head = issueRefLine(lang, "", other)
		} else {
			head = it.Parent + briefText(lang, "brief.tree.not_in_tracker")
		}
		sec.Lines = append(sec.Lines, briefText(lang, "brief.tree.parent", "head", head))
		var sibs []tracker.Issue
		for _, other := range issues {
			if other.Parent == it.Parent && other.ID != it.ID {
				sibs = append(sibs, other)
			}
		}
		sort.Slice(sibs, func(i, j int) bool { return sibs[i].ID < sibs[j].ID })
		if len(sibs) > 0 {
			sec.Lines = append(sec.Lines, briefText(lang, "brief.tree.siblings"))
			for _, s := range sibs {
				sec.Lines = append(sec.Lines, issueRefLine(lang, "  - ", s))
			}
		}
	}
	if len(rel.Children) > 0 {
		sec.Lines = append(sec.Lines, briefText(lang, "brief.tree.children"))
		for _, c := range rel.Children {
			sec.Lines = append(sec.Lines, ref("  - ", c))
		}
	}
	if len(rel.DependsOn) > 0 {
		sec.Lines = append(sec.Lines, briefText(lang, "brief.tree.depends_on"))
		for _, d := range rel.DependsOn {
			sec.Lines = append(sec.Lines, ref("  - "+d.Type+" → ", d.ID))
		}
	}
	if len(rel.Blocks) > 0 {
		sec.Lines = append(sec.Lines, briefText(lang, "brief.tree.blocked_by"))
		for _, b := range rel.Blocks {
			sec.Lines = append(sec.Lines, ref("  - ", b))
		}
	}
	if len(rel.Linked) > 0 {
		sec.Lines = append(sec.Lines, briefText(lang, "brief.tree.linked"))
		for _, d := range rel.Linked {
			sec.Lines = append(sec.Lines, ref("  - "+d.Type+" ← ", d.ID))
		}
	}
	if len(sec.Lines) == 0 {
		sec.Note = briefText(lang, "brief.tree.empty")
	}
	return sec
}

func designSection(lang i18n.Lang, root string, it tracker.Issue) Section {
	sec := Section{Title: briefText(lang, "brief.design.title"), More: "grep -rn " + it.ID + " docs"}
	refs := []string{it.Description, it.Design, it.AcceptanceCriteria}
	for _, c := range it.Comments {
		refs = append(refs, c.Text)
	}
	hits := designDocs(lang, root, it.ID, it.Parent, refs)
	for _, h := range hits {
		line := "- " + h.path
		if !h.self {
			line += briefText(lang, "brief.design.parent_only", "parent", it.Parent)
		}
		sec.Lines = append(sec.Lines, line)
		sec.Lines = append(sec.Lines, h.lines...)
	}
	if len(hits) == 0 {
		sec.Note = briefText(lang, "brief.design.none")
	}
	return sec
}

// commitSection lists the related commits, the files the issue text itself names
// (first: a takeover plan names what to change) and the files those commits touch.
// When no commit names the issue itself, the commit-derived entries come from the
// parent / siblings and are labelled as such — they describe neighbouring work, not
// necessarily where this issue lands.
func commitSection(lang i18n.Lang, id, parent string, commits []commit, mentioned, files, symbols []string, isGit bool) Section {
	more := "git log --grep " + id
	sec := Section{Title: briefText(lang, "brief.commit.title"), More: more}
	if !isGit {
		sec.Note = briefText(lang, "brief.commit.not_git")
		return sec
	}
	if len(commits) == 0 {
		if len(mentioned) == 0 {
			sec.Note = briefText(lang, "brief.commit.none", "id", id, "parent", parent)
			return sec
		}
		sec.Lines = append(sec.Lines, briefText(lang, "brief.commit.none_inline", "id", id, "parent", parent))
	}
	for i, c := range commits {
		if i >= commitsMax {
			sec.Lines = append(sec.Lines, briefText(lang, "brief.commit.more", "n", len(commits)-i, "more", more))
			break
		}
		line := fmt.Sprintf("- %s %s %s", c.Short, c.Date, c.Subject)
		if c.Via != id {
			line += briefText(lang, "brief.commit.via", "via", c.Via)
		}
		sec.Lines = append(sec.Lines, line)
	}
	if len(mentioned) > 0 {
		sec.Lines = append(sec.Lines, briefText(lang, "brief.commit.mentioned"))
		for _, f := range mentioned {
			sec.Lines = append(sec.Lines, "  - "+f)
		}
	}
	listed := map[string]bool{}
	for _, f := range mentioned {
		listed[f] = true
	}
	var rest []string
	for _, f := range files {
		if !listed[f] {
			rest = append(rest, f)
		}
	}
	if len(rest) > 0 {
		hdr := briefText(lang, "brief.commit.entries")
		if !hasOwnCommit(id, commits) {
			hdr = briefText(lang, "brief.commit.entries_ref")
		}
		sec.Lines = append(sec.Lines, hdr)
		for _, f := range rest {
			sec.Lines = append(sec.Lines, "  - "+f)
		}
	}
	if len(symbols) > 0 {
		sec.Lines = append(sec.Lines, briefText(lang, "brief.commit.symbols"))
		for _, s := range symbols {
			sec.Lines = append(sec.Lines, "  - "+s)
		}
	}
	return sec
}

// workSection lists the jobs linked to the issue (issue_id or the tracker:issue tag)
// and the open plans whose title / description / todos name it.
func workSection(opts Options, id string) Section {
	lang := opts.lang()
	sec := Section{Title: briefText(lang, "brief.work.title")}
	if opts.Client == nil {
		sec.Note = opts.serverNote()
		return sec
	}
	jobs, err := opts.Client.ListJobs(job.ListOpts{Project: opts.ProjectKey, Limit: jobScanLimit})
	if err != nil {
		sec.Note = briefText(lang, "brief.work.read_failed", "err", err.Error())
		return sec
	}
	for _, j := range jobs {
		if j.IssueID != id && !hasString(j.Tags, "tracker:issue:"+id) {
			continue
		}
		line := fmt.Sprintf("- job %s [%s] %s", j.ID, j.Status, j.Agent)
		if j.Title != "" {
			line += " · " + j.Title
		}
		if at := unixDate(j.StartedAt); at != "" {
			line += " · " + at
		}
		sec.Lines = append(sec.Lines, line)
		if note := firstLine(j.ReviewNote, 160); note != "" {
			sec.Lines = append(sec.Lines, "    "+briefText(lang, "brief.work.review", "note", note))
		}
	}
	plans, err := opts.Client.ListPlans(client.PlanListOpts{Status: "open", Project: opts.ProjectKey, Limit: 20})
	if err == nil {
		sort.SliceStable(plans.Plans, func(i, j int) bool { return plans.Plans[i].UpdatedAt > plans.Plans[j].UpdatedAt })
		for i, p := range plans.Plans {
			if i >= planScanMax {
				break
			}
			full, err := opts.Client.GetPlan(p.PlanID)
			if err != nil {
				continue
			}
			planHit := mentions(full.Title+"\n"+full.Description, id)
			var todoLines []string
			for _, t := range full.Todos {
				if !mentions(t.Title+"\n"+t.Acceptance+"\n"+t.Note, id) {
					continue
				}
				todoLines = append(todoLines, "    - "+todoLine(t))
			}
			if !planHit && len(todoLines) == 0 {
				continue
			}
			sec.Lines = append(sec.Lines, briefText(lang, "brief.work.plan", "id", full.PlanID,
				"status", full.Status, "title", full.Title))
			sec.Lines = append(sec.Lines, todoLines...)
		}
	}
	if len(sec.Lines) == 0 {
		sec.Note = briefText(lang, "brief.work.none")
	}
	return sec
}

func hasString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// todoLine is `[status] title · job <id> <status>` for one todo.
func todoLine(t client.Todo) string {
	line := "[" + todoStatus(t) + "] " + t.Title
	if len(t.Jobs) > 0 {
		line += fmt.Sprintf(" · job %s %s", t.Jobs[0].ID, t.Jobs[0].Status)
	} else if t.JobID != "" {
		line += " · job " + t.JobID
	}
	return line
}

func todoStatus(t client.Todo) string {
	if t.Status != "" {
		return t.Status
	}
	if t.Done {
		return "done"
	}
	return "pending"
}
