#!/usr/bin/env bash
# Build the three binaries run-smoke.sh needs, into tmp/p3-t7/bin (gitignored).
#
#   gofer-p3  = CURRENT tree (proto v4) — what T7 validates
#   gofer-v3  = commit f11669d (proto v3, pre-roots) — the rollback / v3-matrix anchor
#   gofer-old-v2 (tmp/) = a genuine proto-v2 binary; reused if already present, else
#                          built from the earliest proto-v2 commit is NOT attempted here
#                          (it predates the current module layout) — see README.md.
#
# Uses an isolated git worktree for f11669d so the main tree is never touched, and
# removes it afterwards. Set GOCACHE to a scratch dir to avoid polluting the shared cache.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
BASE="$REPO/tmp/p3-t7"
BIN="$BASE/bin"
V3_COMMIT=f11669d           # last commit before P3 T0: proto v3, no roots (== dcc98dd/c3ee6d1 semantics)
export PATH=/d/env/linux-env/sdk/gosdk/go1.25.10/bin:$PATH
: "${GOCACHE:=$BASE/.gocache}"; export GOCACHE

mkdir -p "$BIN"

echo "==> building gofer-p3 (current tree)"
( cd "$REPO" && go build -o "$BIN/gofer-p3" ./cmd/gofer )

echo "==> building gofer-v3 (commit $V3_COMMIT) in an isolated worktree"
WT="$BASE/wt/gofer-v3"
if [ ! -d "$WT" ]; then ( cd "$REPO" && git worktree add -f "$WT" "$V3_COMMIT" ); fi
( cd "$WT" && go build -o "$BIN/gofer-v3" ./cmd/gofer )
( cd "$REPO" && git worktree remove --force "$WT" ) || true

if [ -x "$REPO/tmp/gofer-old-v2" ]; then
  echo "==> reusing existing proto-v2 binary: $REPO/tmp/gofer-old-v2"
else
  echo "!! $REPO/tmp/gofer-old-v2 missing — the v2 matrix cell (V16e/f) will be UNCOVERED."
  echo "   Provide a genuine proto-v2 gofer build there (see README.md commit-anchor table)."
fi

echo "==> done:"; ls -la "$BIN"
