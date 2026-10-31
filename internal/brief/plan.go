package brief

import (
	"errors"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/tracker"
)

// relatedIssuesMax caps the issues a plan brief summarises.
const relatedIssuesMax = 10

// PlanBrief assembles the brief of a server plan: its fields, the todo list (status,
// dependencies, acceptance, the latest job and its outcome), the handoff note, and a
// short brief of every repository issue the plan or its todos name.
func PlanBrief(id string, opts Options) (Brief, error) {
	if opts.Client == nil {
		return Brief{}, errors.New("plan brief needs the server: " + opts.serverNote())
	}
	p, err := opts.Client.GetPlan(id)
	if err != nil {
		return Brief{}, fmt.Errorf("get plan %s: %w", id, err)
	}
	var issues []tracker.Issue
	if opts.Store != nil {
		issues, _ = opts.Store.ReadIssues()
	}
	links := newPlanLinks(p, issues)
	lang := opts.lang()

	b := Brief{Kind: "plan", ID: id, Lang: lang}
	b.Sections = append(b.Sections, planSection(lang, p), todoSection(lang, p, links))
	hs := Section{Title: briefText(lang, "brief.handoff.title"), More: "gofer plan handoff " + id + " --history"}
	if h, err := opts.Client.GetPlanHandoff(id, 0); err != nil {
		hs.Note = briefText(lang, "brief.handoff.read_failed", "err", err.Error())
	} else if h.Version == 0 || strings.TrimSpace(h.Body) == "" {
		hs.Note = briefText(lang, "brief.handoff.none", "id", id)
	} else {
		hs.Lines = append(hs.Lines, fmt.Sprintf("v%d · %s · %s", h.Version, h.By, unixDate(h.At)))
		hs.Lines = append(hs.Lines, indentBlock(h.Body, "  ")...)
	}
	b.Sections = append(b.Sections, hs, relatedIssueSection(opts, links), hintSection(lang, opts.Store, ""))
	b.Sections = fit(b.Sections, opts.maxLines())
	return b, nil
}

func planSection(lang i18n.Lang, p client.Plan) Section {
	sec := Section{Title: "plan", More: "gofer plan show " + p.PlanID}
	sec.Lines = append(sec.Lines, fmt.Sprintf("%s [%s] %s", p.PlanID, p.Status, p.Title))
	var facts []string
	if p.Project != "" {
		facts = append(facts, briefText(lang, "brief.field.project", "project", p.Project))
	}
	if p.TodoCounts != nil || len(p.Todos) > 0 {
		done := 0
		for _, t := range p.Todos {
			if todoStatus(t) == "done" {
				done++
			}
		}
		facts = append(facts, briefText(lang, "brief.field.progress", "done", done, "total", len(p.Todos)))
	}
	if len(p.Tags) > 0 {
		facts = append(facts, briefText(lang, "brief.field.tags", "tags", strings.Join(p.Tags, ", ")))
	}
	if p.Paused {
		facts = append(facts, briefText(lang, "brief.field.paused"))
	}
	if p.BlockedTodo != "" {
		facts = append(facts, briefText(lang, "brief.field.blocked", "todo", p.BlockedTodo))
	}
	if p.Leader != "" && p.Leader != "off" {
		facts = append(facts, "leader "+p.Leader)
	}
	if len(facts) > 0 {
		sec.Lines = append(sec.Lines, strings.Join(facts, " · "))
	}
	if body := indentBlock(p.Description, "  "); len(body) > 0 {
		sec.Lines = append(sec.Lines, briefText(lang, "brief.field.block", "label", briefText(lang, "brief.field.description")))
		sec.Lines = append(sec.Lines, body...)
	}
	return sec
}

// planLinks maps the plan's todos to the repository issues they name.
type planLinks struct {
	scan  *idScanner
	base  string // the parent id the `.N` shorthand expands against ("" = none)
	byID  map[string]tracker.Issue
	order []string // every linked issue, plan text first, then todo order
}

// newPlanLinks finds the issues named in the plan title / description; when they
// share one parent (or are one epic), `.N` in todo text expands against it.
func newPlanLinks(p client.Plan, issues []tracker.Issue) *planLinks {
	l := &planLinks{byID: make(map[string]tracker.Issue, len(issues))}
	ids := make([]string, 0, len(issues))
	for _, it := range issues {
		l.byID[it.ID] = it
		ids = append(ids, it.ID)
	}
	if len(ids) == 0 {
		return l
	}
	l.scan = newIDScanner(ids)
	head := l.scan.find(p.Title+"\n"+p.Description, "")
	bases := map[string]bool{}
	for _, id := range head {
		if parent := l.byID[id].Parent; parent != "" {
			bases[parent] = true
		} else {
			bases[id] = true
		}
	}
	if len(bases) == 1 {
		for b := range bases {
			l.base = b
		}
	}
	seen := map[string]bool{}
	add := func(found []string) {
		for _, id := range found {
			if !seen[id] {
				seen[id] = true
				l.order = append(l.order, id)
			}
		}
	}
	add(l.scan.find(p.Title+"\n"+p.Description, l.base))
	for _, t := range p.Todos {
		add(l.todoIssues(t))
	}
	return l
}

