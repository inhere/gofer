package work

import "github.com/inhere/gofer/internal/jobstore"

// jobBySessionID resolves an id that is neither an agent session nor a job id to the job
// whose agent session (jobs.session_id) it is: a resident ACP job registers flows under
// the agent's own session id. It returns the newest job that has not ended, else the
// newest one (so a caller can tell "offline"); ok=false when no job carries the id.
func (s *Service) jobBySessionID(sid string) (jobstore.JobRecord, bool, error) {
	rows, err := s.store.ListJobs(jobstore.ListQuery{Session: sid, Limit: 20})
	if err != nil || len(rows) == 0 {
		return jobstore.JobRecord{}, false, err
	}
	best := rows[0]
	for _, r := range rows {
		if !jobStatusEnded(r.Status) {
			best = r
			break
		}
	}
	return best, true, nil
}
