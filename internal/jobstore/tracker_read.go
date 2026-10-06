package jobstore

// FindTrackerIssues returns the mirror rows of one issue id across every tracker repo
// (ids are only unique inside a repo, so the same id may exist twice). It is a read
// helper for the work service's completion write-back.
func (s *Store) FindTrackerIssues(issueID string) ([]TrackerRecord, error) {
	rows, err := s.db.Query(`SELECT tracker_id, body_json, rev, updated_at, changed_seq FROM tracker_issues WHERE issue_id=?`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrackerRecord
	for rows.Next() {
		var r TrackerRecord
		var body string
		if err := rows.Scan(&r.TrackerID, &body, &r.Rev, &r.UpdatedAt, &r.ChangedSeq); err != nil {
			return nil, err
		}
		r.ID = issueID
		r.Body = []byte(body)
		out = append(out, r)
	}
	return out, rows.Err()
}
