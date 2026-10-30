package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// MemorySynthesisRun is the auditable, model-independent record for one P1 material
// snapshot. Candidate rows are intentionally absent; result_json is the future output slot.
type MemorySynthesisRun struct {
	ID                 string `json:"id"`
	ProjectKey         string `json:"project"`
	Day                string `json:"day"`
	Status             string `json:"status"`
	InputDigest        string `json:"input_digest"`
	MaterialSchema     string `json:"material_schema"`
	MaterialCountsJSON string `json:"material_counts_json"`
	OmittedJSON        string `json:"omitted_json"`
	Agent              string `json:"agent,omitempty"`
	Model              string `json:"model,omitempty"`
	OutputDigest       string `json:"output_digest,omitempty"`
	ResultJSON         string `json:"result_json,omitempty"`
	Error              string `json:"error,omitempty"`
	StartedAt          int64  `json:"started_at"`
	FinishedAt         int64  `json:"finished_at,omitempty"`
	LastCheckedAt      int64  `json:"last_checked_at,omitempty"`
	Attempt            int    `json:"attempt"`
	CreatedBy          string `json:"created_by,omitempty"`
	MaterialSnapshot   string `json:"material_snapshot,omitempty"`
}

type MemorySynthesisRunFilter struct {
	ProjectKey string
	Day        string
	Status     string
	Limit      int
}

var (
	ErrMemorySynthesisRunNotFound = errors.New("memory synthesis run not found")
	ErrMemorySynthesisRunInvalid  = errors.New("invalid memory synthesis run")
)

const memorySynthesisRunColumns = `id, project_key, day, status, input_digest, material_schema,
 COALESCE(material_counts_json,'{}'), COALESCE(omitted_json,'{}'), COALESCE(agent,''), COALESCE(model,''),
 COALESCE(output_digest,''), COALESCE(result_json,''), COALESCE(error,''), started_at,
 COALESCE(finished_at,0), COALESCE(last_checked_at,0), attempt, COALESCE(created_by,''), COALESCE(material_snapshot,'')`

func scanMemorySynthesisRun(row interface{ Scan(...any) error }) (MemorySynthesisRun, error) {
	var r MemorySynthesisRun
	err := row.Scan(&r.ID, &r.ProjectKey, &r.Day, &r.Status, &r.InputDigest, &r.MaterialSchema,
		&r.MaterialCountsJSON, &r.OmittedJSON, &r.Agent, &r.Model, &r.OutputDigest, &r.ResultJSON,
		&r.Error, &r.StartedAt, &r.FinishedAt, &r.LastCheckedAt, &r.Attempt, &r.CreatedBy, &r.MaterialSnapshot)
	return r, err
}

func validateMemorySynthesisRun(r MemorySynthesisRun) error {
	if strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.ProjectKey) == "" || strings.TrimSpace(r.Day) == "" || strings.TrimSpace(r.InputDigest) == "" {
		return ErrMemorySynthesisRunInvalid
	}
	switch r.Status {
	case "running", "succeeded", "failed", "skipped":
	default:
		return fmt.Errorf("%w: unknown status %q", ErrMemorySynthesisRunInvalid, r.Status)
	}
	if r.MaterialSchema == "" {
		return fmt.Errorf("%w: material schema is required", ErrMemorySynthesisRunInvalid)
	}
	return nil
}

