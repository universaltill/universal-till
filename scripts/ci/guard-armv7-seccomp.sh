#!/usr/bin/env bash
#
# ut-docs#6300 / ADR-0188: the armv7 (Fire OS 7, Android 9) app must boot
# without making a syscall the Android 9 app seccomp filter would kill it for.
# That filter answers an unknown number with SIGSYS, not ENOSYS, so musl's
# "newest syscall first" code (statx 397, clock_gettime64 403) crashed the app
# before the first screen. A 32-bit ARM libc patch (modernc-libc fork) answers
# those from user space; this guard proves it stays that way.
#
# It cross-builds scripts/armv7smoke (the real mobile.Start boot path, CGO off,
# GOARCH=arm GOARM=7), runs it under `qemu-arm -strace` with the uname of a 4.9
# kernel (so the Go runtime picks the same futex/time32 path as on the device),
# and fails when the run does not print "OK 200" or any syscall it made is not
# in scripts/ci/android9-arm-seccomp-allow.txt.
#
# Usage:
#   guard-armv7-seccomp.sh [GO_MODFILE] [BINARY_OUT]
#       GO_MODFILE   alternate go.mod (go build -modfile), used by the CI
#                    negative proof (pristine libc must FAIL this guard)
#       BINARY_OUT   keep the built binary there
#   guard-armv7-seccomp.sh --check-log LOG
#       only check an existing strace log (no qemu); what the _test.sh drives
# Needs qemu-arm (apt: qemu-user) for the run mode.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ALLOW="${ROOT_DIR}/scripts/ci/android9-arm-seccomp-allow.txt"

# check_log LOG -> 0 when clean; prints why and returns 1 otherwise.
check_log() {
  local log="$1" bad="" rc=0
  [ -f "${log}" ] || { echo "guard-armv7-seccomp: log ${log} missing" >&2; return 1; }
  # strace glues interleaved thread lines together without a newline, so pick
  # "<tid> <name>(" out of the whole stream rather than line starts.
  # qemu prints a number it has no name for as "<tid> Unknown syscall N" (no
  # parenthesis): report it as syscall_N, never on the allow-list, so a
  # syscall newer than qemu's table cannot slip through unseen.
  bad="$( { grep -aoE '[0-9]+ [a-z_0-9]+\(' "${log}" \
      | sed -E 's/^[0-9]+ ([a-z_0-9]+)\($/\1/'
    grep -aoE 'Unknown syscall [0-9]+' "${log}" \
      | sed -E 's/^Unknown syscall ([0-9]+)$/syscall_\1/'
  } | sort -u | grep -vxFf "${ALLOW}" || true)"
  if [ -n "${bad}" ]; then
    echo "guard-armv7-seccomp: the armv7 boot made syscall(s) the Android 9 app seccomp filter does not allow (it would kill the app with SIGSYS, ut-docs#6300, ADR-0188):" >&2
    while IFS= read -r name; do echo "  - ${name}" >&2; done <<< "${bad}"
    echo "  statx/clock_gettime64 mean the modernc.org/libc fork patch is missing or stale (scripts/ci/guard-libc-fork.sh);" >&2
    echo "  anything else is a new dependency/Go change: answer it in the fork patch, or prove the syscall is allowed and regenerate the list (scripts/ci/gen-android9-arm-seccomp-allow.sh)." >&2
    rc=1
  fi
  # Native qemu writes "<tid> write(1,0x..,7)" to the same file just before the
  # program's own output, so the marker usually follows a ')' on that line.
  # Another thread's strace line can land in between and end without a
  # newline (" = 0"), so the marker may also follow a syscall return value
  # (ut-docs#6755: a clean exit_group(0) run was reported as failed).
  if ! grep -aqE '(^|\)|= -?[0-9]+)OK 200' "${log}"; then
    echo "guard-armv7-seccomp: the smoke did not print 'OK 200' (boot or GET / failed); log tail:" >&2
    tail -n 5 "${log}" | cut -c1-200 | sed 's/^/  /' >&2
    rc=1
  fi
  return "${rc}"
}

if [ "${1:-}" = "--check-log" ]; then
  check_log "${2:?usage: --check-log LOG}"
  exit $?
fi

MODFILE="${1:-}"
KEEP="${2:-}"
command -v qemu-arm >/dev/null || { echo "guard-armv7-seccomp: qemu-arm not found (apt install qemu-user)" >&2; exit 2; }

TMP="$(mktemp -d -t armv7-seccomp-XXXXXX)"
trap 'rm -rf "${TMP}"' EXIT
BIN="${TMP}/armv7smoke"
LOG="${TMP}/strace.log"

cd "${ROOT_DIR}"
args=()
[ -n "${MODFILE}" ] && args+=("-modfile=${MODFILE}")
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build "${args[@]+"${args[@]}"}" -o "${BIN}" ./scripts/armv7smoke
[ -n "${KEEP}" ] && cp "${BIN}" "${KEEP}"

# A 4.9 kernel uname makes the Go runtime use the time32 futex like a Fire OS 7 device.
QEMU_UNAME=4.9.190 timeout 180 qemu-arm -strace "${BIN}" > "${LOG}" 2>&1 || true
check_log "${LOG}"
