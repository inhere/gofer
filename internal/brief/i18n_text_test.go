package brief

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/tracker"
)

// The zh-CN side of the brief is pinned against testdata/*.golden: those files hold the
// text the code rendered BEFORE it moved into internal/i18n (design
// 2026-10-27-i18n-design.md §7). An existing deployment's agent must not notice the
// migration, so every character of them has to stay.
//
// GOFER_GOLDEN_UPDATE=1 rewrites a golden with what the current code renders: it is how
// these files were captured from the pre-migration code, and how a deliberate wording
// change is recorded.

var (
	// goldenTime masks the renderings of a date (a fresh repository's commit / comment
	// dates), which no two runs of the fixture can share.
	goldenTime = regexp.MustCompile(`\d{4}-\d{2}-\d{2}( \d{2}:\d{2})?`)
	// goldenSHA masks a commit hash: the fixture commits get a new one every run.
	goldenSHA = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
)

// masked blank out what a run cannot reproduce, so the rest of a brief can be pinned
// verbatim.
func masked(text string) string {
	return goldenTime.ReplaceAllString(goldenSHA.ReplaceAllString(text, "<sha>"), "<time>")
}

// goldenOptions is the fixture the goldens were captured from: briefRepo's tracker (an
// epic with an open child, a design doc, commits and memories) plus a server holding a
// job, a plan and a global memory.
func goldenOptions(t *testing.T) Options {
	t.Helper()
	s, _ := briefRepo(t)
	fc := &fakeClient{
		jobs: []job.JobResult{
			{ID: "job-1", Status: "done", Agent: "codex", IssueID: "t-ep.1", ReviewNote: "接受：OK\n细节"},
			{ID: "job-2", Status: "done", IssueID: "t-ep.3"},
		},
		plans: map[string]client.Plan{"plan-a": {
			PlanID: "plan-a", Title: "flow plan (t-ep)", Status: "open", Project: "proj",
			Description: "范围 t-ep.1（capture）、.3", Paused: true, BlockedTodo: "td-2", Leader: "leader-1",
			Tags:  []string{"flow", "brief"},
			Todos: []client.Todo{
				{TodoID: "td-1", Title: "implement t-ep.1", Status: "done", Note: "job ok\n结论：已合并", Acceptance: "tests pass",
					Jobs: []client.TodoJob{{ID: "job-1", Status: "done"}}},
				{TodoID: "td-2", Title: "next t-ep.3", Status: "blocked", After: []string{"td-1"}, Assignee: "suga",
					DispatchError: "runner busy"},
			},
		}},
		handoff: map[string]client.PlanHandoff{"plan-a": {Version: 2, By: "me", At: 1793000000, Body: "下一步做 td-2"}},
		scoped: map[string][]client.ScopedMemory{"global/": {{Key: "g-rule", Content: "global rule",
			MemoryMeta: tracker.MemoryMeta{Kind: tracker.MemoryKindRule}}}},
	}
	return Options{Store: s, Client: fc, ProjectKey: "proj"}
}

// goldenCase pairs a golden file with the text it pins.
type goldenCase struct {
	file   string
	render func(t *testing.T, opts Options) string
}

