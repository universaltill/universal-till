#!/usr/bin/env bash
#
# Regression test for guard-armv7-seccomp.sh's log checker (ut-docs#6300,
# ADR-0188). Pure bash, no qemu: feeds fixture strace logs through
# `guard-armv7-seccomp.sh --check-log` and proves each way the armv7 boot can
# regress is rejected (statx, clock_gettime64, an unknown syscall number, a
# syscall outside the Android 9 allow-list, a missing OK marker).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-armv7-seccomp.sh"
TMP="$(mktemp -d -t guard-armv7-seccomp-XXXXXX)"
LOG="${TMP}/log"
OUT="${TMP}/out"
trap 'rm -rf "${TMP}"' EXIT
FAIL_COUNT=0

clean_log() {
  cat > "${LOG}" <<'LOGEOF'
342 getpid() = 342
342 openat(AT_FDCWD,"/tmp/x.db",O_RDWR|O_CREAT|O_LARGEFILE|O_CLOEXEC,0644) = 5
342 fstatat64(5,"",0x40800d9c,AT_EMPTY_PATH) = 0
342 clock_gettime(CLOCK_REALTIME,0x40800d9c) = 0 ({tv_sec = 73,tv_nsec = 21})
343 futex(0x1,FUTEX_WAIT_PRIVATE,0,NULL) = 0
OK 200
LOGEOF
}

assert_passes() {
  if bash "${GUARD}" --check-log "${LOG}" >"${OUT}" 2>&1; then
    echo "PASS (correctly accepted): $1"
  else
    echo "FAIL (should have been accepted but was rejected): $1"; cat "${OUT}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}
# assert_fails <label> <text that must appear in the output>
assert_fails() {
  if bash "${GUARD}" --check-log "${LOG}" >"${OUT}" 2>&1; then
    echo "FAIL (should have been rejected but was accepted): $1"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  elif ! grep -q -- "$2" "${OUT}"; then
    echo "FAIL (rejected, but the message lacks '$2'): $1"; cat "${OUT}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "PASS (correctly rejected): $1"
  fi
}

clean_log
assert_passes "only allow-listed syscalls and the OK marker"

clean_log; sed -i.bak 's/^342 fstatat64(.*/342 statx(5,"",AT_EMPTY_PATH,STATX_BASIC_STATS,0x40800d9c) = 0/' "${LOG}"
assert_fails "statx (397) appears" "statx"

clean_log; sed -i.bak 's/^342 clock_gettime(.*/342 clock_gettime64(CLOCK_REALTIME,0x40800d9c) = 0/' "${LOG}"
assert_fails "clock_gettime64 (403) appears" "clock_gettime64"

# qemu's real format for a number it cannot name (checked against qemu-user 10).
clean_log; echo '342 Unknown syscall 999' >> "${LOG}"
assert_fails "an unknown syscall number appears" "syscall_999"

# Native qemu (CI) glues the program's stdout onto the strace write() line.
clean_log; sed -i.bak 's/^OK 200$/14 write(1,0x7170dc0,7)OK 200/' "${LOG}"; echo ' = 7' >> "${LOG}"
assert_passes "OK 200 glued after the strace write() line"

clean_log; sed -i.bak 's/^OK 200$/14 write(1,0x7170dc0,9)NOT OK 200/' "${LOG}"
assert_fails "OK 200 only inside other text" "OK 200"

clean_log; echo '342 mount(0x1,0x2,0x3,0,0) = -1' >> "${LOG}"
assert_fails "a blacklisted syscall (mount) appears" "mount"

# strace interleaves threads without a newline between them.
clean_log; echo '342 getpid() = 342343 pidfd_open(1,0) = -1' >> "${LOG}"
assert_fails "a bad syscall glued onto another line" "pidfd_open"

clean_log; sed -i.bak '/^OK 200/d' "${LOG}"
assert_fails "the OK 200 marker is missing" "OK 200"

clean_log; sed -i.bak 's/^OK 200/FAIL start: boom/' "${LOG}"
assert_fails "the smoke printed FAIL" "OK 200"

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "guard-armv7-seccomp_test: ${FAIL_COUNT} failure(s)"
  exit 1
fi
echo "guard-armv7-seccomp_test: all cases passed"
