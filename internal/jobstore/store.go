// Package jobstore is the SQLite-backed metadata/index store for gofer
// jobs. It is the C1 fix (see docs/design/2026-06-18-sqlite-store-design.md):
// the in-memory job table, jobs.jsonl index and result.json metadata all grow
// without bound on a long-running server. This package moves that state into a
// single SQLite database so listing is one filtered/paginated SQL query and
// terminal jobs no longer have to live in memory.
//
// Job logs (stdout.log/stderr.log) stay as files in the per-job result dir; only
// metadata/index (and, from SP4, interactions) live here.
//
// The package uses modernc.org/sqlite (pure Go, no cgo) so the binary still
// builds in the gcc-less container. It depends on no other internal package — in
// particular NOT internal/job — so that the job service can adopt it (SP2/SP3)
// without forming a job -> jobstore -> job import cycle; JobRecord is therefore a
// neutral struct rather than job.JobResult.
package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

const (
	// DefaultListLimit caps ListJobs when the caller passes Limit <= 0. Mirrors
	// the job package's list default so behaviour is unchanged after the cutover.
	DefaultListLimit = 200
	// busyTimeoutMS is how long a blocked writer waits for the database lock
	// before failing with SQLITE_BUSY. Writes are tiny and infrequent (status /
	// interaction changes), so a few seconds absorbs any realistic contention.
	busyTimeoutMS = 5000
)

// Store is a handle to the SQLite job database. It is safe for concurrent use:
// the underlying *sql.DB is a connection pool and SQLite (in WAL mode) lets
// readers and the single writer proceed concurrently.
//
// writeMu serialises writes in-process so only one SQLite writer is ever active.
// WAL + busy_timeout alone proved insufficient under full-speed concurrent
// upserts (intermittent SQLITE_BUSY "database is locked"); since this is a
// single process owning a single db file, an in-process write lock removes the
// contention entirely while leaving reads (GetJob/ListJobs) free to run on the
// pool concurrently.
type Store struct {
	db      *sql.DB
	writeMu sync.Mutex
}

