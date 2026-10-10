package brief

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/inhere/gofer/internal/client"
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

	siblingReasons := map[string]string{}
	if item.Parent != "" {
		for _, it := range issues {
			if it.Parent == item.Parent && it.ID != id && it.Status == "closed" && it.CloseReason != "" {
				siblingReasons[it.ID] = it.CloseReason
			}
		}
	}
	commits, isGit := relatedCommits(root, id, item.Parent, siblingReasons)
	fromCommits := codeEntries(root, commits)
	mentioned := mentionedFiles(root, item)
	files := mergeEntries(mentioned, fromCommits)

	b := Brief{Kind: "issue", ID: id}
	b.Sections = append(b.Sections,
		issueSection(item),
		treeSection(item, rel, issues, byID),
		designSection(root, item),
		commitSection(id, item.Parent, commits, mentioned, fromCommits, keySymbols(root, commits, files), isGit),
		workSection(opts, id),
		verifySection(root, files),
		memorySection(opts, newMemoryTarget(item, files)),
		hintSection(opts.Store, id),
	)
	b.Sections = fit(b.Sections, opts.maxLines())
	return b, nil
}

func issueHeadLine(it tracker.Issue) string {
	return strings.TrimSpace(fmt.Sprintf("%s [%s] P%d %s %s", it.ID, it.Status, it.Priority, it.Type, it.Title))
}

