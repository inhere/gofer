package job

import (
	"encoding/json"
	"sort"

	"dev-agent-bridge/internal/project"
)

// defaultListLimit caps the number of jobs returned by ListJobs when the caller
// does not specify a positive limit (web-T2).
const defaultListLimit = 200

// ListOpts filters/bounds a ListJobs query. A zero value lists every project's
// jobs (no status filter) up to defaultListLimit.
type ListOpts struct {
	// Project, when non-empty, restricts the result to a single project_key.
	Project string
	// Status, when non-empty, keeps only jobs whose status matches exactly.
	Status string
	// Limit caps the number of returned jobs; <= 0 means defaultListLimit.
	Limit int
}

// ListJobs returns job snapshots across one or all projects, merging the
// persisted per-project index (jobs.jsonl) with the in-memory live state. The
// in-memory snapshot wins for any job present in both (it reflects the latest
// status of a running job and covers jobs whose terminal index line is not yet
// written). Results are sorted by started_at desc (id desc as tiebreaker) and
// truncated to opts.Limit.
//
// Index I/O happens without holding s.mu; the in-memory pass copies entry
// pointers under s.mu and snapshots them after unlocking to avoid taking
// entry.mu while holding s.mu.
func (s *Service) ListJobs(opts ListOpts) ([]JobResult, error) {
	// 1. Resolve the target project set.
	targets := map[string]struct{}{}
	if opts.Project != "" {
		if _, ok := s.cfg.Projects[opts.Project]; !ok {
			return []JobResult{}, nil
		}
		targets[opts.Project] = struct{}{}
	} else {
		for key := range s.cfg.Projects {
			targets[key] = struct{}{}
		}
	}

	// 2. Fold the persisted index per project (last line wins per id). I/O only;
	// no service lock held. A per-project resolution/read error is skipped rather
	// than failing the whole list.
	merged := map[string]JobResult{}
	for key := range targets {
		proj := s.cfg.Projects[key]
		base, err := project.ResultBaseDir(s.cfg, key, proj)
		if err != nil {
			continue
		}
		raws, _ := s.newStore(base).ReadIndex()
		for _, raw := range raws {
			var rec JobResult
			if err := json.Unmarshal(raw, &rec); err != nil {
				continue
			}
			merged[rec.ID] = rec
		}
	}

	// 3. Overlay in-memory snapshots (running jobs are authoritative). Copy entry
	// pointers under s.mu, then snapshot after unlocking to avoid the
	// s.mu -> entry.mu lock-order coupling.
	s.mu.Lock()
	entries := make([]*jobEntry, 0, len(s.jobs))
	for _, e := range s.jobs {
		entries = append(entries, e)
	}
	s.mu.Unlock()
	for _, e := range entries {
		snap := e.snapshot()
		if opts.Project != "" && snap.ProjectKey != opts.Project {
			continue
		}
		merged[snap.ID] = snap
	}

	// 4. Filter (status) -> collect (non-nil) -> sort (started_at desc, id desc)
	// -> truncate.
	out := make([]JobResult, 0, len(merged))
	for _, rec := range merged {
		if opts.Status != "" && rec.Status != opts.Status {
			continue
		}
		out = append(out, rec)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartedAt != out[j].StartedAt {
			return out[i].StartedAt > out[j].StartedAt
		}
		return out[i].ID > out[j].ID
	})

	limit := opts.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
