# P3 policy-push e2e smoke (plan T7)

Fully isolated, re-runnable end-to-end smoke for the worker **policy-push** feature
(server computes a per-worker Policy → pushes it → worker projects it onto local
`roots`). It runs entirely inside the container against `127.0.0.1:18899` (+ a v3
matrix server on `:18900`) and **never touches** the live host server, the live
in-container worker, or any process it did not start.

## Run

```bash
# 1) build the three binaries into tmp/p3-t7/bin (see build-binaries.sh)
bash scripts/smoke/p3/build-binaries.sh

# 2) run the smoke (preflight先证伪 + 15 steps)
bash scripts/smoke/p3/run-smoke.sh
#   KEEP=1 bash scripts/smoke/p3/run-smoke.sh   # leave the stack up at the end
```

Verdicts stream to stdout and to `tmp/p3-t7/out/verdicts.txt`; all run artefacts
(logs, meta snapshots, config copies, the falsify logs) land under `tmp/p3-t7/`
(gitignored). Exit code is 0 only when `fail=0`.

## Safety invariants (baked into the script)

- **Preflight hard-assertion + 先证伪** (verification 25) runs BEFORE any process
  starts: only isolated ports (`18899`/`18900`) are allowed, non-loopback
  worker URLs, a non-`smoke-p3-w` worker_id, host_paths outside `tmp/p3-t7/projects/`,
  or storage paths outside `tmp/p3-t7/`. Five table-driven injections each prove
  the scan exits non-zero before boot.
- Only PIDs the script captured (`$SERVE_PID/$WPID/$V3SERVE_PID/$AUXPID`) are ever
  `kill`ed — via the EXIT trap. **No `pkill`/`killall`.**
- `serve` is always started with `--web-dir tmp/p3-t7/webdir` (an intentionally
  empty dir). **Never** `pnpm build`, never `web/dist`.
- Isolated `GOFER_CONFIG_DIR`, DB (`storage.db_path`), storage root, worker_id
  (`smoke-p3-w`), and independent tokens throughout.

## Commit-anchor mapping (IMPORTANT)

The plan cites old-binary commits `dcc98dd` / `c3ee6d1` / `4def378`, but the P3
branch was **rebased after T6**, so those short hashes no longer resolve. Their
semantic equivalents in the current history are used instead:

| plan hash | role | used here | why equivalent |
|---|---|---|---|
| `dcc98dd` | P3-before / rollback binary | `f11669d` | last commit before T0; `CurrentProtocolVersion=3`; has **no** `roots`/`guards` — reads `projects` only |
| `c3ee6d1` | v3 matrix (server & worker) | `f11669d` | same proto-v3, pre-roots build |
| `4def378` | v2 matrix worker | `tmp/gofer-old-v2` | a genuine proto-v2 binary reused from the T6 run (plan §16: "复用 P1 的旧二进制"); proto=2 is re-verified from `serve.log` on every run |

## 15-step → verification map

| step | verification | what it proves |
|---|---|---|
| A  | 25 | isolated-stack preflight + 5-case先证伪 (exit non-zero before boot) |
| 1  | 1  | LEGACY zero-break: old(v3) vs P3 `/v1/meta` diff empty; server pushes 4 keys, worker keeps its 3 local; exec + tty-fixture jobs run |
| 2  | 3  | switch to POLICY; server adds `proj-e` via SIGHUP; worker PID stable; job cwd = roots-mapped local path |
| 3  | 4  | `POST /v1/projects` (web write) re-pushes with **no** SIGHUP |
| 4  | 8  | server project outside every root → rejected, absent from the worker |
| 5  | 9  | `SIGHUP` the worker → projects kept (POLICY re-projects LKG, not 0) |
| 6  | 10 | worker adds a root + reload → the once-rejected project becomes accepted; PID stable |
| 7  | 11 | `guards.allow_exec:false` → exec rejected; back to true → runs |
| 8  | 12 | `allowed_runners:[]` → pushed to no worker (empty ≠ wildcard) |
| 9  | 13 | `allowed_agents` keeps `tty-codex` verbatim; submitting it errors clearly (codex absent) |
| 10 | 14 | `max_concurrent_jobs:1` serialises; `capture_diff:false` suppresses the diff (positive control: default-ON project DOES capture) |
| 11 | 15 | a fan-out workflow converges to `done` across a worker reconnect (policy_pending never hard-fails it) |
| 12 | 16 | rolling matrix on `:18900`: POLICY cold-start on a v3 server (0 projects, loud warn, online); already-activated POLICY on v3 keeps LKG (no wipe); proto-v2 worker connects + runs |
| 13 | 18 | `project list` / `config validate` in both LEGACY and POLICY forms |
| 14 | 17 | `go list -deps ./internal/wshub` = exactly one gofer dep (`wsproto`) |
| 15 | 24 | rollback: `roots+projects` old binary → safe (uses local projects); `roots`-only old binary → 0 projects (correctly an unsafe rollback point) |

### Coverage notes (honest)

- The `Applied{Degraded:legacy_local_projects}` receipt (verification 1) and the
  verbatim `AllowedAgents` non-intersection (verification 13) are **not exposed over
  HTTP** — they are钉死 by unit tests (`internal/worker` / `commands/worker_policy_test`).
  The e2e asserts the observable behavioural outcome.
- The precise ~ms policy_pending / session-generation / latest-wins races (B4/B5,
  verifications 21/22) are covered by the T5 `-race` unit tests (per plan line 990);
  step 11 asserts only the e2e-observable convergence.
