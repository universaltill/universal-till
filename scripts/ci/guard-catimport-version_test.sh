#!/usr/bin/env bash
#
# Regression test for guard-catimport-version.sh (ut-docs#5882). Runs in a
# throwaway copy of catimport/ + scripts/ci (guard-test-sandbox.sh, #4311):
# each case builds a commit on top of a base that carries tag
# catimport/v0.4.0 and asserts the exit code and a message fragment.
set -euo pipefail

# shellcheck source=scripts/ci/guard-test-sandbox.sh
source "$(dirname "${BASH_SOURCE[0]}")/guard-test-sandbox.sh"
enter_guard_test_sandbox catimport

# Invoked via trap, not a direct call (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() {
  if [[ -n "${CLONE_DIR:-}" && -d "${CLONE_DIR}" ]]; then rm -rf "${CLONE_DIR}"; fi
  remove_guard_test_sandbox
}
trap cleanup EXIT
CLONE_DIR=""
FAIL_COUNT=0
GUARD="scripts/ci/guard-catimport-version.sh"
G=(git -c user.name=t -c user.email=t@example.invalid)

printf '0.4.0\n' >catimport/VERSION
mkdir -p catimport/barcode
[[ -f catimport/go.sum ]] || : >catimport/go.sum
printf 'package barcode\n\nvar X = 1\n' >catimport/barcode/b.go
git add -A -f
"${G[@]}" commit -qm base
git branch -M main
git tag -a catimport/v0.4.0 -m x
BASE="$(git rev-parse HEAD)"

# scenario <label>: reset to the base on a fresh branch; the caller edits
# files and calls `land` to make the PR's single commit.
scenario() { git checkout -q -f -B work "${BASE}"; git clean -fdq catimport; }
land() { git add -A -f; "${G[@]}" commit -qm "$1"; }

# check pass|fail <label> <message-fragment> [guard args...]
check() {
  local want="$1" label="$2" frag="$3" got=pass out
  shift 3
  out="$(bash "${GUARD}" "$@" 2>&1)" || got=fail
  if [[ "${got}" != "${want}" ]]; then
    echo "FAIL: ${label}: ${got}ed, want ${want}"
    echo "${out}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  elif [[ "${out}" != *"${frag}"* ]]; then
    echo "FAIL: ${label}: output lacks '${frag}'"
    echo "${out}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "ok: ${label}"
  fi
}

scenario; printf 'package barcode\n\nvar X = 2\n' >catimport/barcode/b.go; land go
check fail "a .go change, VERSION unchanged" "catimport/barcode/b.go" 

scenario; printf 'package barcode\n\nvar X = 2\n' >catimport/barcode/b.go; printf '0.4.1\n' >catimport/VERSION; land go-bump
check pass "a .go change, VERSION bumped above the tag" "0.4.1"

scenario; printf '# docs\n' >catimport/README.md; land readme
check pass "README only needs no bump" "0.4.0"

scenario; printf 'module x\n' >catimport/go.mod; land gomod
check fail "go.mod only, no bump" "catimport/go.mod"

scenario; printf 'h1:x\n' >catimport/go.sum; land gosum
check fail "go.sum only, no bump" "catimport/go.sum"

scenario; printf 'package barcode\n\nvar X = 3\n' >catimport/barcode/b.go; printf '0.3.9\n' >catimport/VERSION; land lower
check fail "source change with VERSION below the tag" "below"

scenario; printf 'unrelated\n' >notes.txt; land outside
check pass "no catimport/ change at all" "0.4.0"

for bad in 'v0.5.0' '0.5' '0.5.0-rc1' '' '0.5.0
0.5.1' '0.5.0

' ' 0.5.0' '01.5.0'; do
  scenario
  printf 'package barcode\n\nvar X = 4\n' >catimport/barcode/b.go
  printf '%s' "${bad}" >catimport/VERSION
  land malformed
  check fail "malformed VERSION '${bad//$'\n'/\\n}'" "catimport/VERSION"
done

scenario; printf '0.5.0\r\n' >catimport/VERSION; land crlf
check fail "CRLF VERSION" "catimport/VERSION"

scenario; printf '0.4.1' >catimport/VERSION; land nonl
check pass "no trailing newline is accepted" "0.4.1"

scenario; git rm -q catimport/VERSION; land missing
check fail "VERSION missing, even with no source change" "catimport/VERSION"