// schemaStmts is the full DDL, one statement per element so it works regardless
// of whether the driver supports multi-statement Exec. Both tables are created
// up front (建库/建表); SP1 only exercises the jobs table, the interactions table
// is populated from SP4. All statements are IF NOT EXISTS so Open is idempotent.
var schemaStmts = []string{
	`CREATE TABLE IF NOT EXISTS jobs (
  id           TEXT PRIMARY KEY,
  project_key  TEXT NOT NULL,
  agent        TEXT NOT NULL,
  runner       TEXT NOT NULL,
  worker_id    TEXT,
  status       TEXT NOT NULL,
  exit_code    INTEGER NOT NULL DEFAULT 0,
  cwd          TEXT,
  result_dir   TEXT NOT NULL,
  request_json TEXT,
  error        TEXT,
  started_at   INTEGER NOT NULL,
  ended_at     INTEGER,
  updated_at   INTEGER NOT NULL,
  rendered_command TEXT,
  result_json      TEXT,
  artifacts_json   TEXT,
  diff_summary     TEXT,
  source           TEXT,
  tags_json        TEXT,
  workflow_id      TEXT,
  step_index       INTEGER,
  session_id       TEXT,
  channel          TEXT,
  client           TEXT
)`,
	`CREATE INDEX IF NOT EXISTS idx_jobs_started ON jobs(started_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_jobs_proj_status ON jobs(project_key, status)`,
	`CREATE TABLE IF NOT EXISTS interactions (
  id           TEXT NOT NULL,
  job_id       TEXT NOT NULL,
  type         TEXT NOT NULL,
  prompt       TEXT NOT NULL,
  options_json TEXT,
  status       TEXT NOT NULL,
  answer       TEXT,
  created_at   INTEGER NOT NULL,
  answered_at  INTEGER,
  PRIMARY KEY (job_id, id)
)`,
	`CREATE INDEX IF NOT EXISTS idx_inter_job ON interactions(job_id)`,
	// job_events is the append-only lifecycle event stream (E13). One row per
	// recorded event; seq is the monotonic global insertion order (AUTOINCREMENT)
	// used as the SSE/poll cursor (?since=<seq>). detail_json is an optional JSON
	// blob (nullable). The (job_id, seq) index serves ListJobEvents' per-job,
	// seq-ordered scan. Like every table here it is IF NOT EXISTS (idempotent Open).
	`CREATE TABLE IF NOT EXISTS job_events (
  seq         INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id      TEXT    NOT NULL,
  type        TEXT    NOT NULL,
  detail_json TEXT,
  at          INTEGER NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_job_events_job ON job_events(job_id, seq)`,
	// event_deliveries is the E14 webhook outbound queue / state machine (design
	// §5.6). One row per (event, webhook target): status moves pending -> delivered
	// or pending -> ... -> failed under the delivery sweeper. next_retry_at is the
	// unix-second time the row becomes due (initially now); the sweeper claims
	// pending rows whose next_retry_at <= now via a conditional UPDATE (SR303), so
	// a delivery is only ever picked up by one sweep. idx_deliveries_due serves that
	// due-scan. Like every table here it is IF NOT EXISTS (idempotent Open).
	`CREATE TABLE IF NOT EXISTS event_deliveries (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  event_seq     INTEGER NOT NULL,
  job_id        TEXT    NOT NULL,
  target        TEXT    NOT NULL,
  status        TEXT    NOT NULL,
  attempts      INTEGER NOT NULL DEFAULT 0,
  next_retry_at INTEGER NOT NULL,
  last_error    TEXT,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_deliveries_due ON event_deliveries(status, next_retry_at)`,
	`CREATE INDEX IF NOT EXISTS idx_deliveries_job ON event_deliveries(job_id, id)`,
	// workflows is the job-chain header table (工作流, design §5.1). One row per
	// submitted workflow: status (running/done/failed/cancelled), current_step (the
	// 1-based active step) and total_steps frame the串行推进; spec_json holds the full
	// WorkflowSpec (steps) so the engine can rebuild each step's JobRequest. caller_id
	// is inherited by every step-job (D8). current_step is moved via a conditional
	// UPDATE (AdvanceCurrentStep) so推进幂等 (SR303): a step is never started twice.
	// idx_workflows_status serves the sweeper's running-workflow scan. IF NOT EXISTS
	// like every table here (idempotent Open).
	`CREATE TABLE IF NOT EXISTS workflows (
  id           TEXT PRIMARY KEY,
  title        TEXT,
  status       TEXT NOT NULL,
  current_step INTEGER NOT NULL,
  total_steps  INTEGER NOT NULL,
  spec_json    TEXT NOT NULL,
  caller_id    TEXT,
  error        TEXT,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_workflows_status ON workflows(status)`,
	// workflow_events is the workflow-level append-only event stream (P1, design
	// §5.4), the workflow analogue of job_events. One row per recorded event; seq is
	// the monotonic global insertion order (AUTOINCREMENT) used as the poll cursor
	// (?since=<seq>). detail_json is an optional JSON blob (nullable). The
	// (workflow_id, seq) index serves ListWorkflowEvents' per-workflow, seq-ordered
	// scan. IF NOT EXISTS like every table here (idempotent Open).
	`CREATE TABLE IF NOT EXISTS workflow_events (
  seq         INTEGER PRIMARY KEY AUTOINCREMENT,
  workflow_id TEXT    NOT NULL,
  type        TEXT    NOT NULL,
  detail_json TEXT,
  at          INTEGER NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_workflow_events_wf ON workflow_events(workflow_id, seq)`,
	// agent_presence is the driver-agent registry / 名册 (E36, design §9). One row
	// per registered driver agent (the协作主体, distinct from a job agent which is a
	// work unit): agent_id is the serve-issued uuid, agent_token the软隔离 secret the
	// agent presents on inbox/deregister ops (compared in-process, not a real auth).
	// status is the last-written liveness hint; the authoritative online/offline is
	// computed lazily from last_seen_at vs the TTL (presence.Service), so a stale row
	// never has to be rewritten to flip offline. registered_at/last_seen_at are unix
	// seconds; meta_json is an optional JSON blob (nullable). IF NOT EXISTS like every
	// table here (idempotent Open).
	`CREATE TABLE IF NOT EXISTS agent_presence (
  agent_id      TEXT PRIMARY KEY,
  agent_token   TEXT NOT NULL,
  name          TEXT NOT NULL,
  role          TEXT,
  project_key   TEXT,
  caller_id     TEXT,
  client        TEXT,
  status        TEXT NOT NULL,
  registered_at INTEGER NOT NULL,
  last_seen_at  INTEGER NOT NULL,
  meta_json     TEXT
)`,
	`CREATE INDEX IF NOT EXISTS idx_presence_seen ON agent_presence(last_seen_at)`,
	// messages is the agent inbox / 信箱 (E36, design §9). One row per (recipient,
	// message): a direct send is a single row; a role:/broadcast send is fanned out
	// to one row per online recipient (to_agent = that agent_id, to_spec records the
	// original addressing like "role:reviewer"). status is unread/read; a消费 (poll
	// with ack) flips it to read + stamps read_at. created_at/expires_at/read_at are
	// unix seconds (expires_at 0 = no TTL). idx_messages_inbox serves the per-agent
	// unread, creation-ordered inbox scan. IF NOT EXISTS like every table here.
	`CREATE TABLE IF NOT EXISTS messages (
  id         TEXT PRIMARY KEY,
  to_agent   TEXT NOT NULL,
  from_agent TEXT NOT NULL,
  to_spec    TEXT,
  kind       TEXT NOT NULL,
  body       TEXT,
  ref        TEXT,
  status     TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER,
  read_at    INTEGER
)`,
	`CREATE INDEX IF NOT EXISTS idx_messages_inbox ON messages(to_agent, status, created_at)`,
}

