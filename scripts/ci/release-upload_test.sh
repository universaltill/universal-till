#!/usr/bin/env bash
# Test for release-upload.sh (ut-docs#6405): a mocked `gh` records every call.
# Fixture and `A && ok || fail` assertion style used throughout this test.
# shellcheck disable=SC2015
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT_DIR}/scripts/ci/release-upload.sh"
WORK="$(mktemp -d)"
# shellcheck disable=SC2317
cleanup() { rm -rf -- "${WORK:?}"; }
trap cleanup EXIT
mkdir -p "$WORK/bin"
cat >"$WORK/bin/gh" <<'GH'
#!/usr/bin/env bash
echo "gh $*" >>"$MOCK_LOG"
if [ "$1 $2" = "release view" ]; then
  [ "${MOCK_VIEW_FAIL:-}" = 1 ] && exit 1
  case "$*" in
    *isDraft*) echo "$MOCK_DRAFT" ;;
    *assets*) printf '%s\n' $MOCK_ASSETS ;;
  esac
  exit 0
fi
if [ "$1 $2" = "release download" ]; then
  pat="" dir=""
  while [ "$#" -gt 0 ]; do
    case "$1" in --pattern) pat=$2; shift ;; --dir) dir=$2; shift ;; esac
    shift
  done
  [ "${MOCK_DL_FAIL:-}" = 1 ] && exit 1
  [ -f "$MOCK_PUB/$pat" ] || exit 1
  cp "$MOCK_PUB/$pat" "$dir/"
  exit 0
fi
exit 0
GH
chmod +x "$WORK/bin/gh"
echo x >"$WORK/unitill-pos-setup-1.0.0.exe"; echo x >"$WORK/checksums.txt"; echo x >"$WORK/new.dmg"
mkdir -p "$WORK/pub" "$WORK/new"
export PATH="$WORK/bin:$PATH" MOCK_LOG="$WORK/log" MOCK_PUB="$WORK/pub"
fails=0
check() { # check <want-exit> <name> <draft> <assets> <files...>
  local want=$1 name=$2 draft=$3 assets=$4; shift 4
  : >"$MOCK_LOG"
  local rc=0
  MOCK_DRAFT=$draft MOCK_ASSETS=$assets bash "$SCRIPT" v1.0.0 "$@" >"$WORK/out" 2>&1 || rc=$?
  if [ "$rc" = "$want" ]; then echo "ok   - $name"; else echo "FAIL - $name (exit $rc, want $want)" >&2; cat "$WORK/out" >&2; fails=$((fails+1)); fi
}
logs() { grep -c "release upload" "$MOCK_LOG" || true; }

