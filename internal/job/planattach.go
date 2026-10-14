package job

import "strings"

// planattach.go binds an EXISTING job to a plan (`gofer plan attach <plan> <job>`,
// POST /v1/plans/{id}/jobs). It lives in the job Service rather than being a bare
// metadata UPDATE because the durable row is a PROJECTION of the in-memory entry:
// a write that only touches the store is invisible to every reader this process
// serves (Get/ListJobs return the entry) and is undone by the next persist — the
// finish write carries plan_id from the snapshot. gofer-z414 hit exactly that: a
// job attached while it was still tracked kept running "in no plan", and a
// continuation of it (ResumeJob inherits src.PlanID) inherited nothing.
func (s *Service) AttachJobToPlan(jobID, planID string) (bool, error) {
	jobID, planID = strings.TrimSpace(jobID), strings.TrimSpace(planID)
	if jobID == "" {
		return false, nil
	}
	entry := s.entry(jobID)
	if entry == nil {
		// Not tracked here (another process's job, or one evicted after finishing):
		// the row is the only copy, so the plain store update is the whole change.
		return s.meta.AttachJobToPlan(jobID, planID)
	}
	entry.mu.Lock()
	if entry.result.PlanID == planID {
		entry.mu.Unlock()
		return true, nil
	}
	entry.result.PlanID = planID
	snap := entry.result
	entry.mu.Unlock()
	if err := s.persist(snap); err != nil {
		return false, err
	}
	return true, nil
}
