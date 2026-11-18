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
exit 0
GH
chmod +x "$WORK/bin/gh"
echo x >"$WORK/unitill-pos-setup-1.0.0.exe"; echo x >"$WORK/checksums.txt"; echo x >"$WORK/new.dmg"
export PATH="$WORK/bin:$PATH" MOCK_LOG="$WORK/log"
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
check 0 "published: checksums.txt may be replaced" false "checksums.txt" "$WORK/checksums.txt"
check 1 "published: second file existing aborts the batch" false "unitill-pos-setup-1.0.0.exe" "$WORK/new.dmg" "$EXE"
check 2 "usage error" true "" 
MOCK_VIEW_FAIL=1 check 1 "unreadable release fails closed" true "" "$EXE"
check 1 "missing file" true "" "$WORK/nope"
[ "$fails" = 0 ] || { echo "release-upload_test: $fails failure(s)" >&2; exit 1; }
echo "✓ release-upload_test: all cases passed"
