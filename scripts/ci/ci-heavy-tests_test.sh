#!/usr/bin/env bash
#
# Wiring test for ut-docs#4581: ci.yml's heavy test runs sit in their own
# parallel `test-heavy` job, not one after another inside `build`. In run
# 39028767248 they made `build` take 35 of ci's 38-39 min, and every job with
# `needs: build` waited for them.
#
# Pins:
#   - `test-heavy` exists, has a job-level timeout-minutes and no `needs:`
#     (anything it waits on adds straight back to the wall clock);
#   - each heavy command (same flags, -timeout and locale env as before the
#     split) is a `cmd:` leg of that job's matrix, exactly once in ci.yml;
#   - none of them also runs inside `build` (twice in one job is the
#     contention ut-docs#674 found, and serial is what this card removed);
#   - every package `build`'s plain Test step excludes still runs in some
#     other `go test` command in ci.yml — splitting jobs must never drop one.
# Readers consume their whole input (here-strings, no `grep -q` on a pipe) so
# pipefail never turns a SIGPIPE into a false "missing" (ut-docs#2941).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"
WF=".github/workflows/ci.yml"

HEAVY=(
  'go test -timeout 20m -v ./internal/plugins'
  'go test -timeout 20m ./internal/pages'
  'LANG=en_GB.UTF-8 LC_ALL=en_GB.UTF-8 go test -timeout 20m ./internal/pages/...'
  'LANG=de_DE.UTF-8 LC_ALL=de_DE.UTF-8 go test -timeout 20m ./internal/pages/...'
  'go test -race -count=1 ./internal/enroll/ ./internal/logging/ ./internal/netreach/ ./internal/app/ ./internal/server/'
)

fails=0
pass() { echo "ok   - $1"; }
fail() { echo "FAIL - $1" >&2; fails=$((fails + 1)); }

# A 2-space job key, optionally followed by a trailing comment.
JOB_KEY='^  [a-zA-Z_][a-zA-Z0-9_-]*:[[:space:]]*(#.*)?$'
job_block() { # job_block <job-name>
  awk -v j="$1" '$0 ~ KEY {k=$0; sub(/^  /,"",k); sub(/:.*/,"",k); if (on) exit; if (k==j) {on=1; print; next}} on{print}' KEY="$JOB_KEY" "$WF"
}
# Matrix `cmd:` values of a block, one per line, leading "- " and quotes off.
cmds_of() {
  sed -nE "s/^[[:space:]]*(- )?cmd:[[:space:]]*//p" <<<"$1" | sed -E "s/^'(.*)'\$/\1/; s/^\"(.*)\"\$/\1/"
}

check_heavy() {
  local heavy build all c n cmds
  heavy="$(job_block test-heavy)"
  build="$(job_block build)"
  if [ -z "$heavy" ]; then
    fail "no test-heavy job in ${WF}"
    return
  fi
  if grep -qE '^    timeout-minutes: [0-9]+' <<<"$heavy"; then
    pass "test-heavy has a job-level timeout-minutes"
  else
    fail "test-heavy has no job-level timeout-minutes"
  fi
  if grep -qE '^    needs:' <<<"$heavy"; then
    fail "test-heavy has a needs: — it must start beside build, not after it"
  else
    pass "test-heavy has no needs:"
  fi
  if grep -qE '^      fail-fast: false' <<<"$heavy"; then
    pass "test-heavy has fail-fast: false"
  else
    fail "test-heavy lacks fail-fast: false (one red leg would cancel the rest)"
  fi
  # The literal GitHub Actions expression, not a shell expansion.
  # shellcheck disable=SC2016
  if grep -qF '${{ matrix.cmd }}' <<<"$heavy"; then
    pass "test-heavy runs its matrix cmd"
  else
    fail "test-heavy never runs \${{ matrix.cmd }}"
  fi
  cmds="$(cmds_of "$heavy")"
  all="$(cmds_of "$(cat "$WF")")"
  for c in "${HEAVY[@]}"; do
    if grep -qxF -- "$c" <<<"$cmds"; then
      pass "test-heavy leg: ${c}"
    else
      fail "test-heavy has no leg running exactly: ${c}"
    fi
    n="$(grep -cxF -- "$c" <<<"$all" || true)"
    if [ "$n" -gt 1 ]; then
      fail "${c} is a cmd ${n} times in ${WF} (want exactly once)"
    fi
    if grep -qF -- "${c#LANG=*LC_ALL=* }" <<<"$build"; then
      fail "build still runs: ${c#LANG=*LC_ALL=* }"
    else
      pass "build does not run: ${c#LANG=*LC_ALL=* }"
    fi
  done
  # A heavy package back in build under other flags is the same contention
  # (ut-docs#674). Targeted `-run` subsets (the devplugins step) and the plain
  # Test step's `go list | grep -vE` are not whole-package runs.
  local gt p
  gt="$(grep -E '^[^#]*go test ' <<<"$build" | grep -vE '^[[:space:]]*#|grep -vE| -run ' || true)"
  for p in ./internal/plugins ./internal/pages ./internal/enroll ./internal/logging ./internal/netreach ./internal/app ./internal/server; do
    if grep -qE -- "(^|[[:space:]])${p//./\\.}(/|/\.\.\.)?([[:space:]]|\$)" <<<"$gt"; then
      fail "build runs a heavy package: ${p}"
    else
      pass "build runs no whole-package ${p}"
    fi
  done
}

