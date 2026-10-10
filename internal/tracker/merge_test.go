package tracker

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

const (
	tBase  = "2026-10-10T01:00:00Z"
	tOlder = "2026-10-10T02:00:00Z"
	tNewer = "2026-10-10T03:00:00Z"
)

func mIssue(id string, mod ...func(*Issue)) Issue {
	it := Issue{ID: id, Title: "title " + id, Type: "task", Status: "open", Priority: 2, CreatedAt: tBase, UpdatedAt: tBase}
	for _, f := range mod {
		f(&it)
	}
	return it
}

func jsonl[T any](t *testing.T, items ...T) []byte {
	t.Helper()
	var b bytes.Buffer
	for _, it := range items {
		line, err := json.Marshal(it)
		assert.Require(t, assert.NoErr(t, err))
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

func parseIssuesBytes(t *testing.T, b []byte) []Issue {
	t.Helper()
	items, err := parseIssues(bytes.NewReader(b), "merged")
	assert.Require(t, assert.NoErr(t, err))
	return items
}

func comment(at, text string) Comment { return Comment{At: at, By: "a", Text: text} }

func TestMergeIssuesRules(t *testing.T) {
	cBase := comment(tBase, "base")
	cOurs := comment(tNewer, "ours")
	cTheirs := comment(tOlder, "theirs")
	cases := []struct {
		name      string
		base      []Issue
		ours      []Issue
		theirs    []Issue
		want      []Issue
		conflicts []MergeConflict
	}{
		{
			name: "added on ours only is kept",
			base: []Issue{mIssue("a")}, ours: []Issue{mIssue("a"), mIssue("b")}, theirs: []Issue{mIssue("a")},
			want: []Issue{mIssue("a"), mIssue("b")},
		},
		{
			name: "added on theirs only is kept",
			base: []Issue{mIssue("b")}, ours: []Issue{mIssue("b")}, theirs: []Issue{mIssue("a"), mIssue("b")},
			want: []Issue{mIssue("a"), mIssue("b")},
		},
		{
			name: "added on both sides, different ids",
			base: nil, ours: []Issue{mIssue("x")}, theirs: []Issue{mIssue("y")},
			want: []Issue{mIssue("x"), mIssue("y")},
		},
		{
			name: "changed on ours only takes ours",
			base: []Issue{mIssue("a")}, ours: []Issue{mIssue("a", func(i *Issue) { i.Title = "new"; i.UpdatedAt = tOlder })}, theirs: []Issue{mIssue("a")},
			want: []Issue{mIssue("a", func(i *Issue) { i.Title = "new"; i.UpdatedAt = tOlder })},
		},
		{
			name: "changed on theirs only takes theirs even when older",
			base: []Issue{mIssue("a")}, ours: []Issue{mIssue("a")}, theirs: []Issue{mIssue("a", func(i *Issue) { i.Priority = 0 })},
			want: []Issue{mIssue("a", func(i *Issue) { i.Priority = 0 })},
		},
		{
			name: "deleted on ours, unchanged on theirs is deleted",
			base: []Issue{mIssue("a"), mIssue("b")}, ours: []Issue{mIssue("b")}, theirs: []Issue{mIssue("a"), mIssue("b")},
			want: []Issue{mIssue("b")},
		},
		{
			name: "deleted on theirs, unchanged on ours is deleted",
			base: []Issue{mIssue("a"), mIssue("b")}, ours: []Issue{mIssue("a"), mIssue("b")}, theirs: []Issue{mIssue("a")},
			want: []Issue{mIssue("a")},
		},
		{
			name: "deleted on both sides",
			base: []Issue{mIssue("a"), mIssue("b")}, ours: []Issue{mIssue("b")}, theirs: []Issue{mIssue("b")},
			want: []Issue{mIssue("b")},
		},
		{
			name: "deleted on ours, changed on theirs keeps the change",
			base: []Issue{mIssue("a")}, ours: nil, theirs: []Issue{mIssue("a", func(i *Issue) { i.Status = "closed" })},
			want:      []Issue{mIssue("a", func(i *Issue) { i.Status = "closed" })},
			conflicts: []MergeConflict{{Kind: "issue", ID: "a", Field: "deleted", Took: "theirs"}},
		},
		{
			name: "deleted on theirs, changed on ours keeps the change",
			base: []Issue{mIssue("a")}, ours: []Issue{mIssue("a", func(i *Issue) { i.Title = "kept" })}, theirs: nil,
			want:      []Issue{mIssue("a", func(i *Issue) { i.Title = "kept" })},
			conflicts: []MergeConflict{{Kind: "issue", ID: "a", Field: "deleted", Took: "ours"}},
		},
		{
			name:   "both changed different fields keeps both",
			base:   []Issue{mIssue("a")},
			ours:   []Issue{mIssue("a", func(i *Issue) { i.Title = "ours title"; i.UpdatedAt = tNewer })},
			theirs: []Issue{mIssue("a", func(i *Issue) { i.Priority = 1; i.UpdatedAt = tOlder })},
			want:   []Issue{mIssue("a", func(i *Issue) { i.Title = "ours title"; i.Priority = 1; i.UpdatedAt = tNewer })},
		},
		{
			name:      "both changed the same field: newer theirs wins",
			base:      []Issue{mIssue("a")},
			ours:      []Issue{mIssue("a", func(i *Issue) { i.Title = "ours"; i.UpdatedAt = tOlder })},
			theirs:    []Issue{mIssue("a", func(i *Issue) { i.Title = "theirs"; i.UpdatedAt = tNewer })},
			want:      []Issue{mIssue("a", func(i *Issue) { i.Title = "theirs"; i.UpdatedAt = tNewer })},
			conflicts: []MergeConflict{{Kind: "issue", ID: "a", Field: "title", Took: "theirs"}},
		},
		{
			name:      "both changed the same field: newer ours wins",
			base:      []Issue{mIssue("a")},
			ours:      []Issue{mIssue("a", func(i *Issue) { i.Design = "ours"; i.UpdatedAt = tNewer })},
			theirs:    []Issue{mIssue("a", func(i *Issue) { i.Design = "theirs"; i.UpdatedAt = tOlder })},
			want:      []Issue{mIssue("a", func(i *Issue) { i.Design = "ours"; i.UpdatedAt = tNewer })},
			conflicts: []MergeConflict{{Kind: "issue", ID: "a", Field: "design", Took: "ours"}},
		},
		{
			name:      "same updated_at: ours wins",
			base:      []Issue{mIssue("a")},
			ours:      []Issue{mIssue("a", func(i *Issue) { i.Owner = "o"; i.UpdatedAt = tOlder })},
			theirs:    []Issue{mIssue("a", func(i *Issue) { i.Owner = "t"; i.UpdatedAt = tOlder })},
			want:      []Issue{mIssue("a", func(i *Issue) { i.Owner = "o"; i.UpdatedAt = tOlder })},
			conflicts: []MergeConflict{{Kind: "issue", ID: "a", Field: "owner", Took: "ours"}},
		},
		{
			name:   "comments from both sides are unioned in time order without duplicates",
			base:   []Issue{mIssue("a", func(i *Issue) { i.Comments = []Comment{cBase} })},
			ours:   []Issue{mIssue("a", func(i *Issue) { i.Comments = []Comment{cBase, cOurs}; i.UpdatedAt = tNewer })},
			theirs: []Issue{mIssue("a", func(i *Issue) { i.Comments = []Comment{cBase, cTheirs}; i.UpdatedAt = tOlder })},
			want:   []Issue{mIssue("a", func(i *Issue) { i.Comments = []Comment{cBase, cTheirs, cOurs}; i.UpdatedAt = tNewer })},
		},
		{
			name: "notes are unioned like comments",
			base: []Issue{mIssue("a")},
			ours: []Issue{mIssue("a", func(i *Issue) {
				i.Notes = []NoteEntry{{At: tNewer, By: "o", Text: "n1"}}
				i.UpdatedAt = tNewer
			})},
			theirs: []Issue{mIssue("a", func(i *Issue) {
				i.Notes = []NoteEntry{{At: tOlder, By: "t", Text: "n2"}}
				i.UpdatedAt = tOlder
			})},
			want: []Issue{mIssue("a", func(i *Issue) {
				i.Notes = []NoteEntry{{At: tOlder, By: "t", Text: "n2"}, {At: tNewer, By: "o", Text: "n1"}}
				i.UpdatedAt = tNewer
			})},
		},
		{
			name:   "comment removed on one side stays removed",
			base:   []Issue{mIssue("a", func(i *Issue) { i.Comments = []Comment{cBase} })},
			ours:   []Issue{mIssue("a", func(i *Issue) { i.Comments = nil; i.UpdatedAt = tOlder })},
			theirs: []Issue{mIssue("a", func(i *Issue) { i.Comments = []Comment{cBase, cTheirs}; i.UpdatedAt = tOlder })},
			want:   []Issue{mIssue("a", func(i *Issue) { i.Comments = []Comment{cTheirs}; i.UpdatedAt = tOlder })},
		},
		{
			name: "tags and deps merge as sets against the base",
			base: []Issue{mIssue("a", func(i *Issue) { i.Tags = []string{"x", "y"}; i.Deps = []Dep{{ID: "d1", Type: "blocks"}} })},
			ours: []Issue{mIssue("a", func(i *Issue) { i.Tags = []string{"x", "y", "o"}; i.Deps = nil; i.UpdatedAt = tOlder })},
			theirs: []Issue{mIssue("a", func(i *Issue) {
				i.Tags = []string{"y", "t"}
				i.Deps = []Dep{{ID: "d1", Type: "blocks"}, {ID: "d2", Type: "related"}}
				i.UpdatedAt = tNewer
			})},
			want: []Issue{mIssue("a", func(i *Issue) {
				i.Tags = []string{"y", "o", "t"}
				i.Deps = []Dep{{ID: "d2", Type: "related"}}
				i.UpdatedAt = tNewer
			})},
		},
		{
			name: "status, assignee and close fields move together",
			base: []Issue{mIssue("a")},
			ours: []Issue{mIssue("a", func(i *Issue) {
				i.Status, i.Assignee, i.StartedAt, i.UpdatedAt = "in_progress", "agent-1", tOlder, tOlder
			})},
			theirs: []Issue{mIssue("a", func(i *Issue) {
				i.Status, i.ClosedAt, i.CloseReason, i.UpdatedAt = "closed", tNewer, "done", tNewer
			})},
			want: []Issue{mIssue("a", func(i *Issue) {
				i.Status, i.ClosedAt, i.CloseReason, i.UpdatedAt = "closed", tNewer, "done", tNewer
			})},
			conflicts: []MergeConflict{{Kind: "issue", ID: "a", Field: "status", Took: "theirs"}},
		},
		{
			name: "status changed on one side only keeps the whole group from that side",
			base: []Issue{mIssue("a")},
			ours: []Issue{mIssue("a", func(i *Issue) {
				i.Status, i.Assignee, i.StartedAt, i.UpdatedAt = "in_progress", "agent-1", tOlder, tOlder
			})},
			theirs: []Issue{mIssue("a", func(i *Issue) { i.Comments = []Comment{cTheirs}; i.UpdatedAt = tNewer })},
			want: []Issue{mIssue("a", func(i *Issue) {
				i.Status, i.Assignee, i.StartedAt = "in_progress", "agent-1", tOlder
				i.Comments = []Comment{cTheirs}
				i.UpdatedAt = tNewer
			})},
		},
		{
			name: "added on both sides identically",
			base: nil, ours: []Issue{mIssue("a")}, theirs: []Issue{mIssue("a")},
			want: []Issue{mIssue("a")},
		},
		{
			name:      "added on both sides differently merges field by field",
			base:      nil,
			ours:      []Issue{mIssue("a", func(i *Issue) { i.Title = "o"; i.Comments = []Comment{cOurs}; i.UpdatedAt = tOlder })},
			theirs:    []Issue{mIssue("a", func(i *Issue) { i.Title = "t"; i.Comments = []Comment{cTheirs}; i.UpdatedAt = tNewer })},
			want:      []Issue{mIssue("a", func(i *Issue) { i.Title = "t"; i.Comments = []Comment{cTheirs, cOurs}; i.UpdatedAt = tNewer })},
			conflicts: []MergeConflict{{Kind: "issue", ID: "a", Field: "title", Took: "theirs"}},
		},
		{
			name:      "RFC 3339 times compare as times, not strings",
			base:      []Issue{mIssue("a")},
			ours:      []Issue{mIssue("a", func(i *Issue) { i.Title = "o"; i.UpdatedAt = "2026-10-10T03:00:05Z" })},
			theirs:    []Issue{mIssue("a", func(i *Issue) { i.Title = "t"; i.UpdatedAt = "2026-10-10T03:00:05.1Z" })},
			want:      []Issue{mIssue("a", func(i *Issue) { i.Title = "t"; i.UpdatedAt = "2026-10-10T03:00:05.1Z" })},
			conflicts: []MergeConflict{{Kind: "issue", ID: "a", Field: "title", Took: "theirs"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, conflicts, err := MergeJSONL(MergeKindIssues, jsonl(t, tc.base...), jsonl(t, tc.ours...), jsonl(t, tc.theirs...))
			assert.Require(t, assert.NoErr(t, err))
			assert.Eq(t, string(jsonl(t, tc.want...)), string(out))
			assert.Eq(t, tc.conflicts, conflicts)
		})
	}
}

// The merged file is byte-for-byte what the store writes for the same records:
// sorted by id, one json.Marshal line each.
func TestMergeIssuesOutputMatchesStore(t *testing.T) {
	base := jsonl(t, mIssue("p-3"), mIssue("p-1"))
	ours := jsonl(t, mIssue("p-3"), mIssue("p-1"), mIssue("p-10", func(i *Issue) { i.Description = "<b>&</b>" }))
	theirs := jsonl(t, mIssue("p-2"), mIssue("p-3"), mIssue("p-1"))
	out, _, err := MergeJSONL(MergeKindIssues, base, ours, theirs)
	assert.Require(t, assert.NoErr(t, err))

	s := NewStore(t.TempDir())
	assert.Require(t, assert.NoErr(t, s.WriteIssues(parseIssuesBytes(t, out))))
	stored, err := os.ReadFile(filepath.Join(s.Dir, "issues.jsonl"))
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, string(stored), string(out))
	var ids []string
	for _, it := range parseIssuesBytes(t, out) {
		ids = append(ids, it.ID)
	}
	assert.Eq(t, []string{"p-1", "p-10", "p-2", "p-3"}, ids)
}

func mMemory(key, content, updated string, mod ...func(*Memory)) Memory {
	m := Memory{Key: key, Content: content, UpdatedAt: updated, By: "a"}
	for _, f := range mod {
		f(&m)
	}
	return m
}

func TestMergeMemoriesRules(t *testing.T) {
	flag := func(at, reason string) MemoryFlag { return MemoryFlag{At: at, By: "x", Reason: reason} }
	cases := []struct {
		name      string
		base      []Memory
		ours      []Memory
		theirs    []Memory
		want      []Memory
		conflicts []MergeConflict
	}{
		{
			name: "different keys from both sides",
			base: []Memory{mMemory("k", "c", tBase)}, ours: []Memory{mMemory("k", "c", tBase), mMemory("o", "o", tOlder)}, theirs: []Memory{mMemory("a", "a", tOlder), mMemory("k", "c", tBase)},
			want: []Memory{mMemory("a", "a", tOlder), mMemory("k", "c", tBase), mMemory("o", "o", tOlder)},
		},
		{
			name: "changed on one side",
			base: []Memory{mMemory("k", "c", tBase)}, ours: []Memory{mMemory("k", "c", tBase)}, theirs: []Memory{mMemory("k", "c2", tOlder)},
			want: []Memory{mMemory("k", "c2", tOlder)},
		},
		{
			name: "changed on both sides takes the newer record",
			base: []Memory{mMemory("k", "c", tBase)}, ours: []Memory{mMemory("k", "ours", tNewer)}, theirs: []Memory{mMemory("k", "theirs", tOlder, func(m *Memory) { m.Summary = "s" })},
			want:      []Memory{mMemory("k", "ours", tNewer)},
			conflicts: []MergeConflict{{Kind: "memory", ID: "k", Field: "record", Took: "ours"}},
		},
		{
			name: "same content keeps both sides' flags, newest first",
			base: []Memory{mMemory("k", "c", tBase)},
			ours: []Memory{mMemory("k", "c", tBase, func(m *Memory) { m.Flags = []MemoryFlag{flag(tOlder, "o")} })},
			theirs: []Memory{mMemory("k", "c", tNewer, func(m *Memory) {
				m.Summary = "s"
				m.Flags = []MemoryFlag{flag(tNewer, "t")}
			})},
			want: []Memory{mMemory("k", "c", tNewer, func(m *Memory) {
				m.Summary = "s"
				m.Flags = []MemoryFlag{flag(tNewer, "t"), flag(tOlder, "o")}
			})},
			conflicts: []MergeConflict{{Kind: "memory", ID: "k", Field: "record", Took: "theirs"}},
		},
		{
			name:      "a content rewrite drops the other side's flags",
			base:      []Memory{mMemory("k", "c", tBase)},
			ours:      []Memory{mMemory("k", "c", tBase, func(m *Memory) { m.Flags = []MemoryFlag{flag(tOlder, "o")} })},
			theirs:    []Memory{mMemory("k", "rewritten", tNewer)},
			want:      []Memory{mMemory("k", "rewritten", tNewer)},
			conflicts: []MergeConflict{{Kind: "memory", ID: "k", Field: "record", Took: "theirs"}},
		},
		{
			name: "deleted on one side, unchanged on the other",
			base: []Memory{mMemory("k", "c", tBase)}, ours: nil, theirs: []Memory{mMemory("k", "c", tBase)},
			want: nil,
		},
		{
			name: "deleted on one side, changed on the other",
			base: []Memory{mMemory("k", "c", tBase)}, ours: []Memory{mMemory("k", "c2", tOlder)}, theirs: nil,
			want:      []Memory{mMemory("k", "c2", tOlder)},
			conflicts: []MergeConflict{{Kind: "memory", ID: "k", Field: "deleted", Took: "ours"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, conflicts, err := MergeJSONL(MergeKindMemories, jsonl(t, tc.base...), jsonl(t, tc.ours...), jsonl(t, tc.theirs...))
			assert.Require(t, assert.NoErr(t, err))
			assert.Eq(t, string(jsonl(t, tc.want...)), string(out))
			assert.Eq(t, tc.conflicts, conflicts)
		})
	}
}

func TestMergeArchivedMemories(t *testing.T) {
	arch := func(content, archivedAt string) ArchivedMemory {
		return ArchivedMemory{Memory: mMemory("k", content, tBase), ArchivedAt: archivedAt}
	}
	out, conflicts, err := MergeJSONL(MergeKindMemoryArchive, nil, jsonl(t, arch("o", tNewer)), jsonl(t, arch("t", tOlder), ArchivedMemory{Memory: mMemory("z", "z", tBase), ArchivedAt: tBase}))
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, string(jsonl(t, arch("o", tNewer), ArchivedMemory{Memory: mMemory("z", "z", tBase), ArchivedAt: tBase})), string(out))
	assert.Eq(t, []MergeConflict{{Kind: "archived memory", ID: "k", Field: "record", Took: "ours"}}, conflicts)
}

func TestMergeRejectsUnsafeInput(t *testing.T) {
	good := jsonl(t, mIssue("a"))
	cases := map[string][]byte{
		"conflict markers": []byte("<<<<<<< ours\n" + string(good) + "=======\n>>>>>>> theirs\n"),
		"unknown field":    []byte(`{"id":"a","title":"t","type":"task","status":"open","priority":2,"created_at":"x","future_field":1}` + "\n"),
		"trailing data":    []byte(`{"id":"a","title":"t","type":"task","status":"open","priority":2,"created_at":"x"} {}` + "\n"),
		"duplicate id":     append(append([]byte{}, good...), good...),
		"missing id":       []byte(`{"title":"t"}` + "\n"),
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := MergeJSONL(MergeKindIssues, good, bad, good)
			assert.Err(t, err)
		})
	}
	_, _, err := MergeJSONL("sync-base", good, good, good)
	assert.Err(t, err)
}

