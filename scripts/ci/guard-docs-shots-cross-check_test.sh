#!/usr/bin/env bash
#
# Cross-check test (ut-docs#370): scripts/ci/guard-docs-shots.sh (Python) and
# e2e/tests-docs/lib.js (JS) are meant to be a byte-identical spec mirror —
# every comment in both files says so — but nothing actually enforced that
# claim. On 2026-08-06 the two agreed with each other while a stale recorded
# `surface_sha256` (since ut-docs#6654 a per-file `surface_files` record) in web/help/img/manifest.json (most likely produced by an
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

# The per-file surface record (ut-docs#6654) in sha256sum format, one line per
# surface file, compared in full so a divergence names the file(s).
PY_REC="$(GUARD_DOCS_SHOTS_PRINT_SURFACE_ONLY=1 bash scripts/ci/guard-docs-shots.sh)"
JS_REC="$(node -e "for (const [p, v] of Object.entries(require('./e2e/tests-docs/lib.js').surfaceRecord())) console.log(v.sha256 + '  ' + p)")"

# The declared extra shots (ut-docs#2615) must agree too: the same en-markdown
# references, derived independently by the guard and by lib.js.
PY_SHOTS="$(GUARD_DOCS_SHOTS_PRINT_SHOTS_ONLY=1 bash scripts/ci/guard-docs-shots.sh)"
JS_SHOTS="$(node -e "const s=require('./e2e/tests-docs/lib.js').extraShotsByTopic(); for (const t of Object.keys(s).sort()) for (const x of s[t]) console.log(t+' '+x)")"
if [[ "${PY_SHOTS}" != "${JS_SHOTS}" ]]; then
  echo "❌ FAIL: guard-docs-shots.sh (Python) and lib.js (JS) derive DIFFERENT extra shots:" >&2
  echo "     Python: ${PY_SHOTS}" >&2
  echo "     JS:     ${JS_SHOTS}" >&2
  exit 1
fi

if [[ -n "${PY_REC}" && "${PY_REC}" == "${JS_REC}" ]]; then
  echo "✓ guard-docs-shots cross-check: Python and JS agree ($(echo "${PY_REC}" | grep -c . || true) surface files, extra shots: $(echo "${PY_SHOTS}" | grep -c . || true))"
  exit 0
fi

echo "❌ FAIL: scripts/ci/guard-docs-shots.sh (Python) and e2e/tests-docs/lib.js (JS)" >&2
echo "   computed DIFFERENT per-file surface records against the same working tree" >&2
echo "   (diff: < Python, > JS):" >&2
REC_DIFF="$(diff <(echo "${PY_REC}") <(echo "${JS_REC}") || true)"
sed -n '1,40p' <<<"${REC_DIFF}" >&2
echo "   The two implementations have genuinely diverged — reconcile them" >&2
echo "   (they must stay in lockstep; see the HASH ALGORITHM comment atop" >&2
echo "   each file) before merging." >&2
exit 1
