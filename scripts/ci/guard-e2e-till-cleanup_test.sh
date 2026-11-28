#!/usr/bin/env bash
#
# Regression test for guard-e2e-till-cleanup.sh (ut-docs#7177), in two parts:
#
#  1. Guard cases: a launcher that `exec`s its binary, or a Playwright config
#     whose webServer entries are not all stopped with SIGTERM, is rejected.
#  2. A behaviour test of e2e/run-till-lib.sh: a launcher in the repo's shape
#     (mktemp -d data dir, EXIT trap that removes it, a long-running "till")
#     is started as its own process group, stopped with SIGTERM to the group
#     the way Playwright's gracefulShutdown does, and must leave neither its
#     data dir nor its child process behind. The same harness run against the
#     old `exec` shape must FAIL, so the check cannot pass vacuously.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-e2e-till-cleanup.sh"
LIB="${ROOT_DIR}/e2e/run-till-lib.sh" # guard-sandbox:allow read-only: sourced by the behaviour test
FAIL_COUNT=0
WORK="$(mktemp -d)"
# Invoked indirectly via `trap ... EXIT` (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() {
  local status=$?
  rm -rf "${WORK}"
  exit "${status}"
}
trap cleanup EXIT

check() {
  local want="$1" name="$2" got="$3"
  if [[ "${got}" != "${want}" ]]; then
    echo "FAIL: ${name}: want ${want}, got ${got}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "ok: ${name}"
  fi
}

expect() {
  local want="$1" name="$2" dir="$3" got
  if bash "${GUARD}" "${dir}" >/dev/null 2>&1; then got=pass; else got=fail; fi
  check "${want}" "${name}" "${got}"
}

# ---- 1. guard cases ----
for d in good exec execq comment ok-lib twoexec cfg-ok cfg-missing cfg-extra cfg-comment; do
  mkdir -p "${WORK}/g/${d}"
done
# The literal $BIN is file content, not an expansion.
# shellcheck disable=SC2016
{
  printf '#!/usr/bin/env bash\nut_e2e_serve "$BIN"\n' >"${WORK}/g/good/run-till.sh"
  printf '#!/usr/bin/env bash\nexec "$BIN"\n' >"${WORK}/g/exec/run-till.sh"
  printf '#!/usr/bin/env bash\n  exec $BIN\n' >"${WORK}/g/execq/run-till-x.sh"
  printf '#!/usr/bin/env bash\n# exec "$BIN" would drop the EXIT trap\nut_e2e_serve "$BIN"\n' >"${WORK}/g/comment/run-till.sh"
  printf '#!/usr/bin/env bash\nexec bash other.sh\n' >"${WORK}/g/ok-lib/run-till-y.sh"
  printf '#!/usr/bin/env bash\nut_e2e_serve "$BIN"\nexec "$BIN"\n' >"${WORK}/g/twoexec/run-till-z.sh"
}
cat >"${WORK}/g/cfg-ok/playwright.config.ts" <<'TS'
export default { webServer: [
  { command: 'bash ./a.sh', gracefulShutdown: TILL_SHUTDOWN },
  { command: 'bash ./b.sh', gracefulShutdown: TILL_SHUTDOWN },
] };
TS
cat >"${WORK}/g/cfg-missing/playwright.config.ts" <<'TS'
export default { webServer: [
  { command: 'bash ./a.sh', gracefulShutdown: TILL_SHUTDOWN },
  { command: 'bash ./b.sh' },
] };
TS
cat >"${WORK}/g/cfg-extra/playwright.config.ts" <<'TS'
export default { webServer: [
  { command: 'bash ./a.sh', gracefulShutdown: { signal: 'SIGKILL', timeout: 1 } },
] };
TS
cat >"${WORK}/g/cfg-comment/playwright.config.ts" <<'TS'
// command: 'bash ./a.sh' is how a till is booted
export default { webServer: [
  { command: 'bash ./a.sh', gracefulShutdown: TILL_SHUTDOWN },
] };
TS
mkdir -p "${WORK}/g/cfg-nosrv"
printf 'export default { webServer: [] };\n' >"${WORK}/g/cfg-nosrv/playwright.webkit.config.ts"
expect pass "launcher serving via ut_e2e_serve" "${WORK}/g/good"
expect fail "launcher ending in exec \"\$BIN\"" "${WORK}/g/exec"
expect fail "launcher ending in exec \$BIN (unquoted, indented)" "${WORK}/g/execq"
expect pass "exec mentioned only in a comment" "${WORK}/g/comment"
expect pass "exec of a literal command (not a variable binary)" "${WORK}/g/ok-lib"
expect fail "serve then exec in the same launcher" "${WORK}/g/twoexec"
expect pass "every webServer entry has gracefulShutdown" "${WORK}/g/cfg-ok"
expect fail "a webServer entry without gracefulShutdown" "${WORK}/g/cfg-missing"
expect fail "gracefulShutdown not using the shared SIGTERM constant" "${WORK}/g/cfg-extra"
expect pass "command: only in a comment is not a webServer entry" "${WORK}/g/cfg-comment"
expect pass "config with an empty webServer" "${WORK}/g/cfg-nosrv"
expect pass "the real tree" "${ROOT_DIR}/e2e"

