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
while [ $# -gt 0 ]; do case "$1" in -o) out=$2; shift 2;; -H) case "$2" in @*) hdr=${2#@};; esac; shift 2;; --data-binary) cp "${2#@}" "$MOCK_BODY"; shift 2;; *) shift;; esac; done
echo "$args" >>"$MOCK_LOG"
grep -q 'Authorization: Bearer SECRET-TOKEN-VALUE' "$hdr" || { echo "no auth header" >>"$MOCK_LOG"; }
case "$args" in *"X-Seller-Account-Id: 1234567"*) ;; *) echo "no seller header" >>"$MOCK_LOG";; esac
case "$args" in *blob.core.windows.net*) printf '%s' "${MOCK_BLOB:-blob-bytes}" >"$out"; printf 200; exit 0;; esac
case "$args" in *"/status"*) echo '{"responseData":{"isReady":true}}' >"$out";; *) echo '{}' >"$out";; esac
if [ -n "${MOCK_FAIL_ON:-}" ] && [[ "$args" == *"$MOCK_FAIL_ON"* ]]; then
  [ -n "${MOCK_ERR_BODY+x}" ] && printf '%s' "$MOCK_ERR_BODY" >"$out"
  printf '%s' "${MOCK_FAIL_CODE:-500}"
else printf 200; fi
C
chmod +x "$W/bin/"*
D=6d98e8b6420256639e35f3b5e821e1b564efc124a5c842a9b91f490ea6efd663
export PATH="$W/bin:$PATH" MOCK_LOG="$W/log" MOCK_BODY="$W/body.copy" UT_MSSTORE_TEST=1 MSSTORE_ROOT="$T" REPO=o/r MSSTORE_APP_ID=XP9ABCDEF123 MSSTORE_SELLER_ID=1234567 MSSTORE_TOKEN_FILE="$W/tok" MOCK_RELSTATE="false false" MOCK_DIGEST=$D EXPECT_SHA256=$D MSSTORE_POLL_SECONDS=0
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
MOCK_BLOB=tampered t 1 "refuses a blob that is not the verified installer"
! grep -qE -- '-X (PUT|POST)' "$W/log" && echo "ok   - no write when the blob differs" || { echo "FAIL - wrote a tampered blob" >&2; fails=$((fails+1)); }
t 0 "opened + published + valid URL sets the package (again, for the PUT assertion)"
grep -q -- '-X PUT' "$W/log" && grep -q -- '--data-binary' "$W/log" && echo "ok   - package PUT sent" || { echo "FAIL - no PUT" >&2; fails=$((fails+1)); }
! grep -q '/commit' "$W/log" && ! grep -q '/submit' "$W/log" && echo "ok   - no commit or submit by default" || { echo "FAIL - committed" >&2; fails=$((fails+1)); }
! grep -rq 'SECRET-TOKEN-VALUE' "$W/log" "$W/out" && echo "ok   - token never on a command line or in output" || { echo "FAIL - token leaked" >&2; fails=$((fails+1)); }
COMMIT=true t 0 "COMMIT=true commits and submits"
grep -q '/packages/commit' "$W/log" && grep -q -- '-X POST.*/submit' "$W/log" && echo "ok   - commit and submit sent" || { echo "FAIL - commit/submit" >&2; fails=$((fails+1)); }
EXPECT_SHA256=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb t 1 "refuses when the asset changed since verification"
EXPECT_SHA256="" t 1 "refuses without the verified digest"
[ "$(jq -r '.packages[0].packageUrl' "$W/body.copy" 2>/dev/null)" = "https://unitillinstallers.blob.core.windows.net/installers/windows/v1.2.3/unitill-pos-setup-1.2.3.exe" ] && echo "ok   - PUT body packageUrl is the blob URL" || { echo "FAIL - packageUrl is not the blob URL" >&2; fails=$((fails+1)); }
t 1 "refuses the redirecting GitHub release URL" v1.2.3 "https://github.com/o/r/releases/download/v1.2.3/unitill-pos-setup-1.2.3.exe"
t 1 "refuses a /latest/ URL" v1.2.3 "https://unitillinstallers.blob.core.windows.net/installers/windows/latest/unitill-pos-setup-1.2.3.exe"
t 1 "refuses another storage account" v1.2.3 "https://evil.blob.core.windows.net/installers/windows/v1.2.3/unitill-pos-setup-1.2.3.exe"
t 1 "refuses another container" v1.2.3 "https://unitillinstallers.blob.core.windows.net/other/windows/v1.2.3/unitill-pos-setup-1.2.3.exe"
t 1 "refuses another tag's blob" v1.2.3 "https://unitillinstallers.blob.core.windows.net/installers/windows/v1.2.4/unitill-pos-setup-1.2.3.exe"
t 1 "refuses a bad tag" latest
MOCK_RELSTATE="true false" t 1 "refuses a draft release"
MOCK_RELSTATE="false true" t 1 "refuses a prerelease"
MOCK_FAIL_ON="-X PUT" t 1 "a non-2xx update fails closed"
MOCK_FAIL_ON="/packages/commit" COMMIT=true t 1 "a non-2xx commit fails closed"
MSSTORE_APP_ID="bad id" t 1 "refuses a malformed app id"
MSSTORE_TOKEN_FILE="$W/none" t 1 "refuses a missing token file"
# --- diagnosability: a non-2xx prints the code + a sanitised body, and the token's non-secret claims ---
b64u() { printf '%s' "$1" | base64 | tr -d '=\n' | tr '/+' '_-'; }
JWT="$(b64u '{"alg":"RS256"}').$(b64u '{"aud":"https://api.store.microsoft.com","iss":"https://sts.windows.net/T-1/","tid":"T-1","appid":"APP-1","oid":"OID-1","roles":["R1"],"exp":1893456000,"secretish":"LEAK-ME"}').SIGNATURE-PART-LEAK"
printf '%s' "$JWT" >"$W/jwt"
ERR='{"error":{"code":"Forbidden","message":"seller not linked","correlationId":"abc-123"},"junk":"EXTRA-FIELD-LEAK"}'
MSSTORE_TOKEN_FILE="$W/jwt" MOCK_FAIL_ON="/packages" MOCK_FAIL_CODE=403 MOCK_ERR_BODY="$ERR" t 1 "403 with a JSON error body fails closed"
grep -q 'HTTP 403' "$W/out" && grep -q 'Forbidden' "$W/out" && grep -q 'seller not linked' "$W/out" && grep -q 'abc-123' "$W/out" && echo "ok   - 403 prints code, error code, message, correlation id" || { echo "FAIL - 403 body not shown" >&2; cat "$W/out" >&2; fails=$((fails+1)); }
! grep -q 'EXTRA-FIELD-LEAK' "$W/out" && echo "ok   - only the whitelisted error fields are printed" || { echo "FAIL - unlisted field printed" >&2; fails=$((fails+1)); }
grep -q 'T-1' "$W/out" && grep -q 'APP-1' "$W/out" && grep -q 'OID-1' "$W/out" && grep -q 'api.store.microsoft.com' "$W/out" && grep -q 'R1' "$W/out" && echo "ok   - non-secret token claims printed (aud, tid, appid, oid, roles)" || { echo "FAIL - claims not shown" >&2; cat "$W/out" >&2; fails=$((fails+1)); }
! grep -qF -e "$JWT" -e 'SIGNATURE-PART-LEAK' -e 'LEAK-ME' -e "$(printf '%s' "$JWT" | cut -d. -f2)" "$W/out" && echo "ok   - the token and its unlisted claims are never printed" || { echo "FAIL - token material printed" >&2; fails=$((fails+1)); }
MSSTORE_TOKEN_FILE="$W/jwt" MOCK_FAIL_ON="/packages" MOCK_FAIL_CODE=403 MOCK_ERR_BODY='<html>not json eyJhbGciOi.secret.sig '"$JWT"'</html>' t 1 "403 with a non-JSON body fails closed"
grep -q 'HTTP 403' "$W/out" && ! grep -qF -e "$JWT" -e 'eyJhbGciOi' -e 'SIGNATURE-PART-LEAK' "$W/out" && echo "ok   - non-JSON body is safe (token-shaped text scrubbed)" || { echo "FAIL - non-JSON body unsafe" >&2; cat "$W/out" >&2; fails=$((fails+1)); }
MOCK_FAIL_ON="/packages" MOCK_FAIL_CODE=403 MOCK_ERR_BODY='' t 1 "403 with an empty body fails closed"
grep -q 'HTTP 403' "$W/out" && grep -qi 'empty' "$W/out" && echo "ok   - empty body reported" || { echo "FAIL - empty body" >&2; fails=$((fails+1)); }
MOCK_FAIL_ON="/packages" MOCK_FAIL_CODE=403 MOCK_ERR_BODY="$(head -c 5000 /dev/zero | tr '\0' 'x')" t 1 "a huge body fails closed"
[ "$(wc -c <"$W/out")" -lt 3000 ] && echo "ok   - output is length-capped" || { echo "FAIL - not capped" >&2; fails=$((fails+1)); }
MSSTORE_TOKEN_FILE="$W/jwt" MOCK_FAIL_ON="/packages" MOCK_FAIL_CODE=403 MOCK_ERR_BODY="$(printf '{"message":"split %s"}' "$(cut -d. -f3 "$W/jwt")")" t 1 "a signature echoed alone fails closed"
! grep -q 'SIGNATURE-PART-LEAK' "$W/out" && echo "ok   - echoed signature segment scrubbed" || { echo "FAIL - signature leaked" >&2; fails=$((fails+1)); }
MOCK_FAIL_ON="/packages" MOCK_FAIL_CODE=403 MOCK_ERR_BODY='{"x":1}' t 1 "JSON without error fields fails closed"
grep -q 'without a known error field' "$W/out" && echo "ok   - unknown JSON shape reported" || { echo "FAIL - fallback" >&2; fails=$((fails+1)); }
MOCK_FAIL_ON="/packages" MOCK_FAIL_CODE=403 MOCK_ERR_BODY='{"message":"line1\n::error::injected"}' t 1 "workflow-command injection in a body fails closed"
! grep -q '^::error::injected' "$W/out" && echo "ok   - body cannot inject workflow commands" || { echo "FAIL - injection" >&2; fails=$((fails+1)); }
MOCK_FAIL_ON="/packages" MOCK_FAIL_CODE=403 MOCK_ERR_BODY='{"message":"echo SECRET-TOKEN-VALUE and Authorization: Bearer OTHER-OPAQUE-1"}' t 1 "an opaque token echoed in a body fails closed"
grep -qi 'not a decodable JWT' "$W/out" && ! grep -q 'SECRET-TOKEN-VALUE\|OTHER-OPAQUE-1' "$W/out" && echo "ok   - opaque token handled, echoed token and Bearer value scrubbed" || { echo "FAIL - opaque token" >&2; fails=$((fails+1)); }
DRY_RUN=false MOCK_FAIL_ON="-X PUT" MOCK_FAIL_CODE=403 MOCK_ERR_BODY="$ERR" t 1 "a 403 on the write path still fails closed"
DRY_RUN=maybe t 1 "refuses a DRY_RUN that is not true/false"
[ "$fails" = 0 ] || exit 1
echo "✓ msstore-submit_test: all cases passed"
