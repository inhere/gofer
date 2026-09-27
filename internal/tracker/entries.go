package tracker

import (
	"errors"
	"fmt"
	"sort"
	"strings"
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

type IssuePatch struct {
	Title       string
	Status      string
	Claim       bool
	AppendNotes string
	Actor       string
}

func (s *Store) UpdateIssue(id string, patch IssuePatch) (Issue, error) {
	var changed Issue
	err := s.UpdateIssues(func(items []Issue) ([]Issue, error) {
		for i := range items {
			if items[i].ID != id {
				continue
			}
			if patch.Title != "" {
				items[i].Title = patch.Title
			}
			if patch.Status != "" {
				if !ValidStatus(patch.Status) {
					return nil, fmt.Errorf("invalid status %q", patch.Status)
				}
				items[i].Status = patch.Status
			}
			if patch.Claim {
				items[i].Status = "in_progress"
				items[i].Assignee = patch.Actor
				if items[i].StartedAt == "" {
					items[i].StartedAt = Now()
				}
			}
			if patch.AppendNotes != "" {
				items[i].Notes = append(items[i].Notes, NoteEntry{At: Now(), By: patch.Actor, Text: patch.AppendNotes})
			}
			items[i].UpdatedAt = Now()
			changed = items[i]
			return items, nil
		}
		return nil, fmt.Errorf("issue %s not found", id)
	})
	return changed, err
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

func (s *Store) AddDep(id, on string) (Issue, error) {
	var changed Issue
	err := s.UpdateIssues(func(items []Issue) ([]Issue, error) {
		if id == on || !hasIssue(items, on) {
			return nil, fmt.Errorf("dependency %s is invalid or not found", on)
		}
		for i := range items {
			if items[i].ID != id {
				continue
			}
			for _, dep := range items[i].Deps {
				if dep.ID == on && dep.Type == "blocks" {
					changed = items[i]
					return items, nil
				}
			}
			items[i].Deps = append(items[i].Deps, Dep{ID: on, Type: "blocks"})
			items[i].UpdatedAt = Now()
			changed = items[i]
			return items, nil
		}
		return nil, fmt.Errorf("issue %s not found", id)
	})
	return changed, err
}

type IssueFilter struct {
	Status string
	Type   string
	Label  string
	All    bool
}

func (s *Store) ListIssues(filter IssueFilter) ([]Issue, error) {
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
		if filter.Label != "" {
			found := false
			for _, label := range item.Labels {
				found = found || label == filter.Label
			}
			if !found {
				continue
			}
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (s *Store) SetMemory(key, content, actor string) (Memory, error) {
	if strings.TrimSpace(key) == "" || strings.TrimSpace(content) == "" {
		return Memory{}, errors.New("memory key and content are required")
	}
	item := Memory{Key: key, Content: content, UpdatedAt: Now(), By: actor}
	err := s.UpdateMemories(func(items []Memory) ([]Memory, error) {
		for i := range items {
			if items[i].Key == key {
				items[i] = item
				return items, nil
			}
		}
		return append(items, item), nil
	})
	return item, err
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

func (s *Store) ListMemories(keyword string) ([]Memory, error) {
	items, err := s.ReadMemories()
	if err != nil {
		return nil, err
	}
	result := make([]Memory, 0, len(items))
	for _, item := range items {
		if strings.Contains(item.Key, keyword) || strings.Contains(item.Content, keyword) {
			result = append(result, item)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result, nil
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
