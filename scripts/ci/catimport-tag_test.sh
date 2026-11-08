#!/usr/bin/env bash
#
# Regression test for catimport-tag.sh (ut-docs#5882), every row of its
# decision table. Runs in a throwaway copy of catimport/ + scripts/ci
# (guard-test-sandbox.sh, #4311); `origin` is a local bare repo cloned from
# it afresh per case, and the script runs in a full clone of that origin.
set -euo pipefail

# shellcheck source=scripts/ci/guard-test-sandbox.sh
source "$(dirname "${BASH_SOURCE[0]}")/guard-test-sandbox.sh"
enter_guard_test_sandbox catimport

TMPD="$(mktemp -d)"
# Invoked via trap, not a direct call (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() {
  if [[ -n "${TMPD:-}" && -d "${TMPD}" ]]; then rm -rf "${TMPD}"; fi
  remove_guard_test_sandbox
}
trap cleanup EXIT
FAIL_COUNT=0
G=(git -c user.name=t -c user.email=t@example.invalid)
N=0
O="" # current origin (bare)
W="" # current work clone

printf '0.4.0\n' >catimport/VERSION
mkdir -p catimport/barcode
printf 'package barcode\n\nvar X = 1\n' >catimport/barcode/b.go
git add -A -f
"${G[@]}" commit -qm base
git branch -M main
git tag -a catimport/v0.4.0 -m x

# fresh: a new bare origin (main + tags) and a full clone of it in $W.
fresh() {
  N=$((N + 1))
  O="${TMPD}/origin${N}.git"
  W="${TMPD}/work${N}"
  git clone -q --bare "${PWD}" "${O}"
  git clone -q "file://${O}" "${W}"
  git -C "${W}" config user.name t
  git -C "${W}" config user.email t@example.invalid
}
# land <msg>: commit everything in $W and push it to origin main.
land() { git -C "${W}" add -A -f; git -C "${W}" commit -qm "$1"; git -C "${W}" push -q origin HEAD:main; }
tag_sha() { git -C "${O}" rev-parse -q --verify "refs/tags/$1^{commit}" 2>/dev/null || echo none; }

# run_tag pass|fail <label> <fragment> [workdir]
OUT=""
run_tag() {
  local want="$1" label="$2" frag="$3" dir="${4:-${W}}" got=pass
  OUT="$(cd "${dir}" && bash scripts/ci/catimport-tag.sh 2>&1)" || got=fail
  if [[ "${got}" != "${want}" ]]; then
    echo "FAIL: ${label}: ${got}ed, want ${want}"; echo "${OUT}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  elif [[ "${OUT}" != *"${frag}"* ]]; then
    echo "FAIL: ${label}: output lacks '${frag}'"; echo "${OUT}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "ok: ${label}"
  fi
}
expect() { # expect <label> <got> <want>
  if [[ "$2" == "$3" ]]; then echo "ok: $1"; else echo "FAIL: $1: got '$2', want '$3'"; FAIL_COUNT=$((FAIL_COUNT + 1)); fi
}

# V > L: an annotated tag on HEAD, on the origin.
fresh; printf '0.5.0\n' >"${W}/catimport/VERSION"; printf 'package barcode\n\nvar X = 2\n' >"${W}/catimport/barcode/b.go"; land bump
HEADSHA="$(git -C "${W}" rev-parse HEAD)"
SUMMARY="${TMPD}/summary"
GITHUB_STEP_SUMMARY="${SUMMARY}" run_tag pass "V > L creates the tag" "catimport/v0.5.0"
expect "tag object is annotated on the origin" "$(git -C "${O}" cat-file -t refs/tags/catimport/v0.5.0)" tag
expect "tag points at HEAD" "$(tag_sha catimport/v0.5.0)" "${HEADSHA}"
expect "tag message names the version" "$(git -C "${O}" tag -l --format='%(contents:subject)' catimport/v0.5.0)" "catimport v0.5.0"
expect "tagger is the Actions bot" "$(git -C "${O}" tag -l --format='%(taggername)' catimport/v0.5.0)" "github-actions[bot]"
if [[ -s "${SUMMARY}" ]] && grep -q 'catimport/v0.5.0' "${SUMMARY}"; then echo "ok: step summary written"; else echo "FAIL: step summary lacks the tag"; FAIL_COUNT=$((FAIL_COUNT + 1)); fi
run_tag pass "re-run on the same commit is a no-op" "already"
expect "re-run leaves the tag" "$(tag_sha catimport/v0.5.0)" "${HEADSHA}"

