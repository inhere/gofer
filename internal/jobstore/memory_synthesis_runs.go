package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Memory synthesis run states (memory_synthesis_runs.status).
const (
	MemorySynthesisRunning   = "running"
	MemorySynthesisSucceeded = "succeeded"
	MemorySynthesisFailed    = "failed"
	MemorySynthesisSkipped   = "skipped"
)

// MemorySynthesisBeginOutcome tells the caller what BeginMemorySynthesisIfChanged did.
type MemorySynthesisBeginOutcome string

const (
	// MemorySynthesisStarted: a new running record was created; the caller owns it and must Finish it.
	MemorySynthesisStarted MemorySynthesisBeginOutcome = "started"
	// MemorySynthesisReused: a succeeded record with the same input digest already exists
	// (its last_checked_at was refreshed); nothing new was created.
	MemorySynthesisReused MemorySynthesisBeginOutcome = "reused"
	// MemorySynthesisAlreadyRunning: another live run exists for the same project and day.
	MemorySynthesisAlreadyRunning MemorySynthesisBeginOutcome = "running"
)

// ErrMemorySynthesisNotRunning is returned by FinishMemorySynthesisRun when the record is
// missing or no longer running (already finished, or failed by the restart / stale sweep).
var ErrMemorySynthesisNotRunning = errors.New("memory synthesis run is not running")

// MemorySynthesisBegin is the input of BeginMemorySynthesisIfChanged. Now and StaleBefore
// are unix seconds; a running record started before StaleBefore is treated as timed out.
type MemorySynthesisBegin struct {
	ID                 string
	ProjectKey         string
	Day                string
	InputDigest        string
	MaterialSchema     string
	MaterialCountsJSON string
	OmittedJSON        string
	CreatedBy          string
	// MaterialSnapshot is stored only when non-empty (the caller decides, keep_material_snapshot).
	MaterialSnapshot string
	Agent            string
	Now              int64
	StaleBefore      int64
}

// MemorySynthesisFinish is the terminal update of a running record.
type MemorySynthesisFinish struct {
	Status       string
	Agent        string
	Model        string
	OutputDigest string
	ResultJSON   string
	Error        string
	Now          int64
}

