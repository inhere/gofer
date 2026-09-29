#!/usr/bin/env bash
set -euo pipefail

# M1 real-process smoke: server-only memory and prime in a directory without a tracker.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
BASE="$(mktemp -d "${TMPDIR:-/tmp}/gofer-memory-smoke.XXXXXX")"
REPO="$BASE/no-tracker"; CFG="$BASE/server.yaml"; BIN="$BASE/gofer"
PORT="${PORT:-$((20000 + RANDOM % 20000))}"; ADDR="127.0.0.1:$PORT"; TOKEN="memory-smoke-token"
mkdir -p "$REPO"
cleanup(){ [ -n "${PID:-}" ] && kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; rm -rf "$BASE"; }
trap cleanup EXIT INT TERM

go build -o "$BIN" "$ROOT/cmd/gofer"
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
EOF
export GOFER_SERVER_ADDR="http://$ADDR" GOFER_SERVER_TOKEN="$TOKEN"
"$BIN" serve -c "$CFG" >"$BASE/serve.log" 2>&1 & PID=$!
for _ in $(seq 1 40); do curl -fsS "http://$ADDR/health" >/dev/null && break; sleep .25; done
curl -fsS "http://$ADDR/health" >/dev/null

"$BIN" -c "$CFG" memory set --server "http://$ADDR" --token "$TOKEN" --global --tag agent:claude smoke-note "claude memory"
CLAUDE="$(cd "$REPO" && "$BIN" -c "$CFG" repo prime --agent claude)"
CODEX="$(cd "$REPO" && "$BIN" -c "$CFG" repo prime --agent codex)"
grep -q "smoke-note" <<<"$CLAUDE"
! grep -q "smoke-note" <<<"$CODEX"
echo "PASS memory real-process smoke addr=$ADDR (no tracker directory)"
