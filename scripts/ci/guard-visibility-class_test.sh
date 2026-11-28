#!/usr/bin/env bash
#
# Regression test for guard-visibility-class.sh (ut-docs#6332 S3): proves it
# rejects a core row without a class, with an unknown class name and an empty
# table set, and passes on the real tree.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-visibility-class.sh"
FAIL_COUNT=0

WORK="$(mktemp -d)"
# shellcheck disable=SC2317
cleanup() { rm -rf -- "${WORK:?}"; }
trap cleanup EXIT

fixture() { # $1 = the row line inside CoreRail
  printf '%s\n' 'package uislot' 'var CoreRail = []Entry{' "$1" '}' >"${WORK}/slot.go"
}
expect_fail() {
  if bash "${GUARD}" "${WORK}/slot.go" >"${WORK}/out" 2>&1; then
    echo "❌ FAIL: expected the guard to reject $1, but it passed" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard rejected $1"
  fi
}
expect_pass() {
  if bash "${GUARD}" "$1" >"${WORK}/out" 2>&1; then
    echo "✓ guard passed $2"
  else
    echo "❌ FAIL: expected the guard to pass $2" >&2
    cat "${WORK}/out" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

fixture '	{Key: "/x", Href: "/x", LabelKey: "nav.x", Order: 100},'
expect_fail "a row with no Class"
fixture '	{Key: "/x", Class: ClassRare, Href: "/x", Order: 100},'
expect_fail "a row with an unknown class name"
fixture '	{Key: "/x", Class: "common", Href: "/x", Order: 100},'
expect_fail "a row with a string-literal class"
printf '%s\n' 'package uislot' >"${WORK}/slot.go"
expect_fail "a file with no core tables"
fixture '	{Key: "/x", Class: ClassCommon, Href: "/x", Order: 100},'
expect_pass "${WORK}/slot.go" "a classed row"
expect_pass "${ROOT_DIR}/internal/uislot/slot.go" "the real slot.go"

[ "${FAIL_COUNT}" -eq 0 ] || exit 1
