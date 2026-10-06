#!/usr/bin/env bash
#
# CI entry point for the manual screenshot freshness guard (ut-docs#2794).
# surface_sha256 in web/help/img/manifest.json covers nearly the whole app
# UI surface, so enforcing guard-docs-shots.sh on every pull_request made
# any two concurrent UI PRs conflict on that generated file: whichever
# merged second had to merge main, re-run `make docs-shots`, push and sit
# through the full CI run again — sometimes for several rounds. On a
# pull_request run a stale result is therefore reported as a `::warning::`
# and does not block the PR; on push to main (and anywhere
# GITHUB_EVENT_NAME is not `pull_request`, including a local run) the
# guard stays hard-blocking, so real drift still turns main red right after
# merge and is fixed like any other red-main build. The guard itself is
# unchanged — see guard-docs-shots.sh for what it checks and why.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

# Overridable ONLY so run-docs-shots-guard_test.sh can substitute an
# exit-0 / exit-1 stub for the real guard (which needs a full app build and
# Playwright to mean anything). CI never sets it.
GUARD_DOCS_SHOTS_BIN="${GUARD_DOCS_SHOTS_BIN:-${ROOT_DIR}/scripts/ci/guard-docs-shots.sh}"

status=0
bash "${GUARD_DOCS_SHOTS_BIN}" || status=$?

if [ "${status}" -eq 0 ]; then
  exit 0
fi

if [ "${GITHUB_EVENT_NAME:-}" = "pull_request" ]; then
  echo "::warning::guard-docs-shots: screenshots are stale relative to this branch (guard exit ${status}) — not blocking the PR (ut-docs#2794). main's own push-triggered run of this same guard still enforces freshness right after merge." >&2
  exit 0
fi

exit "${status}"
