#!/usr/bin/env bash
#
# ut-docs#3814: the till advertises itself over mDNS and browses it for other
# tills unless UT_LAN_DISCOVERY is off (internal/discovery SetLANDisabled).
# Every e2e launcher that boots a till (any script setting UT_LISTEN_ADDR)
# must switch that off, or a real till on the developer's LAN leaks its name
# into regenerated manual screenshots and sees the throwaway till.
#
# Comment-only mentions don't count. With no dir argument the repo-root
# Makefile is scanned too (its `e2e` target boots a till).
#
# Usage: guard-e2e-no-lan-discovery.sh [dir]   (default: e2e + Makefile)
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DIR="${1:-${ROOT_DIR}/e2e}"
[[ -d "${DIR}" ]] || { echo "::error::guard-e2e-no-lan-discovery: no such dir ${DIR}"; exit 2; }
files="$(find "${DIR}" -path '*/node_modules' -prune -o -type f \( -name '*.sh' -o -name '*.ts' -o -name Makefile \) -print)"
if [[ $# -eq 0 && -f "${ROOT_DIR}/Makefile" ]]; then
  files="${files}"$'\n'"${ROOT_DIR}/Makefile"
fi
[[ -n "${files}" ]] || { echo "::error::guard-e2e-no-lan-discovery: found no launchers under ${DIR}"; exit 2; }
bad=0
while IFS= read -r f; do
  code="$(grep -Ev '^[[:space:]]*(#|//)' "${f}" || true)"
  grep -q 'UT_LISTEN_ADDR' <<<"${code}" || continue
  # Shell (=0, :=0) or TS (UT_LAN_DISCOVERY: ... '0'); ParseBool's false too.
  if ! grep -Eq "UT_LAN_DISCOVERY(=|:=|:[^,]*['\"])(0|false)" <<<"${code}"; then
    echo "::error file=${f}::starts a till (UT_LISTEN_ADDR) but never sets UT_LAN_DISCOVERY=0 — every run would advertise/browse mDNS on the real LAN (ut-docs#3814)"
    bad=1
  fi
done <<<"${files}"
exit "${bad}"