# ---- 2. behaviour: SIGTERM to the process group leaves nothing behind ----
# A fake "till": records its pid so we can prove it died, and on SIGTERM
# takes a moment to stop and writes into the data dir while it does (the
# real till drains HTTP for up to 5 s). A launcher that removes the data dir
# before its till has exited is left with that late write: a leaked dir.
cat >"${WORK}/fake-till" <<'SH'
#!/usr/bin/env bash
echo $$ >"$FAKE_PID_FILE"
trap 'sleep 0.5; mkdir -p "$DATA_DIR/late" && echo late >"$DATA_DIR/late/write"; exit 0' TERM
sleep 300 &
wait
SH
chmod +x "${WORK}/fake-till"

# $1 = shape ("lib" or "exec"). Prints: gone | leaked-dir | leaked-process
# | not-started.
run_shape() {
  local shape="$1" run="${WORK}/run-${1}" launcher="${WORK}/launcher-${1}.sh"
  mkdir -p "${run}"
  # The launcher mirrors e2e/run-till.sh: throwaway data dir + EXIT trap.
  {
    echo '#!/usr/bin/env bash'
    echo 'set -euo pipefail'
    echo "source \"${LIB}\""
    echo "DATA_DIR=\"\$(mktemp -d \"${run}/data.XXXXXX\")\""
    # The literal $DATA_DIR is launcher text, not an expansion here.
    # shellcheck disable=SC2016
    echo 'trap '\''rm -rf "$DATA_DIR"'\'' EXIT; export DATA_DIR'
    echo "export FAKE_PID_FILE=\"${run}/pid\""
    echo "BIN=\"${WORK}/fake-till\""
    if [[ "${shape}" == lib ]]; then
      # The literal $BIN is launcher text, not an expansion here.
      # shellcheck disable=SC2016
      echo 'ut_e2e_serve "$BIN"'
    else
      # The literal $BIN is launcher text, not an expansion here.
      # shellcheck disable=SC2016
      echo 'exec "$BIN"'
    fi
  } >"${launcher}"
  # Its own process group, like Playwright's detached webServer. perl, not
  # setsid: stock macOS (the developers' machines) has no setsid.
  perl -e 'setpgrp(0, 0); exec @ARGV or die' bash "${launcher}" >/dev/null 2>&1 &
  local pgid=$!
  for _ in $(seq 1 50); do [[ -s "${run}/pid" ]] && break; sleep 0.1; done
  if [[ ! -s "${run}/pid" ]]; then kill -KILL -- "-${pgid}" 2>/dev/null || true; echo not-started; return; fi
  local till_pid
  till_pid="$(cat "${run}/pid")"
  kill -TERM -- "-${pgid}" 2>/dev/null || true # what Playwright's gracefulShutdown does
  for _ in $(seq 1 50); do kill -0 "${pgid}" 2>/dev/null || break; sleep 0.1; done
  # Also let the till finish its slow stop, so a late write is seen.
  for _ in $(seq 1 50); do kill -0 "${till_pid}" 2>/dev/null || break; sleep 0.1; done
  local res=gone
  if compgen -G "${run}/data.*" >/dev/null; then res=leaked-dir; fi
  if kill -0 "${till_pid}" 2>/dev/null; then res=leaked-process; kill -KILL "${till_pid}" 2>/dev/null || true; fi
  kill -KILL -- "-${pgid}" 2>/dev/null || true
  echo "${res}"
}

check gone "launcher using ut_e2e_serve cleans its data dir on SIGTERM to the group" "$(run_shape lib)"
check leaked-dir "the old exec shape leaks its data dir (test is not a false pass)" "$(run_shape exec)"

if [[ "${FAIL_COUNT}" -ne 0 ]]; then
  echo "${FAIL_COUNT} case(s) failed"
  exit 1
fi
echo "all cases passed"
