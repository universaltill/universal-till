#!/usr/bin/env bash
#
# Regression test for guard-sdk-hostfns.sh (ut-docs#3951): runs the guard on
# fixture trees and asserts it fails on a host export with no SDK binding, an
# SDK binding with no host export, and an undocumented function, and passes
# on a consistent set and on the real tree.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-sdk-hostfns.sh"
TMP="$(mktemp -d)"
# Invoked via trap, not a direct call (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() { rm -rf "${TMP}"; }
trap cleanup EXIT
FAIL_COUNT=0

mkdir -p "${TMP}/host"
host() {
  printf 'package plugins\n\nfunc reg() {\n' >"${TMP}/host/hostfns.go"
  for fn in "$@"; do printf '\tb.NewFunctionBuilder().WithFunc(f).Export("%s").\n' "${fn}" >>"${TMP}/host/hostfns.go"; done
  printf '}\n' >>"${TMP}/host/hostfns.go"
  # A test file's exports never count.
  printf 'package plugins\nfunc t() { x.Export("test_only") }\n' >"${TMP}/host/x_test.go"
}
sdk() {
  printf '//go:build wasip1\n\npackage plugin\n' >"${TMP}/raw.go"
  for fn in "$@"; do printf '\n//go:wasmimport ut %s\nfunc ut_%s()\n' "${fn}" "${fn}" >>"${TMP}/raw.go"; done
}
# The backticks are literal markdown, not an expansion.
# shellcheck disable=SC2016
ref() { printf '# ref\n' >"${TMP}/ref.md"; for fn in "$@"; do printf -- '- `%s(ptr) i32`\n' "${fn}" >>"${TMP}/ref.md"; done; }

expect() { # expect pass|fail <label>
  local want="$1" label="$2" got=pass
  bash "${GUARD}" "${TMP}/host" "${TMP}" "${TMP}/ref.md" >"${TMP}/out" 2>&1 || got=fail
  if [[ "${got}" != "${want}" ]]; then
    echo "FAIL: ${label}: guard ${got}ed, want ${want}"; cat "${TMP}/out"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "ok: ${label}"
  fi
}

host log_write storage_get; sdk log_write storage_get; ref log_write storage_get
expect pass "consistent set"

host log_write storage_get new_fn; sdk log_write storage_get; ref log_write storage_get new_fn
expect fail "host export without SDK binding"
grep -q 'new_fn' "${TMP}/out" || { echo "FAIL: missing-binding message does not name new_fn"; FAIL_COUNT=$((FAIL_COUNT + 1)); }

host log_write; sdk log_write gone_fn; ref log_write gone_fn
expect fail "SDK binding without host export"

host log_write storage_get; sdk log_write storage_get; ref log_write
expect fail "host function missing from the reference"

# The name must appear as a whole word: storage_get_v2 does not document storage_get.
host storage_get; sdk storage_get; ref storage_get_v2
expect fail "reference only has a longer name"

# A non-literal or wrapped Export( argument is invisible to the name grep: fail loudly.
host log_write; sdk log_write; ref log_write
printf 'package plugins\nfunc more() {\n\tb.Export(\n\t\t"hidden_fn")\n}\n' >"${TMP}/host/more.go"
expect fail "wrapped Export( argument"
rm -f "${TMP}/host/more.go"

# A binding in a second SDK file still counts (the SDK dir is the unit, not one file).
host log_write; sdk log_write; ref log_write ghost_fn
printf '//go:build wasip1\n\npackage plugin\n\n//go:wasmimport ut ghost_fn\nfunc g()\n' >"${TMP}/raw_more.go"
expect fail "binding in another SDK file with no host export"
rm -f "${TMP}/raw_more.go"

if ! DOCS_DIR="${TMP}/nowhere" bash "${GUARD}" >/dev/null 2>&1; then
  echo "ok: DOCS_DIR set but missing fails"
else
  echo "FAIL: DOCS_DIR pointing at nothing passed"; FAIL_COUNT=$((FAIL_COUNT + 1))
fi

if bash "${GUARD}" >"${TMP}/out" 2>&1; then
  echo "ok: real tree"
else
  echo "FAIL: guard fails on the real tree"; cat "${TMP}/out"; FAIL_COUNT=$((FAIL_COUNT + 1))
fi

if [[ "${FAIL_COUNT}" -ne 0 ]]; then
  echo "guard-sdk-hostfns_test: ${FAIL_COUNT} failure(s)"; exit 1
fi
echo "guard-sdk-hostfns_test: all passed"
