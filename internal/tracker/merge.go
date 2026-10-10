package tracker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

// Three-way merge of the tracker JSONL files by record id (gofer-7rqx). It backs
// the git merge driver (`gofer repo merge-driver`, merge_driver.go): parallel
// worktrees that each claim / comment on issues touch adjacent lines, which git's
// line merge reports as conflicts although the records merge cleanly.
//
// Rules, per record (issue id / memory key):
//   - added on one side → kept; deleted on one side and unchanged on the other → deleted;
//   - changed on one side only → that side;
//   - deleted on one side, changed on the other → the changed record is kept (reported);
//   - changed on both sides → issues merge field by field (a field changed on one
//     side takes that side; changed on both takes the side with the newer
//     updated_at, ours on a tie); comments / notes / tags / deps are merged as sets
//     against the base (additions from both sides kept, removals honored, comments
//     and notes ordered by time). Memories and archived memories take the whole
//     newer record; a memory keeps both sides' flags when its content is the same.
//
// The output is what the store itself writes: records sorted by id / key, one
// json.Marshal line each. A line the merge cannot read (bad JSON, conflict
// markers, a field this version does not know) is an error: the caller then falls
// back to a plain text merge rather than dropping data.

// Tracker JSONL kinds the merge understands.
const (
	MergeKindIssues        = "issues"
	MergeKindMemories      = "memories"
	MergeKindMemoryArchive = "memories-archive"
)

// MergeKindForPath maps a tracker file name to its merge kind ("" = unknown).
func MergeKindForPath(p string) string {
	switch path.Base(strings.ReplaceAll(p, "\\", "/")) {
	case "issues.jsonl":
		return MergeKindIssues
	case "memories.jsonl":
		return MergeKindMemories
	case memoryArchiveFile:
		return MergeKindMemoryArchive
	}
	return ""
}

// SniffMergeKind guesses the kind from the first record found in data (used when
// git does not pass the path). "" when every input is empty or unrecognized.
func SniffMergeKind(data ...[]byte) string {
	for _, d := range data {
		for _, line := range bytes.Split(d, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(line, &fields) != nil {
				return ""
			}
			switch {
			case fields["archived_at"] != nil && fields["key"] != nil:
				return MergeKindMemoryArchive
			case fields["id"] != nil:
				return MergeKindIssues
			case fields["key"] != nil:
				return MergeKindMemories
			}
			return ""
		}
	}
	return ""
}

// MergeConflict is one decision the merge had to make between two real changes.
// The merge still succeeds; the driver prints these so a person can check them.
type MergeConflict struct {
	// Kind is "issue", "memory" or "archived memory".
	Kind string `json:"kind"`
	ID   string `json:"id"`
	// Field is the issue field (or "status" for the status / assignee / started /
	// closed group), "record" for a whole memory, or "deleted" for delete vs change.
	Field string `json:"field"`
	// Took is "ours" or "theirs".
	Took string `json:"took"`
}

func (c MergeConflict) String() string {
	if c.Field == "deleted" {
		other := "theirs"
		if c.Took == "theirs" {
			other = "ours"
		}
		return fmt.Sprintf("%s %s: deleted on %s but changed on %s, kept the changed record", c.Kind, c.ID, other, c.Took)
	}
	return fmt.Sprintf("%s %s: %s changed on both sides, took %s (newer updated_at)", c.Kind, c.ID, c.Field, c.Took)
}

// MergeJSONL merges the base / ours / theirs contents of one tracker file of the
// given kind. An error means some input could not be read safely.
func MergeJSONL(kind string, base, ours, theirs []byte) ([]byte, []MergeConflict, error) {
	switch kind {
	case MergeKindIssues:
		return mergeFile(base, ours, theirs, "issue", func(i Issue) string { return i.ID }, mergeIssue)
	case MergeKindMemories:
		return mergeFile(base, ours, theirs, "memory", func(m Memory) string { return m.Key }, mergeMemory)
	case MergeKindMemoryArchive:
		return mergeFile(base, ours, theirs, "archived memory", func(m ArchivedMemory) string { return m.Key }, mergeArchivedMemory)
	}
	return nil, nil, fmt.Errorf("unknown tracker file kind %q", kind)
}

// mergeBoth merges a record changed differently on both sides. hasBase is false
// when both sides added the same id; base is then the zero value. It returns the
// merged record and the fields where both sides' changes collided, plus whether
// ours won those collisions.
type mergeBoth[T any] func(base T, hasBase bool, ours, theirs T) (T, []string, bool)

