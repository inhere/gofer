#!/usr/bin/env bash
# TUN-01 TCP tunnel end-to-end smoke on a FULLY ISOLATED local stack.
# Starts its own `gofer serve` (default 127.0.0.1:18911) + one worker + local TCP echo
# servers, all with a private GOFER_CONFIG_DIR under tmp/tunnel-smoke. It never touches
# a running server or any process it did not start: the trap SIGTERMs only the PIDs it
# captured. Requires: bash, go, curl, jq (Linux/macOS).
# Usage:
#   bash scripts/smoke/tunnel/run-smoke.sh            # full run, exit code = number of failures
#   KEEP=1 bash scripts/smoke/tunnel/run-smoke.sh     # leave the stack up afterwards
#   SMOKE_ADDR=127.0.0.1:19911 bash scripts/smoke/tunnel/run-smoke.sh
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
BASE="$REPO/tmp/tunnel-smoke"
OUT="$BASE/out"; BINDIR="$BASE/bin"; BIN="$BINDIR/gofer"; HELP="$BINDIR/tunsmoke"
ADDR="${SMOKE_ADDR:-127.0.0.1:18911}"
STOK=smoke-tun-server-tok; WTOK=smoke-tun-worker-tok; WID=smoke-tun-w
CFGDIR="$BASE/cfgdir"; SRVCFG="$BASE/server.yaml"; WYAML="$BASE/worker.yaml"
WEBDIR="$BASE/webdir"; PROJ="$BASE/proj"

for bin in go curl jq; do command -v "$bin" >/dev/null || { echo "missing required tool: $bin"; exit 2; }; done
export GOFER_CONFIG_DIR="$CFGDIR" GOFER_CONFIG="$SRVCFG" GOFER_SERVER_ADDR="$ADDR" GOFER_SERVER_TOKEN="$STOK"

rm -rf "$BASE"; mkdir -p "$OUT" "$BINDIR" "$CFGDIR" "$WEBDIR" "$PROJ/p1"
echo "<!-- tunnel smoke: intentionally empty web dir -->" > "$WEBDIR/index.html"

PASS=0; FAIL=0
ok()  { PASS=$((PASS+1)); echo "PASS: $*" | tee -a "$OUT/verdicts.txt"; }
bad() { FAIL=$((FAIL+1)); echo "FAIL: $*" | tee -a "$OUT/verdicts.txt"; }
step(){ echo; echo "================= $* ================="; }
g()   { "$BIN" "$@"; }

