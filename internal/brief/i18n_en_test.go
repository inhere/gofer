package brief

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/tracker"
)

// han matches a Han character: text written for English must carry none.
var han = regexp.MustCompile(`\p{Han}`)

// englishRepo is a repository whose user-written content is English too: the scan
// below checks the text gofer writes, so the user's own words must not be the thing
// that fails it.
func englishRepo(t *testing.T) *tracker.Store {
	t.Helper()
	root := t.TempDir()
	run(t, root, "git", "init", "-q")
	s, _, err := tracker.Init(root, "t", true)
	if err != nil {
		t.Fatalf("tracker.Init: %v", err)
	}
	write(t, root, "internal/flow/flow.go", "package flow\n")
	commitAll(t, root, "feat(flow): capture for t-e1")
	write(t, root, "internal/flow/flow.go", "package flow\n\nfunc Capture() {}\n")
	write(t, root, "docs/design/2026-10-01-flow-design.md", "# Flow design (t-e1)\n\n## Background\n\nfirst line.\n")
	commitAll(t, root, "feat(flow): first step of t-e1")
	write(t, root, "go.mod", "module example.com/flow\n")
	now := time.Now().UTC().Format(time.RFC3339)
	issues := []tracker.Issue{
		{ID: "t-e", Title: "flow epic", Type: "epic", Status: "open", Priority: 2, CreatedAt: now},
		{ID: "t-e1", Title: "improve capture", Type: "feature", Status: "open", Priority: 2, Parent: "t-e",
			Tags: []string{"flow"}, AcceptanceCriteria: "- it captures",
			Description: "see docs/design/2026-10-01-flow-design.md, change internal/flow/flow.go",
			Comments:    []tracker.Comment{{At: now, By: "me", Text: "plan: change flow.go first"}}, CreatedAt: now},
		{ID: "t-e2", Title: "next step", Type: "task", Status: "open", Priority: 3, Parent: "t-e",
			Deps: []tracker.Dep{{ID: "t-e1", Type: "blocks"}}, CreatedAt: now},
	}
	if err := s.UpdateIssues(func([]tracker.Issue) ([]tracker.Issue, error) { return issues, nil }); err != nil {
		t.Fatalf("UpdateIssues: %v", err)
	}
	memories := []tracker.Memory{
		{Key: "verify", Content: "run make test", MemoryMeta: tracker.MemoryMeta{Kind: tracker.MemoryKindRule}},
		{Key: "flow-pitfall", Content: "the TS copy must change too", MemoryMeta: tracker.MemoryMeta{
			When: &tracker.MemoryWhen{Paths: []string{"internal/flow/**"}}}},
	}
	if err := s.UpdateMemories(func([]tracker.Memory) ([]tracker.Memory, error) { return memories, nil }); err != nil {
		t.Fatalf("UpdateMemories: %v", err)
	}
	return s
}

// trackerText is the text internal/tracker still writes in the deployment default (the
// commit policy): it is stripped out of the English text the scan checks, so a leaking
// tracker line cannot pass for a migrated one. The prime migration translates it.
func trackerText(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, policy := range []string{"", "ask", "none"} {
		if text, err := tracker.CommitPolicyText(policy); err == nil {
			out = append(out, text)
		}
	}
	return out
}

// enRefocus is the refocus fixture with English content.
func enRefocus() Refocus {
	return Refocus{
		Topic:  "re-inject after a compact",
		Claims: []RefocusClaim{{ID: "t-e1", Title: "improve capture", Status: "in_progress"}},
		Plans:  []RefocusPlan{{PlanID: "p-1", Title: "wrap up", Status: "blocked", Progress: 2, Total: 5, Blocked: "t-e2"}},
		Jobs:   []RefocusJob{{JobID: "job-9", Title: "full run", Status: "running"}},
		Mail:   []RefocusMail{{ID: 12, From: "alice", Subject: "look at this"}},
	}
}

