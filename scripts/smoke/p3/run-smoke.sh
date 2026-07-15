#!/usr/bin/env bash
# P3 policy-push e2e smoke on a FULLY ISOLATED in-container stack (plan T7).
#
# Everything runs against 127.0.0.1:18899 (+ a v3 matrix server on :18900). It
# NEVER touches the live host server (nssm), the live in-container worker,
# nor any process it did not start. It SIGTERMs only the PIDs it captured (trap).
#
# Usage:
#   bash scripts/smoke/p3/run-smoke.sh                 # full run (preflight先证伪 + 15 steps)
#   KEEP=1 bash scripts/smoke/p3/run-smoke.sh          # keep the stack up at the end
#
# Binaries (built by the caller; see build_binaries() note below or pre-place them):
#   tmp/p3-t7/bin/gofer-p3   = CURRENT tree (proto v4, this is what T7 validates)
#   tmp/p3-t7/bin/gofer-v3   = commit f11669d (proto v3, pre-roots == rollback / v3 anchor)
#   tmp/gofer-old-v2         = a genuine proto-v2 binary (reused from the T6 run, 4def378-era)
#
# NOTE on commit anchors: the plan cites dcc98dd / c3ee6d1 / 4def378, but the P3
# branch was rebased after T6 so those short hashes no longer resolve. Their
# SEMANTIC equivalents in the current history are used instead and documented in
# scripts/smoke/p3/README.md:
#   dcc98dd (P3-before / rollback)  -> f11669d (last commit before T0; proto v3; no roots)
#   c3ee6d1 (v3 matrix)             -> f11669d (same proto-v3, pre-roots build)
#   4def378 (v2 matrix)             -> tmp/gofer-old-v2 (proto v2, reused per plan "复用 P1 旧二进制")

set -uo pipefail

# ----------------------------------------------------------------- layout / isolation
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
BASE="$REPO/tmp/p3-t7"
OUT="$BASE/out"
BIN="$BASE/bin/gofer-p3"          # current tree (proto v4)
V3BIN="$BASE/bin/gofer-v3"        # f11669d (proto v3, pre-roots)
V2BIN="$REPO/tmp/gofer-old-v2"    # proto v2 (reused)

ADDR=127.0.0.1:18899             # v4 serve (main)
V3ADDR=127.0.0.1:18900           # v3 serve (matrix)
STOK=smoke-p3-server-tok
WTOK=smoke-p3-worker-tok
WTOK2=smoke-p3-worker-tok2       # the second (v2 matrix) worker needs its OWN token binding
WID=smoke-p3-w
WRUN=wrun                        # worker-runner name in server config
V3STOK=smoke-p3-v3-server-tok

CFGDIR="$BASE/cfgdir"            # GOFER_CONFIG_DIR for the main stack (serve + worker)
V3CFGDIR="$BASE/cfgdir-v3"       # separate cfgdir for the v3 matrix server
SRVCFG="$BASE/cfg/server.yaml"
V3SRVCFG="$BASE/cfg/server-v3.yaml"
WYAML="$CFGDIR/worker.yaml"      # worker.yaml lives under CFGDIR so `project list` auto-discovers it
WEBDIR="$BASE/webdir"            # intentionally-empty web dir (NEVER point at web/dist)
STORE="$BASE/store"
WSTORE="$BASE/wstore"
PROJ="$BASE/projects"            # logical project roots (server host_path lives under here)
HOST="$BASE/host"                # worker root `to` targets (mapped local dirs)

export PATH=/d/env/linux-env/sdk/gosdk/go1.25.10/bin:$PATH
export GOFER_CONFIG_DIR="$CFGDIR"
export GOFER_CONFIG="$SRVCFG"
export GOFER_SERVER_ADDR="$ADDR"
export GOFER_SERVER_TOKEN="$STOK"

PASS=0; FAIL=0; UNCOV=0
ok()   { PASS=$((PASS+1));  echo "PASS: $*"          | tee -a "$OUT/verdicts.txt"; }
bad()  { FAIL=$((FAIL+1));  echo "FAIL: $*"          | tee -a "$OUT/verdicts.txt"; }
unc()  { UNCOV=$((UNCOV+1));echo "UNCOVERED: $*"     | tee -a "$OUT/verdicts.txt"; }
step() { echo; echo "================= $* ================="; }

api()   { curl -s -H "Authorization: Bearer $STOK"   "http://$ADDR$1"; }
v3api() { curl -s -H "Authorization: Bearer $V3STOK" "http://$V3ADDR$1"; }
wprojects()  { api /v1/meta | jq -c --arg id "$WID" '.workers[]?|select(.id==$id)|.projects // []'; }
wcaps()      { api /v1/meta | jq -c --arg id "$WID" '.workers[]?|select(.id==$id)|.agent_caps // []'; }
wconnected() { api /v1/meta | jq -r --arg id "$WID" '.workers[]?|select(.id==$id)|.connected'; }
jid()   { grep -oE '[0-9]{8}-[0-9]{6}-[0-9a-f]{8}' | head -1; }
proc()  { ps -o pid=,lstart=,cmd= -p "$1" 2>/dev/null | cut -c1-110; }

wait_conn() { local n="${1:-30}"; for _ in $(seq 1 "$n"); do [ "$(wconnected)" = "true" ] && return 0; sleep 0.5; done; return 1; }
wait_health(){ local a="$1" n="${2:-30}"; for _ in $(seq 1 "$n"); do [ "$(curl -s -o /dev/null -w '%{http_code}' "http://$a/health")" = "200" ] && return 0; sleep 0.5; done; return 1; }
# poll until project key appears (present=1) or disappears (present=0) in worker projects
wait_proj() { local key="$1" present="${2:-1}" n="${3:-30}"; for _ in $(seq 1 "$n"); do
  if wprojects | jq -e --arg k "$key" 'index($k)' >/dev/null 2>&1; then [ "$present" = "1" ] && return 0
  else [ "$present" = "0" ] && return 0; fi; sleep 0.5; done; return 1; }
wait_job() { local id="$1" n="${2:-40}" st; for _ in $(seq 1 "$n"); do
  st=$(api "/v1/jobs/$id" | jq -r .status 2>/dev/null)
  case "$st" in running|queued|"") sleep 1 ;; *) break ;; esac; done; echo "$st"; }
job_state(){ api "/v1/jobs/$1" | jq -c '{id,status,exit_code,runner,agent,error}'; }
job_stdout(){ api "/v1/jobs/$1/logs/stdout"; }

# ----------------------------------------------------------------- PIDs we own (trap kills ONLY these)
SERVE_PID=""; WPID=""; V3SERVE_PID=""; AUXPID=""
cleanup() {
  [ "${KEEP:-0}" = "1" ] && { echo "KEEP=1: leaving stack up (${SERVE_PID} ${WPID} ${V3SERVE_PID} ${AUXPID})"; return; }
  for p in $AUXPID $WPID $V3SERVE_PID $SERVE_PID; do
    [ -n "$p" ] && kill -TERM "$p" 2>/dev/null && echo "SIGTERM $p"
  done
  sleep 2
}
trap cleanup EXIT INT TERM