PIDS=""
cleanup() {
  [ "${KEEP:-0}" = "1" ] && { echo "KEEP=1: stack left up: $PIDS"; return; }
  for p in $PIDS; do kill -TERM "$p" 2>/dev/null; done
  sleep 1
}
trap cleanup EXIT INT TERM
bg() { local log="$1"; shift; nohup "$@" > "$OUT/$log" 2>&1 & PIDS="$! $PIDS"; }
wait_http() { for _ in $(seq 1 60); do [ "$(curl -s -o /dev/null -w '%{http_code}' "http://$ADDR/health")" = 200 ] && return 0; sleep 0.5; done; return 1; }
wconnected() { curl -s -H "Authorization: Bearer $STOK" "http://$ADDR/v1/meta" | jq -r --arg id "$WID" '.workers[]?|select(.id==$id)|.connected'; }
wait_worker() { for _ in $(seq 1 60); do [ "$(wconnected)" = true ] && return 0; sleep 0.5; done; return 1; }
wait_port() { for _ in $(seq 1 40); do (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null && return 0; sleep 0.25; done; return 1; }
tunnels_n() { curl -s -H "Authorization: Bearer $STOK" "http://$ADDR/v1/tunnels" | jq '.tunnels | length'; }

step "build (current tree) -> $BINDIR"
( cd "$REPO" && go build -o "$BIN" ./cmd/gofer && go build -o "$HELP" ./scripts/smoke/tunnel/tunsmoke ) || { echo "build failed"; exit 1; }

step "echo servers"
bg echo1.log "$HELP" serve -portfile "$BASE/echo1.port"; sleep 0.5
bg echo2.log "$HELP" serve -portfile "$BASE/echo2.port"; sleep 0.5
E1=$(cat "$BASE/echo1.port"); E2=$(cat "$BASE/echo2.port"); CLOSED=$("$HELP" freeport)
LP=$("$HELP" freeport); LP2=$("$HELP" freeport)
echo "echo1=$E1 echo2=$E2 closed=$CLOSED local=$LP,$LP2"

cat > "$SRVCFG" <<EOF
server:
  addr: $ADDR
  token: $STOK
  workers:
    $WID:
      token: $WTOK
storage:
  db_path: $BASE/store/gofer.db
  root: $BASE/store/blobs
EOF
write_worker() { # $1 = extra allow entry (optional)
  cat > "$WYAML" <<EOF
worker_id: $WID
server_link:
  urls: [ws://$ADDR/v1/workers/connect]
  token: $WTOK
projects:
  p1:
    host_path: $PROJ/p1
    default_agent: exec
    allowed_agents: [exec]
tunnel:
  allow: ["127.0.0.1:$E1", "127.0.0.1:$CLOSED"${1:+, \"$1\"}]
  max_conns: 2
EOF
}
write_worker

step "start serve + worker"
bg serve.log "$BIN" serve -c "$SRVCFG" --web-dir "$WEBDIR"
wait_http || { echo "serve not healthy"; tail -20 "$OUT/serve.log"; exit 1; }
bg worker.log "$BIN" worker --worker-config "$WYAML"
wait_worker && ok "worker connected" || { bad "worker did not connect"; tail -30 "$OUT/worker.log"; exit 1; }

step "S1 check allowed target"
out=$(g tunnel check -w "$WID" "127.0.0.1:$E1" 2>&1); rc=$?; echo "$out"
[ $rc -eq 0 ] && echo "$out" | grep -q "OK" && ok "S1 check allowed -> OK" || bad "S1 check allowed (rc=$rc)"

step "S2 check target not in allowlist -> 403"
out=$(g tunnel check -w "$WID" "127.0.0.1:$E2" 2>&1); rc=$?; echo "$out"
[ $rc -ne 0 ] && echo "$out" | grep -q "403" && ok "S2 not allowed -> 403" || bad "S2 not allowed (rc=$rc)"

step "S3 allowed but nobody listening -> 502"
out=$(g tunnel check -w "$WID" "127.0.0.1:$CLOSED" 2>&1); rc=$?; echo "$out"
[ $rc -ne 0 ] && echo "$out" | grep -q "502" && ok "S3 dial failed -> 502" || bad "S3 dial failed (rc=$rc)"

step "S4 worker token cannot open a tunnel -> 403"
out=$(g tunnel check -w "$WID" --token "$WTOK" "127.0.0.1:$E1" 2>&1); rc=$?; echo "$out"
[ $rc -ne 0 ] && echo "$out" | grep -q "403" && ok "S4 worker token -> 403" || bad "S4 worker token (rc=$rc)"

step "S5 forward two specs + 200KiB roundtrip through each"
bg forward.log "$BIN" tunnel forward -w "$WID" "$LP:127.0.0.1:$E1" "$LP2:127.0.0.1:$E1"
wait_port "$LP" && wait_port "$LP2" || bad "S5 forward did not listen on $LP/$LP2"
"$HELP" roundtrip -addr "127.0.0.1:$LP" -size 204800 && ok "S5 roundtrip 200KiB via spec 1" || bad "S5 roundtrip spec 1"
"$HELP" roundtrip -addr "127.0.0.1:$LP2" -size 204800 && ok "S5 roundtrip 200KiB via spec 2" || bad "S5 roundtrip spec 2"

step "S6 ls shows an open tunnel, empty after close"
"$HELP" hold -addr "127.0.0.1:$LP" -dur 4s > "$OUT/hold1.log" 2>&1 & H1=$!
sleep 1.5; n=$(tunnels_n); g tunnel ls 2>&1 | tee "$OUT/ls.txt"
[ "$n" = 1 ] && grep -q "127.0.0.1:$E1" "$OUT/ls.txt" && ok "S6 ls shows 1 tunnel" || bad "S6 ls during hold (n=$n)"
wait $H1; sleep 1; n=$(tunnels_n); [ "$n" = 0 ] && ok "S6 ls empty after close" || bad "S6 ls after close (n=$n)"

step "S7 max_conns=2 -> third tunnel 429"
"$HELP" hold -addr "127.0.0.1:$LP" -dur 5s > "$OUT/hold2.log" 2>&1 & H2=$!
"$HELP" hold -addr "127.0.0.1:$LP" -dur 5s > "$OUT/hold3.log" 2>&1 & H3=$!
sleep 1.5; out=$(g tunnel check -w "$WID" "127.0.0.1:$E1" 2>&1); rc=$?; echo "$out"
[ $rc -ne 0 ] && echo "$out" | grep -q "429" && ok "S7 limit -> 429" || bad "S7 limit (rc=$rc)"
wait $H2 $H3

step "S8 reload widens allowlist -> new target reachable"
write_worker "127.0.0.1:$E2"
g worker reload "$WID" > "$OUT/reload.txt" 2>&1; tail -3 "$OUT/reload.txt"
out=$(g tunnel check -w "$WID" "127.0.0.1:$E2" 2>&1); rc=$?; echo "$out"
[ $rc -eq 0 ] && ok "S8 reload -> new target OK" || bad "S8 reload (rc=$rc)"

step "audit log lines (serve / worker)"
grep -E "tunnel (opened|closed|connect failed)" "$OUT/serve.log" | tail -4
grep -E "tunnel (opened|closed|rejected)" "$OUT/worker.log" | tail -4

echo; echo "RESULT: PASS=$PASS FAIL=$FAIL  (logs: $OUT)"
exit "$FAIL"
