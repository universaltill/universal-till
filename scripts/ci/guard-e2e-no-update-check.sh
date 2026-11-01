#!/usr/bin/env bash
#
# ut-docs#5237: the till polls the real GitHub releases (internal/updates)
# unless UT_UPDATE_CHECK is off. A throwaway e2e till that does would show
# "Update now vX.Y.Z" in the status bar as soon as a newer release exists —
# wrapping the bar at phone widths (failing specs) and changing regenerated
# manual screenshots. Every e2e launcher that boots a till (any script
# setting UT_LISTEN_ADDR) must either switch the check off, or point
# UT_UPDATE_RELEASES_URL at a loopback fake (run-till-incoming-notes.sh).
#
# Comment-only mentions don't count. With no dir argument the repo-root
# Makefile is scanned too (its `e2e` target boots a till).
#
# Usage: guard-e2e-no-update-check.sh [dir]   (default: e2e + Makefile)
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DIR="${1:-${ROOT_DIR}/e2e}"
[[ -d "${DIR}" ]] || { echo "::error::guard-e2e-no-update-check: no such dir ${DIR}"; exit 2; }
files="$(find "${DIR}" -path '*/node_modules' -prune -o -type f \( -name '*.sh' -o -name '*.ts' -o -name Makefile \) -print)"
if [[ $# -eq 0 && -f "${ROOT_DIR}/Makefile" ]]; then
  files="${files}"$'\n'"${ROOT_DIR}/Makefile"
fi
[[ -n "${files}" ]] || { echo "::error::guard-e2e-no-update-check: found no launchers under ${DIR}"; exit 2; }
bad=0
while IFS= read -r f; do
  code="$(grep -Ev '^[[:space:]]*(#|//)' "${f}" || true)"
  grep -q 'UT_LISTEN_ADDR' <<<"${code}" || continue
  # Shell (=0, :=0) or TS (UT_UPDATE_CHECK: ... '0'); ParseBool's false too.
  if grep -Eq "UT_UPDATE_CHECK(=|:=|:[^,]*['\"])(0|false)" <<<"${code}"; then
    continue
  fi
  # Or a releases URL that only reaches a loopback fake.
  if grep -Eq "UT_UPDATE_RELEASES_URL(=|:=|:[^,]*)[\"']?http://(127\.0\.0\.1|localhost)([:/\"'\$]|\$)" <<<"${code}"; then
    continue
  fi
  echo "::error file=${f}::starts a till (UT_LISTEN_ADDR) but never sets UT_UPDATE_CHECK=0 (or a loopback UT_UPDATE_RELEASES_URL) — a new GitHub release would show \"Update now\" mid-run and move the status bar (ut-docs#5237)"
  bad=1
done <<<"${files}"
exit "${bad}"
