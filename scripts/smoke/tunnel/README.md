# TCP tunnel smoke (TUN-01)

End-to-end check of `gofer tunnel` against a fully isolated local stack: one `gofer serve`,
one worker with a `tunnel.allow` list, and local TCP echo servers standing in for devices.
Design: [`docs/design/2026-09-11-tcp-tunnel-design.md`](../../../docs/design/2026-09-11-tcp-tunnel-design.md).

```bash
bash scripts/smoke/tunnel/run-smoke.sh          # exit code = number of failed checks
KEEP=1 bash scripts/smoke/tunnel/run-smoke.sh   # keep the stack running afterwards
SMOKE_ADDR=127.0.0.1:19911 bash scripts/smoke/tunnel/run-smoke.sh
```

Requirements: bash, `go`, `curl`, `jq` (Linux/macOS). Everything is built from the current
tree into `tmp/tunnel-smoke/` and runs with a private `GOFER_CONFIG_DIR`; the script only
stops the processes it started and never talks to another gofer server.

| Step | Verifies |
|---|---|
| S1 | `tunnel check` to an allowed target succeeds |
| S2 | target outside the worker allowlist → HTTP 403 |
| S3 | allowed target with nobody listening → HTTP 502 |
| S4 | a worker token cannot open tunnels → HTTP 403 |
| S5 | `tunnel forward` with two specs; 200 KiB echo roundtrip through each |
| S6 | `tunnel ls` / `GET /v1/tunnels` shows an open tunnel and is empty after close |
| S7 | worker `max_conns: 2` → the third concurrent tunnel gets HTTP 429 |
| S8 | editing `tunnel.allow` + `gofer worker reload` makes a new target reachable |

Logs (serve / worker / forward / verdicts) land in `tmp/tunnel-smoke/out/`.
