# gofer

English · [中文](README.zh-CN.md) · [Changelog](CHANGELOG.md)

**gofer is a self-hosted control plane that runs coding agents — Claude Code, Codex, ACP agents or plain commands — as tracked, reviewable jobs inside your real projects, on your own machines.**

You run one gofer server on your main host and, optionally, workers on other machines or containers. Every piece of work — a one-off command, an agent prompt, a long-running ACP conversation, a step in a multi-step plan — becomes a *job* bound to a registered project and a runner, with its logs, diff, commits, token usage and a review state. People and agents reach the same control plane through the CLI, an HTTP API, MCP and a web console, and a repository-local tracker (issues, memories, session primes, handoff briefs) gives every new agent session the context the last one left behind.

It is built for one developer, or a small team that trusts each other, driving several coding agents across a few machines. It is **not** an agent or an LLM itself, not a hosted service, not a CI/CD system or a sandbox, and not a multi-tenant platform: access is token based and a job runs with the permissions of the gofer process that executes it.

Core capabilities:

- **Run anywhere you have a checkout** — on the server host or on remote workers (WebSocket, label routing, server-pushed project policy), with git worktree isolation, same-directory locks, reconnect recovery, file transfer and TCP/UDP tunnels.
- **Drive any agent the same way** — `cli-agent` (claude, codex, omp, …), `acp-agent` (Agent Client Protocol) or `exec`; batch, interactive pty or resident ACP sessions; resume, failover to another agent, per-job model and budget limits.
- **Orchestrate multi-step work** — plans with todo chains and dependencies, workflows with fan-out and multi-agent comparison, cron schedules and job wakeups.
- **Keep a human in charge** — verify steps, acceptance criteria, change-scope checks, `needs_review` with accept/reject, an approval gate for ACP tools, terminal session relay to the web and phone, and a decision-centric "Today" page.
- **Shared context for agent collaboration** — repository-local issues and memories, session primes and handoff briefs, knowledge capture from delivered jobs, work items and an optional steward agent.
- **Observe and audit everything** — event timelines, diffs and commits, usage and cost, dashboards, IM/webhook notifications, per-job credentials and redaction tools.

> A gofer is "the one who runs errands": hand a task to an agent, run it in the target project, bring back logs and results. The `gofer` / `gopher` pun is intentional.

## Contents

