#!/usr/bin/env bash
# Build the two binaries run-smoke.sh needs, into tmp/decision-channel/bin
# (gitignored). Windows host: binaries carry the .exe suffix.
#
#   gofer(.exe)    = CURRENT tree — what T5 validates
#   mcp-call(.exe) = the one-shot MCP stdio driver (./mcpcall)
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
BASE="$REPO/tmp/decision-channel"
BIN="$BASE/bin"

mkdir -p "$BIN"

echo "==> building gofer (current tree)"
( cd "$REPO" && go build -o "$BIN/gofer.exe" ./cmd/gofer )

echo "==> building mcp-call"
( cd "$REPO" && go build -o "$BIN/mcp-call.exe" ./scripts/smoke/decision-channel/mcpcall )

echo "==> done:"; ls -la "$BIN"
