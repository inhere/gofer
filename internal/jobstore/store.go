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
// builds in the gcc-less container. It depends on no other internal package but
// internal/util (leaf helpers) and internal/skill (whose Repo interface the skills
// table implements) — in particular NOT internal/job — so that the job service can
// adopt it (SP2/SP3) without forming a job -> jobstore -> job import cycle;
// JobRecord is therefore a neutral struct rather than job.JobResult.
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
	db *sql.DB
	// path is the db file Open was given, kept for the DBStats file picture
	// (size / -wal) the dashboard reports; SQLite itself has no "current file"
	// pragma.
	path    string
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
  interactive  INTEGER NOT NULL DEFAULT 0,
  worker_id    TEXT,
  worker_instance_id TEXT,
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
  ndjson_kept      INTEGER,
  ndjson_dropped   INTEGER,
  ndjson_truncated INTEGER,
  source           TEXT,
  tags_json        TEXT,
  workflow_id      TEXT,
  step_index       INTEGER,
  session_id       TEXT,
  stop_reason      TEXT,
  channel          TEXT,
  client           TEXT,
  origin_agent     TEXT,
  escalate_to      TEXT,
  role         TEXT,
  plan_id      TEXT,
  todo_id      TEXT,
  base_sha     TEXT,
  commits_json TEXT,
  source_job_id    TEXT,
  timeout_sec      INTEGER,
  requested_timeout_sec INTEGER,
  timeout_clamped  INTEGER NOT NULL DEFAULT 0,
  recovering_since INTEGER,
  worktree_path    TEXT,
  worktree_branch  TEXT,
  worktree_base_sha TEXT,
  worktree_head_sha TEXT,
  commits_ahead    INTEGER,
  read_only        INTEGER NOT NULL DEFAULT 0,
  require_review   INTEGER NOT NULL DEFAULT 0,
  reviewed_by      TEXT,
  reviewed_at      INTEGER,
  review_note      TEXT,
  verify_json      TEXT,
  failure_class    TEXT,
  fell_back_from   TEXT,
  fell_back_to     TEXT,
  requested_agent  TEXT,
  fallback_json    TEXT,
  usage_json       TEXT,
  xfer_json        TEXT,
  skills_json      TEXT,
  dir_exclusive    INTEGER NOT NULL DEFAULT 0,
  leader_of_plan   TEXT
)`,
	`CREATE INDEX IF NOT EXISTS idx_jobs_started ON jobs(started_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_jobs_proj_status ON jobs(project_key, status)`,
	// idx_jobs_agent_started serves the SUP-01 P3 per-agent health aggregation
	// (jobs of one agent inside a time window) — the read behind `gofer agent status`,
	// the web badge and the pre-dispatch decision.
	`CREATE INDEX IF NOT EXISTS idx_jobs_agent_started ON jobs(agent, started_at)`,
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
  escalated_at INTEGER,
  answered_by  TEXT,
  needs_human  INTEGER,
  tool_call_json TEXT,
  policy_hint  TEXT,
  expires_at   INTEGER,
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
  updated_at    INTEGER NOT NULL,
  body          TEXT,
  event_type    TEXT
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
	`CREATE TABLE IF NOT EXISTS schedules (
  id           TEXT NOT NULL,
  name         TEXT NOT NULL,
  schedule_type TEXT NOT NULL DEFAULT 'cron',
  cron_expr    TEXT NOT NULL,
  request_json TEXT NOT NULL,
  enabled      INTEGER NOT NULL,
  next_run_at  INTEGER NOT NULL,
  last_run_at  INTEGER,
  last_job_id  TEXT,
  catch_up     INTEGER,
  project_key  TEXT,
  -- AUTO-02b: trigger_token is the schedule's own webhook secret (empty = the webhook
  -- endpoint is not enabled for it). Added by migrateSchedules on a pre-existing db.
  trigger_token TEXT,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  PRIMARY KEY (id)
)`,
	`CREATE INDEX IF NOT EXISTS idx_sched_due ON schedules(enabled, next_run_at)`,
	// pty_sessions is the WEB-03 P3 one-table record of an established pty relay
	// recording (jobstore-owned, design §3). httpapi is the sole writer (Upsert on
	// Open and finalize); the recording download gate reads it. recording_uri is
	// <result_dir>/pty.cast (empty = not recorded / write failed / TTL-expired).
	// encrypted is 1 yes / 2 no (SR301 从1起避0). Times are unix seconds. Both
	// tables/indexes are IF NOT EXISTS so Open stays idempotent — a fresh new table
	// needs no migrate() ALTER.
	`CREATE TABLE IF NOT EXISTS pty_sessions (
  pty_session_id TEXT PRIMARY KEY,
  job_id         TEXT NOT NULL,
  worker_id      TEXT,
  instance_id    TEXT,
  owner          TEXT,
  state          TEXT NOT NULL,
  cols           INTEGER,
  rows           INTEGER,
  recording_uri  TEXT,
  encrypted      INTEGER NOT NULL DEFAULT 2,
  bytes_in       INTEGER NOT NULL DEFAULT 0,
  bytes_out      INTEGER NOT NULL DEFAULT 0,
  started_at     INTEGER NOT NULL,
  ended_at       INTEGER
)`,
	`CREATE INDEX IF NOT EXISTS idx_pty_sessions_job   ON pty_sessions(job_id)`,
	`CREATE INDEX IF NOT EXISTS idx_pty_sessions_ended ON pty_sessions(ended_at)`,
	// plans is the plan-orchestration grouping header. One row per plan; jobs
	// join it via jobs.plan_id. It is a pure grouping container, not a workflow
	// engine state machine.
	`CREATE TABLE IF NOT EXISTS plans (
  plan_id      TEXT PRIMARY KEY,
  title        TEXT,
  description  TEXT,
  status       TEXT NOT NULL,
  owner        TEXT,
  progress     INTEGER NOT NULL DEFAULT 0,
  project_key  TEXT,
  -- PLAN-03: paused stops the automatic chain advance (the plan holds where it is);
  -- blocked_todo names the item a FAILED job parked the chain on (status='blocked'
  -- while set). Both are added by migratePlans on a pre-existing db.
  paused       INTEGER NOT NULL DEFAULT 0,
  blocked_todo TEXT,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_plans_status ON plans(status)`,
	// plan_todos is the plan-orchestration checklist table. job_id NULL means a
	// plain todo; a non-empty job_id binds the item to one job run as metadata.
	// The PLAN-02 columns (assignee … dispatch_error) make a todo DISPATCHABLE: they
	// are the request `Submit` is called with once the item turns `ready` with an
	// assignee.
	`CREATE TABLE IF NOT EXISTS plan_todos (
  todo_id        TEXT PRIMARY KEY,
  plan_id        TEXT NOT NULL,
  job_id         TEXT,
  title          TEXT,
  done           INTEGER NOT NULL DEFAULT 0,
  sort           INTEGER NOT NULL DEFAULT 0,
  assignee       TEXT,
  project_key    TEXT,
  template       TEXT,
  vars_json      TEXT,
  verify_json    TEXT,
  review         INTEGER,
  runner         TEXT,
  cwd            TEXT,
  timeout_sec    INTEGER,
  dispatch_error TEXT,
  -- PLAN-03 chain columns: after_json is the list of plan-todo ids this item waits
  -- for, auto (default 1) allows the chain to start it automatically, cmd_json is the
  -- argv an exec item runs. Added by migratePlanTodos on a pre-existing db.
  after_json     TEXT,
  auto           INTEGER NOT NULL DEFAULT 1,
  cmd_json       TEXT,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_plan_todos_plan ON plan_todos(plan_id)`,
	// plan_decisions is the决策通道 (decision-channel, Part C §C3) table: an agent
	// raises a blocking question (gofer_ask_human), a human answers it on the web.
	// plan_id NULL means a global question (no plan attached); options_json NULL
	// means free-text answer. state OPEN|ANSWERED|EXPIRED; expiry is lazy
	// (expireDueDecisions on the read/answer paths). All timestamps are unix
	// SECONDS, matching unixNow() (workflows.go) — never milliseconds.
	`CREATE TABLE IF NOT EXISTS plan_decisions (
  id          TEXT PRIMARY KEY,
  plan_id     TEXT,
  title       TEXT NOT NULL,
  question    TEXT NOT NULL,
  options_json TEXT,
  answer      TEXT,
  state       TEXT NOT NULL DEFAULT 'OPEN',
  timeout_sec INTEGER NOT NULL DEFAULT 1800,
  asked_at    INTEGER NOT NULL,
  answered_at INTEGER,
  answered_by TEXT,
  released_by TEXT,
  detail      TEXT
)`,
	`CREATE INDEX IF NOT EXISTS idx_plan_decisions_plan ON plan_decisions(plan_id)`,
	`CREATE INDEX IF NOT EXISTS idx_plan_decisions_state ON plan_decisions(state)`,
	// agent_sessions is the terminal agent-CLI session registry (session relay,
	// SESS-01 §5): registered by the CLI's hooks, observed on the web, and the
	// owner of the per-session relay switch — relay_mode (auto|on|off, R1).
	// caller_id is the authenticated caller that registered the session — its
	// owner (SUP-01 D). session_id is the CLI's own id. Relay turns live in
	// plan_decisions (kind='relay', session_id set — the additive columns are
	// added by migratePlanDecisions for pre-existing dbs); the R1/R2 columns are
	// added by migrateAgentSessions.
	`CREATE TABLE IF NOT EXISTS agent_sessions (
  session_id   TEXT PRIMARY KEY,
  agent        TEXT NOT NULL,
  project_key  TEXT,
  runner       TEXT,
  cwd          TEXT,
  title        TEXT,
  transcript   TEXT,
  tmux_pane    TEXT,
  caller_id    TEXT,
  state        TEXT NOT NULL DEFAULT 'running',
  relay_mode   TEXT NOT NULL DEFAULT 'auto',
  -- historic column: pre-R1 binaries wrote mode=='on' here; nothing reads or
  -- writes it since v0.48 (kept because dropping a SQLite column is a table
  -- rebuild, and the one-time relay_mode backfill in migrateAgentSessions reads
  -- it on a pre-R1 db).
  relay        INTEGER NOT NULL DEFAULT 0,
  idle_sec     INTEGER,
  last_human_at INTEGER NOT NULL DEFAULT 0,
  turn_no      INTEGER NOT NULL DEFAULT 0,
  last_message TEXT,
  last_event   TEXT,
  last_seen_at INTEGER NOT NULL,
  started_at   INTEGER NOT NULL,
  ended_at     INTEGER,
  handed_off_job_id TEXT,
  handed_off_at     INTEGER
)`,
	`CREATE INDEX IF NOT EXISTS idx_agent_sessions_seen ON agent_sessions(state, last_seen_at)`,
	`CREATE INDEX IF NOT EXISTS idx_agent_sessions_project ON agent_sessions(project_key)`,
	// xfers is the XFER-01 file-transfer journal (design §一.2). One row per
	// transfer: op put|get, the runner that executes it (worker id or `local`),
	// the project-relative destination/source path, the byte size + sha256 and the
	// state machine staged|dispatched|done|failed|expired. The payload itself lives
	// on disk in the staging area (<storage root or config dir>/xfer/<id>), NEVER in
	// this table. caller_id is who asked (audit); job_id is RESERVED for X2's
	// `job run --upload/--collect` (a transfer owned by a job) and stays empty in X1.
	// expires_at (unix seconds) drives the xfer prune loop. idx_xfers_expiry serves
	// that sweep, idx_xfers_created the management listing. IF NOT EXISTS like every
	// table here (idempotent Open).
	`CREATE TABLE IF NOT EXISTS xfers (
  id           TEXT PRIMARY KEY,
  op           TEXT NOT NULL,
  runner       TEXT NOT NULL,
  project_key  TEXT NOT NULL,
  path         TEXT NOT NULL,
  size         INTEGER NOT NULL DEFAULT 0,
  sha256       TEXT,
  state        TEXT NOT NULL,
  error        TEXT,
  caller_id    TEXT,
  force        INTEGER NOT NULL DEFAULT 0,
  job_id       TEXT,
  created_at   INTEGER NOT NULL,
  finished_at  INTEGER,
  expires_at   INTEGER
)`,
	`CREATE INDEX IF NOT EXISTS idx_xfers_expiry ON xfers(state, expires_at)`,
	`CREATE INDEX IF NOT EXISTS idx_xfers_created ON xfers(created_at DESC)`,
	// job_wakeups is the JOB-09 wakeup registry (design §五.1). One row per wakeup
	// registered on a job: a timer (at/every/cron) or an event subscription, either
	// of which starts a CONTINUATION of that job when it fires. event_types_json and
	// filter_status_json hold JSON arrays of strings (opaque here, like
	// schedules.request_json); the fire path alone writes continuation_job_id /
	// fired_count / coalesced_count. idx_wakeups_due serves the sweeper's
	// DueWakeups, idx_wakeups_job the job-detail listing. IF NOT EXISTS like every
	// table here (idempotent Open).
	`CREATE TABLE IF NOT EXISTS job_wakeups (
  id                  TEXT PRIMARY KEY,
  job_id              TEXT NOT NULL,
  kind                TEXT NOT NULL,
  at                  INTEGER,
  every_sec           INTEGER,
  cron_expr           TEXT,
  timezone            TEXT,
  event_types_json    TEXT,
  filter_job_id       TEXT,
  filter_status_json  TEXT,
  mode                TEXT NOT NULL DEFAULT 'once',
  instruction         TEXT,
  enabled             INTEGER NOT NULL DEFAULT 1,
  revision            INTEGER NOT NULL DEFAULT 1,
  next_run_at         INTEGER,
  last_fired_at       INTEGER,
  fired_count         INTEGER NOT NULL DEFAULT 0,
  coalesced_count     INTEGER NOT NULL DEFAULT 0,
  continuation_job_id TEXT,
  created_by          TEXT,
  created_at          INTEGER NOT NULL,
  expires_at          INTEGER
)`,
	`CREATE INDEX IF NOT EXISTS idx_wakeups_due ON job_wakeups(enabled, next_run_at)`,
	`CREATE INDEX IF NOT EXISTS idx_wakeups_job ON job_wakeups(job_id)`,
	// job_retries is the durable job-level retry queue (R2/AUTO-03, design §二.1).
	// One row per retry the finish path SCHEDULED for a failed job (source_job_id),
	// holding the exact JobRequest to re-submit (opaque here, like
	// schedules.request_json), the attempt it will run as, why it was scheduled and
	// when it becomes due. The serve sweeper claims due rows with a lease
	// (ClaimDueRetries) so a process restart or a crash mid-claim never loses a
	// retry; idx_job_retries_due serves that claim, and the row rides its source
	// job's retention (see PruneJobs). IF NOT EXISTS like every table here
	// (idempotent Open).
	`CREATE TABLE IF NOT EXISTS job_retries (
  id            TEXT PRIMARY KEY,
  source_job_id TEXT NOT NULL,
  attempt       INTEGER NOT NULL,
  request_json  TEXT NOT NULL,
  reason        TEXT NOT NULL,
  next_run_at   INTEGER NOT NULL,
  lease_until   INTEGER NOT NULL DEFAULT 0,
  state         TEXT NOT NULL DEFAULT 'pending',
  new_job_id    TEXT,
  created_at    INTEGER NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_job_retries_due ON job_retries(state, next_run_at)`,
	// skills is the skill library index (JOB-10, design §一.1). One row per skill in
	// <config-dir>/skills/<name>/; the files themselves stay in that directory (they
	// ride the config backup) and the row keeps only what `skill ls`, the binding
	// resolver and the change detection need — where it came from, a version hash and
	// the per-file sha256 list. files_json is opaque JSON here, exactly like
	// schedules.request_json, so this package needs no import of internal/skill; the
	// skill package's JobstoreRepo is the adapter. IF NOT EXISTS like every table
	// here (idempotent Open).
	`CREATE TABLE IF NOT EXISTS skills (
  name        TEXT PRIMARY KEY,
  description TEXT,
  source      TEXT,
  source_ref  TEXT,
  version     TEXT,
  files_json  TEXT,
  size        INTEGER NOT NULL DEFAULT 0,
  updated_at  INTEGER NOT NULL DEFAULT 0,
  updated_by  TEXT
)`,
	// comments is the comment thread on a job / plan / plan-todo (MCP-05 阶段 A,
	// design §二.A.1). One row per comment; scope+scope_id name the object it is
	// written on, author_kind (user|agent|system) is what the @-mention dispatch gate
	// keys on, mentions_json is the parsed @-name list (opaque JSON here, like every
	// other *_json column) and triggered_job_id links the job this comment started
	// (NULL = it started none). Comments are owned by their object: PruneJobs and
	// DeleteTodo sweep the matching thread. IF NOT EXISTS like every table here
	// (idempotent Open).
	`CREATE TABLE IF NOT EXISTS comments (
  id               TEXT PRIMARY KEY,
  scope            TEXT NOT NULL,
  scope_id         TEXT NOT NULL,
  author           TEXT NOT NULL,
  author_kind      TEXT NOT NULL,
  body             TEXT NOT NULL,
  mentions_json    TEXT,
  created_at       INTEGER NOT NULL,
  triggered_job_id TEXT
)`,
	`CREATE INDEX IF NOT EXISTS idx_comments_scope ON comments(scope, scope_id, created_at)`,
	// plan_leader_wakes is the durable "wake the leader" queue (MCP-05 阶段 B, design
	// §二.B): one row per member job whose FINISHED state armed a leader round. It is a
	// table of its own rather than a job_wakeups row because a leader wake starts a NEW
	// job (a different agent, its own prompt) instead of continuing the member job the
	// wakeup table is built around — and because a restart must lose neither the pending
	// round nor the plan's round count, both of which live here.
	//
	// state: pending (armed, waiting for due_at) | firing (a sweep claimed it; a submit
	// is in flight) | fired (leader_job_id holds the job it started) | cancelled (a human
	// spoke, or the plan was paused before it fired). round is the 1-based leader round
	// this wake belongs to; the plan's spent budget is the count of firing|fired rows.
	// IF NOT EXISTS like every table here (idempotent Open).
	`CREATE TABLE IF NOT EXISTS plan_leader_wakes (
  id            TEXT PRIMARY KEY,
  plan_id       TEXT NOT NULL,
  member_job_id TEXT NOT NULL,
  member_status TEXT NOT NULL,
  due_at        INTEGER NOT NULL,
  state         TEXT NOT NULL DEFAULT 'pending',
  round         INTEGER NOT NULL DEFAULT 1,
  leader_job_id TEXT,
  created_at    INTEGER NOT NULL,
  cancelled_by  TEXT,
  cancelled_at  INTEGER
)`,
	`CREATE INDEX IF NOT EXISTS idx_leader_wakes_due ON plan_leader_wakes(state, due_at)`,
	`CREATE INDEX IF NOT EXISTS idx_leader_wakes_plan ON plan_leader_wakes(plan_id, state)`,
	`CREATE INDEX IF NOT EXISTS idx_leader_wakes_member ON plan_leader_wakes(member_job_id)`,
	// job_tokens is SEC-01's job-scoped credential registry: one row per job the hub
	// minted a credential for, holding only the sha256 of the token (the secret lives
	// in the job process's environment and, for a dispatched job, on the wire — never
	// here). job_id is the PRIMARY KEY because a job has at most one live credential
	// and a re-submitted id supersedes the old one. expires_at is the fallback deadline
	// that covers a job whose terminal path never ran (a crashed hub). IF NOT EXISTS
	// like every table here (idempotent Open).
	`CREATE TABLE IF NOT EXISTS job_tokens (
  job_id     TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL,
  kind       TEXT NOT NULL,
  plan_id    TEXT NOT NULL DEFAULT '',
  expires_at INTEGER NOT NULL,
  revoked_at INTEGER,
  created_at INTEGER NOT NULL
)`,
	// The credential lookup is BY HASH (the presented token is hashed and matched), so
	// this index is the hot path of every job-authenticated request.
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_job_tokens_hash ON job_tokens(token_hash)`,
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

	s := &Store{db: db, path: path}
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
	if err := add("interactive", "interactive INTEGER NOT NULL DEFAULT 0"); err != nil { // WEB-03 P1
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
	if err := add("ndjson_kept", "ndjson_kept INTEGER"); err != nil { // ndjson 采集过滤器保留行数
		return err
	}
	if err := add("ndjson_dropped", "ndjson_dropped INTEGER"); err != nil { // ndjson 采集过滤器丢弃行数
		return err
	}
	if err := add("ndjson_truncated", "ndjson_truncated INTEGER"); err != nil { // ndjson 采集器截断行数
		return err
	}
	if err := add("source", "source TEXT"); err != nil { // P4 执行来源 worker:/peer:
		return err
	}
	if err := add("tags_json", "tags_json TEXT"); err != nil { // E5 job 标签（JSON 数组）
		return err
	}
	if err := add("stop_reason", "stop_reason TEXT"); err != nil { // ACP-01 acp-agent 的 stopReason
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
	// 监督分层升级路由（supervisor-routing P1.1）：origin_agent=发起该 job 的主 agent（owner，
	// L1 路由用），escalate_to=可选 job 级 escalate 覆盖。旧库 ALTER ADD，旧行 COALESCE→""。
	if err := add("origin_agent", "origin_agent TEXT"); err != nil { // 发起 owner agent_id
		return err
	}
	if err := add("escalate_to", "escalate_to TEXT"); err != nil { // job 级 escalate 覆盖
		return err
	}
	// 套娃防护（supervisor-routing P2.2）：role=该 job 的角色预设名（如 supervisor）。监督路由器
	// 据此识别"supervisor 自身产生的 interaction"，对其永不自动答/回投 sup（防死循环），留人（L3）。
	// 旧库 ALTER ADD，旧行 COALESCE→""。
	if err := add("role", "role TEXT"); err != nil { // job 角色预设（套娃判定）
		return err
	}
	// plan 编排：客户端可设的归组键，把独立 job 归到一个计划；区别于引擎私有 workflow_id。
	if err := add("plan_id", "plan_id TEXT"); err != nil {
		return err
	}
	// plan todo 联动（SUP-01 C）：todo_id=该 job 挂接的 checklist 项（空=不挂）；终态由 hub
	// 把结果写回 todo。base_sha / commits_json=提交采集（执行机开跑时的 HEAD 与终态 base..HEAD
	// 的提交列表）。旧库 ALTER ADD，旧行 COALESCE→""，读作"未挂 todo / 未采集"，不会把历史
	// job 伪造成有提交。
	if err := add("todo_id", "todo_id TEXT"); err != nil {
		return err
	}
	if err := add("base_sha", "base_sha TEXT"); err != nil {
		return err
	}
	if err := add("commits_json", "commits_json TEXT"); err != nil {
		return err
	}
	// verify 步骤（SUP-01 P2）：该 job 的验证步骤结果（argv/status/exit/duration 的 JSON），
	// 空=没有验证步骤。旧库 ALTER ADD，旧行 COALESCE→""，读作"未跑验证"，不会把历史 job
	// 伪造成已验证或验证失败。
	if err := add("verify_json", "verify_json TEXT"); err != nil {
		return err
	}
	// plan 编排 P5：血缘键——resume/rebuild 出的 job 指回源 job（服务端盖章 source_job_id=源 id）。
	// 旧库 ALTER ADD，旧行 COALESCE→""。区别引擎私有 workflow_id；区别 source 列（执行位置）。
	if err := add("source_job_id", "source_job_id TEXT"); err != nil {
		return err
	}
	// agent 故障转移（SUP-01 P3）：failure_class=失败归类（transient|other|""，健康度按它聚合）；
	// fell_back_from/fell_back_to=转移链的两端（源行指新 job，新 job 指回源）；requested_agent=
	// 调用方原本要求的 agent（提交期改派/转移后仍可追溯）；fallback_json=提交时解析并冻结的候选
	// 列表 + 已用深度（避免运行中改配置导致链条漂移）。旧库 ALTER ADD，旧行 COALESCE→""，
	// 读作"未分类 / 不在这条链上"，不会把历史 job 伪造成转移过的 job。
	if err := add("failure_class", "failure_class TEXT"); err != nil {
		return err
	}
	if err := add("fell_back_from", "fell_back_from TEXT"); err != nil {
		return err
	}
	if err := add("fell_back_to", "fell_back_to TEXT"); err != nil {
		return err
	}
	if err := add("requested_agent", "requested_agent TEXT"); err != nil {
		return err
	}
	if err := add("fallback_json", "fallback_json TEXT"); err != nil {
		return err
	}
	// 用量/成本记录（SUP-01 E）：usage_json=该 job 的 token/成本结算（job.Usage 的 JSON），
	// 空=未采集到（采集失败/agent 没报），读作"没有用量"，不会把历史 job 伪造成 0 用量。
	// /v1/stats 的 24h/7d 聚合直接对它做 json_extract。
	if err := add("usage_json", "usage_json TEXT"); err != nil {
		return err
	}
	// 文件传输摘要（XFER-01 X2）：xfer_json=该 job 的 upload/collect 摘要
	// （job.XferSummary 的 JSON），空=这个 job 没带文件（读作"没有传输"），不会把历史 job
	// 伪造成一份空摘要。收集到的文件本体在 <result_dir>/artifacts/collected/ 下。
	if err := add("xfer_json", "xfer_json TEXT"); err != nil {
		return err
	}
	// 技能绑定（JOB-10，设计 §一.5）：skills_json=该 job 绑定的技能清单（JSON），空=这个
	// job 没带技能（读作"没有绑定"），不会把历史 job 伪造成挂载过技能。旧库 ALTER ADD，旧行
	// COALESCE→""。技能本体不在库里，它在执行机的 <result_dir>/skills/<name>/ 下（随 job 的
	// result_dir 一起过期）。
	if err := add("skills_json", "skills_json TEXT"); err != nil {
		return err
	}
	if err := add("resumed_from", "resumed_from TEXT"); err != nil {
		return err
	}
	if err := add("auto_resume_attempt", "auto_resume_attempt INTEGER DEFAULT 0"); err != nil {
		return err
	}
	if err := add("auto_resumed_by", "auto_resumed_by TEXT"); err != nil {
		return err
	}
	// job 超时上限可配（bd h-aii-s9ck）：timeout_sec=生效deadline、requested_timeout_sec=请求值、
	// timeout_clamped=请求是否被上限截断。旧库 ALTER ADD，旧行 COALESCE→0/false（旧 job 未记录，
	// 正好表示"未知"，不会伪造成"被截断"）。
	if err := add("timeout_sec", "timeout_sec INTEGER"); err != nil {
		return err
	}
	if err := add("requested_timeout_sec", "requested_timeout_sec INTEGER"); err != nil {
		return err
	}
	if err := add("timeout_clamped", "timeout_clamped INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// RECOV-01 worker 断线恢复：recovering_since=job 进入 recovering 的时刻（unix 秒，0=不在
	// recovering）。旧库 ALTER ADD，旧行 COALESCE→0，正好读作"从未 recovering"（RECOV-01 之前
	// 的语义），不会把老 job 伪造成恢复中的 job。
	// RECOV-01 R4 worker 身份持久化：worker_instance_id=dispatch 时该 worker 连接的 process
	// nonce（wsproto.Register.InstanceID），让 serve 重启后新 hub 能把 store 里 recovering 的
	// job 与重连进程对上（instance_id 一致才收养）。旧库 ALTER ADD，旧行 COALESCE→""，读作
	// "无 instance 记录"——永远无法被收养，只能按 worker_lost 结束（安全侧）。
	if err := add("worker_instance_id", "worker_instance_id TEXT"); err != nil {
		return err
	}
	if err := add("recovering_since", "recovering_since INTEGER"); err != nil {
		return err
	}
	// WT-01 受管 worktree：job 在独立 git worktree 里执行时，记录交付物位置（path/branch）、
	// 基线 sha、终态 HEAD sha 与领先提交数。旧库 ALTER ADD，旧行 COALESCE 成 ""/0，正好读作
	// "该 job 没有 worktree"（WT-01 之前的语义），不会把老 job 伪造成有 worktree。
	if err := add("worktree_path", "worktree_path TEXT"); err != nil {
		return err
	}
	if err := add("worktree_branch", "worktree_branch TEXT"); err != nil {
		return err
	}
	if err := add("worktree_base_sha", "worktree_base_sha TEXT"); err != nil {
		return err
	}
	if err := add("worktree_head_sha", "worktree_head_sha TEXT"); err != nil {
		return err
	}
	if err := add("commits_ahead", "commits_ahead INTEGER"); err != nil {
		return err
	}
	// bd h-aii-0ql3 只读 job：read_only=该 job 是否在只读沙箱下运行（cli 走 read_only_args，
	// acp 走 session/set_mode）。旧库 ALTER ADD 默认 0，旧行读作"可写"——正是 S2 之前的语义，
	// 不会把历史 job 伪造成只读。resume 延续该值（同一 job 链内不可升级为可写）。
	if err := add("read_only", "read_only INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// GATE-01 S3 人工验收：require_review=该 job 是否要求人验收（--review / 项目
	// require_review），reviewed_by/at/note=验收决定与理由。旧库 ALTER ADD 全为
	// 0/空 = "未要求、未验收"——正是 S3 之前的语义，不会把历史 job 伪造成待验收。
	if err := add("require_review", "require_review INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := add("reviewed_by", "reviewed_by TEXT"); err != nil {
		return err
	}
	if err := add("reviewed_at", "reviewed_at INTEGER"); err != nil {
		return err
	}
	if err := add("review_note", "review_note TEXT"); err != nil {
		return err
	}
	// JOB-11 同 cwd 串行锁：dir_exclusive=该 job 提交期定下的独占决策。旧库 ALTER ADD 默认
	// 0 = "共享"——正是 JOB-11 之前的语义（谁都不取锁），不会把历史 job 伪造成独占过。
	// MCP-05 阶段 B: the server-set plan marker of a leader job. Additive/optional
	// (NULL = an ordinary job), so a pre-existing db reads every job as a member.
	if err := add("leader_of_plan", "leader_of_plan TEXT"); err != nil {
		return err
	}
	if err := add("dir_exclusive", "dir_exclusive INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.migrateWorkflows(); err != nil {
		return err
	}
	if err := s.migrateInteractions(); err != nil {
		return err
	}
	if err := s.migrateSchedules(); err != nil {
		return err
	}
	if err := s.migratePlanTodos(); err != nil {
		return err
	}
	if err := s.migratePlans(); err != nil {
		return err
	}
	if err := s.migratePlanDecisions(); err != nil {
		return err
	}
	if err := s.migrateAgentSessions(); err != nil {
		return err
	}
	if err := s.migrateDeliveries(); err != nil {
		return err
	}
	// Partial unique index: only non-empty request_id values are constrained, so
	// jobs without a request_id never collide. Created after the column exists.
	if _, err := s.db.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_request_id ON jobs(request_id) WHERE request_id <> ''`,
	); err != nil {
		return fmt.Errorf("jobstore: migrate request_id index: %w", err)
	}
	// plan_id 归组过滤索引（list --plan）。旧库需等 ALTER ADD 后再建索引。
	if _, err := s.db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_jobs_plan_id ON jobs(plan_id)`,
	); err != nil {
		return fmt.Errorf("jobstore: migrate plan_id index: %w", err)
	}
	// source_job_id 反查索引（list ?source_job=）：列出某 job 直接派生出的所有 job。
	if _, err := s.db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_jobs_source_job_id ON jobs(source_job_id)`,
	); err != nil {
		return fmt.Errorf("jobstore: migrate source_job_id index: %w", err)
	}
	// todo_id 反查索引（ListJobsByTodo / plan show 的 todo→jobs 挂接）。
	if _, err := s.db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_jobs_todo_id ON jobs(todo_id)`,
	); err != nil {
		return fmt.Errorf("jobstore: migrate todo_id index: %w", err)
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

// migrateInteractions adds columns introduced after the初始 interactions schema
// (additive only, idempotent — probe PRAGMA first). Two columns so far:
//   - escalated_at（监督分层升级路由 P1.1, design §9）承载 escalate dedup 标记 + owner 超时
//     计时；旧库经 migrate 自动补全，旧行 COALESCE→0。
//   - answered_by（监督分层升级路由 P3.2, design §10 审计区分）记录"谁应答"：auto:<policy>
//     (L0 内置规则器) / agent:<id> (L1 owner / L2 sup) / human (L3 web/CLI)；旧行 COALESCE→""。
//   - needs_human（事件驱动按需派发 y5wt）：通用 sup 对高危/拿不准的 interaction 拒答时置 1，
//     标记"留给人处理"，把它排除出 CountSupPendingDemand 的 sup demand → 不再重复唤醒 sup。
//     旧行 COALESCE→0。
//   - tool_call_json / policy_hint（GATE-01 §1）：kind=permission 审批交互的工具调用详情
//     （toolCallId/title/kind/locations/rawInput 摘要）与策略提示（"ask: kind=edit"）。旧库
//     经 migrate 自动补全，旧行（普通交互）COALESCE→空。
func (s *Store) migrateInteractions() error {
	cols, err := s.tableColumns("interactions")
	if err != nil {
		return err
	}
	add := func(col, ddl string) error {
		if _, ok := cols[col]; ok {
			return nil
		}
		if _, e := s.db.Exec("ALTER TABLE interactions ADD COLUMN " + ddl); e != nil {
			return fmt.Errorf("jobstore: migrate interactions add %s: %w", col, e)
		}
		return nil
	}
	if err := add("escalated_at", "escalated_at INTEGER"); err != nil {
		return err
	}
	if err := add("answered_by", "answered_by TEXT"); err != nil {
		return err
	}
	if err := add("needs_human", "needs_human INTEGER"); err != nil {
		return err
	}
	if err := add("tool_call_json", "tool_call_json TEXT"); err != nil {
		return err
	}
	if err := add("policy_hint", "policy_hint TEXT"); err != nil {
		return err
	}
	return add("expires_at", "expires_at INTEGER")
}

// migrateSchedules adds post-AUTO-02 columns to the schedules table. All changes
// are additive and idempotent: old rows keep cron semantics through DEFAULT.
func (s *Store) migrateSchedules() error {
	cols, err := s.tableColumns("schedules")
	if err != nil {
		return err
	}
	add := func(col, ddl string) error {
		if _, ok := cols[col]; ok {
			return nil
		}
		if _, e := s.db.Exec("ALTER TABLE schedules ADD COLUMN " + ddl); e != nil {
			return fmt.Errorf("jobstore: migrate schedules add %s: %w", col, e)
		}
		return nil
	}
	if err := add("schedule_type", "schedule_type TEXT NOT NULL DEFAULT 'cron'"); err != nil {
		return err
	}
	// AUTO-02b: an old schedule reads back with no token, i.e. its webhook endpoint is
	// off until someone enables it (`schedule create --webhook` / rotate-token).
	return add("trigger_token", "trigger_token TEXT")
}

// migratePlanTodos adds the todo lifecycle columns (Part C §C2): status /
// started_at / done_at / note. Additive + idempotent; when the status column is
// first added, existing rows are backfilled from the legacy done flag so old
// checklists render correctly under the richer lifecycle.
//
// PLAN-02 P2 extends it with the DISPATCH columns (assignee … dispatch_error): a todo
// that predates them reads back as "nobody assigned, nothing to dispatch", which is
// exactly the pre-P2 semantics. `ready` needs no migration — a status value.
func (s *Store) migratePlanTodos() error {
	cols, err := s.tableColumns("plan_todos")
	if err != nil {
		return err
	}
	backfill := false
	add := func(col, ddl string) error {
		if _, ok := cols[col]; ok {
			return nil
		}
		if _, e := s.db.Exec("ALTER TABLE plan_todos ADD COLUMN " + ddl); e != nil {
			return fmt.Errorf("jobstore: migrate plan_todos add %s: %w", col, e)
		}
		return nil
	}
	if _, ok := cols["status"]; !ok {
		backfill = true
	}
	if err := add("status", "status TEXT"); err != nil {
		return err
	}
	if err := add("started_at", "started_at INTEGER"); err != nil {
		return err
	}
	if err := add("done_at", "done_at INTEGER"); err != nil {
		return err
	}
	if err := add("note", "note TEXT"); err != nil {
		return err
	}
	// PLAN-02 P2: the dispatch request a todo carries.
	if err := add("assignee", "assignee TEXT"); err != nil {
		return err
	}
	if err := add("project_key", "project_key TEXT"); err != nil {
		return err
	}
	if err := add("template", "template TEXT"); err != nil {
		return err
	}
	if err := add("vars_json", "vars_json TEXT"); err != nil {
		return err
	}
	if err := add("verify_json", "verify_json TEXT"); err != nil {
		return err
	}
	if err := add("review", "review INTEGER"); err != nil {
		return err
	}
	if err := add("runner", "runner TEXT"); err != nil {
		return err
	}
	if err := add("cwd", "cwd TEXT"); err != nil {
		return err
	}
	if err := add("timeout_sec", "timeout_sec INTEGER"); err != nil {
		return err
	}
	if err := add("dispatch_error", "dispatch_error TEXT"); err != nil {
		return err
	}
	// PLAN-03: the chain columns — the dependencies this item waits for, the
	// auto-advance switch and an exec item's argv. An old row reads back as "no
	// dependencies, auto on, no argv", i.e. a plain standalone item.
	if err := add("after_json", "after_json TEXT"); err != nil {
		return err
	}
	if err := add("auto", "auto INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	if err := add("cmd_json", "cmd_json TEXT"); err != nil {
		return err
	}
	if backfill {
		if _, err := s.db.Exec(
			`UPDATE plan_todos SET status = CASE WHEN done=1 THEN 'done' ELSE 'pending' END
			 WHERE status IS NULL OR status = ''`); err != nil {
			return fmt.Errorf("jobstore: backfill plan_todos.status: %w", err)
		}
	}
	return nil
}

// migratePlans adds the PLAN-02 project_key a plan's todos are dispatched into.
// Additive + idempotent: an old plan reads back as "" (no project), so its todos need
// their own `--project` — the pre-P2 state, not a fabricated target.
func (s *Store) migratePlans() error {
	cols, err := s.tableColumns("plans")
	if err != nil {
		return err
	}
	add := func(col, ddl string) error {
		if _, ok := cols[col]; ok {
			return nil
		}
		if _, e := s.db.Exec("ALTER TABLE plans ADD COLUMN " + ddl); e != nil {
			return fmt.Errorf("jobstore: migrate plans add %s: %w", col, e)
		}
		return nil
	}
	if err := add("project_key", "project_key TEXT"); err != nil {
		return err
	}
	// PLAN-03: paused (chain held by a human) and blocked_todo (the item a failed job
	// parked the chain on). An old row reads as "running, not blocked" — exactly the
	// pre-PLAN-03 semantics, and `blocked` needs no migration (a status value).
	if err := add("paused", "paused INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := add("blocked_todo", "blocked_todo TEXT"); err != nil {
		return err
	}
	return nil
}

// migratePlanDecisions adds the session-relay columns (SESS-01 D3) to
// plan_decisions: session_id (owning agent session) and kind ('relay' for a
// relay turn; NULL for a plain gofer_ask_human decision), plus released_by
// (turn closed without an answer; see migrateAgentSessions' sibling column) and
// detail (the JSON audit blob of a non-turn delivery, e.g. path A's tmux
// injection: {"path":"tmux","job_id":"…"}).
// Old rows read back as "" via COALESCE. The per-session index is created after
// the column exists.
func (s *Store) migratePlanDecisions() error {
	cols, err := s.tableColumns("plan_decisions")
	if err != nil {
		return err
	}
	add := func(col, ddl string) error {
		if _, ok := cols[col]; ok {
			return nil
		}
		if _, e := s.db.Exec("ALTER TABLE plan_decisions ADD COLUMN " + ddl); e != nil {
			return fmt.Errorf("jobstore: migrate plan_decisions add %s: %w", col, e)
		}
		return nil
	}
	if err := add("session_id", "session_id TEXT"); err != nil {
		return err
	}
	if err := add("kind", "kind TEXT"); err != nil {
		return err
	}
	// released_by records a turn closed WITHOUT an answer (SR-A5: the hook saw
	// the human return and the server released the wait). Old rows read "".
	if err := add("released_by", "released_by TEXT"); err != nil {
		return err
	}
	// detail carries the machine-readable audit of a delivery that was NOT a turn
	// (path A's tmux injection: path + the internal job that typed the text).
	if err := add("detail", "detail TEXT"); err != nil {
		return err
	}
	if _, err := s.db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_plan_decisions_session ON plan_decisions(session_id, asked_at)`,
	); err != nil {
		return fmt.Errorf("jobstore: migrate plan_decisions session index: %w", err)
	}
	return nil
}

// migrateAgentSessions brings agent_sessions up to the current shape for dbs
// written by older binaries. Every step is independent and idempotent:
//
//   - idle_sec (SR-A5): the system input idle reading the hook reported last
//     (-1 = never reported; COALESCE covers pre-column rows, so an old session
//     reads as "no evidence the human is away").
//   - relay_mode + last_human_at (R1/R2): the three-state switch and the
//     human-input clock. The migration maps the old boolean: relay=1 → `on`
//     (the human had flipped it), relay=0 → `auto` (nobody did — but the idle
//     rules may now arm it, which is exactly the new default).
//   - handed_off_job_id + handed_off_at (P2-2, §9.1 B): which pty job took the
//     session over (and when); both NULL on a session that was never handed off.
func (s *Store) migrateAgentSessions() error {
	cols, err := s.tableColumns("agent_sessions")
	if err != nil {
		return err
	}
	if _, ok := cols["idle_sec"]; !ok {
		if _, e := s.db.Exec("ALTER TABLE agent_sessions ADD COLUMN idle_sec INTEGER"); e != nil {
			return fmt.Errorf("jobstore: migrate agent_sessions add idle_sec: %w", e)
		}
	}
	if _, ok := cols["relay_mode"]; !ok {
		if _, e := s.db.Exec("ALTER TABLE agent_sessions ADD COLUMN relay_mode TEXT NOT NULL DEFAULT 'auto'"); e != nil {
			return fmt.Errorf("jobstore: migrate agent_sessions add relay_mode: %w", e)
		}
		if _, e := s.db.Exec(`UPDATE agent_sessions
  SET relay_mode = CASE WHEN relay = 1 THEN 'on' ELSE 'auto' END`); e != nil {
			return fmt.Errorf("jobstore: migrate agent_sessions backfill relay_mode: %w", e)
		}
	}
	if _, ok := cols["last_human_at"]; !ok {
		if _, e := s.db.Exec("ALTER TABLE agent_sessions ADD COLUMN last_human_at INTEGER NOT NULL DEFAULT 0"); e != nil {
			return fmt.Errorf("jobstore: migrate agent_sessions add last_human_at: %w", e)
		}
	}
	// handed_off_job_id / handed_off_at (design §9.1 B): the pty job that took the
	// session over and when. Pre-column rows read back as "" / 0 (COALESCE in the
	// select), i.e. "never taken over", which is what they are.
	if _, ok := cols["handed_off_job_id"]; !ok {
		if _, e := s.db.Exec("ALTER TABLE agent_sessions ADD COLUMN handed_off_job_id TEXT"); e != nil {
			return fmt.Errorf("jobstore: migrate agent_sessions add handed_off_job_id: %w", e)
		}
	}
	if _, ok := cols["handed_off_at"]; !ok {
		if _, e := s.db.Exec("ALTER TABLE agent_sessions ADD COLUMN handed_off_at INTEGER"); e != nil {
			return fmt.Errorf("jobstore: migrate agent_sessions add handed_off_at: %w", e)
		}
	}
	// caller_id (SUP-01 D / bd h-aii-esus): the authenticated caller that
	// registered the session — its owner. A pre-column row reads back as ""
	// (COALESCE in the select), which the owner check deliberately treats as
	// "nobody to compare against" rather than locking the human out of a session
	// they registered before the column existed.
	if _, ok := cols["caller_id"]; !ok {
		if _, e := s.db.Exec("ALTER TABLE agent_sessions ADD COLUMN caller_id TEXT"); e != nil {
			return fmt.Errorf("jobstore: migrate agent_sessions add caller_id: %w", e)
		}
	}
	return nil
}

// migrateDeliveries adds the pre-rendered delivery columns (OBS-07a) to
// event_deliveries: body (the exact payload to POST) and event_type (the header
// / audit label that would otherwise come from the events row). Old rows read
// back as "" via COALESCE and keep the rebuild-from-event behaviour.
func (s *Store) migrateDeliveries() error {
	cols, err := s.tableColumns("event_deliveries")
	if err != nil {
		return err
	}
	add := func(col, ddl string) error {
		if _, ok := cols[col]; ok {
			return nil
		}
		if _, e := s.db.Exec("ALTER TABLE event_deliveries ADD COLUMN " + ddl); e != nil {
			return fmt.Errorf("jobstore: migrate event_deliveries add %s: %w", col, e)
		}
		return nil
	}
	if err := add("body", "body TEXT"); err != nil {
		return err
	}
	return add("event_type", "event_type TEXT")
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
