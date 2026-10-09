package tracker

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

func (s *Store) Issue(id string) (Issue, error) {
	items, err := s.ReadIssues()
	if err != nil {
		return Issue{}, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return Issue{}, fmt.Errorf("issue %s not found", id)
}

func (s *Store) CreateIssue(item Issue) (Issue, error) {
	if strings.TrimSpace(item.Title) == "" {
		return Issue{}, errors.New("issue title is required")
	}
	if item.Priority < 0 || item.Priority > 4 {
		return Issue{}, errors.New("priority must be 0..4")
	}
	if item.Type == "" {
		item.Type = "task"
	}
	if item.Status == "" {
		item.Status = "open"
	}
	var created Issue
	err := s.UpdateIssues(func(items []Issue) ([]Issue, error) {
		cfg, err := s.ReadConfig()
		if err != nil {
			return nil, err
		}
		for _, dep := range item.Deps {
			if !hasIssue(items, dep.ID) {
				return nil, fmt.Errorf("dependency %s not found", dep.ID)
			}
		}
		id, err := GenerateIssueID(items, cfg.Prefix, item.Parent)
		if err != nil {
			return nil, err
		}
		item.ID = id
		item.CreatedAt = Now()
		item.UpdatedAt = item.CreatedAt
		created = item
		return append(items, item), nil
	})
	return created, err
}

func hasIssue(items []Issue, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}

// IssuePatch lists the fields UpdateIssue may change. Scalar pointers distinguish
// "leave alone" (nil) from "set"; Clear empties the named fields (ClearableFields).
type IssuePatch struct {
	Title       string
	Status      string
	Claim       bool
	AppendNotes string
	Actor       string
	Tags        []string
	Untag       []string
	Type        string
	Priority    *int
	Description *string
	Design      *string
	Acceptance  *string
	Assignee    *string
	Owner       *string
	Parent      *string
	Clear       []string
	// KeepAssignee keeps the assignee when Status moves the issue back to open
	// (by default a re-opened issue is unclaimed, design §2.7).
	KeepAssignee bool
}

// ClearableFields are the names `issue update --clear` accepts.
var ClearableFields = []string{"description", "design", "acceptance", "assignee", "owner", "parent", "close-reason"}

func (s *Store) UpdateIssue(id string, patch IssuePatch) (Issue, error) {
	var changed Issue
	if patch.Priority != nil && (*patch.Priority < 0 || *patch.Priority > 4) {
		return Issue{}, errors.New("priority must be 0..4")
	}
	for _, field := range patch.Clear {
		if !hasTag(ClearableFields, field) {
			return Issue{}, fmt.Errorf("cannot clear %q (allowed: %s)", field, strings.Join(ClearableFields, ", "))
		}
	}
	err := s.UpdateIssues(func(items []Issue) ([]Issue, error) {
		for i := range items {
			if items[i].ID != id {
				continue
			}
			if patch.Title != "" {
				items[i].Title = patch.Title
			}
			if patch.Type != "" {
				items[i].Type = patch.Type
			}
			if patch.Status != "" {
				if !ValidStatus(patch.Status) {
					return nil, fmt.Errorf("invalid status %q", patch.Status)
				}
				if patch.Status == "open" && items[i].Status != "open" && !patch.KeepAssignee {
					items[i].Assignee = ""
				}
				applyStatus(&items[i], patch.Status)
			}
			if patch.Priority != nil {
				items[i].Priority = *patch.Priority
			}
			if patch.Description != nil {
				items[i].Description = *patch.Description
			}
			if patch.Design != nil {
				items[i].Design = *patch.Design
			}
			if patch.Acceptance != nil {
				items[i].AcceptanceCriteria = *patch.Acceptance
			}
			if patch.Assignee != nil {
				items[i].Assignee = *patch.Assignee
			}
			if patch.Owner != nil {
				items[i].Owner = *patch.Owner
			}
			if patch.Parent != nil {
				if err := checkParent(items, id, *patch.Parent); err != nil {
					return nil, err
				}
				items[i].Parent = *patch.Parent
			}
			for _, field := range patch.Clear {
				switch field {
				case "description":
					items[i].Description = ""
				case "design":
					items[i].Design = ""
				case "acceptance":
					items[i].AcceptanceCriteria = ""
				case "assignee":
					items[i].Assignee = ""
				case "owner":
					items[i].Owner = ""
				case "parent":
					items[i].Parent = ""
				case "close-reason":
					items[i].CloseReason = ""
				}
			}
			if patch.Claim {
				applyStatus(&items[i], "in_progress")
				items[i].Assignee = patch.Actor
				if items[i].StartedAt == "" {
					items[i].StartedAt = Now()
				}
			}
			if patch.AppendNotes != "" {
				items[i].Notes = append(items[i].Notes, NoteEntry{At: Now(), By: patch.Actor, Text: patch.AppendNotes})
			}
			items[i].Tags = addTags(items[i].Tags, patch.Tags)
			if len(patch.Untag) > 0 {
				kept := make([]string, 0, len(items[i].Tags))
				for _, tag := range items[i].Tags {
					if !hasTag(patch.Untag, tag) {
						kept = append(kept, tag)
					}
				}
				items[i].Tags = kept
			}
			items[i].UpdatedAt = Now()
			changed = items[i]
			return items, nil
		}
		return nil, fmt.Errorf("issue %s not found", id)
	})
	return changed, err
}

// applyStatus moves item to status, keeping the closing fields consistent:
// closed stamps closed_at, any other status clears closed_at and close_reason.
func applyStatus(item *Issue, status string) {
	if status == "closed" {
		if item.Status != "closed" || item.ClosedAt == "" {
			item.ClosedAt = Now()
		}
	} else {
		item.ClosedAt = ""
		item.CloseReason = ""
	}
	item.Status = status
}

// checkParent validates re-parenting id under parent: the parent must exist,
// not be the issue itself, and not be one of its own descendants.
func checkParent(items []Issue, id, parent string) error {
	if parent == "" {
		return nil
	}
	if parent == id {
		return errors.New("an issue cannot be its own parent")
	}
	byID := make(map[string]Issue, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	if _, ok := byID[parent]; !ok {
		return fmt.Errorf("parent issue %s not found", parent)
	}
	for cur, hops := parent, 0; cur != "" && hops <= len(items); hops++ {
		if cur == id {
			return fmt.Errorf("parent %s is a descendant of %s", parent, id)
		}
		cur = byID[cur].Parent
	}
	return nil
}

func ValidStatus(status string) bool {
	switch status {
	case "open", "in_progress", "blocked", "closed":
		return true
	}
	return false
}

func (s *Store) CloseIssue(id, reason string) (Issue, error) {
	var closed Issue
	err := s.UpdateIssues(func(items []Issue) ([]Issue, error) {
		for i := range items {
			if items[i].ID == id {
				items[i].Status = "closed"
				items[i].ClosedAt = Now()
				items[i].UpdatedAt = items[i].ClosedAt
				items[i].CloseReason = reason
				closed = items[i]
				return items, nil
			}
		}
		return nil, fmt.Errorf("issue %s not found", id)
	})
	return closed, err
}

// AddDep makes id wait for `on` (a blocking dependency).
func (s *Store) AddDep(id, on string) (Issue, error) { return s.AddDepType(id, on, "blocks") }

// IssueFilter narrows ListIssues. Sort is one of SortFields (default id);
// Reverse flips it; Limit > 0 truncates after sorting.
type IssueFilter struct {
	Status   string
	Type     string
	Tags     []string
	Query    string
	All      bool
	Assignee string
	Priority *int
	Sort     string
	Reverse  bool
	Limit    int
}

// SortFields are the names IssueFilter.Sort accepts.
var SortFields = []string{"id", "priority", "created", "updated"}

func (s *Store) ListIssues(filter IssueFilter) ([]Issue, error) {
	if filter.Sort != "" && !hasTag(SortFields, filter.Sort) {
		return nil, fmt.Errorf("invalid sort %q (allowed: %s)", filter.Sort, strings.Join(SortFields, ", "))
	}
	items, err := s.ReadIssues()
	if err != nil {
		return nil, err
	}
	result := make([]Issue, 0, len(items))
	for _, item := range items {
		if !filter.All && filter.Status == "" && item.Status == "closed" {
			continue
		}
		if filter.Status != "" && item.Status != filter.Status {
			continue
		}
		if filter.Type != "" && item.Type != filter.Type {
			continue
		}
		if filter.Assignee != "" && item.Assignee != filter.Assignee {
			continue
		}
		if filter.Priority != nil && item.Priority != *filter.Priority {
			continue
		}
		if !hasAllTags(item.Tags, filter.Tags) {
			continue
		}
		if filter.Query != "" {
			needle := strings.ToLower(filter.Query)
			if !strings.Contains(strings.ToLower(item.Title), needle) && !strings.Contains(strings.ToLower(item.Description), needle) {
				continue
			}
		}
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]
		less := a.ID < b.ID
		switch filter.Sort {
		case "priority":
			if a.Priority != b.Priority {
				less = a.Priority < b.Priority
			}
		case "created":
			if a.CreatedAt != b.CreatedAt {
				less = a.CreatedAt < b.CreatedAt
			}
		case "updated":
			// Most recently touched first: the useful default for "what moved".
			if a.UpdatedAt != b.UpdatedAt {
				less = a.UpdatedAt > b.UpdatedAt
			}
		}
		if filter.Reverse {
			return !less && (a.ID != b.ID)
		}
		return less
	})
	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}
	return result, nil
}

