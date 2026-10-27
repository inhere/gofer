# Getting started

This page takes you from a downloaded binary to a running setup: a server on the machine that owns your code, your first job, the web console, a second machine, and your terminal agent sessions waiting for you in the browser. It is the long form of the [README walkthrough](../../../README.md#full-walkthrough-about-10-minutes); every step says what you should see and what usually goes wrong.

One thing up front: gofer's CLI messages and most of its prose documentation are Chinese first. Commands, flags, field names and ids are English, and the quotes below are verbatim — expect a Chinese sentence next to English field names. When a flag is in doubt, `gofer <command> -h` is authoritative.

## Before you start

You need:

- one machine that has your projects on its disk (**the server**);
- optionally, other machines or containers (**workers**);
- the CLIs you want gofer to drive already installed and logged in (`claude`, `codex`, `omp`, …). gofer ships no model and no agent: it launches the CLIs you already have. `exec` jobs need nothing extra.

## 1. Install

Take the release package — it embeds the web console:

- [eget](https://github.com/inherelab/eget): `eget install inhere/gofer`
- or download the archive for your platform from [GitHub Releases](https://github.com/inhere/gofer/releases), extract it, and put `gofer` (`gofer.exe` on Windows) on your `PATH`.

`go install github.com/inhere/gofer/cmd/gofer@latest` also works, but that binary has no console: the web UI shows a placeholder page. Building from source (`make web build`) gives you the same binary with the console embedded.

Check it runs:

```bash
gofer --version
```

## 2. Start the server

On the machine that has your projects:

```bash
gofer init server --global
export GOFER_TOKEN=change-me          # Windows: $env:GOFER_TOKEN = "change-me"
```

`init server --global` writes `<config-dir>/config.yaml` — `~/.config/gofer/config.yaml`, or `%USERPROFILE%\.config\gofer\config.yaml` on Windows — and prints that path with a hint to set `GOFER_CONFIG`. The generated file reads the bearer token from `GOFER_TOKEN` (`server.token_env`), so the token never sits in the config.

Now register a project. A project is a directory you allow work to run in, plus what is allowed there:

```bash
gofer project add <project> --host-path /abs/path/to/<project> \
  --default-agent codex --allow-agent codex --allow-agent claude --allow-agent exec \
  --allow-runner server --allow-exec
```

You should see a line like:

```
project "<project>" saved to /home/you/.config/gofer/config.yaml
```

Validate and start:

```bash
gofer config validate     # ends with "config OK" when everything resolves
gofer serve               # foreground: "listening on 0.0.0.0:8765"
```

`gofer serve -d` runs it in the background (`<config-dir>/run/serve.log`), and `gofer serve register` installs it as a managed service (Windows logon task, systemd unit). Stop it with `gofer serve stop`.

### If it does not start

- **"token" errors at startup**: the config refuses to run without a token unless you pass `--allow-empty-token` or set `server.allow_empty_token: true`. Keep the token in the environment, not in the file.
- **Port already in use**: the default is `0.0.0.0:8765`. Change `server.addr` (for a single-machine setup `127.0.0.1:8765` is enough) or pass `--addr`.
- **`config validate` complains about a path**: `host_path` must exist on disk. A relative path is resolved against the current directory when you run `project add` (and stored absolute), but `~` is never expanded — write the real path.
- **You edited the config while the server runs**: `gofer serve reload` re-reads it; projects, tokens, callers and most thresholds take effect immediately. A broken file keeps the old config running.
- **No projects declared at all**: the server injects a built-in `default` project pointing at the default workspace (`~/.gofer/workspace`, or `GOFER_WORKSPACE`). It is there so you can try a command; register your real projects explicitly.

## 3. Your first job

A job is one execution: a project, an agent, and either a prompt or an argv. Start with a plain command:

```bash
gofer job run -p <project> -a exec --title "hello" --sync -- go version
```

`exec` runs the argv after `--` directly, without a shell. You should see:

```
job job-20261026-140721-124c1f7a submitted: status=done result_dir=/abs/path/to/<project>/tmp/gofer/20261026/job-20261026-140721-124c1f7a
job job-20261026-140721-124c1f7a finished: status=done exit_code=0
```

`--sync` returns the status, not the output:

```bash
gofer job logs job-20261026-140721-124c1f7a
gofer job show job-20261026-140721-124c1f7a    # status, runner, cwd, timeout, result dir
```

Anything longer than a few seconds is better submitted without `--sync`, then followed:

```bash
gofer job run -p <project> -a exec --title "full test run" --timeout 1800 -- go test ./...
gofer job watch <id>        # streams status and logs until it ends
```

### If the job is refused

The server validates before anything runs, and says why:

```
ERROR: server 400: job rejected: invalid request: agent "exec" is not allowed in project "<project>"
ERROR: server 400: job rejected: cwd "../../.." escapes project root
ERROR: server 404: job rejected: unknown project: unknown project "<project>"
ERROR: server 401: unauthorized: missing or invalid bearer token
```

So: the agent must be in the project's `allowed_agents`; `--cwd` is a path *inside* the project (`--cwd sub/dir`), never an absolute path — the job may run on another machine where your absolute path means something else; and a missing token is a client-side problem (`GOFER_SERVER_TOKEN`, or `gofer init client` on a machine that only submits jobs).

Two more defaults worth knowing: a job's timeout is 300 s unless you pass `--timeout`, and the server clamps it to `server.max_job_timeout_sec` (3600 s by default, or the project's `max_timeout_sec`); `--sync` gives up after about 30 s (`--wait-timeout`), which is not the job's timeout.

## 4. A real agent job, with a review gate

Now hand over something that writes code. `--review` makes the job stop at `needs_review` when the agent finishes: only a person accepts it.

```bash
gofer job run -p <project> -a claude --title "look around" --review \
  --prompt "Explain how this repository is built"

gofer job watch <id>       # follow it
gofer job review <id>      # status, report tail, verify output, commits, usage, diff summary
gofer job accept <id>      # or: gofer job reject <id> --note "…" --resume
```

`gofer job review` ends with the agent's report, so you can judge the work without opening the logs. `accept` moves the job to `done`; `reject --resume` sends it back with your note and continues the same agent session.

Useful switches on the way there:

- `--worktree` gives the job its own git worktree and branch (`gofer/<job-id>`); `gofer job deliver <id>` merges it back, pushes it, or opens a PR.
- `--verify '<cmd>'` runs a check on the executing machine after the agent stops; a non-zero exit fails the job.
- `--acceptance '…'` and `--scope 'src/auth/**'` are injected into the prompt and compared against what the agent actually changed.
- `--hold` parks the job at `awaiting_approval` *before* it runs, for irreversible operations; a person approves it, and the submitting agent cannot approve its own job.
- `--read-only`, `--model`, `--max-tokens`, `--max-cost` cap what the run can do and cost.

## 5. Open the web console

Browse to `http://<server>:8765/` and paste the token. The home page, **Today**, is one queue of everything waiting on a decision: jobs awaiting approval, agents asking questions, jobs to review, sessions waiting for a reply. It is usable on a phone.

- If the page does not load, check `server.addr` — a server bound to `127.0.0.1` is reachable only from that machine.
- The console is served by `gofer serve` itself; only a `go install` binary shows a placeholder there.
- For HTTPS, a PWA install and web push, configure `server.tls` and see [https-pwa.md](../../runbook/https-pwa.md).

## 6. Add a second machine (worker)

A worker is a remote executor. It dials out to the server over WebSocket, so it needs no inbound port: containers, NAT, home broadband are all fine.

```bash
# on the server (admin token): register the worker, prints its one-time token
gofer worker add w-office --project <project>
```

```
worker w-office registered
worker token (shown once): gw_0f1c…            # copy it now
next: gofer worker init --server http://<server>:8765 --id w-office --token gw_0f1c… --yes
```

```bash
# on that machine: ask the server for its projects, infer directory mappings,
# probe installed agents, write worker.yaml, then run a self-check
gofer worker init --server http://<server>:8765 --id w-office --token gw_0f1c…
gofer worker -d                                  # daemon; foreground: gofer worker

gofer worker ls                                  # on the server: status=connected
gofer worker doctor                              # on the worker: config, token, roots, agents
gofer job run -p <project> -a exec --runner w-office --sync -- uname -a
```

The other direction works too, if `server.worker_enrollment` is enabled: `gofer worker join --server http://<server>:8765 --id w-office --start` prints a pairing code, and you accept it in the console under Runners.

### If the worker stays disconnected

- The `worker_id` must be identical in three places: the worker's own config, the key under `server.workers` on the server, and `runners.<name>.worker_id`. A mismatch is rejected at registration ("worker_id not bound to this token").
- The token is shown once. If you lost it, `gofer worker remove` and register again.
- The worker maps the server's project paths to its own directories through `roots`; a job is refused when the mapped directory does not exist on that machine. `gofer worker projects w-office` shows the effective mapping.
- `--runner` takes `server` (this host — `local` is the same runner, and the name storage and the API use), a worker id, or a peer name. `server` and `local` are reserved: no worker or custom runner may be called that.

## 7. Hand your terminal sessions over to the web

This is the part people keep using: a Claude Code / Codex / omp session running in your terminal stops and waits for you, and you answer from the browser or your phone.

```bash
gofer init hooks --agent all --global   # register sessions + inject memory at session start
gofer init skill --global               # install the gofer-usage skill for your agents
gofer session relay on                  # this session now waits on the web when it stops
```

`gofer session ls` lists sessions; before any hooks are installed it prints “no agent sessions registered”. From the CLI you can answer a waiting session with `gofer session say <sid> "…"`, wake an ended one with `gofer session resume <sid>`, or nudge it on a timer with `gofer session nudge <sid> --every 30m -m "status?"`.

If no session shows up: hooks are per agent and per machine — the session must start *after* `init hooks`, and the hook configs live in that agent's own config directory (`~/.claude`, `~/.codex`, `~/.omp/agent/extensions`).

## Next

- [concepts.md](concepts.md) — what a server, worker, runner, agent, project, job, plan and session are, and how they fit together.
- [features.md](features.md) — everything gofer does, one section per area, with links to the detailed references.
- [security.md](security.md) — read this before letting another person or machine in.
- [scenarios.md](../scenarios.md) — seven worked recipes (Chinese).
- Inside your agent: `gofer repo prime` (issues and memories from the repository), `gofer issue brief <id>`, `gofer plan brief <plan>`.
