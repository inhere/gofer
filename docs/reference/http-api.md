# HTTP API overview

> Moved from the README (2026-10-10). Endpoint groups of the `/v1` API; the handlers in `internal/httpapi` are authoritative.


`/health` is unauthenticated; everything under `/v1/*` requires `Authorization: Bearer <token>`. Error body: `{"error":"…","detail":"…"}`.

Permissions: what each `/v1` route needs, and what every permission means (what it covers, its risk, whether it may be granted to a session, where it comes from) is in [permissions.md](permissions.md) — generated from the server code, so it always describes the running binary. A missing permission is `403` and the body names it.

- A **root administrator** holds every permission, whatever the `governance.require_*` switches say: a caller declared with `can_admin: true`, or the single `default` caller of a deployment that declares no `server.callers` (the legacy server token). A deployment with `allow_empty_token` and no callers has no root administrator at all.
- The capability bits `can_answer` / `can_attach` / `can_tunnel` and the four `governance.require_*` switches are deprecated (they still decide the config-derived, read-only grants, so an existing configuration behaves as before). `can_admin` stays: it is what names a root administrator.
- Permissions are for a **user-class caller** (a person's CLI, the web console, an agent session talking to the server directly). A worker token and a job credential never consult them: they are confined by their own allowlists (see the two sections below).
- `gofer mcp --standalone` is a **trusted local entry**: it works on the same database directly and does not go through these decisions. Permissions exist to prevent mistakes, not to isolate — holding a dangerous permission is equivalent to being an administrator.

| group | main endpoints |
|---|---|
| permissions | `GET /v1/auth/whoami`, `GET /v1/auth/catalog`, `GET /v1/auth/routes`, `GET/POST /v1/auth/grants`, `DELETE /v1/auth/grants/{id}`, `POST /v1/auth/grants/{id}/activate`, `GET /v1/auth/audit` |
| projects / agents / roster | `GET/POST /v1/projects`, `GET/PUT/DELETE /v1/projects/{key}`, `GET /v1/agents`, `GET /v1/runners`, `GET /v1/meta`, `GET /v1/metrics` |
| templates | `GET /v1/projects/{key}/templates`, `GET /v1/projects/{key}/templates/{name}?var=k=v` (read-only; the detail endpoint returns the server's render of it) |
| jobs | `POST/GET /v1/jobs`, `GET /v1/jobs/{id}[?include=events,comments,deliveries,retries,wakeups,pty_sessions,artifacts,session_jobs,messages]`, `/logs/{stdout,stderr}`, `/stream` (日志 SSE), `/acp/stream`（归一化 ACP SSE）, `/events`, `/diff`, `/artifacts`, `POST …/cancel`, `POST …/resume`, `POST/GET …/wakeups`, `GET/PATCH/DELETE /v1/wakeups/{wid}`, `GET/DELETE …/worktree`, `POST …/worktree/merge`, `POST …/attach-ticket`, `GET …/pty/sessions` |
| interactions | `POST/GET /v1/jobs/{id}/interactions`, `POST …/{iid}/answer`, `POST …/{iid}/punt`, `GET /v1/interactions` |
| workbench | `GET /v1/workbench/threads?project=&status=&q=&since=`, `PATCH /v1/workbench/threads/{s:\|j:\|r:…}`, `POST …/turn` |
| plans / decisions | `POST/GET /v1/plans`, `GET /v1/plans/{id}`, `POST …/todos`, `POST …/jobs`, `POST …/run\|pause\|resume`, `POST/GET /v1/decisions`, `POST /v1/decisions/{id}/answer` |
| session relay | `GET/POST /v1/sessions`, `POST /v1/sessions/{sid}/heartbeat`, `…/relay`, `…/say`, `…/deliver`, `…/release-takeover`, `…/owner` (admin hand-over), `…/turns` |
| workflows / schedules | `GET /v1/workflow-templates[/{name}]`, `POST /v1/workflow-templates/{name}/render`, `POST/GET /v1/workflows`, `…/{id}/cancel`, `…/{id}/pick`, `…/events`, `…/export`; `POST/GET /v1/schedules`, `…/enable`, `…/disable`, `…/run-now`, `…/rotate-token`, `POST /v1/schedules/{id}/trigger?token=…` (unauthenticated, schedule token) |
| browser push | `POST /v1/ws-ticket` (user callers only; job/worker credentials get 403) → `GET /v1/ws?ticket=…` (WebSocket, unauthenticated route that consumes the one-time ticket) |
| workers / tunnels | `GET /v1/workers/connect` (WS), `/v1/workers/pty-connect`, `POST /v1/workers/{id}/reload`, `GET /v1/tunnels`, `/v1/tunnels/connect`, `/v1/workers/tunnel-connect`; forwarder registrations `GET/POST /v1/tunnels/forwarders`, `PUT/DELETE /v1/tunnels/forwarders/{id}`, `POST /v1/tunnels/forwarders/{id}/stop` — each spec is `{network: tcp\|udp\|socks, bind, local_port, target}`, a `socks` spec's `target` is always `*` |

`POST /v1/jobs` body (snake_case): `project_key`, `agent`, `runner`, `prompt` / `cmd`, `cwd`, `timeout_sec`, `title`, `worker_id` / `worker_labels`, `interactive`, `lock_paths` / `lock_wait_sec` (the directory lock: the job holds these project-relative paths — and their sub-directories — exclusively while it runs, empty keeps the older behaviour of locking its whole `cwd`; `lock_wait_sec` caps how long it queues behind the holder, `0` = unlimited where the server allows it, see `server.dir_lock_allow_unbounded_wait`), `worktree` / `worktree_base`, `plan_id`, `tags`, `sync` / `wait_timeout_sec`, `request_id` (idempotency key), `template` / `vars` (a server-rendered task book), `prime_keywords` (strings naming the subsystems / languages / modules the task touches: the injected tracker-prime matches them, together with the title and the head of the prompt, against the memories' `when.keywords` / keys / tags / summaries, injecting the hits in full and first). `notify: true` tells a session, through the session messenger, that the job has ENDED (terminal states plus a delivery parked in `needs_review`); `notify_session: "<sid>"` names that session explicitly and implies `notify`, while `notify` alone targets `source_session_id`. It is off by default (each notice starts one messenger process), `400` without any session to tell or with an unknown / foreign session, and each notice is sent at most once per job (the session's job watch is the durable claim, so a restart cannot resend it). The `400` belongs to the CALLER naming the fields: a job gofer submits on its own (a schedule tick or run-now, a plan dispatch, a workflow step, a wakeup / auto-resume / rerun, a hold approval or update re-entry, a failover, a durable retry) instead DROPS an unusable notification and records `job.notify_dropped {target, reason}` on the job — the work runs, and the timeline says why nobody was told. `secrets: ["<name>"]` gives the job secrets declared under the top-level `secrets` and allowed by `projects.<p>.secrets` (server runner only, not interactive): only the names are stored, the values are injected into the job environment and masked in its logs, and a refusal (undeclared, not allowed, unset variable) is `400`. A refusal by a `job.submit` decision hook (`server.decision_hooks`) is `403` with `denied by decision hook <name>: <reason>`; a hook's tightening shows up on the job and as `job.decision_hook` events. `GET /v1/config` lists `server.decision_hooks` read-only (`name, points, command, projects, dir, env_keys, timeout_sec, on_error, include_internal, enabled`); the config write endpoints refuse the key.


## Permission grants (`/v1/auth/*`)

A **grant** is a stored pattern (`tunnel.*`, `job.*.others`, `*`) issued to one subject: `users` (every user-class caller), `caller:<id>` (one caller, i.e. one token), or `session:<sid>` (one terminal session). A caller's effective permissions are the config-derived, read-only ones plus the grants of `users` and of its own `caller:<id>` subject, plus — for the session the request names — the `session:<sid>` grants that are in force (the conditions are below). Managing grants needs `auth.manage`, which only a **root administrator** holds — no pattern can ever reach it, so `*` does not hand it out either. A worker token and a job credential are refused everywhere here: a worker has its own route list, and a job credential may read every `GET` anyway, so these handlers additionally require a person.

| endpoint | needs | answer |
|---|---|---|
| `GET /v1/auth/whoami` | any caller | the caller id, its kind (`user` / `worker` / `job`), whether it is a root administrator, every permission it holds with its source (`root` / `config` / `db`), the grant a `db` permission came from, the session the request named (`X-Gofer-Session-Id`), the deployment's root administrators, the grant-table `grants_version`, and a `hint` when nobody can grant anything or the credential is not a permission subject at all. |
| `GET /v1/auth/catalog` | any caller | the whole permission catalog (name, what it covers, risk, whether it may be granted to a session, scope, source, seed version) plus the preset patterns with what they really hand out. |
| `GET /v1/auth/routes` | any caller | every route of the running binary with its declaration: the user-class category (`require` / `in_handler` / `public` / `no_user_surface`), the InHandler mode (`deny` / `filter`), the permissions it needs, and the other credential it accepts. The same picture as [permissions.md](permissions.md), from the running binary. |
| `GET /v1/auth/grants` | `auth.manage` | `{version, roots, grants[], derived[]}`: the stored rows — revoked and orphaned ones included — each with its `state` (`active` / `orphaned` / `revoked` / `expired` / `unmatched`), the permissions its pattern means today, the stored `snapshot`, and `new_permissions` (what the pattern means now that the snapshot did not contain — 「有新权限待确认」); `derived` holds the config-derived, read-only sets per subject. |
| `POST /v1/auth/grants` | `auth.manage` | body `{subject, pattern, scope?, expires_sec?, note?}` → `201` with the row. |
| `DELETE /v1/auth/grants/{id}` | `auth.manage` | revokes the grant: the row stays (audit, and a rollback must not resurrect it) and stops applying at once. Revoking twice is a no-op that answers `200` with the row. |
| `POST /v1/auth/grants/{id}/activate` | `auth.manage` | clears the orphan mark, so a grant whose caller came back applies again. |
| `GET /v1/auth/audit` | `auth.manage` | the audit table, newest first, filtered by `subject`, `permission` and `grant_id` (`limit`, default 100, max 500). Rows are written as the audit side of the model lands; an empty list means nothing was recorded. |

The details worth knowing:

- **Validation before writing**: subject must be `users` / `caller:<id>` / `session:<sid>`; a `caller:<id>` must be a caller this configuration declares; `scope` is `*` in this phase; the pattern must mean at least one permission (a pattern reaching nothing, only `auth.manage`, or a dangerous permission behind a wildcard is a `400`, not a stored no-op). A session subject must name a session that exists and has not ended (its state is not `ended` **and** its `ended_at` is empty, so a session taken over after it ended is refused too) and carry `expires_sec` between 1 and 86400; the row records that session's owner and incarnation at that moment.
- **Session grants apply to one session, and to one life of it**: a `session:<sid>` grant is merged into a caller's effective permissions only for a request that carries that session's id in `X-Gofer-Session-Id`, and only when **all** of these hold: the caller is a user-class caller that is the session's **current** owner (and the owner the grant recorded); the session's **current** incarnation equals the recorded one; the session has not ended (state is not `ended` and `ended_at` is empty); the grant is neither revoked nor expired. A session that ended and came back, was taken over after it ended, or was deleted and re-registered has a NEW incarnation, so the grants of its previous life never apply again — nothing has to revoke them for that. `POST /v1/sessions/{sid}/owner` does revoke them, in the same transaction as the owner change, so the row says what happened instead of looking live forever.
- **Where `X-Gofer-Session-Id` comes from**: the gofer CLI sets it on every request it makes (requests, the job submit, the SSE stream, file transfers, and the tunnel websocket handshake — one injection point), from the environment: an explicitly set `GOFER_SESSION_ID` wins, otherwise the agent session variables `CLAUDE_CODE_SESSION_ID` / `CODEX_THREAD_ID` / `CODEX_SESSION_ID` must agree on exactly one id — with no candidate, or two different ones, nothing is attached (naming the wrong session would hand the request another terminal's grants). Identification never looks at the working directory or a marker file. A job inherits none of these variables: `GOFER_SESSION_ID`, `CODEX_THREAD_ID` and `CODEX_SESSION_ID` are in the job environment denylist beside `CLAUDE_CODE_SESSION_ID`, so commands run inside a job name no session. An explicit `GOFER_SESSION_ID` is inherited by descendant processes — including a `gofer tunnel forward -d` daemon — so everything started from that terminal names it.
- **Errors**: `400` invalid subject / pattern / scope / caller / expiry; `404` unknown grant or unknown session; `409` a config-derived entry (they follow the configuration) or a grant on an ended session; `403` a caller without `auth.manage` (the body names it) or a worker / job credential.
- **Orphans**: a `caller:<id>` grant whose caller leaves the configuration is marked an orphan and stops applying. The caller coming back does **not** restore it — only `activate` does, so a token reused by somebody else cannot inherit the previous holder's grants.
- **Cache**: the database half of a caller's effective permissions is cached with the grant-table generation. A write through this server (a new grant, a revoke, an activate) applies to the very next request; another process's write is picked up within 5 seconds.
- **CLI**: `gofer auth whoami` prints the same answer as `GET /v1/auth/whoami` (that is the command a `403` points at), and `gofer auth routes` prints the route table.

## Split config and agent extends

`extends` is `builtin` (the built-in template of the same name), `builtin:<template>`, or the key of another agent declared in the same config; the API reports the value as written.

- `GET /v1/config`: `sources: {section: file}` (file relative to the main config file) when the config uses `include`; absent for a one-file config. Agent rows carry `extends` (`"builtin"`) and `overrides` (the keys the declaration wrote) for an agent declared with `extends: builtin`.
- `GET /v1/agents`: the same `extends` / `overrides` per agent.
- `PUT /v1/config/agents/{key}` on an `extends` agent keeps it an `extends` agent: the file gets only the declared keys plus fields that now differ from the template.
- A config reload receipt (`serve reload`, `POST /v1/config/reload`) lists `files` (main first) when the config is split.

## MCP registry

- `GET /v1/mcp-servers` -> `{servers:[{name, enabled, spec, secrets:[{env, set, usage}], bound:{server, agents[], projects[]}}]}`: the `mcp_servers` registry. `spec` is the declaration (`secret_env` / `secret_headers` hold variable NAMES); `secrets[].set` says whether the variable is set in the server's environment; values are never returned. `GET /v1/mcp-servers/{name}` is one entry (404 when undeclared).
- `POST /v1/mcp-servers/{name}/test` -> `{ok, result?:{server_name, server_version, tools[], elapsed_ms}, error?}`: starts / connects to the server from the server's machine and lists its tools; a failure is `200` with `ok:false`. Person credentials only (worker / job credentials 403).
- Jobs: request field `mcp` (names, added to the server / agent / project bindings) and `no_mcp`; the job carries the decided list as `mcp`. Events `job.mcp_injected {servers, notes}` and `job.mcp_skipped {reason, names}` (a worker below protocol v23).
- Config view: `server.mcp`; agent rows `mcp` / `mcp_format` / `mcp_args`. All editable through the config write API (an agent PUT replaces the editable set, so a console must send them back).

## Delivery of worktree jobs (GIT-02)

- `POST /v1/jobs/{id}/deliver` body `{mode?: merge|push|pr, draft?, branch?, dry_run?}` delivers a done `--worktree` job now (default mode: the request's `deliver`, else `projects.<k>.delivery.mode`). With `dry_run: true` (also allowed while the job is `needs_review`), returns `200` with `{mode, branch, remote_branch, remote, base, draft, labels, allow_push, title, body}` and does not execute or record a delivery. When the configured base is empty, the preview resolves it from the main checkout for a local runner when possible; it remains empty for a remote runner or when resolution fails. Without `dry_run`, returns the outcome `{mode, state: merged|pushed|pr_opened|failed|pending|skipped, branch, remote, base, provider, pr_url, pr_number, compare_url, note, error, at}`; a delivery that ran and failed is `200` with `state: failed`. `404` unknown job, `409` not a done worktree job or mode none, `400` bad mode. Person credentials only (worker / job credentials `403`). A job that ran on a worker is delivered there by an internal exec job (tag `delivery`); the outcome reads `pending` until it ends.
- Jobs carry `delivery` (the outcome, persisted) and requests `deliver`, `deliver_draft`, `deliver_branch`. `GET /v1/jobs/{id}` adds `delivery_mode` (what the current config delivers with) for worktree jobs.
- `POST /v1/jobs/{id}/accept` accepts `no_deliver: true` (accept without delivering).
- Events: `job.delivered`, `job.delivery_failed` (default notification), `job.delivery_skipped`.

## Cost estimate (pricing)

- Top-level `pricing` (USD per million tokens per model name or glob) prices usage that reports no cost. Session views carry `usage.estimated_cost_usd` and `usage.unpriced_models`; plan detail `usage.session.estimated_cost_usd`, `usage.overall.estimated_cost_usd` (the estimated part of `overall.cost_usd`); `/v1/stats` `session_usage.windows.<w>.estimated_cost_usd` / `unpriced_models`; `GET /v1/jobs/{id}` `cost_estimate_usd` for a job whose agent reported no cost.

## Job usage

- `usage` on a job (`GET /v1/jobs/{id}`, job list rows): `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`, `total_tokens`, `cost_usd`, `tool_calls` (when the agent reports it), `turns` (budgeted jobs), `source`. Absent counters were not reported.
- `wrap_up` on a job (`GET /v1/jobs/{id}`, job list rows, omitempty; gofer-gk74): the reason the AGENT itself reported for ending the run early instead of answering — `max_steps` / `max_tokens` / `timeout`, read off its structured stream (`result.wrap_up`, or an `error` row saying `max steps exceeded: <n>`). The job keeps `done` and its report: the agent stopped itself, which is how a prematurely wrapped-up task is told apart from a finished one. Absent when the agent answered, or when its dialect reports no such reason (unlike `stop_reason`, which is the ACP protocol's stopReason). Budget kills by gofer itself are a different signal (`failure_class=budget`, `failed`).

## Runner tools

- `GET /v1/runners`: every row with known capabilities has `capabilities.tools: [{name, desc?, bin?}]` — the local row from the server's top-level `tools`, a worker row from the worker's report (also under `worker.tools`). Omitted when nothing is declared.
- `GET /v1/workers/{id}`: `worker.tools`, same shape.
- Descriptive only: the list is what agent jobs on that machine are told; it does not affect routing.

## Job detail aggregation: `include`

`GET /v1/jobs/{id}?include=a,b,c` 一次取回详情页要的附属数据；缺省 `include` 为空，等于普通 job 快照（CLI / MCP 不受影响）。可选项：

- `events`：最近 200 条升序，带 `events_last_seq` / `events_truncated`，更早的用 `&before=<seq>`。
- `comments`：最新 100 条 + `comments_total`。
- `deliveries`、`retries`、`wakeups`。
- `pty_sessions`：无 attach 权限时省略，并在 `include_errors.pty_sessions` 写 `forbidden`。
- `artifacts`：仅终态 job 内联，最多 200 + `artifacts_total`。
- `session_jobs`：同 session 的 job，最多 50。
- `messages`：「发给 agent 的消息」列表（升序），供详情页的「消息」区使用。每条：`role`（`user` = 发给 agent 的、`agent` = 回复摘要）、`job_id`、`at`（所属 job 的时间锚点，同一 job 的轮次共用）、`text`、`truncated`、`omitted`（与上一条之间有一部分记录没读：超出读取窗口）、以及仅在**会话首条**消息上出现的 `injected`（被剥掉的注入 rules 段全文，供前端「查看完整注入内容」展开）。

  `user` 的内容取该 job 存下的 request prompt（剥离注入的 rules 段，与 `GET /v1/jobs/{id}/request` 同样脱敏），持续会话里 `session say` 发出的各轮消息则来自该 job 的 ACP 结构化记录（`artifacts/acp.jsonl`）——say 只进常驻 ACP 进程的输入通道、不落 job 行，那份记录是它唯一的痕迹；`agent` 是同一记录里该轮回复的摘录（限长，`truncated: true`）。没有结构化记录的 job（exec / 纯 CLI agent）只有一条 `user`。这一项是**尽力而为**：会话查询或记录读取失败时退化成「存下的 prompt」，不会失败、也不写 `include_errors`。

每项独立容错：失败项写进 `include_errors`，整体仍 200；未知 include 名是 400。日志正文、diff、`/request` 不在其中。

## Job resume

`POST /v1/jobs/{id}/resume` 续接源 job 的底层会话，产生**新 job**（源 job 须已终态、捕获到 `session_id`；用法见 skill `gofer-usage/references/job-advanced.md`）。请求体字段全部可选：

| 字段 | 说明 |
| --- | --- |
| `prompt` | 新一轮的文本 |
| `runner` | 必须等于源 job 的 runner（跨 runner 是 400） |
| `mode` | `session` \| `interactive` \| `batch`；缺省按源 job 的形态 |
| `agent` | 同会话族的另一个 agent |
| `model` / `budget` / `env` | 覆盖继承自源 job 的 model / budget / env |
| `plan_id` | plan 归组：**不带 = 继承源 job 的 plan**（提交时的 `plan_id`、plan 派发、`plan attach` 都算）；给 id = 挂到该 plan；给空字符串 `""` = 不挂任何 plan（等同 CLI 的 `--no-plan`），同时丢弃继承的 checklist 挂接（todo 属于创建它的那个 plan） |
| `no_notify` | `true` = 不带源 job 的结束通知（gofer-3db4）；**缺省 = 继承**（源 job 开了 `notify` 时，续跑结束也通知同一会话；CLI 的 `job resume --no-notify`） |
| `lock_paths` | 目录锁声明：**非空即覆盖**源 job 继承来的 `lock_paths`（缩小或改锁，CLI 的 `job resume --lock`），并同时要求独占锁（源 job 是 `--shared-dir` 时也会取锁）；缺省/空 = 继承源 job 的声明。cwd 不可改 —— ACP 会话的 `session/load` 必须在同一 cwd，重设锁是为了让别的子目录 job 不再排队 |

返回新 job 的 JobResult（其中的 `plan_id` / `todo_id` 即落到新 job 上的值）。错误：404（源 job 不存在）、400（源 job 非终态、无 `session_id`、agent 不支持续接、跨 runner）。

## Jobs held for approval

- Submit: `POST /v1/jobs` with `hold: true` (optional `hold_reason`, `hold_timeout_sec`) returns **202** with `X-Gofer-Async: 1`; the body is the JobResult plus `approve_url` (empty when `server.web_base_url` is not set). The job stays in `awaiting_approval` and is not dispatched.
- Approve: `POST /v1/jobs/{id}/approve`, body `{note?}`; returns the started JobResult (usually `queued`).
- Reject: `POST /v1/jobs/{id}/reject`, body `{note?}` or `{reason?}` (optional for a held job; `resume: true` is a 400); the job ends `cancelled` with `error="hold rejected by <who>[: <note>]"`. Withdraw: `POST /v1/jobs/{id}/cancel`.
- Errors: 409 (no longer awaiting approval, or the request changed while held), 503 (server draining for an upgrade), 404, 403 (worker token, job credential, or a caller without `job.review`; a root administrator always holds it).
- `hold` on a job: `{reason, timeout_sec, expires_at, origin, command[] | prompt_preview, decision?, decided_by?, decided_at?, note?}`; `decision` is `approved | rejected | expired | cancelled`; an expired hold ends `cancelled` with `error="hold expired"`.
- Events: `job.awaiting_approval{reason,timeout_sec,expires_at,origin}`, `job.hold_approved{by,note?}`, `job.hold_rejected{by,note?}`, `job.hold_expired{expires_at}`. `/v1/today` lists a card of kind `approval` (actions `approve`, `reject` with `optional_text`, `open`); `/v1/stats` `by_status` has an `awaiting_approval` key.

## Jobs parked for an update

- While a one-click update waits for the machine to go idle it parks new work instead of refusing it (gofer-dzv3): the job passes every admission check, gets a result dir and a row in the non-terminal `paused_for_update`, and starts (same id, original request submitted again) once the update releases it. Like `awaiting_approval` it has no in-process entry, no concurrency slot, no directory lock and no dispatch, and a restart keeps it — but nobody decides anything about it, so it appears in no approval list (`GET /v1/jobs?status=awaiting_approval` never contains it, `/v1/today` has no card for it), `POST /v1/jobs/{id}/approve` refuses it (`409`), it has no hold deadline and the hold expiry sweep ignores it, and no notification is sent for it. Cancelling it (`POST /v1/jobs/{id}/cancel`) ends it `cancelled` without running; a submit while its server drains for that update also parks rather than answering `503`.
- Events: `job.paused_for_update{apply_id, reason}`, `job.update_resumed{by}`. `/v1/stats` `by_status` has a `paused_for_update` key; a plan's roll-up counts it as queued.
- gofer's own short control-plane jobs are NOT parked (gofer-qzv5): a session-relay delivery / injection (`relay-deliver` / `relay-inject`) and a session messenger (`session-messenger`) job runs through the pause, because parking it would lose the message it carries (a web reply, a job-ended notice) while blocking the restart for a couple of seconds at most. Everything a person or an agent submits still parks.

## Browser push: `/v1/ws`

web 使用，脚本一般不需要。

- `POST /v1/ws-ticket`（Bearer；job 凭据和 worker token 返回 403）换 30s 一次性 ticket，再 `GET /v1/ws?ticket=…`（auth 组外；Origin 须与取 ticket 时一致，并遵守 `governance.attach_origins`；每个 caller 最多 16 条连接，超出 429）。
- JSON 文本帧：客户端 `sub|unsub`（`topics`，`since_seq` 给 `job:<id>` 断线补发）/ `ping`；服务端 `hello`（`server_time`、`version`）、`snap`（快照）、`evt`（增量）、`inval`（失效，客户端自己 REST 重拉）、`resync`（队列满丢过增量，重订并重拉）、`pong`、`error`。服务端每 20s WS ping，60s 无响应断开。
- 主题：`stats`（快照，合并节流 >=2s，仅有订阅者时计算）、`pending`（快照 = 待应答 interaction + OPEN decision）、`jobs`（inval，带变化的 job id/status）、`job:<id>`（status / event / interaction 增量）、`sessions`（inval，心跳不推）、`runners` / `meta` / `plans` / `workflows` / `schedules` / `work`（inval）。
- web 兜底：WS 断开超过 15s 回落 30s 一次的低频轮询；页面隐藏超过 5 分钟主动断开，回到前台重连；`hello.version` 变化触发「有新版本」提示。
- 反向代理需放行 `Upgrade` 头、关缓冲、空闲超时 > 60s；Vite 开发代理已加 `/v1/ws` 的 `ws: true`（开发源要加进 `governance.attach_origins`）。
- `GET /v1/agents`、`GET /v1/meta` 在前端有模块级缓存，由 `meta` 主题的 inval 失效。

## Today and "needs my decision"

web 默认落地页 `/today` 的数据接口。人与普通 job 凭据可读；steward 凭据只能读 `GET /v1/today` 与记忆整理的读接口；worker token 一律 403。

- `GET /v1/today[?since=<unix>][&include_exec=1]` -> `{digest:{since_last:{since,jobs_done,jobs_failed,commits},title,text,commentary}, decisions:[card], snoozed:N, status:{usage_today,steward_today,runners:{online,total,offline,running_jobs},version,alerts}, generated_at}`。`since` 缺省 = 服务器当天 0 点；`include_exec=1` 把 exec agent 的待验收也纳入。
- 卡片字段：`key`（`<kind>:<ref>`）、`kind`（`interaction|decision|relay|review|work|suggestion|merge|plan_blocked|memory`）、`tag`、`urgency`（`now|blocking|normal`）、`blocks{score,items,text}`、`title`、`project_key`、`agent`、`waiting_since`、`expires_at`、`activity_at`、`summary`、`review{commits,adds,dels,verify}`、`suggestions[]`、`refs{job_id,interaction_id,decision_id,session_id,thread_id,work_item_id,plan_id,todo_id,field,merge_id,memory_suggestion_id,tracker_id,memory_key}`、`memory{tracker_id,key,action,payload,current_kind,current_summary,age,content}`（仅记忆整理卡）、`actions[{id,label,style,value,needs_text}]`（最多 3 个主操作）、`advice`（无建议为 null，否则 `{text, action_id?, digest?, by, at}`）、`woke` / `woke_reason`（`time|job|activity`，从稍后回来的卡才有）。正在稍后的卡不在 `decisions` 里，`snoozed` 是它们的数目。
- 纳入规则：全部 pending interaction；OPEN decision（中继轮次按会话合成一张）；`needs_review` job；工作项 `needs_me` / `needs_onsite` / 提醒或搁置到期；工作项整理建议（仅 `status_hint` 与 `goal`）；待处理的合并建议；`blocked` 的 plan。排序：1 小时内会超时的最前，其余按阻塞分降序、等得久的在前。
- `POST /v1/today/actions {card_key, action_id, advice_action_id?, advice_text?, advice_label?, via_advice?, title?, label?, kind?}`：卡片操作成功后由 web 记一条 `today.action` 审计；只接受人凭据，job 凭据 403。
- `POST /v1/today/advice {card_key, text, action_id?, digest?}` -> `{card_key, advice}`：写（覆盖）一张卡的建议。只接受人或 steward 凭据；卡必须在当前队列里（否则 404）；`text` 必填且 <=60 字，`digest` <=5 行，`action_id` 必须是这张卡可一键执行的操作键，decision 卡不得带 `action_id`（均 400）。
- `GET /v1/today/handled?days=7`（1-30）-> `{handled:[{at,actor,card_key,kind,title,action_id,label,advice_action_id,advice_text,advice_label,via_advice}], days}`，最新在前。
- `POST /v1/today/snooze {card_key, until_at? | until_job_id?}`（二选一；`until_at` 须在未来 30 天内，`until_job_id` 须是存在且未结束的 job）；卡不在当前队列 404，参数错 400；同一张卡再稍后会覆盖；只接受人凭据。稍后不暂停超时：到点照旧按交互 / 决策自己的 `on_timeout` 处理。
- `DELETE /v1/today/snooze/{card_key}`（card_key 需 URL 编码，交互卡的 key 含 `/`）：放回队列；没在稍后 404；只接受人凭据。
- `GET /v1/today/snoozed[?include_exec=1]` -> `{snoozed:[...]}`：当前正在稍后的卡。
- `GET /v1/today/lanes` -> `{lanes:[...], summary:{total, agents_running, attention}, generated_at}`：未结且未搁置的工作项，加上在跑 / 阻塞的 plan；已挂在工作项下的 plan 并入该工作项的行。每行：`id` `kind`（work|plan）`title` `project_key` `status`、`agents[]`、`progress`、`health` / `health_reason`、`started_at` / `elapsed_sec` / `activity_at`、`usage`、`links`。

### Memory tidy-up (repository memories)

- `GET /v1/memory-findings[?tracker_id=][&all=1]` -> `{findings:[{tracker_id,project_key,key,kind,summary,updated_at,age,findings:[{slug,detail}],actions,content}], daily_cap}`（server 端 doctor；默认只列还有可提动作的，`all=1` 连已提 / 30 天内被忽略的也列）。
- `GET /v1/memory-suggestions[?state=pending|adopted|dismissed|stale|all]`。
- `POST /v1/memory-suggestions {tracker_id,key,action:archive|merge|kind|summary|when,payload:{into?,content?,kind?,summary?,keywords?},reason}`：人或 steward 凭据；新建 201、同 key + 动作已在等 200 原样返回、记忆不存在 404、参数错 400、30 天内被忽略或今天已满 5 条 409。
- `POST /v1/memory-suggestions/{id}/adopt|dismiss`：只接受人凭据；已处理 409；记忆 / 合并目标在建议之后被改过 409 并把建议记为 stale。
- `GET /v1/tracker/memories?tracker_id=` 的返回带 `doctor:{<key>:[{slug,detail}]}`。

## Dashboard statistics

- `GET /v1/update` returns the last release check `{current, latest, newer, source: github|mirror, checked_at (0 = never), released_at, release_url, download_url, sha256, description, error}` plus `managed` (the server runs as a registered service and can update itself) and `apply` (the latest one-click update, see below); `POST /v1/update/check` runs one now and returns the same (a source failure is a `200` with `error`; job credentials get `403`). Configured by `server.update` (file-only); `503` when the server runs no checker. A newly found version sends `update.available` (default notification).
- `POST /v1/update/apply {"workers": ["<id>", ...], "wait_idle_sec": 7200}` (admin; job credentials get `403`) starts a one-click update and answers `202` with the apply state `{id, from, version, phase: downloading|waiting_idle|restarting|upgrading_workers|done|failed|cancelled, error, workers: [{id, os, arch, state: pending|accepted|succeeded|failed|skipped, error, upgrade_id}], started_at, updated_at}`. The server downloads and verifies its own platform's binary (and the binaries of chosen workers on other platforms), waits for the machine to go idle, runs `serve upgrade` with the binary, and after the restart upgrades the chosen workers. `409` for a server that is not a managed service, when no newer release was checked, or while another apply runs; `400` for an unknown worker.
- Waiting for idle (gofer-dzv3): `wait_idle_sec` is how long the update may wait for the machine to go idle (`0` = 7200, clamped to `[60, 86400]`). During `waiting_idle` the server **pauses new starts** — a submitted job is admitted and parked in the non-terminal `paused_for_update` state (per project / agent / exec gates as usual) and starts when the update releases it, under its own id; a submit is never refused with `503` for it, not even while the drain of `restarting` has admission closed. Jobs already running keep running. `GET /v1/update`'s `apply` then carries `wait_deadline`, `wait_timeout_sec`, `waiting` (up to 20 `{id, title, status, runner, started_at}` — the jobs that still keep the restart waiting: jobs this server owns plus worker jobs no worker has taken over yet; a job a worker is already running does not block) and `paused` (how many jobs the update has parked).
- Cancel: `DELETE /v1/update/apply` (admin; job credentials get `403`) stops an update that is waiting for idle — the parked jobs run again and the apply becomes `cancelled`. `409` when nothing is waiting for idle (already restarted, or none).
- Every exit releases the parked jobs: the new server does it **before** it upgrades the chosen workers (a restart that did not take effect releases them too), and so does a timeout (`failed`, "waited … for the server to go idle and gave up: N job(s) still running: …"), a cancel, a download failure and a failed `serve upgrade` (whose `error` now quotes the last line of `run/update/serve-upgrade.log`, the jobs that were still running, and that log's path). A server that starts while an apply was still waiting fails that apply (`the server restarted while the update was waiting for it to go idle; run the update again`) after releasing what the dead update had parked.
- Events: `job.paused_for_update{apply_id}`, `job.update_resumed{by}`.
- `GET /v1/stats/overview?range=today|7d|30d|all&tz=<UTC 偏移分钟，东正，UTC+8=480>`：range 缺省 7d，tz 缺省 server 时区，非法 400；鉴权同 `/v1/stats`。
- 返回 `{range{key,from,to,tz,first_job_at}, totals, jobs{total,done,failed,cancelled,rejected,needs_review,in_progress,success_rate}, time{wall_sec,avg_sec,median_sec,active_sec,human_wait_sec}, git{commits,jobs_with_commits,files,insertions,deletions,git_jobs}, signal{turns,tool_calls,human,…,coverage}|null, daily[], hourly?[], best{…}, heatmap{…}, agents[], projects[], review{…}, workload{longest[],quickest[]}, usage{cost_usd,…,by_model[]}|null, notes[], generated_at, cached}`。`hourly` 只在 `range=today` 返回（24 行，tz 下本地小时，补零）。
- 口径：周期指标按 `ended_at` 归入区间（终态或 `needs_review`）；成功率 = done / (done + failed + timeout + rejected)；运行时长 = ended - started（不含排队）；活跃 = 运行 - 等人；信号（轮次 / 工具调用 / 人介入）不计 exec agent；验收按 `reviewed_at` 归入；终端会话用量按 UTC 日切分。没有数据来源的指标返回 null（页面显示「—」，不是 0）。只统计 gofer 自己的消耗，没有供应商额度。
- 数据来源：job 结束时写一行 `job_metrics`（模型、轮次、工具调用、人介入、等人 / 活跃时长、提交数、改动文件 / 增删行、token / 费用）。增删行 = 本机 job 结束时 `git diff --shortstat <base>`，仅在采集 diff 或 job 有提交时跑，远端 job 留空。
- 服务端按 (range, tz) 缓存，job 结束 / 验收 / 会话用量落库后失效（至少保留 10s），最长 60s（`all` 5 分钟），同 key 并发只算一次。
- `POST /v1/stats/backfill`：为老 job 补指标，CLI 入口是 `gofer tool stats-backfill`。

## Work items (REST and MCP)

- `GET|POST /v1/work-items`（筛选 `status` `project` `workspace` `unsorted` `session` `q` `closed` `due`；返回 `{items, summary:{needs_me,due,open}}`）。
- `GET|PATCH|DELETE /v1/work-items/{id}`：PATCH 带 `rev`，409 体里有 `current`；DELETE 只删终态项（`done` / `dropped`），进行中 400、不存在 404。
- `POST /v1/work-items/delete {ids?:[], status?:"dropped"|"done"}`：批量，返回 `{deleted:[...], failed:[{id,error}]}`，逐项独立事务。
- `GET|POST /{id}/journal`（GET 可带 `?level=milestone|detail`，POST 可带 `level`）；`GET|POST /{id}/sessions` + `DELETE /{id}/sessions/{sid}`；`GET|POST|DELETE /{id}/links`。
- `POST /{id}/merge {sources}`、`POST /{id}/split {title,goal,session_ids,keep_sessions}`。
- `POST /{id}/report`（可带 `request_id`）；`POST /{id}/report-request {session_id?, kind?: report|handoff}`（返回每个会话的 `request_id` / `kind` / `state`，不在运行的会话 `kind` 变成 `summarize`）；`GET /{id}/requests?active=1` 与 `GET /v1/work-items/requests`（请求账本）。
- `POST /{id}/summarize`（立即整理，返回账本请求）；`POST /{id}/suggestions/{field}/accept|dismiss`；`GET /v1/work-items/summarizer`（整理器状态 + 有效设置）；`PUT /v1/config/work`；`GET|POST /v1/work-items/digest`。
- `POST /v1/work-items/{id}/to-todo {plan_id?, new_plan_title?}` -> `{todo_id, plan_id, plan_created, item}`：只能转一次（已有 todo 关联再转 409，体里带已有的 `todo_id` / `plan_id`）；job 凭据 / steward 不可调用。
- 详情 / 卡片带 `field_sources`、`requests`、`suggestions`、`milestones`、`health`、`health_reason`。
- 权限：worker token 一律 403；job 凭据只读 + `report`（整理 / 采纳 / 请求都是人的操作）。
- `POST /v1/session-ask {session_id, text, work_id?}`：给一个在线会话捎一句话。`session_id` 是终端中继会话 id，或 ACP / pty 持续会话的 job id（等同 `job say`）。会话不在线或送达失败 409（不排队、不静默丢），找不到 404，空文本 400；普通 member / leader job 凭据 403。
- MCP 工具：`gofer_work_list` / `gofer_work_get` / `gofer_work_update`（描述性字段，不含 status）/ `gofer_work_note` / `gofer_work_report`，只读 `gofer_session_list` / `gofer_session_get`，`gofer_work_requests`（只读账本）、`gofer_work_request_report`、`gofer_work_summarize`（后两个需要连着运行中的 server），`gofer_session_ask`，只读 `gofer_issue_list` / `gofer_issue_get`。采纳建议不提供 MCP 工具（人的决定）。`--project` 收窄的 MCP 只看 / 改本项目的工作项，没有 `gofer_session_ask`，`gofer_issue_list` 固定在本项目。

## Steward credential allowlist

server 给管家 job 签发 `steward` 种类的 job 凭据（与 member / leader 并列）。服务端对读和写都是默认拒绝，只放行：

- 读：`GET /v1/work-items*`（含 journal / sessions / links / requests / merge-suggestions）、`/v1/sessions`、`/v1/sessions/{id}`、`/v1/sessions/{id}/tail`、`/v1/jobs`、`/v1/jobs/{id}`（只读状态，没有日志 / request / 产物）、`/v1/steward`、`/v1/steward/notes`、`/v1/issues*`、`GET /v1/today`、`GET /v1/memory-findings`、`GET /v1/memory-suggestions`。
- 写：`POST /v1/today/advice`、`POST /v1/memory-suggestions`（只是提议）、`POST /v1/session-ask`、`PATCH /v1/work-items/{id}`（状态不得为 done / dropped，人手动设的状态优先，不能碰 `status_source`）、`POST /{id}/journal`（记为 steward 日志）、`POST /{id}/report-request`、`POST /{id}/summarize`、`POST /{id}/merge-suggestions`、`PUT /v1/steward/notes`、`POST /v1/steward/review-summary`。
- 其它一律 403：提交 job、`/v1/config*`、`/v1/steward/ask|start|stop|review`、merge / split、删除、accept / reject 等。

注意：server 以 `allow_empty_token`（无鉴权）运行时不校验 bearer，job 凭据形同虚设，白名单只剩 MCP 工具层；要让服务端强制，必须给 server 配 token。管家 agent 自带的工具（如 shell / 文件读写）也不受 gofer 凭据约束，只受 prompt 约束。

Threat model: the steward works under its own credential, so its `session-ask` / `report-request` are not limited by the owner of the session they address. Whoever may drive the steward (`steward.manage`, seeded to every user-class caller) can therefore speak to somebody else's terminal session indirectly — a residual bypass of the session ownership rules, knowingly kept in this phase.

Steward 相关 REST：`GET /v1/steward`（状态 + 有效设置）、`POST /v1/steward/start|stop|restart|ask|review`、`GET|PUT /v1/steward/notes`（PUT 带 `version`，过期 409 + `current`；`?version=N` 看历史，`?history=1` 列版本）、`POST /v1/steward/review-summary`、`PUT /v1/config/steward`、`GET /v1/work-items/merge-suggestions`、`POST /v1/work-items/{id}/merge-suggestions {source_id, reason}`、`POST /v1/work-items/merge-suggestions/{n}/accept|dismiss`、`GET /v1/sessions/{sid}/tail?bytes=`。除 `GET /v1/steward*` 外，启停 / 提问 / 设置 / 采纳都拒绝一切 job 凭据。`POST /v1/steward/ask {text}` 返回 `job_id`，管家正忙超过 90s 返回 409。

对应 MCP 工具：读 `gofer_work_list|get|requests`、`gofer_session_list|get|tail`、`gofer_list_jobs`、`gofer_get_job`、`gofer_issue_list|get`；写 `gofer_work_update`、`gofer_work_note`、`gofer_work_remind`、`gofer_work_merge_suggest`、`gofer_work_request_report`、`gofer_work_summarize`、`gofer_session_ask`、`gofer_steward_notes`、`gofer_today_list` / `gofer_today_card` / `gofer_today_advise`、`gofer_memory_findings` / `gofer_memory_suggest`。

## Worker credential allowlist

A worker's own token (`server.workers.<id>.token`, resolved with kind `worker`) is a credential for the worker protocol, not a second operator token. Inside the `/v1` group it is default-DENY: only the routes the worker process itself calls pass, everything else is `403` with `worker credentials cannot call this endpoint`.

- `GET /v1/workers/{id}/assignable` (the handler additionally requires the caller to be that worker), `GET /v1/workers/{id}/upgrade/file` (its own staged binary only), `GET /v1/xfer/{id}/content` (the source of a `put` assigned to it) and `PUT /v1/xfer/{id}/content` (the result of a `get` assigned to it).
- The three WebSocket handshakes a worker dials — `GET /v1/workers/connect`, `/v1/workers/pty-connect`, `/v1/workers/tunnel-connect` — are registered outside the `/v1` group (a WS upgrade needs a bare 401), do their own worker-token check and are unaffected by this list.
- Everything else is refused before any handler runs, including `POST /v1/jobs`, `PUT /v1/config/agents/{key}`, `POST /v1/projects`, `POST /v1/workers`, `POST /v1/workers/{id}/reload` and the read surface (a worker mirrors the job it runs over its WebSocket, never over HTTP): job state, logs, results, heartbeats, cancels and reload/policy all travel the worker's connection as frames.
- A worker (or job) credential is never an administrator: the permission model applies to a user-class caller only (see the permissions section above), and every administrative write keeps its own “a person's action” refusal.

## Schedule ownership and trigger tokens

A schedule records the caller that created it (the `caller_id` stamped into its stored request).

- `GET /v1/schedules` and `GET /v1/schedules/{id}` mask `trigger_token` for every caller that is not the schedule's owner and is not a **root administrator**: the first four characters remain, the rest becomes `*`. The owner and a root administrator see the real token; `POST /v1/schedules/{id}/rotate-token` answers under the same masking rule. A schedule whose stored request carries no caller id has no owner, so only a root administrator sees its token.
- `POST /v1/schedules/{id}/run-now` submits the stored request with the identity of the CALLER THAT ASKED (`caller_id` of the new job = that caller), so the run is attributed to the person who started it and their limits apply. The stored request keeps its creator's identity — that is what the cron loop and the webhook use (the webhook runs with no caller at all).

## Transfer (xfer) ownership

Each transfer row records the caller that initiated it (`caller_id`).

- `GET /v1/xfer` (the list) returns only the caller's own transfers unless the caller holds `xfer.manage.others` (root administrators — the permission is deliberately not seeded); the filter is applied in the query, so `limit` still means "my last N".
- `GET /v1/xfer/{id}`, `DELETE /v1/xfer/{id}` and the user download `GET /v1/xfer/{id}/content` (the result of a `get`) are for the owner or a caller holding `xfer.manage.others` — anyone else gets `403`. Job credentials are not owners: a job's own file seam (`job run --upload` / collected files) reads the staging area in-process, never over HTTP.
- The worker paths are unchanged: a worker's token may only fetch the source of a `put` assigned to it and upload the result of a `get` assigned to it.

## Session ownership and refused reports

A session belongs to the caller that registered it (`session.caller_id`): a later registration / heartbeat / reply from any other caller is `403`. That is deliberate — one caller must not take over another's terminal — but a token split (`server.callers`: the human keeps the server token, the agent / container hooks move to their own caller) makes every hook on that machine change identity at once, and the session then silently freezes at the moment of the switch. The refusal is therefore recorded and surfaced:

- `refused_caller_id` / `refused_at` on the session object (both omitted when nobody was ever refused; `GET /v1/sessions` and `GET /v1/sessions/{sid}` carry the same fields). It is the last report refused for coming from a caller other than the owner. The owner's next successful report clears it, and so does a hand-over.
- The server logs one warn per session + refused caller per 10 minutes (`session_id`, `owner`, `refused_caller`); the terminal's hook logs a line in `<config-dir>/run/hook.log` at the same cadence, naming the hand-over command.
- `POST /v1/sessions/{sid}/owner` body `{"caller_id": "<id>"}` hands the session to another caller. The session keeps its state, turns and job watches; the owner changes, the refusal note is cleared, and the response is the updated session view. The session's own `session:<sid>` grants are revoked in the same transaction: ownership is one of the things a session grant is judged by, so they stop applying anyway, and revoking them says so in the row. Gate: `session.transfer`, which belongs to the root administrators alone (it is in the 严格检查 group, independent of the governance switches) — not waived by anything, since "give my session to your token" is exactly what must stay with the operator. Worker credentials are `403` (a machine credential does not act for a person); job credentials are `403` through the SEC-01 default-deny route table (`POST /v1/sessions/*/owner` → "hand a session to another caller"). A caller the server cannot authenticate — not in `server.callers` and not the legacy token's `default` (worker ids count as unknown) — is `400`, an unknown session `404`.
- CLI: `gofer session owner <sid> <caller>`; `gofer session show` prints the owner and the last refusal.

## Session stop-wait policy (watched jobs)

`session.stop_job_wait` (`away` default | `always` | `never`) decides what a Stop hook does about the session's watched jobs (a plan bound to the session, or a job with `source_session_id`). Every session answer (`POST /v1/sessions`, `POST /v1/sessions/{sid}/heartbeat`, `GET /v1/sessions`, `GET /v1/sessions/{sid}`) carries it as `stop_job_wait`, so a hook running where no config file of its own is readable (a container) still obeys the operator; a hook whose own config sets the key uses that instead. The hook owns the decision, because it owns the keyboard reading: `away` blocks only when the relay's own rules say the person is away (`relay=on`, or `relay=auto` with the idle over `auto_relay_idle_sec`; an unreadable idle counts as present), `always` keeps the pre-knob behaviour, `never` never blocks — a released Stop is not a lost notice, the next `SessionStart` / `UserPromptSubmit` catches it up.

## Plan supervisor binding

`POST /v1/plans` / `PATCH /v1/plans/{id}` take `supervisor_session_id`: the terminal session whose agent supervises the plan (plan auto-dispatch and completion notices go there). Both also take `notify` (gofer-3db4): `on` makes every job the plan dispatches tell the supervisor session, through the session messenger, that it ended — the same notice `POST /v1/jobs`'s own `notify` sends, decided once for the whole plan. `off` (the default, and what an omitted PATCH field leaves in place) keeps each job's own setting. A plan with `notify: on` and no `supervisor_session_id` notifies nobody: the create / PATCH response says so in `warnings`, and `GET /v1/plans/{id}` always reports the switch as `notify: on|off`. A supervisor session that has since been cleaned up does not block the chain either: its jobs dispatch without the source claim and record `job.notify_dropped`.

- The session must exist and belong to the plan's project. It must be owned by the requesting caller — `400` otherwise — and the error names both callers (`supervisor session <sid> belongs to caller A, this request is caller B`), says whether the session is simply not registered, and points at `gofer session owner` for the hand-over. This is the message `gofer plan create`'s auto-bind prints when it refuses to bind the session the command runs in.
- `PATCH /v1/plans/{id}` requires the plan owner **or** a caller holding `plan.supervise.others` (root administrators), and such a caller may bind any caller's session of the plan's project — after a token split the person on the web is not the caller that created the plan (an agent usually is), and without this the binding could not be fixed from the console at all. A caller with neither ownership nor the permission is still `403` (`plan owner required`).

## Terminal questions on the web (AskUserQuestion)

- A Claude Code `AskUserQuestion` relayed by the PreToolUse hook is a session permission card (`POST /v1/sessions/{sid}/permissions`) whose body carries `questions: [{question, header?, options?: [{label, description?}], multiSelect?}]`; the decision view's `permission.questions` has them, its title ends in 「有问题要你回答」 and the session's last message is 「等你回答：<first question>」.
- Answer with `POST /v1/sessions/{sid}/permissions/{id}/answer` body `{"answer": "answers:<JSON object question → answer>"}`; every question needs a non-empty answer (a label, comma-separated labels for multiSelect, or free text); allow / deny are refused for such a card. The hook returns the answers to Claude Code as the tool's `updatedInput.answers`.
- Who may answer: the session's owner (the caller the session registered with — with a token split that is the HOOK's identity), anyone on an owner-less session, or a caller holding `session.permission.answer` (a root administrator by default; the person at the web console is usually a different caller id from the session's own). Opening and resolving a prompt stay the hook's own action: the session's owner, or a root administrator. Worker and job credentials are always `403`, and the deprecated `can_answer` grants nothing here.
- Web messages to a session are delivered by a messenger running as Claude Code session `gofer-web`.

## Web messages to a session (outbox)

- `POST /v1/sessions/{sid}/messages` body `{message}` (`text` is accepted too) hands one message to the session and returns the outbox record `{id, session_id, text, operator?, status, channel?, job_id?, error?, created_at, updated_at}`. `GET /v1/sessions/{sid}/outbox?limit=&before=` returns `{messages, has_more, next_before}`, newest state per message in send order (permission: `session.reply.others`, the same as reading what the session has not sent yet).
- `status` walks `queued` → `delivering` → `delivered`. `delivering` (gofer-qzv5) means the internal delivery job had STARTED but had not finished when the server's wait window elapsed (an update waiting for idle used to hold it for minutes): the text is on its way, `job_id` is that job, and the record is written back to `delivered` / `failed` at the job's terminal state — the terminal hook does it, and the outbox read path settles anything the hook missed (a write-back lost with a restart), so a record never stays `delivering` for ever. `channel` is the delivery path the text took (`turn` / `tmux` / `command` / `messenger`).
- Off that path there are two FAILED states, and they differ in whether a retry is safe: `undelivered` — the text provably never reached the session (the delivery ladder never made an attempt, or its internal job had not started when gofer gave up waiting and was cancelled); `error` names the reason, e.g. `not_delivered:paused_for_update: job … 未启动（升级等待（新任务暂停启动）…），已取消：未送达`. `failed` — a delivery WAS attempted and its outcome is unknown (a `deliver_command` that exited non-zero may still have handed the text over): `error` starts with the reason code and carries the job's own state and output (`job <id> 状态 failed exit=5：…`). Only `undelivered` may be resent.
- `POST /v1/sessions/{sid}/deliver` body `{text, allow_takeover?}` is the low-level form of the same ladder and answers `{path, job_id?, decision_id?, delivering?}`. `delivering: true` means the text was handed to an internal job that had begun but had not finished (`path` is `tmux` or `command`, and there is no audit row yet). Its `error.code` carries `inject_failed:<…>` / `deliver_failed:<…>` as before, plus `not_delivered:<job status>` for a job that never started — the body says which status held it, instead of an exit code the job never produced.

## Issues and tracker

- `GET /v1/issues`（`project` `tracker_id` `repo`（rel_path）`status` `type` `tag`（可重复）`q` `limit` 默认 50 最大 200，返回 `{issues,total,truncated}`）与 `GET /v1/issues/{id}?tracker_id=`（同 id 出现在多个仓库时要带 `tracker_id`，否则 409 列出候选）：只读 server 端镜像。
- `POST /v1/tracker/issues/batch` body `{tracker_id, ids[], set:{status?, close_reason?, add_tags?}}`，返回 `{results:[{id,ok,error?}], ok, failed}`：`close_reason` 只能配 `status: closed`，`ids` 最多 2000；逐条执行，个别失败逐条列出、成功的照常生效，变更在下次 `gofer repo sync` 拉回本地 jsonl。
- `POST /v1/tracker/repos/{tracker_id}/sync`：server 派一个隐藏的内部 exec job（tag `tracker-sync`）在该仓库目录执行 `gofer repo sync`，返回 202 `{job_id, runner, cwd, …}`；只允许人凭据。runner 取该仓库最近一次由 job 推送时记录的来源 runner（`GET /v1/tracker/repos` 的 `source_runner`），没有则用项目默认 runner；仓库目录无法落在项目内、或仓库未归属任何项目时 409；项目需 `allow_exec: true`。
- `POST /v1/jobs/{id}/findings/issue` body `{text, priority?}`（priority 0–4，默认 2）：把该 job 汇报里的一条「发现但不碰」建成 issue，写进 server 镜像（标题为 text 折叠空白，描述 `discovered in job <id>`，标签 `discovered`），下次 `gofer repo sync` 进仓库。tracker 取 job 的 `tracker_id`，没有则取该项目唯一登记的 tracker；都没有、项目登记了多个、或 tracker 尚未记录 id 前缀时 422（`detail` 说明原因）。同一 job 的同一条再请求返回已建的 issue（200，`created:false`），新建 201；返回 `{tracker_id, issue, created}`。只允许人凭据，worker / job 凭据 403。
- `POST /v1/tracker/sync`：job 凭据只能同步自己 job 关联的 `tracker_id`，其它 403。
- `POST /v1/tracker/sync` 可带 `client_host`（同步方的主机名）：同步成功后 server 按 (tracker_id, client_host, caller) 记一行最近同步（没带主机名记 `unknown`；job 凭据统一记为 caller `job`）。`GET /v1/tracker/repos/{tracker_id}/clients` 返回 `{"clients":[{tracker_id, client_host, caller, last_sync_at, summary}]}`（最近的在前，`tracker_id` 可为 `global`）；tracker 改名 / 删除时这些行随之移动 / 删除。
- `GET /v1/plans/{id}` 的 `todos[].jobs[]` 带可选的 `delivery_state`（pending / merged / pushed / pr_opened / failed / skipped）与 `pr_url`，没有交付记录时省略。
- 全局记忆是保留的 `tracker_id: "global"`（只有 memories）：`/v1/memories/global/...` 与它是同一份数据，每次写入是一个新 rev。人的凭据可推可拉；job 凭据只能拉（带 memories 推送为 403）；推 issues 为 400。它不出现在 `GET /v1/tracker/repos`，改名与删除返回 400。
- `POST /v1/tracker/repos/{old}/rename` body `{"new_tracker_id": …}`：改 tracker id（server 在一个事务里改 tracker_repos / issues / memories）。幂等：旧 id 不存在返回 200 空操作，新旧并存 409，新 id 不等于派生值（`tracker-` + sha256(旧 id) 前 10 位十六进制）400；允许人凭据，或 job 凭据且该 job 关联的 tracker 就是旧 id。
- `DELETE /v1/tracker/repos/{tracker_id}`：从 server 删除一个仓库的镜像（repo 记录、全部 issue / 记忆含墓碑、记忆建议，一个事务）；只允许人凭据且需 `tracker.delete`（默认部署里全体用户类调用方都有；关上 `governance.require_admin_capability` 之外的写法就是只有根管理员），job 凭据一律 403；不存在 404；返回 `{tracker_id, deleted:{repo, issues, memories, suggestions}}`。仓库本地的 `.gofer/tracker` 不受影响，之后从它 `repo sync` 会重建镜像。
- `repo sync` 的 rev 协议：推送每条记录时带上本机最后见过的 server rev；rev 过期时 server 不写入，在响应 `conflicts` 里回传当前记录，客户端三方合并后在同一次 sync 内重推（最多 3 次请求，未落定的列为 `unresolved`）。