// BeginMemorySynthesisIfChanged is the single decision point of a daily run, taken in one
// transaction: (1) a succeeded record with the same (project, day, input_digest) is returned
// as-is and only its last_checked_at moves; (2) a live running record for the same project
// and day is returned without creating another one; (3) otherwise a running record is
// inserted (attempt = previous runs of that day + 1). Running records older than
// StaleBefore are failed first, so a crashed run never blocks the day forever. The partial
// unique indexes created by migrateMemorySynthesis back this up against any writer that
// bypasses the transaction.
func (s *Store) BeginMemorySynthesisIfChanged(in MemorySynthesisBegin) (MemorySynthesisRun, MemorySynthesisBeginOutcome, error) {
	if strings.TrimSpace(in.ID) == "" || strings.TrimSpace(in.ProjectKey) == "" || strings.TrimSpace(in.Day) == "" ||
		strings.TrimSpace(in.InputDigest) == "" || strings.TrimSpace(in.MaterialSchema) == "" {
		return MemorySynthesisRun{}, "", ErrMemorySynthesisRunInvalid
	}
	if in.Now <= 0 {
		in.Now = s.unixNow()
	}
	if in.MaterialCountsJSON == "" {
		in.MaterialCountsJSON = "{}"
	}
	if in.OmittedJSON == "" {
		in.OmittedJSON = "{}"
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return MemorySynthesisRun{}, "", fmt.Errorf("jobstore: begin memory synthesis run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if in.StaleBefore > 0 {
		if _, err := tx.Exec(`UPDATE memory_synthesis_runs SET status='failed', error='timed out', finished_at=?
 WHERE project_key=? AND day=? AND status='running' AND started_at<?`, in.Now, in.ProjectKey, in.Day, in.StaleBefore); err != nil {
			return MemorySynthesisRun{}, "", fmt.Errorf("jobstore: expire stale memory synthesis run: %w", err)
		}
	}
	row := tx.QueryRow(`SELECT `+memorySynthesisRunColumns+` FROM memory_synthesis_runs
 WHERE project_key=? AND day=? AND input_digest=? AND status='succeeded' ORDER BY started_at DESC, id DESC LIMIT 1`,
		in.ProjectKey, in.Day, in.InputDigest)
	if r, err := scanMemorySynthesisRun(row); err == nil {
		if _, err := tx.Exec(`UPDATE memory_synthesis_runs SET last_checked_at=? WHERE id=?`, in.Now, r.ID); err != nil {
			return MemorySynthesisRun{}, "", fmt.Errorf("jobstore: touch memory synthesis run: %w", err)
		}
		r.LastCheckedAt = in.Now
		if err := tx.Commit(); err != nil {
			return MemorySynthesisRun{}, "", fmt.Errorf("jobstore: commit memory synthesis run: %w", err)
		}
		return r, MemorySynthesisReused, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return MemorySynthesisRun{}, "", fmt.Errorf("jobstore: find memory synthesis run: %w", err)
	}
	row = tx.QueryRow(`SELECT `+memorySynthesisRunColumns+` FROM memory_synthesis_runs
 WHERE project_key=? AND day=? AND status='running' ORDER BY started_at DESC, id DESC LIMIT 1`, in.ProjectKey, in.Day)
	if r, err := scanMemorySynthesisRun(row); err == nil {
		if err := tx.Commit(); err != nil {
			return MemorySynthesisRun{}, "", fmt.Errorf("jobstore: commit memory synthesis run: %w", err)
		}
		return r, MemorySynthesisAlreadyRunning, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return MemorySynthesisRun{}, "", fmt.Errorf("jobstore: find running memory synthesis: %w", err)
	}
	var prior int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM memory_synthesis_runs WHERE project_key=? AND day=?`, in.ProjectKey, in.Day).Scan(&prior); err != nil {
		return MemorySynthesisRun{}, "", fmt.Errorf("jobstore: count memory synthesis runs: %w", err)
	}
	r := MemorySynthesisRun{ID: in.ID, ProjectKey: in.ProjectKey, Day: in.Day, Status: MemorySynthesisRunning,
		InputDigest: in.InputDigest, MaterialSchema: in.MaterialSchema, MaterialCountsJSON: in.MaterialCountsJSON,
		OmittedJSON: in.OmittedJSON, Agent: in.Agent, StartedAt: in.Now, LastCheckedAt: in.Now, Attempt: prior + 1,
		CreatedBy: in.CreatedBy, MaterialSnapshot: in.MaterialSnapshot}
	if _, err := tx.Exec(`INSERT INTO memory_synthesis_runs
 (id, project_key, day, status, input_digest, material_schema, material_counts_json, omitted_json,
  agent, started_at, last_checked_at, attempt, created_by, material_snapshot) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.ProjectKey, r.Day, r.Status, r.InputDigest, r.MaterialSchema, r.MaterialCountsJSON, r.OmittedJSON,
		r.Agent, r.StartedAt, r.LastCheckedAt, r.Attempt, r.CreatedBy, r.MaterialSnapshot); err != nil {
		return MemorySynthesisRun{}, "", fmt.Errorf("jobstore: insert memory synthesis run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return MemorySynthesisRun{}, "", fmt.Errorf("jobstore: commit memory synthesis run: %w", err)
	}
	return r, MemorySynthesisStarted, nil
}

// FinishMemorySynthesisRun moves a running record to its terminal state (compare-and-set on
// status='running'). A record that is gone or already terminal yields ErrMemorySynthesisNotRunning.
func (s *Store) FinishMemorySynthesisRun(id string, f MemorySynthesisFinish) (MemorySynthesisRun, error) {
	switch f.Status {
	case MemorySynthesisSucceeded, MemorySynthesisFailed, MemorySynthesisSkipped:
	default:
		return MemorySynthesisRun{}, fmt.Errorf("%w: finish status %q", ErrMemorySynthesisRunInvalid, f.Status)
	}
	if f.Now <= 0 {
		f.Now = s.unixNow()
	}
	s.writeMu.Lock()
	res, err := s.db.Exec(`UPDATE memory_synthesis_runs SET status=?, agent=CASE WHEN ?<>'' THEN ? ELSE agent END,
 model=?, output_digest=?, result_json=?, error=?, finished_at=?, last_checked_at=?
 WHERE id=? AND status='running'`,
		f.Status, f.Agent, f.Agent, f.Model, f.OutputDigest, f.ResultJSON, f.Error, f.Now, f.Now, strings.TrimSpace(id))
	s.writeMu.Unlock()
	if err != nil {
		return MemorySynthesisRun{}, fmt.Errorf("jobstore: finish memory synthesis run: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return MemorySynthesisRun{}, ErrMemorySynthesisNotRunning
	}
	r, _, err := s.GetMemorySynthesisRun(id)
	return r, err
}

// FailStaleMemorySynthesisRuns turns every running record started before `before` into
// failed with the given reason (server restart recovery) and reports how many it changed.
func (s *Store) FailStaleMemorySynthesisRuns(before, now int64, reason string) (int64, error) {
	if now <= 0 {
		now = s.unixNow()
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE memory_synthesis_runs SET status='failed', error=?, finished_at=?
 WHERE status='running' AND started_at<?`, reason, now, before)
	if err != nil {
		return 0, fmt.Errorf("jobstore: fail stale memory synthesis runs: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// PruneMemorySynthesisRuns deletes terminal records whose last activity is older than
// `before` (unix seconds). Running records are never pruned.
func (s *Store) PruneMemorySynthesisRuns(before int64) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`DELETE FROM memory_synthesis_runs WHERE status<>'running'
 AND MAX(started_at, COALESCE(finished_at,0), COALESCE(last_checked_at,0))<?`, before)
	if err != nil {
		return 0, fmt.Errorf("jobstore: prune memory synthesis runs: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// LatestSucceededMemorySynthesis returns the newest succeeded record of a project strictly
// before `day` (so a run never feeds on its own output), or ok=false.
func (s *Store) LatestSucceededMemorySynthesis(project, day string) (MemorySynthesisRun, bool, error) {
	r, err := scanMemorySynthesisRun(s.db.QueryRow(`SELECT `+memorySynthesisRunColumns+` FROM memory_synthesis_runs
 WHERE project_key=? AND day<? AND status='succeeded' ORDER BY day DESC, started_at DESC, id DESC LIMIT 1`, project, day))
	if errors.Is(err, sql.ErrNoRows) {
		return MemorySynthesisRun{}, false, nil
	}
	if err != nil {
		return MemorySynthesisRun{}, false, fmt.Errorf("jobstore: latest memory synthesis: %w", err)
	}
	return r, true, nil
}

// migrateMemorySynthesis adds the integrity indexes of memory_synthesis_runs (G057: indexes
// live in migrations, never in the base schema). Rows written before the indexes existed are
// normalised first so the unique indexes can always be built: extra running rows of a
// (project, day) are failed, and older duplicates of a succeeded (project, day, digest) are removed.
func (s *Store) migrateMemorySynthesis() error {
	stmts := []string{
		`UPDATE memory_synthesis_runs SET status='failed', error='superseded' WHERE status='running' AND rowid NOT IN
 (SELECT MAX(rowid) FROM memory_synthesis_runs WHERE status='running' GROUP BY project_key, day)`,
		`DELETE FROM memory_synthesis_runs WHERE status='succeeded' AND rowid NOT IN
 (SELECT MAX(rowid) FROM memory_synthesis_runs WHERE status='succeeded' GROUP BY project_key, day, input_digest)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_memory_synthesis_runs_running ON memory_synthesis_runs(project_key, day) WHERE status='running'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_memory_synthesis_runs_succeeded ON memory_synthesis_runs(project_key, day, input_digest) WHERE status='succeeded'`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("jobstore: migrate memory synthesis: %w", err)
		}
	}
	return nil
}
