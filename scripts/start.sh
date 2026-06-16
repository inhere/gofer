#!/usr/bin/env bash
# Build (if needed) and start the agent-bridge HTTP server.
#
# Override via env:
#   ADDR                 listen address (default 0.0.0.0:8765)
#   AGENT_BRIDGE_TOKEN   bearer token; read by the server via config token_env.
#                        Prefer this over a CLI flag so the token never lands in
#                        process args / shell history.
#   TOKEN                fallback inline token; only passed as --token when set.
#   CONFIG               path to a bridge config file (optional)
set -euo pipefail

cd "$(dirname "$0")/.."

ADDR="${ADDR:-0.0.0.0:8765}"
CONFIG="${CONFIG:-}"

ARGS=(serve --addr "$ADDR")
# Prefer AGENT_BRIDGE_TOKEN (the default token_env): export it and let the server
# read it from the environment. Only fall back to --token TOKEN when AGENT_BRIDGE_TOKEN
# is unset but a TOKEN was provided.
if [[ -z "${AGENT_BRIDGE_TOKEN:-}" && -n "${TOKEN:-}" ]]; then
  ARGS+=(--token "$TOKEN")
fi
if [[ -n "$CONFIG" ]]; then
  ARGS+=(--config "$CONFIG")
fi

# Use the built binary if present, else build then run.
if [[ -x ./dist/agent-bridge ]]; then
  exec ./dist/agent-bridge "${ARGS[@]}"
fi
go build -o ./dist/agent-bridge ./cmd/agent-bridge
exec ./dist/agent-bridge "${ARGS[@]}"
