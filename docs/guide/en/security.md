# Security

gofer's security model in one sentence: **holding a token means being able to execute commands on the server**. It is not a sandbox, not a multi-tenant platform, and there is no such thing as tenant isolation here. This page says what that implies and which knobs tighten it.

It is written for the deployment it is built for: **one developer, or a small team that trusts each other**, on machines you control, reachable only from networks you control.

## A token is a capability

- A server refuses to start without a token (`server.token_env` pointing at an environment variable is the recommended form; `server.token` writes it into the file and is not) unless you explicitly pass `--allow-empty-token` / set `server.allow_empty_token: true`, which is meant for experiments on a single machine.
- Anyone holding a token can submit jobs through the CLI, HTTP or MCP. A job is a process on the executing machine, running **with the permissions of the gofer process that runs it**. So give a token only to people you trust, or to a CI system you control.
- The other direction also holds: no token, no entry. Replace or delete the token and `gofer serve reload` and it is revoked — the same applies to any entry in `callers[]`.
- To see who you are and what you hold: `gofer auth whoami`. To see what a route requires: `gofer auth routes`.

## Listen address and network

- The default is `0.0.0.0:8765`. That is deliberate: a container has to reach the host through `host.docker.internal:8765`. It relies on the token and on your network boundary — **not** on the listen address being safe.
- For single-machine use, tighten it: `server.addr: 127.0.0.1:8765`. Open it to a LAN address only when other machines (workers, your phone, a colleague) need to connect.
- Do not expose the server directly to the internet. For remote access use a VPN, a tunnel, or a reverse proxy with TLS that only admits trusted sources.
- Behind a reverse proxy: pass the `Upgrade` header (WebSocket), disable response buffering, and keep the idle timeout above 60 seconds, or the console drops and log streams break.
- For HTTPS (installing the console as a phone app, web push, access across networks): configure `server.tls`, generate a self-signed CA and certificate with `gofer tool cert --hosts <server-ip>`, and follow [https-pwa.md](../../runbook/https-pwa.md).

## Several callers, and permission ceilings

With a single token everyone is the same identity (`default`). To tell people apart, and to limit them, use `server.callers[]`:

```yaml
server:
  callers:
    - id: ops
      name: Ops                        # display name only
      token_env: GOFER_CALLER_OPS_TOKEN
      max_concurrent_jobs: 4           # jobs running at once (the rest queue)
      rate_limit: 5                    # submissions per second
    - id: ci-bot
      token_env: GOFER_TOKEN_CI
      ceiling:                         # scoped ceiling: subtracts, never adds
        projects: ["app-*"]
        runners: ["w-linux-*"]
        agents: [codex, exec]
        allow_exec: true
```

