package brief

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/tracker"
)

type fakeClient struct {
	jobs    []job.JobResult
	plans   map[string]client.Plan
	handoff map[string]client.PlanHandoff
	scoped  map[string][]client.ScopedMemory
	err     error
}

func (f *fakeClient) ListJobs(job.ListOpts) ([]job.JobResult, error) { return f.jobs, f.err }
func (f *fakeClient) ListPlans(client.PlanListOpts) (client.PlanList, error) {
	var out client.PlanList
	for _, p := range f.plans {
		out.Plans = append(out.Plans, client.Plan{PlanID: p.PlanID, UpdatedAt: p.UpdatedAt})
	}
	return out, f.err
}
func (f *fakeClient) GetPlan(id string) (client.Plan, error) {
	if p, ok := f.plans[id]; ok {
		return p, nil
	}
	return client.Plan{}, errors.New("404 plan not found")
}
func (f *fakeClient) GetPlanHandoff(id string, _ int) (client.PlanHandoff, error) {
	return f.handoff[id], nil
}
func (f *fakeClient) ListScopedMemories(opts client.ScopedMemoryListOpts) ([]client.ScopedMemory, error) {
	return f.scoped[opts.Scope+"/"+opts.ScopeKey], f.err
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	assert.Require(t, assert.NoErr(t, os.MkdirAll(filepath.Dir(path), 0o755)))
	assert.Require(t, assert.NoErr(t, os.WriteFile(path, []byte(body), 0o644)))
}

func commitAll(t *testing.T, root, msg string) string {
	t.Helper()
	run(t, root, "git", "add", "-A")
	run(t, root, "git", "commit", "-q", "-m", msg)
	out, err := exec.Command("git", "-C", root, "rev-parse", "--short=8", "HEAD").Output()
	assert.Require(t, assert.NoErr(t, err))
	return strings.TrimSpace(string(out))
}

// briefRepo is a git repository with a tracker: epic t-ep, children t-ep.1 (open, the
// subject), t-ep.2 (closed, its close reason names a commit) and t-ep.3; a design doc
// and commits naming them.
func briefRepo(t *testing.T) (*tracker.Store, string) {
	t.Helper()
	root := t.TempDir()
	run(t, root, "git", "init", "-q")
	s, _, err := tracker.Init(root, "t", true)
	assert.Require(t, assert.NoErr(t, err))
	write(t, root, "internal/flow/flow.go", "package flow\n")
	write(t, root, "internal/flow/flow_test.go", "package flow\n")
	sibSHA := commitAll(t, root, "feat(flow): sibling work")
	write(t, root, "internal/flow/flow.go", "package flow\n// v2\nfunc Capture() {}\n\nfunc (s *Store) Save() {}\n")
	write(t, root, "docs/design/2026-10-01-flow-design.md", "# Flow 设计（t-ep.1 / .2）\n\n> epic t-ep\n\n## 背景\n\n第一行背景。\n\n## 实施记录\n\n- 落地 t-ep.1 的第一步。\n")
	write(t, root, "docs/other.md", "# 其他\n\n只提到 t-ep.10 和 t-ep.2。\n")
	commitAll(t, root, "feat(flow): first step of t-ep.1")
	write(t, root, "web/src/x.ts", "x\n")
	commitAll(t, root, "chore: unrelated t-ep.10 work")
	now := time.Now().UTC().Format(time.RFC3339)
	issues := []tracker.Issue{
		{ID: "t-ep", Title: "epic flow", Type: "epic", Status: "open", Priority: 2, CreatedAt: now},
		{ID: "t-ep.1", Title: "feat: flow capture improvement", Type: "feature", Status: "open", Priority: 2, Parent: "t-ep", Tags: []string{"flow"},
			Description: "see docs/notes/plan.md for the plan", Comments: []tracker.Comment{{At: now, By: "me", Text: "方案：先改 flow.go\n再补测试"}}, CreatedAt: now},
		{ID: "t-ep.2", Title: "done sibling", Type: "task", Status: "closed", Priority: 2, Parent: "t-ep", CloseReason: "landed in " + sibSHA, CreatedAt: now},
		{ID: "t-ep.3", Title: "next sibling", Type: "task", Status: "open", Priority: 3, Parent: "t-ep", Deps: []tracker.Dep{{ID: "t-ep.1", Type: "blocks"}}, CreatedAt: now},
		{ID: "t-ep.10", Title: "far sibling", Type: "task", Status: "open", Priority: 3, Parent: "t-ep", CreatedAt: now},
	}
	assert.Require(t, assert.NoErr(t, s.UpdateIssues(func([]tracker.Issue) ([]tracker.Issue, error) { return issues, nil })))
	mems := []tracker.Memory{
		{Key: "verify", Content: "run make test\nthen make lint", UpdatedAt: now, MemoryMeta: tracker.MemoryMeta{Kind: tracker.MemoryKindRule,
			Flags: []tracker.MemoryFlag{{At: now, Reason: "lint 已并入 test"}}}},
		{Key: "web-rule", Content: "web only rule", UpdatedAt: now, MemoryMeta: tracker.MemoryMeta{Kind: tracker.MemoryKindRule, When: &tracker.MemoryWhen{Paths: []string{"web/**"}}}},
		{Key: "flow-pitfall", Content: "flow.go and the TS copy must change together", UpdatedAt: now, MemoryMeta: tracker.MemoryMeta{When: &tracker.MemoryWhen{Paths: []string{"internal/flow/**"}}}},
		{Key: "capture-tips", Content: "keyword matched note", UpdatedAt: now},
		{Key: "unrelated", Content: "nothing to do with it", UpdatedAt: now},
	}
	assert.Require(t, assert.NoErr(t, s.UpdateMemories(func([]tracker.Memory) ([]tracker.Memory, error) { return mems, nil })))
	return s, sibSHA
}

