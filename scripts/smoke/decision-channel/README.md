# Decision-channel (gofer_ask_human) e2e smoke (plan T5)

Fully isolated, re-runnable end-to-end 演练 for the decision channel
(plan `docs/plans/2026-07-26-decision-channel-plan.md` T5, design §C3).
It runs against `127.0.0.1:19091` with all data under `tmp/decision-channel/`
and **never touches** the live gofer server, its ports, or its data.
Windows host / Git Bash; binaries carry `.exe`.

## Run

```bash
# 1) build the two binaries into tmp/decision-channel/bin
bash scripts/smoke/decision-channel/build-binaries.sh

# 2) run the smoke (preflight + 2 rounds)
bash scripts/smoke/decision-channel/run-smoke.sh
#   KEEP=1 bash scripts/smoke/decision-channel/run-smoke.sh   # leave the serve up
```

Verdicts stream to stdout and `tmp/decision-channel/out/verdicts.txt`; tool
results land in `out/call1.json` / `out/call2.json`, serve log in
`out/serve.log`. Exit code is 0 only when `fail=0`.

## What it proves (验收 2 的两分支,真实部署形态)

- **round 1 (answered)**: a REAL `gofer mcp` subprocess (client mode, forwarding
  to the isolated central serve) blocks inside `gofer_ask_human`; a "human"
  answers through the HTTP API (`POST /v1/decisions/{id}/answer`, the web 作答
  落点); the tool returns `{state:"answered", answer}` with the answer verbatim.
- **round 2 (expired)**: nobody answers; the tool returns `{state:"expired"}`
  after ~timeout_sec (5s), and `gofer plan decisions --state EXPIRED` (CLI 三面
  一致) shows the row while no ghost OPEN decisions remain.

## Pieces

- `mcpcall/main.go` — one-shot MCP stdio driver: spawns `gofer mcp --server …`,
  handshakes (go-sdk `CommandTransport`), calls one tool, prints the raw
  `CallToolResult` JSON. Exists because the smoke must exercise a real stdio
  MCP subprocess, not an in-memory transport.
- `build-binaries.sh` — builds `gofer.exe` + `mcp-call.exe` into
  `tmp/decision-channel/bin`.
- `run-smoke.sh` — preflight isolation assertions (loopback-only port, storage
  under `tmp/decision-channel/`, refuse to boot over an already-listening port,
  trap kills only its own serve PID) + the two rounds.

Requires: `go`, `curl`, `python` (JSON extraction; no jq on this host).
