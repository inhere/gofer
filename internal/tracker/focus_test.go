package tracker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

var focusNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func focusFullInput() FocusInput {
	return FocusInput{
		Now: focusNow,
		Issues: []Issue{
			{ID: "g-1", Title: "active one", Status: "in_progress", UpdatedAt: "2026-10-08T00:00:00Z"},
			{ID: "g-2", Title: "stale claim", Status: "in_progress", UpdatedAt: "2026-09-01T00:00:00Z"},
			{ID: "g-3", Title: "blocker", Status: "closed", ClosedAt: "2026-10-08T00:00:00Z"},
			{ID: "g-4", Title: "now ready", Status: "open", Deps: []Dep{{ID: "g-3", Type: "blocks"}}},
		},
		Memories: []Memory{
			{Key: "h-new", Content: "交接正文", UpdatedAt: "2026-10-08T00:00:00Z", MemoryMeta: MemoryMeta{Kind: "handoff", Summary: "接着做 P2"}},
			{Key: "h-old", Content: "旧交接", UpdatedAt: "2026-10-01T00:00:00Z", MemoryMeta: MemoryMeta{Kind: "handoff"}},
		},
		Plans:  []FocusPlan{{ID: "plan-a", Title: "收尾", Done: 7, Total: 8, NextTodo: "发版"}},
		Git:    &FocusGit{Branch: "main", Head: "8d41dde5", Changed: 3, TrackerChanged: 1, HasUpstream: true, Ahead: 2, HasTag: true, Tag: "v0.128.2", SinceTag: 2},
		Server: &FocusServer{Version: "0.128.2 (6a52776)", Workers: []FocusWorker{{Name: "w-a", Version: "0.128.2 (6a52776)", Online: true}, {Name: "w-b", Online: false}}},
	}
}

func TestRenderFocusAllParts(t *testing.T) {
	out := RenderFocus(focusFullInput())
	assert.True(t, strings.HasPrefix(out, "## 当前重点（自动，"), out)
	for _, want := range []string{
		"- 在做 g-1 active one\n",
		"- plan plan-a「收尾」7/8，下一步：发版\n",
		"- 交接 h-new：接着做 P2\n",
		"- 刚解锁 g-4 now ready（g-3 已关闭）\n",
		"- 未收尾：工作树 3 个文件未提交（含 tracker 1）；领先上游 2 个提交\n",
		"- 仓库：main 8d41dde5，最近 tag v0.128.2（之后 2 个提交）\n",
		"- 服务：server 0.128.2；worker 1/2 在线（同版本）；离线 w-b\n",
	} {
		assert.StrContains(t, out, want)
	}
	assert.NotContains(t, out, "g-2")
	assert.NotContains(t, out, "h-old")
	assert.True(t, len(out) <= FocusBudget)
}

func TestRenderFocusOmitsMissingParts(t *testing.T) {
	assert.Eq(t, "", RenderFocus(FocusInput{Now: focusNow}))

	in := focusFullInput()
	in.Git, in.Server, in.Plans = nil, nil, nil
	out := RenderFocus(in)
	assert.NotContains(t, out, "仓库")
	assert.NotContains(t, out, "服务")
	assert.NotContains(t, out, "plan ")
	assert.StrContains(t, out, "在做 g-1")

	// a clean tree without upstream: no 未收尾 line; no tag: no tag text
	out = RenderFocus(FocusInput{Now: focusNow, Git: &FocusGit{Branch: "dev", Head: "abc"}})
	assert.NotContains(t, out, "未收尾")
	assert.StrContains(t, out, "- 仓库：dev abc\n")
}

