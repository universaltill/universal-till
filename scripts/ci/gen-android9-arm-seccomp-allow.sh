#!/usr/bin/env bash
#
# Regenerates scripts/ci/android9-arm-seccomp-allow.txt, the syscall allow-list
# an Android 9 app process has on 32-bit ARM (ut-docs#6300, ADR-0188).
#
# Network, run rarely (only when the allow-list needs re-deriving). AOSP
# android-9.0.0_r61, bionic libc/: the arm names in SYSCALLS.TXT +
# SECCOMP_WHITELIST_APP.TXT + SECCOMP_WHITELIST_COMMON.TXT, minus
# SECCOMP_BLACKLIST_APP.TXT and SECCOMP_BLACKLIST_COMMON.TXT. The five inputs
# are pinned by sha256; a changed input stops the run.
#
# Usage: gen-android9-arm-seccomp-allow.sh [SRC_DIR]
#   SRC_DIR  use already downloaded copies of the five files (offline; still
#            sha256-checked). Without it they are downloaded.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="${ROOT_DIR}/scripts/ci/android9-arm-seccomp-allow.txt"
TAG="android-9.0.0_r61"
BASE="https://android.googlesource.com/platform/bionic/+/refs/tags/${TAG}/libc"

FILES=(SYSCALLS.TXT SECCOMP_WHITELIST_APP.TXT SECCOMP_WHITELIST_COMMON.TXT SECCOMP_BLACKLIST_APP.TXT SECCOMP_BLACKLIST_COMMON.TXT)
sha_of() {
  case "$1" in
    SYSCALLS.TXT) echo 452b012c4fa9d1f4af1e023e18e6f6ffee83b1d4755789a90e9c7212cac45716 ;;
    SECCOMP_WHITELIST_APP.TXT) echo cdfb23aff0a7745341d9207ea1c293b2e5be924df523e87ab649745e55c77066 ;;
    SECCOMP_WHITELIST_COMMON.TXT) echo a378b8e009a01a887f78a383e978398c417a0a5e0360a289ee48e718884951e0 ;;
    SECCOMP_BLACKLIST_APP.TXT) echo a3cad2b9b51076701a7d44b87d4f99ed5b0771c99151571c9706494c076c36d3 ;;
    SECCOMP_BLACKLIST_COMMON.TXT) echo aafbcd20704317ff2c9f49130461299edbc37d40307e52879aa6ecbbc9ff905f ;;
  esac
}
sha256() { if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }

SRC="${1:-}"
WORK="$(mktemp -d -t android9-allow-XXXXXX)"
trap 'rm -rf "${WORK}"' EXIT
for f in "${FILES[@]}"; do
  if [ -n "${SRC}" ]; then
    cp "${SRC}/${f}" "${WORK}/${f}"
  else
    curl -fsSL "${BASE}/${f}?format=TEXT" | base64 -d > "${WORK}/${f}"
  fi
  got="$(sha256 "${WORK}/${f}")"
  if [ "${got}" != "$(sha_of "${f}")" ]; then
    echo "gen-android9-arm-seccomp-allow: ${f} sha256 ${got} != pinned $(sha_of "${f}")" >&2
    exit 1
  fi
done

WORK="${WORK}" OUT="${OUT}" python3 - <<'PY'
import os, re
d, out = os.environ["WORK"], os.environ["OUT"]

def names(fn):
    res = set()
    for line in open(os.path.join(d, fn)):
        line = line.split("#")[0].strip()
        if not line:
            continue
        m = re.match(r"^[\w\*\s]+?\s([^\s(]+)\(.*\)\s+(\S.*)$", line)
        if not m:
            continue
        head, archs = m.group(1), m.group(2).replace(",", " ").split()
        if not ("all" in archs or "arm" in archs):
            continue
        res.add(head.split(":")[1] if ":" in head else head.split("|")[0])
    return res

allow = names("SYSCALLS.TXT") | names("SECCOMP_WHITELIST_APP.TXT") | names("SECCOMP_WHITELIST_COMMON.TXT")
allow -= names("SECCOMP_BLACKLIST_APP.TXT") | names("SECCOMP_BLACKLIST_COMMON.TXT")
with open(out, "w") as f:
    f.write("\n".join(sorted(allow)) + "\n")
print("wrote %d names to %s" % (len(allow), out))
PY
