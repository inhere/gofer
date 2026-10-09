package tracker

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var primeNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func primeWith(t *testing.T, s *Store, cwd string) string {
	t.Helper()
	body, err := s.PrimeWith(PrimeOptions{Cwd: cwd, Now: primeNow})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestPrimeLayoutRulesIndexHandoffs(t *testing.T) {
	s := primeTestStore(t)
	root := s.RepoRoot()
	if root == "" {
		t.Fatal("repo root")
	}
	mem := []Memory{
		{Key: "z-rule", Content: "Z rule body", UpdatedAt: "2026-10-01T00:00:00Z", MemoryMeta: MemoryMeta{Kind: "rule"}},
		{Key: "a-rule", Content: "A rule body", Tags: []string{"prime"}, UpdatedAt: "2026-09-01T00:00:00Z"},
		{Key: "web-rule", Content: "web rule body", Tags: []string{"web"}, UpdatedAt: "2026-10-05T00:00:00Z", MemoryMeta: MemoryMeta{Kind: "rule", Summary: "web 的坑", When: &MemoryWhen{Paths: []string{"web/**"}}}},
		{Key: "kw-rule", Content: "kw body", UpdatedAt: "2026-10-05T00:00:00Z", MemoryMeta: MemoryMeta{Kind: "rule", Summary: "发版流程", When: &MemoryWhen{Keywords: []string{"发版"}}}},
		{Key: "note-web", Content: "【标题】\nweb build 先拷 assets。其余", Tags: []string{"web"}, UpdatedAt: "2026-10-06T00:00:00Z"},
		{Key: "note-old", Content: "old note", Tags: []string{"tunnel"}, UpdatedAt: "2026-06-01T00:00:00Z"},
		{Key: "note-plain", Content: "plain", UpdatedAt: "2026-10-08T00:00:00Z"},
		{Key: "h-old", Content: "expired", UpdatedAt: "2026-09-01T00:00:00Z", MemoryMeta: MemoryMeta{Kind: "handoff"}},
	}
	for i := 0; i < 4; i++ {
		mem = append(mem, Memory{Key: fmt.Sprintf("h-%d", i), Content: fmt.Sprintf("handoff %d", i), UpdatedAt: fmt.Sprintf("2026-10-0%dT00:00:00Z", i+1), MemoryMeta: MemoryMeta{Kind: "handoff", Summary: fmt.Sprintf("交接 %d", i), ExpiresAt: "2026-10-20T00:00:00Z"}})
	}
	if err := s.UpdateMemories(func([]Memory) ([]Memory, error) { return mem, nil }); err != nil {
		t.Fatal(err)
	}

	body := primeWith(t, s, root)
	rules := strings.Index(body, "## 规则")
	index := strings.Index(body, "## 记忆索引")
	handoff := strings.Index(body, "## 交接（未过期，最新 3 条）")
	if rules < 0 || index < rules || handoff < index {
		t.Fatalf("section order:\n%s", body)
	}
	// Rules: full and in key order; when-scoped rules only in the index.
	if !(strings.Index(body, "- a-rule: A rule body") < strings.Index(body, "- z-rule: Z rule body")) || strings.Contains(body, "web rule body") || strings.Contains(body, "kw body") {
		t.Fatalf("rules:\n%s", body)
	}
	if !strings.Contains(body, "- [web] web-rule（规则） · web 的坑 · 4 天前") || !strings.Contains(body, "- [其他] kw-rule（规则） · 发版流程") {
		t.Fatalf("rules in index:\n%s", body)
	}
	// Index: grouped by first tag, fallback summary, ages, stale marker.
	if !strings.Contains(body, "- [web] note-web · web build 先拷 assets。 · 3 天前") || !strings.Contains(body, "- [tunnel] note-old · old note · 130 天前（久未更新）") {
		t.Fatalf("index lines:\n%s", body)
	}
	if strings.Index(body, "[其他]") < strings.Index(body, "[tunnel]") {
		t.Fatalf("其他 group should be last:\n%s", body)
	}
	// Handoffs: unexpired newest 3, expired hidden.
	hs := body[handoff:]
	if !strings.Contains(hs, "h-3（交接） · 交接 3") || strings.Contains(hs, "h-0") || strings.Contains(body, "h-old") || !strings.Contains(hs, "另有 1 条：`gofer memory ls --kind handoff`") || !strings.Contains(hs, "10 天后过期") {
		t.Fatalf("handoffs:\n%s", hs)
	}

	// cwd under web/: the path rule shows in full and web memories lead the index.
	web := primeWith(t, s, filepath.Join(root, "web", "src"))
	if !strings.Contains(web, "- web-rule: web rule body") || strings.Contains(web, "web-rule（规则）") {
		t.Fatalf("path rule full:\n%s", web)
	}
	idx := web[strings.Index(web, "## 记忆索引"):]
	if !strings.HasPrefix(strings.SplitN(idx, "\n", 3)[1], "- [web] note-web") {
		t.Fatalf("matched group first:\n%s", idx)
	}
}

func TestPrimeRulesBudget(t *testing.T) {
	s := primeTestStore(t)
	var mem []Memory
	for i := 0; i < 6; i++ {
		mem = append(mem, Memory{Key: fmt.Sprintf("rule-%d", i), Content: strings.Repeat("规", 250), UpdatedAt: "2026-10-01T00:00:00Z", MemoryMeta: MemoryMeta{Kind: "rule", Summary: "s"}})
	}
	_ = s.UpdateMemories(func([]Memory) ([]Memory, error) { return mem, nil })
	body := primeWith(t, s, "")
	seg := body[strings.Index(body, "## 规则"):strings.Index(body, "## 进行中")]
	if len(seg) > primeRulesBudget+8 || !strings.Contains(seg, "条未展开（见索引），请精简规则") {
		t.Fatalf("rules segment %d bytes:\n%s", len(seg), seg)
	}
	if !strings.Contains(body, "（规则） · s") {
		t.Fatalf("unexpanded rules must be indexed:\n%s", body)
	}
	if !strings.Contains(seg, "- rule-0: ") {
		t.Fatal("first rule by key must be full")
	}
}

func TestPrimeInProgressOnlyAndReadyAssignee(t *testing.T) {
	s := primeTestStore(t)
	issues := []Issue{
		{ID: "x-1", Title: "Doing", Status: "in_progress", Assignee: "bob", UpdatedAt: "2026-10-08T00:00:00Z"},
		{ID: "x-2", Title: "Stale claim", Status: "in_progress", UpdatedAt: "2026-09-01T00:00:00Z"},
		{ID: "x-3", Title: "Open but assigned", Status: "open", Assignee: "amy", CreatedAt: "2026-10-01T00:00:00Z", UpdatedAt: "2026-10-01T00:00:00Z"},
	}
	if err := s.WriteIssues(issues); err != nil {
		t.Fatal(err)
	}
	body := primeWith(t, s, "")
	active := body[strings.Index(body, "## 进行中 issue"):strings.Index(body, "## ready")]
	if !strings.Contains(active, "x-1 [in_progress] Doing @bob\n") || !strings.Contains(active, "x-2 [in_progress] Stale claim（认领 38 天无更新）") || strings.Contains(active, "x-3") {
		t.Fatalf("active:\n%s", active)
	}
	if !strings.Contains(body, "- x-3 P0 Open but assigned @amy · 8 天前") {
		t.Fatalf("ready assignee:\n%s", body)
	}
}

func TestIssueStatusOpenClearsAssignee(t *testing.T) {
	s := primeTestStore(t)
	if err := s.WriteIssues([]Issue{{ID: "y-1", Title: "a", Status: "in_progress", Assignee: "bob"}, {ID: "y-2", Title: "b", Status: "in_progress", Assignee: "bob"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.UpdateIssue("y-1", IssuePatch{Status: "open"})
	if err != nil || got.Assignee != "" {
		t.Fatalf("clear: %+v %v", got, err)
	}
	got, _ = s.UpdateIssue("y-2", IssuePatch{Status: "open", KeepAssignee: true})
	if got.Assignee != "bob" {
		t.Fatalf("keep: %+v", got)
	}
	who := "amy"
	got, _ = s.UpdateIssue("y-2", IssuePatch{Status: "in_progress"})
	got, _ = s.UpdateIssue("y-2", IssuePatch{Status: "open", Assignee: &who})
	if got.Assignee != "amy" {
		t.Fatalf("explicit assignee wins: %+v", got)
	}
}

func TestPrimeFocusHookAndScopedSection(t *testing.T) {
	s := primeTestStore(t)
	body, err := s.PrimeWith(PrimeOptions{Now: primeNow, Focus: "## 当前重点\n- x\n"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(body, "## 当前重点") < strings.Index(body, "写记忆：") {
		t.Fatalf("focus placement:\n%s", body)
	}
	items := []Memory{
		{Key: "g-rule", Content: "global rule", MemoryMeta: MemoryMeta{Kind: "rule"}},
		{Key: "g-note", Content: "note body", UpdatedAt: "2026-10-01T00:00:00Z"},
		{Key: "g-hand", Content: "h", UpdatedAt: "2026-10-08T00:00:00Z", MemoryMeta: MemoryMeta{Kind: "handoff", Summary: "交接"}},
		{Key: "g-gone", Content: "h", UpdatedAt: "2026-08-08T00:00:00Z", MemoryMeta: MemoryMeta{Kind: "handoff"}},
	}
	out := RenderScopedPrimeSection("全局记忆", items, ScopedPrimeOptions{Now: primeNow, SummaryLimit: -1, Budget: 600})
	if !strings.Contains(out, "## 全局记忆") || !strings.Contains(out, "- g-rule: global rule") || !strings.Contains(out, "] g-note · note body") || !strings.Contains(out, "g-hand（交接） · 交接") || strings.Contains(out, "g-gone") {
		t.Fatalf("scoped:\n%s", out)
	}
	small := RenderScopedPrimeSection("全局记忆", items, ScopedPrimeOptions{Now: primeNow, SummaryLimit: -1, Budget: 80, LsHint: "gofer memory ls --global"})
	if !strings.Contains(small, "条：`gofer memory ls --global <关键字>`") {
		t.Fatalf("scoped budget:\n%s", small)
	}
}

func TestSyncMergesMemoryMeta(t *testing.T) {
	var report SyncReport
	base := map[string]Memory{"k": {Key: "k", Content: "c", UpdatedAt: "1"}}
	local := map[string]Memory{"k": {Key: "k", Content: "c", UpdatedAt: "2", MemoryMeta: MemoryMeta{Summary: "local summary"}}}
	remote := map[string]Memory{"k": {Key: "k", Content: "c", UpdatedAt: "3", MemoryMeta: MemoryMeta{Kind: "rule", When: &MemoryWhen{Paths: []string{"web/**"}}}}}
	out := mergeMemories(base, local, remote, &report)
	if len(out) != 1 || out[0].Summary != "local summary" || out[0].Kind != "rule" || out[0].When == nil || out[0].When.Paths[0] != "web/**" || len(report.Conflicts) != 0 {
		t.Fatalf("merge: %+v %+v", out, report)
	}
}
