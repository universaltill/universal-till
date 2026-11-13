#!/usr/bin/env bash
# Guard: every workflow triggered by pull_request declares a top-level
# `concurrency:` block, so a new push to a PR cancels the superseded run
# instead of queueing on the shared homelab runners (ut-docs#6145).
# The canonical block cancels only for pull_request events, so pushes to main
# and workflow_dispatch runs are never cancelled:
#   group: ${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}
#   cancel-in-progress: ${{ github.event_name == 'pull_request' }}
set -euo pipefail
WORKFLOW_DIR="${1:-.github/workflows}"
fail=0
for f in "$WORKFLOW_DIR"/*.yml "$WORKFLOW_DIR"/*.yaml; do
  [ -f "$f" ] || continue
  grep -qE '^[[:space:]]{2}pull_request(_target)?:' "$f" || continue
  if ! grep -qE '^concurrency:' "$f"; then
    echo "FAIL: $f is pull_request-triggered but has no top-level concurrency: block (ut-docs#6145)"
    fail=1
  fi
done
[ "$fail" -eq 0 ] && echo "guard-pr-concurrency: ok"
exit "$fail"