func mergeFile[T any](base, ours, theirs []byte, kind string, key func(T) string, both mergeBoth[T]) ([]byte, []MergeConflict, error) {
	bm, err := parseMergeSide(base, "base", key)
	if err != nil {
		return nil, nil, err
	}
	om, err := parseMergeSide(ours, "ours", key)
	if err != nil {
		return nil, nil, err
	}
	tm, err := parseMergeSide(theirs, "theirs", key)
	if err != nil {
		return nil, nil, err
	}
	ids := make(map[string]bool, len(om)+len(tm))
	for _, m := range []map[string]mergeRecord[T]{bm, om, tm} {
		for id := range m {
			ids[id] = true
		}
	}
	keys := make([]string, 0, len(ids))
	for id := range ids {
		keys = append(keys, id)
	}
	// The store sorts by id / key with Go string order; so does the merge.
	sort.Strings(keys)

	var out bytes.Buffer
	var conflicts []MergeConflict
	emit := func(r mergeRecord[T]) { out.Write(r.line); out.WriteByte('\n') }
	for _, id := range keys {
		b, bok := bm[id]
		o, ook := om[id]
		t, tok := tm[id]
		switch {
		case !ook && !tok:
			// deleted on both sides (or on one side with the other never having it)
		case ook && !tok:
			if !bok {
				emit(o) // added on ours
			} else if !bytes.Equal(o.line, b.line) {
				emit(o) // theirs deleted, ours changed: keep the change
				conflicts = append(conflicts, MergeConflict{Kind: kind, ID: id, Field: "deleted", Took: "ours"})
			}
		case !ook && tok:
			if !bok {
				emit(t)
			} else if !bytes.Equal(t.line, b.line) {
				emit(t)
				conflicts = append(conflicts, MergeConflict{Kind: kind, ID: id, Field: "deleted", Took: "theirs"})
			}
		case bytes.Equal(o.line, t.line):
			emit(o)
		case bok && bytes.Equal(o.line, b.line):
			emit(t)
		case bok && bytes.Equal(t.line, b.line):
			emit(o)
		default:
			merged, fields, oursWon := both(b.item, bok, o.item, t.item)
			line, err := json.Marshal(merged)
			if err != nil {
				return nil, nil, err
			}
			emit(mergeRecord[T]{line: line})
			took := "theirs"
			if oursWon {
				took = "ours"
			}
			for _, f := range fields {
				conflicts = append(conflicts, MergeConflict{Kind: kind, ID: id, Field: f, Took: took})
			}
		}
	}
	return out.Bytes(), conflicts, nil
}

// mergeRecord is a parsed record plus its canonical line (json.Marshal of the
// typed value, i.e. exactly what the store writes). Equality is by that line.
type mergeRecord[T any] struct {
	item T
	line []byte
}

// parseMergeSide reads one side strictly: unknown fields (a newer gofer wrote the
// file), trailing data, conflict markers, a missing or duplicate key are errors.
func parseMergeSide[T any](data []byte, side string, key func(T) string) (map[string]mergeRecord[T], error) {
	out := map[string]mergeRecord[T]{}
	err := scanLines(bytes.NewReader(data), side, func(line []byte) error {
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		var item T
		if err := dec.Decode(&item); err != nil {
			return err
		}
		if dec.InputOffset() != int64(len(line)) {
			return errors.New("trailing data after the record")
		}
		id := key(item)
		if id == "" {
			return errors.New("record without id / key")
		}
		if _, dup := out[id]; dup {
			return fmt.Errorf("duplicate record %q", id)
		}
		canonical, err := json.Marshal(item)
		if err != nil {
			return err
		}
		out[id] = mergeRecord[T]{item: item, line: canonical}
		return nil
	})
	return out, err
}

// statusGroup changes together (claim, close, reopen), so it merges as one value:
// mixing one side's status with the other side's closed_at would be inconsistent.
type statusGroup struct {
	Status, Assignee, StartedAt, ClosedAt, CloseReason string
}

func issueStatus(i Issue) statusGroup {
	return statusGroup{i.Status, i.Assignee, i.StartedAt, i.ClosedAt, i.CloseReason}
}

