#!/usr/bin/env bash
#
# Regression test for guard-test-sandbox.sh (ut-docs#4311): the sandbox holds
# the requested paths plus scripts/ci and go.mod, is a git repo whose
# `git ls-files` lists them, takes git pathspecs ('internal/*.go'), keeps a
# write inside it out of the real tree, and is gone after
# remove_guard_test_sandbox.
set -euo pipefail

REAL_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/ci/guard-test-sandbox.sh
source "${REAL_ROOT}/scripts/ci/guard-test-sandbox.sh"
FAIL_COUNT=0

check() {
  local label="$1"
  shift
  if "$@"; then
    echo "ok:   ${label}"
  else
    echo "FAIL: ${label}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

# Invoked via trap, not a direct call (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() {
  local status=$?
  remove_guard_test_sandbox
  exit "${status}"
}
trap cleanup EXIT

enter_guard_test_sandbox internal/pages
box="$(pwd)"
check "cwd is a fresh dir outside the real tree" test "${box#"${REAL_ROOT}"}" = "${box}"
check "the requested dir is copied" test -f internal/pages/init.go
check "scripts/ci is always copied" test -f scripts/ci/guard-test-sandbox.sh
check "go.mod is always copied" test -f go.mod
check "an unrequested dir is not copied" test ! -e internal/data
listed="$(git ls-files -- internal/pages)"
check "git ls-files lists the copy" grep -qx internal/pages/init.go <<<"${listed}"
printf 'package pages\n' >internal/pages/zz_guard_test_sandbox_probe.go
check "a write in the sandbox never reaches the real tree" \
  test ! -e "${REAL_ROOT}/internal/pages/zz_guard_test_sandbox_probe.go"
remove_guard_test_sandbox
check "remove_guard_test_sandbox deletes the copy" test ! -e "${box}"
check "remove_guard_test_sandbox is safe to call twice" remove_guard_test_sandbox

enter_guard_test_sandbox 'internal/*.go'
check "a pathspec copies matching files from every subdir" test -f internal/data/plugin_repo.go
non_go="$(find internal -type f ! -name '*.go')"
check "a pathspec leaves non-matching files out" test -z "${non_go}"
remove_guard_test_sandbox

# A tracked file deleted in the working tree is skipped, even when it is the
# last path git lists (the copy loop's status is its last iteration's), and an
# EXIT before the caller's own trap still removes the copy. Run in a scratch
# repo so nothing real is deleted.
scratch="$(mktemp -d)"
mkdir -p "${scratch}/scripts/ci" "${scratch}/web"
cp "${REAL_ROOT}/scripts/ci/guard-test-sandbox.sh" "${scratch}/scripts/ci/"
printf 'a\n' >"${scratch}/web/a.txt"
printf 'z\n' >"${scratch}/web/z.txt"
git -C "${scratch}" init -q
git -C "${scratch}" add -A
rm "${scratch}/web/z.txt"
probe_out="$(cd "${scratch}" && bash -c 'set -euo pipefail
  source scripts/ci/guard-test-sandbox.sh
  enter_guard_test_sandbox web
  test -f web/a.txt && test ! -e web/z.txt && pwd' 2>&1 || echo "probe failed")"
check "a deleted last-listed tracked file is skipped, not fatal" \
  test "${probe_out}" != "probe failed"
check "the helper's own EXIT trap removes the copy" test ! -e "${probe_out}"
rm -rf "${scratch}"

# The sandbox repo never starts a background gc or maintenance that could
# still be writing .git/objects when the cleanup removes it (ut-docs#4941).
gc_out="$(bash -c 'set -euo pipefail
  source "$1"
  enter_guard_test_sandbox web/help/img
  printf "%s %s\n" "$(git config --local gc.auto)" "$(git config --local maintenance.auto)"' _ "${REAL_ROOT}/scripts/ci/guard-test-sandbox.sh" 2>&1 || echo "probe failed")"
check "the sandbox repo has auto-gc and auto-maintenance off" test "${gc_out}" = "0 false"

if [ "${FAIL_COUNT}" -gt 0 ]; then
  echo "${FAIL_COUNT} case(s) failed"
  exit 1
fi
echo "all cases passed"
