# Features

What gofer offers, by area, one line per capability. The full usage reference is the [gofer-usage skill](../../../skills/gofer-usage/SKILL.md) (written for agents, kept in sync with every release) and its [command reference](../../../skills/gofer-usage/references/commands.md); flags are always whatever `gofer <command> -h` says, and HTTP endpoints are in [`docs/reference/http-api.md`](../../reference/http-api.md).

Both the skill and the linked runbooks are Chinese today; the commands, flags and field names in them are the same ones you type.

## Job

- Submit from CLI flags, from a markdown task file with YAML frontmatter (`-f task.md`), or from a task-book template rendered by the server (`-t <name> --var k=v`, `gofer template ls|show`).
- Sync or async (`--sync`, `--wait`), plus `--read-only`, `--model`, `--max-tokens / --max-cost / --max-turns`, `--timeout`, `--retry`, `--fallback`, `--env`, `--upload` / `--collect`.
- Isolation: a directory lock per working directory (`--lock`, `--shared-dir`, `--exclusive-dir`, `--lock-wait`) and managed worktrees (`--worktree`, `gofer job worktree ls|merge|rm`).
- Continue work: `job resume` (same agent session, `--mode`, `--agent` to switch within the family), `job rerun`, `--from-session`, automatic resume on provider-side errors, failover to a backup agent.
- Interaction: `--interactive` pty jobs can be attached in the browser; `--session` opens a persistent ACP session.
- Wake-ups: `gofer job wakeup create` resubmits a finished job on a timer or on an event.

Reference: [jobs in depth](../../../skills/gofer-usage/references/job-advanced.md) · [worktrees](../../runbook/parallel-jobs-with-worktree.md) · [retry](../../runbook/2026-09-22-job-retry-runbook.md).

## Review and quality gates

- `--verify '<cmd>'`: after the agent stops, run a check on the executing machine; non-zero exit fails the job.
- `--review`: the job stops at `needs_review`; only a person can accept it (`gofer job accept`) or reject it (`gofer job reject --note … --resume`).
- `--hold [--hold-reason "…"]`: the job stops at `awaiting_approval` **before** it runs, for outward or irreversible operations the agent's own permissions block (a `git push`, a release, an outgoing message). A person approves it in the job page (works on a phone) or with `gofer job approve <id>`; a rejection or a timeout cancels it and nothing runs. An agent cannot approve its own held job.
- `--acceptance` and `--scope` go into the prompt and into the review panel; changes outside the scope are flagged, and out-of-scope findings can be filed as issues with `gofer job findings --create-issues`.
- Uncommitted-changes guard (`on_uncommitted`), a tool-call approval gate for ACP agents (project `approval`), enforced rules (`gofer agent rule`) and skill binding (`gofer agent skill`).

Reference: [hold and approval](../../../skills/gofer-usage/references/hold-approval.md) · [ACP agent auth and approvals](../../runbook/2026-09-25-acp-agent-auth-runbook.md).

## Orchestration

