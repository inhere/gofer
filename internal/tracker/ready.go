package tracker

import "sort"

func (s *Store) Ready() ([]Issue, error) {
	items, err := s.ReadIssues()
	if err != nil {
		return nil, err
	}
	status := make(map[string]string, len(items))
	for _, item := range items {
		status[item.ID] = item.Status
	}
	ready := make([]Issue, 0, len(items))
	for _, item := range items {
		if item.Status != "open" {
			continue
		}
		blocked := false
		for _, dep := range item.Deps {
			if dep.Type == "blocks" && status[dep.ID] != "closed" {
				blocked = true
				break
			}
		}
		if !blocked {
			ready = append(ready, item)
		}
	}
	sort.Slice(ready, func(i, j int) bool {
		if ready[i].Priority != ready[j].Priority {
			return ready[i].Priority < ready[j].Priority
		}
		if ready[i].CreatedAt != ready[j].CreatedAt {
			return ready[i].CreatedAt < ready[j].CreatedAt
		}
		return ready[i].ID < ready[j].ID
	})
	return ready, nil
}
