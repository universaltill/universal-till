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
out=""; url=""
while [ $# -gt 0 ]; do case "$1" in -o) out=$2; shift 2;; -*) shift;; *) url=$1; shift;; esac; done
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
export SIGN_EXE="$W/ok.sh" SOAK_CMD="$W/ok.sh" SILENT_CMD="$W/ok.sh" SIG_VERIFY_CMD="$W/ok.sh"
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
t 0 "tag without v is normalised" 1.2.3
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
mkdir -p "$W/out"; echo x >"$W/out/x"; rc=0; bash "$V" v1.2.3 "$W/out" >/dev/null 2>&1 || rc=$?; [ "$rc" = 1 ] && echo "ok   - non-empty outdir refused" || { echo "FAIL - outdir" >&2; fails=$((fails+1)); }
[ "$fails" = 0 ] || exit 1
echo "✓ msstore-verify_test: all cases passed"
