# HTTP API overview

> Moved from the README (2026-10-10). Endpoint groups of the `/v1` API; the handlers in `internal/httpapi` are authoritative.


`/health` is unauthenticated; everything under `/v1/*` requires `Authorization: Bearer <token>`. Error body: `{"error":"…","detail":"…"}`.

| group | main endpoints |
|---|---|
| projects / agents / roster | `GET/POST /v1/projects`, `GET/PUT/DELETE /v1/projects/{key}`, `GET /v1/agents`, `GET /v1/runners`, `GET /v1/meta`, `GET /v1/metrics` |
| templates | `GET /v1/projects/{key}/templates`, `GET /v1/projects/{key}/templates/{name}?var=k=v` (read-only; the detail endpoint returns the server's render of it) |
| jobs | `POST/GET /v1/jobs`, `GET /v1/jobs/{id}[?include=events,comments,deliveries,retries,wakeups,pty_sessions,artifacts,session_jobs]`, `/logs/{stdout,stderr}`, `/stream` (日志 SSE), `/acp/stream`（归一化 ACP SSE）, `/events`, `/diff`, `/artifacts`, `POST …/cancel`, `POST …/resume`, `POST/GET …/wakeups`, `GET/PATCH/DELETE /v1/wakeups/{wid}`, `GET/DELETE …/worktree`, `POST …/worktree/merge`, `POST …/attach-ticket`, `GET …/pty/sessions` |
| interactions | `POST/GET /v1/jobs/{id}/interactions`, `POST …/{iid}/answer`, `POST …/{iid}/punt`, `GET /v1/interactions` |
| workbench | `GET /v1/workbench/threads?project=&status=&q=&since=`, `PATCH /v1/workbench/threads/{s:\|j:\|r:…}`, `POST …/turn` |
| plans / decisions | `POST/GET /v1/plans`, `GET /v1/plans/{id}`, `POST …/todos`, `POST …/jobs`, `POST …/run\|pause\|resume`, `POST/GET /v1/decisions`, `POST /v1/decisions/{id}/answer` |
| session relay | `GET/POST /v1/sessions`, `POST /v1/sessions/{sid}/heartbeat`, `…/relay`, `…/say`, `…/deliver`, `…/release-takeover`, `…/turns` |
| workflows / schedules | `GET /v1/workflow-templates[/{name}]`, `POST /v1/workflow-templates/{name}/render`, `POST/GET /v1/workflows`, `…/{id}/cancel`, `…/{id}/pick`, `…/events`, `…/export`; `POST/GET /v1/schedules`, `…/enable`, `…/disable`, `…/run-now`, `…/rotate-token`, `POST /v1/schedules/{id}/trigger?token=…` (unauthenticated, schedule token) |
| browser push | `POST /v1/ws-ticket` (user callers only; job/worker credentials get 403) → `GET /v1/ws?ticket=…` (WebSocket, unauthenticated route that consumes the one-time ticket) |
| workers / tunnels | `GET /v1/workers/connect` (WS), `/v1/workers/pty-connect`, `POST /v1/workers/{id}/reload`, `GET /v1/tunnels`, `/v1/tunnels/connect`, `/v1/workers/tunnel-connect` |

`POST /v1/jobs` body (snake_case): `project_key`, `agent`, `runner`, `prompt` / `cmd`, `cwd`, `timeout_sec`, `title`, `worker_id` / `worker_labels`, `interactive`, `worktree` / `worktree_base`, `plan_id`, `tags`, `sync` / `wait_timeout_sec`, `request_id` (idempotency key), `template` / `vars` (a server-rendered task book).


## Job detail aggregation: `include`

`GET /v1/jobs/{id}?include=a,b,c` 一次取回详情页要的附属数据；缺省 `include` 为空，等于普通 job 快照（CLI / MCP 不受影响）。可选项：

- `events`：最近 200 条升序，带 `events_last_seq` / `events_truncated`，更早的用 `&before=<seq>`。
- `comments`：最新 100 条 + `comments_total`。
- `deliveries`、`retries`、`wakeups`。
- `pty_sessions`：无 attach 权限时省略，并在 `include_errors.pty_sessions` 写 `forbidden`。
- `artifacts`：仅终态 job 内联，最多 200 + `artifacts_total`。
- `session_jobs`：同 session 的 job，最多 50。

每项独立容错：失败项写进 `include_errors`，整体仍 200；未知 include 名是 400。日志正文、diff、`/request` 不在其中。

## Jobs held for approval

- Submit: `POST /v1/jobs` with `hold: true` (optional `hold_reason`, `hold_timeout_sec`) returns **202** with `X-Gofer-Async: 1`; the body is the JobResult plus `approve_url` (empty when `server.web_base_url` is not set). The job stays in `awaiting_approval` and is not dispatched.
- Approve: `POST /v1/jobs/{id}/approve`, body `{note?}`; returns the started JobResult (usually `queued`).
- Reject: `POST /v1/jobs/{id}/reject`, body `{note?}` or `{reason?}` (optional for a held job; `resume: true` is a 400); the job ends `cancelled` with `error="hold rejected by <who>[: <note>]"`. Withdraw: `POST /v1/jobs/{id}/cancel`.
- Errors: 409 (no longer awaiting approval, or the request changed while held), 503 (server draining for an upgrade), 404, 403 (worker token, job credential, or no `can_answer` when `governance.require_answer_capability` is on).
- `hold` on a job: `{reason, timeout_sec, expires_at, origin, command[] | prompt_preview, decision?, decided_by?, decided_at?, note?}`; `decision` is `approved | rejected | expired | cancelled`; an expired hold ends `cancelled` with `error="hold expired"`.
- Events: `job.awaiting_approval{reason,timeout_sec,expires_at,origin}`, `job.hold_approved{by,note?}`, `job.hold_rejected{by,note?}`, `job.hold_expired{expires_at}`. `/v1/today` lists a card of kind `approval` (actions `approve`, `reject` with `optional_text`, `open`); `/v1/stats` `by_status` has an `awaiting_approval` key.

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

Steward 相关 REST：`GET /v1/steward`（状态 + 有效设置）、`POST /v1/steward/start|stop|restart|ask|review`、`GET|PUT /v1/steward/notes`（PUT 带 `version`，过期 409 + `current`；`?version=N` 看历史，`?history=1` 列版本）、`POST /v1/steward/review-summary`、`PUT /v1/config/steward`、`GET /v1/work-items/merge-suggestions`、`POST /v1/work-items/{id}/merge-suggestions {source_id, reason}`、`POST /v1/work-items/merge-suggestions/{n}/accept|dismiss`、`GET /v1/sessions/{sid}/tail?bytes=`。除 `GET /v1/steward*` 外，启停 / 提问 / 设置 / 采纳都拒绝一切 job 凭据。`POST /v1/steward/ask {text}` 返回 `job_id`，管家正忙超过 90s 返回 409。

对应 MCP 工具：读 `gofer_work_list|get|requests`、`gofer_session_list|get|tail`、`gofer_list_jobs`、`gofer_get_job`、`gofer_issue_list|get`；写 `gofer_work_update`、`gofer_work_note`、`gofer_work_remind`、`gofer_work_merge_suggest`、`gofer_work_request_report`、`gofer_work_summarize`、`gofer_session_ask`、`gofer_steward_notes`、`gofer_today_list` / `gofer_today_card` / `gofer_today_advise`、`gofer_memory_findings` / `gofer_memory_suggest`。

## Issues and tracker

- `GET /v1/issues`（`project` `tracker_id` `repo`（rel_path）`status` `type` `tag`（可重复）`q` `limit` 默认 50 最大 200，返回 `{issues,total,truncated}`）与 `GET /v1/issues/{id}?tracker_id=`（同 id 出现在多个仓库时要带 `tracker_id`，否则 409 列出候选）：只读 server 端镜像。
- `POST /v1/tracker/issues/batch` body `{tracker_id, ids[], set:{status?, close_reason?, add_tags?}}`，返回 `{results:[{id,ok,error?}], ok, failed}`：`close_reason` 只能配 `status: closed`，`ids` 最多 2000；逐条执行，个别失败逐条列出、成功的照常生效，变更在下次 `gofer repo sync` 拉回本地 jsonl。
- `POST /v1/tracker/repos/{tracker_id}/sync`：server 派一个隐藏的内部 exec job（tag `tracker-sync`）在该仓库目录执行 `gofer repo sync`，返回 202 `{job_id, runner, cwd, …}`；只允许人凭据。runner 取该仓库最近一次由 job 推送时记录的来源 runner（`GET /v1/tracker/repos` 的 `source_runner`），没有则用项目默认 runner；仓库目录无法落在项目内、或仓库未归属任何项目时 409；项目需 `allow_exec: true`。
- `POST /v1/tracker/sync`：job 凭据只能同步自己 job 关联的 `tracker_id`，其它 403。
- `POST /v1/tracker/repos/{old}/rename` body `{"new_tracker_id": …}`：改 tracker id（server 在一个事务里改 tracker_repos / issues / memories）。幂等：旧 id 不存在返回 200 空操作，新旧并存 409，新 id 不等于派生值（`tracker-` + sha256(旧 id) 前 10 位十六进制）400；允许人凭据，或 job 凭据且该 job 关联的 tracker 就是旧 id。
- `repo sync` 的 rev 协议：推送每条记录时带上本机最后见过的 server rev；rev 过期时 server 不写入，在响应 `conflicts` 里回传当前记录，客户端三方合并后在同一次 sync 内重推（最多 3 次请求，未落定的列为 `unresolved`）。