func TestRenderFocusBudgetTrimOrder(t *testing.T) {
	in := focusFullInput()
	in.Issues = append(in.Issues,
		Issue{ID: "g-5", Title: strings.Repeat("长", 40), Status: "in_progress", UpdatedAt: "2026-10-07T00:00:00Z"},
		Issue{ID: "g-6", Title: strings.Repeat("长", 40), Status: "in_progress", UpdatedAt: "2026-10-06T00:00:00Z"},
	)
	in.Budget = 1 << 20
	full := RenderFocus(in)
	assert.StrContains(t, full, "服务：")
	assert.True(t, len(full) > FocusBudget)

	// trim just below the full size: the last env line (服务) goes first
	in.Budget = len(full) - 1
	out := RenderFocus(in)
	assert.NotContains(t, out, "服务：")
	assert.StrContains(t, out, "仓库：")
	assert.True(t, len(out) <= in.Budget)

	// tighter: env and 未收尾 gone, 刚解锁 next, 在做 survives longest
	in.Budget = 400
	out = RenderFocus(in)
	assert.NotContains(t, out, "仓库：")
	assert.NotContains(t, out, "未收尾")
	assert.StrContains(t, out, "在做 g-1")
	assert.True(t, len(out) <= 400)

	in.Budget = 10 // not even the heading fits
	assert.Eq(t, "", RenderFocus(in))
}

func TestFocusActiveLimitAndUnlocked(t *testing.T) {
	var issues []Issue
	for i := 0; i < 5; i++ {
		issues = append(issues, Issue{ID: fmt.Sprintf("a-%d", i), Status: "in_progress", UpdatedAt: fmt.Sprintf("2026-10-0%dT00:00:00Z", i+1)})
	}
	got := focusActiveIssues(issues, focusNow)
	assert.Len(t, got, 3)
	assert.Eq(t, "a-4", got[0].ID)

	issues = []Issue{
		{ID: "c-new", Status: "closed", ClosedAt: "2026-10-08T00:00:00Z"},
		{ID: "c-old", Status: "closed", ClosedAt: "2026-10-01T00:00:00Z"},
		{ID: "c-upd", Status: "closed", UpdatedAt: "2026-10-09T00:00:00Z"},
		{ID: "x-open", Status: "open"},
		{ID: "r-1", Status: "open", Priority: 2, Deps: []Dep{{ID: "c-new", Type: "blocks"}}},
		{ID: "r-2", Status: "open", Priority: 1, Deps: []Dep{{ID: "c-upd", Type: "blocks"}}},
		{ID: "r-old", Status: "open", Deps: []Dep{{ID: "c-old", Type: "blocks"}}},
		{ID: "r-blocked", Status: "open", Deps: []Dep{{ID: "c-new", Type: "blocks"}, {ID: "x-open", Type: "blocks"}}},
		{ID: "r-parent", Status: "open", Deps: []Dep{{ID: "c-new", Type: "parent-child"}}},
		{ID: "r-busy", Status: "in_progress", Deps: []Dep{{ID: "c-new", Type: "blocks"}}},
	}
	unlocked := unlockedIssues(issues, focusNow)
	assert.Len(t, unlocked, 2)
	assert.Eq(t, "r-2", unlocked[0].issue.ID)
	assert.Eq(t, "c-upd", unlocked[0].by)
	assert.Eq(t, "r-1", unlocked[1].issue.ID)
}

func TestFocusServerLine(t *testing.T) {
	assert.Eq(t, "", focusServerLine(nil))
	assert.Eq(t, "服务：server 1.0", focusServerLine(&FocusServer{Version: "1.0"}))
	line := focusServerLine(&FocusServer{Version: "1.0 (abc)", Workers: []FocusWorker{
		{Name: "w-a", Version: "1.0 (abc)", Online: true},
		{Name: "w-b", Version: "0.9", Online: true},
		{Name: "w-c"}, {Name: "w-d"}, {Name: "w-e"}, {Name: "w-f"},
	}})
	assert.Eq(t, "服务：server 1.0；worker 2/6 在线，版本不同：w-b 0.9；离线 w-c · w-d · w-e 等 4 个", line)
}

// fakeGit answers by subcommand; a missing answer is an error.
type fakeGit map[string]string

