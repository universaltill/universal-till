#!/usr/bin/env bash
#
# Regression test for guard-e2e-no-lan-discovery.sh (ut-docs#3814): proves the
# guard rejects a till launcher that doesn't disable the server's
# LAN (mDNS) discovery, accepts one that does, and ignores scripts that
# don't start a till at all.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-e2e-no-lan-discovery.sh"
FAIL_COUNT=0
WORK="$(mktemp -d)"
# Invoked indirectly via `trap ... EXIT` (SC2317 false positive).
# shellcheck disable=SC2317
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

mkdir -p "${WORK}/comment" "${WORK}/make" "${WORK}/badts" "${WORK}/goodts" "${WORK}/false" "${WORK}/bad" "${WORK}/good" "${WORK}/other" "${WORK}/override"
printf '#!/usr/bin/env bash\nexport UT_DATA_DIR=x UT_LISTEN_ADDR=127.0.0.1:8091\n' >"${WORK}/bad/run-till.sh"
printf '#!/usr/bin/env bash\nexport UT_LAN_DISCOVERY=0\nexport UT_LISTEN_ADDR=127.0.0.1:8091\n' >"${WORK}/good/run-till.sh"
# The literal ${...} is the point: it's file content, not an expansion.
# shellcheck disable=SC2016
printf '#!/usr/bin/env bash\n: "${UT_LAN_DISCOVERY:=0}"; export UT_LAN_DISCOVERY\nexport UT_LISTEN_ADDR=127.0.0.1:8092\n' >"${WORK}/override/run-till-x.sh"
printf '#!/usr/bin/env bash\necho no till here\n' >"${WORK}/other/helper.sh"

printf "const env = { ...process.env, UT_LISTEN_ADDR: '127.0.0.1:1' };\n" >"${WORK}/badts/worker.ts"
printf "const env = { UT_LAN_DISCOVERY: process.env.UT_LAN_DISCOVERY ?? '0', UT_LISTEN_ADDR: 'x' };\n" >"${WORK}/goodts/worker.ts"
printf '#!/usr/bin/env bash\nexport UT_LAN_DISCOVERY=false UT_LISTEN_ADDR=127.0.0.1:1\n' >"${WORK}/false/run.sh"
printf '#!/usr/bin/env bash\n# TODO: set UT_LAN_DISCOVERY=0\nexport UT_LISTEN_ADDR=127.0.0.1:8091\n' >"${WORK}/comment/run-till.sh"
printf 'e2e:\n\tUT_LISTEN_ADDR=127.0.0.1:8080 go run .\n' >"${WORK}/make/Makefile"
expect fail "launcher mentioning UT_LAN_DISCOVERY only in a comment" "${WORK}/comment"
expect fail "Makefile target booting a till without UT_LAN_DISCOVERY" "${WORK}/make"
expect fail "TS spawner without UT_LAN_DISCOVERY" "${WORK}/badts"
expect pass "TS spawner defaulting UT_LAN_DISCOVERY to '0'" "${WORK}/goodts"
expect pass "launcher using UT_LAN_DISCOVERY=false" "${WORK}/false"
expect fail "launcher without UT_LAN_DISCOVERY" "${WORK}/bad"
expect pass "launcher exporting UT_LAN_DISCOVERY=0" "${WORK}/good"
expect pass "launcher defaulting UT_LAN_DISCOVERY to 0" "${WORK}/override"
expect pass "script that starts no till" "${WORK}/other"
expect pass "the real e2e/ launchers" "${ROOT_DIR}/e2e"
if bash "${GUARD}" >/dev/null 2>&1; then echo "ok: default run (e2e/ + Makefile)"; else echo "FAIL: default run (e2e/ + Makefile)"; FAIL_COUNT=$((FAIL_COUNT + 1)); fi

if [[ "${FAIL_COUNT}" -gt 0 ]]; then
  echo "${FAIL_COUNT} case(s) failed"
  exit 1
fi
echo "guard-e2e-no-lan-discovery: all cases passed"
