package tracker

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// SyncSnapshot is the versioned local view exchanged with the tracker mirror.
// Memories remain deletion-free in the repository; server-side tombstones are
// represented by ServerMemory and are folded into the snapshot during sync.
type SyncSnapshot struct {
	Issues   []Issue  `json:"issues"`
	Memories []Memory `json:"memories"`
}

type ServerMemory struct {
	Memory
	Deleted   bool   `json:"deleted"`
	DeletedAt string `json:"deleted_at,omitempty"`
	DeletedBy string `json:"deleted_by,omitempty"`
}

type SyncConflict struct {
	Kind  string `json:"kind"`
	Key   string `json:"key"`
	Field string `json:"field"`
	Took  string `json:"took"`
	Other string `json:"other"`
}

type SyncReport struct {
	Conflicts []SyncConflict `json:"conflicts,omitempty"`
	Summary   string         `json:"summary,omitempty"`
	// Rejected counts pushed records the server refused as stale (merged and
	// pushed again within the same sync).
	Rejected int `json:"rejected,omitempty"`
	// RepairedToServer / RepairedToLocal count the records the one-time repair
	// (first sync with rev tracking) settled in each direction.
	RepairedToServer int `json:"repaired_to_server,omitempty"`
	RepairedToLocal  int `json:"repaired_to_local,omitempty"`
	// Unresolved lists records ("issue:<id>" / "memory:<key>") still not on the
	// server after maxSyncRounds; the next sync pushes them again.
	Unresolved []string `json:"unresolved,omitempty"`
}

func (r SyncReport) summary() string {
	var parts []string
	if r.RepairedToServer > 0 || r.RepairedToLocal > 0 {
		parts = append(parts, fmt.Sprintf("repaired local→server=%d server→local=%d", r.RepairedToServer, r.RepairedToLocal))
	}
	if r.Rejected > 0 {
		parts = append(parts, fmt.Sprintf("stale pushes merged=%d", r.Rejected))
	}
	if len(r.Conflicts) > 0 {
		parts = append(parts, fmt.Sprintf("field conflicts=%d", len(r.Conflicts)))
	}
	if len(r.Unresolved) > 0 {
		parts = append(parts, fmt.Sprintf("unresolved=%d (%s)", len(r.Unresolved), strings.Join(r.Unresolved, ",")))
	}
	return strings.Join(parts, "; ")
}

type SyncState struct {
	Base    SyncSnapshot
	Current SyncSnapshot
	Report  SyncReport
}

