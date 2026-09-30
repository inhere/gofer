package job

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SetTitle changes only the human-readable title. It works for live entries and
// for terminal jobs that have already been evicted from memory.
func (s *Service) SetTitle(id, title, operator string) (JobResult, error) {
	title = strings.TrimSpace(title)
	if len([]rune(title)) > TitleMaxRunes {
		return JobResult{}, fmt.Errorf("%w: title must be at most %d characters", ErrInvalidRequest, TitleMaxRunes)
	}
	if entry := s.entry(id); entry != nil {
		entry.mu.Lock()
		old := entry.result.Title
		if old == title {
			out := entry.result
			entry.mu.Unlock()
			return out, nil
		}
		var req JobRequest
		if err := json.Unmarshal([]byte(entry.result.RequestJSON), &req); err != nil {
			entry.mu.Unlock()
			return JobResult{}, fmt.Errorf("decode job request: %w", err)
		}
		req.Title = title
		b, err := json.Marshal(req)
		if err != nil {
			entry.mu.Unlock()
			return JobResult{}, fmt.Errorf("encode job request: %w", err)
		}
		entry.result.Title = title
		entry.result.RequestJSON = string(b)
		out := entry.result
		entry.mu.Unlock()
		if err := s.persist(out); err != nil {
			return JobResult{}, err
		}
		s.recordEvent(id, EventJobTitleChanged, map[string]any{"old_title": old, "new_title": title, "operator": operator})
		return out, nil
	}
	rec, ok, err := s.meta.GetJob(id)
	if err != nil {
		return JobResult{}, err
	}
	if !ok {
		return JobResult{}, fmt.Errorf("unknown job %q", id)
	}
	var req JobRequest
	if err := json.Unmarshal([]byte(rec.RequestJSON), &req); err != nil {
		return JobResult{}, fmt.Errorf("decode job request: %w", err)
	}
	old := req.Title
	if old == title {
		return fromRecord(rec), nil
	}
	req.Title = title
	b, err := json.Marshal(req)
	if err != nil {
		return JobResult{}, fmt.Errorf("encode job request: %w", err)
	}
	rec.RequestJSON = string(b)
	rec.UpdatedAt = s.nowFn().Unix()
	if err := s.meta.UpsertJob(rec); err != nil {
		return JobResult{}, err
	}
	s.recordEvent(id, EventJobTitleChanged, map[string]any{"old_title": old, "new_title": title, "operator": operator})
	return fromRecord(rec), nil
}