# Self-test: a synthetic workflow with the legs back in build, a needs: and
# a missing leg must be reported.
selftest_wf="$(mktemp)"
trap 'rm -f "$selftest_wf"' EXIT
cat >"$selftest_wf" <<'YAML'
name: synthetic
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: go test -timeout 20m ./internal/pages
      - run: go test -count=1 -v ./internal/plugins
      - run: go test -tags devplugins ./internal/plugins/ -run 'Devplugins'
  test-heavy: # ut-docs#4581
    runs-on: ubuntu-latest
    needs: build
    strategy:
      matrix:
        include:
          - cmd: go test -timeout 20m -v ./internal/plugins
          - cmd: 'go test -timeout 20m ./internal/pages'
    steps:
      - run: ${{ matrix.cmd }}
YAML
real_wf="$WF"
WF="$selftest_wf"
real_fails="$fails"
selftest_out="$(check_heavy 2>&1 || true)"
fails="$real_fails"
WF="$real_wf"
if grep -qF 'FAIL - test-heavy has no job-level timeout-minutes' <<<"$selftest_out" \
  && grep -qF 'FAIL - test-heavy has a needs:' <<<"$selftest_out" \
  && grep -qF 'ok   - test-heavy leg: go test -timeout 20m -v ./internal/plugins' <<<"$selftest_out" \
  && grep -qF 'ok   - test-heavy leg: go test -timeout 20m ./internal/pages' <<<"$selftest_out" \
  && grep -qF 'FAIL - build still runs: go test -timeout 20m ./internal/pages' <<<"$selftest_out" \
  && grep -qF 'FAIL - test-heavy has no leg running exactly: go test -race' <<<"$selftest_out" \
  && grep -qF 'FAIL - test-heavy has no leg running exactly: LANG=de_DE.UTF-8' <<<"$selftest_out" \
  && grep -qF 'FAIL - test-heavy lacks fail-fast: false' <<<"$selftest_out" \
  && grep -qF 'FAIL - build runs a heavy package: ./internal/plugins' <<<"$selftest_out" \
  && grep -qF 'ok   - build runs no whole-package ./internal/app' <<<"$selftest_out"; then
  pass "self-test: a needs:, a missing leg and a leg left in build are reported"
else
  echo "$selftest_out" >&2
  fail "self-test: parser did not report the synthetic workflow's faults"
fi

check_heavy

# Nothing dropped: every package build's plain Test step filters out of
# `go list ./...` is named by another `go test` command in ci.yml.
re="$(sed -nE "s/^[[:space:]]*run: go test \\\$\(go list \.\/\.\.\. \| grep -vE '([^']+)'\)\$/\1/p" "$WF")"
if [ -z "$re" ]; then
  fail "could not find build's plain Test step (run: go test \$(go list ./... | grep -vE '...')) in ${WF}"
else
  # Comment lines never count: a "# reproduce: go test ./x" is not a run.
  others="$(grep -E 'go test ' "$WF" | grep -vE '^[[:space:]]*#' | grep -vF "grep -vE '${re}'" || true)"
  module="$(go list -m)"
  excluded="$(go list ./... | grep -E "$re" || true)"
  if [ -z "$excluded" ]; then
    fail "the Test step's exclude matches no package (regex broke?)"
  fi
  while IFS= read -r p; do
    [ -n "$p" ] || continue
    rel="./${p#"${module}"/}"
    # Word-bounded: ./internal/pages must not count as ./internal/pages/common.
    if grep -qE -- "(^|[[:space:]])${rel//./\\.}(/|/\.\.\.)?([[:space:]]|\$)" <<<"$others"; then
      pass "excluded from Test, still run elsewhere: ${rel}"
    else
      fail "excluded from build's Test step but run by no other go test: ${rel}"
    fi
  done <<<"$excluded"
fi

if [ "$fails" -ne 0 ]; then
  echo "${fails} check(s) failed" >&2
  exit 1
fi
echo "all ci heavy-test wiring checks passed"
