# gofer

English · [中文](README.zh-CN.md)

gofer bridges configurable **CLI agents** (`codex` / `claude` / `omp` / `opencode` / any command) and registered **projects** into one **asynchronous job control plane**: submit `{project, agent, prompt or command, cwd}`, and gofer runs it inside that project's real working directory (locally, on a remote worker, or on a peer), streams status, logs, exit code and results back, and lets you submit and observe through **CLI / HTTP / MCP / Web console**.

> A gofer is "the one who runs errands": hand a task to an agent → run it in the target project → bring back logs and results. The `gofer`↔`gopher` pun is intentional.

## Contents

- [Features](#features) · [Architecture](#architecture) · [Install / build](#install--build) · [Quick start](#quick-start)
- [Core concepts](#core-concepts) · [Submitting jobs](#submitting-jobs) · [Parallel jobs: --worktree](#parallel-jobs---worktree) · [Continuing a job: resume](#continuing-an-interrupted-job-job-resume)
- [Remote execution and workers](#remote-execution-and-workers) · [Reconnect recovery](#reconnect-recovery-recovering) · [Tunnels](#tunnels)
- [Human in the loop](#human-in-the-loop-interactions-plans-session-relay) · [Logging and observability](#logging-and-observability)
- [Configuration](#configuration) · [CLI](#cli-reference) · [MCP](#mcp) · [Web console](#web-console) · [HTTP API](#http-api)
- [Deployment](#deployment) · [Security](#security-notes) · [History](#history)

## Features

- **One control plane, four entry points**: CLI (`gofer job …`), HTTP (`/v1/*`), MCP (stdio, 23 `gofer_*` tools) and a Web console (board, job detail, live logs, runners, plans, sessions, new-job form) — all on the same `job.Service`.
- **Many agents, one key for two modes**: `type: cli-agent` renders an argv template (`args` for batch runs, `interactive_args` for pty sessions); `type: exec` runs argv verbatim. Agents that are not installed are merely marked `unavailable`.
- **Per-project governance**: `host_path` / `container_path`, allowed agents and runners, `allow_exec`, `allow_interactive`, concurrency cap, timeout ceiling, default worktree.
- **Three execution places (runners)**: `local` (in-process), `peer-http` (forward to another gofer), `worker` (remote executor over WebSocket, label-based scheduling). Remote logs, status and interactions are mirrored back transparently.
- **Reconnect recovery**: when a worker link blips or the server restarts, in-flight jobs enter `recovering`; the same worker process reconnecting within the window resumes log streaming and delivers the result — jobs no longer fail on the first hiccup.
- **Managed worktrees**: `--worktree` runs each job in its own git worktree so parallel agents never step on each other.
- **Same-directory serialization and stall detection**: two writable agent jobs never edit one working directory at once — the second parks in the non-terminal `waiting_dir` (queued, cancellable, `job.waiting_dir {holder_job}` names who it waits for) until the first releases it; exec and read-only jobs are shared by default and `--exclusive-dir` / `--shared-dir` / `server.dir_lock: false` override either way (`agents.<k>.max_concurrent` caps one agent's parallelism the same way). A job that produces no output at all for `server.stall_timeout_sec` (default 900s; off for exec, per-agent overridable, `--stall-timeout` / `--no-stall` per job) is killed as `failed: stalled: no output for Ns` and classified **transient**, so the resume/failover chain takes over instead of waiting out the deadline.
- **Resume**: `job resume` lets codex/claude continue from where an interrupted job stopped, with its own session context.
  When a configured transient error matches, the server automatically resumes once (`server.auto_resume_max`; set it to `0` to disable).
  If that continuation also dies (or there is nothing to continue), the job is handed to the next agent instead — see *Failover to another agent* below.
- **Tunnels**: `gofer tunnel` forwards TCP/UDP ports through a worker under an allowlist (e.g. container → shop-floor PLC/HMI); all three ends log the same `tunnel_id` with per-stage latencies.
- **Human in the loop**: mid-run questions (`pending_interaction`), `plan` + todo progress boards, blocking `ask_human` decisions, and a terminal session relay that arms itself when you walk away and injects your web/phone reply into the very same session.
- **Failover to another agent**: when codex dies of a provider error (`at capacity` / `stream disconnected` / a sandbox that never started) and cannot continue the job itself, the server hands the SAME work to the next candidate as an ordinary job — `fallback_agents: [omp]` on the agent or `agent_fallbacks: {codex: [omp]}` per project (one job can override with `job run --fallback omp` / `--no-fallback`). The takeover keeps the worktree, plan/todo, verify step and caller, gets a prompt that says what happened and to finish only the remaining work, and the source row records the link (`fell_back_to`) plus `job.fell_back` instead of a terminal failure. Health is aggregated per agent (`failure_class` on every failed job): `gofer agent status` shows who is degraded, `gofer agent probe <key>` submits a real one-line OK job to check NOW, and the web Agents page badges it (with `server.agent_fallback.pre_dispatch: true` a degraded agent is even replaced before dispatch — off by default).
- **Task-book templates**: the constraints you paste into every task book live in a file the SERVER renders — `<project>/.gofer/templates/<name>.md` or the global `<config-dir>/templates/<name>.md` (YAML frontmatter = job defaults + variable declarations, body = prompt with `{{var}}`, `{{include: sibling.md}}`, `{{project}}`/`{{cwd}}`/`{{date}}`/`{{head}}`). Submit with `job run -t <name> --var k=v …`, inspect with `gofer template ls|show`, or pick one in the web form (variables + rendered preview included). A flag beats the template default beats the project default; `request_json` keeps the RENDERED prompt beside the template/vars, so a rerun never re-renders. See *Task-book templates* below.
- **Checklist linkage**: `job run --todo <todo-id>` turns that plan item `doing` and writes the outcome back into its note when the job ends — status plus the commits it produced (`<job-id> ✓ 3 commits: …`), or the failure reason. A todo can also dispatch itself: `ready` + an assignee (`plan set-todo <id> --assign omp --status ready`) starts the job for it, so a plan of assigned items runs itself one step at a time — and since PLAN-03 the items can be **chained**: `plan add-todo … --after prev` makes an item wait for the one before it, `plan run <plan>` queues the ready roots, and each finished item queues the next, so a whole multi-step task (ending in an `--assign exec --cmd '<build/test>'` verification step) runs itself. A failing chain item parks the plan (`status=blocked` + a `plan.blocked` notification) until a human re-runs or skips it. The commits themselves are captured on every job (`base_sha` → `git log base..HEAD`) and shown by `job show` / the job detail page.
- **Supervision-aware relay**: a session whose authenticated caller currently has jobs in flight no longer arms the automatic session relay ("you walked away" while you are actually watching jobs). `agent_sessions.caller_id` makes that decidable; only `relay_mode: auto` is affected.
- **Verification, not vibes**: an agent's report is not acceptance — add `job run --verify 'go test ./...'` (or a project `verify:` default) and the check runs on the machine that did the work, right after the agent, in the job's own cwd/env. A non-zero exit fails the job (or parks it in `needs_review`), the output lands in the job's stderr log with its banners, and a worker-side step reports its result back through the same outcome channel. Remote jobs also mirror their approval-gate and verify events up to the hub, so notifications and audit see them.
- **Scheduling and orchestration**: `schedule` for cron jobs (each one can also expose an external webhook trigger — `schedule add … --webhook` mints its own `trigger_token`, `POST /v1/schedules/{id}/trigger?token=…` runs it with no gofer bearer, 10s rate-limited), `workflow` for dependent multi-step chains (fan-out / join / retries).
- **Usage and cost**: agents that report their own tokens land on the job (`jobs.usage_json`: `in/out/cache/total` + `cost_usd` + which parser produced it) — read from omp/claude ndjson, codex `exec` stderr and acp `usage_update`. `job show` prints one `usage:` line, the job detail page has a block, and `/v1/stats` + the Home card aggregate 24h/7d per agent. Best-effort by design: an agent that reports nothing shows `-`, never `0`.
- **Worker events reach the hub**: approval-gate and verify events of jobs running on a worker (`job.permission_requested|answered|timed_out`, `job.verify_started|finished`) are mirrored into the hub's job events (deduplicated), so notifications and audits see remote jobs too.
- **Observable and auditable**: JSONL file logs (rotation, redaction), `/v1/runners` health roster, SSE live streams, `caller_id` / `worker_id` persisted, retention pruning; SQLite (pure Go) for metadata.
- **Windows friendly**: `scripts/start.ps1` runs the server as a logon scheduled task inside the desktop session (crash-restarting watchdog, one-shot `upgrade`), ConPTY-backed interactive sessions.

## Architecture

```txt
  entry points                     control plane (serve)            execution (runner)         agent
┌───────────────┐           ┌────────────────────────┐      ┌─ local  (in-process) ─┐
│ CLI  gofer job │──HTTP────▶│  /v1/*   job.Service    │─────▶│ peer-http (another gofer) │──▶ cli-agent
│ HTTP /v1/*     │           │  registry: project/agent │      │ worker (remote over WS) │     (codex/claude/…)
│ MCP  stdio     │           │  runners + ws hub        │      └────────┬───────────┘      or exec(argv)
│ Web  console   │           │  jobstore(SQLite)+logs   │◀──── mirrored logs/status/interactions ─┘
└───────────────┘           └────────────────────────┘
   Authorization: Bearer <token> (except /health)        results: <host_path>/tmp/gofer/<job_id>/ + DB
```

## Install / build

Go 1.25+ (plus Node + pnpm if you want the Web console).

```bash
make build            # current platform → dist/gofer (upx-compressed by default; without upx use go build below)
go build -o dist/gofer ./cmd/gofer
make web build        # build the console and embed it into the binary
make build-all        # linux/darwin/windows × amd64/arm64
```

## Quick start

```bash
# 1. Register a project (host-path must be an existing absolute directory)
gofer project add workspace \
  --host-path /work/projects/workspace --container-path /workspace \
  --default-agent codex --allow-agent codex --allow-agent claude --allow-agent exec \
  --allow-runner local --allow-exec

# 2. Start the server (token from env)
export GOFER_TOKEN=dev-token
gofer serve --addr 0.0.0.0:8765

# 3. An exec job (argv after --); --sync makes the server wait for the terminal state
gofer job run -p workspace -a exec --sync -- go version
# → status=done exit_code=0

# 4. A cli-agent job; read its logs
gofer job run -p workspace -a codex --prompt "Summarise the failing tests in this directory" --wait
gofer job logs <id> --stream stdout

# 5. Open http://<addr>/ in a browser and paste the token
```

A pure client (for example inside a container that only submits work) needs no yaml at all:

```bash
gofer init client      # writes <config-dir>/.env: GOFER_SERVER_ADDR / GOFER_SERVER_TOKEN / GOFER_RUN_MODE=client
gofer job list         # fill in the address and token and you are done
```

## Core concepts

- **project**: a real directory work can run in. Fields: `host_path` / `container_path` / `default_agent` / `allowed_agents` / `agent_fallbacks` (per-project fallback candidates, keyed by the failing agent) / `allowed_runners` / `allow_exec` / `allow_interactive` (the project-level switch for pty/interactive jobs, off by default, and the **only** interactive gate on the project side) / `max_concurrent_jobs` / `max_timeout_sec` / `worktree_default` / `verify` + `verify_timeout_sec` (the project's default verification step and its own deadline).
- **agent**: how to run. A `cli-agent` renders `command` + `args` (placeholders `{{prompt}}` `{{cwd}}` `{{job_id}}` `{{result_dir}}`, substituted per argv element, never through a shell); add `interactive_args` and **the same key serves both batch and pty runs** (`[]` = bare TUI launch; must not contain `{{prompt}}`). `exec` runs the request's `cmd` argv verbatim (needs the project's `allow_exec`).
- **runner**: where to run. `local` (child process of this server) / `peer-http` (forward to another gofer) / `worker` (remote executor connected over WebSocket).
- **job lifecycle**: `queued → running → done | failed | cancelled | timeout`; a job queued behind the same-directory lock is `queued → waiting_dir → running`; a mid-run question is `running → pending_interaction → running`; a worker link loss is `running → recovering → running | failed`.

## Submitting jobs

One `JobRequest`, four entry points, two timings:

```bash
# CLI — --prompt for cli-agents, -- argv for exec
gofer job run -p workspace -a codex --prompt "Review the changes and list the risks"
gofer job run -p workspace -a exec  --sync -- mvn -q test     # --sync: server waits for the terminal state
gofer job run -f task.md                                      # md+yaml: frontmatter = parameters, body = prompt
gofer job run -p workspace -t impl-batch --var tasks="add a foo subcommand" \
                                             --prompt "extra: do not touch web/"   # -t: server-rendered task book
gofer job run -p workspace -a codex --read-only --prompt "Review only: list the risks, change nothing"
                                                              # --read-only: the agent cannot write (cli sandbox
                                                              # args / acp-agent session/set_mode; inherited by resume)
gofer job run -p workspace -a codex --review --prompt "Refactor the parser"   # a human must accept the result
gofer job accept <job-id> [--note "looks good"]              # needs_review -> done
gofer job reject <job-id> --note "why it is refused" [--resume]  # -> rejected; --resume continues with the note
```

```markdown
<!-- task.md -->
---
project_key: workspace
agent: codex
runner: worker
worker_labels: [gpu]      # or worker_id: w-01
worktree: true
---
Generate batch scripts under scripts/ that read the task list from config.yaml … (the body is the prompt)
```

- **Templates**: `job run -t <name> [--var k=v …]` renders a server-side task book into the prompt (`--prompt` is appended); `gofer template ls|show` lists and previews them (the preview is the server's own render, includes expanded).
- **HTTP**: `POST /v1/jobs` (JSON or `text/markdown`). `"sync": true` or `?wait=1` waits (terminal state → `200` with the full result; past the server cap → `202` + `X-Gofer-Async: 1` + id).
- **MCP**: `gofer_run_job` and friends (see [MCP](#mcp)).
- **Web**: the "+ New job" form.
- **Sync vs async**: async by default (returns the `id` immediately; follow with `job watch <id>`); `--sync` waits server-side (30s cap by default, `--wait-timeout` adjusts it, then falls back to async).
- **Timeout ceiling**: a `--timeout` above `server.max_job_timeout_sec` (default 3600) or the project's `max_timeout_sec` is **clamped, never silently**: the CLI prints `warning: --timeout <requested>s exceeds the project ceiling (<max>s)…` and the response carries `requested_timeout_sec` / `timeout_clamped`.

### Task-book templates

Every batch of work repeats the same preamble ("no push, LF only, one commit per task, paste the raw
`go test` lines"). Put it in a template once and submit with variables:

```bash
gofer template ls [-p workspace]                       # project .gofer/templates first, then <config-dir>/templates
gofer template show impl-batch --var tasks="add a foo subcommand"   # source path + variables + rendered preview
gofer job run -p workspace -t impl-batch --var tasks="add a foo subcommand" --var base=main
```

```markdown
<!-- <project>/.gofer/templates/impl-batch.md -->
---
desc: one implementation batch
agent: omp
timeout_sec: 3600
verify: [go, test, ./...]
vars:
  tasks: {required: true, desc: the batch body}
  base: {default: main}
---
# Batch

{{include: common.md}}          <!-- one level, same directory only -->

## Tasks

{{tasks}}
```

- **Precedence** is explicit flag > template default > project default: the frontmatter may preset
  `agent`, `runner`, `timeout_sec`, `tags`, `verify`, `verify_timeout_sec`, `review`, `read_only`,
  `worktree`, `fallback_agents` — and only fills what the request left unset. A missing required
  variable is a 400 naming it.
- **Where they live**: `<project host_path>/.gofer/templates/<name>.md` (wins on a name clash) or the
  server's `<config-dir>/templates/<name>.md`. Templates are read by the server, so a remote console or
  CLI still sees the same list; a worker-only project uses the global directory.
- **Audit**: `request_json` stores the RENDERED prompt next to `template`/`vars`, and replays (rerun /
  rebuild / failover) replay that prompt instead of rendering the task book a second time.
- Ready-made examples: `docs/examples/templates/` (`cp docs/examples/templates/*.md ~/.config/gofer/templates/`).

### Parallel jobs: `--worktree`

Several jobs editing the same checkout collide (stale `.git/index.lock`, overwritten changes). `--worktree` gives each job **its own git worktree**:

```bash
gofer job run -p workspace -a codex --worktree [--worktree-base v1.2.0] --prompt "Fix the 3 issues, one commit each"
gofer job worktree ls [-p workspace]                        # branch / commits ahead / dirty / merged
gofer job worktree rm <job-id> [--force] [--delete-branch]  # refuses a dirty tree without --force; keeps the branch by default
```

- Location `<repo top>/tmp/gofer/wt/<job-id>`, branch `gofer/<job-id>` (base defaults to the current HEAD); `--cwd sub` maps to `<worktree>/sub`; nested repositories use the nearest git top-level of the cwd; the worktree is created on the **executing machine**.
- Environment `GOFER_WORKTREE` / `GOFER_WORKTREE_BRANCH` / `GOFER_WORKTREE_BASE`; the result records `worktree_path` / `worktree_branch` / `worktree_base_sha` / `worktree_head_sha` / `commits_ahead`; `changes.diff` has a `committed (base..HEAD)` and an `uncommitted` section.
- **Kept by default** when the job ends (the commits on the branch are the deliverable); retention only removes worktrees that are clean *and* merged. `worktree_default: true` on a project turns it on for every job. See `docs/runbook/parallel-jobs-with-worktree.md`.

### Continuing an interrupted job: `job resume`

When a provider capacity error, a network blip or a timeout kills a job halfway, do not resubmit the whole task (a new session re-reads all the context):

```bash
gofer job resume <source job-id> --prompt "The previous run was interrupted by <reason>. Check git status/log to see how far you got, finish only the remaining items, do not redo committed work."
```

Requirements: the source job is terminal, it captured a `session_id` (visible in `job show`; codex/omp via output capture — the ndjson session row or the TUI exit banner — and claude via `--session-id` injection), the agent has a resume template (built in for claude/codex/omp; **every other cli-agent gets the generic fallback** — a `--resume`-style capture regex and `--resume {{session_id}}` argv — so a newly declared agent is resumable without any `session_*` config; override with `session_capture` / `session_resume` when its syntax differs), same runner. `rerun`, by contrast, resubmits the same request as a fresh session. See `docs/runbook/2026-09-22-cli-agent-onboarding-runbook.md`.

### Human review: `needs_review`, `job accept` / `job reject`

"Agent finished" is not "the work is accepted". With `job run --review` — or a project's `require_review: true`, or a workflow step's `review: true|false` override — a job whose agent finishes **normally** stops in the non-terminal `needs_review` state instead of `done`, and stays there until a person rules:

```bash
gofer job accept <job-id> [--note "looks good"]              # -> done (job.reviewed{accepted} + job.terminal{done})
gofer job reject <job-id> --note "tests are red"             # -> rejected (terminal; the note is required)
gofer job reject <job-id> --note "tests are red" --resume    # also start a continuation with the note as its prompt
gofer job list --status needs_review                         # what is waiting on a human
gofer job review <job-id> [--tail 60] [--diff]               # the acceptance material on ONE screen: status / review / verify / commits / usage / diff --stat + the report tail (--diff appends the full patch)
```

- A failure (`failed`/`cancelled`/`timeout`) never enters review; only a normal completion does. `rejected` is terminal, is aggregated as a failure by a workflow step, and is **never** retried or auto-continued — only a person's `--resume` continues the work.
- **Only a person can accept**: `POST /v1/jobs/{id}/accept|reject` refuses a worker token with 403 (and requires `can_answer` when `governance.require_answer_capability` is on); MCP exposes `gofer_reject_job` and deliberately **no** accept tool. Accepting happens on the web job page's review card, in the CLI, or over HTTP.
- `job cancel` refuses a `needs_review` job (409) — there is nothing left to cancel, use `reject`. `job resume` likewise demands the review be settled first.
- Audit fields `require_review` / `reviewed_by` / `reviewed_at` / `review_note` are persisted and shown by `job show` and the web page. `job.needs_review` is a **default IM notification event** (like `job.terminal`); `job.reviewed` can be subscribed explicitly.

## Remote execution and workers

Logs, status and mid-run interactions of remote jobs are mirrored back to the local job; every read path stays the same.

```yaml
# A: peer-http — forward to another gofer
runners:
  docker-peer: { type: peer-http, base_url: http://127.0.0.1:8766, token_env: PEER_TOKEN }

# B: ws-worker — a remote executor dials into this hub
server:
  workers:                         # registered workers (worker_id ↔ token binding + scheduling labels)
    w-gpu: { token_env: WTOK_GPU, labels: [gpu, linux] }
runners:
  w-gpu:  { type: worker, worker_id: w-gpu }   # named worker runner (visible in the roster, explicitly routable)
  worker: { type: worker }                     # generic worker runner (label-based dispatch)
```

The worker side connects with its own config and executes locally:

```bash
gofer init worker -o worker.yaml             # template
gofer -c worker.yaml config validate worker  # checks token / host_path / roots
gofer worker --worker-config worker.yaml     # start
```

```yaml
worker_id: w-gpu
server_link: { urls: [ws://hub:8765/v1/workers/connect], token_env: WTOK_GPU }
labels: [gpu, linux]
roots:                                   # POLICY mode: projects are pushed by the server, only path prefixes are mapped here
  - { from: D:/work, to: /srv/work }
guards: { allow_exec: true, allow_interactive: true }   # local tightening only
```

- **Routing**: explicit `{"runner":"w-gpu"}` / `--worker-id`, or `{"runner":"worker","worker_labels":["gpu"]}` picks among connected workers whose labels contain all requested ones, by `in_flight↑ → heartbeat freshness↑`; no candidate → `503`. The chosen `worker_id` is recorded on the result.
- **Three places must agree**: the `server.workers.<id>` key, `runners.<name>.worker_id` and the worker's own `worker_id` are the same string; the worker's token equals `server.workers.<id>`'s.
- **LEGACY vs POLICY**: a worker.yaml with `roots` is POLICY (the project set comes from the server; adding a project touches nothing on the worker); one with only `projects` is LEGACY. Self-check with `gofer config validate worker` / `gofer project list`.
- **Pre-flight self-check**: `gofer worker doctor` prints one `PASS|WARN|FAIL` table for the config, hub URLs (host resolution + TCP reachability), token, roots, installed agents, and a real register handshake against the hub (`--json` for scripts, exit 1 on any FAIL); a container worker (Docker on the same host) is covered step by step in [`docs/runbook/container-worker.md`](docs/runbook/container-worker.md).
- Workers reconnect to multiple hub URLs with full-jitter backoff; `POST /v1/workers/{id}/reload` makes a worker re-read its config.

### Reconnect recovery (`recovering`)

When a worker link blips (WSL / Docker / VPN), its in-flight jobs **no longer fail immediately**: they enter **`recovering`** (yellow badge on the board, `job list --status recovering`) and wait for **the same worker process** to reconnect within `server.job_recover_window_sec` (default 120; `0` disables recovery, i.e. the old behaviour):

- On reconnect the worker reports its in-flight list and sent log offsets; the server answers with what it has persisted, so logs are **neither lost nor duplicated**; results produced while offline are delivered afterwards; a `job cancel` issued during the outage is delivered on reconnect.
- Window expired, or a *new* process reconnecting (`instance_id` changed) → `failed` with `worker lost …`.
- **A server restart is covered too**: the new server re-arms the window for the non-terminal worker jobs it inherited, and a reconnecting worker gets them **adopted** (back to `running`, logs appended to the same files).
- Notifications only fire on terminal states: recovering→running is silent, recovering→failed is delivered.

## Tunnels

Forward local ports through a worker to targets on the worker's network (e.g. a container or office PC → a shop-floor PLC / HMI). Targets are restricted by the worker's `tunnels.allow` list; the server only relays:

```bash
gofer tunnel forward -w w-plc 1502:192.168.1.10:502 udp/21845:192.168.1.20:21845   # TCP + UDP
gofer tunnel forward -w w-plc 1502:192.168.1.10:502,11217:127.0.0.1:1217            # several rules in one argument
gofer tunnel save hmi -w w-plc udp/21845:192.168.1.20:21845 && gofer tunnel forward --name hmi
gofer tunnel check -w w-plc udp/192.168.1.20:21845   # proves the worker can open the socket, not that the device answers
gofer tunnel ls                                      # FORWARDERS (listening processes) + CONNECTIONS (active tunnels)
gofer tunnel presets push                            # upload local presets to the server
```

Presets live on the **server** (the `tunnel_presets` table), so `tun forward -n <name>` works from any machine; `tun save` writes the local `tunnels.yaml` only when the server is unreachable (that read path is marked deprecated, removal in v0.63). A `tun forward` registers itself with the hub (expires after 90s without a heartbeat; `server.tunnel.forwarder_ttl_sec` tunes it), so the console and `tun ls` show who is *listening*, not only who is connected.

Forwarder, server and worker log the same `tunnel_id`, with `dial_ms`, `first_byte_ms`, `bytes_up|down`, `packets_up|down` (UDP) and `close_reason`; `GOFER_TUNNEL_TRACE=1` logs every datagram. How to tell a slow relay from a slow device or a chatty protocol is in [`docs/runbook/tcp-tunnel.md`](docs/runbook/tcp-tunnel.md).

## File transfer: `gofer tool cp`

Move a single file between this machine and a worker (or the server's own machine) — no more base64 in a job log:

```bash
gofer tool cp ./firmware.bin w-plc:shop-floor/tmp/in/firmware.bin    # push to a worker
gofer tool cp w-plc:shop-floor/tmp/out/report.csv ./report.csv       # pull from a worker
gofer tool cp ./x.tar server:build/tmp/x.tar                         # the server host (`local` is the same)
gofer tool xfer ls [--state staged] | show <id> | rm <id>            # staging area
```

The remote side is `<runner>:<project>/<relative path>`, resolved on the **executing** machine inside that project's root — the same boundary a job's `--cwd` obeys; an existing destination needs `--force`. The payload rides HTTP (staged on the server, sha256 verified end to end, 256MB per file by default via `server.xfer`), while the WebSocket carries only the instruction, so a transfer that cannot run fails immediately with its reason (`exists`, `worker offline`, `path escapes project`, `too large`) instead of hanging. v1 moves single files and does not resume: tar / `Compress-Archive` a directory first. A job can also carry files with it: `gofer job run --upload <local file>:<dest>` stages the file onto the executing machine before the agent starts, and `--collect '<glob>'` uploads what the job left in its cwd into that job's artifacts (`collected/<path>`), so the web job page and the artifact download serve it directly.

## Human in the loop: interactions, plans, session relay

- **Mid-run interactions**: an agent asks via `POST /v1/jobs/{id}/interactions` → the job becomes `pending_interaction` → a human answers via `POST …/answer` → the job continues. MCP: `gofer_get_interactions` / `gofer_answer_interaction`; web and IM notifications (DingTalk / Feishu webhooks under `server.notification`, which the config page edits as a PATCH — a master `enabled` switch, a per-target pause, and `secret_env` given as a NAME that is kept when left blank).
- **Approval gate (acp-agent)**: a project's `approval` block decides what an ACP agent may do unattended — `mode: off` (default) auto-approves as before, `ask` auto-approves `auto_allow_kinds` (read/search/think/fetch) and asks a human for the rest, `strict` asks for everything. A pending request becomes an interaction of type `permission` (the gated tool call + the agent's own ACP options + a countdown from `timeout_sec`), answered in the web job page, `gofer job answer <id> <interaction-id> <optionId>`, or MCP `gofer_answer_interaction`; an unanswered one is resolved by `on_timeout` (`reject` by default, or `allow`). An agent can only TIGHTEN the project policy via `agents.<key>.acp.permission_policy`; IM gets a notification (`job.permission_requested`, web link) but never answers. Each decision is audited in the job events (`job.permission_requested|answered|timed_out`) and `<result_dir>/artifacts/acp.jsonl`.
- **Plan boards**: `gofer plan create/add-todo/set-todo`, jobs attached with `--plan <id>`; the web Plan page (phone-friendly) is the live progress view. A todo can also carry its own dispatch request (`--assign omp --project <key> --template <book> --var k=v --verify '<cmd>' --review --runner <key> --cwd <dir> --timeout <sec>`), and **`ready` + an assignee dispatches it on the spot** — one job per step, outcome written back automatically (`plan dispatch <todo-id>` is the explicit fallback, `gofer_dispatch_todo` over MCP) — and the plan header rolls up the tokens/`$` its jobs reported. **Chained items (PLAN-03)**: `--after <ids|prev>` declares dependencies, `plan run|pause|resume <plan>` starts/holds/releases the chain, an item whose predecessors are done is queued automatically (`--auto` / `--no-auto` per item), an `--assign exec --cmd '<argv>'` item runs a command instead of an agent (the chain's build/test step), and a failed item parks the plan (`blocked_todo` + `plan.blocked`, in the notification default set) until someone sets it `ready`/`skipped` or resumes. **Decisions**: the MCP tool `gofer_ask_human` blocks until a human answers on the web (with a timeout fallback).
- **Wakeups (JOB-09)**: register an event subscription or a timer on a job and let the job finish — when the condition arrives gofer starts ONE continuation of it (`job resume` when the source has a session, else a re-run of the original request with the instruction appended), so "wait for the verify result / wait for a reply / look again in an hour" needs no resident process. `gofer job wakeup create <job> --kind at --after 10m|every --every 1h|cron --cron '0 9 * * 1-5' [--tz …]|event --event job.terminal --job-id <other> [--status done,failed] (-m "…"|-f instr.md)`; `--mode once` (default for at/event) or `continuous` (default for every/cron). One wakeup has ONE outstanding continuation — further triggers only bump `coalesced_count`; timers never replay a missed tick; the default TTL is 7 days (`wakeup.ttl_sec`). From inside a job an agent registers on itself with `$GOFER_JOB_ID`; MCP `gofer_wakeup_create|list|disable`; the web job page has a 唤醒 block (list / switch / create / trigger history from the `job.wakeup_*` events).
- **Terminal session relay**: after `gofer init hooks`, a Claude Code / Codex session that stops can post its last message to the web "Sessions" page and wait; the reply is injected into **the same** session (`gofer session relay auto|on|off`, `gofer session say`; a session is owned by the caller that registered it — and while THAT caller still has jobs running, `auto` deliberately does not arm, so the job's completion notice is not stuck behind your own blocked Stop, `session.auto_relay_skip_when_supervising`). The switch is **three-state**: `on` waits on every stop, `off` never does, `auto` (the default) leaves it to the server — it arms once you have been away from the keyboard for `session.auto_relay_idle_sec` (default 300, `0` disables) and releases the moment you touch it. Where the keyboard cannot be probed at all (a container hook, no X11 → `xprintidle`), the `session.auto_relay_turn_sec` fallback (default 900, `0` disables) arms on the time since your last input in that session instead, and your next input or Esc releases it. For a session that is merely **idle** (no turn waiting), the Sessions drawer's composer becomes "send to terminal" (`gofer session say --deliver`): the server types your text into that session's **tmux** pane via an internal exec job, so you can pick a parked session back up from the web — it needs the session running in tmux and a registered runner (a container session needs a gofer worker inside the container with `GOFER_HOOK_RUNNER` pointing at it). Where there is no usable tmux pane (a Windows Terminal session, a pane that is gone), the drawer offers a **takeover**: with an explicit opt-in (`allow_takeover`, the web's confirmation / `session say --deliver --takeover`) the server starts a NEW interactive pty job that continues the same CLI session (`claude --resume <sid>` et al., argv from the agent's interactive resume template) in the session's own project directory and types your text into it as the first input once the resumed TUI has settled (`session.takeover_input_delay_ms`, default 1500ms; 10s cap). The session is then reported `handed_off` and its original terminal stops relaying — its next Stop hook prints a one-line notice on stderr and lets the agent stop, because two processes writing one CLI session diverge. `POST /v1/sessions/{sid}/release-takeover` (the drawer's "release takeover") cancels that job and hands the session back. Takeover needs `allow_interactive` on the project and an agent with an interactive resume template; the exec carrier is authorised as the source agent, so it does not need `allow_exec`. A takeover job that ENDS auto-releases the session back to idle (`gofer session release-takeover <id>` is the manual form, and an injection that never reached the pane — `inject_failed:runner_error` — now falls back to a takeover too); and `gofer job review <id> [--diff]` prints a job's whole acceptance material on one screen.

- **Interactive pty jobs leave a readable record and a resumable session**: a pty job's output never enters `stdout.log`, so the relay also writes a de-ANSI'd transcript to `<result_dir>/pty.txt` (`job logs` / the web log page fall back to it, tail-capped by `pty.transcript_max_bytes`, default 4MB). The session id is captured from the de-ANSI'd **tail** (head+tail windows, re-scanned when the relay closes, plus a terminal scan of `pty.txt`), so `job resume` can continue an interactive session — including from the exit banner a text-mode TUI prints (`omp --resume <uuid>`); agents with `session_inject` (claude `--session-id`) get it injected in TUI argv too, and the transcript keeps the words such a TUI drew with cursor motion apart (a `ESC[nC` column pad becomes spaces).
## Logging and observability

- **File logs**: server `<config-dir>/run/serve.log`, worker `run/worker-<id>.log`, `tunnel forward` `run/tunnels/forward-<time>-<pid>.log` (`--log-file` / `--log-dir`; `--quiet` silences only the terminal). JSON Lines, rotated by `log.max_size_mb` / `max_age_days` / `max_backups`, with `token` / `authorization` / `password` / `secret` keys redacted. An explicit path that cannot be opened fails startup; a default path only warns. `-d` daemon mode (`serve` / `worker`) additionally keeps a `run/*.out.log` sidecar for panics and other non-slog output; a foreground process writes the file log only.
- **Events**: each line carries `event` (`server.*` / `worker.*` / `tunnel.*` / `job.*`), `operation_id` (one process run), and `job_id` / `worker_id` / `tunnel_id` where relevant; `GOFER_LOG_LEVEL=debug|info|warn|error` applies to stderr and file alike.
- **Agent output**: a structured agent (`omp --mode json`, `claude --output-format stream-json`) writes one JSON event per line; with `output_format: ndjson` gofer PROJECTS that stream **at capture time** — `stdout.log` holds the agent's text only (omp defaults to `ndjson_stdout: assistant_text` — every non-empty assistant message, narration plus the final report, blank-line separated, because a harness-injected trailing turn otherwise hides the report behind the last one-liner; claude defaults to `final_text`, the `result` text) and `stderr.log` the compact events, one bounded line each (over-long fields marked `…(truncated)`), with the per-token noise dropped — 10-30x smaller, codex-shaped, and the web job detail renders the events as a timeline (`ndjson_keep` gates what reaches the projector, `ndjson_events_to` / `ndjson_stdout` / `ndjson_stdout_path` / `ndjson_fields` tune it, `ndjson_raw` also keeps the unfiltered `stdout.raw.log`; the counts land in `ndjson_kept` / `ndjson_dropped` / `ndjson_truncated`). An **`acp-agent`** (`type: acp-agent`) gets the same two-stream shape from its own runner: `stdout.log` holds the agent's text, one block per message (a tool call ends a block and the next block starts after a blank line), `stderr.log` the compact `{"type":…}` events — `tool_call` per status change, `thought` coalesced into one line (`acp.log_thoughts: false` drops it entirely), `permission`, `plan`, `stop` — while the job timeline keeps lifecycle only (the approval gate's `job.permission_*` plus ONE `job.acp_summary` per turn) and the full structured stream stays in `artifacts/acp.jsonl`.
- **API**: `GET /v1/runners` health roster; `GET /v1/jobs` filters, `/v1/jobs/{id}/stream` SSE (logs + status + interactions), `/logs/{stdout,stderr}` (last 256KB), `/diff`, `/artifacts`, `/events`; `GET /v1/metrics`.
- **Audit**: `caller_id` (who submitted, derived from the token and overwritten server-side) and `worker_id` / `worker_instance_id` (where it ran) are persisted with the job; `storage.retention` prunes old / excess terminal jobs.
- **Usage and cost**: what the agent reported about its own run — tokens in/out/cached and, where the agent prices it, the cost — is captured from four sources (`output_format: ndjson` omp/claude streams, codex's `tokens used` stderr tail, an acp agent's `usage_update` events), persisted as `jobs.usage_json` and shown by `job show` (`usage: in 12.3k / out 3.8k / cache 289k / total 305k / $0.0032 (ndjson:omp)`), the web job page and the Home dashboard's **Agent 用量** card (24h/7d per agent, from `GET /v1/stats` → `usage`); `gofer agent status` adds 24h tokens/$ per agent. Capture is best-effort: an agent that reports nothing produces no usage (never a zero tally), the `source` field says where the numbers came from, and a remote job's usage is captured on the executing machine and returned with its outcome.

## Configuration

### Lookup chain and run modes

1. `--config <path>` → 2. `GOFER_CONFIG` → 3. `./.gofer.local.yaml` → `./.gofer.yaml` → 4. `<config-dir>/config.yaml` (default `~/.config/gofer`, override with `GOFER_CONFIG_DIR`).

| `GOFER_RUN_MODE` | local config | purpose |
|---|---|---|
| `server` (default) | the chain above | `gofer serve`; local jobs |
| `worker` | `<config-dir>/worker.yaml` | connect to a hub and execute dispatched jobs |
| `client` | **none** (only `<config-dir>/.env`) | pure client: `project list` / `agent list` read the server; `serve` / `worker` / `project add` etc. are refused with a clear message |

`.env` auto-loading: `<config-dir>/.env` (global) then `./.env` (current directory, overrides); exported OS env always wins; **never commit real tokens**.

### Key server sections

Full example: [`config/gofer.example.yaml`](config/gofer.example.yaml).

```yaml
server:
  addr: 0.0.0.0:8765            # reachable from containers; security = mandatory token + network admission
  token_env: GOFER_TOKEN        # token source: token_env > inline token > --token
  allow_empty_token: false
  # max_job_timeout_sec: 3600   # --timeout ceiling; a project's max_timeout_sec overrides it
  # job_recover_window_sec: 120 # reconnect recovery window; 0 = off
  # workers: { w-gpu: { token_env: WTOK_GPU, labels: [gpu] } }
  # callers: [ { id: docker, token_env: DOCKER_CALLER_TOKEN } ]
session:                             # terminal session relay: auto-arm thresholds (0 = that rule off)
  # auto_relay_idle_sec: 300         # keyboard idle >= this -> a stopping session waits on the web
  # auto_relay_turn_sec: 900         # no keyboard probe (container)? use the time since your last input
  # auto_relay_skip_when_supervising: true   # a session whose caller still has live jobs never auto-arms
  # supervising_window_sec: 7200     # how far back that check looks for the caller's jobs
  # inject_commands: [claude, codex, omp, node, gemini, opencode]  # panes a web reply may be typed into
  # takeover_input_delay_ms: 1500    # no tmux? wait this long after the resumed TUI's first output before typing
log:
  max_size_mb: 50
  max_age_days: 14
  max_backups: 10
storage:
  default_exchange_subdir: tmp
  default_result_subdir: gofer
  # root: /var/lib/gofer
  # retention: { max_age_days: 30, max_count: 5000, prune_interval_minutes: 60 }
projects:
  my-project:
    host_path: /work/projects/my-project
    container_path: /work/my-project
    default_agent: codex
    allowed_agents: [codex, claude, exec]
    allowed_runners: [local, w-gpu]
    allow_exec: true
    allow_interactive: true
    max_concurrent_jobs: 4
    # max_timeout_sec: 7200
    # worktree_default: true
agents:                            # placeholders: {{prompt}} {{cwd}} {{job_id}} {{result_dir}}
  codex:  { type: cli-agent, command: codex,  args: [exec, "{{prompt}}"], interactive_args: [], detect: { command: codex,  args: [--version] } }
  claude: { type: cli-agent, command: claude, args: ["-p", "{{prompt}}"], interactive_args: [], detect: { command: claude, args: [--version] } }
  exec:   { type: exec }
runners:
  local: { type: local }
```

- The four agent shapes: `args` only = batch only; `args` + `interactive_args` = dual mode; legacy `interactive: true` (args *are* the pty argv) = interactive only; `interactive: true` with `{{prompt}}` in `args` = **configuration error, rejected at load time**.
- Result directory: `<host_path>/tmp/gofer/<job_id>/` by default (`<root>/<project_key>/<job_id>/` with `storage.root`), holding `stdout.log` / `stderr.log` / `changes.diff` / artifacts; status and metadata live in SQLite.

## CLI reference

The global `-c/--config` is an app-level flag and goes before the subcommand: `gofer -c <path> <command> …`.

```bash
gofer init     [server|worker|client] [-o path]     # config templates; init hooks installs the session-relay hooks; init skill installs the gofer-usage skill
gofer serve    --addr 0.0.0.0:8765 [--no-web] [-d]  # -d = daemon (detached; linux + windows)
gofer worker   --worker-config worker.yaml [-d]
gofer config   info | show <project> | validate [server|worker] | edit
gofer project  list [--remote] | show <k> | add <k> … | remove <k> | validate <k>
gofer agent    list [--local] | detect | show <k>
gofer job      run … | list … | show <id> | watch <id> | logs <id> --stream … | cancel <id> | rerun <id> | resume <id> --prompt … | worktree ls|rm
gofer template ls [-p <project>] | show <name> [-p <project>] [--var k=v …]
gofer plan     create | list | show <id> | add-todo | set-todo | dispatch <todo> | run | pause | resume <plan> | set-status | attach | ask | decisions | answer
gofer workflow run <file.yaml> [-w] | list | show <id> | events <id> | cancel <id> | export <id>
gofer schedule add … | list | show | enable | disable | run <id> | rotate-token <id> | rm <id>
gofer session  ls | show <id> | relay auto|on|off | say <id> "…" | rm <id>
gofer tunnel   forward | check | ls | save | saved | forget
gofer tool     cp <src> <dst> [--force] [--timeout 600] | xfer ls | show <id> | rm <id>
gofer mcp      [--standalone]                        # stdio MCP server
```

Key `job run` flags: `-p/--project`, `-a/--agent`, `--runner` (default `server`; `local` is the canonical key and `server` the alias — BOTH are accepted on every surface, the CLI, the HTTP API, a `-f` task file and a task-book template, and `allowed_runners` may list either; give the runner name for workers/peers), `--cwd` (relative to the project root), `--prompt` / `-- argv` / `-f task.md` / `-t <template> [--var k=v …]` (a server-rendered task book; `--prompt` is appended to it), `--sync` + `--wait-timeout`, `--wait`, `--worker-id` / `--worker-labels`, `--interactive` + `--cols`/`--rows` (needs the project's `allow_interactive` and an agent with `interactive_args`), `--read-only` (the agent cannot write: cli-agent `read_only_args` — built-in `codex -s read-only` / `claude --permission-mode plan` — or acp-agent `acp.modes.read_only` → `session/set_mode`; exec agents and agents without a read-only mode are refused), `--worktree` + `--worktree-base`, `--review` (a normal completion parks in `needs_review` until a human accepts or rejects it), `--plan`, `--tags`, `--timeout`, `--title`, `-s/--server`, `--token`.

> Passing values across workflow steps: `${steps.N.result_dir}` is an absolute path on the executing machine and is only readable within the same filesystem; across workers/peers use `${steps.N.result}` (inline result.json ≤ 32KB) / `${steps.N.stdout}` or a shared drive.

## MCP

`gofer mcp` exposes the same control plane as a **stdio MCP server** (it talks to the server at `GOFER_SERVER_ADDR` by default; `--standalone` executes in-process). stdout is the protocol channel; no logs are written there.

```json
{ "mcpServers": { "gofer": { "command": "/abs/path/to/gofer", "args": ["mcp"], "env": { "GOFER_CONFIG_DIR": "/abs/config/dir" } } } }
```

Tools (snake_case, aligned with HTTP): `gofer_list_projects` `gofer_list_agents` `gofer_run_job` `gofer_get_job` `gofer_tail_log` `gofer_get_result` `gofer_get_artifacts` `gofer_cancel_job` `gofer_attach_job` `gofer_get_interactions` `gofer_list_pending_interactions` `gofer_answer_interaction` `gofer_punt_interaction` `gofer_create_plan` `gofer_get_plan` `gofer_add_todo` `gofer_update_todo` `gofer_dispatch_todo` `gofer_plan_run` `gofer_ask_human` `gofer_list_templates` `gofer_register` `gofer_list_presence` `gofer_post_message` `gofer_poll_inbox` `gofer_wakeup_create` `gofer_wakeup_list` `gofer_wakeup_disable`.

## Web console

`serve` embeds a static SPA (the page itself needs no auth; its `/v1/*` calls do). Build and embed it with `make web build`; a bare `go build` serves a placeholder page without affecting the API. Pages: Home (service health, drivers/runners, escalations, jobs by status, schedules, projects, plus two metadata-db cards — **Server DB**: file + WAL size, page geometry, the busiest tables by row count; **Sessions**: totals by state, relay mode split, turns still waiting for a reply, the card links through to Sessions) / board (the plan filter is a free-text plan-id input — prefix match — with a one-click "recent open plans" hint, and `?plan=` in the URL) / **review queue (`/review`, REV-01)** — jobs parked in `needs_review` with their acceptance material in one row (verify badge, commit count, usage, wait time; longest wait first), inline accept/reject and a top-bar count badge / job detail (live logs, diff, artifacts, pty attach; a job in `needs_review`, `rejected`, or `done` with `require_review` opens on a five-tab **review panel** — report / commits / diff / verify / usage — with accept/reject at the bottom) / Plans (todos, decisions; **plan board** — five-column kanban, drag a card to `ready` to dispatch it; the list filters by status/project/keyword and pages 20 at a time, all carried in the URL) / Sessions (relay switch, `auto (idle Xm)`) / Workflows / Schedules / Agents (configured agents and their detect status, with the online driver presence listed below it — a row opens the inbox at `/agents/presence/:id`, and the old `/drivers` + `/drivers/:id` links redirect there) / Runners / Projects (including "allow interactive jobs") / Skills (the skill library: list, SKILL.md body, import/update/remove/export) / New job. The left rail groups the observing pages as Board, Review, Plans, Sessions, Workflows, Schedules and the fleet pages as Agents, Runners, Projects, Skills (Drivers is no longer a separate entry). Disable with `serve --no-web` or `server.web_enabled: false`.

## HTTP API

`/health` is unauthenticated; everything under `/v1/*` requires `Authorization: Bearer <token>`. Error body: `{"error":"…","detail":"…"}`.

| group | main endpoints |
|---|---|
| projects / agents / roster | `GET/POST /v1/projects`, `GET/PUT/DELETE /v1/projects/{key}`, `GET /v1/agents`, `GET /v1/runners`, `GET /v1/meta`, `GET /v1/metrics` |
| templates | `GET /v1/projects/{key}/templates`, `GET /v1/projects/{key}/templates/{name}?var=k=v` (read-only; the detail endpoint returns the server's render of it) |
| jobs | `POST/GET /v1/jobs`, `GET /v1/jobs/{id}`, `/logs/{stdout,stderr}`, `/stream` (SSE), `/events`, `/diff`, `/artifacts`, `POST …/cancel`, `POST …/resume`, `POST/GET …/wakeups`, `GET/PATCH/DELETE /v1/wakeups/{wid}`, `GET/DELETE …/worktree`, `POST …/attach-ticket`, `GET …/pty/sessions` |
| interactions | `POST/GET /v1/jobs/{id}/interactions`, `POST …/{iid}/answer`, `POST …/{iid}/punt`, `GET /v1/interactions` |
| plans / decisions | `POST/GET /v1/plans`, `GET /v1/plans/{id}`, `POST …/todos`, `POST …/jobs`, `POST …/run\|pause\|resume`, `POST/GET /v1/decisions`, `POST /v1/decisions/{id}/answer` |
| session relay | `GET/POST /v1/sessions`, `POST /v1/sessions/{sid}/heartbeat`, `…/relay`, `…/say`, `…/deliver`, `…/release-takeover`, `…/turns` |
| workflows / schedules | `POST/GET /v1/workflows`, `…/{id}/cancel`, `…/events`, `…/export`; `POST/GET /v1/schedules`, `…/enable`, `…/disable`, `…/run-now`, `…/rotate-token`, `POST /v1/schedules/{id}/trigger?token=…` (unauthenticated, schedule token) |
| workers / tunnels | `GET /v1/workers/connect` (WS), `/v1/workers/pty-connect`, `POST /v1/workers/{id}/reload`, `GET /v1/tunnels`, `/v1/tunnels/connect`, `/v1/workers/tunnel-connect` |

`POST /v1/jobs` body (snake_case): `project_key`, `agent`, `runner`, `prompt` / `cmd`, `cwd`, `timeout_sec`, `title`, `worker_id` / `worker_labels`, `interactive`, `worktree` / `worktree_base`, `plan_id`, `tags`, `sync` / `wait_timeout_sec`, `request_id` (idempotency key), `template` / `vars` (a server-rendered task book).

## Deployment

**Single machine (Linux/macOS)**:

```bash
export GOFER_CONFIG=~/.config/gofer/config.yaml
gofer init server --global && $EDITOR ~/.config/gofer/config.yaml
gofer project add demo-api --host-path /abs/demo-api --container-path /work/demo-api
gofer serve -d                                     # daemon; log at <config-dir>/run/serve.log
```

**Windows: logon scheduled task in the desktop session** — `scripts/start.ps1` (ordinary window; `-Elevated` needs admin):

```powershell
pwsh -File scripts\start.ps1 -Action up -ConfigDir 'D:/path/to/gofer'   # register + start a logon task; local jobs run on YOUR desktop, as you
pwsh -File scripts\start.ps1 -Action upgrade [-Web]                    # make build first (server keeps running) → stop → swap serve-run\gofer.exe → start; previous exe kept as .prev
pwsh -File scripts\start.ps1 -Action status|logs|restart|stop|remove
```

Runs as a scheduled task rather than a service on purpose: a service lives in session 0 and cannot drive the desktop, so `--runner local` GUI jobs (DTools / CODESYS / screenshots) fail there. Migrating off an old nssm service: [runbook §7](docs/runbook/2026-07-11-windows-server-selfupdate-runbook.md).

**Container ↔ host**: the container is a pure client (`gofer init client`, `GOFER_SERVER_ADDR=http://host.docker.internal:8765`), the host runs the server (and/or a worker); a job's `--cwd` resolves against the executing machine's project root, so never hard-code container paths in commands.

## Security notes

- **Listening on `0.0.0.0:8765`** keeps the server reachable from containers; security relies on **a mandatory token + network admission**. Tighten to `127.0.0.1` for purely local use.
- **Mandatory token**: startup is refused without one; an empty token needs an explicit `--allow-empty-token`. Multiple caller tokens are compared in constant time and `caller_id` is persisted to prevent spoofing.
- **Worker binding**: a `worker_id` is bound to its token; in POLICY mode projects are pushed by the server and the worker can only tighten via `roots` + `guards`.
- **Execution boundaries**: exec needs both the project's and the request's consent; cwd is confined to the project (safeJoin blocks `../`); argv arrays, never shell strings; `--worktree` stays under the repo's `tmp/gofer/wt`; tunnel targets are allowlisted on the worker.
- **Logs**: tokens / Authorization / nonces / payloads are never written; the log API serves only the last 256KB.

## History

Codenames: `codex-bridge` (single codex + exec) → `dev-agent-bridge` (multi-agent/project registry + `/v1` async jobs) → **`gofer`** (+ WS workers / label scheduling / sync & markdown submission / Web / MCP / SQLite / plans & decisions / session relay / tunnels / reconnect recovery / worktrees).

> Designs and implementation plans live under [`docs/`](docs/) (`design/`, `plans/`, `runbook/`).
