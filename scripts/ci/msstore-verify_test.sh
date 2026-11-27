#!/usr/bin/env bash
# Test for msstore-verify.sh with mocked gh/curl and stubbed signature tools.
# Fixture and `A && ok || fail` assertion style used throughout this test.
# shellcheck disable=SC2015
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
V="${ROOT_DIR}/scripts/ci/msstore-verify.sh"
W="$(mktemp -d)"
# shellcheck disable=SC2317
cleanup() { rm -rf -- "${W:?}"; }
trap cleanup EXIT
mkdir -p "$W/bin" "$W/rel"
printf 'fake-exe-bytes' >"$W/rel/unitill-pos-setup-1.2.3.exe"
SHA="$(shasum -a 256 "$W/rel/unitill-pos-setup-1.2.3.exe" | awk '{print $1}')"
SIZE="$(wc -c <"$W/rel/unitill-pos-setup-1.2.3.exe" | tr -d ' ')"
cat >"$W/bin/curl" <<'C'
#!/usr/bin/env bash
out=""; url=""; head=0
while [ $# -gt 0 ]; do case "$1" in -o) out=$2; shift 2;; -I) head=1; shift;; -*) shift;; *) url=$1; shift;; esac; done
if [ "$head" = 1 ]; then echo "HEAD $url" >>"$MOCK_LOG"; case "$url" in *blob.core.windows.net*) printf '%s' "${MOCK_BLOB_PROBE-200 }";; esac; [ -z "${MOCK_BLOB_RC:-}" ] || exit "$MOCK_BLOB_RC"; exit 0; fi
echo "$url" >>"$MOCK_LOG"
cp "$MOCK_REL/$(basename "$url")" "$out"
C
cat >"$W/bin/gh" <<'G'
#!/usr/bin/env bash
if [ "$2" = view ]; then printf '%s\n' "$MOCK_META"; exit 0; fi
if [ "$2" = download ]; then
  while [ $# -gt 0 ]; do case "$1" in --pattern) p=$2; shift 2;; --dir) d=$2; shift 2;; *) shift;; esac; done
  cp "$MOCK_REL/$p" "$d/$p"; exit 0
fi
G
cat >"$W/ok.sh" <<'S'
#!/usr/bin/env bash
exit 0
S
cat >"$W/fail.sh" <<'S'
#!/usr/bin/env bash
exit 1
S
chmod +x "$W/bin/"* "$W/ok.sh" "$W/fail.sh"
export PATH="$W/bin:$PATH" MOCK_LOG="$W/log" MOCK_REL="$W/rel" REPO=o/r
export SIGN_PUBLISHER="TASK RUNNER TECHNOLOGY LTD" UT_RELEASE_KEYS_HEX=aa
export UT_MSSTORE_TEST=1
cat >"$W/soak.sh" <<'S'
#!/usr/bin/env bash
echo "$*" >"$SOAK_LOG"
S
chmod +x "$W/soak.sh"
export SOAK_LOG="$W/soaklog"
export SIGN_EXE="$W/ok.sh" SOAK_CMD="$W/soak.sh" SILENT_CMD="$W/ok.sh" SIG_VERIFY_CMD="$W/ok.sh"
printf '%s  unitill-pos-setup-1.2.3.exe\n' "$SHA" >"$W/rel/checksums.txt"; echo sig >"$W/rel/checksums.txt.sig"
meta() { printf '{"isDraft":%s,"isPrerelease":%s,"assets":[{"name":"unitill-pos-setup-1.2.3.exe","size":%s,"digest":"sha256:%s"}]}' "${1:-false}" "${2:-false}" "${3:-$SIZE}" "${4:-$SHA}"; }
fails=0
t() { # t <want-rc> <name> <tag> ; env prepared by caller
  rm -rf "$W/out"; local rc=0; bash "$V" "$3" "$W/out" >"$W/o.txt" 2>&1 || rc=$?
  if [ "$rc" = "$1" ]; then echo "ok   - $2"; else echo "FAIL - $2 (rc $rc)" >&2; cat "$W/o.txt" >&2; fails=$((fails+1)); fi
}
verdict() { grep -q "\"ready_to_submit\": $1" "$W/out/result.json" && echo "ok   - verdict $1" || { echo "FAIL - verdict $1" >&2; fails=$((fails+1)); }; }
export INSTALLER_NSI="$W/ok.sh"
export MOCK_META; MOCK_META="$(meta)"
t 0 "a good release is ready" v1.2.3; verdict true
grep -q 'releases/download/v1.2.3/unitill-pos-setup-1.2.3.exe' "$W/log" && ! grep -q latest "$W/log" && echo "ok   - versioned URL, never /latest/" || { echo "FAIL - URL" >&2; fails=$((fails+1)); }
BLOB=https://unitillinstallers.blob.core.windows.net/installers/windows/v1.2.3/unitill-pos-setup-1.2.3.exe
[ "$(cut -d' ' -f1 "$W/soaklog")" = "$BLOB" ] && echo "ok   - the soak runs against the blob URL" || { echo "FAIL - soak URL: $(cat "$W/soaklog")" >&2; fails=$((fails+1)); }
[ "$(cut -d' ' -f2,3 "$W/soaklog")" = "$SHA $SIZE" ] && echo "ok   - soak gets the proved sha256 and size" || { echo "FAIL - soak args" >&2; fails=$((fails+1)); }
[ "$(jq -r .url "$W/out/result.json")" = "$BLOB" ] && echo "ok   - result.json url is the blob URL" || { echo "FAIL - result url" >&2; fails=$((fails+1)); }
t 0 "tag without v is normalised" 1.2.3
MOCK_BLOB_RC=22 t 1 "blob missing (publish-installer not green) is not ready" v1.2.3; verdict false
jq -e '.reason | contains("publish-installer")' "$W/out/result.json" >/dev/null && echo "ok   - reason names publish-installer" || { echo "FAIL - reason" >&2; fails=$((fails+1)); }
MOCK_BLOB_PROBE="302 https://elsewhere/x" t 1 "a redirecting blob is not ready" v1.2.3; verdict false
MOCK_BLOB_PROBE="404 " t 1 "a 404 blob is not ready" v1.2.3
t 1 "bad tag" latest; 
t 1 "bad tag with suffix" v1.2.3-rc1
MOCK_META="$(meta true)" t 1 "draft refused" v1.2.3; verdict false
MOCK_META="$(meta false true)" t 1 "prerelease refused" v1.2.3
MOCK_META="$(meta false false 1)" t 1 "API size mismatch refused" v1.2.3
MOCK_META="$(meta false false "$SIZE" "$(printf 'a%.0s' $(seq 1 64))")" t 1 "API digest mismatch refused" v1.2.3
MOCK_META='{"isDraft":false,"isPrerelease":false,"assets":[]}' t 1 "missing asset refused" v1.2.3
printf '%s  unitill-pos-setup-1.2.3.exe\n' "$(printf 'b%.0s' $(seq 1 64))" >"$W/rel/checksums.txt"
t 1 "checksums.txt mismatch refused" v1.2.3
printf '%s  unitill-pos-setup-1.2.3.exe\n' "$SHA" >"$W/rel/checksums.txt"
UT_RELEASE_KEYS_HEX='' t 1 "unconfigured release keys refused" v1.2.3
SIG_VERIFY_CMD="$W/fail.sh" t 1 "bad checksums signature refused" v1.2.3
SIGN_EXE="$W/fail.sh" t 1 "bad Authenticode refused" v1.2.3
SIGN_PUBLISHER="Someone Else" t 1 "wrong expected signer refused" v1.2.3
SILENT_CMD="$W/fail.sh" t 1 "no silent install refused" v1.2.3
SOAK_CMD="$W/fail.sh" t 1 "soak failure refused" v1.2.3; verdict false
# ut-docs#6636: a test override is honoured only with UT_MSSTORE_TEST=1 and
# otherwise fails closed, naming the variable.
for v in SIGN_EXE SOAK_CMD SILENT_CMD SIG_VERIFY_CMD INSTALLER_NSI; do
  rm -rf "$W/out"; rc=0
  env -u UT_MSSTORE_TEST -u SIGN_EXE -u SOAK_CMD -u SILENT_CMD -u SIG_VERIFY_CMD -u INSTALLER_NSI "$v=$W/ok.sh" bash "$V" v1.2.3 "$W/out" >"$W/o.txt" 2>&1 || rc=$?
  if [ "$rc" = 1 ] && grep -q '"ready_to_submit": false' "$W/out/result.json" && jq -e --arg v "$v" '.reason | contains($v)' "$W/out/result.json" >/dev/null; then
    echo "ok   - $v without UT_MSSTORE_TEST fails closed, named"
  else echo "FAIL - $v without the flag (rc $rc)" >&2; cat "$W/o.txt" >&2; fails=$((fails+1)); fi
done
rm -rf "$W/out"; rc=0
env -u INSTALLER_NSI UT_MSSTORE_TEST= SIGN_EXE="$W/ok.sh" bash "$V" v1.2.3 "$W/out" >/dev/null 2>&1 || rc=$?
[ "$rc" = 1 ] && jq -e '.reason | contains("SIGN_EXE")' "$W/out/result.json" >/dev/null && echo "ok   - empty flag does not enable an override" || { echo "FAIL - empty flag (rc $rc)" >&2; fails=$((fails+1)); }
# No override exported here, so only the "must be 1 or unset" check can refuse it.
rm -rf "$W/out"; rc=0
env -u SIGN_EXE -u SOAK_CMD -u SILENT_CMD -u SIG_VERIFY_CMD -u INSTALLER_NSI UT_MSSTORE_TEST=yes bash "$V" v1.2.3 "$W/out" >"$W/o.txt" 2>&1 || rc=$?
[ "$rc" = 1 ] && jq -e '.reason | contains("must be 1 or unset")' "$W/out/result.json" >/dev/null && echo "ok   - a flag value other than 1 is refused, with no override set" || { echo "FAIL - flag=yes (rc $rc)" >&2; cat "$W/o.txt" >&2; fails=$((fails+1)); }
mkdir -p "$W/out"; echo x >"$W/out/x"; rc=0; bash "$V" v1.2.3 "$W/out" >/dev/null 2>&1 || rc=$?; [ "$rc" = 1 ] && echo "ok   - non-empty outdir refused" || { echo "FAIL - outdir" >&2; fails=$((fails+1)); }
[ "$fails" = 0 ] || exit 1
echo "✓ msstore-verify_test: all cases passed"
