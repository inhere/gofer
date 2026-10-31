package sources

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/memorysynthesis"
	"github.com/inhere/gofer/internal/today"
	"github.com/inhere/gofer/internal/tracker"
)

var testLoc = time.FixedZone("T", 8*3600)

const testDay = "2026-10-28"

// at returns a unix second on the test day (local clock hh:mm in +08:00); dayOffset moves whole days.
func at(dayOffset, hh, mm int) int64 {
	return time.Date(2026, 10, 28+dayOffset, hh, mm, 0, 0, testLoc).Unix()
}

func cfg() Config { return Config{Loc: testLoc, Lookback: 24 * time.Hour} }

func collect(t *testing.T, s memorysynthesis.Source, project string) []memorysynthesis.Item {
	t.Helper()
	items, err := s.Collect(context.Background(), project, testDay)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func assertItems(t *testing.T, got, want []memorysynthesis.Item) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("items differ\n got: %#v\nwant: %#v", got, want)
	}
}

type fakeWork struct {
	items   []jobstore.WorkItem
	journal map[string][]jobstore.WorkJournalEntry
}

func (f fakeWork) ListWorkItems(jobstore.WorkListOpts) ([]jobstore.WorkItem, error) {
	return f.items, nil
}
func (f fakeWork) ListWorkJournalLevel(id string, _ int, _ int64, _ string) ([]jobstore.WorkJournalEntry, error) {
	return f.journal[id], nil
}

func TestWorkSourceGolden(t *testing.T) {
	store := fakeWork{
		items: []jobstore.WorkItem{
			{ID: "w-1", ProjectKey: "proj", Title: "Ship feature", Goal: "land it", Status: "active", NextStep: "write tests", LastActivityAt: at(0, 10, 0)},
			{ID: "w-old", ProjectKey: "proj", Title: "Yesterday", Status: "done", LastActivityAt: at(-1, 23, 59), UpdatedAt: at(-1, 23, 59)},
			{ID: "w-next", ProjectKey: "proj", Title: "Tomorrow", Status: "active", LastActivityAt: at(1, 0, 0)},
			{ID: "w-other", ProjectKey: "other", Title: "Hidden", LastActivityAt: at(0, 9, 0)},
			{ID: "w-merged", ProjectKey: "proj", Title: "Merged", MergedInto: "w-1", LastActivityAt: at(0, 9, 0)},
		},
		journal: map[string][]jobstore.WorkJournalEntry{
			"w-1":   {{ID: 7, Text: "reported progress", At: at(0, 11, 0)}, {ID: 3, Text: "too early", At: at(-1, 12, 0)}},
			"w-old": {{ID: 9, Text: "journal line today", At: at(0, 0, 0)}},
		},
	}
	got := collect(t, Work{Config: cfg(), Store: store}, "proj")
	assertItems(t, got, []memorysynthesis.Item{
		{Kind: KindWorkItem, OccurredAt: "2026-10-28T10:00:00+08:00", Title: "Ship feature", SourceRef: "work:w-1",
			Text: "status: active\ngoal: land it\nnext_step: write tests"},
		{Kind: KindWorkJournal, OccurredAt: "2026-10-28T11:00:00+08:00", Title: "Ship feature", Text: "reported progress", SourceRef: "work:w-1#journal-7"},
		{Kind: KindWorkItem, OccurredAt: "2026-10-27T23:59:00+08:00", Title: "Yesterday", SourceRef: "work:w-old", Text: "status: done"},
		{Kind: KindWorkJournal, OccurredAt: "2026-10-28T00:00:00+08:00", Title: "Yesterday", Text: "journal line today", SourceRef: "work:w-old#journal-9"},
	})
}

type fakeJobs struct{ recs []jobstore.JobRecord }

func (f fakeJobs) ListJobs(jobstore.ListQuery) ([]jobstore.JobRecord, error) { return f.recs, nil }