// InsertMemorySynthesisRun records one run. The caller controls the id so retries and
// test fixtures can be deterministic; duplicate ids are rejected by SQLite.
func (s *Store) InsertMemorySynthesisRun(r MemorySynthesisRun) error {
	if err := validateMemorySynthesisRun(r); err != nil {
		return err
	}
	if r.Attempt <= 0 {
		r.Attempt = 1
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`INSERT INTO memory_synthesis_runs
 (id, project_key, day, status, input_digest, material_schema, material_counts_json, omitted_json,
  agent, model, output_digest, result_json, error, started_at, finished_at, last_checked_at,
  attempt, created_by, material_snapshot) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.ProjectKey, r.Day, r.Status, r.InputDigest, r.MaterialSchema, r.MaterialCountsJSON, r.OmittedJSON,
		r.Agent, r.Model, r.OutputDigest, r.ResultJSON, r.Error, r.StartedAt, nullableInt64(r.FinishedAt),
		nullableInt64(r.LastCheckedAt), r.Attempt, r.CreatedBy, r.MaterialSnapshot)
	if err != nil {
		return fmt.Errorf("jobstore: insert memory synthesis run: %w", err)
	}
	return nil
}

// UpsertMemorySynthesisRun is useful to a retrying coordinator and preserves the same
// audit row identity. It is intentionally not a candidate/state transition API.
func (s *Store) UpsertMemorySynthesisRun(r MemorySynthesisRun) error {
	if err := validateMemorySynthesisRun(r); err != nil {
		return err
	}
	if r.Attempt <= 0 {
		r.Attempt = 1
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`INSERT INTO memory_synthesis_runs
 (id, project_key, day, status, input_digest, material_schema, material_counts_json, omitted_json,
  agent, model, output_digest, result_json, error, started_at, finished_at, last_checked_at,
  attempt, created_by, material_snapshot) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(id) DO UPDATE SET project_key=excluded.project_key, day=excluded.day, status=excluded.status,
 input_digest=excluded.input_digest, material_schema=excluded.material_schema,
 material_counts_json=excluded.material_counts_json, omitted_json=excluded.omitted_json,
 agent=excluded.agent, model=excluded.model, output_digest=excluded.output_digest,
 result_json=excluded.result_json, error=excluded.error, started_at=excluded.started_at,
 finished_at=excluded.finished_at, last_checked_at=excluded.last_checked_at, attempt=excluded.attempt,
 created_by=excluded.created_by, material_snapshot=excluded.material_snapshot`,
		r.ID, r.ProjectKey, r.Day, r.Status, r.InputDigest, r.MaterialSchema, r.MaterialCountsJSON, r.OmittedJSON,
		r.Agent, r.Model, r.OutputDigest, r.ResultJSON, r.Error, r.StartedAt, nullableInt64(r.FinishedAt),
		nullableInt64(r.LastCheckedAt), r.Attempt, r.CreatedBy, r.MaterialSnapshot)
	if err != nil {
		return fmt.Errorf("jobstore: upsert memory synthesis run: %w", err)
	}
	return nil
}

func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func (s *Store) GetMemorySynthesisRun(id string) (MemorySynthesisRun, bool, error) {
	r, err := scanMemorySynthesisRun(s.db.QueryRow(`SELECT `+memorySynthesisRunColumns+` FROM memory_synthesis_runs WHERE id=?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return MemorySynthesisRun{}, false, nil
	}
	if err != nil {
		return MemorySynthesisRun{}, false, fmt.Errorf("jobstore: get memory synthesis run: %w", err)
	}
	return r, true, nil
}

func (s *Store) ListMemorySynthesisRuns(f MemorySynthesisRunFilter) ([]MemorySynthesisRun, error) {
	where := []string{"1=1"}
	args := make([]any, 0, 3)
	if f.ProjectKey != "" {
		where = append(where, "project_key=?")
		args = append(args, f.ProjectKey)
	}
	if f.Day != "" {
		where = append(where, "day=?")
		args = append(args, f.Day)
	}
	if f.Status != "" && f.Status != "all" {
		where = append(where, "status=?")
		args = append(args, f.Status)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT `+memorySynthesisRunColumns+` FROM memory_synthesis_runs WHERE `+strings.Join(where, " AND ")+` ORDER BY started_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list memory synthesis runs: %w", err)
	}
	defer rows.Close()
	out := make([]MemorySynthesisRun, 0)
	for rows.Next() {
		r, err := scanMemorySynthesisRun(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan memory synthesis run: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list memory synthesis runs: %w", err)
	}
	return out, nil
}