- [Why gofer](#why-gofer) · [Architecture](#architecture) · [Install](#install) · [Quick start](#quick-start)
- [Core concepts](#core-concepts) · [Feature overview](#feature-overview) · [Configuration](#configuration)
- [Documentation](#documentation) · [Development](#development) · [Changelog](#changelog) · [License](#license)

## Why gofer

Running coding agents for real work quickly runs into the same problems:

| Problem | What gofer does |
|---|---|
| The agent you want lives on another machine (host vs. container, Windows vs. Linux, a GPU box) | One server, many workers; submit from anywhere and the job runs where the project and the agent are |
| Agent runs disappear into terminal scrollback | Every run is a job with status, logs, diff, commits, usage and an event timeline, kept in SQLite |
| "The agent says it is done" is not acceptance | Verify commands, acceptance criteria, scope checks and a `needs_review` gate that only a person can accept |
| Parallel agents trample one checkout | Directory locks, `--worktree` isolation and explicit merge back |
| Long tasks need several steps and several agents | Plans with dependent todos, workflows with fan-out / pick, schedules and wakeups |
| You walk away and the session stalls waiting for you | Session relay, web push and the "Today" decision queue let you answer from a browser or phone |
| Every new agent session starts from zero | Repository tracker: issues, memories, `repo prime`, `issue brief` / `plan brief`, knowledge candidates |

## Architecture

```txt
 clients                         control plane                      execution
┌──────────────────┐        ┌──────────────────────────┐      ┌──────────────────────────┐
│ CLI   gofer …    │─HTTP──▶│ gofer serve              │─────▶│ server (built-in runner) │──▶ agents
│ MCP   gofer mcp  │        │  /v1 API · web console   │      ├──────────────────────────┤    cli-agent: claude, codex, omp…
│ Web   console    │        │  projects · agents       │◀─WS─▶│ worker  (remote, dials in)│──▶ acp-agent: *-acp
│ hooks claude/    │        │  jobs · plans · workflows│      ├──────────────────────────┤    exec: any argv
│   codex/omp/…    │        │  sessions · tracker      │─HTTP▶│ peer-http (another gofer) │
└──────────────────┘        │  SQLite + result dirs    │      └──────────────────────────┘
                            └──────────────────────────┘
 Authorization: Bearer <token> on /v1/*      logs, status, interactions and outcomes are mirrored back to the server
```

- **server** (`gofer serve`): the single source of truth — project registry and admission rules, job store, scheduler, web console, MCP/HTTP API. One server per machine.
- **workers** (`gofer worker`): remote executors that dial into the server over WebSocket, report which agents and directories they have, and run jobs locally. In POLICY mode the server pushes the project set; the worker only maps path prefixes (`roots`) and may tighten `guards`.
- **agents**: the programs that do the work. gofer does not embed a model; it launches and supervises existing CLIs.
- **clients**: anything that submits or observes work — the CLI (also in client-only mode inside containers), MCP clients, the browser, and agent hooks that register terminal sessions.

A fuller concept map (in Chinese) is in [`docs/architecture-overview.md`](docs/architecture-overview.md).

## Install

Go 1.25+; Node.js and pnpm only if you build the web console.

```bash
make web build          # build the web console, embed it, build dist/gofer (compressed with upx)
make install            # the same, then copy the binary to $GOPATH/bin
go build -o dist/gofer ./cmd/gofer   # API/CLI only; the console shows a placeholder page
make build-all          # cross-compile linux / darwin / windows × amd64 / arm64
```

## Quick start

**1. Server** (the machine that owns your projects):

```bash
gofer init server --global          # writes <config-dir>/config.yaml (default ~/.config/gofer)
$EDITOR ~/.config/gofer/config.yaml # review it and drop the sample projects you do not need
export GOFER_TOKEN=change-me        # the example config reads the bearer token from GOFER_TOKEN
gofer project add demo --host-path /abs/path/to/demo \
  --default-agent codex --allow-agent codex --allow-agent claude --allow-agent exec \
  --allow-runner server --allow-exec
gofer config validate
gofer serve                         # or: gofer serve -d (background), gofer serve register (managed service)
```

Open `http://<server>:8765/` and paste the token to use the web console.

**2. Client** (a container or another machine that only submits work — no YAML needed):

```bash
gofer init client                   # writes <config-dir>/.env with GOFER_SERVER_ADDR / GOFER_SERVER_TOKEN / GOFER_RUN_MODE=client
gofer project list                  # in client mode this lists the server's projects
```

**3. Worker** (optional, a machine that should execute jobs):

```bash
# on the server (admin token): register the worker and print its one-time token
gofer worker add w-01 --project demo
# on the worker machine: write worker.yaml + .env, infer roots, probe agents and run the doctor
gofer worker init --server http://<server>:8765 --id w-01 --token <one-time-token>
gofer worker -d
```

**4. First jobs:**

```bash
gofer job run -p demo -a exec --sync -- go version                      # a command; --sync waits for the result
gofer job run -p demo -a codex --prompt "Summarise the failing tests" --wait
gofer job run -p demo -a claude --runner w-01 --worktree --review --prompt "Fix issue X"
gofer job list
gofer job watch <id>                                                     # live status + logs
gofer job logs <id> --stderr --tail -n 50
gofer job review <id>                                                    # acceptance material on one screen
gofer job accept <id>                                                    # or: gofer job reject <id> --note "…" [--resume]
```

**5. Optional integrations:**

```bash
gofer init hooks --agent all --global   # register Claude Code / Codex / omp / jcode sessions (session relay + memory prime)
gofer init skill --global               # install the gofer-usage skill for agents
```

## Core concepts

| Concept | Meaning |
|---|---|
| **project** | A registered directory work can run in: `host_path` (and optional `container_path`), allowed agents and runners, `allow_exec`, `allow_interactive`, concurrency and timeout limits, default verify step, review and scope policies. A built-in `default` project points at the default workspace (`~/.gofer/workspace` or `GOFER_WORKSPACE`) when none is declared. |
| **agent** | How to run: `cli-agent` (an argv template with `{{prompt}}`, `{{cwd}}`, … for batch and optional `interactive_args` for pty), `acp-agent` (an ACP server over stdio), or `exec` (the request's argv verbatim). Built-in templates are injected for CLIs found on `PATH`. |
| **runner** | Where to run: `server` (the built-in runner on the server host; `local` is the canonical stored name), a `worker`, or a `peer-http` gofer. |
| **job** | One unit of work: project + agent + prompt or argv + cwd. Lifecycle `queued → running → done / failed / cancelled / timeout`, plus `waiting_dir`, `pending_interaction`, `awaiting_input`, `recovering`, `needs_review → rejected`. Results live in the project's `tmp/gofer/<job-id>/` and the database. |
| **workflow** | A chain of steps (`gofer workflow run file.yaml`), with `${steps.N.*}` references, retries, fan-out/join, sub-workflows and built-in templates (`compare`, `plan-implement`, `review-committee`). |
| **plan / todo** | A plan groups jobs; todos are its checklist. A todo with an assignee and `ready` status dispatches itself; `--after` chains todos and `gofer plan run` drives the chain. |
| **session** | An agent conversation: a resident ACP job (`job run --session`, `job say`, `job end`), a pty job, or a terminal session registered through hooks (relay, wake-up, nudges). |
| **work item** | One card per thing you are doing across sessions (`gofer work`), with status, reminders, a daily digest and an optional steward. |
| **tracker** | `.gofer/tracker/` in a repository: issues and memories as JSONL committed with the code, mirrored to the server, injected into new sessions by `gofer repo prime`. |

## Feature overview

The full user-facing reference is the [gofer-usage skill](skills/gofer-usage/SKILL.md) (Chinese) and its [command reference](skills/gofer-usage/references/commands.md); `gofer <command> -h` is authoritative for flags.

### Jobs

- Submit by CLI flags, a markdown task file with YAML front matter (`-f task.md`), or a server-rendered task-book template (`-t <name> --var k=v`, `gofer template ls|show`).
- Sync or async (`--sync`, `--wait`), `--read-only`, `--model`, `--max-tokens / --max-cost / --max-turns`, `--timeout`, `--retry`, `--fallback`, `--env`, `--upload` / `--collect`.
- Isolation: same-directory locks (`--lock`, `--shared-dir`, `--exclusive-dir`, `--lock-wait`), managed worktrees (`--worktree`, `gofer job worktree ls|merge|rm`).
- Continuation: `job resume` (same agent session, `--mode`, `--agent` within a session family), `job rerun`, `--from-session`, automatic resume of transient provider errors, failover to fallback agents.
- Interactive: `--interactive` pty jobs with browser attach; resident ACP sessions with `--session`.
- Wakeups: `gofer job wakeup create` resumes a finished job on a timer or an event.

### Review and quality gates

- `--verify '<cmd>'` runs a check on the executing machine after the agent; `--review` parks the job in `needs_review`; only a person can `job accept`.
- `--acceptance` criteria and `--scope` change globs flow into the prompt and the review panel; out-of-scope findings become `gofer job findings [--create-issues]`.
- Uncommitted-change guard (`on_uncommitted`), approval gate for ACP tool calls (project `approval`), mandatory rules (`gofer agent rule`) and skill bindings (`gofer agent skill`).

### Orchestration

- Plans and todos: `gofer plan create|add-todo|set-todo|run|pause|resume|dispatch|import`, plan handoff notes, decisions (`plan ask`, MCP `gofer_ask_human`), board view on the web.
- Workflows: `gofer workflow run|show|pick|template`, multi-agent compare with pick-and-merge.
- Schedules: `gofer schedule add|run|enable|disable` with cron or one-off timing and optional webhook triggers.
- Comments with `@agent` mentions that dispatch a job, and optional leader rounds per plan.

### Sessions and the human in the loop

- Terminal session relay: after `gofer init hooks`, a stopped Claude Code / Codex / omp session can wait for a reply from the web (`gofer session relay auto|on|off`, `session say`), be woken up again (`session resume`) or nudged (`session nudge`).
- Claude Code permission prompts mirrored to the web; ACP and pty sessions in the Workbench; PWA with Web Push over the optional HTTPS listener (`server.tls`, `gofer tool cert`).
- "Today" home page: one queue of decisions waiting for you (interactions, plan decisions, reviews, work items), parallel lanes, snooze and focus mode.
- Work items and steward: `gofer work …` tracks what you are doing across sessions; the optional steward (`gofer steward …`) tidies them with a restricted credential.

### Repository tracker and agent memory

- `gofer repo init|status|prime|sync|migrate`, `gofer issue …`, `gofer memory …` — issues and memories in `.gofer/tracker/*.jsonl`, plus global and per-project memories on the server.
- `repo prime` (installed as a SessionStart hook) gives a new session the current focus, open issues, rules and a memory index; `gofer issue brief` / `gofer plan brief` hand over a task in one command.
- `memory flag` reports stale memories, `memory doctor` checks them, `memory candidates|accept|reject` turns reusable lessons from delivered jobs into memories.

### Workers, networking and operations

- Workers: `gofer worker add|init|doctor|show|projects|reload|upgrade|remove`, LEGACY and POLICY configuration, reconnect recovery.
- Tunnels: `gofer tunnel forward|check|ls|save|stop` forwards TCP/UDP through a worker under its allowlist; presets are stored on the server and can be hosted by it.
- Files: `gofer tool cp <src> <runner>:<project>/<path>` and `gofer tool xfer`.
- Managed server: `gofer serve register|start|stop|restart|status|logs|upgrade|uninstall` (Windows logon task or Linux systemd unit), `gofer serve reload` for hot config reload.

### Observability and security

- Event timeline, diff and commits, usage and cost per job, the Dashboard (`/dashboard`), Prometheus `/metrics`, JSONL file logs with rotation and redaction.
- Notifications through webhooks and DingTalk / Feishu adapters (`server.notification`).
- Per-job scoped credentials, caller tokens with optional capabilities, `gofer job redact`, `job delete`, `job secret-scan`.

### Entry points

- **CLI** — commands grouped by `gofer -h`.
- **HTTP** — `/v1/*` with `Authorization: Bearer <token>`; `/health` and the Prometheus `/metrics` endpoint sit outside it (`/metrics` can take its own token).
- **MCP** — `gofer mcp` is a stdio MCP server that forwards to the server at `GOFER_SERVER_ADDR` (`--standalone` runs in-process, `--project` scopes it):

  ```json
  { "mcpServers": { "gofer": { "command": "/abs/path/to/gofer", "args": ["mcp"] } } }
  ```

- **Web console** — embedded in `gofer serve` (disable with `--no-web`): Today, Dashboard, Workbench, Board, Review, Plans, Work, Sessions, Issues, Workflows, Schedules, Agents, Runners, Projects and Settings.

## Configuration

Config lookup: `-c/--config` → `GOFER_CONFIG` → `./.gofer.local.yaml` / `./.gofer.yaml` → `<config-dir>/config.yaml` (`<config-dir>` defaults to `~/.config/gofer`, override with `GOFER_CONFIG_DIR`). A project directory may carry a thin `.gofer.project.yaml` with preferences only. `<config-dir>/.env` and `./.env` are loaded automatically; never commit real tokens.

| `GOFER_RUN_MODE` | Local config | Purpose |
|---|---|---|
| `server` (default) | the lookup chain above | `gofer serve`, local administration |
| `worker` | `<config-dir>/worker.yaml` | connect to a server and execute dispatched jobs |
| `client` | only `<config-dir>/.env` | submit and observe; project and agent commands read the server |

- Examples: [`config/gofer.example.yaml`](config/gofer.example.yaml) and [`config/worker.example.yaml`](config/worker.example.yaml).
- References: [server](skills/gofer-usage/references/server-config.md) · [worker](skills/gofer-usage/references/worker-config.md) · [client](skills/gofer-usage/references/client-config.md) · [setup recipes](skills/gofer-usage/references/setup-recipes.md).
- Inspect and check: `gofer config info`, `gofer config show <project>`, `gofer config validate [server|worker]`, `gofer worker doctor`.
- Security baseline: the server refuses to start without a token (unless `--allow-empty-token`); listen on `127.0.0.1` when you do not need remote access; exec jobs need the project's `allow_exec`; argv is never passed through a shell; a job's cwd is confined to its project.

## Documentation

| Where | What |
|---|---|
| [`skills/gofer-usage/`](skills/gofer-usage/SKILL.md) | User and agent guide, kept in sync with every user-visible change |
| [`docs/runbook/`](docs/runbook/) | Operating guides: session relay, container worker, tunnels, HTTPS/PWA, Web Push, IM notifications, managed server, worktrees, retries… |
| [`docs/reference/`](docs/reference/) | HTTP API endpoint overview and web console details (Workbench layout, shortcuts, live push) |
| [`docs/design/`](docs/design/) | Design records for each feature |
| [`docs/gofer-enhancements-roadmap.md`](docs/gofer-enhancements-roadmap.md) | Roadmap: landed features by id, next candidates ([history](docs/roadmap-history.md)) |
| [`docs/examples/templates/`](docs/examples/templates/) | Ready-made task-book templates |

Most design documents and runbooks are written in Chinese.

## Development

```bash
go build ./... && go vet ./...
go test ./...                                   # full suite; use -race -count=1 for the release check
cd web && npx vue-tsc --noEmit && npx vitest run && npx vite build
GOOS=darwin go vet ./...                        # after touching platform-specific code
```

- Project rules (layering, CLI conventions, compatibility policy, shared utilities) are in [`AGENTS.md`](AGENTS.md). In short: entry layers (`commands`, `httpapi`, `mcpserver`) only bind and forward; dependencies point one way; new small utilities go under `gofer tool`; temporary compatibility code carries a `DEPRECATED(vX): remove in vY` marker.
- A user-visible change updates [`skills/gofer-usage/`](skills/gofer-usage/) in the same change set, and gets a line in [`CHANGELOG.md`](CHANGELOG.md) under "未发布" (unreleased).
- Parallel work uses git worktrees under `.worktrees/`.

## Changelog

See [`CHANGELOG.md`](CHANGELOG.md) (in Chinese): one entry per release since v0.100.0, earlier history condensed by milestone.

## License

This repository does not include a license file yet.
