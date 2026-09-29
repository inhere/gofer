package jobstore

import (
	"encoding/json"
	"errors"
	"fmt"
)

var ErrTrackerConflict = errors.New("tracker revision conflict")

type TrackerRepo struct {
	TrackerID   string `json:"tracker_id"`
	ProjectKey  string `json:"project_key"`
	RelPath     string `json:"rel_path"`
	Prefix      string `json:"prefix"`
	LastSyncAt  int64  `json:"last_sync_at"`
	SyncSummary string `json:"sync_summary"`
}

type TrackerRecord struct {
	TrackerID  string          `json:"tracker_id"`
	ID         string          `json:"id"`
	Body       json.RawMessage `json:"body"`
	Rev        int64           `json:"rev"`
	UpdatedAt  string          `json:"updated_at"`
	Deleted    bool            `json:"deleted"`
	DeletedAt  string          `json:"deleted_at"`
	DeletedBy  string          `json:"deleted_by"`
	ChangedSeq int64           `json:"changed_seq"`
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

func (s *Store) ListTrackerRepos() ([]TrackerRepo, error) {
	rows, err := s.db.Query(`SELECT tracker_id,project_key,rel_path,prefix,last_sync_at,sync_summary FROM tracker_repos ORDER BY tracker_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrackerRepo
	for rows.Next() {
		var r TrackerRepo
		if err := rows.Scan(&r.TrackerID, &r.ProjectKey, &r.RelPath, &r.Prefix, &r.LastSyncAt, &r.SyncSummary); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) UpsertTrackerIssue(rec TrackerRecord) error {
	if rec.TrackerID == "" || rec.ID == "" {
		return fmt.Errorf("tracker_id and issue id required")
	}
	return s.upsertTracker(rec, false)
}

func (s *Store) UpsertTrackerMemory(rec TrackerRecord) error {
	if rec.TrackerID == "" || rec.ID == "" {
		return fmt.Errorf("tracker_id and memory key required")
	}
	return s.upsertTracker(rec, true)
}

func (s *Store) upsertTracker(rec TrackerRecord, memory bool) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.Exec(`INSERT INTO tracker_repos(tracker_id) VALUES(?) ON CONFLICT(tracker_id) DO NOTHING`, rec.TrackerID); err != nil {
		return err
	}
	var seq int64
	if err = tx.QueryRow(`UPDATE tracker_repos SET next_seq=next_seq+1 WHERE tracker_id=? RETURNING next_seq`, rec.TrackerID).Scan(&seq); err != nil {
		return err
	}
	if memory {
		_, err = tx.Exec(`INSERT INTO tracker_memories(tracker_id,memory_key,body_json,rev,updated_at,deleted,deleted_at,deleted_by,changed_seq) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(tracker_id,memory_key) DO UPDATE SET body_json=excluded.body_json,rev=excluded.rev,updated_at=excluded.updated_at,deleted=excluded.deleted,deleted_at=excluded.deleted_at,deleted_by=excluded.deleted_by,changed_seq=excluded.changed_seq WHERE excluded.rev >= tracker_memories.rev`, rec.TrackerID, rec.ID, string(rec.Body), rec.Rev, rec.UpdatedAt, boolInt(rec.Deleted), rec.DeletedAt, rec.DeletedBy, seq)
	} else {
		_, err = tx.Exec(`INSERT INTO tracker_issues(tracker_id,issue_id,body_json,rev,updated_at,changed_seq) VALUES(?,?,?,?,?,?) ON CONFLICT(tracker_id,issue_id) DO UPDATE SET body_json=excluded.body_json,rev=excluded.rev,updated_at=excluded.updated_at,changed_seq=excluded.changed_seq WHERE excluded.rev >= tracker_issues.rev`, rec.TrackerID, rec.ID, string(rec.Body), rec.Rev, rec.UpdatedAt, seq)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListTrackerIssues(trackerID string, sinceRev int64) ([]TrackerRecord, error) {
	rows, err := s.db.Query(`SELECT issue_id,body_json,rev,updated_at,changed_seq FROM tracker_issues WHERE tracker_id=? AND changed_seq>? ORDER BY changed_seq,issue_id`, trackerID, sinceRev)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrackerRecord
	for rows.Next() {
		var r TrackerRecord
		var body string
		if err := rows.Scan(&r.ID, &body, &r.Rev, &r.UpdatedAt, &r.ChangedSeq); err != nil {
			return nil, err
		}
		r.TrackerID = trackerID
		r.Body = json.RawMessage(body)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) ListTrackerMemories(trackerID string, sinceRev int64) ([]TrackerRecord, error) {
	rows, err := s.db.Query(`SELECT memory_key,body_json,rev,updated_at,deleted,deleted_at,deleted_by,changed_seq FROM tracker_memories WHERE tracker_id=? AND changed_seq>? ORDER BY changed_seq,memory_key`, trackerID, sinceRev)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrackerRecord
	for rows.Next() {
		var r TrackerRecord
		var body string
		var deleted int
		if err := rows.Scan(&r.ID, &body, &r.Rev, &r.UpdatedAt, &deleted, &r.DeletedAt, &r.DeletedBy, &r.ChangedSeq); err != nil {
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

func (s *Store) PatchTrackerIssue(trackerID, id string, expected int64, patch map[string]json.RawMessage, now, by string) (TrackerRecord, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return TrackerRecord{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var body string
	var rev int64
	if err = tx.QueryRow(`SELECT body_json,rev FROM tracker_issues WHERE tracker_id=? AND issue_id=?`, trackerID, id).Scan(&body, &rev); err != nil {
		return TrackerRecord{}, err
	}
	if expected > 0 && expected != rev {
		return TrackerRecord{}, ErrTrackerConflict
	}
	var obj map[string]json.RawMessage
	_ = json.Unmarshal([]byte(body), &obj)
	for k, v := range patch {
		obj[k] = v
	}
	obj["updated_at"], _ = json.Marshal(now)
	obj["updated_by"], _ = json.Marshal(by)
	out, _ := json.Marshal(obj)
	var seq int64
	if err = tx.QueryRow(`UPDATE tracker_repos SET next_seq=next_seq+1 WHERE tracker_id=? RETURNING next_seq`, trackerID).Scan(&seq); err != nil {
		return TrackerRecord{}, err
	}
	rev++
	if _, err = tx.Exec(`UPDATE tracker_issues SET body_json=?,rev=?,updated_at=?,changed_seq=? WHERE tracker_id=? AND issue_id=?`, string(out), rev, now, seq, trackerID, id); err != nil {
		return TrackerRecord{}, err
	}
	if err = tx.Commit(); err != nil {
		return TrackerRecord{}, err
	}
	return TrackerRecord{TrackerID: trackerID, ID: id, Body: out, Rev: rev, UpdatedAt: now, ChangedSeq: seq}, nil
}

func (s *Store) PatchTrackerMemory(trackerID, id string, expected int64, patch map[string]json.RawMessage, now, by string) (TrackerRecord, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return TrackerRecord{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var body string
	var rev int64
	if err = tx.QueryRow(`SELECT body_json,rev FROM tracker_memories WHERE tracker_id=? AND memory_key=?`, trackerID, id).Scan(&body, &rev); err != nil {
		return TrackerRecord{}, err
	}
	if expected > 0 && expected != rev {
		return TrackerRecord{}, ErrTrackerConflict
	}
	var obj map[string]json.RawMessage
	_ = json.Unmarshal([]byte(body), &obj)
	for k, v := range patch {
		obj[k] = v
	}
	obj["updated_at"], _ = json.Marshal(now)
	obj["updated_by"], _ = json.Marshal(by)
	out, _ := json.Marshal(obj)
	var seq int64
	if err = tx.QueryRow(`UPDATE tracker_repos SET next_seq=next_seq+1 WHERE tracker_id=? RETURNING next_seq`, trackerID).Scan(&seq); err != nil {
		return TrackerRecord{}, err
	}
	rev++
	if _, err = tx.Exec(`UPDATE tracker_memories SET body_json=?,rev=?,updated_at=?,changed_seq=? WHERE tracker_id=? AND memory_key=?`, string(out), rev, now, seq, trackerID, id); err != nil {
		return TrackerRecord{}, err
	}
	if err = tx.Commit(); err != nil {
		return TrackerRecord{}, err
	}
	return TrackerRecord{TrackerID: trackerID, ID: id, Body: out, Rev: rev, UpdatedAt: now, ChangedSeq: seq}, nil
}
