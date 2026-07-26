#!/usr/bin/env bash
# Decision-channel (gofer_ask_human) e2e smoke on a FULLY ISOLATED local stack
# (plan T5). Windows host / Git Bash; re-runnable.
#
# Everything runs against 127.0.0.1:19091 with data under tmp/decision-channel/.
# It NEVER touches the live gofer server, its ports, or its data, and it kills
# only the PIDs it captured (trap).
#
# Usage:
#   bash scripts/smoke/decision-channel/build-binaries.sh   # first (or after code changes)
#   bash scripts/smoke/decision-channel/run-smoke.sh
#
# Flow:
#   round 1: MCP gofer_ask_human blocks -> human answers via HTTP API ->
#            tool returns {state:"answered", answer}
#   round 2: nobody answers -> tool returns {state:"expired"} at timeout and
#            CLI `plan decisions` shows the row EXPIRED

set -uo pipefail

# ----------------------------------------------------------------- layout / isolation
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
BASE="$REPO/tmp/decision-channel"
OUT="$BASE/out"
GOFER="$BASE/bin/gofer.exe"
MCPCALL="$BASE/bin/mcp-call.exe"

ADDR=127.0.0.1:19091            # isolated serve port (live uses 8765 etc. — never touched)
STOK=smoke-dc-tok
CFGDIR="$BASE/cfgdir"
SRVCFG="$BASE/cfg/server.yaml"
STORE="$BASE/store"
PROJ="$BASE/projects"

export GOFER_CONFIG_DIR="$CFGDIR"
export GOFER_CONFIG="$SRVCFG"
export GOFER_SERVER_ADDR="$ADDR"
export GOFER_SERVER_TOKEN="$STOK"

PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); echo "PASS: $*" | tee -a "$OUT/verdicts.txt"; }
bad()  { FAIL=$((FAIL+1)); echo "FAIL: $*" | tee -a "$OUT/verdicts.txt"; }
step() { echo; echo "================= $* =================" | tee -a "$OUT/verdicts.txt"; }

