# gofer

English · [中文](README.zh-CN.md) · [Changelog](CHANGELOG.md) · [User guide](docs/guide/README.md)

**gofer is a self-hosted control plane that runs coding agents — Claude Code, Codex, ACP agents or plain commands — as tracked, reviewable jobs inside your real projects, on your own machines.**

You run one gofer server on your main host and, optionally, workers on other machines or containers. Everything you hand over becomes a *job* with its logs, diff, commits, token usage and review state; people and agents reach the same jobs through the CLI, an HTTP API, MCP and a web console.

```mermaid
flowchart LR
    cli["CLI · MCP · Web · session hooks"] -->|"HTTP · Bearer token"| server["gofer serve<br/>projects · agents · jobs<br/>plans · sessions · tracker"]
    server --> local["server runner<br/>(this host)"]
    server <-->|WebSocket| worker["worker<br/>(other machines, dials in)"]
    local & worker --> agents["claude · codex · omp · exec …"]
```

- **server** (`gofer serve`) — the single source of truth: projects and their admission rules, job store, scheduler, API and web console. One per machine.
- **worker** (`gofer worker`) — a remote executor that dials into the server, reports its directories and agents, and runs jobs locally. No inbound port needed.
- **agent** — the program that does the work. gofer embeds no model; it launches and supervises CLIs you already have.

It is built for one developer, or a small team that trusts each other. It is **not** an agent or an LLM itself, not a hosted service, not a CI/CD system or a sandbox, and not a multi-tenant platform: access is token based, and a job runs with the permissions of the gofer process that executes it.

## Install

1. **Release package (recommended)** — includes the built-in web console:
   - [eget](https://github.com/inherelab/eget): `eget install inhere/gofer`
   - or download the archive for your platform from [GitHub Releases](https://github.com/inhere/gofer/releases), extract it, and put the `gofer` binary (`gofer.exe` on Windows) on your `PATH`.
2. **`go install`** — CLI, HTTP API and MCP only, **without** the web console (it shows a placeholder page):

   ```bash
   go install github.com/inhere/gofer/cmd/gofer@latest
   ```

3. **From source** — Go 1.26+; Node.js and pnpm only if you build the console:

   ```bash
   make web build        # console embedded, binary in dist/gofer
   make install          # same, then copy to $GOPATH/bin
   ```

Same binary in all three cases; only the embedded frontend differs.

## Quick start (about 10 minutes)

### 1. Server

On the machine that owns your projects:

```bash
gofer init server --global          # writes <config-dir>/config.yaml (default ~/.config/gofer)
export GOFER_TOKEN=change-me        # the generated config reads the token from GOFER_TOKEN

gofer project add <project> --host-path /abs/path/to/<project> \
  --default-agent codex --allow-agent codex --allow-agent claude --allow-agent exec \
  --allow-runner server --allow-exec
gofer config validate
gofer serve                         # foreground; `gofer serve -d` for background,
                                    # `gofer serve register` for a managed service
```

The token line is POSIX shell; on Windows use `$env:GOFER_TOKEN = "change-me"` and `<config-dir>` is `%USERPROFILE%\.config\gofer`.

### 2. Your first job

```bash
gofer job run -p <project> -a exec --title "hello" --sync -- go version   # a command
gofer job logs <id>                                                       # its output

gofer job run -p <project> -a claude --title "look around" --review \
  --prompt "Explain how this repository is built"
gofer job watch <id>          # follow it live
gofer job review <id>         # report, commits, diff, verify, usage on one screen
gofer job accept <id>         # or: gofer job reject <id> --note "…" --resume
```

`exec` runs the argv after `--` (no shell); `claude` / `codex` / `omp` / … run an agent. Agent jobs can be isolated in a git worktree (`--worktree`), held for approval before running (`--hold`), and parked for human review (`--review`). See [scenarios](docs/guide/scenarios.md) for these.

### 3. Open the web console

`http://<server>:8765/` — paste the token. The home page ("Today") is one queue of everything waiting for a decision: approvals, questions, reviews, sessions waiting for a reply. It works on a phone too.

### 4. Optional: add another machine

```bash
# on the server (admin token): register the worker, print its one-time token
gofer worker add <worker-id> --project <project>
# on that machine: infer directories, probe agents, write worker.yaml, self-check
gofer worker init --server http://<server>:8765 --id <worker-id> --token <one-time token>
gofer worker -d

gofer job run -p <project> -a claude --runner <worker-id> --prompt "…"
```

Or let the worker ask to be admitted (`server.worker_enrollment`): `gofer worker join --server http://<server>:8765`, then accept the pairing code in the console.

### 5. Optional: hand your terminal sessions over to the web

```bash
gofer init hooks --agent all --global   # register Claude Code / Codex / omp sessions
gofer init skill --global               # install the gofer-usage skill for agents
gofer session relay on                  # this session now waits on the web when it stops
```

Now a session that stops can be answered from the browser or phone, woken up again (`gofer session resume`) or nudged on a timer (`gofer session nudge`).

## Documentation

| Where | What |
|---|---|
| [`docs/guide/`](docs/guide/README.md) | **Start here.** Concepts, common scenarios, configuration, security, troubleshooting, stability |
| [`skills/gofer-usage/`](skills/gofer-usage/SKILL.md) | The same platform for agents: command reference, config fields, protocol notes (kept in sync with every release) |
| [`docs/reference/`](docs/reference/) | HTTP API, permissions catalogue, web console details |
| [`docs/runbook/`](docs/runbook/) | Step-by-step operating guides (session relay, container worker, tunnels, HTTPS/PWA, Web Push, managed server, worktrees, retries…) |
| [`docs/design/`](docs/design/) | Design records for each feature |
| [`CHANGELOG.md`](CHANGELOG.md) | What changed in each release (Chinese) |

The guide, most runbooks and design records are written in Chinese; the command line, `gofer <cmd> -h`, is authoritative for flags.

## Contributing

See [docs/guide/development.md](docs/guide/development.md) for build and test commands, and [AGENTS.md](AGENTS.md) for the project rules (layering, CLI conventions, compatibility policy, shared utilities). User-visible changes update [`skills/gofer-usage/`](skills/gofer-usage/) and get an entry in [CHANGELOG.md](CHANGELOG.md) under "未发布" in the same change set.

## License

[MIT](LICENSE)
