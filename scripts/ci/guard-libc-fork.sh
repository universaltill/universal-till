#!/usr/bin/env bash
#
# ut-docs#6300 / ADR-0188: modernc.org/libc is replaced by our fork
# github.com/universaltill/modernc-libc, which is upstream plus one reviewed
# patch (scripts/ci/libc-ut.patch). Drop the replace and the armv7 Android app
# crashes with SIGSYS on Fire OS 7 again.
#
# Static mode (default, fast, no network): go.mod requires modernc.org/libc vX,
# has exactly one replace for it, to the fork at vX-ut.N with the same X, and
# go.sum carries both fork lines.
#
# --verify-upstream (network): proves fork tag == upstream vX + the checked-in
# patch, exactly three files differing.
#
# Usage: guard-libc-fork.sh [GO_MOD [GO_SUM]]
#        guard-libc-fork.sh --verify-upstream [GO_MOD]
#
# A sqlite bump that raises the libc require lands here: re-fork upstream at the
# new version (ADR-0188 section 2.4), tag vX-ut.1, apply the patch, update the
# replace and scripts/ci/libc-ut.patch. Drop the fork when upstream's 32-bit ARM
# libc stops needing statx/time64 from the kernel (ADR-0188 drop conditions).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FORK="github.com/universaltill/modernc-libc"

VERIFY=0
if [ "${1:-}" = "--verify-upstream" ]; then VERIFY=1; shift; fi
GOMOD="${1:-${ROOT_DIR}/go.mod}"
GOSUM="${2:-${GOMOD%go.mod}go.sum}"

fail() {
  echo "guard-libc-fork: $1" >&2
  echo "  Why: modernc.org/libc must stay our fork or the armv7 Android app dies with SIGSYS on Fire OS 7 (ut-docs#6300)." >&2
  echo "  Fix: re-fork upstream at the required version, patch, tag vX-ut.N, point the replace at it (ADR-0188 section 2.4)." >&2
  exit 1
}

[ -f "${GOMOD}" ] || fail "${GOMOD} missing"

# Strip comments; accept both single-line and block require/replace forms.
code="$(sed -E 's#[[:space:]]*//.*$##' "${GOMOD}")"
req="$(printf '%s\n' "${code}" | awk '
  /^require[[:space:]]*\(/ {inb=1; next}
  inb && /^\)/ {inb=0; next}
  inb && $1=="modernc.org/libc" {print $2}
  /^require[[:space:]]+modernc.org\/libc[[:space:]]/ {print $3}')"
rep="$(printf '%s\n' "${code}" | awk '
  /^replace[[:space:]]*\(/ {inb=1; next}
  inb && /^\)/ {inb=0; next}
  inb && $1=="modernc.org/libc" && $2=="=>" {print $3" "$4}
  /^replace[[:space:]]+modernc.org\/libc[[:space:]]/ {print $4" "$5}')"

[ -n "${req}" ] || fail "go.mod has no 'require modernc.org/libc vX'"
[ "$(printf '%s\n' "${req}" | wc -l | tr -d ' ')" = "1" ] || fail "go.mod requires modernc.org/libc more than once"
[ -n "${rep}" ] || fail "go.mod has no 'replace modernc.org/libc => ${FORK} vX-ut.N'"
[ "$(printf '%s\n' "${rep}" | wc -l | tr -d ' ')" = "1" ] || fail "go.mod has more than one replace for modernc.org/libc"

rep_path="${rep%% *}"; rep_ver="${rep#* }"
[ "${rep_path}" = "${FORK}" ] || fail "modernc.org/libc is replaced by '${rep_path}', expected ${FORK}"
[[ "${rep_ver}" =~ ^"${req}"-ut\.[0-9]+$ ]] || fail "replace version '${rep_ver}' does not match the required libc ${req} (expected ${req}-ut.N)"

[ -f "${GOSUM}" ] || fail "${GOSUM} missing"
awk -v p="${FORK}" -v v="${rep_ver}" '$1==p && $2==v && $3 ~ /^h1:/ {f=1} END {exit !f}' "${GOSUM}" || fail "go.sum lacks the ${FORK} ${rep_ver} module hash (run go mod tidy)"
awk -v p="${FORK}" -v v="${rep_ver}/go.mod" '$1==p && $2==v && $3 ~ /^h1:/ {f=1} END {exit !f}' "${GOSUM}" || fail "go.sum lacks the ${FORK} ${rep_ver}/go.mod hash (run go mod tidy)"

