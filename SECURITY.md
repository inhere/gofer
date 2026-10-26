# Security policy

## Supported versions

gofer is pre-1.0: **only the latest release gets fixes**. Please reproduce on it — or on `main` — before reporting; `gofer --version` prints the version and commit you are running. Older releases are not patched.

## Reporting a vulnerability

Please report privately. **Do not open a public issue**, and do not paste tokens, internal addresses, host names or project paths into an issue, a discussion or a screenshot.

- Preferred: GitHub private reporting on this repository — **Security** tab → **Report a vulnerability** ([direct link](https://github.com/inhere/gofer/security/advisories/new)). You get a private advisory where we can discuss the report, agree on a fix and publish an advisory once a release is out.
- If that path is unavailable to you, contact the maintainer through the address on their GitHub profile and say in the first message that it is a security report; the thread moves to a private advisory.

A useful report has: the output of `gofer --version`, OS and architecture, how you deployed it (server, worker, container), the smallest reproduction you can manage, and the impact you see (what an attacker gains, what they need to start with). We aim to acknowledge within a week; there is no bug bounty. Please give us a reasonable window to ship a fix before disclosing publicly, and we will credit you in the advisory unless you prefer otherwise.

## Trust model

gofer is self-hosted, built for one developer or a small team that trusts each other, and it is a launcher and supervisor rather than a sandbox:

- **A token is the ability to run commands on the machines that execute jobs.** `exec` jobs run as the user of the gofer process, and an agent job can do whatever that agent CLI can do there.
- There is no per-user isolation, no multi-tenant mode and no container sandbox in the core. Jobs are kept apart by project admission rules, a directory lock and — when you ask for it — a git worktree per job, not by an OS boundary.
- The server is meant for your machine, a private network or a VPN. Do not put it on the public internet without a reverse proxy, TLS and network filtering; the default listen address assumes your network boundary does the rest.
- Everything that reaches the server comes in as a token, a worker connection, a hook or an update source; each has a switch that narrows it, listed in the security guide below.
- Job credentials are scoped and revoked when the job ends, so an agent running `gofer` inside its own job does not inherit your admin token. Secrets passed with `--secret` are read from the server's environment and redacted from job output (server runner only for now); values given with `--env` are stored in the job request and are visible to anyone who can read that job.

## Where the details are

- [Security guide](docs/guide/security.md) — token and caller ceilings, listen address, secrets, log redaction, metrics endpoints, and a one-page checklist before you let another machine or person in. Written in Chinese, like most of `docs/`.
- [Stability and upgrades](docs/guide/stability.md) — what the `0.x` line promises, how upgrades and rollbacks work.
- [HTTP API](docs/reference/http-api.md) and [permission catalogue](docs/reference/permissions.md) — what each route and permission requires.
