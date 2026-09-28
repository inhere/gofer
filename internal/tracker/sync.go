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
}

type SyncState struct {
	Base    SyncSnapshot
	Current SyncSnapshot
	Report  SyncReport
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
		m.Tags = unionStrings(l.Tags, r.Tags)
		m.Deps = unionDeps(l.Deps, r.Deps)
		m.Notes = unionNotes(l.Notes, r.Notes)
		m.Comments = unionComments(l.Comments, r.Comments)
		if l.UpdatedAt < r.UpdatedAt {
			m.UpdatedAt = r.UpdatedAt
		}
		out = append(out, m)
	}
	return out
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
		m.Tags = unionStrings(l.Tags, r.Tags)
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
func unionStrings(a, b []string) []string {
	set := map[string]bool{}
	for _, x := range append(append([]string{}, a...), b...) {
		set[x] = true
	}
	out := make([]string, 0, len(set))
	for x := range set {
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}
func unionDeps(a, b []Dep) []Dep {
	set := map[string]Dep{}
	for _, x := range append(append([]Dep{}, a...), b...) {
		set[x.ID+"\x00"+x.Type] = x
	}
	out := make([]Dep, 0, len(set))
	for _, x := range set {
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID+out[i].Type < out[j].ID+out[j].Type })
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
