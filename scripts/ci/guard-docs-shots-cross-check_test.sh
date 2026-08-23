#!/usr/bin/env bash
#
# Cross-check test (ut-docs#370): scripts/ci/guard-docs-shots.sh (Python) and
# e2e/tests-docs/lib.js (JS) are meant to be a byte-identical spec mirror —
# every comment in both files says so — but nothing actually enforced that
# claim. On 2026-08-06 the two agreed with each other while a stale recorded
# `surface_sha256` in web/help/img/manifest.json (most likely produced by an
# environment with untracked dotfiles like macOS .DS_Store present when
# `make docs-shots` ran, back when the algorithm did no dotfile filtering —
# see docs/code-reviews/2026-08-06-docs-shots-surface-hash-mismatch.md and
# the ut-docs#370 root-cause writeup) blocked all of `main`'s CI until
# universal-till#195 corrected it. That incident was the two implementations
# still agreeing — this test is the backstop for the day they genuinely
# don't: a real algorithmic divergence between the Python and JS copies
# should fail fast and legibly here, not surface as an unexplained red
# `guard-docs-shots` days later with no clue why.
#
# Deliberately runs against the REAL working tree, not a synthetic fixture:
# the two implementations already scan the same live directories on every
# real run, so the working tree at HEAD *is* the shared fixture both sides
# must agree on — inventing a separate one would just be another tree that
# could itself drift from what the guard actually checks in CI.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

PY_HASH="$(GUARD_DOCS_SHOTS_PRINT_SURFACE_ONLY=1 bash scripts/ci/guard-docs-shots.sh)"
JS_HASH="$(node -e "console.log(require('./e2e/tests-docs/lib.js').buildManifest().surface_sha256)")"

if [[ "${PY_HASH}" == "${JS_HASH}" ]]; then
  echo "✓ guard-docs-shots cross-check: Python and JS agree (${PY_HASH:0:12}…)"
  exit 0
fi

echo "❌ FAIL: scripts/ci/guard-docs-shots.sh (Python) and e2e/tests-docs/lib.js (JS)" >&2
echo "   computed DIFFERENT surface_sha256 values against the same working tree:" >&2
echo "     Python: ${PY_HASH}" >&2
echo "     JS:     ${JS_HASH}" >&2
echo "   The two implementations have genuinely diverged — reconcile them" >&2
echo "   (they must stay in lockstep; see the HASH ALGORITHM comment atop" >&2
echo "   each file) before merging." >&2
exit 1
