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
	// AnsweredBy 记录"谁应答了该 interaction"（监督分层升级路由 P3.2, design §10 审计区分）：
	// auto:<policy>(L0 内置规则器) / agent:<id>(L1 owner / L2 sup) / human(L3 web/CLI)。
	// "" 表示尚未应答或未归因（旧库/relay，COALESCE→""）。
	AnsweredBy string
	// NeedsHuman 标记该 interaction 已被通用 sup 判为高危/拿不准、显式留给人处理（事件驱动按需
	// 派发 y5wt）：1=留给人。CountSupPendingDemand 据此把它排除出 sup demand，避免反复唤醒 sup
	// 去重新拒答同一条。0=未标记（旧库/未拒答，COALESCE→0）。
	NeedsHuman int64
}

// selectInterCols is the shared projection for ListInteractions. COALESCE guards
// the nullable columns (options_json/answer/answered_at) so a NULL scans into the
// zero value instead of failing the scan, mirroring jobs.selectCols.
const selectInterCols = `SELECT id, job_id, type, prompt, COALESCE(options_json,''),
  status, COALESCE(answer,''), created_at, COALESCE(answered_at,0),
  COALESCE(escalated_at,0), COALESCE(answered_by,''), COALESCE(needs_human,0)
  FROM interactions`

// scanInteraction reads one row (in selectInterCols order) into an InteractionRecord.
func scanInteraction(sc rowScanner) (InteractionRecord, error) {
	var r InteractionRecord
	err := sc.Scan(
		&r.ID, &r.JobID, &r.Type, &r.Prompt, &r.OptionsJSON,
		&r.Status, &r.Answer, &r.CreatedAt, &r.AnsweredAt,
		&r.EscalatedAt, &r.AnsweredBy, &r.NeedsHuman,
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
   escalated_at, answered_by, needs_human)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
  ON CONFLICT(job_id, id) DO UPDATE SET
    type=excluded.type,
    prompt=excluded.prompt,
    options_json=excluded.options_json,
    status=excluded.status,
    answer=excluded.answer,
    created_at=excluded.created_at,
    answered_at=excluded.answered_at,
    escalated_at=excluded.escalated_at,
    answered_by=excluded.answered_by,
    needs_human=excluded.needs_human`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(q,
		rec.ID, rec.JobID, rec.Type, rec.Prompt, rec.OptionsJSON,
		rec.Status, rec.Answer, rec.CreatedAt, rec.AnsweredAt,
		rec.EscalatedAt, rec.AnsweredBy, rec.NeedsHuman,
	)
	if err != nil {
		return fmt.Errorf("jobstore: upsert interaction %q/%q: %w", rec.JobID, rec.ID, err)
	}
	return nil
}

// MarkInteractionEscalated stamps escalated_at on one interaction row — the
// supervisor's owner-first routing dedup + owner-timeout clock (P1.2 / design §9). It
// is a TARGETED UPDATE (not a full upsert) so it touches only escalated_at and never
// clobbers status/answer written elsewhere; an unknown (job_id, id) is a silent no-op
// (0 rows). Writes go through writeMu like every other writer so SQLite never sees two
// concurrent writers.
func (s *Store) MarkInteractionEscalated(jobID, interactionID string, ts int64) error {
	if jobID == "" || interactionID == "" {
		return errors.New("jobstore: MarkInteractionEscalated: empty job/interaction id")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(
		`UPDATE interactions SET escalated_at=? WHERE job_id=? AND id=?`,
		ts, jobID, interactionID,
	); err != nil {
		return fmt.Errorf("jobstore: mark interaction %q/%q escalated: %w", jobID, interactionID, err)
	}
	return nil
}

// MarkInteractionNeedsHuman sets needs_human=1 on one interaction row — the通用 sup 对
// 高危/拿不准的 interaction 拒答（留给人）的标记（事件驱动按需派发 y5wt）。TARGETED UPDATE
// (not a full upsert) so it touches only needs_human and never clobbers status/answer; an
// unknown (job_id, id) is a silent no-op (0 rows). Writes go through writeMu like every
// other writer. The interaction itself stays pending (a human answers it later).
func (s *Store) MarkInteractionNeedsHuman(jobID, interactionID string) error {
	if jobID == "" || interactionID == "" {
		return errors.New("jobstore: MarkInteractionNeedsHuman: empty job/interaction id")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(
		`UPDATE interactions SET needs_human=1 WHERE job_id=? AND id=?`,
		jobID, interactionID,
	); err != nil {
		return fmt.Errorf("jobstore: mark interaction %q/%q needs-human: %w", jobID, interactionID, err)
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
  COALESCE(i.escalated_at,0), COALESCE(i.answered_by,''), COALESCE(i.needs_human,0)
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

// CountSupPendingDemand counts pending interactions that genuinely NEED a通用 sup (L2)
// right now — the durable demand signal driving the event-driven sup reconciler (y5wt). It
// mirrors ListPendingInteractions' active-job JOIN + terminal-job filter, and excludes:
//   - needs_human=1 rows: a sup already saw them and punted to a human (never re-wake a sup
//     to re-decline the same high-risk one — the议程1 infinite-respawn trap, avoided here);
//   - jobs whose own role is supervisor (套娃防护, design §8.4): a sup's own interaction must
//     never route back to a sup.
//
// PRECISION (avoids waking a sup for the COMMON owner-pending case): an interaction routed
// to its OWNER (L1) and still within the owner-answer window is NOT sup demand — the owner
// should answer it. It counts only interactions the router would route to the SUP (design
// §8.1/§8.2): no owner at all (origin_agent==''), OR an owner whose answer window has
// elapsed (now - escalated_at > ownerTimeoutSec, mirroring maybeOwnerTimeoutFallback). So a
// freshly owner-escalated interaction stays off demand until its owner times out. now is the
// current unix seconds; ownerTimeoutSec mirrors supervisor.owner_answer_timeout_sec. 0 demand
// ⇒ no sup dispatched ⇒ idle零成本.
func (s *Store) CountSupPendingDemand(ownerTimeoutSec, now int64) (int, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(pendingInteractionTerminalJobStatuses)), ",")
	q := `SELECT COUNT(*) FROM interactions i JOIN jobs j ON i.job_id = j.id
  WHERE i.status = 'pending' AND j.status NOT IN (` + placeholders + `)
    AND COALESCE(i.needs_human,0) = 0
    AND COALESCE(j.role,'') <> 'supervisor'
    AND ( COALESCE(j.origin_agent,'') = ''
          OR (COALESCE(i.escalated_at,0) > 0 AND (? - COALESCE(i.escalated_at,0)) > ?) )`
	args := make([]any, 0, len(pendingInteractionTerminalJobStatuses)+2)
	for _, st := range pendingInteractionTerminalJobStatuses {
		args = append(args, st)
	}
	args = append(args, now, ownerTimeoutSec)
	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("jobstore: count sup pending demand: %w", err)
	}
	return n, nil
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