func TestMergeKindDetection(t *testing.T) {
	assert.Eq(t, MergeKindIssues, MergeKindForPath(".gofer/tracker/issues.jsonl"))
	assert.Eq(t, MergeKindMemories, MergeKindForPath(`.gofer\tracker\memories.jsonl`))
	assert.Eq(t, MergeKindMemoryArchive, MergeKindForPath("memories-archive.jsonl"))
	assert.Eq(t, "", MergeKindForPath(".gofer/tracker/other.jsonl"))

	assert.Eq(t, MergeKindIssues, SniffMergeKind(nil, jsonl(t, mIssue("a"))))
	assert.Eq(t, MergeKindMemories, SniffMergeKind([]byte("\n"), jsonl(t, mMemory("k", "c", tBase))))
	assert.Eq(t, MergeKindMemoryArchive, SniffMergeKind(jsonl(t, ArchivedMemory{Memory: mMemory("k", "c", tBase), ArchivedAt: tBase})))
	assert.Eq(t, "", SniffMergeKind(nil, []byte("not json\n")))
	assert.Eq(t, "", SniffMergeKind(nil, nil))
}

func TestMergeConflictString(t *testing.T) {
	c := MergeConflict{Kind: "issue", ID: "a", Field: "title", Took: "theirs"}
	assert.True(t, strings.Contains(c.String(), "title changed on both sides, took theirs"))
	d := MergeConflict{Kind: "memory", ID: "k", Field: "deleted", Took: "ours"}
	assert.True(t, strings.Contains(d.String(), "deleted on theirs but changed on ours"))
}