func sectionOf(b Brief, title string) Section {
	for _, s := range b.Sections {
		if s.Title == title {
			return s
		}
	}
	return Section{}
}

func TestIssueBriefSections(t *testing.T) {
	s, sibSHA := briefRepo(t)
	fc := &fakeClient{
		jobs: []job.JobResult{{ID: "job-1", Status: "done", Agent: "codex", IssueID: "t-ep.1", ReviewNote: "接受：OK\n细节"}, {ID: "job-2", Status: "done", IssueID: "t-ep.3"}},
		plans: map[string]client.Plan{"plan-a": {PlanID: "plan-a", Title: "flow plan", Status: "open", Todos: []client.Todo{
			{TodoID: "td-1", Title: "implement t-ep.1", Status: "doing", Jobs: []client.TodoJob{{ID: "job-1", Status: "done"}}},
			{TodoID: "td-2", Title: "other", Status: "ready"}}}},
		scoped: map[string][]client.ScopedMemory{"global/": {{Key: "g-rule", Content: "global rule", MemoryMeta: tracker.MemoryMeta{Kind: tracker.MemoryKindRule}}}},
	}
	b, err := IssueBrief("t-ep.1", Options{Store: s, Client: fc})
	assert.Require(t, assert.NoErr(t, err))
	var titles []string
	for _, sec := range b.Sections {
		titles = append(titles, sec.Title)
	}
	assert.Eq(t, []string{"issue", "上下文树", "设计稿", "相关提交", "相关 job / plan", "本 issue 的验证命令", "适用记忆", "接手提示"}, titles)
	text := b.Text()

	assert.Contains(t, text, "t-ep.1 [open] P2 feature feat: flow capture improvement")
	assert.Contains(t, text, "验收标准：无验收标准")
	assert.Contains(t, text, "评论（最近 1 / 共 1）：")
	assert.Contains(t, text, "  再补测试")

	tree := strings.Join(sectionOf(b, "上下文树").Lines, "\n")
	assert.Contains(t, tree, "父：t-ep [open] P2 epic epic flow")
	assert.Contains(t, tree, "  - t-ep.2 [closed] P2 task done sibling — 关闭：landed in "+sibSHA)
	assert.Contains(t, tree, "被依赖（等本 issue）：\n  - t-ep.3")

	design := strings.Join(sectionOf(b, "设计稿").Lines, "\n")
	assert.Contains(t, design, "- docs/notes/plan.md\n  （文件不存在）")
	assert.Contains(t, design, "- docs/design/2026-10-01-flow-design.md\n  § Flow 设计（t-ep.1 / .2）")
	assert.Contains(t, design, "  § 实施记录\n    - 落地 t-ep.1 的第一步。")
	assert.NotContains(t, design, "docs/other.md") // only t-ep.10 / a sibling, not t-ep.1 or the parent

	commits := strings.Join(sectionOf(b, "相关提交").Lines, "\n")
	assert.Contains(t, commits, "feat(flow): first step of t-ep.1")
	assert.Contains(t, commits, "feat(flow): sibling work（经 t-ep.2 关闭说明）")
	assert.NotContains(t, commits, "unrelated t-ep.10")
	// The comment names bare "flow.go": resolved to its repo path and listed first.
	assert.Contains(t, commits, "issue 文本提到的文件：\n  - internal/flow/flow.go")
	assert.NotContains(t, commits, "代码入口") // its only commit-derived entry is already listed
	assert.Contains(t, commits, "关键符号")
	assert.Contains(t, commits, "  - internal/flow/flow.go:3  Capture")
	assert.Contains(t, commits, "  - internal/flow/flow.go:5  Store.Save")
	assert.NotContains(t, commits, "flow_test.go")
	assert.NotContains(t, commits, "docs/design")

	work := strings.Join(sectionOf(b, "相关 job / plan").Lines, "\n")
	assert.Contains(t, work, "- job job-1 [done] codex")
	assert.Contains(t, work, "    评审：接受：OK")
	assert.NotContains(t, work, "job-2")
	assert.Contains(t, work, "- plan plan-a [open] flow plan（`gofer plan brief plan-a`）\n    - [doing] implement t-ep.1 · job job-1 done")

	verify := strings.Join(sectionOf(b, "本 issue 的验证命令").Lines, "\n")
	assert.Contains(t, verify, "`go test -race -count=1 ./internal/flow`")

	mem := strings.Join(sectionOf(b, "适用记忆").Lines, "\n")
	assert.Contains(t, mem, "- ⚠ 待复核（lint 已并入 test） verify（规则）: run make test\n    then make lint")
	assert.Contains(t, mem, "- [全局] g-rule（规则）: global rule")
	assert.NotContains(t, mem, "web-rule") // when.paths does not match the code entries
	assert.Contains(t, mem, "- flow-pitfall · flow.go and the TS copy must change together")
	assert.Contains(t, mem, "- capture-tips") // title word "capture" in the key
	assert.NotContains(t, mem, "unrelated")

	assert.Contains(t, text, "`gofer issue update t-ep.1 --claim`")
	assert.Contains(t, text, "提交策略：")

	_, err = IssueBrief("t-missing", Options{Store: s})
	assert.ErrMsgContains(t, err, "not found")
}

