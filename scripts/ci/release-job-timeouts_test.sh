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
#     `timeout` and gives `apt-get install` the same Acquire options.
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
