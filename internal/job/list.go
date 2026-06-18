package job

import (
	"sort"

	"dev-agent-bridge/internal/jobstore"
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
// DB-persisted index (metadata store, DB-side filtered/sorted/paginated) with
// the in-memory live state. The in-memory snapshot wins for any job present in
// both (it reflects the latest status of a running job). Results are sorted by
// started_at desc (id desc as tiebreaker) and truncated to opts.Limit.
//
// DB I/O happens without holding s.mu; the in-memory pass copies entry pointers
// under s.mu and snapshots them after unlocking to avoid taking entry.mu while
// holding s.mu.
func (s *Service) ListJobs(opts ListOpts) ([]JobResult, error) {
	// 1. An explicit project that is not registered yields an empty (non-nil)
	// result, matching the pre-DB behaviour (the list is scoped to known projects).
	if opts.Project != "" {
		if _, ok := s.cfg.Projects[opts.Project]; !ok {
			return []JobResult{}, nil
		}
	}

	// 2. Pull the persisted base set from the metadata store (DB-side project/
	// status filter + ordering). DB read; no service lock held. A query error is
	// non-fatal: fall back to whatever the in-memory overlay provides.
	merged := map[string]JobResult{}
	recs, _ := s.meta.ListJobs(jobstore.ListQuery{
		Project: opts.Project,
		Status:  opts.Status,
		Limit:   opts.Limit,
	})
	for _, rec := range recs {
		merged[rec.ID] = fromRecord(rec)
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