func TestIssueBriefWithoutServer(t *testing.T) {
	s, _ := briefRepo(t)
	b, err := IssueBrief("t-ep.1", Options{Store: s, ClientNote: "dial tcp: connection refused"})
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, "未连接 server，本节跳过（dial tcp: connection refused）", sectionOf(b, "相关 job / plan").Note)
	mem := sectionOf(b, "适用记忆")
	assert.Contains(t, mem.Note, "全局 / 项目记忆：未连接 server")
	assert.Contains(t, strings.Join(mem.Lines, "\n"), "verify（规则）") // local memories still listed
	assert.Contains(t, b.Text(), "（未连接 server，本节跳过（dial tcp: connection refused））")
}

func TestBriefMaxLines(t *testing.T) {
	s, _ := briefRepo(t)
	long := strings.Repeat("line\n", 80)
	assert.Require(t, assert.NoErr(t, s.UpdateIssues(func(items []tracker.Issue) ([]tracker.Issue, error) {
		for i := range items {
			if items[i].ID == "t-ep.1" {
				items[i].Design = long
			}
		}
		return items, nil
	})))
	b, err := IssueBrief("t-ep.1", Options{Store: s, MaxLines: 40})
	assert.Require(t, assert.NoErr(t, err))
	text := b.Text()
	assert.True(t, strings.Count(text, "\n") <= 40, strings.Count(text, "\n"))
	issue := sectionOf(b, "issue")
	assert.True(t, issue.Truncated > 0)
	assert.Contains(t, text, "[本节截断 ")
	assert.Contains(t, text, "：`gofer issue show t-ep.1`]")
	// Every section keeps its heading; the takeover hints are never cut.
	for _, title := range []string{"## 上下文树", "## 设计稿", "## 相关提交", "## 适用记忆", "## 接手提示"} {
		assert.Contains(t, text, title)
	}
	assert.Eq(t, 0, sectionOf(b, "接手提示").Truncated)
	assert.Contains(t, text, "--claim")
}