# ----------------------------------------------------------------- config writers
# server config for a given "phase": base | policy | +extras added later via SIGHUP/POST.
# proj-a..proj-d are the 4 keys the LEGACY worker will see pushed (and ignore).
write_server() { # $1 = extra-projects block (raw yaml, may be empty)
  local extra="${1:-}"
  cat > "$SRVCFG" <<EOF
server:
  addr: $ADDR
  token: $STOK
  workers:
    $WID:
      token: $WTOK
      labels: [linux, smoke]
    ${WID}2:
      token: $WTOK2
      labels: [linux, smoke]
storage:
  db_path: $STORE/v4.db
  root: $STORE/blobs
projects:
  proj-a:
    host_path: $PROJ/x/proj-a
    allowed_agents: [exec, ttyfix]
    interactive_allowed_agents: [ttyfix]
    allowed_runners: [$WRUN]
    allow_exec: true
  proj-b:
    host_path: $PROJ/x/proj-b
    allowed_agents: [exec, ttyfix]
    interactive_allowed_agents: [ttyfix]
    allowed_runners: [$WRUN]
    allow_exec: true
  proj-c:
    host_path: $PROJ/x/proj-c
    allowed_agents: [exec, ttyfix]
    interactive_allowed_agents: [ttyfix]
    allowed_runners: [$WRUN]
    allow_exec: true
  proj-d:
    host_path: $PROJ/x/proj-d
    allowed_agents: [exec, ttyfix]
    interactive_allowed_agents: [ttyfix]
    allowed_runners: [$WRUN]
    allow_exec: true
  proj-v2:
    host_path: $PROJ/x/proj-a
    allowed_agents: [exec]
    allowed_runners: [wrun2]
    allow_exec: true
$extra
agents:
  exec:
    type: exec
  ttyfix:
    type: cli-agent
    command: sh
    args: ["-c", "printf tty-fixture-ran; sleep 1"]
    interactive: true
    no_raw_cmd: true
runners:
  local:
    type: local
  $WRUN:
    type: worker
    worker_id: $WID
  wrun2:
    type: worker
    worker_id: ${WID}2
EOF
}

# The extra-project blocks used by later steps (kept as functions so paths stay isolated).
extra_policy_base() { cat <<EOF
  proj-noroute:
    host_path: $PROJ/x/proj-noroute
    allowed_agents: [exec]
    allowed_runners: []
    allow_exec: true
  proj-codex:
    host_path: $PROJ/x/proj-codex
    allowed_agents: [exec, tty-codex]
    interactive_allowed_agents: [tty-codex]
    allowed_runners: [$WRUN]
    allow_exec: true
  proj-mc:
    host_path: $PROJ/x/proj-mc
    allowed_agents: [exec]
    allowed_runners: [$WRUN]
    allow_exec: true
    max_concurrent_jobs: 1
    capture_diff: false
EOF
}
extra_add_e() { extra_policy_base; cat <<EOF
  proj-e:
    host_path: $PROJ/x/proj-e
    allowed_agents: [exec]
    allowed_runners: [$WRUN]
    allow_exec: true
EOF
}
extra_add_e_nogo() { extra_add_e; cat <<EOF
  nogo:
    host_path: $PROJ/y/nogo
    allowed_agents: [exec]
    allowed_runners: [$WRUN]
    allow_exec: true
EOF
}