// TestEnglishBriefHasNoCJK: an English project's brief — the issue brief, the plan
// brief, the prime's plan lines and the refocus section — is written in English.
func TestEnglishBriefHasNoCJK(t *testing.T) {
	s := englishRepo(t)
	fc := &fakeClient{
		jobs: []job.JobResult{{ID: "job-1", Status: "done", Agent: "codex", IssueID: "t-e1", ReviewNote: "accepted"}},
		plans: map[string]client.Plan{"plan-a": {PlanID: "plan-a", Title: "flow work", Status: "open",
			Project: "proj", Todos: []client.Todo{
				{TodoID: "td-1", Title: "implement t-e1", Status: "doing", Acceptance: "tests pass", Assignee: "me",
					After: []string{"td-0"}, Jobs: []client.TodoJob{{ID: "job-1", Status: "done"}}},
			}}},
		handoff: map[string]client.PlanHandoff{"plan-a": {Version: 2, By: "me", At: 1793000000, Body: "next: td-1"}},
		scoped:  map[string][]client.ScopedMemory{"global/": {{Key: "g-rule", Content: "global rule", MemoryMeta: tracker.MemoryMeta{Kind: tracker.MemoryKindRule}}}},
	}
	opts := Options{Store: s, Client: fc, ProjectKey: "proj", Language: i18n.EN}

	b, err := IssueBrief("t-e1", opts)
	if err != nil {
		t.Fatalf("IssueBrief (en): %v", err)
	}
	issue := b.Text()
	for _, text := range trackerText(t) {
		issue = strings.ReplaceAll(issue, text, "<tracker text>")
	}
	pb, err := PlanBrief("plan-a", opts)
	if err != nil {
		t.Fatalf("PlanBrief (en): %v", err)
	}
	plan := pb.Text()
	for _, text := range trackerText(t) {
		plan = strings.ReplaceAll(plan, text, "<tracker text>")
	}
	issues, err := s.ReadIssues()
	if err != nil {
		t.Fatalf("ReadIssues: %v", err)
	}
	checks := map[string]string{
		"issue brief": issue,
		"plan brief":  plan,
		"refocus":     RenderRefocusIn(i18n.EN, enRefocus(), true, 4000),
		"prime": strings.Join(PrimePlanLinesIn(i18n.EN, client.Plan{PlanID: "plan-a", Title: "flow work",
			Todos: []client.Todo{{TodoID: "td-1", Title: "implement t-e1", Status: "doing"}}}, issues), "\n"),
	}
	for name, text := range checks {
		if m := han.FindString(text); m != "" {
			t.Errorf("%s: English text carries CJK (%q):\n%s", name, m, text)
		}
	}
	// English keeps gofer's own layout — the heading, the section titles, the field
	// lines and the command lines are the same shape as the zh-CN ones.
	for name, text := range checks {
		cases := map[string][]string{
			"issue brief": {
				"# Handoff brief issue t-e1",
				"\n## Context tree\n",
				"parent: t-e [open] P2 epic flow epic",
				"blocking (waiting for this issue):\n  - t-e2 [open] P3 task next step",
				"\n## Appliable memories\n- verify(rule): run make test",
				"- Claim it before you start `gofer issue update t-e1 --claim`",
			},
			"plan brief": {
				"# Handoff brief plan plan-a",
				"project proj · 0/1 todos done",
				"\n## todo\n- [doing] implement t-e1 · job job-1 done · td-1 · assigned me",
				"    depends on: td-0\n    acceptance: tests pass",
				"takeover: `gofer issue brief t-e1`",
			},
			"refocus": {"[gofer · hint] the session was just resumed", "blocked on t-e2", "from alice: look at this"},
			"prime":   {"- plan-a flow work (takeover: `gofer plan brief plan-a`)", "  - [doing] implement t-e1"},
		}[name]
		for _, want := range cases {
			if !strings.Contains(text, want) {
				t.Errorf("%s lacks %q:\n%s", name, want, text)
			}
		}
	}
}

// migratedFiles are the brief files whose text lives in internal/i18n (design
// 2026-10-27-i18n-design.md §7). A string literal added to one of them must not be
// written in Chinese.
var migratedFiles = []string{
	"brief.go", "issue.go", "plan.go", "memory.go", "verify.go", "refocus.go", "docs.go", "git.go",
}

// cjkExceptions are the literals that match what the USER wrote rather than text gofer
// generates: planComment recognises a comment that opens like a plan in either
// language (design §5 — recognition text, so it stays with its parser).
var cjkExceptions = map[string]bool{
	"`(?i)^[\\s#*>\\-]*(实施方案|实现方案|方案|计划|plan\\b|implementation plan\\b)`": true,
}

// TestMigratedBriefFilesHaveNoCJK is the scan guard of design §7. Comments are not
// scanned (they are not text an agent reads) and neither are test files.
func TestMigratedBriefFilesHaveNoCJK(t *testing.T) {
	for _, file := range migratedFiles {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, file, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || cjkExceptions[lit.Value] {
				return true
			}
			if m := han.FindString(lit.Value); m != "" {
				t.Errorf("%s:%d: string literal carries CJK (%q) — move the text into internal/i18n: %s",
					file, fset.Position(lit.Pos()).Line, m, lit.Value)
			}
			return true
		})
	}
}
