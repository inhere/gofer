package jobstore

import (
	"database/sql"
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
	// SourceRunner is the runner of the job that last pushed this repo (TRK-05): where
	// a server-dispatched `repo sync` has to run. Empty = never seen from a job, so the
	// dispatcher falls back to the project's default runner.
	SourceRunner string `json:"source_runner,omitempty"`
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
	_, err := s.db.Exec(`INSERT INTO tracker_repos(tracker_id,project_key,rel_path,prefix,last_sync_at,sync_summary,source_runner) VALUES(?,?,?,?,?,?,?)
ON CONFLICT(tracker_id) DO UPDATE SET project_key=excluded.project_key,rel_path=excluded.rel_path,prefix=excluded.prefix,last_sync_at=excluded.last_sync_at,sync_summary=excluded.sync_summary,source_runner=CASE WHEN excluded.source_runner<>'' THEN excluded.source_runner ELSE tracker_repos.source_runner END`, repo.TrackerID, repo.ProjectKey, repo.RelPath, repo.Prefix, repo.LastSyncAt, repo.SyncSummary, repo.SourceRunner)
	return err
}

func (s *Store) ListTrackerRepos() ([]TrackerRepo, error) {
	rows, err := s.db.Query(`SELECT tracker_id,project_key,rel_path,prefix,last_sync_at,sync_summary,source_runner FROM tracker_repos ORDER BY tracker_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrackerRepo
	for rows.Next() {
		var r TrackerRepo
		if err := rows.Scan(&r.TrackerID, &r.ProjectKey, &r.RelPath, &r.Prefix, &r.LastSyncAt, &r.SyncSummary, &r.SourceRunner); err != nil {
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
	// Unconditional: an early return of a sentinel (ErrTrackerConflict) leaves err
	// nil, and a conditional rollback then leaked the transaction and its pooled
	// connection. After Commit this is a no-op (sql.ErrTxDone).
	defer func() { _ = tx.Rollback() }()
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

// TrackerSyncResult is the outcome of one record pushed by `repo sync`.
type TrackerSyncResult struct {
	// Rec is the stored record: the accepted write (new rev), or on Conflict the
	// current server record the client has not seen yet.
	Rec      TrackerRecord
	Conflict bool
	// Ahead marks a write accepted although the client claimed a newer base rev
	// than the server holds (the mirror was reset or restored).
	Ahead bool
}

// SyncTrackerIssue / SyncTrackerMemory apply one pushed record under optimistic
// concurrency. baseRev is the server rev the client last saw (0 = never seen):
// baseRev < stored rev is a stale write and is NOT applied (the current record is
// returned as a conflict); otherwise the record is stored as stored rev + 1
// (rev 1 for a new record). Lookup and write share one transaction.
func (s *Store) SyncTrackerIssue(rec TrackerRecord, baseRev int64) (TrackerSyncResult, error) {
	if rec.TrackerID == "" || rec.ID == "" {
		return TrackerSyncResult{}, fmt.Errorf("tracker_id and issue id required")
	}
	return s.syncTracker(rec, baseRev, false)
}

func (s *Store) SyncTrackerMemory(rec TrackerRecord, baseRev int64) (TrackerSyncResult, error) {
	if rec.TrackerID == "" || rec.ID == "" {
		return TrackerSyncResult{}, fmt.Errorf("tracker_id and memory key required")
	}
	return s.syncTracker(rec, baseRev, true)
}

func (s *Store) syncTracker(rec TrackerRecord, baseRev int64, memory bool) (res TrackerSyncResult, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return res, err
	}
	// Unconditional: an early return of a sentinel (ErrTrackerConflict) leaves err
	// nil, and a conditional rollback then leaked the transaction and its pooled
	// connection. After Commit this is a no-op (sql.ErrTxDone).
	defer func() { _ = tx.Rollback() }()
	old := TrackerRecord{TrackerID: rec.TrackerID, ID: rec.ID}
	var body string
	var deleted int
	if memory {
		err = tx.QueryRow(`SELECT body_json,rev,updated_at,deleted,deleted_at,deleted_by,changed_seq FROM tracker_memories WHERE tracker_id=? AND memory_key=?`, rec.TrackerID, rec.ID).Scan(&body, &old.Rev, &old.UpdatedAt, &deleted, &old.DeletedAt, &old.DeletedBy, &old.ChangedSeq)
	} else {
		err = tx.QueryRow(`SELECT body_json,rev,updated_at,changed_seq FROM tracker_issues WHERE tracker_id=? AND issue_id=?`, rec.TrackerID, rec.ID).Scan(&body, &old.Rev, &old.UpdatedAt, &old.ChangedSeq)
	}
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return res, err
	}
	err = nil
	if exists && baseRev < old.Rev {
		old.Body, old.Deleted = json.RawMessage(body), deleted != 0
		err = tx.Commit()
		return TrackerSyncResult{Rec: old, Conflict: true}, err
	}
	rec.Rev = old.Rev + 1
	res.Ahead = baseRev > old.Rev
	if _, err = tx.Exec(`INSERT INTO tracker_repos(tracker_id) VALUES(?) ON CONFLICT(tracker_id) DO NOTHING`, rec.TrackerID); err != nil {
		return res, err
	}
	if err = tx.QueryRow(`UPDATE tracker_repos SET next_seq=next_seq+1 WHERE tracker_id=? RETURNING next_seq`, rec.TrackerID).Scan(&rec.ChangedSeq); err != nil {
		return res, err
	}
	if memory {
		_, err = tx.Exec(`INSERT INTO tracker_memories(tracker_id,memory_key,body_json,rev,updated_at,deleted,deleted_at,deleted_by,changed_seq) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(tracker_id,memory_key) DO UPDATE SET body_json=excluded.body_json,rev=excluded.rev,updated_at=excluded.updated_at,deleted=excluded.deleted,deleted_at=excluded.deleted_at,deleted_by=excluded.deleted_by,changed_seq=excluded.changed_seq`, rec.TrackerID, rec.ID, string(rec.Body), rec.Rev, rec.UpdatedAt, boolInt(rec.Deleted), rec.DeletedAt, rec.DeletedBy, rec.ChangedSeq)
	} else {
		_, err = tx.Exec(`INSERT INTO tracker_issues(tracker_id,issue_id,body_json,rev,updated_at,changed_seq) VALUES(?,?,?,?,?,?) ON CONFLICT(tracker_id,issue_id) DO UPDATE SET body_json=excluded.body_json,rev=excluded.rev,updated_at=excluded.updated_at,changed_seq=excluded.changed_seq`, rec.TrackerID, rec.ID, string(rec.Body), rec.Rev, rec.UpdatedAt, rec.ChangedSeq)
	}
	if err != nil {
		return res, err
	}
	if err = tx.Commit(); err != nil {
		return res, err
	}
	res.Rec = rec
	return res, nil
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
	return s.mutateTrackerIssue(trackerID, id, expected, now, by, func(obj map[string]json.RawMessage) error {
		for k, v := range patch {
			obj[k] = v
		}
		return nil
	})
}

// mutateTrackerIssue is the shared read-modify-write of one mirrored issue: it
// loads the body, lets mutate edit it, then stamps updated_at/updated_by, bumps
// rev and the repo change sequence in one transaction.
func (s *Store) mutateTrackerIssue(trackerID, id string, expected int64, now, by string, mutate func(map[string]json.RawMessage) error) (TrackerRecord, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return TrackerRecord{}, err
	}
	// Unconditional: an early return of a sentinel (ErrTrackerConflict) leaves err
	// nil, and a conditional rollback then leaked the transaction and its pooled
	// connection. After Commit this is a no-op (sql.ErrTxDone).
	defer func() { _ = tx.Rollback() }()
	var body string
	var rev int64
	if err = tx.QueryRow(`SELECT body_json,rev FROM tracker_issues WHERE tracker_id=? AND issue_id=?`, trackerID, id).Scan(&body, &rev); err != nil {
		return TrackerRecord{}, err
	}
	if expected > 0 && expected != rev {
		err = ErrTrackerConflict
		return TrackerRecord{}, err
	}
	var obj map[string]json.RawMessage
	_ = json.Unmarshal([]byte(body), &obj)
	if obj == nil {
		obj = map[string]json.RawMessage{}
	}
	if err = mutate(obj); err != nil {
		return TrackerRecord{}, err
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
	// Unconditional: an early return of a sentinel (ErrTrackerConflict) leaves err
	// nil, and a conditional rollback then leaked the transaction and its pooled
	// connection. After Commit this is a no-op (sql.ErrTxDone).
	defer func() { _ = tx.Rollback() }()
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
	// `by` is the memory's last-writer field (tracker.Memory has no updated_by): clones
	// only keep `by`, so without it a server-side edit shows the previous author.
	if by != "" {
		obj["by"], _ = json.Marshal(by)
	}
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

// TombstoneTrackerMemoryAt writes a tombstone over one mirrored memory only while it is
// still LIVE at rev expected — a compare-and-set inside the write lock, so a sync that
// landed in between is never overwritten. A missing key, a tombstone or another rev
// returns ErrTrackerConflict and changes nothing. The tombstone gets rev expected+1 and a
// fresh changed_seq (clones pick it up on their next pull). Unlike UpsertTrackerMemory
// (the sync path, "newer rev wins") it never applies blindly.
func (s *Store) TombstoneTrackerMemoryAt(trackerID, key string, expected int64, body []byte, now, deletedBy string) (TrackerRecord, error) {
	if trackerID == "" || key == "" {
		return TrackerRecord{}, fmt.Errorf("tracker_id and memory key required")
	}
	if len(body) == 0 {
		body = []byte("{}")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return TrackerRecord{}, err
	}
	// Unconditional: an early return of a sentinel (ErrTrackerConflict) leaves err
	// nil, and a conditional rollback then leaked the transaction and its pooled
	// connection. After Commit this is a no-op (sql.ErrTxDone).
	defer func() { _ = tx.Rollback() }()
	var rev int64
	var deleted int
	err = tx.QueryRow(`SELECT rev,deleted FROM tracker_memories WHERE tracker_id=? AND memory_key=?`, trackerID, key).Scan(&rev, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrTrackerConflict
		return TrackerRecord{}, err
	}
	if err != nil {
		return TrackerRecord{}, err
	}
	if deleted != 0 || rev != expected {
		err = ErrTrackerConflict
		return TrackerRecord{}, err
	}
	var seq int64
	if err = tx.QueryRow(`UPDATE tracker_repos SET next_seq=next_seq+1 WHERE tracker_id=? RETURNING next_seq`, trackerID).Scan(&seq); err != nil {
		return TrackerRecord{}, err
	}
	rev++
	if _, err = tx.Exec(`UPDATE tracker_memories SET body_json=?,rev=?,updated_at=?,deleted=1,deleted_at=?,deleted_by=?,changed_seq=? WHERE tracker_id=? AND memory_key=?`,
		string(body), rev, now, now, deletedBy, seq, trackerID, key); err != nil {
		return TrackerRecord{}, err
	}
	if err = tx.Commit(); err != nil {
		return TrackerRecord{}, err
	}
	return TrackerRecord{TrackerID: trackerID, ID: key, Body: body, Rev: rev, UpdatedAt: now, Deleted: true,
		DeletedAt: now, DeletedBy: deletedBy, ChangedSeq: seq}, nil
}

// ErrTrackerRenameConflict means both the old and the new tracker id already exist.
var ErrTrackerRenameConflict = errors.New("tracker rename: both ids exist")

// RenameTracker moves a mirrored repository from oldID to newID across tracker_repos,
// tracker_issues and tracker_memories in one transaction. It is idempotent: when oldID
// is absent it is a no-op (renamed=false). When both exist it returns
// ErrTrackerRenameConflict and changes nothing.
func (s *Store) RenameTracker(oldID, newID string) (renamed bool, err error) {
	if oldID == "" || newID == "" || oldID == newID {
		return false, fmt.Errorf("distinct old and new tracker_id required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	// Unconditional: an early return of a sentinel (ErrTrackerConflict) leaves err
	// nil, and a conditional rollback then leaked the transaction and its pooled
	// connection. After Commit this is a no-op (sql.ErrTxDone).
	defer func() { _ = tx.Rollback() }()
	count := func(id string) (n int, e error) {
		e = tx.QueryRow(`SELECT (SELECT COUNT(*) FROM tracker_repos WHERE tracker_id=?)+(SELECT COUNT(*) FROM tracker_issues WHERE tracker_id=?)+(SELECT COUNT(*) FROM tracker_memories WHERE tracker_id=?)`, id, id, id).Scan(&n)
		return
	}
	oldN, err := count(oldID)
	if err != nil {
		return false, err
	}
	if oldN == 0 {
		err = tx.Commit()
		return false, err
	}
	newN, err := count(newID)
	if err != nil {
		return false, err
	}
	if newN > 0 {
		err = ErrTrackerRenameConflict
		return false, err
	}
	for _, table := range []string{"tracker_repos", "tracker_issues", "tracker_memories"} {
		if _, err = tx.Exec(`UPDATE `+table+` SET tracker_id=? WHERE tracker_id=?`, newID, oldID); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