// MergeServerMemories applies the fixed deletion policy. The returned slice is
// the server mirror representation; deleted entries are retained as tombstones
// while the repository projection may omit them.
func MergeServerMemories(base, local, remote []ServerMemory) ([]ServerMemory, SyncReport) {
	bm, lm, rm := mapServerMemories(base), mapServerMemories(local), mapServerMemories(remote)
	keys := map[string]bool{}
	for k := range bm {
		keys[k] = true
	}
	for k := range lm {
		keys[k] = true
	}
	for k := range rm {
		keys[k] = true
	}
	var report SyncReport
	out := make([]ServerMemory, 0, len(keys))
	for key := range keys {
		b, bok := bm[key]
		l, lok := lm[key]
		r, rok := rm[key]
		if !lok && bok {
			l = b
			l.Deleted = true
			l.DeletedAt = Now()
			l.DeletedBy = "local"
		}
		if !rok && bok {
			r = b
		}
		if l.Deleted && !r.Deleted {
			if l.DeletedAt >= r.UpdatedAt {
				out = append(out, l)
			} else {
				out = append(out, r)
				report.Conflicts = append(report.Conflicts, SyncConflict{Kind: "memory", Key: key, Field: "deleted", Took: "server", Other: "local tombstone"})
			}
			continue
		}
		if r.Deleted && !l.Deleted {
			if r.DeletedAt >= l.UpdatedAt {
				out = append(out, r)
			} else {
				out = append(out, l)
				report.Conflicts = append(report.Conflicts, SyncConflict{Kind: "memory", Key: key, Field: "deleted", Took: "local", Other: "server tombstone"})
			}
			continue
		}
		if lok {
			out = append(out, l)
		} else if rok {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, report
}

func mapServerMemories(items []ServerMemory) map[string]ServerMemory {
	out := map[string]ServerMemory{}
	for _, item := range items {
		out[item.Key] = item
	}
	return out
}

func (s *SyncState) Apply(local SyncSnapshot, remote *SyncSnapshot) error {
	if remote == nil {
		s.Current = cloneSnapshot(local)
		return nil
	}
	merged, report := ThreeWayMerge(s.Base, local, *remote)
	s.Current, s.Report = merged, report
	s.Base = cloneSnapshot(merged)
	return nil
}

func (s SyncState) Pending() int {
	if equalSnapshot(s.Base, s.Current) {
		return 0
	}
	return 1
}

func ThreeWayMerge(base, local, remote SyncSnapshot) (SyncSnapshot, SyncReport) {
	report := SyncReport{}
	issues := mergeIssues(indexIssues(base.Issues), indexIssues(local.Issues), indexIssues(remote.Issues), &report)
	memories := mergeMemories(indexMemories(base.Memories), indexMemories(local.Memories), indexMemories(remote.Memories), &report)
	return SyncSnapshot{Issues: issues, Memories: memories}, report
}

func mergeIssues(base, local, remote map[string]Issue, report *SyncReport) []Issue {
	keys := unionKeysIssue(base, local, remote)
	out := make([]Issue, 0, len(keys))
	for _, key := range keys {
		b := base[key]
		l, lok := local[key]
		r, rok := remote[key]
		if !lok && rok {
			out = append(out, r)
			continue
		}
		if lok && !rok {
			out = append(out, l)
			continue
		}
		if !lok && !rok {
			continue
		}
		m := l
		conflictStart := len(report.Conflicts)
		mergeIssueScalar(&m.Title, b.Title, l.Title, r.Title, l.UpdatedAt, r.UpdatedAt, key, "title", report)
		mergeIssueScalar(&m.Type, b.Type, l.Type, r.Type, l.UpdatedAt, r.UpdatedAt, key, "type", report)
		mergeIssueScalar(&m.Status, b.Status, l.Status, r.Status, l.UpdatedAt, r.UpdatedAt, key, "status", report)
		if l.Priority != r.Priority {
			m.Priority = chooseScalar(b.Priority, l.Priority, r.Priority, l.UpdatedAt, r.UpdatedAt, key, "priority", report)
		}
		mergeIssueScalar(&m.Description, b.Description, l.Description, r.Description, l.UpdatedAt, r.UpdatedAt, key, "description", report)
		mergeIssueScalar(&m.Design, b.Design, l.Design, r.Design, l.UpdatedAt, r.UpdatedAt, key, "design", report)
		mergeIssueScalar(&m.AcceptanceCriteria, b.AcceptanceCriteria, l.AcceptanceCriteria, r.AcceptanceCriteria, l.UpdatedAt, r.UpdatedAt, key, "acceptance_criteria", report)
		mergeIssueScalar(&m.Assignee, b.Assignee, l.Assignee, r.Assignee, l.UpdatedAt, r.UpdatedAt, key, "assignee", report)
		mergeIssueScalar(&m.Owner, b.Owner, l.Owner, r.Owner, l.UpdatedAt, r.UpdatedAt, key, "owner", report)
		mergeIssueScalar(&m.CloseReason, b.CloseReason, l.CloseReason, r.CloseReason, l.UpdatedAt, r.UpdatedAt, key, "close_reason", report)
		mergeIssueScalar(&m.Parent, b.Parent, l.Parent, r.Parent, l.UpdatedAt, r.UpdatedAt, key, "parent", report)
		mergeIssueScalar(&m.ExternalRef, b.ExternalRef, l.ExternalRef, r.ExternalRef, l.UpdatedAt, r.UpdatedAt, key, "external_ref", report)
		mergeIssueScalar(&m.SpecID, b.SpecID, l.SpecID, r.SpecID, l.UpdatedAt, r.UpdatedAt, key, "spec_id", report)
		// A reopen (closed_at cleared) must propagate instead of being re-filled.
		mergeIssueScalar(&m.ClosedAt, b.ClosedAt, l.ClosedAt, r.ClosedAt, l.UpdatedAt, r.UpdatedAt, key, "closed_at", report)
		m.Tags = mergeStringSet(b.Tags, l.Tags, r.Tags)
		m.Deps = mergeDepSet(b.Deps, l.Deps, r.Deps)
		m.Notes = unionNotes(l.Notes, r.Notes)
		for _, conflict := range report.Conflicts[conflictStart:] {
			m.Notes = append(m.Notes, NoteEntry{At: maxTime(l.UpdatedAt, r.UpdatedAt), By: "sync", Text: fmt.Sprintf("同步冲突：%s 取了 %s，另一方为 %s", conflict.Field, conflict.Took, conflict.Other)})
		}
		m.Comments = unionComments(l.Comments, r.Comments)
		if l.UpdatedAt < r.UpdatedAt {
			m.UpdatedAt = r.UpdatedAt
		}
		out = append(out, m)
	}
	return out
}

func maxTime(a, b string) string {
	if a >= b {
		return a
	}
	return b
}

func mergeMemories(base, local, remote map[string]Memory, report *SyncReport) []Memory {
	keys := unionKeysMemory(base, local, remote)
	out := make([]Memory, 0, len(keys))
	for _, key := range keys {
		b := base[key]
		l, lok := local[key]
		r, rok := remote[key]
		if !lok && rok {
			out = append(out, r)
			continue
		}
		if lok && !rok {
			out = append(out, l)
			continue
		}
		if !lok && !rok {
			continue
		}
		m := l
		mergeMemoryScalar(&m.Content, b.Content, l.Content, r.Content, l.UpdatedAt, r.UpdatedAt, key, "content", report)
		mergeMemoryScalar(&m.By, b.By, l.By, r.By, l.UpdatedAt, r.UpdatedAt, key, "by", report)
		mergeMemoryMeta(&m.MemoryMeta, b.MemoryMeta, l.MemoryMeta, r.MemoryMeta, l.UpdatedAt, r.UpdatedAt, key, report)
		m.Tags = mergeStringSet(b.Tags, l.Tags, r.Tags)
		if l.UpdatedAt < r.UpdatedAt {
			m.UpdatedAt = r.UpdatedAt
		}
		out = append(out, m)
	}
	return out
}

func mergeIssueScalar(dst *string, b, l, r, lt, rt, key, field string, report *SyncReport) {
	if l == r {
		*dst = l
		return
	}
	if l == b {
		*dst = r
		return
	}
	if r == b {
		*dst = l
		return
	}
	if lt >= rt {
		*dst = l
		addConflict(report, "issue", key, field, "local", r)
	} else {
		*dst = r
		addConflict(report, "issue", key, field, "server", l)
	}
}

// mergeMemoryMeta three-way merges the optional memory fields field by field;
// when is compared as one value (its JSON form).
func mergeMemoryMeta(dst *MemoryMeta, b, l, r MemoryMeta, lt, rt, key string, report *SyncReport) {
	for _, f := range []struct {
		dst     *string
		b, l, r string
		name    string
	}{
		{&dst.Kind, b.Kind, l.Kind, r.Kind, "kind"},
		{&dst.Summary, b.Summary, l.Summary, r.Summary, "summary"},
		{&dst.ExpiresAt, b.ExpiresAt, l.ExpiresAt, r.ExpiresAt, "expires_at"},
		{&dst.Source, b.Source, l.Source, r.Source, "source"},
		{&dst.CreatedAt, b.CreatedAt, l.CreatedAt, r.CreatedAt, "created_at"},
	} {
		mergeMemoryScalar(f.dst, f.b, f.l, f.r, lt, rt, key, f.name, report)
	}
	whenJSON := func(w *MemoryWhen) string {
		if w.empty() {
			return ""
		}
		out, _ := json.Marshal(w)
		return string(out)
	}
	dst.DoctorIgnore = mergeStringSet(b.DoctorIgnore, l.DoctorIgnore, r.DoctorIgnore)
	merged := whenJSON(l.When)
	mergeMemoryScalar(&merged, whenJSON(b.When), merged, whenJSON(r.When), lt, rt, key, "when", report)
	dst.When = nil
	if merged != "" {
		var w MemoryWhen
		if json.Unmarshal([]byte(merged), &w) == nil {
			dst.When = &w
		}
	}
}

func mergeMemoryScalar(dst *string, b, l, r, lt, rt, key, field string, report *SyncReport) {
	if l == r {
		*dst = l
		return
	}
	if l == b {
		*dst = r
		return
	}
	if r == b {
		*dst = l
		return
	}
	if lt >= rt {
		*dst = l
		addConflict(report, "memory", key, field, "local", r)
	} else {
		*dst = r
		addConflict(report, "memory", key, field, "server", l)
	}
}

func chooseScalar(b, l, r int, lt, rt, key, field string, report *SyncReport) int {
	if l == b {
		return r
	}
	if r == b {
		return l
	}
	if lt >= rt {
		addConflict(report, "issue", key, field, "local", fmt.Sprint(r))
		return l
	}
	addConflict(report, "issue", key, field, "server", fmt.Sprint(l))
	return r
}

func addConflict(report *SyncReport, kind, key, field, took, other string) {
	report.Conflicts = append(report.Conflicts, SyncConflict{Kind: kind, Key: key, Field: field, Took: took, Other: other})
}

func LinkIssueToJob(issue Issue, event JobIssueEvent) Issue {
	if event.Phase == "started" {
		issue.Status = "in_progress"
	}
	if event.Phase == "finished" {
		text := "job " + event.JobID + " finished status=" + event.Status
		if len(event.Commits) > 0 {
			text += " commits=" + strings.Join(event.Commits, ",")
		}
		if len(event.Uncommitted) > 0 {
			text += " uncommitted=" + strings.Join(event.Uncommitted, ",")
		}
		issue.Notes = append(issue.Notes, NoteEntry{At: event.At, By: "job:" + event.JobID, Text: text})
	}
	issue.UpdatedAt = event.At
	return issue
}

type JobIssueEvent struct {
	JobID, Phase, Status, At string
	Commits, Uncommitted     []string
}

func indexIssues(items []Issue) map[string]Issue {
	out := map[string]Issue{}
	for _, item := range items {
		out[item.ID] = item
	}
	return out
}
func indexMemories(items []Memory) map[string]Memory {
	out := map[string]Memory{}
	for _, item := range items {
		out[item.Key] = item
	}
	return out
}
func unionKeysIssue(ms ...map[string]Issue) []string {
	set := map[string]bool{}
	for _, m := range ms {
		for k := range m {
			set[k] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func unionKeysMemory(ms ...map[string]Memory) []string {
	set := map[string]bool{}
	for _, m := range ms {
		for k := range m {
			set[k] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// mergeStringSet is a three-way set merge: an element one side removed since
// base stays removed (a plain union would resurrect every `--untag`). With no
// base (an issue first seen on both sides) it degrades to the union.
func mergeStringSet(base, local, remote []string) []string {
	inBase, inLocal, inRemote := toSet(base), toSet(local), toSet(remote)
	set := map[string]bool{}
	for x := range inLocal {
		if inRemote[x] || !inBase[x] {
			set[x] = true
		}
	}
	for x := range inRemote {
		if inLocal[x] || !inBase[x] {
			set[x] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for x := range set {
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}

func toSet(items []string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, x := range items {
		out[x] = true
	}
	return out
}

func depKey(d Dep) string { return d.ID + "\x00" + d.Type }

// mergeDepSet is mergeStringSet for dependencies, so `dep rm` survives a sync.
func mergeDepSet(base, local, remote []Dep) []Dep {
	all := map[string]Dep{}
	keys := func(items []Dep) []string {
		out := make([]string, 0, len(items))
		for _, d := range items {
			all[depKey(d)] = d
			out = append(out, depKey(d))
		}
		return out
	}
	merged := mergeStringSet(keys(base), keys(local), keys(remote))
	if len(merged) == 0 {
		return nil
	}
	out := make([]Dep, 0, len(merged))
	for _, k := range merged {
		out = append(out, all[k])
	}
	sort.Slice(out, func(i, j int) bool { return depKey(out[i]) < depKey(out[j]) })
	return out
}

func unionNotes(a, b []NoteEntry) []NoteEntry {
	set := map[string]NoteEntry{}
	for _, x := range append(append([]NoteEntry{}, a...), b...) {
		set[x.At+"\x00"+x.By+"\x00"+x.Text] = x
	}
	out := make([]NoteEntry, 0, len(set))
	for _, x := range set {
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}
func unionComments(a, b []Comment) []Comment {
	set := map[string]Comment{}
	for _, x := range append(append([]Comment{}, a...), b...) {
		set[x.At+"\x00"+x.By+"\x00"+x.Text] = x
	}
	out := make([]Comment, 0, len(set))
	for _, x := range set {
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}
func cloneSnapshot(s SyncSnapshot) SyncSnapshot {
	b, _ := json.Marshal(s)
	var out SyncSnapshot
	_ = json.Unmarshal(b, &out)
	return out
}
func equalSnapshot(a, b SyncSnapshot) bool { return string(mustJSON(a)) == string(mustJSON(b)) }
func mustJSON(v any) []byte                { b, _ := json.Marshal(v); return b }
