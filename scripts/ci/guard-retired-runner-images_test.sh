#!/usr/bin/env bash
# Regression test for guard-retired-runner-images.sh (ut-docs#5214).
set -euo pipefail
# shellcheck source=scripts/ci/guard-test-sandbox.sh
source "$(dirname "${BASH_SOURCE[0]}")/guard-test-sandbox.sh"
enter_guard_test_sandbox .github/workflows
trap remove_guard_test_sandbox EXIT
GUARD="scripts/ci/guard-retired-runner-images.sh"
fails=0

expect() { # expect pass|fail <label>
  local want="$1" label="$2" out got
  if out="$(bash "$GUARD" 2>&1)"; then got=pass; else got=fail; fi
  if [ "$got" = "$want" ]; then
    echo "ok   - ${label}"
  else
    echo "FAIL - ${label}: guard said ${got}, want ${want}"
    printf '%s\n' "$out"
    fails=$((fails + 1))
  fi
}

plant() { # plant <line>...  (replaces the planted workflow)
  printf '%s\n' 'on: push' 'jobs:' '  a:' "$@" '    steps: []' >.github/workflows/zz-planted.yml
}

expect pass "real tree passes"

plant '    runs-on: macos-14'
expect fail "runs-on: macos-14 fails"
plant '    runs-on: macos-14 # Apple Silicon'
expect fail "macos-14 with trailing comment fails"
plant '    runs-on: [self-hosted, ubuntu-20.04]'
expect fail "ubuntu-20.04 in a runs-on list fails"
plant '    strategy:' '      matrix:' '        os: [ubuntu-latest, windows-2019]'
expect fail "matrix os: windows-2019 fails"
plant '    strategy:' '      matrix:' '        include:' '          - runner: macos-13'
expect fail "matrix runner: macos-13 fails"
plant '    runs-on: macos-15'
expect pass "macos-15 passes"
plant '    runs-on: macos-15 # moved off macos-14 (ut-docs#5214)' '    # macos-13 ubuntu-20.04 history'
expect pass "comment-only mentions pass"
plant '    runs-on: macos-14-large'
expect fail "the -large variant retires with its base image"
plant '    runs-on: macos-14-xlarge'
expect fail "the -xlarge variant retires with its base image"
plant '    runs-on: macos-14-arm64-custom'
expect pass "an unrelated longer label is not the retired one"

[ "$fails" -eq 0 ] || { echo "$fails case(s) failed"; exit 1; }
echo "all cases passed"