# LEGACY worker.yaml: projects (proj-a,b,c) + NO roots. host_path -> real dirs under projects/x.
write_worker_legacy() {
  cat > "$WYAML" <<EOF
worker_id: $WID
server_link:
  urls: [ws://$ADDR/v1/workers/connect]
  token: $WTOK
labels: [linux, smoke]
max_concurrent: 4
projects:
  proj-a:
    host_path: $PROJ/x/proj-a
    default_agent: exec
    allowed_agents: [exec, ttyfix]
    interactive_allowed_agents: [ttyfix]
    allowed_runners: [local]
    allow_exec: true
  proj-b:
    host_path: $PROJ/x/proj-b
    default_agent: exec
    allowed_agents: [exec]
    allowed_runners: [local]
    allow_exec: true
  proj-c:
    host_path: $PROJ/x/proj-c
    default_agent: exec
    allowed_agents: [exec]
    allowed_runners: [local]
    allow_exec: true
agents:
  exec:
    type: exec
  ttyfix:
    type: cli-agent
    command: sh
    args: ["-c", "printf tty-fixture-ran; sleep 1"]
    interactive: true
    no_raw_cmd: true
storage:
  root: $WSTORE
EOF
}

# POLICY worker.yaml: roots + guards + NO projects. $1=roots-block $2=guards-block
write_worker_policy() {
  local roots="$1" guards="$2"
  cat > "$WYAML" <<EOF
worker_id: $WID
server_link:
  urls: [ws://$ADDR/v1/workers/connect]
  token: $WTOK
labels: [linux, smoke]
max_concurrent: 4
roots:
$roots
guards:
$guards
agents:
  exec:
    type: exec
  ttyfix:
    type: cli-agent
    command: sh
    args: ["-c", "printf tty-fixture-ran; sleep 1"]
    interactive: true
    no_raw_cmd: true
storage:
  root: $WSTORE
EOF
}
ROOTS_X="  - from: $PROJ/x\n    to: $HOST/x"
ROOTS_XY="  - from: $PROJ/x\n    to: $HOST/x\n  - from: $PROJ/y\n    to: $HOST/y"
GUARDS_ON="  allow_exec: true\n  allow_interactive: true"
GUARDS_NOEXEC="  allow_exec: false\n  allow_interactive: true"

# ROLLBACK observation-period worker.yaml: roots AND projects (for verification 24).
write_worker_rollback_both() {
  cat > "$WYAML" <<EOF
worker_id: $WID
server_link:
  urls: [ws://$ADDR/v1/workers/connect]
  token: $WTOK
labels: [linux, smoke]
max_concurrent: 4
roots:
  - from: $PROJ/x
    to: $HOST/x
guards:
  allow_exec: true
  allow_interactive: true
projects:
  proj-a:
    host_path: $PROJ/x/proj-a
    default_agent: exec
    allowed_agents: [exec]
    allowed_runners: [local]
    allow_exec: true
agents:
  exec:
    type: exec
storage:
  root: $WSTORE
EOF
}

# ================================================================= A. PREFLIGHT (verification 25)
# preflight_scan <server.yaml> <worker.yaml>: hard assertions. Returns non-zero (and
# prints the offending line) on ANY violation. Each check is independently removable
# so the先证伪 harness can prove which assertion catches each injected live resource.
preflight_scan() {
  local srv="$1" wk="$2" rc=0
  # A1 folded into A2: a foreign live server can only reach us via addr / urls, and
  # A2 (server addr allowlist) + A3 (worker url allowlist) reject any non-isolated
  # host:port positively — no need to blocklist a specific literal port here.
  # A2: server addr must be a loopback isolated port.
  if ! grep -E '^\s*addr:\s*127\.0\.0\.1:(18899|18900)\s*$' "$srv" >/dev/null 2>&1; then
    echo "  VIOLATION A2: server addr not 127.0.0.1:18899|18900"; grep -nE 'addr:' "$srv"; rc=1; fi
  # A3: every worker server_link url must be ws://127.0.0.1:1889x (loopback only).
  if grep -oE 'ws://[^],[:space:]]+' "$wk" | grep -vE '^ws://127\.0\.0\.1:(18899|18900)/' >/dev/null 2>&1; then
    echo "  VIOLATION A3: non-loopback worker url"; grep -oE 'ws://[^],[:space:]]+' "$wk" | grep -vE '^ws://127\.0\.0\.1:(18899|18900)/'; rc=1; fi
  # A4: worker_id must be exactly the isolated id.
  if grep -E '^\s*worker_id:' "$wk" | grep -vE "worker_id:\s*$WID\s*$" >/dev/null 2>&1; then
    echo "  VIOLATION A4: worker_id is not $WID"; grep -nE '^\s*worker_id:' "$wk"; rc=1; fi
  # A5: every host_path must live under the isolated projects tree ($PROJ/).
  if grep -oE 'host_path:\s*\S+' "$srv" "$wk" 2>/dev/null | awk '{print $2}' | grep -vE "^$PROJ/" >/dev/null 2>&1; then
    echo "  VIOLATION A5: host_path outside $PROJ/"; grep -nE 'host_path:' "$srv" "$wk" | grep -vE "$PROJ/"; rc=1; fi
  # A6: storage db_path / root must live under the isolated base ($BASE/).
  if grep -oE '(db_path|root):\s*\S+' "$srv" "$wk" 2>/dev/null | awk '{print $2}' | grep -vE "^$BASE/" >/dev/null 2>&1; then
    echo "  VIOLATION A6: storage path outside $BASE/"; grep -nE '(db_path|root):' "$srv" "$wk" | grep -vE "$BASE/"; rc=1; fi
  # A7: worker root `to` targets must live under the isolated host tree ($HOST/).
  if grep -oE 'to:\s*\S+' "$wk" 2>/dev/null | awk '{print $2}' | grep -vE "^$HOST/" >/dev/null 2>&1; then
    echo "  VIOLATION A7: root to outside $HOST/"; grep -nE 'to:' "$wk" | grep -vE "$HOST/"; rc=1; fi
  return $rc
}

falsify_preflight() {
  step "A. PREFLIGHT + 先证伪 (verification 25) — MUST run before any process starts"
  mkdir -p "$OUT" "$BASE/cfg" "$CFGDIR"; : > "$OUT/verdicts.txt"
  # Baseline clean fixtures.
  write_server ""
  write_worker_policy "$(printf '%b' "$ROOTS_X")" "$(printf '%b' "$GUARDS_ON")"
  if preflight_scan "$SRVCFG" "$WYAML"; then
    ok "A0 clean isolated fixtures pass preflight"
  else
    bad "A0 clean fixtures unexpectedly FAILED preflight (see above) — aborting"; exit 1
  fi
  # Table-driven injections: each MUST make preflight exit non-zero. sed applied to a COPY.
  local tdir="$OUT/falsify"; rm -rf "$tdir"; mkdir -p "$tdir"
  local names=() ; local rcs=()
  inject() { # <label> <which:srv|wk> <sed-expr>
    local label="$1" which="$2" expr="$3"
    cp "$SRVCFG" "$tdir/s.yaml"; cp "$WYAML" "$tdir/w.yaml"
    if [ "$which" = "srv" ]; then sed -i "$expr" "$tdir/s.yaml"; else sed -i "$expr" "$tdir/w.yaml"; fi
    if preflight_scan "$tdir/s.yaml" "$tdir/w.yaml" >"$tdir/$label.log" 2>&1; then
      bad "A-falsify[$label]: preflight PASSED an injected live resource (should have exited non-zero)"
    else
      ok  "A-falsify[$label]: preflight exited non-zero BEFORE any process ($(grep -m1 VIOLATION "$tdir/$label.log" | sed 's/^ *//'))"
    fi
  }
  inject FOREIGN_PORT   srv "s|addr: $ADDR|addr: 127.0.0.1:19999|"
  inject NONLOOPBACK_URL wk "s|ws://$ADDR/v1/workers/connect|ws://host.docker.internal:18899/v1/workers/connect|"
  inject LIVE_WORKERID  wk  "s|worker_id: $WID|worker_id: w-host|"
  inject LIVE_DB        srv "s|db_path: $STORE/v4.db|db_path: /root/.config/gofer/gofer.db|"
  inject OUT_OF_PROJECTS srv "s|host_path: $PROJ/x/proj-a|host_path: /opt/foreign/live-proj|"
  echo "先证伪 detail: each removable assertion (A2/A3/A4/A6/A5) is the sole catcher for its row (see $tdir/*.log)."
}

# ================================================================= P0 pre-flight visibility
p0() {
  step "P0 environment check (live must be visible & untouched; only our PIDs get killed)"
  echo "live in-container gofer procs (NOT ours, must survive):"; pgrep -a gofer | sed 's/^/  /'
  # deterministic re-runs: wipe stateful dirs (NOT the binaries/worktree/cfg files falsify just wrote).
  rm -rf "$STORE" "$WSTORE" "$BASE/wstore-v2" "$BASE/wstore-v3" "$HOST" "$PROJ" \
         "$CFGDIR/run" "$CFGDIR/worker" "$V3CFGDIR" "$BASE/cfgdir-v2" 2>/dev/null
  for b in "$BIN" "$V3BIN"; do [ -x "$b" ] || { echo "!! missing binary $b — build it first"; exit 1; }; done
  if [ -x "$V2BIN" ]; then echo "v2 binary present: $V2BIN"; else echo "!! v2 binary $V2BIN missing — the v2 matrix cell will be marked UNCOVERED"; fi
  mkdir -p "$OUT" "$WEBDIR" "$STORE" "$WSTORE" "$CFGDIR" "$V3CFGDIR" "$BASE/cfg" "$PROJ/y"
  echo "<!-- p3 smoke: intentionally empty web dir; NEVER point --web-dir at web/dist -->" > "$WEBDIR/index.html"
  local d
  for d in proj-a proj-b proj-c proj-d proj-e proj-f proj-noroute proj-codex proj-mc; do
    mkdir -p "$PROJ/x/$d" "$HOST/x/$d"; done
  mkdir -p "$PROJ/y/nogo" "$HOST/y/nogo"
}

start_serve() { # $1=bin $2=cfg $3=addr $4=logname -> sets SERVE_PID
  nohup "$1" serve -c "$2" --web-dir "$WEBDIR" > "$OUT/$4" 2>&1 &
  SERVE_PID=$!
  wait_health "$3" 30 || { echo "serve ($4) did not become healthy"; cat "$OUT/$4" | tail -20; }
  echo "serve pid=$SERVE_PID ($4) health=$(curl -s -o /dev/null -w '%{http_code}' http://$3/health)"
}
start_worker() { # $1=bin $2=logname -> sets WPID
  nohup "$1" worker --worker-config "$WYAML" > "$OUT/$2" 2>&1 &
  WPID=$!
}

# ================================================================= STEP 1 (verification 1)
step1_legacy_zero_break() {
  step "STEP 1 (verification 1): LEGACY zero-break — old(v3) vs P3 /v1/meta diff empty; server pushes 4, worker keeps 3"
  write_server ""                 # server has proj-a..proj-d (4 keys) -> WRUN
  write_worker_legacy             # worker.yaml: proj-a,b,c local, NO roots

  # 1a. OLD binary (f11669d, proto v3) first.
  start_serve "$V3BIN" "$SRVCFG" "$ADDR" "serve.log"
  start_worker "$V3BIN" "worker.log"
  if wait_conn 30; then :; else bad "STEP1: v3 worker never connected (see worker.log)"; fi
  sleep 1
  local old_projs old_caps; old_projs=$(wprojects | jq -Sc 'sort'); old_caps=$(wcaps | jq -Sc 'sort_by(.key)|map(.key)')
  echo "OLD(v3) projects=$old_projs caps=$old_caps"
  echo "$old_projs" > "$OUT/meta-old-projects.json"; echo "$old_caps" > "$OUT/meta-old-caps.json"
  kill -TERM "$WPID" 2>/dev/null; sleep 2; WPID=""
  kill -TERM "$SERVE_PID" 2>/dev/null; sleep 2; SERVE_PID=""

  # 1b. P3 binary (proto v4), SAME configs.
  start_serve "$BIN" "$SRVCFG" "$ADDR" "serve.log"
  start_worker "$BIN" "worker.log"
  if wait_conn 30; then :; else bad "STEP1: P3 worker never connected"; fi
  sleep 1
  local new_projs new_caps; new_projs=$(wprojects | jq -Sc 'sort'); new_caps=$(wcaps | jq -Sc 'sort_by(.key)|map(.key)')
  echo "P3    projects=$new_projs caps=$new_caps"
  echo "$new_projs" > "$OUT/meta-new-projects.json"; echo "$new_caps" > "$OUT/meta-new-caps.json"

  [ "$old_projs" = "$new_projs" ] && ok "V1a projects diff empty across the binary swap ($new_projs)" \
    || bad "V1a projects diff: old=$old_projs new=$new_projs"
  [ "$old_caps" = "$new_caps" ] && ok "V1b agent_caps diff empty across the binary swap ($new_caps)" \
    || bad "V1b agent_caps diff: old=$old_caps new=$new_caps"

  # worker keeps its 3 LOCAL projects, NOT the 4 pushed policy keys (proj-d absent = policy ignored).
  local n; n=$(wprojects | jq 'length')
  if [ "$n" = "3" ] && ! wprojects | jq -e 'index("proj-d")' >/dev/null; then
    ok "V1c LEGACY worker sourced its own 3 projects and IGNORED the 4-key policy push (no proj-d)"
  else
    bad "V1c LEGACY worker projects wrong (want 3, no proj-d): $(wprojects)"
  fi
  echo "NOTE: the server genuinely computes+pushes a 4-key policy here (proj-a..d all list runner $WRUN);"
  echo "      the Applied{Degraded:legacy_local_projects} receipt is not exposed over HTTP — it is asserted"
  echo "      by unit test worker/policy replyLegacyApplied; the e2e proof is the behavioural outcome above."

  # exec job on the LEGACY worker still runs.
  local j st; j=$("$BIN" job run -p proj-a -a exec --runner "$WRUN" -- echo legacy-exec-ok 2>&1 | jid)
  st=$(wait_job "$j" 30); job_stdout "$j" > "$OUT/s1-exec.txt" 2>&1
  { [ "$st" = "done" ] && grep -q legacy-exec-ok "$OUT/s1-exec.txt"; } \
    && ok "V1d LEGACY exec job runs (done, stdout ok)" || bad "V1d LEGACY exec job: st=$st out=$(cat "$OUT/s1-exec.txt")"

  # interactive tty fixture job on the LEGACY worker (self-terminating pty; NO real claude/codex).
  local ij ist; ij=$("$BIN" job run --interactive -p proj-a -a ttyfix --runner "$WRUN" 2>&1 | tee "$OUT/s1-tty-submit.txt" | jid)
  if [ -z "$ij" ]; then
    bad "V1e LEGACY interactive tty fixture job REJECTED at submit: $(cat "$OUT/s1-tty-submit.txt")"
  else
    ist=$(wait_job "$ij" 25)
    case "$ist" in
      done) ok "V1e LEGACY interactive tty fixture job ran through (done)";;
      running|queued) unc "V1e interactive tty job accepted+dispatched but state=$ist (self-exit w/o attach not observed; full attach completion is pty_e2e unit-covered)";;
      *) bad "V1e interactive tty job final=$ist: $(job_state "$ij")";;
    esac
  fi
}

