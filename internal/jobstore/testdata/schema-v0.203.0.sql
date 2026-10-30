-- Schema of a database created by gofer v0.203.0 (sqlite_master, schema only).
-- TestOpenUpgradesPreviousReleaseSchema opens a database built from it with the
-- current code: a migration that only works on a fresh database fails there.
CREATE TABLE agent_presence (
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
);
CREATE TABLE agent_sessions (
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
  handed_off_at     INTEGER,
  peer_name         TEXT,
  peer_status       TEXT,
  peer_messaging    INTEGER NOT NULL DEFAULT 0,
  progress_text     TEXT,
  last_cwd          TEXT,
  usage_json        TEXT,
  progress_at       INTEGER NOT NULL DEFAULT 0,
  usage_at          INTEGER NOT NULL DEFAULT 0,
  -- gofer-eh53: the last report refused because it came from a caller that is not
  -- the owner (additive; pre-column rows read back as '' / 0 via COALESCE).
  refused_caller_id TEXT,
  refused_at        INTEGER NOT NULL DEFAULT 0,
  -- 会话化身（权限设计 §2/§4，plan T1.3）：会话每次新建或从 ended 复活时换新，会话授权靠它
  -- 判定「这条授权还是不是当初那个会话」。additive，旧库经 migrateAgentSessions 补列并回填；
  -- 值本身不透明，只做相等比较。
  incarnation       TEXT NOT NULL DEFAULT '',
  -- 会话主题（第一期收集箱设计「会话主题」）：topic 一句话、scope_json 项目根相对子路径的 JSON
  -- 数组、topic_source 为 self|human、topic_at 为最近一次设置时间。推断值不落库。additive，
  -- 旧库经 migrateAgentSessions 补列，旧行读回 ''/0。
  topic             TEXT NOT NULL DEFAULT '',
  scope_json        TEXT NOT NULL DEFAULT '',
  topic_source      TEXT NOT NULL DEFAULT '',
  topic_at          INTEGER NOT NULL DEFAULT 0,
  -- human_prompts（降噪设计 2026-10-20 §总体方案 1）：这个会话里人一共提问了几次。草稿工作项
  -- 等到第 work.draft.min_prompts 次才建，短命会话的判据也看它。additive，旧库经
  -- migrateAgentSessions 补列，旧行读回 0（= 没记过，按「还没有提问」算）。
  human_prompts     INTEGER NOT NULL DEFAULT 0,
  -- stop_park_until (gofer-fv6a) is the unix second until which the session's Stop
  -- hook is PARKED waiting for its watched jobs (the hook says so on a heartbeat).
  -- While it is in the future a job-end notice must not be taken away from the
  -- parked hook and handed to the messenger: the hook is the channel that reaches
  -- the agent NOW. 0 = no park announced. additive, old rows read back 0.
  stop_park_until   INTEGER NOT NULL DEFAULT 0,
  -- mail_paused parks delivery to this session without removing letters from its FIFO queue.
  mail_paused       INTEGER NOT NULL DEFAULT 0
, peer_name_source TEXT, inject_mode TEXT NOT NULL DEFAULT 'auto');
CREATE TABLE attachments (
  id         TEXT PRIMARY KEY,
  caller     TEXT NOT NULL DEFAULT '',
  thread_id  TEXT NOT NULL DEFAULT '',
  ref_kind   TEXT NOT NULL DEFAULT '',
  ref_id     TEXT NOT NULL DEFAULT '',
  mime       TEXT NOT NULL,
  bytes      INTEGER NOT NULL,
  sha256     TEXT NOT NULL DEFAULT '',
  width      INTEGER NOT NULL DEFAULT 0,
  height     INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL DEFAULT 0,
  path       TEXT NOT NULL
);
CREATE TABLE audit_events (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  kind        TEXT NOT NULL,
  target_id   TEXT NOT NULL,
  actor       TEXT NOT NULL DEFAULT '',
  at          INTEGER NOT NULL,
  detail_json TEXT NOT NULL DEFAULT ''
);
CREATE TABLE auth_audit (
  at         INTEGER NOT NULL,
  actor      TEXT NOT NULL DEFAULT '',
  action     TEXT NOT NULL,
  subject    TEXT NOT NULL DEFAULT '',
  permission TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL DEFAULT '',
  grant_id   TEXT NOT NULL DEFAULT '',
  method     TEXT NOT NULL DEFAULT '',
  path       TEXT NOT NULL DEFAULT '',
  status     INTEGER NOT NULL DEFAULT 0,
  reason     TEXT NOT NULL DEFAULT ''
);
CREATE TABLE auth_grants (
  id                  TEXT PRIMARY KEY,
  subject             TEXT NOT NULL,
  pattern             TEXT NOT NULL,
  scope               TEXT NOT NULL DEFAULT '*',
  expires_at          INTEGER,
  session_owner       TEXT,
  session_incarnation TEXT,
  granted_by          TEXT NOT NULL DEFAULT '',
  granted_at          INTEGER NOT NULL,
  revoked_at          INTEGER,
  revoked_by          TEXT,
  orphaned_at         INTEGER,
  expand_snapshot     TEXT,
  note                TEXT,
  last_used_at        INTEGER,
  use_count           INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE auth_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT ''
);
CREATE TABLE comments (
  id               TEXT PRIMARY KEY,
  scope            TEXT NOT NULL,
  scope_id         TEXT NOT NULL,
  author           TEXT NOT NULL,
  author_kind      TEXT NOT NULL,
  body             TEXT NOT NULL,
  mentions_json    TEXT,
  created_at       INTEGER NOT NULL,
  triggered_job_id TEXT
);
CREATE TABLE decision_advice (
  card_key  TEXT PRIMARY KEY,
  text      TEXT NOT NULL DEFAULT '',
  action_id TEXT NOT NULL DEFAULT '',
  digest    TEXT NOT NULL DEFAULT '',
  by        TEXT NOT NULL DEFAULT '',
  at        INTEGER NOT NULL,
  job_id    TEXT NOT NULL DEFAULT ''
);
CREATE TABLE event_deliveries (
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
);
CREATE TABLE interactions (
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
);
CREATE TABLE job_events (
  seq         INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id      TEXT    NOT NULL,
  type        TEXT    NOT NULL,
  detail_json TEXT,
  at          INTEGER NOT NULL
);
CREATE TABLE job_followups (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id         TEXT NOT NULL,
  prompt         TEXT NOT NULL,
  created_at     INTEGER NOT NULL,
  created_by     TEXT,
  review_each    INTEGER NOT NULL DEFAULT 0,
  status         TEXT NOT NULL DEFAULT 'pending',
  claimed_at     INTEGER,
  continuation_id TEXT
);
CREATE TABLE job_metrics (
  job_id            TEXT PRIMARY KEY,
  model             TEXT,
  turns             INTEGER,
  tool_calls        INTEGER,
  human_count       INTEGER,
  human_wait_sec    INTEGER,
  active_sec        INTEGER,
  commits           INTEGER,
  files_changed     INTEGER,
  insertions        INTEGER,
  deletions         INTEGER,
  input_tokens      INTEGER,
  output_tokens     INTEGER,
  cache_read_tokens INTEGER,
  cost_usd          REAL,
  computed_at       INTEGER NOT NULL,
  version           INTEGER NOT NULL
);
CREATE TABLE job_retries (
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
);
CREATE TABLE job_tokens (
  job_id     TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL,
  kind       TEXT NOT NULL,
  plan_id    TEXT NOT NULL DEFAULT '',
  expires_at INTEGER NOT NULL,
  revoked_at INTEGER,
  created_at INTEGER NOT NULL
);
CREATE TABLE job_wakeups (
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
);
CREATE TABLE jobs (
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
  queued_at    INTEGER,
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
  source_session_id TEXT,
  stop_reason      TEXT,
  wrap_up          TEXT,
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
  worktree_links_json TEXT,
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
  rules_json       TEXT,
  dir_exclusive    INTEGER NOT NULL DEFAULT 0,
  leader_of_plan   TEXT,
  uncommitted_files_json TEXT,
  uncommitted_count INTEGER NOT NULL DEFAULT 0,
  resume_agent TEXT NOT NULL DEFAULT '',
  session_state_json TEXT NOT NULL DEFAULT '',
  hold_expires_at  INTEGER,
  hold_json        TEXT,
  mcp_json         TEXT,
  delivery_json    TEXT
, caller_id TEXT, origin_caller TEXT, request_id TEXT, attempt INTEGER, fan_index INTEGER, resumed_from TEXT, auto_resume_attempt INTEGER DEFAULT 0, auto_resumed_by TEXT, timeout_limit_source TEXT);
CREATE TABLE mail (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id       TEXT NOT NULL,
  reply_to        INTEGER,
  from_kind       TEXT NOT NULL,
  from_id         TEXT NOT NULL DEFAULT '',
  from_caller     TEXT NOT NULL DEFAULT '',
  to_address      TEXT NOT NULL DEFAULT '',
  to_kind         TEXT NOT NULL DEFAULT '',
  to_id           TEXT NOT NULL DEFAULT '',
  to_caller       TEXT NOT NULL DEFAULT '',
  job_id          TEXT NOT NULL DEFAULT '',
  urgent          INTEGER NOT NULL DEFAULT 0,
  body            TEXT NOT NULL DEFAULT '',
  state           TEXT NOT NULL,
  claimed_by      TEXT NOT NULL DEFAULT '',
  claim_until     INTEGER NOT NULL DEFAULT 0,
  attempts        INTEGER NOT NULL DEFAULT 0,
  last_error      TEXT NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL,
  delivered_at    INTEGER,
  read_at         INTEGER,
  deliver_by      INTEGER NOT NULL DEFAULT 0,
  capture_item_id TEXT NOT NULL DEFAULT '',
  broadcast_id    INTEGER NOT NULL DEFAULT 0,
  ack_kind        TEXT NOT NULL DEFAULT '',
  ack_by          TEXT NOT NULL DEFAULT '',
  ack_at          INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE mail_resident_agents (
  name       TEXT NOT NULL,
  project    TEXT NOT NULL DEFAULT '',
  job_id     TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL DEFAULT '',
  letters    INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  ended_at   INTEGER NOT NULL DEFAULT 0,
  status     TEXT NOT NULL,
  last_error TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (name, project)
);
CREATE TABLE memory_candidates (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id      TEXT NOT NULL,
  project_key TEXT NOT NULL DEFAULT '',
  text        TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'pending',
  memory_key  TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  decided_at  INTEGER NOT NULL DEFAULT 0,
  decided_by  TEXT NOT NULL DEFAULT '',
  when_paths  TEXT NOT NULL DEFAULT '',
  UNIQUE(job_id, text)
);
CREATE TABLE memory_suggestions (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  tracker_id   TEXT NOT NULL,
  memory_key   TEXT NOT NULL,
  action       TEXT NOT NULL,
  payload_json TEXT NOT NULL DEFAULT '{}',
  reason       TEXT NOT NULL DEFAULT '',
  state        TEXT NOT NULL DEFAULT 'pending',
  by           TEXT NOT NULL DEFAULT '',
  job_id       TEXT NOT NULL DEFAULT '',
  base_rev     INTEGER NOT NULL DEFAULT 0,
  target_rev   INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  decided_at   INTEGER NOT NULL DEFAULT 0,
  decided_by   TEXT NOT NULL DEFAULT '',
  note         TEXT NOT NULL DEFAULT ''
);
CREATE TABLE messages (
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
);
CREATE TABLE plan_decisions (
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
, session_id TEXT, kind TEXT, acked_at INTEGER, acked_by TEXT);
CREATE TABLE plan_handoffs (
  plan_id  TEXT NOT NULL,
  version  INTEGER NOT NULL,
  body     TEXT NOT NULL,
  by       TEXT NOT NULL,
  at       INTEGER NOT NULL,
  PRIMARY KEY (plan_id, version)
);
CREATE TABLE plan_leader_wakes (
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
);
CREATE TABLE plan_session_usage (
  plan_id    TEXT NOT NULL,
  session_id TEXT NOT NULL,
  usage_json TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (plan_id, session_id)
);
CREATE TABLE plan_todos (
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
  -- N1 §B: the model the dispatched job asks for ('' / NULL = the agent's default).
  model          TEXT,
  -- N2 §B: the spend ceiling of the dispatched job (JSON config.Budget; NULL = unlimited).
  budget_json    TEXT,
  -- acceptance criteria the dispatched job's prompt and review panel carry.
  acceptance     TEXT,
  -- declared change scope (JSON []string of path globs) of the dispatched job.
  scope_json     TEXT,
  -- task keywords (JSON []string) of the item's job: the tracker-prime material the
  -- dispatched job receives is selected and ranked by them (gofer-11tz).
  prime_keywords_json TEXT,
  -- 接着上一项的会话做: continue_from = the todo whose agent session this item continues
  -- (NULL = an ordinary item with its own session, and dispatch reads a value whose todo
  -- was deleted as "nothing to continue from"); continued_by = the successor that took
  -- this item's session (the continuation slot, claimed by ONE conditional UPDATE, so a
  -- predecessor session is continued at most once); continue_note = why the last dispatch
  -- did NOT continue (diagnostic only). Added by migratePlanTodos on a pre-existing db.
  continue_from  TEXT,
  continued_by   TEXT,
  continue_note  TEXT,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
, status TEXT, started_at INTEGER, done_at INTEGER, note TEXT, read_dirs_json TEXT, author_caller TEXT);
CREATE TABLE plans (
  plan_id      TEXT PRIMARY KEY,
  title        TEXT,
  description  TEXT,
  status       TEXT NOT NULL,
  owner        TEXT,
  progress     INTEGER NOT NULL DEFAULT 0,
  project_key  TEXT,
  supervisor_session_id TEXT,
  -- PLAN-03: paused stops the automatic chain advance (the plan holds where it is);
  -- blocked_todo names the item a FAILED job parked the chain on (status='blocked'
  -- while set). Both are added by migratePlans on a pre-existing db.
  paused       INTEGER NOT NULL DEFAULT 0,
  blocked_todo TEXT,
  -- LEAD-02: the per-plan leader-round switch ('off' by default, so an existing plan
  -- is never woken by an upgrade). Added by migratePlans on a pre-existing db.
  leader       TEXT NOT NULL DEFAULT 'off',
  -- gofer-3db4: the per-plan end-notification switch ('off' by default, so an upgrade
  -- never starts spending a messenger process per finished job). Added by
  -- migratePlans on a pre-existing db.
  notify       TEXT NOT NULL DEFAULT 'off',
  tags_json    TEXT NOT NULL DEFAULT '[]',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);
CREATE TABLE pty_sessions (
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
);
CREATE TABLE push_subscriptions (
  caller_id  TEXT NOT NULL,
  endpoint   TEXT PRIMARY KEY,
  p256dh     TEXT NOT NULL,
  auth       TEXT NOT NULL,
  user_agent TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  last_ok_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE rules (
  name        TEXT PRIMARY KEY,
  description TEXT,
  size        INTEGER NOT NULL DEFAULT 0,
  sha256      TEXT,
  updated_at  INTEGER NOT NULL DEFAULT 0,
  updated_by  TEXT
);
CREATE TABLE schedules (
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
  -- Scheduled job parameters (design 2026-10-22 §D1): the JSON array of declared
  -- {name,type,default,desc,choices} params, overlaid on the stored request env at
  -- submit. Empty = no params (every row written before the column).
  params_json  TEXT,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL, creator_caller TEXT,
  PRIMARY KEY (id)
);
CREATE TABLE scoped_memories (
  scope      TEXT NOT NULL,
  scope_key  TEXT NOT NULL DEFAULT '',
  key        TEXT NOT NULL,
  content    TEXT NOT NULL,
  tags_json  TEXT NOT NULL DEFAULT '[]',
  updated_at TEXT NOT NULL,
  updated_by TEXT NOT NULL DEFAULT '',
  deleted    INTEGER NOT NULL DEFAULT 0, meta_json TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (scope, scope_key, key)
);
CREATE TABLE session_job_watches (
  session_id TEXT NOT NULL,
  job_id     TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (session_id, job_id)
);
CREATE TABLE session_nudges (
  id           TEXT PRIMARY KEY,
  session_id   TEXT NOT NULL,
  kind         TEXT NOT NULL,
  interval_sec INTEGER NOT NULL,
  text         TEXT NOT NULL,
  until_at     INTEGER NOT NULL DEFAULT 0,
  state        TEXT NOT NULL DEFAULT 'active',
  pause_reason TEXT NOT NULL DEFAULT '',
  ended_reason TEXT NOT NULL DEFAULT '',
  created_by   TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  next_run_at  INTEGER NOT NULL DEFAULT 0,
  last_fired_at INTEGER NOT NULL DEFAULT 0,
  fire_count   INTEGER NOT NULL DEFAULT 0,
  fail_count   INTEGER NOT NULL DEFAULT 0,
  last_error   TEXT NOT NULL DEFAULT ''
);
CREATE TABLE session_usage_daily (
  day          TEXT NOT NULL,
  session_id   TEXT NOT NULL,
  model        TEXT NOT NULL,
  project_key  TEXT NOT NULL DEFAULT '',
  agent        TEXT NOT NULL DEFAULT '',
  input_tokens       INTEGER NOT NULL DEFAULT 0,
  output_tokens      INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
  cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  total_tokens       INTEGER NOT NULL DEFAULT 0,
  cost_usd           REAL NOT NULL DEFAULT 0,
  PRIMARY KEY (day, session_id, model)
);
CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_by TEXT,
  updated_at INTEGER
);
CREATE TABLE settings_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE skills (
  name        TEXT PRIMARY KEY,
  description TEXT,
  source      TEXT,
  source_ref  TEXT,
  version     TEXT,
  files_json  TEXT,
  size        INTEGER NOT NULL DEFAULT 0,
  updated_at  INTEGER NOT NULL DEFAULT 0,
  updated_by  TEXT
);
CREATE TABLE steward_events (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  kind       TEXT NOT NULL,
  ref        TEXT NOT NULL,
  detail     TEXT NOT NULL DEFAULT '',
  at         INTEGER NOT NULL,
  handled_at INTEGER NOT NULL DEFAULT 0,
  UNIQUE (kind, ref)
);
CREATE TABLE steward_jobs (
  job_id     TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE steward_reviews (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  day        TEXT NOT NULL,
  state      TEXT NOT NULL,
  cause      TEXT NOT NULL DEFAULT '',
  item_ids   TEXT NOT NULL DEFAULT '',
  summary    TEXT NOT NULL DEFAULT '',
  job_id     TEXT NOT NULL DEFAULT '',
  started_at INTEGER NOT NULL,
  ended_at   INTEGER NOT NULL DEFAULT 0,
  error      TEXT NOT NULL DEFAULT ''
);
CREATE TABLE summarizer_sessions (
  project_key  TEXT PRIMARY KEY,
  session_id   TEXT NOT NULL DEFAULT '',
  last_job_id  TEXT NOT NULL DEFAULT '',
  runs         INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL DEFAULT 0,
  updated_at   INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE today_snooze (
  card_key     TEXT PRIMARY KEY,
  until_at     INTEGER NOT NULL DEFAULT 0,
  until_job_id TEXT NOT NULL DEFAULT '',
  activity_at  INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  woke_at      INTEGER NOT NULL DEFAULT 0,
  woke_reason  TEXT NOT NULL DEFAULT ''
);
CREATE TABLE tracker_issues (
  tracker_id TEXT NOT NULL,
  issue_id TEXT NOT NULL,
  body_json TEXT NOT NULL,
  rev INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT NOT NULL,
  changed_seq INTEGER NOT NULL DEFAULT 0,
  server_created INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (tracker_id, issue_id)
);
CREATE TABLE tracker_memories (
  tracker_id TEXT NOT NULL,
  memory_key TEXT NOT NULL,
  body_json TEXT NOT NULL,
  rev INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT NOT NULL,
  deleted INTEGER NOT NULL DEFAULT 0,
  deleted_at TEXT NOT NULL DEFAULT '',
  deleted_by TEXT NOT NULL DEFAULT '',
  changed_seq INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (tracker_id, memory_key)
);
CREATE TABLE tracker_repos (
  tracker_id TEXT PRIMARY KEY,
  project_key TEXT NOT NULL DEFAULT '',
  rel_path TEXT NOT NULL DEFAULT '',
  prefix TEXT NOT NULL DEFAULT '',
  last_sync_at INTEGER NOT NULL DEFAULT 0,
  sync_summary TEXT NOT NULL DEFAULT ''
  ,next_seq INTEGER NOT NULL DEFAULT 0
  ,source_runner TEXT NOT NULL DEFAULT ''
);
CREATE TABLE tracker_sync_clients (
  tracker_id   TEXT NOT NULL,
  client_host  TEXT NOT NULL,
  caller       TEXT NOT NULL DEFAULT '',
  last_sync_at INTEGER NOT NULL DEFAULT 0,
  summary      TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (tracker_id, client_host, caller)
);
CREATE TABLE tunnel_presets (
  name       TEXT PRIMARY KEY,
  worker     TEXT NOT NULL,
  specs_json TEXT NOT NULL,
  note       TEXT,
  autostart  INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  updated_by TEXT
);
CREATE TABLE work_field_sources (
  work_item_id TEXT NOT NULL,
  field        TEXT NOT NULL,
  by           TEXT NOT NULL DEFAULT '',
  at           INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (work_item_id, field)
);
CREATE TABLE work_item_sessions (
  work_item_id TEXT NOT NULL,
  session_id   TEXT NOT NULL,
  role         TEXT NOT NULL DEFAULT 'current',
  attached_at  INTEGER NOT NULL,
  detached_at  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (work_item_id, session_id)
);
CREATE TABLE work_items (
  id               TEXT PRIMARY KEY,
  title            TEXT NOT NULL DEFAULT '',
  goal             TEXT NOT NULL DEFAULT '',
  status           TEXT NOT NULL DEFAULT 'active',
  status_source    TEXT NOT NULL DEFAULT 'auto',
  blocker_kind     TEXT NOT NULL DEFAULT '',
  blocker_text     TEXT NOT NULL DEFAULT '',
  next_step        TEXT NOT NULL DEFAULT '',
  summary          TEXT NOT NULL DEFAULT '',
  project_key      TEXT NOT NULL DEFAULT '',
  workspace        TEXT NOT NULL DEFAULT '',
  priority         INTEGER NOT NULL DEFAULT 0,
  park_until       INTEGER NOT NULL DEFAULT 0,
  park_note        TEXT NOT NULL DEFAULT '',
  remind_at        INTEGER NOT NULL DEFAULT 0,
  reminded_at      INTEGER NOT NULL DEFAULT 0,
  source           TEXT NOT NULL DEFAULT 'human',
  unsorted         INTEGER NOT NULL DEFAULT 0,
  merged_into      TEXT NOT NULL DEFAULT '',
  rev              INTEGER NOT NULL DEFAULT 1,
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  updated_by       TEXT NOT NULL DEFAULT '',
  closed_at        INTEGER NOT NULL DEFAULT 0,
  last_activity_at INTEGER NOT NULL DEFAULT 0,
  status_at        INTEGER NOT NULL DEFAULT 0
, kind TEXT NOT NULL DEFAULT '', capture_source_session TEXT NOT NULL DEFAULT '', capture_source_job TEXT NOT NULL DEFAULT '');
CREATE TABLE work_journal (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  work_item_id TEXT NOT NULL,
  kind         TEXT NOT NULL,
  text         TEXT NOT NULL DEFAULT '',
  by           TEXT NOT NULL DEFAULT '',
  at           INTEGER NOT NULL,
  origin_item  TEXT NOT NULL DEFAULT '',
  level        TEXT NOT NULL DEFAULT 'detail'
);
CREATE TABLE work_kv (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL DEFAULT ''
);
CREATE TABLE work_links (
  work_item_id TEXT NOT NULL,
  kind         TEXT NOT NULL,
  ref          TEXT NOT NULL,
  created_at   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (work_item_id, kind, ref)
);
CREATE TABLE work_merge_suggestions (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  target_id TEXT NOT NULL,
  source_id TEXT NOT NULL,
  reason    TEXT NOT NULL DEFAULT '',
  by        TEXT NOT NULL DEFAULT '',
  at        INTEGER NOT NULL,
  state     TEXT NOT NULL DEFAULT 'pending'
);
CREATE TABLE work_requests (
  id           TEXT PRIMARY KEY,
  work_item_id TEXT NOT NULL,
  session_id   TEXT NOT NULL DEFAULT '',
  kind         TEXT NOT NULL,
  state        TEXT NOT NULL,
  channel      TEXT NOT NULL DEFAULT '',
  text         TEXT NOT NULL DEFAULT '',
  by           TEXT NOT NULL DEFAULT '',
  error        TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  sent_at      INTEGER NOT NULL DEFAULT 0,
  answered_at  INTEGER NOT NULL DEFAULT 0,
  deadline     INTEGER NOT NULL DEFAULT 0,
  parent_id    TEXT NOT NULL DEFAULT ''
);
CREATE TABLE work_suggestions (
  work_item_id TEXT NOT NULL,
  field        TEXT NOT NULL,
  value        TEXT NOT NULL DEFAULT '',
  confidence   REAL NOT NULL DEFAULT 0,
  by           TEXT NOT NULL DEFAULT '',
  at           INTEGER NOT NULL DEFAULT 0,
  state        TEXT NOT NULL DEFAULT 'pending',
  job_id       TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (work_item_id, field)
);
CREATE TABLE work_summaries (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  work_item_id TEXT NOT NULL,
  session_id   TEXT NOT NULL DEFAULT '',
  at           INTEGER NOT NULL,
  activity_at  INTEGER NOT NULL DEFAULT 0,
  state        TEXT NOT NULL,
  job_id       TEXT NOT NULL DEFAULT '',
  cause        TEXT NOT NULL DEFAULT '',
  error        TEXT NOT NULL DEFAULT ''
);
CREATE TABLE workbench_layouts (
  caller_id  TEXT PRIMARY KEY,
  version    INTEGER NOT NULL,
  body_json  TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE workbench_thread_prefs (
  caller_id TEXT NOT NULL,
  thread_id TEXT NOT NULL,
  title     TEXT NOT NULL DEFAULT '',
  seen_at   INTEGER NOT NULL DEFAULT 0,
  pinned    INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (caller_id, thread_id)
);
CREATE TABLE worker_enrollments (
  id              TEXT PRIMARY KEY,
  state           TEXT NOT NULL,
  secret_hash     TEXT NOT NULL,
  pairing_code    TEXT NOT NULL,
  code_failures   INTEGER NOT NULL DEFAULT 0,
  secret_failures INTEGER NOT NULL DEFAULT 0,
  requested_id    TEXT NOT NULL DEFAULT '',
  worker_id       TEXT NOT NULL DEFAULT '',
  labels_json     TEXT NOT NULL DEFAULT '',
  projects_json   TEXT NOT NULL DEFAULT '',
  host_json       TEXT NOT NULL DEFAULT '',
  source          TEXT NOT NULL DEFAULT '',
  token_fp        TEXT NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL,
  decide_until    INTEGER NOT NULL,
  collect_until   INTEGER NOT NULL DEFAULT 0,
  decided_at      INTEGER NOT NULL DEFAULT 0,
  decided_by      TEXT NOT NULL DEFAULT '',
  reason          TEXT NOT NULL DEFAULT '',
  collected_at    INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE workflow_events (
  seq         INTEGER PRIMARY KEY AUTOINCREMENT,
  workflow_id TEXT    NOT NULL,
  type        TEXT    NOT NULL,
  detail_json TEXT,
  at          INTEGER NOT NULL
);
CREATE TABLE workflows (
  id           TEXT PRIMARY KEY,
  title        TEXT,
  status       TEXT NOT NULL,
  current_step INTEGER NOT NULL,
  total_steps  INTEGER NOT NULL,
  spec_json    TEXT NOT NULL,
  project_key  TEXT,
  caller_id    TEXT,
  error        TEXT,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
, step_attempt INTEGER, next_step_at INTEGER, parent_workflow_id TEXT, parent_step_index INTEGER, rerun_of TEXT);
CREATE TABLE xfers (
  id           TEXT PRIMARY KEY,
  op           TEXT NOT NULL,
  runner       TEXT NOT NULL,
  project_key  TEXT NOT NULL,
  path         TEXT NOT NULL,
  -- abs_path is the absolute destination the EXECUTING machine reported for a put (the
  -- same file "path" addresses, as that machine's own filesystem sees it). Empty = not
  -- reported: a get, a failed transfer, or a worker below protocol v25. Added by
  -- migrateXfers on a pre-existing db.
  abs_path     TEXT,
  -- target names the directory tree "path" is relative to on the EXECUTING machine:
  -- empty = the project, 'attachments' = that machine's own attachment directory
  -- (<config-dir>/run/attachments), where project_key is empty. Added by migrateXfers.
  target       TEXT,
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
);
CREATE INDEX idx_agent_sessions_project ON agent_sessions(project_key);
CREATE INDEX idx_agent_sessions_seen ON agent_sessions(state, last_seen_at);
CREATE INDEX idx_attachments_expires ON attachments(expires_at);
CREATE INDEX idx_attachments_thread ON attachments(thread_id);
CREATE INDEX idx_audit_events_kind ON audit_events(kind, at);
CREATE INDEX idx_audit_events_target ON audit_events(target_id, at);
CREATE INDEX idx_auth_grants_subject ON auth_grants(subject);
CREATE INDEX idx_comments_scope ON comments(scope, scope_id, created_at);
CREATE INDEX idx_deliveries_due ON event_deliveries(status, next_retry_at);
CREATE INDEX idx_deliveries_job ON event_deliveries(job_id, id);
CREATE INDEX idx_inter_job ON interactions(job_id);
CREATE INDEX idx_job_events_job ON job_events(job_id, seq);
CREATE INDEX idx_job_followups_pending ON job_followups(job_id, status, id);
CREATE INDEX idx_job_retries_due ON job_retries(state, next_run_at);
CREATE UNIQUE INDEX idx_job_tokens_hash ON job_tokens(token_hash);
CREATE INDEX idx_jobs_agent_started ON jobs(agent, started_at);
CREATE INDEX idx_jobs_ended ON jobs(ended_at);
CREATE INDEX idx_jobs_hold_due ON jobs(hold_expires_at) WHERE status='awaiting_approval';
CREATE INDEX idx_jobs_plan_id ON jobs(plan_id);
CREATE INDEX idx_jobs_proj_status ON jobs(project_key, status);
CREATE UNIQUE INDEX idx_jobs_request_id ON jobs(request_id) WHERE request_id <> '';
CREATE INDEX idx_jobs_session_id ON jobs(session_id) WHERE session_id <> '';
CREATE INDEX idx_jobs_source_job_id ON jobs(source_job_id);
CREATE INDEX idx_jobs_started ON jobs(started_at DESC);
CREATE INDEX idx_jobs_todo_id ON jobs(todo_id);
CREATE INDEX idx_leader_wakes_due ON plan_leader_wakes(state, due_at);
CREATE INDEX idx_leader_wakes_member ON plan_leader_wakes(member_job_id);
CREATE INDEX idx_leader_wakes_plan ON plan_leader_wakes(plan_id, state);
CREATE INDEX idx_mail_broadcast ON mail(broadcast_id, id);
CREATE INDEX idx_mail_claim ON mail(state, claim_until);
CREATE INDEX idx_mail_due ON mail(state, deliver_by);
CREATE INDEX idx_mail_from ON mail(from_kind, from_id, id);
CREATE INDEX idx_mail_resident_status ON mail_resident_agents(status);
CREATE INDEX idx_mail_thread ON mail(thread_id, id);
CREATE INDEX idx_mail_to_state ON mail(to_kind, to_id, state, id);
CREATE INDEX idx_memory_candidates_status ON memory_candidates(status, project_key, created_at);
CREATE INDEX idx_memory_suggestions_key ON memory_suggestions(tracker_id, memory_key, action, state);
CREATE INDEX idx_memory_suggestions_state ON memory_suggestions(state, created_at);
CREATE INDEX idx_messages_inbox ON messages(to_agent, status, created_at);
CREATE INDEX idx_plan_decisions_plan ON plan_decisions(plan_id);
CREATE INDEX idx_plan_decisions_session ON plan_decisions(session_id, asked_at);
CREATE INDEX idx_plan_decisions_session_cursor ON plan_decisions(session_id, asked_at DESC, id DESC);
CREATE INDEX idx_plan_decisions_state ON plan_decisions(state);
CREATE INDEX idx_plan_handoffs_latest ON plan_handoffs(plan_id, version DESC);
CREATE INDEX idx_plan_todos_plan ON plan_todos(plan_id);
CREATE INDEX idx_plans_status ON plans(status);
CREATE INDEX idx_plans_supervisor ON plans(supervisor_session_id);
CREATE INDEX idx_presence_seen ON agent_presence(last_seen_at);
CREATE INDEX idx_pty_sessions_ended ON pty_sessions(ended_at);
CREATE INDEX idx_pty_sessions_job   ON pty_sessions(job_id);
CREATE INDEX idx_push_subscriptions_caller
  ON push_subscriptions(caller_id, created_at);
CREATE INDEX idx_sched_due ON schedules(enabled, next_run_at);
CREATE INDEX idx_scoped_memories_scope ON scoped_memories(scope, scope_key, deleted, updated_at);
CREATE INDEX idx_session_job_watches_job ON session_job_watches(job_id);
CREATE INDEX idx_session_nudges_session ON session_nudges(session_id);
CREATE INDEX idx_session_nudges_state ON session_nudges(state);
CREATE INDEX idx_session_usage_daily_day ON session_usage_daily(day);
CREATE INDEX idx_steward_events_pending ON steward_events(handled_at, at);
CREATE INDEX idx_steward_reviews_day ON steward_reviews(day, id);
CREATE INDEX idx_tracker_issues_updated ON tracker_issues(tracker_id, rev);
CREATE INDEX idx_tracker_memories_updated ON tracker_memories(tracker_id, rev);
CREATE INDEX idx_wakeups_due ON job_wakeups(enabled, next_run_at);
CREATE INDEX idx_wakeups_job ON job_wakeups(job_id);
CREATE INDEX idx_work_item_sessions_sid ON work_item_sessions(session_id);
CREATE INDEX idx_work_items_project ON work_items(project_key);
CREATE INDEX idx_work_items_status ON work_items(status, updated_at);
CREATE INDEX idx_work_journal_item ON work_journal(work_item_id, id);
CREATE INDEX idx_work_journal_level ON work_journal(work_item_id, level, id);
CREATE INDEX idx_work_merge_suggestions_state ON work_merge_suggestions(state, at);
CREATE INDEX idx_work_requests_item ON work_requests(work_item_id, created_at);
CREATE INDEX idx_work_requests_state ON work_requests(state, deadline);
CREATE INDEX idx_work_summaries_at ON work_summaries(at);
CREATE INDEX idx_work_summaries_session ON work_summaries(session_id, at);
CREATE INDEX idx_worker_enrollments_source ON worker_enrollments(source, state);
CREATE INDEX idx_worker_enrollments_state ON worker_enrollments(state, created_at);
CREATE INDEX idx_workflow_events_wf ON workflow_events(workflow_id, seq);
CREATE INDEX idx_workflows_project ON workflows(project_key);
CREATE INDEX idx_workflows_status ON workflows(status);
CREATE INDEX idx_xfers_created ON xfers(created_at DESC);
CREATE INDEX idx_xfers_expiry ON xfers(state, expires_at);
INSERT INTO work_kv(k, v) VALUES ('migrated_work_deleted_audit', '1');
INSERT INTO work_kv(k, v) VALUES ('migrated_captured_dispatch_finalizes', '1');
