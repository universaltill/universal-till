#!/usr/bin/env bash
#
# Regression test for guard-builddate-stamped.sh (ut-docs#4369): fixture
# trees in a fresh mktemp dir prove it fails a Version stamp with no
# BuildDate (Makefile, workflow, gradle), ignores comments and echo lines,
# fails a tree with no stamp at all, and passes on the real tree.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-builddate-stamped.sh"
FAIL_COUNT=0

WORK="$(mktemp -d)"
# Invoked indirectly via `trap ... EXIT` (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() { rm -rf -- "${WORK:?}"; }
trap cleanup EXIT

REPO="${WORK}/repo"
X='-X github.com/universaltill/universal-till/internal/buildinfo'

reset_tree() {
  rm -rf -- "${REPO:?}"
  mkdir -p "${REPO}/.github/workflows" "${REPO}/android/app"
  echo "LDFLAGS=-s -w ${X}.Version=\$(VERSION) ${X}.BuildDate=\$(BUILD_DATE)" >"${REPO}/Makefile"
  cat >"${REPO}/.github/workflows/release.yml" <<YAML
jobs:
  build:
    steps:
      - run: go build -ldflags "${X}.Version=\${VERSION} ${X}.BuildDate=\$(git show -s --format=%cI HEAD)" .
      - run: echo "::error::embeds buildinfo.Version=dev"
YAML
}

expect() {
  local want="$1" what="$2"
  if bash "${GUARD}" "${REPO}" >"${WORK}/out" 2>&1; then got=pass; else got=fail; fi
  if [ "$got" = "$want" ]; then
    echo "✓ guard → ${want}: ${what}"
  else
    echo "❌ FAIL: guard should ${want} ${what}, got ${got}" >&2
    cat "${WORK}/out" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

reset_tree
expect pass "a tree whose every stamp carries BuildDate (echo lines ignored)"

reset_tree
echo "LDFLAGS=-s -w ${X}.Version=\$(VERSION)" >"${REPO}/Makefile"
expect fail "a Makefile Version stamp without BuildDate"

reset_tree
echo "      - run: go build -ldflags \"${X}.Version=\${VERSION}\" ." >>"${REPO}/.github/workflows/release.yml"
expect fail "a workflow Version stamp without BuildDate"

reset_tree
echo "\"-ldflags\", \"${X}.Version=\$v -s -w\"," >"${REPO}/android/app/build.gradle.kts"
expect fail "a gradle Version stamp without BuildDate"

reset_tree
printf '# example: %s.Version=1.2.3\n' "$X" >>"${REPO}/Makefile"
printf '            // %s.Version=1.2.3\n' "$X" >"${REPO}/android/app/build.gradle.kts"
expect pass "commented-out stamps"

reset_tree
: >"${REPO}/Makefile"
: >"${REPO}/.github/workflows/release.yml"
expect fail "a tree with no Version stamp at all (broken scan)"

if bash "${GUARD}" >"${WORK}/out" 2>&1; then
  echo "✓ guard passes on the real tree"
else
  echo "❌ FAIL: guard fails on the real tree" >&2
  cat "${WORK}/out" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "❌ guard-builddate-stamped_test: ${FAIL_COUNT} failure(s)" >&2
  exit 1
fi
echo "✓ guard-builddate-stamped_test: all cases pass"
