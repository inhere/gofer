package jobstore

import (
	"encoding/json"
	"fmt"
)

type TrackerRepo struct {
	TrackerID   string `json:"tracker_id"`
	ProjectKey  string `json:"project_key"`
	RelPath     string `json:"rel_path"`
	Prefix      string `json:"prefix"`
	LastSyncAt  int64  `json:"last_sync_at"`
	SyncSummary string `json:"sync_summary"`
}

type TrackerRecord struct {
	TrackerID string
	ID        string
	Body      json.RawMessage
	Rev       int64
	UpdatedAt string
	Deleted   bool
	DeletedAt string
	DeletedBy string
}

func (s *Store) UpsertTrackerRepo(repo TrackerRepo) error {
	if repo.TrackerID == "" {
		return fmt.Errorf("tracker_id required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`INSERT INTO tracker_repos(tracker_id,project_key,rel_path,prefix,last_sync_at,sync_summary) VALUES(?,?,?,?,?,?)
ON CONFLICT(tracker_id) DO UPDATE SET project_key=excluded.project_key,rel_path=excluded.rel_path,prefix=excluded.prefix,last_sync_at=excluded.last_sync_at,sync_summary=excluded.sync_summary`, repo.TrackerID, repo.ProjectKey, repo.RelPath, repo.Prefix, repo.LastSyncAt, repo.SyncSummary)
	return err
}

func (s *Store) UpsertTrackerIssue(rec TrackerRecord) error {
	if rec.TrackerID == "" || rec.ID == "" {
		return fmt.Errorf("tracker_id and issue id required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`INSERT INTO tracker_issues(tracker_id,issue_id,body_json,rev,updated_at) VALUES(?,?,?,?,?)
ON CONFLICT(tracker_id,issue_id) DO UPDATE SET body_json=excluded.body_json,rev=excluded.rev,updated_at=excluded.updated_at`, rec.TrackerID, rec.ID, string(rec.Body), rec.Rev, rec.UpdatedAt)
	return err
}

func (s *Store) UpsertTrackerMemory(rec TrackerRecord) error {
	if rec.TrackerID == "" || rec.ID == "" {
		return fmt.Errorf("tracker_id and memory key required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`INSERT INTO tracker_memories(tracker_id,memory_key,body_json,rev,updated_at,deleted,deleted_at,deleted_by) VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(tracker_id,memory_key) DO UPDATE SET body_json=excluded.body_json,rev=excluded.rev,updated_at=excluded.updated_at,deleted=excluded.deleted,deleted_at=excluded.deleted_at,deleted_by=excluded.deleted_by`, rec.TrackerID, rec.ID, string(rec.Body), rec.Rev, rec.UpdatedAt, boolInt(rec.Deleted), rec.DeletedAt, rec.DeletedBy)
	return err
}

func (s *Store) ListTrackerIssues(trackerID string, sinceRev int64) ([]TrackerRecord, error) {
	rows, err := s.db.Query(`SELECT issue_id,body_json,rev,updated_at FROM tracker_issues WHERE tracker_id=? AND rev>? ORDER BY rev,issue_id`, trackerID, sinceRev)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrackerRecord
	for rows.Next() {
		var r TrackerRecord
		var body string
		if err := rows.Scan(&r.ID, &body, &r.Rev, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.TrackerID = trackerID
		r.Body = json.RawMessage(body)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) ListTrackerMemories(trackerID string, sinceRev int64) ([]TrackerRecord, error) {
	rows, err := s.db.Query(`SELECT memory_key,body_json,rev,updated_at,deleted,deleted_at,deleted_by FROM tracker_memories WHERE tracker_id=? AND rev>? ORDER BY rev,memory_key`, trackerID, sinceRev)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrackerRecord
	for rows.Next() {
		var r TrackerRecord
		var body string
		var deleted int
		if err := rows.Scan(&r.ID, &body, &r.Rev, &r.UpdatedAt, &deleted, &r.DeletedAt, &r.DeletedBy); err != nil {
			return nil, err
		}
		r.TrackerID = trackerID
		r.Body = json.RawMessage(body)
		r.Deleted = deleted != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
