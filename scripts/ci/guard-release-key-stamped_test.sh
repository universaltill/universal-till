#!/usr/bin/env bash
#
# Regression test for guard-release-key-stamped.sh (ADR-0178): fixture trees in
# a fresh mktemp dir prove it fails a MarketplacePublicKey stamp with no
# ReleasePublicKeys (Makefile, workflow, gradle), ignores comments and grep
# lines, fails a tree with no stamp at all, and passes on the real tree.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-release-key-stamped.sh"
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
  echo "LDFLAGS += ${X}.MarketplacePublicKey=\$(K) ${X}.ReleasePublicKeys=\$(R)" >"${REPO}/Makefile"
  cat >"${REPO}/.github/workflows/release.yml" <<YAML
jobs:
  build:
    steps:
      - run: go build -ldflags "${X}.MarketplacePublicKey=\${K} ${X}.ReleasePublicKeys=\${R}" .
      - run: grep -oE 'buildinfo\.MarketplacePublicKey=[0-9a-f]{64}' <<< "\$ldflags"
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
expect pass "a tree whose every stamp carries ReleasePublicKeys (grep line ignored)"

reset_tree
echo "LDFLAGS += ${X}.MarketplacePublicKey=\$(K)" >"${REPO}/Makefile"
expect fail "a Makefile stamp without ReleasePublicKeys"

reset_tree
echo "      - run: go build -ldflags \"${X}.MarketplacePublicKey=\${K}\" ." >>"${REPO}/.github/workflows/release.yml"
expect fail "a workflow stamp without ReleasePublicKeys"

reset_tree
echo "\"-ldflags\", \"${X}.MarketplacePublicKey=\$k\"," >"${REPO}/android/app/build.gradle.kts"
expect fail "a gradle stamp without ReleasePublicKeys"

reset_tree
echo "# ${X}.MarketplacePublicKey=\$(K)" >>"${REPO}/Makefile"
expect pass "a comment mentioning the stamp"

reset_tree
rm -f "${REPO}/Makefile" "${REPO}/.github/workflows/release.yml"
expect fail "a tree with no stamp at all"

if bash "${GUARD}" "${ROOT_DIR}" >"${WORK}/out" 2>&1; then
  echo "✓ guard → pass: the real tree"
else
  echo "❌ FAIL: guard fails on the real tree" >&2
  cat "${WORK}/out" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

[ "$FAIL_COUNT" -eq 0 ] || exit 1
echo "all guard-release-key-stamped checks passed"