func (l *planLinks) todoIssues(t client.Todo) []string {
	if l == nil || l.scan == nil {
		return nil
	}
	return l.scan.find(t.Title+"\n"+t.Acceptance, l.base)
}

func todoSection(lang i18n.Lang, p client.Plan, links *planLinks) Section {
	sec := Section{Title: "todo", More: "gofer plan show " + p.PlanID}
	if len(p.Todos) == 0 {
		sec.Note = briefText(lang, "brief.todo.none")
		return sec
	}
	titles := make(map[string]string, len(p.Todos))
	for _, t := range p.Todos {
		titles[t.TodoID] = t.Title
	}
	for _, t := range p.Todos {
		line := "- " + todoLine(t) + " · " + t.TodoID
		if t.Assignee != "" {
			line += " · " + briefText(lang, "brief.todo.assigned", "name", t.Assignee)
		}
		if ids := links.todoIssues(t); len(ids) > 0 {
			line += " · issue " + strings.Join(ids, ", ")
		}
		sec.Lines = append(sec.Lines, line)
		if len(t.After) > 0 {
			after := make([]string, 0, len(t.After))
			for _, a := range t.After {
				if title := titles[a]; title != "" {
					after = append(after, capRunes(title, 30))
				} else {
					after = append(after, a)
				}
			}
			sec.Lines = append(sec.Lines, "    "+briefText(lang, "brief.todo.deps",
				"list", strings.Join(after, briefText(lang, "brief.todo.deps_join"))))
		}
		if acc := strings.TrimSpace(t.Acceptance); acc != "" {
			sec.Lines = append(sec.Lines, "    "+briefText(lang, "brief.todo.acceptance",
				"text", capRunes(strings.Join(strings.Fields(acc), " "), 200)))
		}
		if note := lastLine(t.Note); note != "" {
			sec.Lines = append(sec.Lines, "    "+briefText(lang, "brief.todo.conclusion", "text", capRunes(note, 200)))
		}
		if t.DispatchError != "" {
			sec.Lines = append(sec.Lines, "    "+briefText(lang, "brief.todo.dispatch_failed",
				"err", firstLine(t.DispatchError, 160)))
		}
	}
	return sec
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// relatedIssueSection is a short brief (head line, acceptance, design docs) of each
// issue the plan names.
func relatedIssueSection(opts Options, links *planLinks) Section {
	lang := opts.lang()
	sec := Section{Title: briefText(lang, "brief.related.title")}
	if opts.Store == nil {
		sec.Note = briefText(lang, "brief.related.no_tracker")
		return sec
	}
	if len(links.order) == 0 {
		sec.Note = briefText(lang, "brief.related.none")
		return sec
	}
	root := opts.root()
	for i, id := range links.order {
		if i >= relatedIssuesMax {
			sec.Lines = append(sec.Lines, briefText(lang, "brief.related.more", "n", len(links.order)-i))
			break
		}
		it := links.byID[id]
		sec.Lines = append(sec.Lines, "- "+issueRefLine(lang, "", it))
		if acc := strings.TrimSpace(it.AcceptanceCriteria); acc != "" {
			sec.Lines = append(sec.Lines, "    "+briefText(lang, "brief.todo.acceptance",
				"text", capRunes(strings.Join(strings.Fields(acc), " "), 200)))
		}
		var docs []string
		for _, h := range designDocs(lang, root, it.ID, "", []string{it.Description, it.Design}) {
			if !h.missing {
				docs = append(docs, h.path)
			}
		}
		if len(docs) > 0 {
			sec.Lines = append(sec.Lines, "    "+briefText(lang, "brief.related.design_docs",
				"docs", strings.Join(docs, briefText(lang, "brief.related.design_docs_join"))))
		}
		if it.Status != "closed" {
			sec.Lines = append(sec.Lines, "    "+briefText(lang, "brief.related.takeover", "id", it.ID))
		}
	}
	return sec
}

// PrimePlanLinesIn renders one open plan for the prime 「进行中 plan」 section in lang: the
// plan line with the brief hint, then its doing / ready todos with the issues they name.
func PrimePlanLinesIn(lang i18n.Lang, p client.Plan, issues []tracker.Issue) []string {
	links := newPlanLinks(p, issues)
	out := []string{briefText(lang, "brief.prime.plan", "id", p.PlanID, "title", p.Title)}
	for _, t := range p.Todos {
		status := todoStatus(t)
		if status != "doing" && status != "ready" {
			continue
		}
		line := fmt.Sprintf("  - [%s] %s", status, t.Title)
		if ids := links.todoIssues(t); len(ids) > 0 {
			line += " · " + strings.Join(ids, ", ")
		}
		out = append(out, line)
	}
	return out
}