- A **ceiling** is "the furthest this caller can reach": projects, runners, workers, agents, whether `exec` is allowed, whether it may write global memories, and whether it is read-only. What it can actually do is the intersection of the existing permission and the ceiling — both have to allow it. A caller without a `ceiling` behaves as before.
- When a `ceiling` is written, **the local runner (`local` / `server`) and `exec` are denied by default**; you have to list `runners: [local]` or `allow_exec: true` explicitly. Once you do, the ceiling no longer constitutes a security boundary (the local runner is a host shell), and the server prints a warning at startup.
- Permissions come in two layers: the **ceiling** decides what is in range, **permissions** decide what may be done with it. The fine-grained permissions (read other people's jobs, accept jobs, edit config, manage workers …) are catalogued in [`docs/reference/permissions.md`](../../reference/permissions.md). A root administrator can grant them temporarily to one terminal session from the console (1 / 4 / 24 hours, renewable) — **whoever can type in that terminal gets those permissions**.
- Out-of-range access is answered with "does not exist" (404), not "exists but forbidden".

Why both exist:

- **Session grant** — lend a few permissions to one terminal session for a while ("this Claude session may push branches for the next half hour"); it expires or is revoked when the session ends. Use it instead of giving a long-running agent permanent permissions.
- **Caller ceiling** — keep a token permanently inside a range ("this colleague's token may touch two projects, only codex, no exec"). Use it to hold an occasional visitor away from your core projects.

The question that comes up most: **writing a ceiling and then allowing the local runner means there is no ceiling**. A real boundary is "worker runners only, no exec, named projects" — those project directories are not visible to that person anyway.

## metrics and health endpoints

- `/health` and Prometheus `/metrics` are **outside** the `/v1` auth group.
- A config generated by `gofer init server` sets `server.metrics.enabled: false`. If the `metrics` section is missing altogether, the endpoint is on and unauthenticated (the behaviour of older configs) — `gofer config validate` warns about that, especially when the server listens on a non-loopback address.
- To keep it on and still authenticate: set `server.metrics.token` and have the scraper send `Authorization: Bearer <token>`.
- These endpoints expose counts and durations by caller / project / agent / runner. They do not expose job content.

## Secrets: keep them out of logs and job requests

- **`--env K=V` values are stored in the job request (`request_json`)**, so anyone who can see job details can read them. Do not pass secrets that way.
- For real secrets use `secrets`: the config only says *which environment variable of the gofer process* the value comes from, and the job refers to it by name.

  ```yaml
  secrets:
    gh-token:
      env: GITHUB_TOKEN        # the value lives in the server's environment or <config-dir>/.env
      as: GH_TOKEN             # variable name inside the job
  projects:
    <project>:
      secrets: [gh-token]      # which ones this project may use
  ```

  ```bash
  gofer job run -p <project> -a exec --secret gh-token --sync -- ./scripts/push.sh
  ```

  The value never enters the config or the job request, and it is replaced with `***` when job output is written to disk. Only the server's built-in runner is supported today; secrets on workers are not implemented.
- A job's environment has the server / worker token **stripped**; it gets a scoped job credential that is revoked when the job reaches a terminal state. So an agent running `gofer` inside its own job does not inherit your administrator rights.
- If a prompt or command looks like it contains a secret, submission warns you (`--no-secret-check` disables the check; be careful).
- Already leaked something? `gofer job secret-scan --literal-from-stdin` finds it across jobs; `gofer job redact <id> …` erases it from a request and its logs (needs the matching permission); `job secret-scan --redact --yes` also handles matching letters, and `gofer session mail redact <id>` does the same for the mailbox.
- Log files (`<config-dir>/run/*.log`) are JSONL with sensitive keys redacted and are rotated; that is not a reason to put secrets on a command line or in a prompt.
- **Screenshots are data too.** Images sent to a session are kept on the server (14 days by default, `storage.attachment_retention_days`) and in `tmp/gofer-attachments/` on the executing machine for 7 days, visible to anyone who can read the session. Terminal screenshots often contain tokens and internal addresses; look before you send.

## Execution-side boundaries

- A job's working directory is confined to the project directory (paths escaping it are rejected), and argv never goes through a shell.
- `exec` jobs require the project's `allow_exec: true`; interactive pty jobs require `allow_interactive: true`.
- Authorisation stays with the executor: a worker re-validates every dispatched job against **its own** config (project and agent allowlists, `exec` gate, cwd inside the project). A compromised server cannot make a worker run something the worker does not allow.
- `--hold` stops irreversible operations (`git push`, releases, outgoing messages) before they run, until a person approves; an agent cannot approve its own held job.
- Event hooks (`server.event_hooks`) and decision hooks (`server.decision_hooks`) are equivalent to running commands on the server: they can only be changed in the config file, and the write API refuses them.

## Checklist

- [ ] the token comes from an environment variable (`token_env`), never from the repository or the config file
- [ ] non-loopback listen addresses only where needed; public access goes through a VPN or a reverse proxy with TLS
- [ ] one `callers[]` entry per person / CI system, with a `ceiling` where it matters
- [ ] no `allow_exec` on important projects, or only for selected callers
- [ ] `/metrics` disabled, or protected by its own token
- [ ] secrets through `secrets` + `--secret`, never through `--env`, a prompt or a command line
- [ ] irreversible operations go through `--hold`, and `git push` still needs an explicit human go-ahead
- [ ] run `gofer job secret-scan` now and then to see what has slipped out