// Open opens (creating if absent) the SQLite database at path, applies the schema
// and returns a ready Store. The parent directory is created if needed; the db
// file is restricted to 0600 (private; see design §12). Callers must Close it.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("jobstore: empty db path")
	}
	// SQLite creates the db file but not its parent directory.
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("jobstore: create db dir: %w", err)
		}
	}

	// modernc applies every _pragma to EACH pooled connection as it is opened,
	// so busy_timeout/foreign_keys hold for all goroutines (not just the first).
	// WAL is a persistent db setting; re-asserting it per connection is harmless.
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)",
		path, busyTimeoutMS,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("jobstore: open %q: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("jobstore: ping %q: %w", path, err)
	}

	s := &Store{db: db}
	if err := s.applySchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	// migrate runs AFTER applySchema so additive columns/indexes introduced after
	// the initial schema are present on both fresh and pre-existing databases.
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Best-effort: the db (and its -wal/-shm side files) live in the private logs
	// area; tighten perms on the main file regardless of umask.
	_ = os.Chmod(path, 0o600)
	return s, nil
}

// applySchema runs the DDL. Each statement is idempotent (IF NOT EXISTS), so it
// is safe to call on every Open.
func (s *Store) applySchema() error {
	for _, stmt := range schemaStmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("jobstore: apply schema: %w", err)
		}
	}
	return nil
}