// goldenCases are every render the migration touched.
func goldenCases() []goldenCase {
	return []goldenCase{
		{"issue-brief.txt", func(t *testing.T, opts Options) string {
			b, err := IssueBrief("t-ep.1", opts)
			if err != nil {
				t.Fatalf("IssueBrief: %v", err)
			}
			return b.Text()
		}},
		// The sections that need the server say why they are missing.
		{"issue-brief-no-server.txt", func(t *testing.T, opts Options) string {
			opts.Client, opts.ClientNote = nil, "dial tcp: connection refused"
			b, err := IssueBrief("t-ep.1", opts)
			if err != nil {
				t.Fatalf("IssueBrief: %v", err)
			}
			return b.Text()
		}},
		// A cut section says how many of its lines it left out and how to see them.
		{"issue-brief-truncated.txt", func(t *testing.T, opts Options) string {
			if err := opts.Store.UpdateIssues(func(items []tracker.Issue) ([]tracker.Issue, error) {
				for i := range items {
					if items[i].ID == "t-ep.1" {
						items[i].Design = strings.Repeat("设计行\n", 80)
					}
				}
				return items, nil
			}); err != nil {
				t.Fatalf("UpdateIssues: %v", err)
			}
			opts.MaxLines = 40
			b, err := IssueBrief("t-ep.1", opts)
			if err != nil {
				t.Fatalf("IssueBrief: %v", err)
			}
			return b.Text()
		}},
		{"plan-brief.txt", func(t *testing.T, opts Options) string {
			b, err := PlanBrief("plan-a", opts)
			if err != nil {
				t.Fatalf("PlanBrief: %v", err)
			}
			return b.Text()
		}},
		// A plan brief whose handoff note cannot be read says so.
		{"plan-brief-handoff-error.txt", func(t *testing.T, opts Options) string {
			opts.Client = &failingHandoff{fakeClient: opts.Client.(*fakeClient)}
			b, err := PlanBrief("plan-a", opts)
			if err != nil {
				t.Fatalf("PlanBrief: %v", err)
			}
			return b.Text()
		}},
		// A plan with no todos and no repository issues.
		{"plan-brief-empty.txt", func(t *testing.T, opts Options) string {
			fc := opts.Client.(*fakeClient)
			fc.plans["plan-b"] = client.Plan{PlanID: "plan-b", Title: "empty", Status: "open"}
			opts.Store = nil
			b, err := PlanBrief("plan-b", opts)
			if err != nil {
				t.Fatalf("PlanBrief: %v", err)
			}
			return b.Text()
		}},
		// The prime's one line per open plan.
		{"prime-plan-lines.txt", func(t *testing.T, opts Options) string {
			issues, err := opts.Store.ReadIssues()
			if err != nil {
				t.Fatalf("ReadIssues: %v", err)
			}
			plan := client.Plan{PlanID: "plan-a", Title: "flow plan (t-ep)", Status: "open",
				Description: "t-ep",
				Todos: []client.Todo{
					{TodoID: "td-1", Title: "implement t-ep.1", Status: "doing"},
					{TodoID: "td-2", Title: "next", Status: "ready", After: []string{"td-1"}},
					{TodoID: "td-3", Title: "docs", Status: "done"},
				}}
			return strings.Join(PrimePlanLines(plan, issues), "\n")
		}},
		// The post-compact re-injection of what the session was doing.
		{"refocus.txt", func(t *testing.T, _ Options) string {
			return RenderRefocus(fullRefocus(), false, 4000)
		}},
	}
}

func TestZhBriefIsUnchanged(t *testing.T) {
	for _, c := range goldenCases() {
		c := c
		t.Run(c.file, func(t *testing.T) {
			got := masked(c.render(t, goldenOptions(t)))
			if os.Getenv("GOFER_GOLDEN_UPDATE") != "" {
				writeGolden(t, c.file, got)
				return
			}
			if want := readGolden(t, c.file); got != want {
				t.Errorf("%s changed\n--- got ---\n%s\n--- want ---\n%s", c.file, got, want)
			}
		})
	}
}

func goldenPath(file string) string { return filepath.Join("testdata", file) }

func readGolden(t *testing.T, file string) string {
	t.Helper()
	raw, err := os.ReadFile(goldenPath(file))
	if err != nil {
		t.Fatalf("read golden %s: %v", file, err)
	}
	return string(raw)
}

func writeGolden(t *testing.T, file, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(goldenPath(file)), 0o755); err != nil {
		t.Fatalf("create testdata: %v", err)
	}
	if err := os.WriteFile(goldenPath(file), []byte(text), 0o644); err != nil {
		t.Fatalf("write golden %s: %v", file, err)
	}
}

// failingHandoff is the fixture client of a plan whose handoff note cannot be read.
type failingHandoff struct{ *fakeClient }

func (f *failingHandoff) GetPlanHandoff(string, int) (client.PlanHandoff, error) {
	return client.PlanHandoff{}, errHandoffDown
}

var errHandoffDown = errors.New("dial tcp: connection refused")
