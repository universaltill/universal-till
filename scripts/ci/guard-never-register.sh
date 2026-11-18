#!/usr/bin/env bash
#
# ADR-0200 (ut-docs#6559): a till registers itself in the cloud and counts in
# the fleet numbers. A throwaway till that a harness boots (e2e, docs-shots,
# device tests, CI smoke runs) must never do that, or every CI run would mint a
# cloud store and inflate the "how many tills are in use" count. The till's own
# honesty guards already refuse a source build (Version "dev"), a demo and a
# test boot, and UT_NEVER_REGISTER=1 (read once by internal/config) is the
# belt-and-braces switch for a harness that runs a release-stamped binary.
#
# Every launcher that boots a till (any script, workflow or Makefile setting
# UT_LISTEN_ADDR) must also set UT_NEVER_REGISTER to 1/true, in the shell form
# (=1, :=1), the TypeScript form (UT_NEVER_REGISTER: ... '1') or the workflow
# env form (UT_NEVER_REGISTER: "1"). Comment-only mentions don't count.
#
# Usage: guard-never-register.sh [dir]
#   default: e2e, .github/workflows and the repo-root Makefile
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
if [[ $# -ge 1 ]]; then
  DIRS=("$1")
else
  DIRS=("${ROOT_DIR}/e2e" "${ROOT_DIR}/.github/workflows")
fi
files=""
for d in "${DIRS[@]}"; do
  [[ -d "${d}" ]] || { echo "::error::guard-never-register: no such dir ${d}"; exit 2; }
  files+="$(find "${d}" -path '*/node_modules' -prune -o -type f \( -name '*.sh' -o -name '*.ts' -o -name '*.yml' -o -name '*.yaml' -o -name Makefile \) -print)"$'\n'
done
if [[ $# -eq 0 && -f "${ROOT_DIR}/Makefile" ]]; then
  files+="${ROOT_DIR}/Makefile"$'\n'
fi
files="$(printf '%s' "${files}" | sed '/^$/d')"
[[ -n "${files}" ]] || { echo "::error::guard-never-register: found no launchers"; exit 2; }
bad=0
while IFS= read -r f; do
  code="$(grep -Ev '^[[:space:]]*(#|//)' "${f}" || true)"
  grep -q 'UT_LISTEN_ADDR' <<<"${code}" || continue
  # UT_NEVER_REGISTER=1, :=1, "1", true; the TS form puts a quote between.
  if grep -Eq "UT_NEVER_REGISTER(=|:=|:[^,]*['\"]?)(1|true)" <<<"${code}"; then
    continue
  fi
  echo "::error file=${f}::starts a till (UT_LISTEN_ADDR) but never sets UT_NEVER_REGISTER=1 — a release-stamped test till would register itself in the cloud and inflate the fleet count (ADR-0200, ut-docs#6559)"
  bad=1
done <<<"${files}"
exit "${bad}"