api() { curl -s -H "Authorization: Bearer $STOK" "http://$ADDR$1"; }
# first_decision_id <state> — extract the first decision id of a state via python (no jq on this host).
first_decision_id() {
  api "/v1/decisions?state=$1" | python -c 'import sys,json; d=json.load(sys.stdin)["decisions"]; print(d[0]["id"] if d else "")'
}
wait_health() { for _ in $(seq 1 40); do [ "$(curl -s -o /dev/null -w '%{http_code}' "http://$ADDR/health")" = "200" ] && return 0; sleep 0.5; done; return 1; }
wait_open_decision() { for _ in $(seq 1 40); do id="$(first_decision_id OPEN)"; [ -n "$id" ] && { echo "$id"; return 0; }; sleep 0.5; done; return 1; }

# ----------------------------------------------------------------- PIDs we own (trap kills ONLY these)
SERVE_PID=""
cleanup() {
  [ "${KEEP:-0}" = "1" ] && { echo "KEEP=1: leaving stack up ($SERVE_PID)"; return; }
  [ -n "$SERVE_PID" ] && kill -TERM "$SERVE_PID" 2>/dev/null && echo "SIGTERM $SERVE_PID"
  sleep 1
}
trap cleanup EXIT INT TERM

# ----------------------------------------------------------------- preflight: isolation hard-assertions
mkdir -p "$OUT" "$CFGDIR" "$STORE" "$PROJ/self" "$BASE/cfg"
: > "$OUT/verdicts.txt"
step "A. preflight (isolation)"

case "$ADDR" in 127.0.0.1:19091) ok "isolated loopback port $ADDR";; *) bad "addr must be 127.0.0.1:19091, got $ADDR"; exit 1;; esac
case "$STORE" in "$BASE"/*) ok "storage under $BASE";; *) bad "storage escapes $BASE: $STORE"; exit 1;; esac
for b in "$GOFER" "$MCPCALL"; do
  [ -x "$b" ] && ok "binary present: $b" || { bad "missing binary $b — run build-binaries.sh first"; exit 1; }
done
# refuse to boot over something already listening on the smoke port (never kill foreign PIDs)
if [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "http://$ADDR/health")" = "200" ]; then
  bad "$ADDR already serves /health — a stack is already up; refusing to touch it"; exit 1
fi
ok "smoke port is free"

cat > "$SRVCFG" <<EOF
server:
  addr: $ADDR
  token: $STOK
storage:
  db_path: $STORE/gofer.db
  root: $STORE/blobs
projects:
  self:
    host_path: $PROJ/self
    allowed_agents: [exec]
    allowed_runners: [local]
    allow_exec: true
EOF

step "B. boot isolated serve"
"$GOFER" serve --no-web >"$OUT/serve.log" 2>&1 &
SERVE_PID=$!
wait_health && ok "serve up ($ADDR, pid $SERVE_PID)" || { bad "serve did not come up; see $OUT/serve.log"; exit 1; }

# plan to attach decisions to (CLI path also exercises T3 read side later)
"$GOFER" plan create --plan-id plan-smoke --title "decision-channel smoke" >/dev/null \
  && ok "plan-smoke created via CLI" || bad "plan create failed"

# ----------------------------------------------------------------- round 1: answered
step "round 1: ask_human blocked -> answered via HTTP API"
"$MCPCALL" -gofer "$GOFER" -server "$ADDR" -token "$STOK" \
  -tool gofer_ask_human \
  -args '{"plan_id":"plan-smoke","title":"deploy window","question":"which window?","options":["tonight","tomorrow"],"timeout_sec":30}' \
  -deadline 60s >"$OUT/call1.json" 2>"$OUT/call1.err" &
CALL1_PID=$!

DEC_ID="$(wait_open_decision)" && ok "OPEN decision visible via HTTP: $DEC_ID" || { bad "no OPEN decision appeared"; kill -TERM "$CALL1_PID" 2>/dev/null; exit 1; }
code=$(curl -s -o "$OUT/answer1.json" -w '%{http_code}' -X POST -H "Authorization: Bearer $STOK" \
  -H "Content-Type: application/json" -d '{"answer":"tonight"}' \
  "http://$ADDR/v1/decisions/$DEC_ID/answer")
[ "$code" = "200" ] && ok "answer via HTTP API accepted (200)" || bad "answer HTTP status=$code"

wait "$CALL1_PID"; rc=$?
[ $rc -eq 0 ] && ok "mcp-call exited 0 (tool result, not error)" || { bad "mcp-call exit=$rc"; cat "$OUT/call1.err"; }
grep -q '"state":"answered"' "$OUT/call1.json" && ok 'tool returned {state:"answered"}' || { bad "call1 result:"; cat "$OUT/call1.json"; }
grep -q '"answer":"tonight"' "$OUT/call1.json" && ok 'tool returned the human answer verbatim ("tonight")' || bad "answer missing from call1.json"

# ----------------------------------------------------------------- round 2: expired
step "round 2: ask_human unanswered -> expired at timeout"
start=$(date +%s)
"$MCPCALL" -gofer "$GOFER" -server "$ADDR" -token "$STOK" \
  -tool gofer_ask_human \
  -args '{"plan_id":"plan-smoke","title":"unattended","question":"nobody answers this","timeout_sec":5}' \
  -deadline 60s >"$OUT/call2.json" 2>"$OUT/call2.err"
rc=$?
elapsed=$(( $(date +%s) - start ))
[ $rc -eq 0 ] && ok "mcp-call exited 0" || { bad "mcp-call exit=$rc"; cat "$OUT/call2.err"; }
grep -q '"state":"expired"' "$OUT/call2.json" && ok 'tool returned {state:"expired"}' || { bad "call2 result:"; cat "$OUT/call2.json"; }
[ "$elapsed" -ge 4 ] && ok "tool actually blocked ~timeout (${elapsed}s >= 4s)" || bad "returned too fast (${elapsed}s)"

# CLI read path: the expired row shows as EXPIRED (T3 三面一致)
"$GOFER" plan decisions --state EXPIRED >"$OUT/decisions-expired.txt" 2>&1
grep -q "EXPIRED" "$OUT/decisions-expired.txt" && grep -q "unattended" "$OUT/decisions-expired.txt" \
  && ok "CLI 'plan decisions --state EXPIRED' shows the expired decision" \
  || { bad "CLI decisions output:"; cat "$OUT/decisions-expired.txt"; }
"$GOFER" plan decisions --state OPEN >"$OUT/decisions-open.txt" 2>&1
grep -q "no decisions matched" "$OUT/decisions-open.txt" \
  && ok "no ghost OPEN decisions remain" \
  || bad "unexpected OPEN decisions: $(cat "$OUT/decisions-open.txt")"

step "verdict"
echo "pass=$PASS fail=$FAIL" | tee -a "$OUT/verdicts.txt"
[ "$FAIL" -eq 0 ]