// SetMemory writes content (and tags, when non-nil) keeping every other stored
// field. It skips the CLI summary rule; `memory set` uses SetMemoryPatch.
func (s *Store) SetMemory(key, content, actor string, tags ...string) (Memory, error) {
	return s.SetMemoryPatch(key, MemoryPatch{Content: content, Tags: tags, By: actor}, false)
}

// SetMemoryPatch merges patch into the stored memory (or creates it). validate
// applies ValidateMemoryForWrite to the merged result before anything is written.
func (s *Store) SetMemoryPatch(key string, patch MemoryPatch, validate bool) (Memory, error) {
	if strings.TrimSpace(key) == "" || strings.TrimSpace(patch.Content) == "" {
		return Memory{}, errors.New("memory key and content are required")
	}
	var item Memory
	err := s.UpdateMemories(func(items []Memory) ([]Memory, error) {
		idx := -1
		var existing *Memory
		for i := range items {
			if items[i].Key == key {
				idx, existing = i, &items[i]
				break
			}
		}
		merged, err := ApplyMemoryPatch(existing, key, patch, time.Now())
		if err != nil {
			return nil, err
		}
		if validate {
			if err := ValidateMemoryForWrite(merged); err != nil {
				return nil, err
			}
		}
		item = merged
		if idx >= 0 {
			items[idx] = merged
			return items, nil
		}
		return append(items, merged), nil
	})
	if err != nil {
		return Memory{}, err
	}
	return item, nil
}

