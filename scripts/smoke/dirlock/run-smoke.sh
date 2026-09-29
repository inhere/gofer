#!/usr/bin/env bash
set -euo pipefail

# U5 real-process smoke. Linux/macOS runner only; Windows supervision should run
# this script inside the project container because it needs bash, git and curl.
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    echo "SKIP dirlock real-process smoke on Windows; run it in the Linux project container"
    exit 0
    ;;
esac
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
BASE="$(mktemp -d "${TMPDIR:-/tmp}/gofer-dirlock-smoke.XXXXXX")"
REPO="$BASE/repo"; CFGDIR="$BASE/config"; CFG="$BASE/server.yaml"; BIN="$BASE/gofer"
PORT="${PORT:-$((20000 + RANDOM % 20000))}"; ADDR="127.0.0.1:$PORT"; TOKEN="dirlock-smoke-token"
mkdir -p "$REPO" "$CFGDIR" "$REPO/a" "$REPO/b" "$REPO/markers"
cleanup(){ [ -n "${PID:-}" ] && kill "$PID" 2>/dev/null || true; wait "${PID:-}" 2>/dev/null || true; rm -rf "$BASE"; }
trap cleanup EXIT INT TERM

go build -o "$BIN" "$ROOT/cmd/gofer"
git -C "$REPO/a" init -q
git -C "$REPO/b" init -q
cat > "$CFG" <<EOF
server:
  addr: $ADDR
  token: $TOKEN
  web_enabled: false
storage:
  db_path: $BASE/gofer.db
projects:
  smoke:
    host_path: $REPO
    dir_lock_mode: repo
    allowed_agents: [exec]
    allowed_runners: [local]
    allow_exec: true
EOF
printf 'GOFER_SERVER_ADDR=http://%s\nGOFER_SERVER_TOKEN=%s\n' "$ADDR" "$TOKEN" > "$CFGDIR/.env"
export GOFER_CONFIG_DIR="$CFGDIR" GOFER_CONFIG="$CFG" GOFER_SERVER_ADDR="http://$ADDR" GOFER_SERVER_TOKEN="$TOKEN"
"$BIN" serve -c "$CFG" >"$BASE/serve.log" 2>&1 & PID=$!
for _ in $(seq 1 40); do curl -fsS "http://$ADDR/health" >/dev/null && break; sleep .25; done
curl -fsS "http://$ADDR/health" >/dev/null

run_job() {
  local lock="$1" marker="$2"
  "$BIN" --server "http://$ADDR" --token "$TOKEN" job run --project smoke --agent exec --runner local --cwd . --lock "$lock" --sync --wait-timeout 10 -- sh -c "date +%s%N > markers/${marker}.start; sleep 2; date +%s%N > markers/${marker}.end"
}

run_job a parallel-a >"$BASE/parallel-a.log" 2>&1 & p1=$!
run_job b parallel-b >"$BASE/parallel-b.log" 2>&1 & p2=$!
wait "$p1"; wait "$p2"
a0=$(cat "$REPO/markers/parallel-a.start"); a1=$(cat "$REPO/markers/parallel-a.end")
b0=$(cat "$REPO/markers/parallel-b.start"); b1=$(cat "$REPO/markers/parallel-b.end")
if [ "$a0" -ge "$b1" ] || [ "$b0" -ge "$a1" ]; then
  echo "parallel jobs did not overlap: $a0-$a1 $b0-$b1" >&2; exit 1
fi

run_job a serial-a >"$BASE/serial-a.log" 2>&1 & p1=$!
sleep .2
run_job a serial-b >"$BASE/serial-b.log" 2>&1 & p2=$!
wait "$p1"; wait "$p2"
a0=$(cat "$REPO/markers/serial-a.start"); a1=$(cat "$REPO/markers/serial-a.end")
b0=$(cat "$REPO/markers/serial-b.start"); b1=$(cat "$REPO/markers/serial-b.end")
if [ "$a0" -lt "$b1" ] && [ "$b0" -lt "$a1" ]; then
  echo "same lock jobs overlapped: $a0-$a1 $b0-$b1" >&2; exit 1
fi

if "$BIN" --server "http://$ADDR" --token "$TOKEN" job run --project smoke --agent exec --runner local --cwd . --sync --wait-timeout 10 -- sh -c 'exit 0' >"$BASE/repo-rejected.log" 2>&1; then
  echo "repo-mode submit without lock unexpectedly succeeded" >&2; exit 1
fi
grep -q -- 'repo lock mode requires an explicit lock declaration' "$BASE/repo-rejected.log"
grep -q -- 'a' "$BASE/repo-rejected.log"
grep -q -- 'b' "$BASE/repo-rejected.log"
"$BIN" --server "http://$ADDR" --token "$TOKEN" job run --project smoke --agent exec --runner local --cwd . --lock a --sync --wait-timeout 10 -- sh -c 'exit 0' >/dev/null

echo "PASS dirlock real-process smoke addr=$ADDR"
