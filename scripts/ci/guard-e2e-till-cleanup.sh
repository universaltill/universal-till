#!/usr/bin/env bash
#
# ut-docs#7177: every e2e launcher makes a `mktemp -d` data dir holding a
# ~100 MB copy of the till binary and removes it from an EXIT trap. Two ways
# the trap used to be skipped, so each run left a dir behind:
#   (a) `exec "$BIN"` replaces the shell (the trap never fires) — launchers
#       serve through `ut_e2e_serve` (e2e/run-till-lib.sh) instead;
#   (b) Playwright stops a webServer with SIGKILL unless it has a
#       `gracefulShutdown` — every entry sets `gracefulShutdown: TILL_SHUTDOWN`
#       (e2e/till-webserver.ts: SIGTERM then SIGKILL after a timeout).
# Comment-only mentions don't count.
#
# Usage: guard-e2e-till-cleanup.sh [dir]   (default: e2e)
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DIR="${1:-${ROOT_DIR}/e2e}"
[[ -d "${DIR}" ]] || { echo "::error::guard-e2e-till-cleanup: no such dir ${DIR}"; exit 2; }
launchers="$(find "${DIR}" -path '*/node_modules' -prune -o -type f -name 'run-till*.sh' -print)"
configs="$(find "${DIR}" -path '*/node_modules' -prune -o -type f -name 'playwright*.ts' -print)"
[[ -n "${launchers}${configs}" ]] || { echo "::error::guard-e2e-till-cleanup: found no launchers or configs under ${DIR}"; exit 2; }
bad=0
while IFS= read -r f; do
  [[ -n "${f}" ]] || continue
  code="$(grep -Ev '^[[:space:]]*#' "${f}" || true)"
  if grep -Eq '^[[:space:]]*exec[[:space:]]+"?\$' <<<"${code}"; then
    echo "::error file=${f}::execs a variable binary — exec drops the EXIT trap, so the throwaway data dir (~100 MB) is never removed; use ut_e2e_serve from run-till-lib.sh (ut-docs#7177)"
    bad=1
  fi
done <<<"${launchers}"
while IFS= read -r f; do
  [[ -n "${f}" ]] || continue
  code="$(grep -Ev '^[[:space:]]*//' "${f}" || true)"
  cmds="$(grep -Ec "(^|[{,[:space:]])command:[[:space:]]" <<<"${code}" || true)"
  shut="$(grep -Ec "(^|[{,[:space:]])gracefulShutdown:[[:space:]]*TILL_SHUTDOWN([^A-Za-z0-9_]|$)" <<<"${code}" || true)"
  if [[ "${cmds}" != "${shut}" ]]; then
    echo "::error file=${f}::${cmds} webServer command(s) but ${shut} 'gracefulShutdown: TILL_SHUTDOWN' — Playwright's default SIGKILL skips the launcher's EXIT trap and leaks the data dir (ut-docs#7177)"
    bad=1
  fi
done <<<"${configs}"
exit "${bad}"
