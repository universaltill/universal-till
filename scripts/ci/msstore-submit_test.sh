#!/usr/bin/env bash
# Test for msstore-submit.sh with mocked gh and curl in a throwaway tree.
# Fixture and `A && ok || fail` assertion style used throughout this test.
# shellcheck disable=SC2015
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
W="$(mktemp -d)"
# shellcheck disable=SC2317
cleanup() { rm -rf -- "${W:?}"; }
trap cleanup EXIT
T="$W/tree"; mkdir -p "$T/scripts/ci" "$W/bin"
cp "$ROOT_DIR/scripts/ci/msstore-submit.sh" "$ROOT_DIR/scripts/ci/msstore-submit.endpoints" "$T/scripts/ci/"
printf 'SECRET-TOKEN-VALUE' >"$W/tok"
cat >"$W/bin/gh" <<'G'
#!/usr/bin/env bash
echo "$MOCK_RELSTATE"
G
cat >"$W/bin/curl" <<'C'
#!/usr/bin/env bash
# Records the call (never the header file contents) and answers per MOCK_STATUS.
out=""; hdr=""; args="$*"
while [ $# -gt 0 ]; do case "$1" in -o) out=$2; shift 2;; -H) case "$2" in @*) hdr=${2#@};; esac; shift 2;; *) shift;; esac; done
echo "$args" >>"$MOCK_LOG"
grep -q 'Authorization: Bearer SECRET-TOKEN-VALUE' "$hdr" || { echo "no auth header" >>"$MOCK_LOG"; }
case "$args" in *"-X POST"*submissions) echo '{"id":"sub1"}' >"$out";; *) echo '{}' >"$out";; esac
if [ -n "${MOCK_FAIL_ON:-}" ] && [[ "$args" == *"$MOCK_FAIL_ON"* ]]; then printf 500; else printf 200; fi
C
chmod +x "$W/bin/"*
export PATH="$W/bin:$PATH" MOCK_LOG="$W/log" MSSTORE_ROOT="$T" REPO=o/r MSSTORE_APP_ID=9ABCDEF12345 MSSTORE_TOKEN_FILE="$W/tok" MOCK_RELSTATE="false false"
S="$T/scripts/ci/msstore-submit.sh"
fails=0
t() { # t <want-rc> <name> [args]
  local want=$1 name=$2; shift 2; : >"$MOCK_LOG"; local rc=0
  bash "$S" "${@:-v1.2.3}" >"$W/out" 2>&1 || rc=$?
  if [ "$rc" = "$want" ]; then echo "ok   - $name"; else echo "FAIL - $name (rc $rc)" >&2; cat "$W/out" >&2; fails=$((fails+1)); fi
}
t 1 "refuses while store-release/microsoft.opened is missing"
[ ! -s "$W/log" ] && echo "ok   - no API call while closed" || { echo "FAIL - API called while closed" >&2; fails=$((fails+1)); }
mkdir -p "$T/store-release"; echo "no card" >"$T/store-release/microsoft.opened"
t 1 "refuses an opening file without a card"
echo "opened by ut-docs#6408" >"$T/store-release/microsoft.opened"
t 0 "opened + published + valid URL submits"
grep -q -- '-X PUT' "$W/log" && grep -q 'X64\|--data-binary' "$W/log" && echo "ok   - package PUT sent" || { echo "FAIL - no PUT" >&2; fails=$((fails+1)); }
! grep -q -- '/commit' "$W/log" && echo "ok   - no commit by default" || { echo "FAIL - committed" >&2; fails=$((fails+1)); }
! grep -rq 'SECRET-TOKEN-VALUE' "$W/log" "$W/out" && echo "ok   - token never on a command line or in output" || { echo "FAIL - token leaked" >&2; fails=$((fails+1)); }
COMMIT=true t 0 "COMMIT=true commits"
grep -q '/commit' "$W/log" && echo "ok   - commit call sent" || { echo "FAIL - commit" >&2; fails=$((fails+1)); }
t 1 "refuses a /latest/ URL" v1.2.3 "https://github.com/o/r/releases/latest/download/unitill-pos-setup-1.2.3.exe"
t 1 "refuses another repo's URL" v1.2.3 "https://github.com/evil/r/releases/download/v1.2.3/unitill-pos-setup-1.2.3.exe"
t 1 "refuses a bad tag" latest
MOCK_RELSTATE="true false" t 1 "refuses a draft release"
MOCK_RELSTATE="false true" t 1 "refuses a prerelease"
MOCK_FAIL_ON="-X PUT" t 1 "a non-2xx update fails closed"
MOCK_FAIL_ON="-X POST" t 1 "a non-2xx create fails closed"
MSSTORE_APP_ID="bad id" t 1 "refuses a malformed app id"
MSSTORE_TOKEN_FILE="$W/none" t 1 "refuses a missing token file"
[ "$fails" = 0 ] || exit 1
echo "✓ msstore-submit_test: all cases passed"
