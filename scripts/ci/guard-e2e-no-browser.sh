#!/usr/bin/env bash
#
# ut-docs#2704: the till server opens the OS default browser on start
# unless UT_OPEN_BROWSER is set (internal/server/server.go
# shouldOpenBrowser). Every e2e launcher that boots a till (any script
# setting UT_LISTEN_ADDR) must disable that, or each test run opens real
# browser tabs on the developer's desktop. Playwright itself is headless.
#
# Usage: guard-e2e-no-browser.sh [dir]   (default: e2e)
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DIR="${1:-${ROOT_DIR}/e2e}"
[[ -d "${DIR}" ]] || { echo "::error::guard-e2e-no-browser: no such dir ${DIR}"; exit 2; }
files="$(find "${DIR}" -path '*/node_modules' -prune -o -type f \( -name '*.sh' -o -name '*.ts' \) -print)"
[[ -n "${files}" ]] || { echo "::error::guard-e2e-no-browser: found no launchers under ${DIR}"; exit 2; }
bad=0
while IFS= read -r f; do
  grep -q 'UT_LISTEN_ADDR' "${f}" || continue
  # Shell (=0, :=0) or TS (UT_OPEN_BROWSER: ... '0'); ParseBool's false too.
  if ! grep -Eq "UT_OPEN_BROWSER(=|:=|:[^,]*['\"])(0|false)" "${f}"; then
    echo "::error file=${f}::starts a till (UT_LISTEN_ADDR) but never sets UT_OPEN_BROWSER=0 — every run would open a browser tab (ut-docs#2704)"
    bad=1
  fi
done <<<"${files}"
exit "${bad}"
