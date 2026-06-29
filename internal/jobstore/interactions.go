package jobstore

import (
	"errors"
	"fmt"
	"strings"
)

// InteractionRecord is the SQLite-persisted projection of one running-job
// interaction (SP4, design §8). Like JobRecord it is a neutral struct, decoupled
// from internal/job, so the interactions table can be populated without this
// package importing job (which would form a job -> jobstore -> job cycle).
//
// OptionsJSON holds the marshalled []InteractionOption (the choice/confirmation
// options) as the job package serialises it; an empty string means "no options".
// Answer/AnsweredAt are unset (empty / 0) while the interaction is still pending;
// the nullable columns COALESCE to those zero values on read (see selectInterCols).
type InteractionRecord struct {
	ID          string
	JobID       string
	Type        string
	Prompt      string
	OptionsJSON string
	Status      string
	Answer      string
	CreatedAt   int64
	AnsweredAt  int64
	// EscalatedAt 是该 interaction 被 escalate（投递给上层应答者）的 unix 秒时间戳（监督分层
	// 升级路由 P1.1, design §9）：承载 escalate dedup 标记 + owner 超时计时。0 表示尚未
	// escalate（旧库/未升级，COALESCE→0）。P1.1 仅落列 + 读出，写入由 P1.2/P2.1 落地。
	EscalatedAt int64
}

// selectInterCols is the shared projection for ListInteractions. COALESCE guards
// the nullable columns (options_json/answer/answered_at) so a NULL scans into the
// zero value instead of failing the scan, mirroring jobs.selectCols.
const selectInterCols = `SELECT id, job_id, type, prompt, COALESCE(options_json,''),
  status, COALESCE(answer,''), created_at, COALESCE(answered_at,0),
  COALESCE(escalated_at,0)
  FROM interactions`

// scanInteraction reads one row (in selectInterCols order) into an InteractionRecord.
func scanInteraction(sc rowScanner) (InteractionRecord, error) {
	var r InteractionRecord
	err := sc.Scan(
		&r.ID, &r.JobID, &r.Type, &r.Prompt, &r.OptionsJSON,
		&r.Status, &r.Answer, &r.CreatedAt, &r.AnsweredAt,
		&r.EscalatedAt,
	)
	return r, err
}

// UpsertInteraction inserts an interaction row or updates the existing one with
// the same (job_id, id). The pending and answered writes for one interaction are
// two upserts on the same row (the answered snapshot overwrites the pending one),
// so the table keeps a single, latest row per interaction — the DB equivalent of
// folding interactions.jsonl. Writes go through s.writeMu (like UpsertJob) so
// SQLite never sees two concurrent writers and cannot return SQLITE_BUSY.
func (s *Store) UpsertInteraction(rec InteractionRecord) error {
	if rec.ID == "" {
		return errors.New("jobstore: UpsertInteraction: empty interaction id")
	}
	if rec.JobID == "" {
		return errors.New("jobstore: UpsertInteraction: empty job id")
	}
	const q = `INSERT INTO interactions
  (id, job_id, type, prompt, options_json, status, answer, created_at, answered_at,
   escalated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?)
  ON CONFLICT(job_id, id) DO UPDATE SET
    type=excluded.type,
    prompt=excluded.prompt,
    options_json=excluded.options_json,
    status=excluded.status,
    answer=excluded.answer,
    created_at=excluded.created_at,
    answered_at=excluded.answered_at,
    escalated_at=excluded.escalated_at`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(q,
		rec.ID, rec.JobID, rec.Type, rec.Prompt, rec.OptionsJSON,
		rec.Status, rec.Answer, rec.CreatedAt, rec.AnsweredAt,
		rec.EscalatedAt,
	)
	if err != nil {
		return fmt.Errorf("jobstore: upsert interaction %q/%q: %w", rec.JobID, rec.ID, err)
	}
	return nil
}

// ListInteractions returns the interactions for a job in creation order
// (created_at asc, id asc as a stable tiebreaker). A job with no interactions
// yields an empty slice and no error.
func (s *Store) ListInteractions(jobID string) ([]InteractionRecord, error) {
	rows, err := s.db.Query(selectInterCols+" WHERE job_id = ? ORDER BY created_at ASC, id ASC", jobID)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list interactions %q: %w", jobID, err)
	}
	defer rows.Close()

	out := make([]InteractionRecord, 0)
	for rows.Next() {
		rec, scanErr := scanInteraction(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan interaction row: %w", scanErr)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list interactions %q rows: %w", jobID, err)
	}
	return out, nil
}

// pendingInteractionTerminalJobStatuses mirrors the job package's terminal set
// (done/failed/cancelled/timeout). Kept as literals here (like prune.go's
// terminalStatuses) to avoid a job -> jobstore -> job import cycle.
var pendingInteractionTerminalJobStatuses = []string{"done", "failed", "cancelled", "timeout"}

// ListPendingInteractions returns the pending interactions across ALL jobs that
// are still ACTIVE (the job's status is NOT terminal), creation order (E25 监督).
// The JOIN + terminal-job filter (复审 #4) excludes 僵尸 pending rows left on a job
// that finished while an interaction was unanswered — a supervisor must never be
// pointed at a dead job. (finish() reconciles those to cancelled, and
// ReconcileOrphanInteractions sweeps crash残留; this filter is the read-side guard.)
func (s *Store) ListPendingInteractions() ([]InteractionRecord, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(pendingInteractionTerminalJobStatuses)), ",")
	q := `SELECT i.id, i.job_id, i.type, i.prompt, COALESCE(i.options_json,''),
  i.status, COALESCE(i.answer,''), i.created_at, COALESCE(i.answered_at,0),
  COALESCE(i.escalated_at,0)
  FROM interactions i JOIN jobs j ON i.job_id = j.id
  WHERE i.status = 'pending' AND j.status NOT IN (` + placeholders + `)
  ORDER BY i.created_at ASC, i.id ASC`
	args := make([]any, 0, len(pendingInteractionTerminalJobStatuses))
	for _, st := range pendingInteractionTerminalJobStatuses {
		args = append(args, st)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list pending interactions: %w", err)
	}
	defer rows.Close()

	out := make([]InteractionRecord, 0)
	for rows.Next() {
		rec, scanErr := scanInteraction(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan pending interaction row: %w", scanErr)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list pending interactions rows: %w", err)
	}
	return out, nil
}

// ReconcileOrphanInteractions flips to cancelled every pending interaction whose
// job is already terminal — the crash-recovery backstop (复审 #4) for the in-memory
// reconciliation finish() does (a process that died mid-job never ran finish, so
// its pending rows are stuck). ts stamps answered_at. Returns the rows fixed. Run
// once at serve startup. Writes go through writeMu like every other writer.
func (s *Store) ReconcileOrphanInteractions(ts int64) (int, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(pendingInteractionTerminalJobStatuses)), ",")
	q := `UPDATE interactions SET status = 'cancelled', answered_at = ?
  WHERE status = 'pending'
    AND job_id IN (SELECT id FROM jobs WHERE status IN (` + placeholders + `))`
	args := make([]any, 0, len(pendingInteractionTerminalJobStatuses)+1)
	args = append(args, ts)
	for _, st := range pendingInteractionTerminalJobStatuses {
		args = append(args, st)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return 0, fmt.Errorf("jobstore: reconcile orphan interactions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("jobstore: reconcile orphan interactions rows: %w", err)
	}
	return int(n), nil
}
