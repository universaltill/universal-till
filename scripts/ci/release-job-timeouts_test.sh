#!/usr/bin/env bash
#
# Wiring test for ut-docs#4059: every release.yml job is time-bounded. In
# release run 38031306576 (v0.31.6) windows-installer had no timeout-minutes
# and its "Build osslsigncode (pinned commit)" step hung for 30+ min, holding
# the draft release for up to GitHub's 6 h default.
#
# Pins:
#   - every job under `jobs:` in .github/workflows/release.yml has a job-level
#     `timeout-minutes: <N>`;
#   - every "Build osslsigncode (pinned commit)" step has its own
#     timeout-minutes and a retrying, time-limited `apt-get update`;
#   - packaging/windows/install-osslsigncode.sh bounds its git clone with
#     `timeout` and gives `apt-get install` the same Acquire options;
#   - ut-docs#7091: release-tests' every `go test` carries `-timeout 20m`, is
#     piped into `go run ./scripts/ci/gotestdur` under `shell: bash`, there are
#     at least two (the `go list ./...` bulk, which includes internal/pages, and
#     ./internal/plugins), and the job timeout-minutes >= 20 * commands + 5.
# Readers consume their whole input (here-strings, no `grep -q` on a pipe) so
# pipefail never turns a SIGPIPE into a false "missing" (ut-docs#2941).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"
WF=".github/workflows/release.yml"
INSTALL="packaging/windows/install-osslsigncode.sh"

fails=0
pass() { echo "ok   - $1"; }
fail() { echo "FAIL - $1" >&2; fails=$((fails + 1)); }

# A 2-space job key, optionally followed by a trailing comment: `  name:` or
# `  name: # why` (review finding — a comment must not hide a job).
JOB_KEY='^  [a-zA-Z_][a-zA-Z0-9_-]*:[[:space:]]*(#.*)?$'
jobs_of() { # job names under jobs:, one per line
  awk '/^jobs:/{on=1; next} on && /^[^ #]/{on=0} on && $0 ~ KEY {sub(/^  /,""); sub(/:.*/,""); print}' KEY="$JOB_KEY" "$WF"
}
job_block() { # job_block <job-name>
  awk -v j="$1" '$0 ~ KEY {k=$0; sub(/^  /,"",k); sub(/:.*/,"",k); if (on) exit; if (k==j) {on=1; print; next}} on{print}' KEY="$JOB_KEY" "$WF"
}
# Steps named "Build osslsigncode (pinned commit)", one block per step,
# blocks separated by a line "----".
osslsigncode_steps() {
  awk '
    /^      - / { if (on) print "----"; on = ($0 ~ /name: Build osslsigncode \(pinned commit\)/) }
    on { print }
    END { if (on) print "----" }' "$WF"
}

check_jobs() { # prints pass/fail for every job; fails when none found
  local jobs j block n=0
  jobs="$(jobs_of)"
  while IFS= read -r j; do
    [ -n "$j" ] || continue
    n=$((n + 1))
    block="$(job_block "$j")"
    if grep -qE '^    timeout-minutes: [0-9]+' <<<"$block"; then
      pass "${j} has a job-level timeout-minutes"
    elif grep -qE '^    uses: \./\.github/workflows/publish-installer\.yml' <<<"$block" && grep -qE '^    timeout-minutes: [0-9]+' "${WF%/*}/publish-installer.yml"; then
      # A reusable-workflow call job cannot carry timeout-minutes (ut-docs#6407);
      # the callee's own job must.
      pass "${j} calls publish-installer.yml, whose job has timeout-minutes"
    else
      fail "${j} has no job-level timeout-minutes (GitHub's default is 6 h)"
    fi
  done <<<"$jobs"
  if [ "$n" -eq 0 ]; then
    fail "found zero jobs in ${WF} (parser broke?)"
  fi
}

# Self-test: a synthetic workflow with an unbounded job must be reported.
selftest_wf="$(mktemp)"
trap 'rm -f "$selftest_wf"' EXIT
cat >"$selftest_wf" <<'YAML'
name: synthetic
jobs:
  bounded:
    runs-on: ubuntu-latest
    timeout-minutes: 5
    steps:
      - run: echo hi
  unbounded:
    runs-on: ubuntu-latest
    steps:
      - run: echo hi
  commented: # a trailing comment must not hide the job or end the previous one
    runs-on: ubuntu-latest
    timeout-minutes: 5
    steps:
      - run: echo hi
  commented-unbounded: # ut-docs#4059
    runs-on: ubuntu-latest
    steps:
      - run: echo hi
YAML
real_wf="$WF"
WF="$selftest_wf"
real_fails="$fails"
selftest_out="$(check_jobs 2>&1 || true)"
fails="$real_fails"
WF="$real_wf"
if grep -qF 'bounded has a job-level' <<<"$selftest_out" \
  && grep -qF 'FAIL - unbounded has no job-level' <<<"$selftest_out" \
  && grep -qF 'ok   - commented has a job-level' <<<"$selftest_out" \
  && grep -qF 'FAIL - commented-unbounded has no job-level' <<<"$selftest_out" \
  && ! grep -qF 'FAIL - bounded' <<<"$selftest_out"; then
  pass "self-test: a job without timeout-minutes is reported"
else
  fail "self-test: parser did not report the unbounded synthetic job"
fi

check_jobs