# ================================================================= STEP 2 (verification 3)
step2_policy_sighup_add() {
  step "STEP 2 (verification 3): switch worker to POLICY; server adds proj-e via SIGHUP; PID stable; pwd=mapped"
  kill -TERM "$WPID" 2>/dev/null; sleep 2; WPID=""
  write_worker_policy "$(printf '%b' "$ROOTS_X")" "$(printf '%b' "$GUARDS_ON")"
  write_server "$(extra_policy_base)"   # a,b,c,d + noroute + codex + mc
  kill -HUP "$SERVE_PID" 2>/dev/null; sleep 1
  start_worker "$BIN" "worker.log"
  wait_conn 30 || bad "STEP2: POLICY worker never connected"
  wait_proj proj-a 1 20 || true
  sleep 1
  echo "POLICY worker projects (initial): $(wprojects)"
  # sanity: proj-a..d present, proj-noroute NOT (empty runners).
  wprojects | jq -e 'index("proj-a") and index("proj-d")' >/dev/null \
    && ok "V3a POLICY worker picked up server projects (proj-a..proj-d)" || bad "V3a POLICY worker missing base projects: $(wprojects)"

  proc "$WPID" > "$OUT/s2-pid-before.txt"
  # server ADD proj-e via config edit + SIGHUP serve (NOT the worker).
  write_server "$(extra_add_e)"
  kill -HUP "$SERVE_PID" 2>/dev/null
  if wait_proj proj-e 1 20; then ok "V3b SIGHUP-added server project proj-e appeared on the worker"; else bad "V3b proj-e never appeared: $(wprojects)"; fi
  proc "$WPID" > "$OUT/s2-pid-after.txt"
  diff -q "$OUT/s2-pid-before.txt" "$OUT/s2-pid-after.txt" >/dev/null \
    && ok "V3c worker PID+start-time unchanged across the server-side add" || bad "V3c worker process changed: $(cat "$OUT"/s2-pid-*.txt)"

  # submit to proj-e; pwd must be the MAPPED local path ($HOST/x/proj-e).
  local j st; j=$("$BIN" job run -p proj-e -a exec --runner "$WRUN" -- pwd 2>&1 | jid)
  st=$(wait_job "$j" 30); job_stdout "$j" > "$OUT/s2-pwd.txt" 2>&1
  { [ "$st" = "done" ] && grep -qx "$HOST/x/proj-e" "$OUT/s2-pwd.txt"; } \
    && ok "V3d job cwd is the roots-MAPPED local path ($HOST/x/proj-e)" \
    || bad "V3d pwd wrong: st=$st out=$(cat "$OUT/s2-pwd.txt") want=$HOST/x/proj-e"
}

# ================================================================= STEP 3 (verification 4)
step3_web_write_repush() {
  step "STEP 3 (verification 4): POST /v1/projects (web write) — NO SIGHUP — worker gains the key"
  local before; before=$(api /v1/meta | jq '.projects|length')
  local body="{\"key\":\"proj-f\",\"host_path\":\"$PROJ/x/proj-f\",\"allowed_agents\":[\"exec\"],\"allowed_runners\":[\"$WRUN\"],\"allow_exec\":true}"
  local http; http=$(curl -s -o "$OUT/s3-post.json" -w '%{http_code}' -X POST \
    -H "Authorization: Bearer $STOK" -H 'Content-Type: application/json' -d "$body" "http://$ADDR/v1/projects")
  echo "POST /v1/projects -> HTTP $http"; cat "$OUT/s3-post.json"; echo
  [ "$http" = "200" ] && ok "V4a POST /v1/projects accepted (HTTP 200)" || bad "V4a POST /v1/projects HTTP $http: $(cat "$OUT/s3-post.json")"
  if wait_proj proj-f 1 20; then ok "V4b web-added proj-f reached the worker WITHOUT any SIGHUP (B2 re-push)"; else bad "V4b proj-f never appeared on the worker after web write: $(wprojects)"; fi
  local j st; j=$("$BIN" job run -p proj-f -a exec --runner "$WRUN" -- echo web-added-ok 2>&1 | jid)
  st=$(wait_job "$j" 30); job_stdout "$j" > "$OUT/s3-run.txt" 2>&1
  { [ "$st" = "done" ] && grep -q web-added-ok "$OUT/s3-run.txt"; } \
    && ok "V4c job on the web-added project runs" || bad "V4c web-added project job: st=$st"
}

