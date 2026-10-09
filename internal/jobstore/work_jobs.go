package jobstore

import (
	"fmt"
	"strings"
)

// jobSubmitOrder is the SQL "when was this job submitted" key: started_at, or — for a
// job still queued on a remote runner, whose started_at stays 0 until the worker / peer
// actually starts it — the row's updated_at, which is the submit moment until then. A
// queued rerun after a failure thus sorts as the newest job, not the oldest.
const jobSubmitOrder = `CASE WHEN started_at > 0 THEN started_at ELSE updated_at END`

// JobSubmitAt is jobSubmitOrder for a record already in memory.
func JobSubmitAt(j JobRecord) int64 {
	if j.StartedAt > 0 {
		return j.StartedAt
	}
	return j.UpdatedAt
}

// WorkItemLinkedJobs returns the jobs a work item is linked to — what its current
// sessions watch, its explicit job links, and the newest perPlan jobs of each linked
// plan — newest submitted first, at most limit rows, in ONE query (the Works list, the
// Today page and the lanes call this per item; a per-session / per-job lookup there was
// an N+1 re-run on every live push).
func (s *Store) WorkItemLinkedJobs(itemID string, limit, perPlan int) ([]JobRecord, error) {
	itemID = strings.TrimSpace(itemID)
	if limit <= 0 {
		limit = 50
	}
	if perPlan <= 0 {
		perPlan = 10
	}
	q := `WITH cand(id) AS (
  SELECT jw.job_id FROM work_item_sessions ws JOIN session_job_watches jw ON jw.session_id = ws.session_id
    WHERE ws.work_item_id = ? AND ws.role = 'current'
  UNION SELECT ref FROM work_links WHERE work_item_id = ? AND kind = 'job'
  UNION SELECT id FROM (
    SELECT id, ROW_NUMBER() OVER (PARTITION BY plan_id ORDER BY ` + jobSubmitOrder + ` DESC, id DESC) AS rn
    FROM jobs WHERE plan_id IN (SELECT ref FROM work_links WHERE work_item_id = ? AND kind = 'plan')
  ) WHERE rn <= ?
) ` + selectCols + ` WHERE id IN (SELECT id FROM cand) ORDER BY ` + jobSubmitOrder + ` DESC, id DESC LIMIT ?`
	rows, err := s.db.Query(q, itemID, itemID, itemID, perPlan, limit)
	if err != nil {
		return nil, fmt.Errorf("jobstore: work item linked jobs: %w", err)
	}
	defer rows.Close()
	out := make([]JobRecord, 0)
	for rows.Next() {
		rec, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan linked job: %w", err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// GetJobsByIDs reads several jobs in one query, keyed by id (unknown ids are absent).
func (s *Store) GetJobsByIDs(ids []string) (map[string]JobRecord, error) {
	uniq := make([]any, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	out := make(map[string]JobRecord, len(uniq))
	if len(uniq) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(selectCols+` WHERE id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(uniq)), ",")+`)`, uniq...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: get jobs by ids: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		rec, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan job row: %w", err)
		}
		out[rec.ID] = rec
	}
	return out, rows.Err()
}
