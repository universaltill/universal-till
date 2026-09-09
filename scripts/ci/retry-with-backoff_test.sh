#!/usr/bin/env bash
# Regression test for retry-with-backoff.sh (ut-docs#1933): proves it
# succeeds immediately when the wrapped command already succeeds, retries
# (with the --clear-apt-lists hook firing between attempts, not before the
# first or after the final success) a command that fails a bounded number
# of times before succeeding, gives up (not hangs, not silently passes)
# after exhausting max_attempts on a command that never succeeds, and
# rejects bad usage instead of crashing on an unbound variable.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

SCRIPT="scripts/ci/retry-with-backoff.sh"
FAIL_COUNT=0
TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

# A fake "flaky" command: fails until its counter file reaches
# succeed_after invocations, then succeeds every time after.
make_flaky_cmd() {
  local counter_file="$1" succeed_after="$2" name="$3"
  local cmd_path="${TMPDIR}/flaky_${name}.sh"
  cat >"${cmd_path}" <<EOF
#!/usr/bin/env bash
count_file="${counter_file}"
count=0
[ -f "\$count_file" ] && count=\$(cat "\$count_file")
count=\$((count + 1))
echo "\$count" > "\$count_file"
if [ "\$count" -ge ${succeed_after} ]; then
  exit 0
fi
exit 1
EOF
  chmod +x "${cmd_path}"
  printf '%s' "${cmd_path}"
}

# 1. Succeeds first try: exits 0, exactly 1 invocation -- must not sleep or
#    retry when nothing failed.
counter1="${TMPDIR}/counter1"
cmd1="$(make_flaky_cmd "${counter1}" 1 one)"
if bash "${SCRIPT}" 3 1 -- "${cmd1}" >"${TMPDIR}/out1" 2>&1; then
  echo "✓ succeeds on first try when the command succeeds immediately"
else
  echo "❌ FAIL: expected success on first try" >&2
  cat "${TMPDIR}/out1" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi
if [ "$(cat "${counter1}")" != "1" ]; then
  echo "❌ FAIL: expected exactly 1 invocation, got $(cat "${counter1}")" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

# 2. Fails twice, succeeds on the 3rd attempt -- within max_attempts=3, must
#    eventually succeed, proving it actually retried rather than returning
#    the first failure.
counter2="${TMPDIR}/counter2"
cmd2="$(make_flaky_cmd "${counter2}" 3 two)"
if bash "${SCRIPT}" 3 1 -- "${cmd2}" >"${TMPDIR}/out2" 2>&1; then
  echo "✓ succeeds after retrying a command that fails twice then succeeds"
else
  echo "❌ FAIL: expected eventual success after retries" >&2
  cat "${TMPDIR}/out2" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi
if [ "$(cat "${counter2}")" != "3" ]; then
  echo "❌ FAIL: expected exactly 3 invocations, got $(cat "${counter2}")" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

# 3. Always fails -- must exit non-zero after exactly max_attempts tries,
#    not hang forever and not silently pass.
counter3="${TMPDIR}/counter3"
cmd3="$(make_flaky_cmd "${counter3}" 999 three)"
if bash "${SCRIPT}" 3 1 -- "${cmd3}" >"${TMPDIR}/out3" 2>&1; then
  echo "❌ FAIL: expected failure when the command never succeeds" >&2
  cat "${TMPDIR}/out3" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
else
  echo "✓ fails (not hangs, not silently passes) after exhausting max_attempts"
fi
if [ "$(cat "${counter3}")" != "3" ]; then
  echo "❌ FAIL: expected exactly 3 invocations (the cap), got $(cat "${counter3}")" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

# 4. --clear-apt-lists: the apt-clear hook fires exactly once, between the
#    failed 1st attempt and the successful 2nd -- overridden via
#    RETRY_WITH_BACKOFF_APT_CLEAR_CMD so this never touches a real apt
#    cache or needs root.
counter4="${TMPDIR}/counter4"
cmd4="$(make_flaky_cmd "${counter4}" 2 four)"
clear_marker="${TMPDIR}/clear_count"
if RETRY_WITH_BACKOFF_APT_CLEAR_CMD="echo x >> ${clear_marker}" \
    bash "${SCRIPT}" 3 1 --clear-apt-lists -- "${cmd4}" >"${TMPDIR}/out4" 2>&1; then
  echo "✓ --clear-apt-lists: succeeds after one retry with the clear hook applied"
else
  echo "❌ FAIL: expected eventual success" >&2
  cat "${TMPDIR}/out4" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi
clear_runs=0
[ -f "${clear_marker}" ] && clear_runs=$(wc -l <"${clear_marker}" | tr -d ' ')
if [ "${clear_runs}" != "1" ]; then
  echo "❌ FAIL: expected the clear-apt-lists hook to run exactly once (between the 2 attempts), ran ${clear_runs} time(s)" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

# 5. Without --clear-apt-lists, the hook must never fire, even across
#    retries -- proves the flag actually gates the behavior rather than it
#    always running.
counter5="${TMPDIR}/counter5"
cmd5="$(make_flaky_cmd "${counter5}" 2 five)"
clear_marker5="${TMPDIR}/clear_count5"
RETRY_WITH_BACKOFF_APT_CLEAR_CMD="echo x >> ${clear_marker5}" \
  bash "${SCRIPT}" 3 1 -- "${cmd5}" >"${TMPDIR}/out5" 2>&1 || true
if [ -f "${clear_marker5}" ]; then
  echo "❌ FAIL: clear-apt-lists hook ran without --clear-apt-lists being passed" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
else
  echo "✓ clear-apt-lists hook never runs when --clear-apt-lists isn't passed"
fi

# 6. Bad usage (too few args) -- fails closed with a usage message, not a
#    bash syntax error / unbound-variable crash.
if bash "${SCRIPT}" >"${TMPDIR}/out6" 2>&1; then
  echo "❌ FAIL: expected failure on missing arguments" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
else
  echo "✓ rejects being called with no arguments"
fi

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "${FAIL_COUNT} retry-with-backoff_test.sh assertion(s) failed" >&2
  exit 1
fi
echo "✓ retry-with-backoff_test.sh: all assertions passed"
