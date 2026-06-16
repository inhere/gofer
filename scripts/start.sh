#!/usr/bin/env bash
# Start the agent-bridge HTTP server.
# Override via env: ADDR, TOKEN, CONFIG.
set -euo pipefail

cd "$(dirname "$0")/.."

ADDR="${ADDR:-0.0.0.0:8765}"
TOKEN="${TOKEN:-dev-token}"
CONFIG="${CONFIG:-}"

ARGS=(serve --addr "$ADDR" --token "$TOKEN")
if [[ -n "$CONFIG" ]]; then
  ARGS+=(--config "$CONFIG")
fi

if [[ -x ./dist/agent-bridge ]]; then
  exec ./dist/agent-bridge "${ARGS[@]}"
else
  exec go run ./cmd/agent-bridge "${ARGS[@]}"
fi
