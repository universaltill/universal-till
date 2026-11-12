#!/usr/bin/env bash
#
# Behaviour + wiring test for latest-stable-tag.sh (ut-docs#1807): the latest
# release tag and the next bump ignore every pre-release / non-plain tag. Each
# case builds a throwaway repo in mktemp -d; nothing is written to the tree.
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT_DIR}/scripts/ci/latest-stable-tag.sh"

FAIL_COUNT=0
PASS_COUNT=0
fail() { echo "FAIL: $1" >&2; FAIL_COUNT=$((FAIL_COUNT + 1)); }
pass() { echo "ok   - $1"; PASS_COUNT=$((PASS_COUNT + 1)); }

if [ ! -f "${SCRIPT}" ]; then
  echo "FAIL: ${SCRIPT} does not exist" >&2
  exit 1
fi

export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

# mkrepo TAG... -> prints the path of a fresh repo carrying those tags
mkrepo() {
  local d t
  d="$(mktemp -d "${TMP}/repo.XXXXXX")"
  git init -q "$d"
  git -C "$d" -c user.name=test -c user.email=test@example.invalid commit -q --allow-empty -m c
  for t in "$@"; do git -C "$d" tag "$t"; done
  echo "$d"
}

# check DESC EXPECTED ARGS... : run the helper (DIR arg form) and compare stdout
check() {
  local desc="$1" want="$2"; shift 2
  local got rc=0
  got="$(bash "${SCRIPT}" "$@" 2>/dev/null)" || rc=$?
  if [ "$rc" = 0 ] && [ "$got" = "$want" ]; then pass "$desc"; else fail "$desc (want '${want}', got '${got}', rc=${rc})"; fi
}

# 1. betas of an unreleased version never count
R="$(mkrepo v0.36.0 v0.37.0-beta.1 v0.37.0-beta.2)"
check "1 latest ignores betas" "v0.36.0" "$R"
check "1 --next patch" "0.36.1" --next patch "$R"
check "1 --next minor" "0.37.0" --next minor "$R"
check "1 --next major" "1.0.0" --next major "$R"
got="$(cd "$R" && bash "${SCRIPT}")"
if [ "$got" = "v0.36.0" ]; then pass "1 DIR defaults to cwd"; else fail "1 DIR defaults to cwd (got '${got}')"; fi

# 2. the stable of that version wins once it exists
R="$(mkrepo v0.36.0 v0.37.0-beta.1 v0.37.0-beta.2 v0.37.0)"
check "2 latest" "v0.37.0" "$R"
check "2 --next patch" "0.37.1" --next patch "$R"

# 3. numeric, not lexical, order
R="$(mkrepo v0.2.9 v0.2.10 v0.9.0 v0.10.0)"
check "3 numeric order" "v0.10.0" "$R"
R="$(mkrepo v0.2.9 v0.2.10)"
check "3 patch order" "v0.2.10" "$R"

# 4. noise ignored
R="$(mkrepo v0.1.0 v1.0.0-rc1 v1.0.0-beta.1 v0.37.0-nightly.20261112.abc nightly vfoo v1.2 v1.2.3.4 v1.2.3+meta x9.9.9)"
check "4 noise ignored" "v0.1.0" "$R"
check "4 noise --next minor" "0.2.0" --next minor "$R"

# 4b. minor/major bumps reset the patch (a mutant keeping it would give 0.37.2)
R="$(mkrepo v0.36.2)"
check "4b --next minor resets patch" "0.37.0" --next minor "$R"
check "4b --next major resets minor+patch" "1.0.0" --next major "$R"
check "4b --next patch" "0.36.3" --next patch "$R"

# 4c. an embedded plain version must not match (needs the ^ anchor)
R="$(mkrepo v0.1.0 v99-v1.2.3)"
check "4c embedded version ignored" "v0.1.0" "$R"

# 4d. a leading-zero component is decimal, not octal (review nit)
R="$(mkrepo v0.1.07 v0.1.08)"
check "4d leading zero latest" "v0.1.08" "$R"
check "4d leading zero --next patch" "0.1.9" --next patch "$R"

# 5. no tags
R="$(mkrepo)"
check "5 no tags -> empty" "" "$R"
check "5 no tags --next patch" "0.0.1" --next patch "$R"
check "5 no tags --next minor" "0.1.0" --next minor "$R"
check "5 no tags --next major" "1.0.0" --next major "$R"

# 6. only pre-releases
R="$(mkrepo v1.0.0-beta.1)"
check "6 only pre-release -> empty" "" "$R"
check "6 only pre-release --next patch" "0.0.1" --next patch "$R"

# 7. not a repo -> fail closed
mkdir "${TMP}/plain"
out="$(bash "${SCRIPT}" "${TMP}/plain" 2>/dev/null)"; rc=$?
if [ "$rc" != 0 ] && [ -z "$out" ]; then pass "7 non-repo fails closed"; else fail "7 non-repo fails closed (rc=${rc}, out='${out}')"; fi

# 8. bad usage -> exit 2
bash "${SCRIPT}" --next bogus "$R" >/dev/null 2>&1; rc=$?
if [ "$rc" = 2 ]; then pass "8 unknown kind exits 2"; else fail "8 unknown kind exits 2 (rc=${rc})"; fi
bash "${SCRIPT}" --next >/dev/null 2>&1; rc=$?
if [ "$rc" = 2 ]; then pass "8 missing kind exits 2"; else fail "8 missing kind exits 2 (rc=${rc})"; fi

# ---- wiring (read-only) -----------------------------------------------------
for f in .github/workflows/release.yml scripts/release.sh .github/workflows/nightly.yml; do
  if grep -q 'latest-stable-tag\.sh' "${ROOT_DIR}/${f}"; then pass "wiring: ${f} uses the helper"; else fail "wiring: ${f} does not use latest-stable-tag.sh"; fi
  if grep -Eq "git tag -l 'v\\*'.*sort" "${ROOT_DIR}/${f}"; then fail "wiring: ${f} still sorts git tag output itself"; else pass "wiring: ${f} has no inline tag sort"; fi
done
if grep -q "git describe.*--exclude 'v\\*-\\*'" "${ROOT_DIR}/.github/workflows/ios-testflight.yml"; then pass "wiring: ios-testflight describe excludes pre-releases"; else fail "wiring: ios-testflight describe lacks --exclude 'v*-*'"; fi
if grep -q 'latest-stable-tag_test\.sh' "${ROOT_DIR}/.github/workflows/ci.yml"; then pass "wiring: ci.yml runs this test"; else fail "wiring: ci.yml does not run latest-stable-tag_test.sh"; fi

echo "${PASS_COUNT} passed, ${FAIL_COUNT} failed"
[ "${FAIL_COUNT}" = 0 ]
