package brief

import (
	"errors"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/client"
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

	b := Brief{Kind: "plan", ID: id}
	b.Sections = append(b.Sections, planSection(p), todoSection(p, links))
	hs := Section{Title: "交接说明", More: "gofer plan handoff " + id + " --history"}
	if h, err := opts.Client.GetPlanHandoff(id, 0); err != nil {
		hs.Note = "读取失败（" + err.Error() + "）"
	} else if h.Version == 0 || strings.TrimSpace(h.Body) == "" {
		hs.Note = "无；用 `gofer plan handoff " + id + " --set \"…\"` 写入"
	} else {
		hs.Lines = append(hs.Lines, fmt.Sprintf("v%d · %s · %s", h.Version, h.By, unixDate(h.At)))
		hs.Lines = append(hs.Lines, indentBlock(h.Body, "  ")...)
	}
	b.Sections = append(b.Sections, hs, relatedIssueSection(opts, links), hintSection(opts.Store, ""))
	b.Sections = fit(b.Sections, opts.maxLines())
	return b, nil
}

func planSection(p client.Plan) Section {
	sec := Section{Title: "plan", More: "gofer plan show " + p.PlanID}
	sec.Lines = append(sec.Lines, fmt.Sprintf("%s [%s] %s", p.PlanID, p.Status, p.Title))
	var facts []string
	if p.Project != "" {
		facts = append(facts, "项目 "+p.Project)
	}
	if p.TodoCounts != nil || len(p.Todos) > 0 {
		done := 0
		for _, t := range p.Todos {
			if todoStatus(t) == "done" {
				done++
			}
		}
		facts = append(facts, fmt.Sprintf("进度 %d/%d todo", done, len(p.Todos)))
	}
	if len(p.Tags) > 0 {
		facts = append(facts, "标签 "+strings.Join(p.Tags, ", "))
	}
	if p.Paused {
		facts = append(facts, "已暂停")
	}
	if p.BlockedTodo != "" {
		facts = append(facts, "阻塞于 "+p.BlockedTodo)
	}
	if p.Leader != "" && p.Leader != "off" {
		facts = append(facts, "leader "+p.Leader)
	}
	if len(facts) > 0 {
		sec.Lines = append(sec.Lines, strings.Join(facts, " · "))
	}
	if body := indentBlock(p.Description, "  "); len(body) > 0 {
		sec.Lines = append(sec.Lines, "描述：")
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

func todoSection(p client.Plan, links *planLinks) Section {
	sec := Section{Title: "todo", More: "gofer plan show " + p.PlanID}
	if len(p.Todos) == 0 {
		sec.Note = "还没有 todo"
		return sec
	}
	titles := make(map[string]string, len(p.Todos))
	for _, t := range p.Todos {
		titles[t.TodoID] = t.Title
	}
	for _, t := range p.Todos {
		line := "- " + todoLine(t) + " · " + t.TodoID
		if t.Assignee != "" {
			line += " · 指派 " + t.Assignee
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
			sec.Lines = append(sec.Lines, "    依赖："+strings.Join(after, "；"))
		}
		if acc := strings.TrimSpace(t.Acceptance); acc != "" {
			sec.Lines = append(sec.Lines, "    验收："+capRunes(strings.Join(strings.Fields(acc), " "), 200))
		}
		if note := lastLine(t.Note); note != "" {
			sec.Lines = append(sec.Lines, "    结论："+capRunes(note, 200))
		}
		if t.DispatchError != "" {
			sec.Lines = append(sec.Lines, "    派发失败："+firstLine(t.DispatchError, 160))
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
	sec := Section{Title: "关联 issue"}
	if opts.Store == nil {
		sec.Note = "当前目录没有仓库 tracker，无法解析 issue"
		return sec
	}
	if len(links.order) == 0 {
		sec.Note = "plan 与 todo 里没有提到本仓库的 issue"
		return sec
	}
	root := opts.root()
	for i, id := range links.order {
		if i >= relatedIssuesMax {
			sec.Lines = append(sec.Lines, fmt.Sprintf("- 另有 %d 个", len(links.order)-i))
			break
		}
		it := links.byID[id]
		sec.Lines = append(sec.Lines, "- "+issueRefLine("", it))
		if acc := strings.TrimSpace(it.AcceptanceCriteria); acc != "" {
			sec.Lines = append(sec.Lines, "    验收："+capRunes(strings.Join(strings.Fields(acc), " "), 200))
		}
		var docs []string
		for _, h := range designDocs(root, it.ID, "", []string{it.Description, it.Design}) {
			if !h.missing {
				docs = append(docs, h.path)
			}
		}
		if len(docs) > 0 {
			sec.Lines = append(sec.Lines, "    设计稿："+strings.Join(docs, "，"))
		}
		if it.Status != "closed" {
			sec.Lines = append(sec.Lines, "    接手：`gofer issue brief "+it.ID+"`")
		}
	}
	return sec
}

// PrimePlanLines renders one open plan for the prime 「进行中 plan」 section: the plan
// line with the brief hint, then its doing / ready todos with the issues they name.
func PrimePlanLines(p client.Plan, issues []tracker.Issue) []string {
	links := newPlanLinks(p, issues)
	out := []string{fmt.Sprintf("- %s %s（接手：`gofer plan brief %s`）", p.PlanID, p.Title, p.PlanID)}
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
