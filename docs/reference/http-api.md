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