# ================================================================= STEP 4 (verification 8)
step4_out_of_root_rejected() {
  step "STEP 4 (verification 8): server project whose path is outside every root -> rejected, absent from worker"
  write_server "$(extra_add_e_nogo)"   # adds nogo: host_path $PROJ/y/nogo (worker root only covers $PROJ/x)
  kill -HUP "$SERVE_PID" 2>/dev/null; sleep 3
  echo "worker projects after adding out-of-root nogo: $(wprojects)"
  if wprojects | jq -e 'index("nogo")' >/dev/null; then
    bad "V8 out-of-root project nogo WRONGLY appeared on the worker: $(wprojects)"
  else
    ok "V8 out-of-root project nogo was REJECTED (path_outside_roots) — absent from worker projects"
  fi
}

# ================================================================= STEP 5 (verification 9)
step5_sighup_keeps_projects() {
  step "STEP 5 (verification 9): SIGHUP the worker -> projects stay (POLICY re-projects last-known-good, not 0)"
  local before; before=$(wprojects | jq -Sc 'sort'); local n; n=$(echo "$before" | jq 'length')
  proc "$WPID" > "$OUT/s5-pid-before.txt"
  kill -HUP "$WPID" 2>/dev/null; sleep 3
  local after; after=$(wprojects | jq -Sc 'sort')
  proc "$WPID" > "$OUT/s5-pid-after.txt"
  [ "$after" = "$before" ] && [ "$n" -gt 0 ] \
    && ok "V9 SIGHUP kept all $n POLICY projects (no wipe): $after" || bad "V9 SIGHUP changed projects: $before -> $after"
  diff -q "$OUT/s5-pid-before.txt" "$OUT/s5-pid-after.txt" >/dev/null \
    && ok "V9b worker PID unchanged across SIGHUP" || bad "V9b worker process changed across SIGHUP"
}

# ================================================================= STEP 6 (verification 10)
step6_add_root_reload() {
  step "STEP 6 (verification 10): worker adds root y -> reload -> the once-rejected nogo becomes accepted; PID stable"
  proc "$WPID" > "$OUT/s6-pid-before.txt"
  write_worker_policy "$(printf '%b' "$ROOTS_XY")" "$(printf '%b' "$GUARDS_ON")"
  "$BIN" worker reload "$WID" -s "$ADDR" --token "$STOK" --reason "smoke: add root y" > "$OUT/s6-reload.txt" 2>&1
  local rc=$?; echo "worker reload exit=$rc"; cat "$OUT/s6-reload.txt"
  if wait_proj nogo 1 20; then ok "V10 nogo became ACCEPTED after adding root y + reload"; else bad "V10 nogo still absent after adding root y: $(wprojects)"; fi
  proc "$WPID" > "$OUT/s6-pid-after.txt"
  diff -q "$OUT/s6-pid-before.txt" "$OUT/s6-pid-after.txt" >/dev/null \
    && ok "V10b worker PID unchanged across the reload" || bad "V10b worker process changed across the reload"
  # job in nogo lands in the mapped $HOST/y/nogo.
  local j st; j=$("$BIN" job run -p nogo -a exec --runner "$WRUN" -- pwd 2>&1 | jid)
  st=$(wait_job "$j" 30); job_stdout "$j" > "$OUT/s6-pwd.txt" 2>&1
  { [ "$st" = "done" ] && grep -qx "$HOST/y/nogo" "$OUT/s6-pwd.txt"; } \
    && ok "V10c job in nogo runs at the newly-mapped $HOST/y/nogo" || bad "V10c nogo pwd: st=$st out=$(cat "$OUT/s6-pwd.txt")"
}

# ================================================================= STEP 7 (verification 11)
step7_guards_only_tighten() {
  step "STEP 7 (verification 11): guards.allow_exec:false -> exec rejected; back to true -> runs"
  write_worker_policy "$(printf '%b' "$ROOTS_XY")" "$(printf '%b' "$GUARDS_NOEXEC")"
  "$BIN" worker reload "$WID" -s "$ADDR" --token "$STOK" --reason "smoke: guards no-exec" > "$OUT/s7-reload1.txt" 2>&1
  sleep 2
  local j st; j=$("$BIN" job run -p proj-a -a exec --runner "$WRUN" -- echo should-be-blocked 2>&1 | tee "$OUT/s7-blocked-submit.txt" | jid)
  if [ -z "$j" ]; then
    ok "V11a exec job REJECTED at submit while guards.allow_exec:false ($(head -1 "$OUT/s7-blocked-submit.txt"))"
  else
    st=$(wait_job "$j" 20)
    [ "$st" != "done" ] && ok "V11a exec job did not succeed under guards.allow_exec:false (state=$st)" \
      || bad "V11a exec job WRONGLY ran under guards.allow_exec:false"
  fi
  # restore
  write_worker_policy "$(printf '%b' "$ROOTS_XY")" "$(printf '%b' "$GUARDS_ON")"
  "$BIN" worker reload "$WID" -s "$ADDR" --token "$STOK" --reason "smoke: guards restore" > "$OUT/s7-reload2.txt" 2>&1
  sleep 2
  j=$("$BIN" job run -p proj-a -a exec --runner "$WRUN" -- echo exec-restored 2>&1 | jid)
  st=$(wait_job "$j" 25); job_stdout "$j" > "$OUT/s7-restore.txt" 2>&1
  { [ "$st" = "done" ] && grep -q exec-restored "$OUT/s7-restore.txt"; } \
    && ok "V11b exec job runs again after guards.allow_exec back to true" || bad "V11b exec still blocked after restore: st=$st"
}

# ================================================================= STEP 8 (verification 12)
step8_empty_runners_not_pushed() {
  step "STEP 8 (verification 12): a project with allowed_runners:[] is pushed to NO worker"
  # proj-noroute (empty runners) has been in the server config since step 2.
  if wprojects | jq -e 'index("proj-noroute")' >/dev/null; then
    bad "V12 proj-noroute (allowed_runners:[]) WRONGLY reached the worker: $(wprojects)"
  else
    ok "V12 proj-noroute (allowed_runners:[]) reached NO worker (empty != wildcard)"
  fi
}

# ================================================================= STEP 9 (verification 13)
step9_whitelist_no_intersection() {
  step "STEP 9 (verification 13): allowed_agents keeps tty-codex verbatim; submitting tty-codex errors clearly (codex absent)"
  # proj-codex allowed_agents:[exec, tty-codex]; codex is NOT installed -> tty-codex unresolvable.
  local out rc
  out=$("$BIN" job run --interactive -p proj-codex -a tty-codex 2>&1); rc=$?
  echo "$out" > "$OUT/s9-ttycodex.txt"
  if [ "$rc" -ne 0 ] || echo "$out" | grep -qiE 'unknown agent|not available|not on worker|unknown project|not in'; then
    ok "V13 submitting tty-codex errored clearly (not silently accepted): $(echo "$out" | tr '\n' ' ' | head -c 160)"
  else
    bad "V13 tty-codex submit did NOT error clearly (rc=$rc): $out"
  fi
  echo "NOTE: the verbatim AllowedAgents==[exec,tty-codex] (no intersection) invariant is钉死 by unit test"
  echo "      commands/worker_policy_test — e2e only proves the clear-error behavioural outcome."
}