EXE="$WORK/unitill-pos-setup-1.0.0.exe"
check 0 "draft: re-upload of an existing asset is allowed (retry-safe)" true "unitill-pos-setup-1.0.0.exe" "$EXE"
grep -q -- "--clobber" "$MOCK_LOG" && echo "ok   - draft upload uses --clobber" || { echo "FAIL - draft clobber" >&2; fails=$((fails+1)); }
check 1 "published: an existing exe is refused" false "unitill-pos-setup-1.0.0.exe" "$EXE"
[ "$(logs)" = 0 ] && echo "ok   - refused upload never reached gh release upload" || { echo "FAIL - upload happened" >&2; fails=$((fails+1)); }
check 0 "published: a new asset (the dmg) is uploaded without --clobber" false "unitill-pos-setup-1.0.0.exe" "$WORK/new.dmg"
! grep -q -- "--clobber" "$MOCK_LOG" && echo "ok   - new asset has no --clobber" || { echo "FAIL - clobber on new" >&2; fails=$((fails+1)); }
# --- extend-only checksum files (ut-docs#6636) ---
SA="$(printf 'a%.0s' {1..64})  unitill-pos-setup-1.0.0.exe"
SB="$(printf 'b%.0s' {1..64})  other.zip"
SC="$(printf 'c%.0s' {1..64})  new.dmg"
SBX="$(printf 'b%.0s' {1..63})d  other.zip"
SDUP="$(printf 'd%.0s' {1..64})  unitill-pos-setup-1.0.0.exe"
CS="$WORK/new/checksums.txt"; SIG="$WORK/new/checksums.txt.sig"
# pub/newf <name> <printf-format> [args]
# The format is a test-controlled printf template, args are the data.
# shellcheck disable=SC2059
pub() { local n=$1 f=$2; shift 2; printf "$f" "$@" >"$WORK/pub/$n"; }
# shellcheck disable=SC2059
newf() { local n=$1 f=$2; shift 2; printf "$f" "$@" >"$WORK/new/$n"; }
nolog() { [ "$(logs)" = 0 ] && echo "ok   - $1: no upload" || { echo "FAIL - $1: upload happened" >&2; fails=$((fails+1)); }; }
CK="checksums.txt checksums.txt.sig unitill-pos-setup-1.0.0.exe"
pub checksums.txt '%s\n%s\n' "$SA" "$SB"
newf checksums.txt '%s\n%s\n%s\n' "$SA" "$SB" "$SC"
check 0 "(a) checksums.txt extended by one line" false "$CK" "$CS"
[ "$(logs)" = 1 ] && grep -q -- "--clobber" "$MOCK_LOG" && echo "ok   - (a) uploaded once with --clobber" || { echo "FAIL - (a) upload" >&2; fails=$((fails+1)); }
newf checksums.txt '%s\n%s\n' "$SA" "$SB"
check 0 "(a2) unchanged checksums.txt (N=0)" false "$CK" "$CS"
newf checksums.txt '%s\n%s\n%s\n' "$SA" "$SBX" "$SC"
check 1 "(b) a published line changed" false "$CK" "$CS"; nolog "(b)"
newf checksums.txt '%s\n%s\n' "$SA" "$SC"
check 1 "(c) a published line removed" false "$CK" "$CS"; nolog "(c)"
newf checksums.txt '%s\n%s\n%s\n' "$SB" "$SA" "$SC"
check 1 "(c2) published lines reordered" false "$CK" "$CS"; nolog "(c2)"
newf checksums.txt '%s\n' "$SA"
check 1 "(c3) truncated: new file is a strict prefix of the published one" false "$CK" "$CS"; nolog "(c3)"
grep -q "published line 2 is changed" "$WORK/out" && echo "ok   - (c3) names one line number" || { echo "FAIL - (c3) message" >&2; cat "$WORK/out" >&2; fails=$((fails+1)); }
newf checksums.txt '%s\n%s\n%s\n' "$SBX" "$SB" "$SC"
check 1 "(c4) changed line reports exactly one line number" false "$CK" "$CS"
grep -q "published line 1 is changed" "$WORK/out" && echo "ok   - (c4) message has a single line number" || { echo "FAIL - (c4) message" >&2; cat "$WORK/out" >&2; fails=$((fails+1)); }
newf checksums.txt '%s\n%s\n%s\n' "$SA" "$SB" "$SDUP"
check 1 "(d) second hash for a published asset" false "$CK" "$CS"; nolog "(d)"
newf checksums.txt '%s\n%s\n%s\n%s\n' "$SA" "$SB" "$SC" "$(printf 'e%.0s' {1..64})  new.dmg"
check 1 "(d1) second hash inside the appended block" false "$CK" "$CS"; nolog "(d1)"
newf checksums.txt '%s\n%s\njunk\n' "$SA" "$SB"
check 1 "(d2) malformed appended line" false "$CK" "$CS"; nolog "(d2)"
# A CR or tab after the name hid a second hash from the duplicate check
# (strings.Fields on the till drops the CR and reads the same asset name).
newf checksums.txt '%s\n%s\n%s\r\n' "$SA" "$SB" "$SDUP"
check 1 "(d3) CRLF-terminated second hash for a published asset" false "$CK" "$CS"; nolog "(d3)"
newf checksums.txt '%s\n%s\n%s\tx\n' "$SA" "$SB" "$SDUP"
check 1 "(d4) tab after a published asset name" false "$CK" "$CS"; nolog "(d4)"
MOCK_DL_FAIL=1 check 1 "(e) published copy cannot be downloaded" false "$CK" "$CS" ; 
newf checksums.txt '%s\n%s\n%s\n' "$SA" "$SB" "$SC"
rm -f "$WORK/pub/checksums.txt"
check 1 "(e2) listed but not downloadable" false "$CK" "$CS"; nolog "(e2)"
pub checksums.txt '%s\n%s\n' "$SA" "$SB"
pub checksums.txt.sig 'S1\n'
newf checksums.txt.sig 'S1\nS2\n'
check 0 "(f) .sig extended" false "$CK" "$SIG"
newf checksums.txt.sig 'S2\n'
check 1 "(f) .sig with the published line dropped" false "$CK" "$SIG"; nolog "(f drop)"
newf checksums.txt.sig 'S1x\nS2\n'
check 1 "(f) .sig with the published line altered" false "$CK" "$SIG"; nolog "(f alter)"
pub checksums.txt '%s\n%s' "$SA" "$SB"
newf checksums.txt '%s\n%s\n%s\n' "$SA" "$SB" "$SC"
check 0 "(f2) published file without trailing newline" false "$CK" "$CS"
# (g) draft: clobbered as before, never downloads
check 0 "(g) draft checksums.txt clobbered" true "$CK" "$CS"
! grep -q "release download" "$MOCK_LOG" && grep -q -- "--clobber" "$MOCK_LOG" && echo "ok   - (g) draft never downloads" || { echo "FAIL - (g)" >&2; fails=$((fails+1)); }
check 1 "published: second file existing aborts the batch" false "unitill-pos-setup-1.0.0.exe" "$WORK/new.dmg" "$EXE"
check 2 "usage error" true "" 
MOCK_VIEW_FAIL=1 check 1 "unreadable release fails closed" true "" "$EXE"
check 1 "missing file" true "" "$WORK/nope"
[ "$fails" = 0 ] || { echo "release-upload_test: $fails failure(s)" >&2; exit 1; }
echo "✓ release-upload_test: all cases passed"