scenario; printf '0.5\n' >catimport/VERSION; land malformed-nosrc
check fail "malformed VERSION with no source change" "catimport/VERSION"

scenario; printf '0.3.0\n' >catimport/VERSION; land below-nosrc
check fail "VERSION below the tag with no source change" "below"

scenario; printf '0.4.1\n' >catimport/VERSION; land bump-only
check pass "bump-only PR above the tag" "0.4.1"

# Bootstrap: base has no VERSION at all; the PR adds 0.5.0 over tag 0.4.0.
scenario; git rm -q catimport/VERSION; "${G[@]}" commit -qm "base without VERSION"
BOOT="$(git rev-parse HEAD)"
printf '0.5.0\n' >catimport/VERSION; printf 'package barcode\n\nvar X = 5\n' >catimport/barcode/b.go; land bootstrap
check pass "bootstrap: base lacks VERSION, PR adds 0.5.0" "0.5.0"
git checkout -q -f -B work "${BOOT}"; printf '0.4.0\n' >catimport/VERSION; printf 'package barcode\n\nvar X = 6\n' >catimport/barcode/b.go; land bootstrap-equal
check fail "bootstrap: PR adds VERSION equal to the tag" "above"

# Untagged bump already on the base (a failed tag run): a second source PR
# must not reuse that number.
scenario; printf '0.5.0\n' >catimport/VERSION; printf 'package barcode\n\nvar X = 7\n' >catimport/barcode/b.go; land untagged-bump
UNTAGGED="$(git rev-parse HEAD)"
printf 'package barcode\n\nvar X = 8\n' >catimport/barcode/b.go; land second
check fail "source change under an untagged bump on the base" "0.5.0"
printf '0.5.1\n' >catimport/VERSION; printf 'package barcode\n\nvar X = 9\n' >catimport/barcode/b.go
git add -A -f; "${G[@]}" commit -qm third --amend
check pass "second source PR bumps past the untagged base" "0.5.1"
: "${UNTAGGED}"

# A non-semver tag is ignored, and does not mask a real one.
scenario; git tag catimport/vfoo; printf 'package barcode\n\nvar X = 10\n' >catimport/barcode/b.go; land go-foo
check fail "non-semver tag ignored, VERSION unchanged" "above 0.4.0"
git tag -d catimport/vfoo >/dev/null

# Explicit base argument.
scenario; printf 'package barcode\n\nvar X = 11\n' >catimport/barcode/b.go; land go-explicit
check fail "explicit base-rev argument" "catimport/barcode/b.go" "${BASE}" .

# Fail closed: shallow checkout (parent missing).
CLONE_DIR="$(mktemp -d)"
scenario; printf 'package barcode\n\nvar X = 12\n' >catimport/barcode/b.go; printf '0.4.1\n' >catimport/VERSION; land for-shallow
git clone -q --depth 1 "file://${PWD}" "${CLONE_DIR}/shallow"
git -C "${CLONE_DIR}/shallow" fetch -q --no-tags origin 'refs/tags/catimport/*:refs/tags/catimport/*'
got=pass; out="$(cd "${CLONE_DIR}/shallow" && bash scripts/ci/guard-catimport-version.sh 2>&1)" || got=fail
if [[ "${got}" == fail && "${out}" == *"parent"* ]]; then echo "ok: shallow checkout fails closed"; else echo "FAIL: shallow checkout: ${got}: ${out}"; FAIL_COUNT=$((FAIL_COUNT + 1)); fi

# Fail closed: no catimport/v* tags at all.
scenario; printf '# d\n' >catimport/README.md; land tagless-commit
git tag -d catimport/v0.4.0 >/dev/null
check fail "tagless checkout fails closed" "tags"
git tag -a catimport/v0.4.0 -m x "${BASE}"

# The two other scripts share the guard's pathspec (single definition).
for f in check-catimport-pin.sh catimport-tag.sh guard-catimport-version.sh; do
  if grep -q 'catimport-version-lib.sh' "scripts/ci/${f}"; then
    echo "ok: ${f} sources the shared lib"
  else
    echo "FAIL: ${f} does not source catimport-version-lib.sh"; FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
done

if [[ "${FAIL_COUNT}" -ne 0 ]]; then
  echo "${FAIL_COUNT} check(s) failed"
  exit 1
fi
echo "all catimport version guard checks passed"