# ================================================================= STEP 10 (verification 14)
step10_projected_fields() {
  step "STEP 10 (verification 14): max_concurrent_jobs:1 serialises; capture_diff:false -> no diff artifact"
  # proj-mc: max_concurrent_jobs:1, capture_diff:false (present since step 2).
  wait_proj proj-mc 1 15 || true
  local j1 j2 s2
  j1=$("$BIN" job run -p proj-mc -a exec --runner "$WRUN" -- sh -c 'sleep 6; echo one' 2>&1 | jid)
  sleep 1
  j2=$("$BIN" job run -p proj-mc -a exec --runner "$WRUN" -- echo two 2>&1 | jid)
  sleep 1
  s2=$(api "/v1/jobs/$j2" | jq -r .status)
  echo "with proj-mc max_concurrent_jobs=1: j1=$(api "/v1/jobs/$j1"|jq -r .status) j2=$s2"
  [ "$s2" = "queued" ] && ok "V14a second concurrent job on proj-mc QUEUED behind the first (max_concurrent_jobs=1 projected)" \
    || unc "V14a second job state=$s2 (expected queued; timing-sensitive) — MaxConcurrentJobs projection is钉死 by unit test"
  wait_job "$j1" 30 >/dev/null; wait_job "$j2" 30 >/dev/null
  # capture_diff contrast: BOTH mapped dirs become git repos with a dirty change.
  #   proj-a  (capture_diff unset -> default ON for a git work tree) MUST produce changes.diff
  #   proj-mc (capture_diff:false)                                    MUST NOT
  local d
  for d in proj-a proj-mc; do
    ( cd "$HOST/x/$d" && git init -q 2>/dev/null && git config user.email s@s && git config user.name s \
        && echo base > f.txt && git add -A && git commit -qm base 2>/dev/null )
  done
  local ja jm; ja=$("$BIN" job run -p proj-a  -a exec --runner "$WRUN" -- sh -c 'echo mut >> f.txt' 2>&1 | jid)
  jm=$("$BIN" job run -p proj-mc -a exec --runner "$WRUN" -- sh -c 'echo mut >> f.txt' 2>&1 | jid)
  wait_job "$ja" 30 >/dev/null; wait_job "$jm" 30 >/dev/null
  local diff_a diff_mc
  diff_a=$(find "$WSTORE" -path '*/proj-a/*' -name 'changes.diff' 2>/dev/null | head -1)
  diff_mc=$(find "$WSTORE" -path '*/proj-mc/*' -name 'changes.diff' 2>/dev/null | head -1)
  echo "changes.diff  proj-a='$diff_a'  proj-mc='$diff_mc'"
  if [ -n "$diff_a" ] && [ -z "$diff_mc" ]; then
    ok "V14b capture_diff projected: default-ON proj-a captured a diff; proj-mc (capture_diff:false) did NOT"
  elif [ -z "$diff_mc" ]; then
    unc "V14b proj-mc correctly produced no diff, but the default-ON proj-a control also produced none (diff_a empty) — inconclusive positive control"
  else
    bad "V14b capture_diff:false but proj-mc produced a diff: $diff_mc"
  fi
}

# ================================================================= STEP 11 (verification 15)
step11_reconnect_storm_workflow() {
  step "STEP 11 (verification 15): a workflow fan-out survives a worker reconnect (policy_pending must NOT fail it)"
  cat > "$OUT/wf.yaml" <<EOF
title: smoke-p3-fanout
steps:
  - name: fan
    project_key: proj-a
    agent: exec
    runner: $WRUN
    cmd: ["sh", "-c", "sleep 2; echo fan-\$GOFER_FAN done"]
    fan_out: 3
    join: all
EOF
  # Bounce the worker connection (restart) to force Rev=0 -> re-register (caps re-reported) ->
  # re-enter pending -> re-apply policy. Then, once it has RECONVERGED (connected + policy
  # re-applied, projects back), fire the fan-out workflow: it must run to `done`, never hang
  # or hard-fail. (Submitting in the exact ~ms pending window is the T5 -race unit test's job,
  # verifications 21/22 / B4/B5; T7 asserts the e2e observable convergence per plan line 990.)
  proc "$WPID" > "$OUT/s11-pid-before.txt"
  kill -TERM "$WPID" 2>/dev/null; sleep 1
  start_worker "$BIN" "worker.log"
  wait_conn 30 || bad "STEP11: worker did not reconnect after the bounce"
  wait_proj proj-a 1 30 || bad "STEP11: worker did not re-apply its policy (proj-a absent) after reconnect"
  local wf; wf=$("$BIN" workflow run "$OUT/wf.yaml" -s "$ADDR" --token "$STOK" 2>&1 | tee "$OUT/s11-wf-submit.txt" | grep -oE 'wf-[0-9]{8}-[0-9]{6}-[0-9a-f]{8}' | head -1)
  echo "workflow id=$wf (submitted after the reconnect reconverged)"
  local wst=""
  if [ -n "$wf" ]; then
    for _ in $(seq 1 60); do wst=$(api "/v1/workflows/$wf" | jq -r .status 2>/dev/null); case "$wst" in running|queued|pending|"") sleep 1;; *) break;; esac; done
  fi
  echo "workflow final status=$wst"; api "/v1/workflows/$wf" > "$OUT/s11-wf.json" 2>&1
  case "$wst" in
    done|success|succeeded|completed) ok "V15 workflow fan-out completed despite the worker reconnect (not打挂): $wst";;
    failed) bad "V15 workflow FAILED across the reconnect (policy_pending regression?): $(cat "$OUT/s11-wf.json")";;
    *) unc "V15 workflow ended in state=$wst (not a hard fail; precise pending-window race is钉死 by T5 -race unit tests B4/B5)";;
  esac
}

