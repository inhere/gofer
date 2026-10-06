package tracker

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// DepTypes are the dependency kinds `issue dep add --type` accepts. Only
// "blocks" gates Ready; the rest are informational links (bd vocabulary).
// parent-child is deliberately absent: the parent field owns that relation.
var DepTypes = []string{"blocks", "related", "relates-to", "discovered-from", "supersedes"}

// AddComment appends one comment to an issue. Comments are append-only and
// merge by union across sync.
func (s *Store) AddComment(id, text, actor string) (Issue, error) {
	if strings.TrimSpace(text) == "" {
		return Issue{}, errors.New("comment text is required")
	}
	var changed Issue
	err := s.UpdateIssues(func(items []Issue) ([]Issue, error) {
		for i := range items {
			if items[i].ID != id {
				continue
			}
			now := Now()
			items[i].Comments = append(items[i].Comments, Comment{At: now, By: actor, Text: text})
			items[i].UpdatedAt = now
			changed = items[i]
			return items, nil
		}
		return nil, fmt.Errorf("issue %s not found", id)
	})
	return changed, err
}

// ReopenIssue moves a closed issue back to open, clearing closed_at and
// close_reason. A non-empty reason is recorded as a comment so the history of
// why it came back is kept. Reopening an issue that is not closed is an error.
func (s *Store) ReopenIssue(id, reason, actor string) (Issue, error) {
	var changed Issue
	err := s.UpdateIssues(func(items []Issue) ([]Issue, error) {
		for i := range items {
			if items[i].ID != id {
				continue
			}
			if items[i].Status != "closed" {
				return nil, fmt.Errorf("issue %s is %s, not closed", id, items[i].Status)
			}
			applyStatus(&items[i], "open")
			now := Now()
			if strings.TrimSpace(reason) != "" {
				items[i].Comments = append(items[i].Comments, Comment{At: now, By: actor, Text: "reopened: " + reason})
			}
			items[i].UpdatedAt = now
			changed = items[i]
			return items, nil
		}
		return nil, fmt.Errorf("issue %s not found", id)
	})
	return changed, err
}

// AddDepType records that id depends on `on` with the given kind ("" = blocks).
// A blocking edge that would close a cycle is rejected.
func (s *Store) AddDepType(id, on, kind string) (Issue, error) {
	if kind == "" {
		kind = "blocks"
	}
	if !hasTag(DepTypes, kind) {
		return Issue{}, fmt.Errorf("invalid dependency type %q (allowed: %s; use --parent for parent-child)", kind, strings.Join(DepTypes, ", "))
	}
	var changed Issue
	err := s.UpdateIssues(func(items []Issue) ([]Issue, error) {
		if id == on || !hasIssue(items, on) {
			return nil, fmt.Errorf("dependency %s is invalid or not found", on)
		}
		if kind == "blocks" && blocksTransitively(items, on, id) {
			return nil, fmt.Errorf("dependency %s -> %s would create a cycle", id, on)
		}
		for i := range items {
			if items[i].ID != id {
				continue
			}
			for _, dep := range items[i].Deps {
				if dep.ID == on && dep.Type == kind {
					changed = items[i]
					return items, nil
				}
			}
			items[i].Deps = append(items[i].Deps, Dep{ID: on, Type: kind})
			items[i].UpdatedAt = Now()
			changed = items[i]
			return items, nil
		}
		return nil, fmt.Errorf("issue %s not found", id)
	})
	return changed, err
}

// blocksTransitively reports whether `from` already waits (directly or
// through other blocking edges) on `target`.
func blocksTransitively(items []Issue, from, target string) bool {
	byID := make(map[string]Issue, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	seen := map[string]bool{}
	stack := []string{from}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur == target {
			return true
		}
		if seen[cur] {
			continue
		}
		seen[cur] = true
		for _, dep := range byID[cur].Deps {
			if dep.Type == "blocks" {
				stack = append(stack, dep.ID)
			}
		}
	}
	return false
}

// RemoveDep drops id's dependency on `on`. kind "" removes every kind; an
// edge that does not exist is an error so typos are visible.
func (s *Store) RemoveDep(id, on, kind string) (Issue, error) {
	var changed Issue
	err := s.UpdateIssues(func(items []Issue) ([]Issue, error) {
		for i := range items {
			if items[i].ID != id {
				continue
			}
			kept := make([]Dep, 0, len(items[i].Deps))
			removed := 0
			for _, dep := range items[i].Deps {
				if dep.ID == on && (kind == "" || dep.Type == kind) {
					removed++
					continue
				}
				kept = append(kept, dep)
			}
			if removed == 0 {
				return nil, fmt.Errorf("issue %s has no dependency on %s", id, on)
			}
			items[i].Deps = kept
			items[i].UpdatedAt = Now()
			changed = items[i]
			return items, nil
		}
		return nil, fmt.Errorf("issue %s not found", id)
	})
	return changed, err
}

// Relations is the dependency neighbourhood of one issue.
type Relations struct {
	Parent    string   `json:"parent,omitempty"`
	Children  []string `json:"children,omitempty"`
	BlockedBy []Dep    `json:"blocked_by,omitempty"` // this issue waits on these (blocking, not yet closed)
	Blocks    []string `json:"blocks,omitempty"`     // issues waiting on this one (blocking, still unfinished)
	DependsOn []Dep    `json:"depends_on,omitempty"` // every outgoing edge, any kind or state
	Linked    []Dep    `json:"linked,omitempty"`     // incoming non-blocking edges: ID is the issue that points here
}

// Relations computes the neighbourhood of id from the whole issue set.
func (s *Store) Relations(id string) (Issue, Relations, error) {
	items, err := s.ReadIssues()
	if err != nil {
		return Issue{}, Relations{}, err
	}
	return relationsOf(items, id)
}

func relationsOf(items []Issue, id string) (Issue, Relations, error) {
	var self Issue
	found := false
	status := make(map[string]string, len(items))
	for _, item := range items {
		status[item.ID] = item.Status
		if item.ID == id {
			self, found = item, true
		}
	}
	if !found {
		return Issue{}, Relations{}, fmt.Errorf("issue %s not found", id)
	}
	rel := Relations{Parent: self.Parent, DependsOn: append([]Dep(nil), self.Deps...)}
	for _, dep := range self.Deps {
		if dep.Type == "blocks" && status[dep.ID] != "closed" {
			rel.BlockedBy = append(rel.BlockedBy, dep)
		}
	}
	for _, item := range items {
		if item.Parent == id {
			rel.Children = append(rel.Children, item.ID)
		}
		for _, dep := range item.Deps {
			if dep.ID != id {
				continue
			}
			if dep.Type == "blocks" {
				if item.Status != "closed" {
					rel.Blocks = append(rel.Blocks, item.ID)
				}
			} else {
				rel.Linked = append(rel.Linked, Dep{ID: item.ID, Type: dep.Type})
			}
		}
	}
	sort.Strings(rel.Children)
	sort.Strings(rel.Blocks)
	sort.Slice(rel.Linked, func(i, j int) bool { return rel.Linked[i].ID < rel.Linked[j].ID })
	return self, rel, nil
}