- Plans and todos: `gofer plan create|add-todo|set-todo|run|pause|resume|dispatch|import`, plan handoff notes, decisions (`gofer plan ask`, MCP `gofer_ask_human`), kanban view in the console.
- Workflows: `gofer workflow run|show|pick|template`, several agents on the same step with a comparison and a pick.
- Schedules: `gofer schedule add|run|enable|disable`, cron or one-off, optional webhook trigger.
- A `@agent` mention in a comment dispatches a job for it (only mentions in a *person's* comment dispatch); leader rounds can be turned on per plan.

Reference: [plans and workflows](../../../skills/gofer-usage/references/plans-workflows.md) · [schedules](../../runbook/2026-06-30-cron-schedule-runbook.md) · [comments](../../runbook/2026-09-23-comments-runbook.md).

## Sessions and human in the loop

- Terminal session relay: after `gofer init hooks`, a stopped Claude Code / Codex / omp session can wait for a reply in the browser (`gofer session relay auto|on|off`, `session say`), be woken up (`session resume`) or nudged on a timer (`session nudge`).
- Claude Code permission prompts and multiple-choice questions are mirrored into the web console; ACP and pty sessions are visible in the workbench; with the optional HTTPS listener (`server.tls`, `gofer tool cert`) you can install the console as a PWA and receive web push.
- The "Today" home page is one queue of everything waiting on your decision (interactions, plan decisions, reviews, approvals, work items, unassigned letters), with parallel lanes, postpone and focus mode.
- Work items and the steward: `gofer work …` tracks what you are doing across sessions; the optional steward (`gofer steward …`) tidies them up with a restricted credential. `gofer work capture "…"` drops things into the capture box for later dispatch.
- Mailbox: `gofer session mail send|ls|read|reply|cancel` between people, sessions and jobs; `/mail` in the console.

Reference: [sessions and relay](../../../skills/gofer-usage/references/sessions-relay.md) · [session relay runbook](../../runbook/session-relay.md) · [work items](../../../skills/gofer-usage/references/work-items.md) · [mailbox](../../runbook/2026-10-18-mailbox-runbook.md) · [web push](../../runbook/web-push.md).

## Repository tracker and agent memory

- `gofer repo init|status|prime|sync|migrate`, `gofer issue …`, `gofer memory …` — issues and memories live in `.gofer/tracker/*.jsonl` and are committed with the code; global and project memories live on the server.
- `repo prime` (installed as a SessionStart hook) gives a new session the current focus, open issues, rules and the memory index; `gofer issue brief` / `gofer plan brief` hand over a task in one command.
- `memory flag` reports a memory as stale, `memory doctor` checks for staleness, and `memory candidates|accept|reject` turns the reusable lessons of delivered jobs into memories.
- The tracker JSONL files have a git merge driver (`gofer repo merge-driver`), so branches that each change issues merge cleanly.

Reference: [tracker](../../../skills/gofer-usage/references/tracker.md) · [tracker transfer](../../runbook/tracker-transfer.md).

## Workers, network and operations

- Workers: `gofer worker add|init|doctor|show|projects|reload|upgrade|remove`, two config modes (LEGACY and POLICY), reconnection recovery; self-enrollment via `gofer worker join` (off by default).
- Tunnels: `gofer tunnel forward|check|ls|save|stop` forwards TCP/UDP within a worker's whitelist (including SOCKS / HTTP CONNECT dynamic forwarding); presets live on the server and the server can host the forward.
- Files: `gofer tool cp <src> <runner>:<project>/<path>` in both directions, and `gofer tool xfer` for the staging area; jobs can also `--upload` / `--collect`.
- Managed server: `gofer serve register|start|stop|restart|status|logs|upgrade|uninstall` (a Windows logon task or a systemd unit), and `gofer serve reload` to re-read the config without restarting.

Reference: [worker config](../../../skills/gofer-usage/references/worker-config.md) · [container worker](../../runbook/container-worker.md) · [tunnels](../../runbook/tcp-tunnel.md) · [files and wake-ups](../../../skills/gofer-usage/references/files-and-wakeup.md) · [managed server](../../runbook/2026-10-08-serve-management-runbook.md).

## Observability and security

- An event timeline, diff and commits, per-job usage and cost, a stats dashboard (`/dashboard`), Prometheus `/metrics`, and JSONL file logs with rotation and redaction.
- Notifications over webhooks and the DingTalk / Feishu adapters (`server.notification`); event hooks (`server.event_hooks`) and decision hooks (`server.decision_hooks`).
- Job-scoped credentials, caller tokens with optional capability ceilings and quotas, temporary session grants, `gofer job redact`, `job delete`, `job secret-scan`.
- The trust model, deployment advice and what is and is not redacted are in [security.md](security.md); the permission catalogue is [`docs/reference/permissions.md`](../../reference/permissions.md).

Reference: [operations](../../../skills/gofer-usage/references/operations.md) · [caller ceilings](../../runbook/2026-10-18-caller-ceiling-runbook.md) · [job credentials](../../runbook/2026-09-23-job-credentials-runbook.md) · [notifications](../../runbook/im-notification.md).

## Entry points

- **CLI** — commands are grouped by `gofer -h`: Setup & config / Repository tracking / Control plane / Jobs & workflows / Utilities.
- **HTTP** — `/v1/*` needs `Authorization: Bearer <token>`; `/health` and Prometheus `/metrics` are outside it (`/metrics` can carry its own token).
- **MCP** — `gofer mcp` is a stdio MCP server that forwards to the server in `GOFER_SERVER_ADDR` (`--standalone` executes in-process, `--project` narrows it to one project):

  ```json
  { "mcpServers": { "gofer": { "command": "/abs/path/to/gofer", "args": ["mcp"] } } }
  ```

- **Web console** — embedded in `gofer serve` (`--no-web` turns it off): Today, Workbench, Works, Plans, Jobs, Sessions, with the rest under "more" (Issues, Dashboard, Agents, Runners, Projects, Workflows, Schedules), plus settings, `/mail` and `/review`.
- **Session hooks** — `gofer init hooks` registers Claude Code / Codex / omp / jcode sessions (relay plus memory injection) through the same HTTP API.

Reference: [command reference](../../../skills/gofer-usage/references/commands.md) · [HTTP API](../../reference/http-api.md) · [web console](../../reference/web-console.md).

## Outputs and artifacts

- A job's stdout / stderr land in the project's own `tmp/gofer/<job-id>/` (change with `storage.root` or a project's `exchange_subdir`); the database holds status, events and indexes, and logs can be pruned by retention settings.
- For NDJSON-emitting agents such as Claude Code and Codex, stdout holds only the final answer and process events go to stderr, which the web console renders as a structured view.
- At the end of a job, files matching a glob in its cwd can be collected as artifacts (`--collect`), and files can be pulled from another machine with `tool cp`.
