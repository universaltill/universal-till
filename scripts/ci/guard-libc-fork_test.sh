#!/usr/bin/env bash
#
# Regression test for guard-libc-fork.sh (ut-docs#6300, ADR-0188): the libc
# fork pin must survive a require bump, a dropped replace, a replace to another
# path, a require/replace version mismatch and a missing go.sum line.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-libc-fork.sh"
TMP="$(mktemp -d -t guard-libc-fork-XXXXXX)"
MOD="${TMP}/go.mod"
SUM="${TMP}/go.sum"
OUT="${TMP}/out"
trap 'rm -rf "${TMP}"' EXIT
FAIL_COUNT=0

good() {
  cat > "${MOD}" <<'M'
module example.com/x

go 1.27

require modernc.org/libc v1.75.6 // indirect

replace modernc.org/libc => github.com/universaltill/modernc-libc v1.75.6-ut.1
M
  cat > "${SUM}" <<'S'
github.com/universaltill/modernc-libc v1.75.6-ut.1 h1:abc=
github.com/universaltill/modernc-libc v1.75.6-ut.1/go.mod h1:def=
S
}

assert_passes() {
  if bash "${GUARD}" "${MOD}" "${SUM}" >"${OUT}" 2>&1; then
    echo "PASS (correctly accepted): $1"
  else
    echo "FAIL (should have been accepted but was rejected): $1"; cat "${OUT}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}
assert_fails() {
  if bash "${GUARD}" "${MOD}" "${SUM}" >"${OUT}" 2>&1; then
    echo "FAIL (should have been rejected but was accepted): $1"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  elif ! grep -q "ADR-0188" "${OUT}"; then
    echo "FAIL (rejected, but the message does not point at ADR-0188): $1"; cat "${OUT}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "PASS (correctly rejected): $1"
  fi
}

good; assert_passes "require and replace agree, go.sum has both lines"
if bash "${GUARD}" >"${OUT}" 2>&1; then
  echo "PASS (correctly accepted): the real go.mod + go.sum"
else
  echo "FAIL (should have been accepted but was rejected): the real tree"; cat "${OUT}"
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

good; sed -i.bak 's/^require modernc.org\/libc v1.75.6/require modernc.org\/libc v1.76.0/' "${MOD}"
assert_fails "sqlite bump raised the libc require, replace left behind"

good; sed -i.bak '/^replace/d' "${MOD}"
assert_fails "replace dropped"

good; sed -i.bak 's#github.com/universaltill/modernc-libc#github.com/someone/else-libc#' "${MOD}"
assert_fails "replace points at another path"

good; sed -i.bak 's#v1.75.6-ut.1#v1.76.0-ut.1#' "${MOD}"
assert_fails "replace version is for a different libc"

# The require version is matched literally, not as a regex ('.' is not "any").
good; sed -i.bak 's#=> github.com/universaltill/modernc-libc v1.75.6-ut.1#=> github.com/universaltill/modernc-libc v1x75x6-ut.1#' "${MOD}"
sed -i.bak 's#modernc-libc v1.75.6-ut.1#modernc-libc v1x75x6-ut.1#' "${SUM}"
assert_fails "replace version only matches the require as a regex"

good; sed -i.bak 's#v1.75.6-ut.1#v1.75.6#' "${MOD}"
assert_fails "replace to the fork at the plain upstream version (no -ut.N)"

good; echo 'replace modernc.org/libc => ./local/libc' >> "${MOD}"
assert_fails "a second replace for libc"

good; sed -i.bak '/\/go.mod h1:/d' "${SUM}"
assert_fails "go.sum lacks the fork /go.mod line"

good; sed -i.bak '/ut.1 h1:/d' "${SUM}"
assert_fails "go.sum lacks the fork module hash"

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "guard-libc-fork_test: ${FAIL_COUNT} failure(s)"
  exit 1
fi
echo "guard-libc-fork_test: all cases passed"
