#!/usr/bin/env bash
#
# Regression test for guard-catimport-module.sh and check-catimport-pin.sh
# (ADR-0164 §1, ut-docs#4525). Builds fixture trees and git repos in a
# mktemp dir (never the real tree) and asserts each guard passes a clean
# shape and fails every drift it exists to catch; then runs the module
# guard on the real tree.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-catimport-module.sh"
PIN="${ROOT_DIR}/scripts/ci/check-catimport-pin.sh"
TMP="$(mktemp -d)"
# Invoked via trap, not a direct call (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() { rm -rf "${TMP}"; }
trap cleanup EXIT
FAIL_COUNT=0
MOD="github.com/universaltill/universal-till/catimport"

check() { # check pass|fail <label> <cmd...>
  local want="$1" label="$2" got=pass
  shift 2
  "$@" >"${TMP}/out" 2>&1 || got=fail
  if [[ "${got}" != "${want}" ]]; then
    echo "FAIL: ${label}: ${got}ed, want ${want}"
    cat "${TMP}/out"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "ok: ${label}"
  fi
}

# fixture <dir>: a clean tree the module guard accepts.
fixture() {
  local d="$1"
  rm -rf "${d}"
  mkdir -p "${d}/catimport/barcode" "${d}/internal/catimport"
  printf 'module %s\n\ngo 1.25.0\n\nrequire github.com/xuri/excelize/v2 v2.11.0\n' "${MOD}" >"${d}/catimport/go.mod"
  printf 'package catimport\n\nimport (\n\t"strings"\n\n\t"%s/barcode"\n)\n\nfunc DetectFormat(h []string) string { _ = strings.ToLower; _ = barcode.X; return "" }\n' "${MOD}" >"${d}/catimport/catimport.go"
  printf 'package barcode\n\nvar X = 1\n' >"${d}/catimport/barcode/barcode.go"
  printf 'module github.com/universaltill/universal-till\n\nrequire %s v0.0.0\n\nreplace %s => ./catimport\n' "${MOD}" "${MOD}" >"${d}/go.mod"
  printf 'package catimport\n\nimport shared "%s"\n\nvar _ = shared.DetectFormat\n' "${MOD}" >"${d}/internal/catimport/bkp.go"
  # A till test may build an xlsx fixture with excelize.
  printf 'package pages\n\nimport "github.com/xuri/excelize/v2"\n\nvar _ = excelize.NewFile\n' >"${d}/internal/catimport/x_test.go"
}

T="${TMP}/tree"
fixture "${T}"
check pass "clean module layout" bash "${GUARD}" "${T}"

fixture "${T}"; rm "${T}/catimport/go.mod"
check fail "no catimport/go.mod" bash "${GUARD}" "${T}"

fixture "${T}"; printf 'module example.com/other\n' >"${T}/catimport/go.mod"
check fail "wrong module path" bash "${GUARD}" "${T}"

fixture "${T}"; printf 'require github.com/universaltill/universal-till v0.0.0\n' >>"${T}/catimport/go.mod"
check fail "module requires the till's root module" bash "${GUARD}" "${T}"

fixture "${T}"; printf 'package catimport\n\nimport "github.com/universaltill/universal-till/internal/money"\n\nvar _ = money.X\n' >"${T}/catimport/leak.go"
check fail "module imports a till internal package" bash "${GUARD}" "${T}"
grep -q 'leak.go' "${TMP}/out" || { echo "FAIL: import message does not name leak.go"; FAIL_COUNT=$((FAIL_COUNT + 1)); }

fixture "${T}"; printf 'package barcode_test\n\nimport "github.com/universaltill/universal-till/internal/data"\n' >"${T}/catimport/barcode/leak_test.go"
check fail "module test imports a till package" bash "${GUARD}" "${T}"

fixture "${T}"; printf 'module github.com/universaltill/universal-till\n\nrequire %s v0.1.0\n' "${MOD}" >"${T}/go.mod"
check fail "till pins a tag instead of the in-tree replace" bash "${GUARD}" "${T}"

fixture "${T}"; printf 'package catimport\n\nimport "github.com/xuri/excelize/v2"\n\nvar _ = excelize.NewFile\n' >"${T}/internal/catimport/xlsx.go"
check fail "xlsx parsing back under internal/" bash "${GUARD}" "${T}"

fixture "${T}"; printf 'package catimport\n\nfunc DetectFormat(h []string) string { return "" }\n' >"${T}/internal/catimport/detect.go"
check fail "format detection back under internal/" bash "${GUARD}" "${T}"

# check-catimport-pin.sh against a throwaway git repo.
R="${TMP}/repo"
mkdir -p "${R}/catimport" "${R}/internal"
git -C "${R}" init -q
git -C "${R}" config user.email test@example.com
git -C "${R}" config user.name test
printf 'package catimport\n' >"${R}/catimport/a.go"
printf 'package x\n' >"${R}/internal/x.go"
git -C "${R}" add -A
git -C "${R}" commit -qm one
git -C "${R}" tag catimport/v0.1.0

check pass "pin matches the tree" bash "${PIN}" v0.1.0 "${R}"
check fail "pseudo-version refused" bash "${PIN}" v0.0.0-20261016000000-abcdefabcdef "${R}"
check fail "missing tag" bash "${PIN}" v0.2.0 "${R}"

printf 'package x\n\nvar Y = 1\n' >"${R}/internal/x.go"
git -C "${R}" commit -qam "outside the module"
check pass "a change outside catimport/ needs no bump" bash "${PIN}" v0.1.0 "${R}"

printf '# docs\n' >"${R}/catimport/README.md"
git -C "${R}" add -A
git -C "${R}" commit -qm "module docs only"
check pass "a README-only change needs no bump" bash "${PIN}" v0.1.0 "${R}"

mkdir -p "${R}/catimport/barcode"
printf 'package barcode\n' >"${R}/catimport/barcode/b.go"
git -C "${R}" add -A
git -C "${R}" commit -qm "new subpackage"
check fail "a new Go file in a subpackage since the pinned tag" bash "${PIN}" v0.1.0 "${R}"
git -C "${R}" reset -q --hard HEAD~1

printf 'package catimport\n\nvar Z = 1\n' >"${R}/catimport/a.go"
git -C "${R}" commit -qam "inside the module"
check fail "catimport/ changed since the pinned tag" bash "${PIN}" v0.1.0 "${R}"
grep -q 'catimport/a.go' "${TMP}/out" || { echo "FAIL: drift message does not name catimport/a.go"; FAIL_COUNT=$((FAIL_COUNT + 1)); }

git -C "${R}" tag catimport/v0.1.1
check pass "bumped pin matches again" bash "${PIN}" v0.1.1 "${R}"

check pass "real tree" bash "${GUARD}"

if [[ "${FAIL_COUNT}" -ne 0 ]]; then
  echo "${FAIL_COUNT} check(s) failed"
  exit 1
fi
echo "all catimport guard checks passed"