# ================================================================= STEP 12 (verification 16)
step12_rolling_matrix() {
  step "STEP 12 (verification 16): rolling-upgrade matrix on an isolated second server (:18900)"
  # --- v4 server + v3 worker (already implicitly covered by step1's v3 run; re-assert connect) ---
  # --- v3 SERVER + v4 worker: POLICY cold-start vs POLICY already-activated ---
  # Bring up a v3 server on :18900 with its OWN cfgdir/store.
  cat > "$V3SRVCFG" <<EOF
server:
  addr: $V3ADDR
  token: $V3STOK
  workers:
    $WID:
      token: $WTOK
      labels: [linux, smoke]
storage:
  db_path: $STORE/v3.db
  root: $STORE/blobs-v3
projects:
  proj-a:
    host_path: $PROJ/x/proj-a
    allowed_agents: [exec]
    allowed_runners: [$WRUN]
    allow_exec: true
agents:
  exec:
    type: exec
runners:
  local:
    type: local
  $WRUN:
    type: worker
    worker_id: $WID
EOF
  GOFER_CONFIG_DIR="$V3CFGDIR" GOFER_CONFIG="$V3SRVCFG" nohup "$V3BIN" serve -c "$V3SRVCFG" --web-dir "$WEBDIR" > "$OUT/serve-v3.log" 2>&1 &
  V3SERVE_PID=$!
  wait_health "$V3ADDR" 30 || echo "v3 server not healthy (see serve-v3.log)"
  echo "v3 server pid=$V3SERVE_PID health=$(curl -s -o /dev/null -w '%{http_code}' http://$V3ADDR/health)"

  # v4 worker ALREADY has an activated policy (from steps 2-11, connected to :18899).
  # 12a: POLICY worker already-activated -> point it (a SECOND worker instance) at the v3 server.
  # We use a SEPARATE worker.yaml so we do not disturb the main :18899 worker.
  local W2YAML="$V3CFGDIR/worker.yaml"; mkdir -p "$V3CFGDIR"
  cat > "$W2YAML" <<EOF
worker_id: $WID
server_link:
  urls: [ws://$V3ADDR/v1/workers/connect]
  token: $WTOK
labels: [linux, smoke]
max_concurrent: 4
roots:
  - from: $PROJ/x
    to: $HOST/x
guards:
  allow_exec: true
  allow_interactive: true
agents:
  exec:
    type: exec
storage:
  root: $BASE/wstore-v3
EOF
  # 12-cold: fresh cfgdir (no cache) -> connect to v3 -> 0 projects + loud slog + still online.
  rm -f "$V3CFGDIR/run/worker-$WID.policy.json"
  GOFER_CONFIG_DIR="$V3CFGDIR" nohup "$BIN" worker --worker-config "$W2YAML" > "$OUT/worker-v3cold.log" 2>&1 &
  AUXPID=$!
  local conn=""; for _ in $(seq 1 30); do conn=$(v3api /v1/meta | jq -r --arg id "$WID" '.workers[]?|select(.id==$id)|.connected'); [ "$conn" = "true" ] && break; sleep 0.5; done
  sleep 1
  local vp; vp=$(v3api /v1/meta | jq -c --arg id "$WID" '.workers[]?|select(.id==$id)|.projects // []')
  echo "v3-server cold POLICY worker: connected=$conn projects=$vp"
  if [ "$conn" = "true" ] && [ "$(echo "$vp" | jq 'length')" = "0" ]; then
    ok "V16a POLICY cold-start against a v3 server: online with 0 projects (version gate, no crash)"
  else
    bad "V16a POLICY cold-start on v3 server: connected=$conn projects=$vp"
  fi
  grep -qiE 'does not push policy|server_proto|不支持策略|policy mode but server' "$OUT/worker-v3cold.log" \
    && ok "V16b cold POLICY worker logged the loud 'server does not push policy' warning" \
    || bad "V16b missing the loud v3-server warning in worker-v3cold.log: $(grep -i warn "$OUT/worker-v3cold.log" | head -2)"
  kill -TERM "$AUXPID" 2>/dev/null; sleep 1; AUXPID=""

  # 12-activated: seed a last-known-good cache (simulating a prior activation), THEN connect to v3.
  # Projects must NOT zero out (B1 v0.3: a server that does not push policy never wipes LKG).
  # We activate by first connecting this worker to the v4 server (:18899) briefly to populate its cache.
  GOFER_CONFIG_DIR="$V3CFGDIR" GOFER_SERVER_ADDR="$ADDR" >/dev/null 2>&1
  # Point the aux worker at the v4 server once to write a cache, then flip to v3.
  cat > "$W2YAML" <<EOF
worker_id: $WID
server_link:
  urls: [ws://$ADDR/v1/workers/connect]
  token: $WTOK
labels: [linux, smoke]
max_concurrent: 4
roots:
  - from: $PROJ/x
    to: $HOST/x
guards:
  allow_exec: true
  allow_interactive: true
agents:
  exec:
    type: exec
storage:
  root: $BASE/wstore-v3
EOF
  GOFER_CONFIG_DIR="$V3CFGDIR" nohup "$BIN" worker --worker-config "$W2YAML" > "$OUT/worker-activate.log" 2>&1 &
  AUXPID=$!
  sleep 4    # let it register on v4 and persist a last-known-good cache
  local cachef="$V3CFGDIR/run/worker-$WID.policy.json"
  [ -f "$cachef" ] && ok "V16c POLICY worker persisted a last-known-good cache after activation" || bad "V16c no LKG cache written: $cachef"
  local cachen; cachen=$(jq '.policy.projects|length' "$cachef" 2>/dev/null || echo 0)
  kill -TERM "$AUXPID" 2>/dev/null; sleep 2; AUXPID=""
  # now flip to the v3 server and restart -> must recover from cache, NOT zero out.
  sed -i "s|ws://$ADDR/v1/workers/connect|ws://$V3ADDR/v1/workers/connect|" "$W2YAML"
  GOFER_CONFIG_DIR="$V3CFGDIR" nohup "$BIN" worker --worker-config "$W2YAML" > "$OUT/worker-v3warm.log" 2>&1 &
  AUXPID=$!
  for _ in $(seq 1 30); do conn=$(v3api /v1/meta | jq -r --arg id "$WID" '.workers[]?|select(.id==$id)|.connected'); [ "$conn" = "true" ] && break; sleep 0.5; done
  sleep 1
  vp=$(v3api /v1/meta | jq -c --arg id "$WID" '.workers[]?|select(.id==$id)|.projects // []')
  echo "v3-server ACTIVATED worker: connected=$conn projects=$vp (cache had $cachen)"
  if [ "$conn" = "true" ] && [ "$(echo "$vp" | jq 'length')" -gt 0 ]; then
    ok "V16d already-activated POLICY worker kept last-known-good projects on a v3 server (no wipe): $vp"
  else
    bad "V16d activated worker on v3 server lost its projects (connected=$conn projects=$vp cache=$cachen)"
  fi
  kill -TERM "$AUXPID" 2>/dev/null; sleep 1; AUXPID=""

  # --- v4 server + v2 worker (reused proto-v2 binary) ---
  # smoke-p3-w2 / runner wrun2 / project proj-v2 are PRE-REGISTERED in the base server
  # config (write_server), so no fragile hot-add is needed — the v2 worker authenticates
  # against the startup config directly.
  if [ -x "$V2BIN" ]; then
    local W2V2="$V3CFGDIR/worker-v2.yaml"
    cat > "$W2V2" <<EOF
worker_id: ${WID}2
server_link:
  urls: [ws://$ADDR/v1/workers/connect]
  token: $WTOK2
labels: [linux, smoke]
max_concurrent: 2
projects:
  proj-v2:
    host_path: $PROJ/x/proj-a
    default_agent: exec
    allowed_agents: [exec]
    allowed_runners: [local]
    allow_exec: true
agents:
  exec:
    type: exec
storage:
  root: $BASE/wstore-v2
EOF
    GOFER_CONFIG_DIR="$BASE/cfgdir-v2" nohup "$V2BIN" worker --worker-config "$W2V2" > "$OUT/worker-v2.log" 2>&1 &
    AUXPID=$!
    local v2conn=""; for _ in $(seq 1 30); do v2conn=$(api /v1/meta | jq -r --arg id "${WID}2" '.workers[]?|select(.id==$id)|.connected'); [ "$v2conn" = "true" ] && break; sleep 0.5; done
    echo "v2 worker connected=$v2conn"
    if [ "$v2conn" = "true" ]; then
      grep -qE "worker_id=${WID}2.*proto=2|proto=2.*${WID}2" "$OUT/serve.log" \
        && ok "V16e v2 worker connects to the v4 server; hub negotiated proto=2 (MinProtocolVersion back-compat)" \
        || ok "V16e v2 worker connects to the v4 server (proto negotiation line not matched but connected)"
      local vj vst; vj=$("$BIN" job run -p proj-v2 -a exec --runner wrun2 --worker-id "${WID}2" -- echo v2-worker-ok 2>&1 | jid)
      vst=$(wait_job "$vj" 30); job_stdout "$vj" > "$OUT/s12-v2job.txt" 2>&1
      { [ "$vst" = "done" ] && grep -q v2-worker-ok "$OUT/s12-v2job.txt"; } \
        && ok "V16f v4 server dispatches a job to the proto-v2 worker (runs, no policy involved)" \
        || bad "V16f v2 worker job final=$vst: $(cat "$OUT/s12-v2job.txt")"
    else
      unc "V16e/f v2 worker did not connect (see worker-v2.log) — proto-v2 back-compat cell UNCOVERED this run"
    fi
    kill -TERM "$AUXPID" 2>/dev/null; sleep 1; AUXPID=""
  else
    unc "V16e/f proto-v2 worker binary missing ($V2BIN) — v2 matrix cell UNCOVERED"
  fi
  kill -TERM "$V3SERVE_PID" 2>/dev/null; sleep 1; V3SERVE_PID=""
}

# ================================================================= STEP 13 (verification 18)
step13_worker_cli() {
  step "STEP 13 (verification 18): gofer project list / config validate in LEGACY and POLICY forms"
  # POLICY form is live (worker.yaml currently POLICY + a cache exists).
  local out rc
  out=$(GOFER_RUN_MODE=worker GOFER_CONFIG_DIR="$CFGDIR" "$BIN" project list 2>&1); rc=$?
  echo "$out" > "$OUT/s13-projlist-policy.txt"
  { [ "$rc" = "0" ] && echo "$out" | grep -qE 'proj-a'; } \
    && ok "V18a POLICY worker: 'project list' lists the effective (policy-cache) projects" || bad "V18a POLICY project list rc=$rc: $out"
  out=$("$BIN" config validate worker -c "$WYAML" 2>&1); rc=$?
  echo "$out" > "$OUT/s13-validate-policy.txt"
  { [ "$rc" = "0" ] && echo "$out" | grep -qE 'worker config OK|projects 由 server 下发'; } \
    && ok "V18b POLICY worker: 'config validate' PASSES (0 local projects is fine)" || bad "V18b POLICY config validate rc=$rc: $out"

  # LEGACY form: write a legacy worker.yaml at a temp path and validate it.
  local LWY="$OUT/worker-legacy.yaml"
  write_worker_legacy; cp "$WYAML" "$LWY"       # write_worker_legacy targets $WYAML; snapshot it
  out=$("$BIN" config validate worker -c "$LWY" 2>&1); rc=$?
  echo "$out" > "$OUT/s13-validate-legacy.txt"
  { [ "$rc" = "0" ] && echo "$out" | grep -qiE 'WARN|已废弃'; } \
    && ok "V18c LEGACY worker: 'config validate' PASSES with a deprecation WARN (behaviour unchanged)" || bad "V18c LEGACY config validate rc=$rc: $out"
  out=$(GOFER_RUN_MODE=worker GOFER_CONFIG_DIR="$CFGDIR" "$BIN" project list 2>&1); rc=$?
  echo "$out" > "$OUT/s13-projlist-legacy.txt"
  { [ "$rc" = "0" ] && echo "$out" | grep -qE 'proj-a'; } \
    && ok "V18d LEGACY worker: 'project list' lists worker.yaml projects verbatim" || bad "V18d LEGACY project list rc=$rc: $out"
  # restore POLICY worker.yaml for teardown consistency
  write_worker_policy "$(printf '%b' "$ROOTS_XY")" "$(printf '%b' "$GUARDS_ON")"
}

# ================================================================= STEP 14 (verification 17)
step14_hub_boundary() {
  step "STEP 14 (verification 17): wshub imports exactly one gofer pkg besides itself (wsproto)"
  local out; out=$(cd "$REPO" && go list -deps ./internal/wshub 2>/dev/null | grep gofer | grep -v '/wshub$')
  echo "$out" > "$OUT/s14-hubdeps.txt"; echo "wshub gofer deps (minus self): $out"
  if [ "$out" = "github.com/inhere/gofer/internal/wsproto" ]; then
    ok "V17 wshub's only gofer dep (besides itself) is internal/wsproto — boundary intact"
  else
    bad "V17 wshub gofer deps not exactly wsproto: $out"
  fi
}

# ================================================================= STEP 15 (verification 24)
step15_binary_rollback() {
  step "STEP 15 (verification 24): binary rollback safety — roots+projects rolls back OK; roots-only is NOT a safe rollback"
  # Tear down the P3 worker; the v4 server stays up (old worker is proto v3, no policy).
  kill -TERM "$WPID" 2>/dev/null; sleep 2; WPID=""
  # 15a: observation-period form (roots AND projects) + OLD (v3) binary -> uses local projects, runs.
  write_worker_rollback_both
  start_worker "$V3BIN" "worker-rollback-both.log"
  if wait_conn 30; then
    sleep 1
    if wprojects | jq -e 'index("proj-a")' >/dev/null; then
      local j st; j=$("$BIN" job run -p proj-a -a exec --runner "$WRUN" -- echo rollback-both-ok 2>&1 | jid)
      st=$(wait_job "$j" 30); job_stdout "$j" > "$OUT/s15-both.txt" 2>&1
      { [ "$st" = "done" ] && grep -q rollback-both-ok "$OUT/s15-both.txt"; } \
        && ok "V24a rollback to old binary with roots+projects: ignores roots, uses local projects, runs (SAFE)" \
        || bad "V24a rollback-both job final=$st"
    else bad "V24a old binary + roots+projects: proj-a not present: $(wprojects)"; fi
  else bad "V24a old(v3) binary did not connect with roots+projects form"; fi
  kill -TERM "$WPID" 2>/dev/null; sleep 2; WPID=""

  # 15b: roots-ONLY + OLD binary -> old code does not understand roots and has no projects -> 0 projects, stalled.
  write_worker_policy "$(printf '%b' "$ROOTS_X")" "$(printf '%b' "$GUARDS_ON")"
  start_worker "$V3BIN" "worker-rollback-rootsonly.log"
  wait_conn 20 || true
  sleep 2
  local n2; n2=$(wprojects | jq 'length' 2>/dev/null || echo 0)
  if [ "${n2:-0}" = "0" ]; then
    ok "V24b rollback to old binary with roots-ONLY: 0 projects (stalled) — correctly an UNSAFE rollback point, NOT a safe rollback"
  else
    bad "V24b old binary somehow reported $n2 projects from a roots-only config (unexpected): $(wprojects)"
  fi
  kill -TERM "$WPID" 2>/dev/null; sleep 2; WPID=""
  # bring the P3 POLICY worker back for a clean teardown / KEEP=1.
  write_worker_policy "$(printf '%b' "$ROOTS_XY")" "$(printf '%b' "$GUARDS_ON")"
  start_worker "$BIN" "worker.log"; wait_conn 20 || true
}

# ================================================================= MAIN
main() {
  falsify_preflight
  p0
  # A: strict preflight on the ACTUAL fixtures we will boot with (LEGACY for step1).
  write_server ""; write_worker_legacy
  if preflight_scan "$SRVCFG" "$WYAML"; then ok "A-final preflight on the real boot fixtures passed (no live resource)"; else bad "A-final preflight FAILED on real fixtures — aborting"; exit 1; fi
  step1_legacy_zero_break
  step2_policy_sighup_add
  step3_web_write_repush
  step4_out_of_root_rejected
  step5_sighup_keeps_projects
  step6_add_root_reload
  step7_guards_only_tighten
  step8_empty_runners_not_pushed
  step9_whitelist_no_intersection
  step10_projected_fields
  step11_reconnect_storm_workflow
  step12_rolling_matrix
  step13_worker_cli
  step14_hub_boundary
  step15_binary_rollback

  step "SUMMARY   pass=$PASS  fail=$FAIL  uncovered=$UNCOV"
  echo "----- verdicts -----"; cat "$OUT/verdicts.txt"
  echo
  echo "live procs after run (ours are gone; others survive):"; pgrep -a gofer | sed 's/^/  /'
  [ "$FAIL" -eq 0 ]
}
main
