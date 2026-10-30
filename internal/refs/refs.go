// Package refs extracts references to gofer objects from free-form text.
package refs

import "regexp"

// Kind names the object category of a reference.
type Kind string

const (
	KindIssue Kind = "issue"
	KindJob   Kind = "job"
	KindPlan  Kind = "plan"
	KindTodo  Kind = "todo"
	KindWF    Kind = "wf"
)

// Reference is one unique object id mentioned in text.
type Reference struct {
	Kind Kind
	ID   string
}

var referencePattern = regexp.MustCompile(`(?i)(gofer-[a-z0-9]{4}|job-[0-9]{8}-[0-9]{6}-[a-z0-9]{8}|plan-[a-z0-9][a-z0-9_-]*|todo-[a-z0-9][a-z0-9_-]*|wf-[a-z0-9][a-z0-9_-]*)`)

// Extract returns references in first-seen order, without duplicates. References
// are deliberately found in all text, including code blocks and URLs.
func Extract(text string) []Reference {
	matches := referencePattern.FindAllString(text, -1)
	out := make([]Reference, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, id := range matches {
		key := id
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		kind := KindIssue
		switch {
		case len(id) >= 4 && id[:4] == "job-":
			kind = KindJob
		case len(id) >= 5 && id[:5] == "plan-":
			kind = KindPlan
		case len(id) >= 5 && id[:5] == "todo-":
			kind = KindTodo
		case len(id) >= 3 && id[:3] == "wf-":
			kind = KindWF
		}
		out = append(out, Reference{Kind: kind, ID: id})
	}
	return out
}
