#!/usr/bin/env bash
#
# Regression test for guard-e2e-no-update-check.sh (ut-docs#5237): proves the
# guard rejects a till launcher that leaves the update check polling the real
# GitHub releases, accepts one that disables it or points it at a loopback
# fake, and ignores scripts that don't start a till at all.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-e2e-no-update-check.sh"
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

for d in comment make badts goodts false bad good override loopback localhost remote other; do
  mkdir -p "${WORK}/${d}"
done
printf '#!/usr/bin/env bash\nexport UT_DATA_DIR=x UT_LISTEN_ADDR=127.0.0.1:8091\n' >"${WORK}/bad/run-till.sh"
printf '#!/usr/bin/env bash\nexport UT_UPDATE_CHECK=0\nexport UT_LISTEN_ADDR=127.0.0.1:8091\n' >"${WORK}/good/run-till.sh"
# The literal ${...} is the point: it's file content, not an expansion.
# shellcheck disable=SC2016
printf '#!/usr/bin/env bash\n: "${UT_UPDATE_CHECK:=0}"; export UT_UPDATE_CHECK\nexport UT_LISTEN_ADDR=127.0.0.1:8092\n' >"${WORK}/override/run-till-x.sh"
# shellcheck disable=SC2016
printf '#!/usr/bin/env bash\nexport UT_UPDATE_RELEASES_URL="http://127.0.0.1:${FAKE_PORT}/latest"\nexport UT_LISTEN_ADDR=127.0.0.1:8093\n' >"${WORK}/loopback/run-till-n.sh"
printf '#!/usr/bin/env bash\nexport UT_UPDATE_RELEASES_URL=http://localhost:9000/latest\nexport UT_LISTEN_ADDR=127.0.0.1:8093\n' >"${WORK}/localhost/run-till-n.sh"
printf '#!/usr/bin/env bash\nexport UT_UPDATE_RELEASES_URL=https://api.github.com/repos/x/y/releases/latest\nexport UT_LISTEN_ADDR=127.0.0.1:8093\n' >"${WORK}/remote/run-till-n.sh"
printf '#!/usr/bin/env bash\necho no till here\n' >"${WORK}/other/helper.sh"

printf "const env = { ...process.env, UT_LISTEN_ADDR: '127.0.0.1:1' };\n" >"${WORK}/badts/worker.ts"
printf "const env = { UT_UPDATE_CHECK: process.env.UT_UPDATE_CHECK ?? '0', UT_LISTEN_ADDR: 'x' };\n" >"${WORK}/goodts/worker.ts"
printf '#!/usr/bin/env bash\nexport UT_UPDATE_CHECK=false UT_LISTEN_ADDR=127.0.0.1:1\n' >"${WORK}/false/run.sh"
printf '#!/usr/bin/env bash\n# TODO: set UT_UPDATE_CHECK=0\nexport UT_LISTEN_ADDR=127.0.0.1:8091\n' >"${WORK}/comment/run-till.sh"
printf 'e2e:\n\tUT_LISTEN_ADDR=127.0.0.1:8080 go run .\n' >"${WORK}/make/Makefile"
expect fail "launcher mentioning UT_UPDATE_CHECK only in a comment" "${WORK}/comment"
expect fail "Makefile target booting a till without UT_UPDATE_CHECK" "${WORK}/make"
expect fail "TS spawner without UT_UPDATE_CHECK" "${WORK}/badts"
expect pass "TS spawner defaulting UT_UPDATE_CHECK to '0'" "${WORK}/goodts"
expect pass "launcher using UT_UPDATE_CHECK=false" "${WORK}/false"
expect fail "launcher without UT_UPDATE_CHECK" "${WORK}/bad"
expect pass "launcher exporting UT_UPDATE_CHECK=0" "${WORK}/good"
expect pass "launcher defaulting UT_UPDATE_CHECK to 0" "${WORK}/override"
expect pass "launcher with a loopback (127.0.0.1) UT_UPDATE_RELEASES_URL" "${WORK}/loopback"
expect pass "launcher with a localhost UT_UPDATE_RELEASES_URL" "${WORK}/localhost"
expect fail "launcher with a non-loopback UT_UPDATE_RELEASES_URL and no UT_UPDATE_CHECK=0" "${WORK}/remote"
expect pass "script that starts no till" "${WORK}/other"
expect pass "the real e2e/ launchers" "${ROOT_DIR}/e2e"
if bash "${GUARD}" >/dev/null 2>&1; then echo "ok: default run (e2e/ + Makefile)"; else echo "FAIL: default run (e2e/ + Makefile)"; FAIL_COUNT=$((FAIL_COUNT + 1)); fi

if [[ "${FAIL_COUNT}" -gt 0 ]]; then
  echo "${FAIL_COUNT} case(s) failed"
  exit 1
fi
echo "guard-e2e-no-update-check: all cases passed"
