package tracker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"reflect"
	"sort"
	"strings"

	"github.com/inhere/gofer/internal/procattr"
)

const memorySummaryRunes = 60

// Change is one tracker entry that differs from git HEAD.
type Change struct {
	// Kind is "issue" or "memory".
	Kind string `json:"kind"`
	// Op is "added", "changed" or "removed".
	Op string `json:"op"`
	// ID is the issue id or the memory key.
	ID        string   `json:"id"`
	Title     string   `json:"title,omitempty"`
	OldStatus string   `json:"old_status,omitempty"`
	NewStatus string   `json:"new_status,omitempty"`
	Fields    []string `json:"fields,omitempty"`
}

// ChangeReport lists tracker changes of the working tree relative to HEAD.
type ChangeReport struct {
	Tracker string   `json:"tracker"`
	InGit   bool     `json:"in_git"`
	Added   int      `json:"added"`
	Changed int      `json:"changed"`
	Removed int      `json:"removed"`
	Changes []Change `json:"changes"`
}

// ChangedSinceHEAD compares issues.jsonl and memories.jsonl in the working
// tree with their versions at git HEAD. A tracker outside git, or a file that
// does not exist at HEAD, counts as empty at HEAD.
func (s *Store) ChangedSinceHEAD() (ChangeReport, error) {
	rep := ChangeReport{Tracker: s.Dir, Changes: []Change{}}
	prefix, inGit := gitPrefix(s.Dir)
	rep.InGit = inGit
	headFile := func(name string) []byte {
		if !inGit {
			return nil
		}
		return gitShowHEAD(s.Dir, prefix+name)
	}

	curIssues, err := s.ReadIssues()
	if err != nil {
		return rep, err
	}
	oldIssues, err := parseIssues(bytes.NewReader(headFile("issues.jsonl")), "HEAD:issues.jsonl")
	if err != nil {
		return rep, err
	}
	curMems, err := s.ReadMemories()
	if err != nil {
		return rep, err
	}
	oldMems, err := parseMemories(bytes.NewReader(headFile("memories.jsonl")), "HEAD:memories.jsonl")
	if err != nil {
		return rep, err
	}

	rep.Changes = append(rep.Changes, diffEntries("issue", oldIssues, curIssues,
		func(i Issue) string { return i.ID },
		func(i Issue) (string, string) { return i.Status, i.Title })...)
	rep.Changes = append(rep.Changes, diffEntries("memory", oldMems, curMems,
		func(m Memory) string { return m.Key },
		func(m Memory) (string, string) { return "", memorySummary(m.Content) })...)
	for _, c := range rep.Changes {
		switch c.Op {
		case "added":
			rep.Added++
		case "changed":
			rep.Changed++
		case "removed":
			rep.Removed++
		}
	}
	return rep, nil
}

// Lines renders the report as one line per change plus a summary line.
func (r ChangeReport) Lines() []string {
	if len(r.Changes) == 0 {
		return []string{"no tracker changes vs HEAD"}
	}
	lines := make([]string, 0, len(r.Changes)+1)
	for _, c := range r.Changes {
		lines = append(lines, c.line())
	}
	return append(lines, fmt.Sprintf("%d added, %d changed, %d removed", r.Added, r.Changed, r.Removed))
}

func (c Change) line() string {
	// memories have no status; join skips empty parts.
	join := func(parts ...string) string {
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p != "" {
				out = append(out, p)
			}
		}
		return strings.Join(out, " ")
	}
	switch c.Op {
	case "added":
		return "+ " + join(c.ID, c.NewStatus, c.Title)
	case "removed":
		return "- " + join(c.ID, c.Title)
	}
	status := c.NewStatus
	if c.OldStatus != c.NewStatus {
		status = c.OldStatus + "→" + c.NewStatus
		return "~ " + join(c.ID, status, c.Title)
	}
	return "~ " + join(c.ID, status, c.Title, "(fields: "+strings.Join(c.Fields, ",")+")")
}

func memorySummary(content string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(content), "\n")
	r := []rune(strings.TrimSpace(first))
	if len(r) > memorySummaryRunes {
		return string(r[:memorySummaryRunes]) + "…"
	}
	return string(r)
}

// diffEntries compares two lists by key. brief returns (status, title).
func diffEntries[T any](kind string, old, cur []T, key func(T) string, brief func(T) (string, string)) []Change {
	oldBy := make(map[string]T, len(old))
	for _, it := range old {
		oldBy[key(it)] = it
	}
	var out []Change
	seen := make(map[string]bool, len(cur))
	for _, it := range cur {
		k := key(it)
		seen[k] = true
		status, title := brief(it)
		prev, ok := oldBy[k]
		if !ok {
			out = append(out, Change{Kind: kind, Op: "added", ID: k, Title: title, NewStatus: status})
			continue
		}
		oldStatus, _ := brief(prev)
		fields := changedFields(prev, it)
		if len(fields) == 0 {
			continue
		}
		c := Change{Kind: kind, Op: "changed", ID: k, Title: title, OldStatus: oldStatus, NewStatus: status}
		if oldStatus == status {
			c.Fields = fields
		}
		out = append(out, c)
	}
	for _, it := range old {
		if k := key(it); !seen[k] {
			_, title := brief(it)
			out = append(out, Change{Kind: kind, Op: "removed", ID: k, Title: title})
		}
	}
	return out
}

// changedFields returns the sorted top-level JSON field names that differ.
func changedFields(a, b any) []string {
	am, bm := toMap(a), toMap(b)
	var names []string
	for k, av := range am {
		if bv, ok := bm[k]; !ok || !reflect.DeepEqual(av, bv) {
			names = append(names, k)
		}
	}
	for k := range bm {
		if _, ok := am[k]; !ok {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	return names
}

func toMap(v any) map[string]any {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return m
}

// gitPrefix returns dir's path relative to its git work tree root (with a
// trailing slash) and whether dir is inside a git work tree.
func gitPrefix(dir string) (string, bool) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-prefix")
	procattr.Background(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// gitShowHEAD returns the content of rel at HEAD, or nil when unavailable
// (no commits yet, file not tracked at HEAD).
func gitShowHEAD(dir, rel string) []byte {
	cmd := exec.Command("git", "-C", dir, "show", "HEAD:"+rel)
	procattr.Background(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return out
}