# ut-docs#7091: release-tests' go test commands are each bounded and the job
# timeout covers them back to back.
check_release_tests() {
  local block code segs seg n=0 listed=0 plug=0 tm
  block="$(job_block release-tests)"
  if [ -z "$block" ]; then
    fail "no release-tests job in ${WF}"
    return
  fi
  code="$(grep -vE '^[[:space:]]*#' <<<"$block" || true)"
  # One segment per line or `&&`-separated command.
  segs="$(sed 's/&&/\n/g' <<<"$code")"
  while IFS= read -r seg; do
    case "$seg" in
    *"go test "*) ;;
    *) continue ;;
    esac
    n=$((n + 1))
    if grep -qE -- '-timeout[ =]20m([[:space:]]|$)' <<<"$seg"; then
      pass "release-tests: go test has -timeout 20m"
    else
      fail "release-tests: go test without -timeout 20m: ${seg}"
    fi
    if grep -qE -- '\|[[:space:]]*go run \./scripts/ci/gotestdur' <<<"$seg"; then
      pass "release-tests: go test is piped into gotestdur"
    else
      fail "release-tests: go test not piped into go run ./scripts/ci/gotestdur: ${seg}"
    fi
    case "$seg" in *"go list ./..."*) listed=$((listed + 1)) ;; esac
    case "$seg" in *"./internal/plugins"*) plug=$((plug + 1)) ;; esac
  done <<<"$segs"
  if [ "$n" -lt 2 ] || [ "$listed" -lt 1 ] || [ "$plug" -lt 1 ]; then
    fail "release-tests: want >= 2 go test commands (a go list ./... bulk and ./internal/plugins), found ${n} (bulk ${listed}, plugins ${plug})"
  else
    pass "release-tests: bulk and plugins go test commands present"
  fi
  if grep -qE '^        shell: bash[[:space:]]*$' <<<"$block"; then
    pass "release-tests: step runs under shell: bash (pipefail)"
  else
    fail "release-tests: no shell: bash (a pipe would hide go test's exit status)"
  fi
  tm="$(sed -nE '0,/^    timeout-minutes: ([0-9]+).*/s//\1/p' <<<"$block")"
  if [ -z "$tm" ]; then
    fail "release-tests: no job-level timeout-minutes"
  elif [ "$tm" -ge $((20 * n + 5)) ]; then
    pass "release-tests: timeout-minutes ${tm} >= 20 * ${n} + 5"
  else
    fail "release-tests: timeout-minutes ${tm} < 20 * ${n} + 5 (two serial 20m commands must hit their own -timeout first)"
  fi
}

rt_wf="$(mktemp)"
trap 'rm -f "$selftest_wf" "$rt_wf"' EXIT
cat >"$rt_wf" <<'YAML'
name: synthetic
jobs:
  release-tests:
    runs-on: ubuntu-latest
    timeout-minutes: 40
    steps:
      - name: Run tests before releasing
        shell: bash
        run: |
          go test -timeout 20m -json $(go list ./... | grep -v x) | go run ./scripts/ci/gotestdur
          go test -json ./internal/plugins | go run ./scripts/ci/gotestdur
YAML
WF="$rt_wf"
real_fails="$fails"
rt_out="$(check_release_tests 2>&1 || true)"
sed -i 's/go test -json \.\/internal/go test -timeout 20m -json .\/internal/' "$rt_wf"
rt_bound="$(check_release_tests 2>&1 || true)"
sed -i 's/timeout-minutes: 40/timeout-minutes: 45/' "$rt_wf"
rt_ok="$(check_release_tests 2>&1 || true)"
fails="$real_fails"
WF="$real_wf"
if grep -qF 'FAIL - release-tests: go test without -timeout 20m: ' <<<"$rt_out" \
  && grep -qF 'FAIL - release-tests: timeout-minutes 40 < 20 * 2 + 5' <<<"$rt_bound" \
  && ! grep -qF 'FAIL' <<<"$rt_ok"; then
  pass "self-test: release-tests without -timeout, a too-small bound and a compliant job"
else
  echo "$rt_out$rt_bound$rt_ok" >&2
  fail "self-test: release-tests checker did not report the synthetic faults"
fi

check_release_tests

# Build osslsigncode steps.
steps="$(osslsigncode_steps)"
if [ -z "$steps" ]; then
  fail "no 'Build osslsigncode (pinned commit)' step found in ${WF}"
else
  idx=0
  cur=""
  while IFS= read -r line; do
    if [ "$line" = "----" ]; then
      idx=$((idx + 1))
      if grep -qE '^        timeout-minutes: [0-9]+' <<<"$cur"; then
        pass "osslsigncode step #${idx} has a step-level timeout-minutes"
      else
        fail "osslsigncode step #${idx} has no step-level timeout-minutes"
      fi
      upd="$(grep -E 'apt-get .*update' <<<"$cur" || true)"
      for opt in 'Acquire::Retries=' 'Acquire::http::Timeout='; do
        if grep -qF -- "-o ${opt}" <<<"$upd"; then
          pass "osslsigncode step #${idx}: apt-get update has -o ${opt}"
        else
          fail "osslsigncode step #${idx}: apt-get update lacks -o ${opt}"
        fi
      done
      cur=""
    else
      cur+="${line}"$'\n'
    fi
  done <<<"$steps"
fi

# Install script.
if grep -qE '^[[:space:]]*(timeout [0-9]+ )git .*clone' "$INSTALL"; then
  pass "install-osslsigncode.sh wraps git clone in timeout"
else
  fail "install-osslsigncode.sh: git clone is not wrapped in 'timeout <seconds>'"
fi
inst="$(grep -E 'apt-get .*install' "$INSTALL" || true)"
for opt in 'Acquire::Retries=' 'Acquire::http::Timeout='; do
  if grep -qF -- "-o ${opt}" <<<"$inst"; then
    pass "install-osslsigncode.sh: apt-get install has -o ${opt}"
  else
    fail "install-osslsigncode.sh: apt-get install lacks -o ${opt}"
  fi
done

if [ "$fails" -ne 0 ]; then
  echo "${fails} check(s) failed" >&2
  exit 1
fi
echo "all release job timeout checks passed"
