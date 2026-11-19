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
case "$*" in *assets*) echo "sha256:${MOCK_DIGEST}";; *) echo "$MOCK_RELSTATE";; esac
G
cat >"$W/bin/curl" <<'C'
#!/usr/bin/env bash
# Records the call (never the header file contents) and answers per MOCK_STATUS.
out=""; hdr=""; args="$*"
while [ $# -gt 0 ]; do case "$1" in -o) out=$2; shift 2;; -H) case "$2" in @*) hdr=${2#@};; esac; shift 2;; *) shift;; esac; done
echo "$args" >>"$MOCK_LOG"
grep -q 'Authorization: Bearer SECRET-TOKEN-VALUE' "$hdr" || { echo "no auth header" >>"$MOCK_LOG"; }
case "$args" in *"X-Seller-Account-Id: 1234567"*) ;; *) echo "no seller header" >>"$MOCK_LOG";; esac
case "$args" in *"/status"*) echo '{"responseData":{"isReady":true}}' >"$out";; *) echo '{}' >"$out";; esac
if [ -n "${MOCK_FAIL_ON:-}" ] && [[ "$args" == *"$MOCK_FAIL_ON"* ]]; then printf 500; else printf 200; fi
C
chmod +x "$W/bin/"*
D=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
export PATH="$W/bin:$PATH" MOCK_LOG="$W/log" UT_MSSTORE_TEST=1 MSSTORE_ROOT="$T" REPO=o/r MSSTORE_APP_ID=XP9ABCDEF123 MSSTORE_SELLER_ID=1234567 MSSTORE_TOKEN_FILE="$W/tok" MOCK_RELSTATE="false false" MOCK_DIGEST=$D EXPECT_SHA256=$D MSSTORE_POLL_SECONDS=0
S="$T/scripts/ci/msstore-submit.sh"
fails=0
t() { # t <want-rc> <name> [args]
  local want=$1 name=$2; shift 2; : >"$MOCK_LOG"; local rc=0
  bash "$S" "${@:-v1.2.3}" >"$W/out" 2>&1 || rc=$?
  if [ "$rc" = "$want" ]; then echo "ok   - $name"; else echo "FAIL - $name (rc $rc)" >&2; cat "$W/out" >&2; fails=$((fails+1)); fi
}
export DRY_RUN=true
UT_MSSTORE_TEST='' t 1 "MSSTORE_ROOT without UT_MSSTORE_TEST fails closed"
! grep -q . "$W/log" && grep -q 'MSSTORE_ROOT' "$W/out" && echo "ok   - refused before any API call, variable named" || { echo "FAIL - MSSTORE_ROOT gate" >&2; fails=$((fails+1)); }
UT_MSSTORE_TEST=yes t 1 "MSSTORE_ROOT with UT_MSSTORE_TEST=yes (not 1) fails closed"
! grep -q . "$W/log" && grep -q 'MSSTORE_ROOT' "$W/out" && echo "ok   - flag=yes refused before any API call, variable named" || { echo "FAIL - flag=yes gate" >&2; fails=$((fails+1)); }
t 0 "dry run probes without an opening file"
grep -q -- '-X GET' "$W/log" && echo "ok   - probe reads" || { echo "FAIL - no probe" >&2; fails=$((fails+1)); }
! grep -qE -- '-X (PUT|POST|PATCH)|--data-binary' "$W/log" && echo "ok   - dry run never writes" || { echo "FAIL - dry run wrote" >&2; fails=$((fails+1)); }
grep -q 'api.store.microsoft.com/submission/v1/product/XP9ABCDEF123/packages' "$W/log" && echo "ok   - EXE API path" || { echo "FAIL - wrong API path" >&2; fails=$((fails+1)); }
! grep -q 'no seller header\|no auth header' "$W/log" && echo "ok   - seller + auth headers" || { echo "FAIL - headers" >&2; fails=$((fails+1)); }
! grep -rq 'manage.devcenter' "$W/log" && echo "ok   - not the MSIX API" || { echo "FAIL - MSIX API used" >&2; fails=$((fails+1)); }
MSSTORE_SELLER_ID="x" t 1 "refuses a malformed seller id"
MOCK_FAIL_ON="/packages" t 1 "a failing probe fails closed"
export DRY_RUN=false
t 1 "refuses a write while store-release/microsoft.opened is missing"
! grep -qE -- '-X (PUT|POST)' "$W/log" && echo "ok   - no write while closed" || { echo "FAIL - wrote while closed" >&2; fails=$((fails+1)); }
mkdir -p "$T/store-release"; echo "no card" >"$T/store-release/microsoft.opened"
t 1 "refuses an opening file without a card"
echo "opened by ut-docs#6408" >"$T/store-release/microsoft.opened"
t 0 "opened + published + valid URL sets the package"
grep -q -- '-X PUT' "$W/log" && grep -q -- '--data-binary' "$W/log" && echo "ok   - package PUT sent" || { echo "FAIL - no PUT" >&2; fails=$((fails+1)); }
! grep -q '/commit' "$W/log" && ! grep -q '/submit' "$W/log" && echo "ok   - no commit or submit by default" || { echo "FAIL - committed" >&2; fails=$((fails+1)); }
! grep -rq 'SECRET-TOKEN-VALUE' "$W/log" "$W/out" && echo "ok   - token never on a command line or in output" || { echo "FAIL - token leaked" >&2; fails=$((fails+1)); }
COMMIT=true t 0 "COMMIT=true commits and submits"
grep -q '/packages/commit' "$W/log" && grep -q -- '-X POST.*/submit' "$W/log" && echo "ok   - commit and submit sent" || { echo "FAIL - commit/submit" >&2; fails=$((fails+1)); }
EXPECT_SHA256=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb t 1 "refuses when the asset changed since verification"
EXPECT_SHA256="" t 1 "refuses without the verified digest"
t 1 "refuses a /latest/ URL" v1.2.3 "https://github.com/o/r/releases/latest/download/unitill-pos-setup-1.2.3.exe"
t 1 "refuses another repo's URL" v1.2.3 "https://github.com/evil/r/releases/download/v1.2.3/unitill-pos-setup-1.2.3.exe"
t 1 "refuses a bad tag" latest
MOCK_RELSTATE="true false" t 1 "refuses a draft release"
MOCK_RELSTATE="false true" t 1 "refuses a prerelease"
MOCK_FAIL_ON="-X PUT" t 1 "a non-2xx update fails closed"
MOCK_FAIL_ON="/packages/commit" COMMIT=true t 1 "a non-2xx commit fails closed"
MSSTORE_APP_ID="bad id" t 1 "refuses a malformed app id"
MSSTORE_TOKEN_FILE="$W/none" t 1 "refuses a missing token file"
DRY_RUN=maybe t 1 "refuses a DRY_RUN that is not true/false"
[ "$fails" = 0 ] || exit 1
echo "✓ msstore-submit_test: all cases passed"