func TestJobsSourceGolden(t *testing.T) {
	recs := []jobstore.JobRecord{
		{ID: "j-1", ProjectKey: "proj", Status: "done", EndedAt: at(0, 9, 30), RequestJSON: `{"prompt":"SECRET-PROMPT"}`, RenderedCommand: "argv-secret"},
		{ID: "j-2", ProjectKey: "proj", Status: "failed", EndedAt: at(0, 23, 59), Error: "exit 2"},
		{ID: "j-run", ProjectKey: "proj", Status: "running", EndedAt: 0},
		{ID: "j-next", ProjectKey: "proj", Status: "done", EndedAt: at(1, 0, 0)},
		{ID: "j-other", ProjectKey: "other", Status: "done", EndedAt: at(0, 9, 0)},
	}
	tail := func(rec jobstore.JobRecord, max int64) string {
		if rec.ID == "j-1" {
			return "l1\nl2\nl3\nl4\n"
		}
		return ""
	}
	got := collect(t, Jobs{Config: cfg(), Store: fakeJobs{recs}, ReportTail: tail, TailLines: 2}, "proj")
	assertItems(t, got, []memorysynthesis.Item{
		{Kind: KindJobReport, OccurredAt: "2026-10-28T09:30:00+08:00", Title: "job j-1 done", SourceRef: "job:j-1#report", Text: "status: done\nreport: l3\nl4"},
		{Kind: KindJobReport, OccurredAt: "2026-10-28T23:59:00+08:00", Title: "job j-2 failed", SourceRef: "job:j-2#report", Text: "status: failed\nerror: exit 2"},
	})
	for _, it := range got {
		if strings.Contains(it.Text, "SECRET-PROMPT") || strings.Contains(it.Text, "argv-secret") {
			t.Fatalf("prompt or argv leaked: %+v", it)
		}
	}
}

type fakePlans struct {
	plans []jobstore.Plan
	todos map[string][]jobstore.PlanTodo
}

func (f fakePlans) ListPlans(jobstore.PlanFilter) ([]jobstore.Plan, error) { return f.plans, nil }
func (f fakePlans) ListTodosByPlan(id string) ([]jobstore.PlanTodo, error) { return f.todos[id], nil }

func TestPlansSourceGolden(t *testing.T) {
	store := fakePlans{
		plans: []jobstore.Plan{
			{PlanID: "p-1", Title: "Plan one", Status: "active", Description: "do it", ProjectKey: "proj", UpdatedAt: at(-3, 8, 0)},
			{PlanID: "p-quiet", Title: "Quiet", ProjectKey: "proj", UpdatedAt: at(-3, 8, 0)},
			{PlanID: "p-other", Title: "Other", ProjectKey: "other", UpdatedAt: at(0, 8, 0)},
		},
		todos: map[string][]jobstore.PlanTodo{
			"p-1": {
				{TodoID: "t-1", Title: "Step one", Status: "done", Note: "all green", Acceptance: "tests pass", DoneAt: at(0, 15, 0), UpdatedAt: at(0, 15, 0)},
				{TodoID: "t-old", Title: "Old", Status: "done", DoneAt: at(-2, 15, 0), UpdatedAt: at(-2, 15, 0)},
				{TodoID: "t-x", Title: "Other project", ProjectKey: "other", DoneAt: at(0, 16, 0)},
			},
			"p-quiet": {{TodoID: "t-q", Title: "Quiet todo", DoneAt: at(-2, 1, 0), UpdatedAt: at(-2, 1, 0)}},
		},
	}
	got := collect(t, Plans{Config: cfg(), Store: store}, "proj")
	assertItems(t, got, []memorysynthesis.Item{
		{Kind: KindPlan, OccurredAt: "2026-10-25T08:00:00+08:00", Title: "Plan one", SourceRef: "plan:p-1", Text: "status: active\ndescription: do it"},
		{Kind: KindPlanTodo, OccurredAt: "2026-10-28T15:00:00+08:00", Title: "Step one", SourceRef: "todo:t-1",
			Text: "plan: p-1\nstatus: done\nconclusion: all green\nacceptance: tests pass"},
	})
}

type fakeDoctor struct {
	findings []today.MemoryFinding
	sugs     []today.MemorySuggestion
}

func (f fakeDoctor) MemoryFindings(today.MemoryFindingsQuery) ([]today.MemoryFinding, error) {
	return f.findings, nil
}
func (f fakeDoctor) ListMemorySuggestions(string) ([]today.MemorySuggestion, error) {
	return f.sugs, nil
}