# V == L, HEAD is the tagged commit.
fresh
run_tag pass "V == L at the tagged commit is a no-op" "already"

# V == L, README-only change since the tag.
fresh; printf '# d\n' >"${W}/catimport/README.md"; land readme
run_tag pass "V == L, README only since the tag, is a no-op" "no catimport source change"
expect "no new tag" "$(git -C "${O}" tag -l 'catimport/*' | tr '\n' ' ')" "catimport/v0.4.0 "

# V == L, source changed since the tag.
fresh; BEFORE="$(tag_sha catimport/v0.4.0)"; printf 'package barcode\n\nvar X = 3\n' >"${W}/catimport/barcode/b.go"; land src
run_tag fail "V == L with source changed is red" "VERSION was not bumped"
expect "existing tag untouched" "$(tag_sha catimport/v0.4.0)" "${BEFORE}"

# V < L.
fresh; printf '0.3.0\n' >"${W}/catimport/VERSION"; land lower
run_tag fail "V < L is red" "below"

# Missing / malformed VERSION.
fresh; git -C "${W}" rm -q catimport/VERSION; land missing
run_tag fail "missing VERSION is red" "catimport/VERSION"
fresh; printf 'v0.5.0\n' >"${W}/catimport/VERSION"; land malformed
run_tag fail "malformed VERSION is red" "catimport/VERSION"
expect "nothing tagged on a malformed VERSION" "$(tag_sha catimport/v0.5.0)" none

# HEAD not on origin/main (committed locally, never pushed).
fresh; printf '0.5.0\n' >"${W}/catimport/VERSION"; git -C "${W}" add -A -f; git -C "${W}" commit -qm unpushed
run_tag fail "HEAD not an ancestor of origin/main is red" "origin/main"
expect "nothing tagged off main" "$(tag_sha catimport/v0.5.0)" none

# Shallow clone.
fresh; printf '0.5.0\n' >"${W}/catimport/VERSION"; land bump
git clone -q --depth 1 "file://${O}" "${TMPD}/shallow${N}"
run_tag fail "shallow checkout is red" "shallow" "${TMPD}/shallow${N}"
expect "nothing tagged from a shallow checkout" "$(tag_sha catimport/v0.5.0)" none

# No tags at all in the checkout.
fresh; printf '0.5.0\n' >"${W}/catimport/VERSION"; land bump
git -C "${W}" tag -d catimport/v0.4.0 >/dev/null
run_tag fail "tagless checkout is red" "tags"

# Push rejected: another commit already holds the tag on the origin (made
# after this checkout, so the work clone cannot see it).
fresh; printf '0.5.0\n' >"${W}/catimport/VERSION"; land bump
OTHER="$(git -C "${O}" rev-parse refs/tags/catimport/v0.4.0^{})"
git -C "${O}" tag -a catimport/v0.5.0 -m other "${OTHER}"
run_tag fail "tag held by another commit is red" "catimport/v0.5.0"
expect "the other commit's tag is never moved" "$(tag_sha catimport/v0.5.0)" "${OTHER}"

# Push rejected because a concurrent run already tagged this very commit.
fresh; printf '0.5.0\n' >"${W}/catimport/VERSION"; land bump
git -C "${O}" tag -a catimport/v0.5.0 -m racer "$(git -C "${W}" rev-parse HEAD)"
run_tag pass "a concurrent run that tagged HEAD is green" "already"

if [[ "${FAIL_COUNT}" -ne 0 ]]; then
  echo "${FAIL_COUNT} check(s) failed"
  exit 1
fi
echo "all catimport tag checks passed"