func TestPlanBriefAndPrimeLines(t *testing.T) {
	s, _ := briefRepo(t)
	plan := client.Plan{PlanID: "plan-a", Title: "flow work (t-ep)", Status: "open", Project: "proj", Description: "范围 t-ep.1（capture）、.3",
		Todos: []client.Todo{
			{TodoID: "td-1", Title: "T1 implement .1", Status: "done", Note: "job ok\n结论：已合并", Jobs: []client.TodoJob{{ID: "job-1", Status: "done"}}},
			{TodoID: "td-2", Title: "T2 next .3", Status: "doing", After: []string{"td-1"}, Acceptance: "tests pass"},
			{TodoID: "td-3", Title: "T3 docs", Status: "ready"},
			{TodoID: "td-4", Title: "T4 later .2", Status: "pending"},
		}}
	fc := &fakeClient{plans: map[string]client.Plan{"plan-a": plan}, handoff: map[string]client.PlanHandoff{"plan-a": {Version: 2, By: "me", At: 1, Body: "下一步做 T2"}}}

	_, err := PlanBrief("plan-a", Options{Store: s, ClientNote: "down"})
	assert.ErrMsgContains(t, err, "needs the server")

	b, err := PlanBrief("plan-a", Options{Store: s, Client: fc})
	assert.Require(t, assert.NoErr(t, err))
	text := b.Text()
	assert.Contains(t, text, "plan-a [open] flow work (t-ep)\n项目 proj · 进度 1/4 todo")
	assert.Contains(t, text, "- [done] T1 implement .1 · job job-1 done · td-1 · issue t-ep.1\n    结论：结论：已合并")
	assert.Contains(t, text, "- [doing] T2 next .3 · td-2 · issue t-ep.3\n    依赖：T1 implement .1\n    验收：tests pass")
	assert.Contains(t, text, "v2 · me · ")
	assert.Contains(t, text, "  下一步做 T2")
	related := strings.Join(sectionOf(b, "关联 issue").Lines, "\n")
	assert.Contains(t, related, "- t-ep [open] P2 epic epic flow")
	assert.Contains(t, related, "- t-ep.1 [open] P2 feature feat: flow capture improvement\n    设计稿：docs/design/2026-10-01-flow-design.md\n    接手：`gofer issue brief t-ep.1`")
	assert.Contains(t, related, "- t-ep.2 [closed]")

	issues, _ := s.ReadIssues()
	lines := PrimePlanLines(plan, issues)
	assert.Eq(t, []string{
		"- plan-a flow work (t-ep)（接手：`gofer plan brief plan-a`）",
		"  - [doing] T2 next .3 · t-ep.3",
		"  - [ready] T3 docs",
	}, lines)
}

func TestMentions(t *testing.T) {
	assert.True(t, mentions("见 t-ep.1。", "t-ep.1"))
	assert.True(t, mentions("(t-ep.1)", "t-ep.1"))
	assert.False(t, mentions("t-ep.10", "t-ep.1"))
	assert.False(t, mentions("t-ep.1", "t-ep"))
	assert.True(t, mentions("epic t-ep.", "t-ep"))
	assert.False(t, mentions("xt-ep", "t-ep"))
	sc := newIDScanner([]string{"t-ep", "t-ep.1", "t-ep.2", "t-ep.12"})
	assert.Eq(t, []string{"t-ep.1", "t-ep.12", "t-ep.2"}, sc.find("t-ep.1、.2 and t-ep.12", "t-ep"))
	assert.Eq(t, []string{"t-ep.1"}, sc.find("t-ep.1、.2", ""))
}

