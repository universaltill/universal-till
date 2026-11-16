#!/usr/bin/env bash
#
# ut-docs#6300, ADR-0188: SIMULATE the Android 9 app seccomp kill, not just
# list the syscalls (guard-armv7-seccomp.sh does that). Boots a pinned Linux
# 4.9 armhf kernel under qemu-system-arm, installs the byte-exact AOSP
# android-9.0.0_r61 32-bit app filter (RET_TRAP) in the guest and proves:
#   * the pristine-libc smoke build is KILLED (SIGSYS, sigcode=1, statx 397),
#   * the fork build boots and serves GET / = 200 under the same filter,
#   * the pristine build boots WITHOUT the filter (so the filter is the cause).
# Any other outcome is red, in particular a filter that does not kill.
#
# What it does NOT prove (the Fire OS 7 hardware check stays open, ut-docs#6300):
#   * it is the AOSP filter; a vendor build (Fire OS) may filter more;
#   * the smoke is a static executable exec'd under the filter, not the
#     c-shared library inside an ART/zygote-forked Java process;
#   * the CPU is qemu TCG cortex-a15 on a Debian 4.9 kernel, not the device's.
#
# Usage: sim-armv7-seccomp.sh [--fork BIN] [--pristine BIN] [--log FILE] [--qemu BIN]
#   --fork/--pristine  prebuilt linux/arm smoke binaries (CI passes the ones the
#                      guard steps built); omitted -> built here
#   --pristine         may be pointed at the fork build to prove the simulator
#                      goes RED (a simulator that cannot kill must never be green)
# Needs: go, curl, ar, tar (xz), sha256sum or shasum, qemu-system-arm
# (apt install qemu-system-arm | brew install qemu).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# Pinned kernel: Debian stretch 4.9 armhf LPAE (no statx/time64, like a Fire OS 7
# kernel; seccomp runs before dispatch so the filter still traps statx).
# The deb sha256 equals the one in Debian's signed stretch Packages index.
DEB_URL="https://archive.debian.org/debian/pool/main/l/linux/linux-image-4.9.0-13-armmp-lpae_4.9.228-1_armhf.deb"
DEB_SHA="500d153687354a9085d1b50e5c03659ccf56c5d4330f2929f04134486c470894"
VMLINUZ_MEMBER="./boot/vmlinuz-4.9.0-13-armmp-lpae"
VMLINUZ_SHA="7153156d15993865b4c09510dbf7393f3a5d9936724262ef5dac610b1173560f"

FORK="" PRISTINE="" LOG="" QEMU="qemu-system-arm"
while [ $# -gt 0 ]; do
  case "$1" in
    --fork) FORK="${2:?--fork needs a path}"; shift 2 ;;
    --pristine) PRISTINE="${2:?--pristine needs a path}"; shift 2 ;;
    --log) LOG="${2:?--log needs a path}"; shift 2 ;;
    --qemu) QEMU="${2:?--qemu needs a path}"; shift 2 ;;
    *) echo "sim-armv7-seccomp: unknown argument $1" >&2; exit 2 ;;
  esac
done

sha256() {
  if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}
command -v "${QEMU}" >/dev/null || { echo "sim-armv7-seccomp: ${QEMU} not found (apt install qemu-system-arm | brew install qemu)" >&2; exit 2; }
"${QEMU}" --version | sed -n 1p

WORK="$(mktemp -d -t armv7sim-XXXXXX)"
trap 'rm -rf "${WORK}"' EXIT
[ -n "${LOG}" ] || LOG="${WORK}/sim.log"

# --- kernel: https only, sha256 verified before AND after extraction, cached ---
CACHE="${XDG_CACHE_HOME:-${HOME}/.cache}/ut-armv7sim/${DEB_SHA}"
VMLINUZ="${CACHE}/vmlinuz"
if [ -f "${VMLINUZ}" ] && [ "$(sha256 "${VMLINUZ}")" = "${VMLINUZ_SHA}" ]; then
  echo "kernel: cached ${VMLINUZ}"
else
  rm -f "${VMLINUZ}"
  mkdir -p "${CACHE}" "${WORK}/dl" "${WORK}/x"
  curl -fsSL --proto '=https' --tlsv1.2 --retry 3 -o "${WORK}/dl/kernel.deb" "${DEB_URL}"
  [ "$(sha256 "${WORK}/dl/kernel.deb")" = "${DEB_SHA}" ] || { echo "sim-armv7-seccomp: kernel .deb sha256 mismatch" >&2; exit 1; }
  (cd "${WORK}/x" && ar x "${WORK}/dl/kernel.deb" data.tar.xz && tar -xJf data.tar.xz "${VMLINUZ_MEMBER}")
  [ "$(sha256 "${WORK}/x/${VMLINUZ_MEMBER}")" = "${VMLINUZ_SHA}" ] || { echo "sim-armv7-seccomp: vmlinuz sha256 mismatch" >&2; exit 1; }
  cp "${WORK}/x/${VMLINUZ_MEMBER}" "${VMLINUZ}"
  echo "kernel: downloaded and verified"
fi

# --- binaries ---
cd "${ROOT_DIR}"
# build OUT [go build flags] PKG
build() { CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -o "$1" "${@:2}"; }
build "${WORK}/init" ./scripts/armv7sim/guestinit
if [ -z "${FORK}" ]; then FORK="${WORK}/smoke-fork"; build "${FORK}" ./scripts/armv7smoke; fi
if [ -z "${PRISTINE}" ]; then
  PRISTINE="${WORK}/smoke-pristine"
  cp go.mod go.sum "${WORK}/"
  go mod edit -modfile="${WORK}/go.mod" -dropreplace modernc.org/libc
  go mod tidy -modfile="${WORK}/go.mod"
  build "${PRISTINE}" -modfile="${WORK}/go.mod" ./scripts/armv7smoke
fi

go run ./scripts/armv7sim -kernel "${VMLINUZ}" -init "${WORK}/init" \
  -pristine "${PRISTINE}" -fork "${FORK}" -log "${LOG}" -qemu "${QEMU}"