func (f fakeGit) run(_ context.Context, _ string, args ...string) (string, error) {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if out, ok := f[a]; ok {
			return out, nil
		}
		return "", errors.New("fake git: no " + a)
	}
	return "", errors.New("fake git: no args")
}

const fakeStatus = "# branch.oid 7c97ea7ac4b99a6e563fc821c3dc2ccea1fe7ef9\n" +
	"# branch.head m-p2\n" +
	"# branch.upstream origin/m-p2\n" +
	"# branch.ab +3 -1\n" +
	"1 .M N... 100644 100644 100644 aaa bbb internal/a.go\n" +
	"1 M. N... 100644 100644 100644 aaa bbb .gofer/tracker/issues.jsonl\n" +
	"2 R. N... 100644 100644 100644 aaa bbb R100 docs/new name.md\tdocs/old.md\n"

func TestCollectFocusGit(t *testing.T) {
	ctx := context.Background()
	g, err := CollectFocusGit(ctx, fakeGit{"status": fakeStatus, "describe": "v0.128.2-12-g7c97ea7a\n", "rev-parse": ".gofer/tracker/\n"}.run, "/repo", "/repo/.gofer/tracker")
	assert.NoErr(t, err)
	assert.Eq(t, FocusGit{Branch: "m-p2", Head: "7c97ea7a", Changed: 3, TrackerChanged: 1, HasUpstream: true, Ahead: 3, HasTag: true, Tag: "v0.128.2", SinceTag: 12}, *g)

	// no tag, no upstream, detached, prefix unknown
	g, err = CollectFocusGit(ctx, fakeGit{"status": "# branch.oid abcdef1234\n# branch.head (detached)\n1 .M N... 1 1 1 a b .gofer/tracker/x\n"}.run, "", "/t")
	assert.NoErr(t, err)
	assert.Eq(t, FocusGit{Head: "abcdef12", Changed: 1}, *g)

	// not a repository
	_, err = CollectFocusGit(ctx, fakeGit{}.run, "/repo", "/repo/.gofer/tracker")
	assert.Err(t, err)
}

func TestParseGitDescribe(t *testing.T) {
	tag, n, ok := parseGitDescribe("v1-rc-1-0-gabc\n")
	assert.True(t, ok)
	assert.Eq(t, "v1-rc-1", tag)
	assert.Eq(t, 0, n)
	_, _, ok = parseGitDescribe("garbage")
	assert.False(t, ok)
}

func TestBuildFocusSourcesAndTimeout(t *testing.T) {
	s := primeTestStore(t)
	assert.NoErr(t, s.WriteIssues([]Issue{{ID: "g-1", Title: "doing", Status: "in_progress", UpdatedAt: "2026-10-08T00:00:00Z"}}))

	git := fakeGit{"status": fakeStatus, "describe": "v1.0.0-1-gabcdef12"}.run
	remote := func(context.Context) (FocusRemote, error) {
		return FocusRemote{Server: &FocusServer{Version: "1.0.0"}, Plans: []FocusPlan{{ID: "p-1", Title: "t", Total: 2, Done: 1}}}, nil
	}
	out := s.BuildFocus(context.Background(), focusNow, FocusSources{Git: git, Remote: remote})
	for _, want := range []string{"在做 g-1 doing", "plan p-1「t」1/2", "仓库：m-p2 7c97ea7a，最近 tag v1.0.0", "服务：server 1.0.0"} {
		assert.StrContains(t, out, want)
	}

	// a failing remote and a hanging git only drop their own lines
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	hang := func(ctx context.Context, _ string, _ ...string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	failing := func(context.Context) (FocusRemote, error) { return FocusRemote{}, errors.New("down") }
	start := time.Now()
	out = s.BuildFocus(ctx, focusNow, FocusSources{Git: hang, Remote: failing})
	assert.True(t, time.Since(start) < time.Second)
	assert.StrContains(t, out, "在做 g-1 doing")
	assert.NotContains(t, out, "仓库")
	assert.NotContains(t, out, "服务")
}