func TestDoctorSourceGolden(t *testing.T) {
	store := fakeDoctor{
		findings: []today.MemoryFinding{
			{TrackerID: "tr-1", ProjectKey: "proj", Key: "note-a", Kind: "note", Summary: "about a", Age: "9d", Content: "FULL-BODY-TEXT",
				Findings: []tracker.DoctorFinding{{Slug: "note-stale", Detail: "old"}}, Actions: []string{"archive"}},
			{TrackerID: "tr-2", ProjectKey: "other", Key: "note-b"},
		},
		sugs: []today.MemorySuggestion{
			{ID: 4, ProjectKey: "proj", Key: "note-a", Action: "archive", State: "pending", Text: "suggest archive", Reason: "stale", CreatedAt: at(0, 7, 0)},
			{ID: 5, ProjectKey: "other", Key: "x"},
		},
	}
	got := collect(t, Doctor{Config: cfg(), Reader: store}, "proj")
	assertItems(t, got, []memorysynthesis.Item{
		{Kind: KindMemoryFind, Title: "note-a", SourceRef: "memory:tr-1/note-a",
			Text: "kind: note\nsummary: about a\nfindings: note-stale old\nopen_actions: archive"},
		{Kind: KindMemorySugg, OccurredAt: "2026-10-28T07:00:00+08:00", Title: "note-a", SourceRef: "memory-suggestion:4",
			Text: "action: archive\nstate: pending\ntext: suggest archive\nreason: stale"},
	})
	for _, it := range got {
		if strings.Contains(it.Text, "FULL-BODY-TEXT") || strings.Contains(it.Text, "9d") {
			t.Fatalf("memory body or relative age leaked: %+v", it)
		}
	}
}

type fakePrev struct {
	run jobstore.MemorySynthesisRun
	ok  bool
}

func (f fakePrev) LatestSucceededMemorySynthesis(string, string) (jobstore.MemorySynthesisRun, bool, error) {
	return f.run, f.ok, nil
}

func TestPreviousSourceGolden(t *testing.T) {
	run := jobstore.MemorySynthesisRun{ID: "ms-1", ProjectKey: "proj", Day: "2026-10-27", ResultJSON: "abcdefghij", FinishedAt: at(-1, 23, 0)}
	got := collect(t, Previous{Config: cfg(), Store: fakePrev{run, true}, MaxRunes: 4}, "proj")
	assertItems(t, got, []memorysynthesis.Item{
		{Kind: KindPrevious, OccurredAt: "2026-10-27T23:00:00+08:00", Title: "synthesis 2026-10-27", SourceRef: "synthesis:ms-1", Text: "abcd"},
	})
	if got := collect(t, Previous{Config: cfg(), Store: fakePrev{}}, "proj"); len(got) != 0 {
		t.Fatalf("no previous run must yield nothing: %+v", got)
	}
	run.ProjectKey = "other"
	if got := collect(t, Previous{Config: cfg(), Store: fakePrev{run, true}}, "proj"); len(got) != 0 {
		t.Fatalf("another project's run must not leak: %+v", got)
	}
}

// TestSourcesBuildDeterministicEnvelope runs every adapter through Build twice (the second
// time with the sources in another order) and expects an identical digest.
func TestSourcesBuildDeterministicEnvelope(t *testing.T) {
	w := Work{Config: cfg(), Store: fakeWork{items: []jobstore.WorkItem{{ID: "w-1", ProjectKey: "proj", Title: "T", LastActivityAt: at(0, 1, 0)}}}}
	j := Jobs{Config: cfg(), Store: fakeJobs{[]jobstore.JobRecord{{ID: "j", ProjectKey: "proj", Status: "done", EndedAt: at(0, 2, 0)}}}}
	build := func(srcs ...memorysynthesis.Source) memorysynthesis.Materials {
		items, err := memorysynthesis.Collect(context.Background(), "proj", testDay, srcs...)
		if err != nil {
			t.Fatal(err)
		}
		m, err := memorysynthesis.Build("proj", testDay, "+08:00", items, memorysynthesis.Options{})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	a, b := build(w, j), build(j, w)
	if a.InputDigest != b.InputDigest || len(a.Items) != 2 || a.Items[0].ID != "m-001" {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
}
