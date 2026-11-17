#!/usr/bin/env bash
# Seeded-violation tests for guard-android-play-channel.sh (review S7).
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"
GUARD="scripts/ci/guard-android-play-channel.sh"
WORK="$(mktemp -d -t guard-android-play-XXXXXX)"
trap 'rm -rf "${WORK:?}"' EXIT
mkdir -p "${WORK}/wf"
FAIL=0
expect() { # name want(0|1) gradle wfdir
  local rc=0
  bash "${GUARD}" "$3" "$4" >"${WORK}/out" 2>&1 || rc=$?
  if [ "${rc}" -ne "$2" ]; then echo "FAIL ${1}: exit ${rc}, want $2"; cat "${WORK}/out"; FAIL=1; else echo "ok ${1}"; fi
}
REAL=android/app/build.gradle.kts
expect "real tree passes" 0 "${REAL}" .github/workflows

grep -v 'taskGraph.whenReady' "${REAL}" > "${WORK}/nogate.kts"
expect "assertion removed" 1 "${WORK}/nogate.kts" "${WORK}/wf"

printf 'jobs:\n  b:\n    steps:\n      - run: ./gradlew bundlePlayRelease\n' > "${WORK}/wf/a.yml"
expect "Play task without the property" 1 "${REAL}" "${WORK}/wf"

printf 'jobs:\n  b:\n    steps:\n      - run: |\n          ./gradlew \\\n            -PinstallChannel=play \\\n            bundlePlayRelease\n' > "${WORK}/wf/a.yml"
expect "Play task with the property on a continuation line" 0 "${REAL}" "${WORK}/wf"

printf 'jobs:\n  b:\n    steps:\n      - run: ./gradlew -PinstallChannel=sideload bundlePlayRelease\n' > "${WORK}/wf/a.yml"
expect "Play task with the wrong channel" 1 "${REAL}" "${WORK}/wf"
exit "${FAIL}"