// migrate adds columns/indexes introduced after the initial C1 schema (additive
// only — never drops or rewrites). SQLite has no ADD COLUMN IF NOT EXISTS, so we
// probe `PRAGMA table_info` first; the partial unique index is created here (not
// in schemaStmts) because it references request_id, which does not exist on a
// pre-existing C1 database until the ALTER below runs. Idempotent on every Open.
func (s *Store) migrate() error {
	cols, err := s.tableColumns("jobs")
	if err != nil {
		return err
	}
	add := func(col, ddl string) error {
		if _, ok := cols[col]; ok {
			return nil
		}
		if _, e := s.db.Exec("ALTER TABLE jobs ADD COLUMN " + ddl); e != nil {
			return fmt.Errorf("jobstore: migrate add %s: %w", col, e)
		}
		return nil
	}
	if err := add("caller_id", "caller_id TEXT"); err != nil { // C2
		return err
	}
	if err := add("request_id", "request_id TEXT"); err != nil { // C5
		return err
	}
	// 产出与审计（job-outcomes-audit）：4 列 additive 加入，旧库经 migrate 自动补全。
	if err := add("rendered_command", "rendered_command TEXT"); err != nil { // E15 渲染命令
		return err
	}
	if err := add("result_json", "result_json TEXT"); err != nil { // E6 结构化结果
		return err
	}
	if err := add("artifacts_json", "artifacts_json TEXT"); err != nil { // E1 产物清单(P2)
		return err
	}
	if err := add("diff_summary", "diff_summary TEXT"); err != nil { // E12 diff 摘要(P3)
		return err
	}
	if err := add("source", "source TEXT"); err != nil { // P4 执行来源 worker:/peer:
		return err
	}
	if err := add("tags_json", "tags_json TEXT"); err != nil { // E5 job 标签（JSON 数组）
		return err
	}
	// 工作流(job 链)：step-job 反向关联其所属 workflow + 1-based 步序号，additive 加入，
	// 旧库经 migrate 自动补全（旧 job 两列为空/NULL，selectCols COALESCE 成零值）。
	if err := add("workflow_id", "workflow_id TEXT"); err != nil { // 所属工作流 id（空=非工作流 job）
		return err
	}
	if err := add("step_index", "step_index INTEGER"); err != nil { // 在工作流中的 1-based 步序号
		return err
	}
	// 工作流 v2 (P1)：step-job 的 1-based 重试 attempt（首次=1）。旧行 COALESCE→1。
	if err := add("attempt", "attempt INTEGER"); err != nil { // 重试尝试号（P1）
		return err
	}
	// 工作流 v2 (P2)：fan-out 同 step 内第几个并行 job（1-based；非 fan 为 0）。P1 不写，
	// 与 attempt 一并 ALTER ADD 以减少后续迁移（design §5.3）。旧行 COALESCE→0。
	if err := add("fan_index", "fan_index INTEGER"); err != nil { // fan-out 并行序号（P2，预留）
		return err
	}
	// session 捕获：底层 agent CLI 会话标识（claude/codex）。旧库自动 ALTER ADD，
	// 旧行 COALESCE→""（session-capture，design §6.2）。
	if err := add("session_id", "session_id TEXT"); err != nil { // agent CLI 会话 id
		return err
	}
	// 提交来源（provenance）：channel=cli/web/mcp/im，client=来源主机/IP。旧库 ALTER ADD，
	// 旧行 COALESCE→""。配合既有 caller_id 标识"谁/哪台/经哪个渠道提交"。
	if err := add("channel", "channel TEXT"); err != nil { // 提交渠道
		return err
	}
	if err := add("client", "client TEXT"); err != nil { // 来源主机/IP
		return err
	}
	if err := s.migrateWorkflows(); err != nil {
		return err
	}
	// Partial unique index: only non-empty request_id values are constrained, so
	// jobs without a request_id never collide. Created after the column exists.
	if _, err := s.db.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_request_id ON jobs(request_id) WHERE request_id <> ''`,
	); err != nil {
		return fmt.Errorf("jobstore: migrate request_id index: %w", err)
	}
	return nil
}

// migrateWorkflows adds the工作流 v2 columns to the workflows table (P1, design
// §5.2). All additive (ALTER ADD), idempotent (probe PRAGMA first), so a
// pre-existing v1 database gains them with旧行 COALESCEd to the v1-equivalent
// zero value. P1 uses step_attempt/next_step_at; parent_* are added now (P3, with
// no writers yet) to avoid a second migration pass (plan T1.2 note).
func (s *Store) migrateWorkflows() error {
	cols, err := s.tableColumns("workflows")
	if err != nil {
		return err
	}
	add := func(col, ddl string) error {
		if _, ok := cols[col]; ok {
			return nil
		}
		if _, e := s.db.Exec("ALTER TABLE workflows ADD COLUMN " + ddl); e != nil {
			return fmt.Errorf("jobstore: migrate workflows add %s: %w", col, e)
		}
		return nil
	}
	// P1: the active step's 1-based attempt (旧行 COALESCE→1) + 退避到点时间 (旧行 →0).
	if err := add("step_attempt", "step_attempt INTEGER"); err != nil {
		return err
	}
	if err := add("next_step_at", "next_step_at INTEGER"); err != nil {
		return err
	}
	// P3 (预留)：子工作流的父 wf id + 在父中的 step 序号。P1 无写入方，提前 ALTER ADD
	// 减少后续迁移（design §5.2）。旧行 COALESCE→""/0。
	if err := add("parent_workflow_id", "parent_workflow_id TEXT"); err != nil {
		return err
	}
	if err := add("parent_step_index", "parent_step_index INTEGER"); err != nil {
		return err
	}
	return nil
}

// tableColumns returns the set of column names of a table via PRAGMA table_info.
func (s *Store) tableColumns(table string) (map[string]struct{}, error) {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, fmt.Errorf("jobstore: table_info(%s): %w", table, err)
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var (
			cid         int
			name, typ   string
			notnull, pk int
			dflt        any
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, fmt.Errorf("jobstore: scan table_info: %w", err)
		}
		out[name] = struct{}{}
	}
	return out, rows.Err()
}

// Close closes the underlying database. WAL auto-checkpoints on the final close,
// so no explicit checkpoint is needed for graceful shutdown (design §14).
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
