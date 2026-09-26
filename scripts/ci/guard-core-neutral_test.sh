#!/usr/bin/env bash
#
# Wrapper self-test for guard-core-neutral.sh (ut-docs#2888). The detector
# itself is unit-tested in scripts/ci/coreneutral/main_test.go; this proves
# the wrapper's plumbing: exit codes, the fixture args, and the shrink-only
# check against a base ref (CORE_NEUTRAL_BASE) resolved in the scanned repo.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-core-neutral.sh"
FAILS=0
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

REPO="${WORK}/repo"
LIST="${REPO}/scripts/ci/core-neutral-allowlist.txt"
mkdir -p "${REPO}/internal/x" "${REPO}/cmd/app" "${REPO}/scripts/ci"
printf 'package main\nfunc main() {}\n' >"${REPO}/cmd/app/main.go"
printf 'package x\nvar r = "/static/*"\nfunc h(cc string) bool { return cc == "DE" }\n' >"${REPO}/internal/x/x.go"
: >"${LIST}"

g() { git -C "${REPO}" -c user.name=t -c user.email=t@example.invalid -c core.hooksPath=/dev/null -c commit.gpgsign=false "$@"; }
g init -q
g add -A
g commit -qm base
g tag base

run() { CORE_NEUTRAL_BASE="$1" bash "${GUARD}" "${REPO}" "${LIST}" >"${WORK}/out" 2>&1; }
expect() {
  local want="$1" label="$2" base="$3" got=0
  run "${base}" || got=1
  if [ "${got}" = "${want}" ]; then
    echo "✓ ${label}"
  else
    echo "❌ FAIL: ${label} (exit ${got}, want ${want})" >&2
    cat "${WORK}/out" >&2
    FAILS=$((FAILS + 1))
  fi
}

expect 1 "offender after a \"/*\" string literal is caught" missing-ref
if ! grep -q 'base ref missing-ref not available' "${WORK}/out"; then
  echo "❌ FAIL: no shrink-check skip notice without a base ref" >&2
  FAILS=$((FAILS + 1))
fi

echo 'internal/x/x.go|h|"DE" # #2879 test entry' >"${LIST}"
expect 0 "allow-listed offender passes without a base ref" missing-ref
expect 1 "entry the base branch lacks fails (shrink-only)" base

g commit -qam grow
g tag -f base >/dev/null
expect 0 "entry present on the base branch passes" base

printf 'package x\nfunc h(cc string) bool { return cc != "" }\n' >"${REPO}/internal/x/x.go"
expect 1 "stale entry fails" base

if [ "${FAILS}" -ne 0 ]; then
  echo "❌ ${FAILS} guard-core-neutral wrapper case(s) failed" >&2
  exit 1
fi
echo "✓ guard-core-neutral wrapper self-test passed"
