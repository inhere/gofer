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
//
// It ALSO prunes terminal workflows past their (independent) age (P1, design §5.4
// / D22) — connected step-jobs + workflow_events are removed连带 (PruneWorkflows),
// then their result dirs are best-effort cleaned. The returned count is the JOB
// count (the loose-job prune); the workflow prune's own count is logged separately
// (PruneWorkflowsCount). Standalone jobs that happen to belong to a NOT-yet-aged
// workflow are not double-counted: the job age policy and the workflow age policy
// are independent and each removes its own victims (a step-job is removed either by
// its workflow's prune or by the loose-job prune, whichever first selects it; the
// deletes are id-keyed and idempotent across passes).
func (s *Service) Prune() (int, error) {
	r := s.config().Storage.Retention
	now := s.nowFn().Unix()

	// Workflow retention first: drop aged terminal workflows + their step-jobs +
	// workflow_events. Doing this before the loose-job prune means a workflow's
	// step-jobs are removed via the workflow path (with the header), not left as
	// orphans for the job prune to reap piecemeal.
	wfPolicy := jobstore.WorkflowRetentionPolicy{MaxAge: r.WorkflowMaxAge()}
	if _, wfDirs, werr := s.meta.PruneWorkflows(wfPolicy, now); werr != nil {
		return 0, werr
	} else {
		// best-effort 清理工作流 result 目录：DB 行已删（真源已一致），残留目录失败
		// （已不存在/权限）无害，不阻断 prune，无诊断价值故不记日志。
		for _, dir := range wfDirs {
			if dir != "" {
				_ = os.RemoveAll(dir)
			}
		}
	}

	policy := jobstore.RetentionPolicy{MaxAge: r.MaxAge(), MaxCount: r.MaxCount}
	deleted, prunedDirs, err := s.meta.PruneJobs(policy, now)
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