func TestIssueSectionPlanCommentAndAcceptanceHint(t *testing.T) {
	long := "实施方案\n"
	for i := 1; i <= 70; i++ {
		long += "步骤 " + strconv.Itoa(i) + "\n"
	}
	other := "杂项\n1\n2\n3\n4\n5\n6\n7\n8"
	it := tracker.Issue{ID: "t-x", Title: "x", Status: "open", Comments: []tracker.Comment{
		{By: "a", Text: long}, {By: "b", Text: other}, {By: "c", Text: "short"}}}
	text := strings.Join(issueSection(it).Lines, "\n")
	assert.Contains(t, text, "已有方案评论")
	assert.Contains(t, text, "步骤 59")
	assert.NotContains(t, text, "步骤 61")
	assert.Contains(t, text, "另 11 行")
	assert.True(t, strings.Index(text, "已有方案评论") < strings.Index(text, "评论（最近"))
	assert.Contains(t, text, "    （方案评论，见上）")
	assert.Contains(t, text, "…（另 3 行")                                     // ordinary long comment truncated
	assert.Contains(t, text, "`gofer issue update t-x --acceptance \"…\"`") // acceptance hint

	it.AcceptanceCriteria = "done"
	it.Comments = []tracker.Comment{{By: "b", Text: other}}
	text = strings.Join(issueSection(it).Lines, "\n")
	assert.NotContains(t, text, "--acceptance")
	assert.Contains(t, text, "已有方案评论") // longest recent comment
}

func TestDeclName(t *testing.T) {
	assert.Eq(t, "Foo", declName("a.go", "func Foo(x int) {"))
	assert.Eq(t, "Store.Save", declName("a.go", "func (s *Store) Save() error {"))
	assert.Eq(t, "Box.Get", declName("a.go", "func (b Box[T]) Get() T {"))
	assert.Eq(t, "", declName("a.go", "type Foo struct {"))
	assert.Eq(t, "load", declName("a.ts", "export async function load(id: string) {"))
	assert.Eq(t, "useX", declName("a.ts", "export const useX = () => {"))
	assert.Eq(t, "", declName("a.ts", "const y = 1"))
}

func TestVerifySection(t *testing.T) {
	root := t.TempDir()
	write(t, root, "internal/a/a.go", "package a\n")
	write(t, root, "internal/b/b_windows.go", "package b\n")
	sec := verifySection(root, []string{"internal/b/b_windows.go", "internal/a/a.go", "internal/gone/g.go", "web/src/x.ts", "skills/x.md"})
	text := strings.Join(sec.Lines, "\n")
	assert.Contains(t, text, "go test -race -count=1 ./internal/a ./internal/b`")
	assert.NotContains(t, text, "gone")
	assert.Contains(t, text, "GOOS=darwin go vet")
	assert.Contains(t, text, "npx vue-tsc --noEmit && npx vitest run && npx vite build")
	assert.Eq(t, "", sec.Note)

	none := verifySection(root, []string{"skills/x.md"})
	assert.NotEq(t, "", none.Note)
	assert.Empty(t, none.Lines)
}

// An issue with no commit of its own: the commit-derived entries come from the parent /
// siblings and are labelled as reference only.
func TestIssueBriefEntriesWithoutOwnCommits(t *testing.T) {
	s, _ := briefRepo(t)
	b, err := IssueBrief("t-ep.3", Options{Store: s})
	assert.Require(t, assert.NoErr(t, err))
	sec := sectionOf(b, "相关提交")
	commits := strings.Join(sec.Lines, "\n")
	// Seen empty once under a full Windows run: print the section note (git missing /
	// no commits found) so the next occurrence says which.
	if !strings.Contains(commits, "代码入口（来自父 / 兄弟 issue 的提交；本 issue 尚无提交，仅供参考）：") {
		t.Fatalf("code entries not labelled; section note=%q lines=%q", sec.Note, commits)
	}
	assert.NotContains(t, commits, "issue 文本提到的文件")
}