func issueSection(it tracker.Issue) Section {
	sec := Section{Title: "issue", More: "gofer issue show " + it.ID}
	sec.Lines = append(sec.Lines, issueHeadLine(it))
	var facts []string
	if len(it.Tags) > 0 {
		facts = append(facts, "标签 "+strings.Join(it.Tags, ", "))
	}
	if it.Assignee != "" {
		facts = append(facts, "指派 "+it.Assignee)
	}
	if it.UpdatedAt != "" {
		facts = append(facts, "更新 "+shortTime(it.UpdatedAt))
	}
	if len(facts) > 0 {
		sec.Lines = append(sec.Lines, strings.Join(facts, " · "))
	}
	block := func(label, text string) {
		if body := indentBlock(text, "  "); len(body) > 0 {
			sec.Lines = append(sec.Lines, label+"：")
			sec.Lines = append(sec.Lines, body...)
		}
	}
	plan := planComment(it.Comments)
	if plan >= 0 {
		c := it.Comments[plan]
		lines := indentBlock(c.Text, "  ")
		sec.Lines = append(sec.Lines, fmt.Sprintf("已有方案评论（%s %s，共 %d 行）：", shortTime(c.At), c.By, len(lines)))
		if len(lines) > planCommentLines {
			sec.Lines = append(sec.Lines, lines[:planCommentLines]...)
			sec.Lines = append(sec.Lines, fmt.Sprintf("  …（另 %d 行：`gofer issue show %s`）", len(lines)-planCommentLines, it.ID))
		} else {
			sec.Lines = append(sec.Lines, lines...)
		}
	}
	block("描述", it.Description)
	block("design", it.Design)
	if strings.TrimSpace(it.AcceptanceCriteria) == "" {
		sec.Lines = append(sec.Lines, "验收标准：无验收标准",
			fmt.Sprintf("  → 补写：`gofer issue update %s --acceptance \"…\"`", it.ID))
	} else {
		block("验收标准", it.AcceptanceCriteria)
	}
	if n := len(it.Comments); n > 0 {
		from := 0
		if n > commentsMax {
			from = n - commentsMax
		}
		sec.Lines = append(sec.Lines, fmt.Sprintf("评论（最近 %d / 共 %d）：", n-from, n))
		for i := from; i < n; i++ {
			c := it.Comments[i]
			sec.Lines = append(sec.Lines, fmt.Sprintf("  - %s %s:", shortTime(c.At), c.By))
			if i == plan {
				sec.Lines = append(sec.Lines, "    （方案评论，见上）")
				continue
			}
			lines := indentBlock(c.Text, "    ")
			if len(lines) > commentLines {
				sec.Lines = append(sec.Lines, lines[:commentLines]...)
				sec.Lines = append(sec.Lines, fmt.Sprintf("    …（另 %d 行：`gofer issue show %s`）", len(lines)-commentLines, it.ID))
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

func issueRefLine(prefix string, it tracker.Issue) string {
	line := prefix + issueHeadLine(it)
	if it.Status == "closed" && it.CloseReason != "" {
		line += " — 关闭：" + firstLine(it.CloseReason, 120)
	}
	return line
}

func treeSection(it tracker.Issue, rel tracker.Relations, issues []tracker.Issue, byID map[string]tracker.Issue) Section {
	sec := Section{Title: "上下文树", More: "gofer issue show " + it.ID}
	ref := func(prefix, id string) string {
		if other, ok := byID[id]; ok {
			return issueRefLine(prefix, other)
		}
		return prefix + id + "（不在本仓库 tracker）"
	}
	if it.Parent != "" {
		sec.Lines = append(sec.Lines, ref("父：", it.Parent))
		var sibs []tracker.Issue
		for _, other := range issues {
			if other.Parent == it.Parent && other.ID != it.ID {
				sibs = append(sibs, other)
			}
		}
		sort.Slice(sibs, func(i, j int) bool { return sibs[i].ID < sibs[j].ID })
		if len(sibs) > 0 {
			sec.Lines = append(sec.Lines, "兄弟：")
			for _, s := range sibs {
				sec.Lines = append(sec.Lines, issueRefLine("  - ", s))
			}
		}
	}
	if len(rel.Children) > 0 {
		sec.Lines = append(sec.Lines, "子：")
		for _, c := range rel.Children {
			sec.Lines = append(sec.Lines, ref("  - ", c))
		}
	}
	if len(rel.DependsOn) > 0 {
		sec.Lines = append(sec.Lines, "依赖：")
		for _, d := range rel.DependsOn {
			sec.Lines = append(sec.Lines, ref("  - "+d.Type+" → ", d.ID))
		}
	}
	if len(rel.Blocks) > 0 {
		sec.Lines = append(sec.Lines, "被依赖（等本 issue）：")
		for _, b := range rel.Blocks {
			sec.Lines = append(sec.Lines, ref("  - ", b))
		}
	}
	if len(rel.Linked) > 0 {
		sec.Lines = append(sec.Lines, "被关联：")
		for _, d := range rel.Linked {
			sec.Lines = append(sec.Lines, ref("  - "+d.Type+" ← ", d.ID))
		}
	}
	if len(sec.Lines) == 0 {
		sec.Note = "无父 / 子 / 依赖"
	}
	return sec
}

func designSection(root string, it tracker.Issue) Section {
	sec := Section{Title: "设计稿", More: "grep -rn " + it.ID + " docs"}
	refs := []string{it.Description, it.Design, it.AcceptanceCriteria}
	for _, c := range it.Comments {
		refs = append(refs, c.Text)
	}
	hits := designDocs(root, it.ID, it.Parent, refs)
	for _, h := range hits {
		line := "- " + h.path
		if !h.self {
			line += "（只提到父 " + it.Parent + "）"
		}
		sec.Lines = append(sec.Lines, line)
		sec.Lines = append(sec.Lines, h.lines...)
	}
	if len(hits) == 0 {
		sec.Note = "docs/ 下没有提到本 issue 或其父的文档"
	}
	return sec
}

// commitSection lists the related commits, the files the issue text itself names
// (first: a takeover plan names what to change) and the files those commits touch.
// When no commit names the issue itself, the commit-derived entries come from the
// parent / siblings and are labelled as such — they describe neighbouring work, not
// necessarily where this issue lands.
func commitSection(id, parent string, commits []commit, mentioned, files, symbols []string, isGit bool) Section {
	more := "git log --grep " + id
	sec := Section{Title: "相关提交", More: more}
	if !isGit {
		sec.Note = "不是 git 仓库，本节跳过"
		return sec
	}
	if len(commits) == 0 {
		if len(mentioned) == 0 {
			sec.Note = "git log 中没有提到 " + id + "（或父 " + parent + "）的提交"
			return sec
		}
		sec.Lines = append(sec.Lines, "（git log 中没有提到 "+id+"（或父 "+parent+"）的提交）")
	}
	for i, c := range commits {
		if i >= commitsMax {
			sec.Lines = append(sec.Lines, fmt.Sprintf("- 另有 %d 个：`%s`", len(commits)-i, more))
			break
		}
		line := fmt.Sprintf("- %s %s %s", c.Short, c.Date, c.Subject)
		if c.Via != id {
			line += "（经 " + c.Via + "）"
		}
		sec.Lines = append(sec.Lines, line)
	}
	if len(mentioned) > 0 {
		sec.Lines = append(sec.Lines, "issue 文本提到的文件：")
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
		hdr := "代码入口（这些提交触及最多的文件）："
		if !hasOwnCommit(id, commits) {
			hdr = "代码入口（来自父 / 兄弟 issue 的提交；本 issue 尚无提交，仅供参考）："
		}
		sec.Lines = append(sec.Lines, hdr)
		for _, f := range rest {
			sec.Lines = append(sec.Lines, "  - "+f)
		}
	}
	if len(symbols) > 0 {
		sec.Lines = append(sec.Lines, "关键符号（这些提交新增 / 改动的 Go / TS 函数，行号为当前工作区）：")
		for _, s := range symbols {
			sec.Lines = append(sec.Lines, "  - "+s)
		}
	}
	return sec
}

// workSection lists the jobs linked to the issue (issue_id or the tracker:issue tag)
// and the open plans whose title / description / todos name it.
func workSection(opts Options, id string) Section {
	sec := Section{Title: "相关 job / plan"}
	if opts.Client == nil {
		sec.Note = opts.serverNote()
		return sec
	}
	jobs, err := opts.Client.ListJobs(job.ListOpts{Project: opts.ProjectKey, Limit: jobScanLimit})
	if err != nil {
		sec.Note = "读取 server 失败，本节跳过（" + err.Error() + "）"
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
			sec.Lines = append(sec.Lines, "    评审："+note)
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
			sec.Lines = append(sec.Lines, fmt.Sprintf("- plan %s [%s] %s（`gofer plan brief %s`）", full.PlanID, full.Status, full.Title, full.PlanID))
			sec.Lines = append(sec.Lines, todoLines...)
		}
	}
	if len(sec.Lines) == 0 {
		sec.Note = "没有关联本 issue 的 job，也没有提到它的进行中 plan"
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