if [ "${VERIFY}" -eq 0 ]; then
  echo "guard-libc-fork: modernc.org/libc ${req} => ${FORK} ${rep_ver}"
  exit 0
fi

# --- supply-chain proof: fork tag == upstream + the reviewed patch ---------
TMP="$(mktemp -d -t libc-fork-XXXXXX)"
trap 'chmod -R u+w "${TMP}" 2>/dev/null || true; rm -rf "${TMP}"' EXIT
cat > "${TMP}/go.mod" <<M
module verify.invalid/x

go 1.21
M
cd "${TMP}"
info="$(GOFLAGS=-mod=mod go mod download -json "modernc.org/libc@${req}" "${FORK}@${rep_ver}")"
up_dir="$(printf '%s' "${info}" | python3 -c '
import json,sys
dec=json.JSONDecoder(); s=sys.stdin.read(); i=0
while i<len(s):
    while i<len(s) and s[i].isspace(): i+=1
    if i>=len(s): break
    o,i=dec.raw_decode(s,i)
    if o["Path"]=="modernc.org/libc": print(o["Dir"])')"
fk_dir="$(printf '%s' "${info}" | python3 -c '
import json,sys
dec=json.JSONDecoder(); s=sys.stdin.read(); i=0
while i<len(s):
    while i<len(s) and s[i].isspace(): i+=1
    if i>=len(s): break
    o,i=dec.raw_decode(s,i)
    if o["Path"]=="'"${FORK}"'": print(o["Dir"])')"
[ -d "${up_dir}" ] && [ -d "${fk_dir}" ] || fail "could not download upstream ${req} and the fork ${rep_ver}"

# The tree compared below must be the one this repo builds: its hashes must be
# the ones pinned in go.sum (the download itself went through sum.golang.org).
fk_sums="$(printf '%s' "${info}" | python3 -c '
import json,sys
dec=json.JSONDecoder(); s=sys.stdin.read(); i=0
while i<len(s):
    while i<len(s) and s[i].isspace(): i+=1
    if i>=len(s): break
    o,i=dec.raw_decode(s,i)
    if o["Path"]=="'"${FORK}"'": print(o.get("Sum","")+" "+o.get("GoModSum",""))')"
want_sums="$(awk -v p="${FORK}" -v v="${rep_ver}" '$1==p && $2==v {a=$3} $1==p && $2==v"/go.mod" {b=$3} END {print a" "b}' "${GOSUM}")"
[ "${fk_sums}" = "${want_sums}" ] || fail "the downloaded fork ${rep_ver} hashes (${fk_sums}) are not the go.sum ones (${want_sums})"

# Compare the two trees file by file in python: exactly the three expected
# files may differ, and their diff must equal the checked-in patch.
set +e
msg="$(UP="${up_dir}" FK="${fk_dir}" PATCH="${ROOT_DIR}/scripts/ci/libc-ut.patch" python3 -I - <<'PY'
import difflib, os, sys

up, fk, patch = os.environ["UP"], os.environ["FK"], os.environ["PATCH"]
want = ["syscall_musl.go", "syscall_ut_pre411.go", "syscall_ut_pre411_test.go"]

def tree(root):
    out = {}
    for d, _, fs in os.walk(root):
        for f in fs:
            p = os.path.join(d, f)
            out[os.path.relpath(p, root)] = open(p, "rb").read()
    return out

a, b = tree(up), tree(fk)
diff = sorted(k for k in set(a) | set(b) if a.get(k) != b.get(k))
if diff != want:
    print("unexpected differing files: " + ", ".join(diff))
    sys.exit(1)

def hunks(lines):
    # compare only hunk bodies, not the file header lines
    return [l for l in lines if not l.startswith(("diff ", "--- ", "+++ "))]

got = []
for f in want:
    x = a.get(f, b"").decode().splitlines()
    y = b[f].decode().splitlines()
    got += hunks(list(difflib.unified_diff(x, y, "a/" + f, "b/" + f, lineterm="")))
exp = hunks(open(patch).read().splitlines())
if got != exp:
    print("the diff is not scripts/ci/libc-ut.patch")
    sys.exit(1)
PY
)"
rc=$?
set -e
[ "${rc}" -eq 0 ] || fail "fork ${rep_ver} vs upstream ${req}: ${msg}"
echo "guard-libc-fork: ${FORK} ${rep_ver} == modernc.org/libc ${req} + scripts/ci/libc-ut.patch"
