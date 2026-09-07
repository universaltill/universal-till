#!/usr/bin/env bash
#
# ut-docs#1581 (split from ut-docs#1565): whole-program `deadcode` baseline,
# analyzed under the `desktop` build tag so `cmd/unitill-desktop` type-checks
# at all (cgo + real GTK/WebKit).
#
# IMPORTANT — this is a WHOLE-PROGRAM analysis, not scoped to
# cmd/unitill-desktop alone (independent review, ut-docs#1581): only 1 of
# the 78 seeded baseline entries is actually under cmd/unitill-desktop/; the
# other 77 are `internal/**` code unreachable from either of this script's
# two roots. That's deliberate, not a bug -- `deadcode` needs real entry
# points to compute reachability, and scoping the *roots* to just
# cmd/unitill-desktop (dropping `.`, the server main) would make every
# internal/ function only the server ever calls look "unreachable" too, a
# far worse false-positive class than the one this script actually has.
# What it means in practice: a finding can legitimately be anywhere in the
# module, including packages a given PR never touched.
#
# `.golangci.yml`'s `unused` gate (ut-docs#1565) deliberately excludes
# cmd/unitill-desktop: linting it WITHOUT the `desktop` tag produces false
# "unused" positives on genuine per-platform stub functions
# (attach_gate_other.go/startup_gate_other.go are only called from their
# `desktop`-tagged sibling files; shell_poll.go's two symbols are only
# called from webview_fallback.go, tag `desktop && !darwin`) -- deleting any
# of those would break the real desktop build. Doing this analysis WITH the
# `desktop` tag, as this script does, sees those call sites and does not
# flag them (verified while writing this guard: 0 false positives across
# either of the two stub files or shell_poll.go).
#
# This is a BASELINE gate, not a zero-tolerance one -- the 2026-09-04
# measurement found ~97 pre-existing unreachable functions across ~1,242
# commits that never had any gate on cmd/unitill-desktop before now
# (ut-docs#1566 tracks burning that number down separately, once this gate
# exists to keep it from regrowing). So: a NEW unreachable function not
# already in the baseline fails the PR; an entry that disappears (someone
# burned it down) does not -- that's a strict improvement and this guard
# doesn't force a baseline update to notice it. Update the baseline
# manually (regenerate with this script's own DEADCODE_PKG below, normalize,
# `sort`) when you deliberately add or remove a baseline entry.
#
# Known false-positive shape worth knowing before deleting anything this
# flags as new: `deadcode` runs with `-test=false`, so a call site that
# exists ONLY in a `_test.go` file is invisible to it -- an exported
# production-code helper used exclusively by its own tests (several
# existing baseline entries look like this, e.g. `ResetCacheForTests`)
# reads as "unreachable" even though it's genuinely in use. Check callers
# in `_test.go` files before deleting a flagged function; if it's
# test-only-reachable, that's a real (if odd) baseline entry, not a bug in
# this guard.
#
# Requires the same GTK/WebKit dev headers as the desktop-shell CI job
# (cgo + real windowing libs to even type-check cmd/unitill-desktop under
# `desktop`) -- mirror its "Install GTK/WebKit dev headers" step wherever
# this runs.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

BASELINE_FILE="scripts/ci/deadcode-baseline.txt"
# Pinned to the same golang.org/x/tools version already resolved in go.sum
# (as an indirect dependency) -- avoids pulling a second, different version
# of the module just to run its `deadcode` command, and keeps this guard's
# findings reproducible across runs rather than drifting with @latest.
DEADCODE_PKG="golang.org/x/tools/cmd/deadcode@v0.48.0"

if [[ ! -f "${BASELINE_FILE}" ]]; then
  echo "❌ deadcode-baseline guard: ${BASELINE_FILE} is missing" >&2
  exit 1
fi

raw_output="$(go run "${DEADCODE_PKG}" -tags=desktop -test=false . ./cmd/unitill-desktop ./cmd/unitill-uninstall 2>/tmp/deadcode-baseline-guard-stderr.$$)" \
  || {
    echo "❌ deadcode-baseline guard: 'deadcode' itself failed" >&2
    cat /tmp/deadcode-baseline-guard-stderr.$$ >&2
    rm -f /tmp/deadcode-baseline-guard-stderr.$$
    exit 1
  }
rm -f /tmp/deadcode-baseline-guard-stderr.$$

# Strip ":line:col:" so unrelated line-number churn elsewhere in a changed
# file doesn't spuriously fail this guard -- keep "file: unreachable func:
# Name", which is what actually identifies a finding. The `|| true` matters
# under `set -eo pipefail`: once the baseline is fully burned down (the
# ut-docs#1566 goal), `deadcode` legitimately reports zero "unreachable
# func:" lines, `grep` alone would exit 1 on no match, and pipefail would
# kill this script silently (empty output, no error message) right at the
# milestone this gate exists to protect -- independent review, ut-docs#1581.
current="$(echo "${raw_output}" | { grep 'unreachable func:' || true; } | sed -E 's/^([^:]+):[0-9]+:[0-9]+: /\1: /' | sort -u)"
baseline="$(sort -u "${BASELINE_FILE}")"

new_entries="$(comm -23 <(echo "${current}") <(echo "${baseline}"))"

if [[ -n "${new_entries}" ]]; then
  echo "❌ deadcode-baseline guard: new unreachable function(s) not in ${BASELINE_FILE}:" >&2
  echo "${new_entries}" >&2
  echo "" >&2
  echo "If this is genuinely dead code, remove it. If it's a false positive" >&2
  echo "(e.g. called only from a differently-tagged file this pass doesn't" >&2
  echo "compile), investigate before adding it to the baseline -- the" >&2
  echo "baseline is meant to shrink over time (ut-docs#1566), not grow." >&2
  exit 1
fi

burned_down="$(comm -13 <(echo "${current}") <(echo "${baseline}"))"
if [[ -n "${burned_down}" ]]; then
  burned_count="$(echo "${burned_down}" | wc -l | tr -d ' ')"
  echo "✓ deadcode-baseline guard: ${burned_count} baseline entry/entries no longer found (burned down; not required, but consider refreshing ${BASELINE_FILE}):"
  echo "${burned_down}"
fi

echo "✓ deadcode-baseline guard: no new unreachable functions (whole-program, desktop tag) beyond the checked-in baseline"
