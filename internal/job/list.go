package job

import (
	"slices"
	"sort"

	"github.com/inhere/gofer/internal/jobstore"
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
	// Caller, when non-empty, keeps only jobs submitted by that caller id (C2).
	Caller string
	// Tag, when non-empty, keeps only jobs carrying that exact tag element (E5).
	Tag string
	// Agent, when non-empty, keeps only jobs run by that agent (E5).
	Agent string
	// Runner, when non-empty, keeps only jobs run on that runner (E5).
	Runner string
	// Session, when non-empty, keeps only jobs whose session_id matches exactly
	// (P3, list --session：列出某 agent 会话链的所有 turn)。
	Session string
	// Since, when > 0, keeps only jobs with started_at >= Since (E5; unix 秒)。
	Since int64
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
		// One config snapshot for the whole call (see Service.cfg / config()).
		if _, ok := s.config().Projects[opts.Project]; !ok {
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
		Caller:  opts.Caller,
		Tag:     opts.Tag,
		Agent:   opts.Agent,
		Runner:  opts.Runner,
		Session: opts.Session,
		Since:   opts.Since,
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
		// 内存 overlay 必须与 DB 路（jobstore.ListJobs WHERE）逐维一致，否则未落终态的
		// live job 会绕过新过滤维度。tag 用元素精确匹配（对应 DB 的 tags_json LIKE '%"tag"%'）。
		if opts.Project != "" && snap.ProjectKey != opts.Project {
			continue
		}
		if opts.Caller != "" && snap.CallerID != opts.Caller {
			continue
		}
		if opts.Tag != "" && !slices.Contains(snap.Tags, opts.Tag) {
			continue
		}
		if opts.Agent != "" && snap.Agent != opts.Agent {
			continue
		}
		if opts.Runner != "" && snap.Runner != opts.Runner {
			continue
		}
		if opts.Session != "" && snap.SessionID != opts.Session {
			continue
		}
		if opts.Since > 0 && snap.StartedAt < opts.Since {
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
