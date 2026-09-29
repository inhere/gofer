#!/usr/bin/env bash
set -euo pipefail

# TRK-01 P4 real-process smoke. Starts only the binary built from this tree.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
BASE="$(mktemp -d "${TMPDIR:-/tmp}/gofer-tracker-smoke.XXXXXX")"
REPO="$BASE/repo"; CFGDIR="$BASE/config"; CFG="$BASE/server.yaml"; BIN="$BASE/gofer"
PORT="${PORT:-$((20000 + RANDOM % 20000))}"; ADDR="127.0.0.1:$PORT"; TOKEN="tracker-smoke-token"
mkdir -p "$REPO" "$CFGDIR"
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
    allowed_agents: [exec]
    allowed_runners: [local]
    allow_exec: true
EOF
printf 'GOFER_SERVER_ADDR=http://%s\nGOFER_SERVER_TOKEN=%s\n' "$ADDR" "$TOKEN" > "$CFGDIR/.env"
export GOFER_CONFIG_DIR="$CFGDIR" GOFER_CONFIG="$CFG" GOFER_SERVER_ADDR="http://$ADDR" GOFER_SERVER_TOKEN="$TOKEN"
(cd "$REPO" && "$BIN" repo init --no-agents-md)
"$BIN" serve -c "$CFG" >"$BASE/serve.log" 2>&1 & PID=$!
for _ in $(seq 1 40); do curl -fsS "http://$ADDR/health" >/dev/null && break; sleep .25; done
curl -fsS "http://$ADDR/health" >/dev/null
TRACKER_ID="$(awk '/tracker_id:/ {print $2}' "$REPO/.gofer/tracker/config.yaml")"
kill -STOP "$PID"; (cd "$REPO" && "$BIN" issue create -t offline --type task >/dev/null && "$BIN" memory set offline value >/dev/null); kill -CONT "$PID"
(cd "$REPO" && "$BIN" repo sync >/dev/null)
ISSUE_ID="$(jq -r .id "$REPO/.gofer/tracker/issues.jsonl")"
# The offline auto-sync request may still be delivered once the server resumes
# (at-least-once), so read the current rev instead of assuming 1.
ISSUE_REV="$(curl -fsS -H "Authorization: Bearer $TOKEN" "http://$ADDR/v1/tracker/issues?tracker_id=$TRACKER_ID" | jq -r --arg id "$ISSUE_ID" '.issues[]|select(.id==$id)|.rev')"
curl -fsS -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' "http://$ADDR/v1/tracker/issues/$ISSUE_ID?tracker_id=$TRACKER_ID" -d "{\"status\":\"blocked\",\"expected_rev\":$ISSUE_REV}" >/dev/null
curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' "http://$ADDR/v1/tracker/issues/$ISSUE_ID/comments?tracker_id=$TRACKER_ID" -d '{"text":"smoke comment"}' >/dev/null
curl -fsS -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' "http://$ADDR/v1/tracker/memories/offline?tracker_id=$TRACKER_ID" -d '{"content":"edited"}' >/dev/null
(cd "$REPO" && "$BIN" repo sync >/dev/null)
grep -q 'blocked' "$REPO/.gofer/tracker/issues.jsonl"; grep -q 'smoke comment' "$REPO/.gofer/tracker/issues.jsonl"; grep -q 'edited' "$REPO/.gofer/tracker/memories.jsonl"
ISSUES_BEFORE=$(sha256sum "$REPO/.gofer/tracker/issues.jsonl" | cut -d' ' -f1); MEM_BEFORE=$(sha256sum "$REPO/.gofer/tracker/memories.jsonl" | cut -d' ' -f1); BASE_BEFORE=$(sha256sum "$REPO/.gofer/tracker/.local/sync-base.jsonl" | cut -d' ' -f1)
(cd "$REPO" && "$BIN" repo sync >/dev/null)
[ "$ISSUES_BEFORE" = "$(sha256sum "$REPO/.gofer/tracker/issues.jsonl" | cut -d' ' -f1)" ]; [ "$MEM_BEFORE" = "$(sha256sum "$REPO/.gofer/tracker/memories.jsonl" | cut -d' ' -f1)" ]; [ "$BASE_BEFORE" = "$(sha256sum "$REPO/.gofer/tracker/.local/sync-base.jsonl" | cut -d' ' -f1)" ]
echo "PASS tracker real-process smoke addr=$ADDR (sync-status timestamp may change on second sync)"
