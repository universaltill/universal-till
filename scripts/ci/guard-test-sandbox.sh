#!/usr/bin/env bash
#
# Sourced by guard regression tests that plant fixtures or rewrite files
# (ut-docs#4311, generalising i18n-guard-sandbox.sh from ut-docs#4258). Doing
# that in the REAL tree races with anything else reading it: a concurrent
# `go test ./...` compiling a planted internal/pages/*.go, or a tracked file
# caught mid-rewrite. Each test instead runs inside a throwaway copy of the
# paths its guard reads, plus scripts/ci and the root go.mod/go.sum/.gitignore.
# Guards derive their root from their own location, so the copy only ever
# sees the copy.
#
# The copy is the tracked plus untracked-but-not-ignored files under those
# paths (what `git ls-files --cached --others --exclude-standard` lists, so
# build output and node_modules stay out), and it is itself a git repo with
# those files staged: a guard that lists files with `git ls-files` sees the
# same set it would in the real tree.
#
# Usage (from a test, after `set -euo pipefail`):
#   source "$(dirname "${BASH_SOURCE[0]}")/guard-test-sandbox.sh"
#   enter_guard_test_sandbox internal/pages web/ui   # copies, then cd's in
#   trap ... EXIT                   # cleanup must call remove_guard_test_sandbox
# Pass `.` for the whole tree. scripts/ci/guard-ci-tests-sandboxed.sh
# enforces this for every test that names a real-tree fixture path.

GUARD_TEST_SANDBOX=""

enter_guard_test_sandbox() {
  local real_root
  real_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
  GUARD_TEST_SANDBOX="$(mktemp -d)"
  # Until the caller installs its own EXIT trap (which must also call
  # remove_guard_test_sandbox), a failure or kill must not orphan the copy.
  trap remove_guard_test_sandbox EXIT
  trap 'exit 130' INT TERM
  # A failed copy removes the half-built sandbox itself. Auto-gc and
  # auto-maintenance are off in the sandbox repo: a test's own `git commit`
  # could otherwise start a detached gc that is still writing .git/objects
  # when the cleanup's rm -rf runs, failing the job (ut-docs#4941).
  if ! (
    cd "${real_root}" || exit 1
    git ls-files -z --cached --others --exclude-standard -- \
      scripts/ci go.mod go.sum .gitignore "$@" |
      while IFS= read -r -d '' f; do
        # --cached still lists a tracked file deleted in the working tree.
        if [[ -f "${f}" ]]; then printf '%s\0' "${f}"; fi
      done |
      tar --null -T - -cf -
  ) | tar -xf - -C "${GUARD_TEST_SANDBOX}" ||
    ! git -C "${GUARD_TEST_SANDBOX}" init -q ||
    ! git -C "${GUARD_TEST_SANDBOX}" config gc.auto 0 ||
    ! git -C "${GUARD_TEST_SANDBOX}" config maintenance.auto false ||
    ! git -C "${GUARD_TEST_SANDBOX}" add -A -f; then
    echo "guard-test-sandbox: could not build the sandbox copy for: $*" >&2
    remove_guard_test_sandbox
    return 1
  fi
  cd "${GUARD_TEST_SANDBOX}" || return 1
}

# Removes only the mktemp dir created above. Safe to call twice or when
# enter_guard_test_sandbox never ran.
remove_guard_test_sandbox() {
  if [[ -n "${GUARD_TEST_SANDBOX:-}" && -d "${GUARD_TEST_SANDBOX}" ]]; then
    cd /
    rm -rf "${GUARD_TEST_SANDBOX}"
  fi
  GUARD_TEST_SANDBOX=""
}
