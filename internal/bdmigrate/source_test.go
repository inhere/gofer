package bdmigrate

import (
	"bytes"
	"strings"
	"testing"
)

func parseFixture(t *testing.T, text string) dataset {
	t.Helper()
	ds, err := parseRecords(bytes.NewReader([]byte(text)), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

func TestParseRecordsSplitsIssuesMemoriesAndSkipsOthers(t *testing.T) {
	ds := parseFixture(t, `Warning: something on stdout
{"_type":"issue","id":"d-1","title":"one"}
{"id":"d-2","title":"legacy line without _type"}
{"_type":"memory","key":"k","value":"v"}
{"_type":"gate","id":"g-1"}

`)
	if len(ds.issues) != 2 || ds.memories["k"] != "v" || ds.skipped["gate"] != 1 {
		t.Fatalf("dataset: %+v", ds)
	}
	if _, err := parseRecords(bytes.NewReader([]byte(`{"_type":"issue","title":"x"}`)), "f"); err == nil {
		t.Fatal("an issue without id must be an error")
	}
}

func TestParseBdMemoriesSkipsMetadata(t *testing.T) {
	got, err := parseBdMemories([]byte("Warning: schema skew\n" + `{"a":"one","schema_version":1,"b":"two","c":null}`))
	if err != nil || len(got) != 2 || got["a"] != "one" || got["b"] != "two" {
		t.Fatalf("memories = %#v, %v", got, err)
	}
	if _, err := parseBdMemories([]byte(`[1,2]`)); err == nil {
		t.Fatal("a non-object export must be an error")
	}
}

func TestConvertIssuesMapsEveryModelledField(t *testing.T) {
	ds := parseFixture(t, strings.Join([]string{
		`{"_type":"issue","id":"p-aaa","title":"epic","status":"open","priority":1,"issue_type":"epic","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`,
		`{"_type":"issue","id":"p-aaa.1","title":"child","description":"d","design":"ds","acceptance_criteria":"ac","status":"in_progress","priority":0,"issue_type":"bug","assignee":"ann","owner":"o@x","labels":["proj01"," proj01","b"],"notes":"a note","created_at":"2026-01-01T00:00:00Z","created_by":"cr","updated_at":"2026-01-03T00:00:00Z","started_at":"2026-01-02T00:00:00Z","external_ref":"EXT-1","spec_id":"docs/spec.md","lease_expires_at":"2026-01-03T01:00:00Z","heartbeat_at":"2026-01-03T00:55:00Z","dependencies":[{"issue_id":"p-aaa.1","depends_on_id":"p-aaa","type":"parent-child","created_at":"2026-01-01T00:00:00Z","metadata":"{}"},{"issue_id":"p-aaa.1","depends_on_id":"p-bbb","type":"blocks","metadata":"{}"},{"issue_id":"p-aaa.1","depends_on_id":"gone-1","type":"related","metadata":"{}"}],"comments":[{"id":"1","issue_id":"p-aaa.1","author":"rev","text":"looks fine","created_at":"2026-01-02T12:00:00Z"}]}`,
		`{"_type":"issue","id":"p-bbb","title":"closed","status":"closed","priority":2,"issue_type":"","closed_at":"2026-01-04T00:00:00Z","close_reason":"done","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-04T00:00:00Z"}`,
		`{"_type":"issue","id":"p-ccc","title":"odd status","status":"deferred","priority":3,"issue_type":"task","created_at":"2026-01-01T00:00:00Z"}`,
		`{"_type":"issue","id":"p-ddd.7","title":"dotted without edge","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z"}`,
		`{"_type":"issue","id":"p-ddd","title":"dotted parent","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z"}`,
	}, "\n"))
	issues, m := convertIssues(ds.issues)
	byID := map[string]int{}
	for i, it := range issues {
		byID[it.ID] = i
	}
	child := issues[byID["p-aaa.1"]]
	if child.Parent != "p-aaa" || child.Type != "bug" || child.Priority != 0 || child.Assignee != "ann" || child.Owner != "o@x" || child.Design != "ds" || child.AcceptanceCriteria != "ac" || child.ExternalRef != "EXT-1" || child.SpecID != "docs/spec.md" || child.StartedAt == "" || child.CreatedBy != "cr" {
		t.Fatalf("child: %+v", child)
	}
	if strings.Join(child.Tags, ",") != "proj01,b" {
		t.Fatalf("labels must map to tags (deduped, trimmed): %v", child.Tags)
	}
	if len(child.Notes) != 1 || child.Notes[0].Text != "a note" || len(child.Comments) != 1 || child.Comments[0].By != "rev" || child.Comments[0].At != "2026-01-02T12:00:00Z" {
		t.Fatalf("notes/comments: %+v / %+v", child.Notes, child.Comments)
	}
	// The parent-child edge became the parent field; the other edges stay.
	if len(child.Deps) != 2 || child.Deps[0].ID != "p-bbb" || child.Deps[0].Type != "blocks" || child.Deps[1].Type != "related" {
		t.Fatalf("deps: %+v", child.Deps)
	}
	closed := issues[byID["p-bbb"]]
	if closed.Status != "closed" || closed.CloseReason != "done" || closed.ClosedAt == "" || closed.Type != "task" {
		t.Fatalf("closed: %+v", closed)
	}
	odd := issues[byID["p-ccc"]]
	if odd.Status != "open" || strings.Join(odd.Tags, ",") != "bd:deferred" || odd.UpdatedAt != odd.CreatedAt {
		t.Fatalf("unknown status: %+v", odd)
	}
	if issues[byID["p-ddd.7"]].Parent != "p-ddd" || m.ParentFromDotID != 1 || m.ParentFromDep != 1 {
		t.Fatalf("parent mapping: %+v", m)
	}
	if m.LeasesDropped != 1 || m.DanglingDeps != 1 || m.DepMetaDropped != 1 || m.StatusRemapped["deferred"] != 1 {
		t.Fatalf("mapping stats: %+v", m)
	}
}

func TestConvertIssuesKeepsExtraParentEdgesAndPrefersDottedParent(t *testing.T) {
	ds := parseFixture(t, strings.Join([]string{
		`{"_type":"issue","id":"p-a","title":"a","status":"open","priority":2,"issue_type":"epic","created_at":"x"}`,
		`{"_type":"issue","id":"p-b","title":"b","status":"open","priority":2,"issue_type":"epic","created_at":"x"}`,
		`{"_type":"issue","id":"p-a.1","title":"c","status":"open","priority":2,"issue_type":"task","created_at":"x","dependencies":[{"depends_on_id":"p-b","type":"parent-child"},{"depends_on_id":"p-a","type":"parent-child"}]}`,
	}, "\n"))
	issues, m := convertIssues(ds.issues)
	c := issues[1] // sorted: p-a, p-a.1, p-b
	if c.Parent != "p-a" || len(c.Deps) != 1 || c.Deps[0].ID != "p-b" || c.Deps[0].Type != "parent-child" || m.ExtraParentDeps != 1 {
		t.Fatalf("multi-parent: %+v %+v", c, m)
	}
}

func TestInferPrefix(t *testing.T) {
	ds := parseFixture(t, `{"id":"h-aii-0hl8"}
{"id":"h-aii-0hl8.4"}
{"id":"h-aii-zzzz"}
{"id":"tools-ab1"}`)
	if got := inferPrefix(ds.issues); got != "h-aii" {
		t.Fatalf("prefix %q", got)
	}
	if inferPrefix(nil) != "" {
		t.Fatal("no issues, no prefix")
	}
}

func TestCompareSources(t *testing.T) {
	live := parseFixture(t, `{"id":"a","updated_at":"2"}
{"id":"b","updated_at":"1"}
{"id":"c","updated_at":"1"}`)
	file := parseFixture(t, `{"id":"b","updated_at":"1"}
{"id":"c","updated_at":"0"}
{"id":"gone","updated_at":"1"}`)
	var rep SourceReport
	compareSources(live, file, &rep)
	if strings.Join(rep.OnlyLive, ",") != "a" || strings.Join(rep.OnlyJSONL, ",") != "gone" || strings.Join(rep.NewerLive, ",") != "c" || !rep.Stale {
		t.Fatalf("compare: %+v", rep)
	}
}