func mergeIssue(b Issue, _ bool, o, t Issue) (Issue, []string, bool) {
	oursWins := !timeAfter(IssueTouchedAt(t), IssueTouchedAt(o))
	var fields []string
	m := Issue{ID: o.ID}
	m.Title = pick3("title", b.Title, o.Title, t.Title, oursWins, &fields)
	m.Type = pick3("type", b.Type, o.Type, t.Type, oursWins, &fields)
	m.Priority = pick3("priority", b.Priority, o.Priority, t.Priority, oursWins, &fields)
	m.Description = pick3("description", b.Description, o.Description, t.Description, oursWins, &fields)
	m.Design = pick3("design", b.Design, o.Design, t.Design, oursWins, &fields)
	m.AcceptanceCriteria = pick3("acceptance_criteria", b.AcceptanceCriteria, o.AcceptanceCriteria, t.AcceptanceCriteria, oursWins, &fields)
	m.Notes = sortedByTime(mergeSet(b.Notes, o.Notes, t.Notes), func(n NoteEntry) string { return n.At })
	m.Owner = pick3("owner", b.Owner, o.Owner, t.Owner, oursWins, &fields)
	m.Tags = mergeSet(b.Tags, o.Tags, t.Tags)
	m.Parent = pick3("parent", b.Parent, o.Parent, t.Parent, oursWins, &fields)
	m.Deps = mergeSet(b.Deps, o.Deps, t.Deps)
	m.Comments = sortedByTime(mergeSet(b.Comments, o.Comments, t.Comments), func(c Comment) string { return c.At })
	m.CreatedAt = pick3("created_at", b.CreatedAt, o.CreatedAt, t.CreatedAt, oursWins, &fields)
	m.CreatedBy = pick3("created_by", b.CreatedBy, o.CreatedBy, t.CreatedBy, oursWins, &fields)
	st := pick3("status", issueStatus(b), issueStatus(o), issueStatus(t), oursWins, &fields)
	m.Status, m.Assignee, m.StartedAt, m.ClosedAt, m.CloseReason = st.Status, st.Assignee, st.StartedAt, st.ClosedAt, st.CloseReason
	m.ExternalRef = pick3("external_ref", b.ExternalRef, o.ExternalRef, t.ExternalRef, oursWins, &fields)
	m.SpecID = pick3("spec_id", b.SpecID, o.SpecID, t.SpecID, oursWins, &fields)
	// Both sides changed the record: the merge is as new as the newer side.
	m.UpdatedAt = o.UpdatedAt
	if timeAfter(t.UpdatedAt, o.UpdatedAt) {
		m.UpdatedAt = t.UpdatedAt
	}
	return m, fields, oursWins
}

func mergeMemory(b Memory, _ bool, o, t Memory) (Memory, []string, bool) {
	oursWins := !timeAfter(t.UpdatedAt, o.UpdatedAt)
	m, other := o, t
	if !oursWins {
		m, other = t, o
	}
	// A flag leaves updated_at alone and a content rewrite clears the flags, so
	// with the same content both sides' flags still describe it: keep them all.
	if m.Content == other.Content {
		m.Flags = mergeMemoryFlags(b.Flags, o.Flags, t.Flags)
	}
	return m, []string{"record"}, oursWins
}

func mergeMemoryFlags(b, o, t []MemoryFlag) []MemoryFlag {
	flags := mergeSet(b, o, t)
	// newest first, at most MemoryFlagsMax, like AddMemoryFlag
	sort.SliceStable(flags, func(i, j int) bool { return timeAfter(flags[i].At, flags[j].At) })
	if len(flags) > MemoryFlagsMax {
		flags = flags[:MemoryFlagsMax]
	}
	return flags
}

func mergeArchivedMemory(_ ArchivedMemory, _ bool, o, t ArchivedMemory) (ArchivedMemory, []string, bool) {
	oursWins := !timeAfter(archivedTouchedAt(t), archivedTouchedAt(o))
	if oursWins {
		return o, []string{"record"}, true
	}
	return t, []string{"record"}, false
}

func archivedTouchedAt(m ArchivedMemory) string {
	if timeAfter(m.UpdatedAt, m.ArchivedAt) {
		return m.UpdatedAt
	}
	return m.ArchivedAt
}

// pick3 is the three-way merge of one value. A collision (both sides changed it
// differently) is recorded under field and resolved by oursWins.
func pick3[T comparable](field string, b, o, t T, oursWins bool, fields *[]string) T {
	switch {
	case o == t:
		return o
	case o == b:
		return t
	case t == b:
		return o
	}
	*fields = append(*fields, field)
	if oursWins {
		return o
	}
	return t
}

// mergeSet merges list-valued fields as sets against the base: an element either
// side removed is gone, every element either side has (and did not remove) stays,
// once. Order: ours, then the elements only theirs has.
func mergeSet[T comparable](b, o, t []T) []T {
	removed := map[T]bool{}
	for _, side := range [][]T{o, t} {
		has := make(map[T]bool, len(side))
		for _, v := range side {
			has[v] = true
		}
		for _, v := range b {
			if !has[v] {
				removed[v] = true
			}
		}
	}
	var out []T
	seen := map[T]bool{}
	for _, side := range [][]T{o, t} {
		for _, v := range side {
			if removed[v] || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// sortedByTime orders comments / notes chronologically (stable for equal times).
func sortedByTime[T any](items []T, at func(T) string) []T {
	sort.SliceStable(items, func(i, j int) bool { return timeAfter(at(items[j]), at(items[i])) })
	return items
}

// timeAfter reports whether timestamp a is later than b. RFC 3339 values are
// compared as times (RFC3339Nano drops trailing zeros, so "…05Z" sorts after
// "…05.1Z" as a string); anything else falls back to string order.
func timeAfter(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339Nano, a)
	tb, errB := time.Parse(time.RFC3339Nano, b)
	if errA == nil && errB == nil {
		return ta.After(tb)
	}
	return a > b
}
