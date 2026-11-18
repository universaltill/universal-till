#!/usr/bin/env bash
#
# Regression test for guard-never-register.sh (ADR-0200, ut-docs#6559): the
# guard rejects a till launcher that never sets UT_NEVER_REGISTER, accepts the
# shell, TypeScript and workflow forms, ignores comment-only mentions and
# scripts that don't boot a till.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-never-register.sh"
FAIL_COUNT=0
WORK="$(mktemp -d)"
# Invoked indirectly via `trap ... EXIT` (SC2317 and SC2329 false positives).
# shellcheck disable=SC2317,SC2329
cleanup() {
  local status=$?
  rm -rf "${WORK}"
  exit "${status}"
}
trap cleanup EXIT

expect() {
  local want="$1" name="$2" dir="$3"
  if bash "${GUARD}" "${dir}" >/dev/null 2>&1; then got=pass; else got=fail; fi
  if [[ "${got}" != "${want}" ]]; then
    echo "FAIL: ${name}: want ${want}, got ${got}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "ok: ${name}"
  fi
}

for d in bad good override ts_bad ts_good yml_bad yml_good false comment other make; do
  mkdir -p "${WORK}/${d}"
done
printf '#!/usr/bin/env bash\nexport UT_LISTEN_ADDR=127.0.0.1:8091\n' >"${WORK}/bad/run-till.sh"
printf '#!/usr/bin/env bash\nexport UT_NEVER_REGISTER=1\nexport UT_LISTEN_ADDR=127.0.0.1:8091\n' >"${WORK}/good/run-till.sh"
# The literal ${...} is the point: it's file content, not an expansion.
# shellcheck disable=SC2016
printf '#!/usr/bin/env bash\n: "${UT_NEVER_REGISTER:=1}"; export UT_NEVER_REGISTER\nexport UT_LISTEN_ADDR=127.0.0.1:8092\n' >"${WORK}/override/run-till-x.sh"
printf '#!/usr/bin/env bash\nexport UT_NEVER_REGISTER=0 UT_LISTEN_ADDR=127.0.0.1:1\n' >"${WORK}/false/run.sh"
printf '#!/usr/bin/env bash\n# TODO: set UT_NEVER_REGISTER=1\nexport UT_LISTEN_ADDR=127.0.0.1:8091\n' >"${WORK}/comment/run-till.sh"
printf "const env = { ...process.env, UT_LISTEN_ADDR: '127.0.0.1:1' };\n" >"${WORK}/ts_bad/worker.ts"
printf "const env = { UT_NEVER_REGISTER: process.env.UT_NEVER_REGISTER ?? '1', UT_LISTEN_ADDR: 'x' };\n" >"${WORK}/ts_good/worker.ts"
printf 'jobs:\n  e2e:\n    steps:\n      - run: UT_LISTEN_ADDR=127.0.0.1:8080 ./app &\n' >"${WORK}/yml_bad/ci.yml"
printf 'jobs:\n  e2e:\n    steps:\n      - run: UT_NEVER_REGISTER=1 UT_LISTEN_ADDR=127.0.0.1:8080 ./app &\n' >"${WORK}/yml_good/ci.yml"
printf '#!/usr/bin/env bash\necho no till here\n' >"${WORK}/other/helper.sh"
printf 'e2e:\n\tUT_LISTEN_ADDR=127.0.0.1:8080 go run .\n' >"${WORK}/make/Makefile"

expect fail "launcher without UT_NEVER_REGISTER" "${WORK}/bad"
expect pass "launcher exporting UT_NEVER_REGISTER=1" "${WORK}/good"
expect pass "launcher defaulting UT_NEVER_REGISTER to 1" "${WORK}/override"
expect fail "launcher setting UT_NEVER_REGISTER=0" "${WORK}/false"
expect fail "launcher mentioning it only in a comment" "${WORK}/comment"
expect fail "TS spawner without UT_NEVER_REGISTER" "${WORK}/ts_bad"
expect pass "TS spawner defaulting UT_NEVER_REGISTER to '1'" "${WORK}/ts_good"
expect fail "workflow step booting a till without it" "${WORK}/yml_bad"
expect pass "workflow step setting it inline" "${WORK}/yml_good"
expect fail "Makefile target booting a till without it" "${WORK}/make"
expect pass "script that boots no till" "${WORK}/other"

# And the real repo passes.
if bash "${GUARD}" >/dev/null 2>&1; then echo "ok: the repo's launchers"; else
  echo "FAIL: the repo's own launchers do not set UT_NEVER_REGISTER"
  bash "${GUARD}" || true
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

exit "${FAIL_COUNT}"
