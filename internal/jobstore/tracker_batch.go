package jobstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/tracker"
)

// TrackerBatchSet is the change applied to every issue of a batch. Status and
// CloseReason follow the single-edit semantics: closing stamps closed_at (kept if
// already closed) and records the reason; any other status clears both.
type TrackerBatchSet struct {
	Status      string   `json:"status,omitempty"`
	CloseReason string   `json:"close_reason,omitempty"`
	AddTags     []string `json:"add_tags,omitempty"`
}

// TrackerBatchResult is the per-issue outcome of BatchPatchTrackerIssues.
type TrackerBatchResult struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Validate rejects a set that changes nothing or is inconsistent.
func (b TrackerBatchSet) Validate() error {
	if b.Status == "" && len(cleanTags(b.AddTags)) == 0 {
		return errors.New("set needs status or add_tags")
	}
	if b.Status != "" && !tracker.ValidStatus(b.Status) {
		return fmt.Errorf("invalid status %q", b.Status)
	}
	if b.CloseReason != "" && b.Status != "closed" {
		return errors.New("close_reason requires status closed")
	}
	return nil
}

func cleanTags(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range in {
		t := strings.TrimSpace(raw)
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// BatchPatchTrackerIssues applies set to each id independently (one transaction
// per issue), so a missing or failing id never blocks the others. Results come
// back in request order with duplicates collapsed.
func (s *Store) BatchPatchTrackerIssues(trackerID string, ids []string, set TrackerBatchSet, now, by string) []TrackerBatchResult {
	tags := cleanTags(set.AddTags)
	seen := make(map[string]bool, len(ids))
	out := make([]TrackerBatchResult, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		_, err := s.mutateTrackerIssue(trackerID, id, 0, now, by, func(obj map[string]json.RawMessage) error {
			return applyBatchSet(obj, set, tags, now)
		})
		res := TrackerBatchResult{ID: id, OK: err == nil}
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				res.Error = "issue not found"
			} else {
				res.Error = err.Error()
			}
		}
		out = append(out, res)
	}
	return out
}

func applyBatchSet(obj map[string]json.RawMessage, set TrackerBatchSet, tags []string, now string) error {
	if set.Status != "" {
		var cur, closedAt string
		_ = json.Unmarshal(obj["status"], &cur)
		_ = json.Unmarshal(obj["closed_at"], &closedAt)
		obj["status"], _ = json.Marshal(set.Status)
		if set.Status == "closed" {
			if cur != "closed" || closedAt == "" {
				obj["closed_at"], _ = json.Marshal(now)
			}
			if set.CloseReason != "" {
				obj["close_reason"], _ = json.Marshal(set.CloseReason)
			}
		} else {
			delete(obj, "closed_at")
			delete(obj, "close_reason")
		}
	}
	if len(tags) > 0 {
		var have []string
		_ = json.Unmarshal(obj["tags"], &have)
		for _, t := range tags {
			dup := false
			for _, h := range have {
				if h == t {
					dup = true
					break
				}
			}
			if !dup {
				have = append(have, t)
			}
		}
		obj["tags"], _ = json.Marshal(have)
	}
	return nil
}
