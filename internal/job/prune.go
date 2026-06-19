package job

import (
	"os"

	"github.com/inhere/gofer/internal/jobstore"
)

// Prune enforces the configured retention policy (storage.retention): it evicts
// terminal jobs from the metadata store per the policy and best-effort removes
// each evicted job's on-disk log directory. It returns the number of jobs
// deleted.
//
// Only terminal jobs are touched (PruneJobs guarantees this); live jobs in
// memory are unaffected — an evicted job is, by definition, already terminal and
// no longer in s.jobs. When retention is unconfigured the policy is zero and
// PruneJobs is a no-op, so Prune is safe to call unconditionally.
func (s *Service) Prune() (int, error) {
	r := s.config().Storage.Retention
	policy := jobstore.RetentionPolicy{MaxAge: r.MaxAge(), MaxCount: r.MaxCount}
	deleted, prunedDirs, err := s.meta.PruneJobs(policy, s.nowFn().Unix())
	if err != nil {
		return 0, err
	}
	// The DB rows are gone; remove their log directories best-effort. A failure
	// here (e.g. dir already gone, permissions) must not fail the prune — the
	// authoritative state (the DB) is already consistent.
	for _, dir := range prunedDirs {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
	}
	return deleted, nil
}
