#!/usr/bin/env bash
set -euo pipefail

# U5 smoke contract. The caller supplies a built gofer binary and temporary config.
# This script deliberately never touches a live config directory or a running server.
: "${GOFER_BIN:?set GOFER_BIN to the built gofer binary}"
: "${GOFER_SERVER:?set GOFER_SERVER to an isolated temporary server URL}"

echo "U5 dirlock smoke requires an isolated server/config and a project with nested repositories."
echo "Run two exec jobs with --lock child-a and --lock child-b, then two with --lock child-a."
echo "For repo mode, set project.dir_lock_mode=repo and verify different nested roots overlap only when touched."
echo "GOFER_BIN=$GOFER_BIN"
echo "GOFER_SERVER=$GOFER_SERVER"
