#!/usr/bin/env bash
#
# Guard (ADR-0164 §1, ut-docs#4525): the shared catalogue parser is the
# nested module catimport/ (github.com/universaltill/universal-till/catimport),
# imported by the till through an in-repo replace and by ut-cloud through a
# published catimport/vX.Y.Z tag. Fails when:
#   1. catimport/go.mod is missing or names another module path;
#   2. catimport/go.mod requires or replaces any other universaltill module,
#      or a .go file under catimport/ imports a till package (internal/... or
#      the root module). Go's internal rule would allow that, since the
#      module path sits under the till's, but ut-cloud could not build it;
#   3. the root go.mod lacks the `=> ./catimport` replace (one source: the
#      till must build the in-tree copy, never a published tag);
#   4. parser code is back under internal/: a non-test file there imports
#      excelize, or declares DetectFormat / ParseXLSX.
#
# The version-drift half (ut-cloud's pin vs this tree) is
# check-catimport-pin.sh, run by ut-cloud's manifest-contract-guard job.
#
# Args (fixture testing): [repo root]
set -euo pipefail

ROOT="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
MOD="github.com/universaltill/universal-till/catimport"
FAIL=0
fail() { echo "❌ catimport-module guard: $*" >&2; FAIL=1; }

gomod="${ROOT}/catimport/go.mod"
if [[ ! -f "${gomod}" ]]; then
  fail "catimport/go.mod is missing — the shared parser is a nested module (ADR-0164 §1)"
else
  if ! grep -qx "module ${MOD}" "${gomod}"; then
    fail "catimport/go.mod must declare 'module ${MOD}'"
  fi
  ours="$(grep -n 'github\.com/universaltill/' "${gomod}" | grep -v ':module ' || true)"
  if [[ -n "${ours}" ]]; then
    fail "catimport/go.mod must not require or replace another universaltill module:"
    printf '%s\n' "${ours}" | sed 's/^/  /' >&2
  fi
  bad_imports="$(grep -rnE --include='*.go' '"github\.com/universaltill/universal-till(/[^"]*)?"' "${ROOT}/catimport" |
    grep -vE "\"${MOD//./\\.}(/[^\"]*)?\"" || true)"
  if [[ -n "${bad_imports}" ]]; then
    fail "catimport/ must not import a till package (ut-cloud builds this module on its own):"
    printf '%s\n' "${bad_imports}" | sed "s|^${ROOT}/|  |" >&2
  fi
fi

if ! grep -qE "^replace ${MOD//./\\.} => \./catimport\$" "${ROOT}/go.mod"; then
  fail "go.mod must carry 'replace ${MOD} => ./catimport' so the till builds the in-tree copy"
fi

leftovers="$(grep -rlE --include='*.go' --exclude='*_test.go' \
  -e '"github\.com/xuri/excelize(/v[0-9]+)?"' -e '^func (DetectFormat|ParseXLSX)\(' \
  "${ROOT}/internal" 2>/dev/null || true)"
if [[ -n "${leftovers}" ]]; then
  fail "catalogue parser code under internal/ — it lives in the catimport module (ADR-0164 §1):"
  printf '%s\n' "${leftovers}" | sed "s|^${ROOT}/|  |" >&2
fi

if [[ "${FAIL}" -ne 0 ]]; then
  exit 1
fi
echo "✅ catimport-module guard: catimport/ is a self-contained module, used in-tree by the till"