func (s *Store) Memory(key string) (Memory, error) {
	items, err := s.ReadMemories()
	if err != nil {
		return Memory{}, err
	}
	for _, item := range items {
		if item.Key == key {
			return item, nil
		}
	}
	return Memory{}, fmt.Errorf("memory %s not found", key)
}

func (s *Store) ListMemories(keyword string, tags ...string) ([]Memory, error) {
	return s.ListMemoriesFiltered(MemoryFilter{Keyword: keyword, Tags: tags})
}

// MemoryFilter narrows `memory ls`: keyword searches key, summary and content
// (case-insensitive); Kind compares the effective kind (legacy `prime` = rule).
type MemoryFilter struct {
	Keyword string
	Tags    []string
	Kind    string
}

func (s *Store) ListMemoriesFiltered(filter MemoryFilter) ([]Memory, error) {
	items, err := s.ReadMemories()
	if err != nil {
		return nil, err
	}
	result := make([]Memory, 0, len(items))
	for _, item := range items {
		if MemoryMatchesFilter(item, filter) {
			result = append(result, item)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result, nil
}

// MemoryMatchesFilter is the shared local / scoped `memory ls` predicate.
func MemoryMatchesFilter(item Memory, filter MemoryFilter) bool {
	needle := strings.ToLower(filter.Keyword)
	if needle != "" && !strings.Contains(strings.ToLower(item.Key), needle) && !strings.Contains(strings.ToLower(item.Summary), needle) && !strings.Contains(strings.ToLower(item.Content), needle) {
		return false
	}
	if filter.Kind != "" && item.EffectiveKind() != filter.Kind {
		return false
	}
	return hasAllTags(item.Tags, filter.Tags)
}

func ParseTags(value string) []string {
	return addTags(nil, strings.Split(value, ","))
}

func addTags(existing, incoming []string) []string {
	out := append([]string(nil), existing...)
	for _, raw := range incoming {
		tag := strings.TrimSpace(raw)
		if tag != "" && !hasTag(out, tag) {
			out = append(out, tag)
		}
	}
	return out
}

func hasTag(tags []string, target string) bool {
	for _, tag := range tags {
		if tag == target {
			return true
		}
	}
	return false
}

func hasAllTags(tags, wanted []string) bool {
	for _, tag := range wanted {
		if !hasTag(tags, tag) {
			return false
		}
	}
	return true
}

func (s *Store) RemoveMemory(key string) error {
	return s.UpdateMemories(func(items []Memory) ([]Memory, error) {
		for i := range items {
			if items[i].Key == key {
				return append(items[:i], items[i+1:]...), nil
			}
		}
		return nil, fmt.Errorf("memory %s not found", key)
	})
}
