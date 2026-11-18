#!/usr/bin/env bash
#
# Self-test for publish-installer.sh (ut-docs#6407) with stubbed gh/az/curl in
# a fresh mktemp dir. Also guards the workflow wiring.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT_DIR}/scripts/ci/publish-installer.sh"
fails=0
pass() { echo "ok   - $1"; }
fail() { echo "FAIL - $1" >&2; fails=$((fails + 1)); }

T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/bin"
printf 'MZ-installer-bytes' >"$T/asset.exe"
printf 'other' >"$T/other.exe"
SHA="$(sha256sum "$T/asset.exe" 2>/dev/null | awk '{print $1}' || shasum -a 256 "$T/asset.exe" | awk '{print $1}')"
SIZE="$(wc -c <"$T/asset.exe" | tr -d ' ')"

cat >"$T/bin/gh" <<'STUB'
#!/usr/bin/env bash
if [ "$1 $2" = "release download" ]; then
  while [ $# -gt 0 ]; do [ "$1" = --dir ] && d="$2"; shift; done
  cp "$ASSET" "$d/unitill-pos-setup-0.36.1.exe"
elif [ "$1" = api ]; then
  echo "${DIGEST}"
fi
STUB
cat >"$T/bin/az" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$AZ_LOG"
[ -z "${AZ_FAIL:-}" ] || { [ "$AZ_FAIL" = exists ] && echo "ERROR: BlobAlreadyExists" >&2 || echo "AuthorizationFailure" >&2; exit 1; }
STUB
cat >"$T/bin/curl" <<'STUB'
#!/usr/bin/env bash
if [ "$1" = -sI ]; then printf '%b' "$HEAD"; exit 0; fi
while [ $# -gt 0 ]; do [ "$1" = -o ] && o="$2"; shift; done
cp "${SERVED:-$ASSET}" "$o"
STUB
printf '#!/usr/bin/env bash\nexit "${SIGN_RC:-0}"\n' >"$T/bin/sign"
chmod +x "$T"/bin/*

# run <tag> [expected] -> RC, OUT
run() {
  : >"$T/az.log"
  set +e
  OUT="$(PATH="$T/bin:$PATH" ASSET="$T/asset.exe" AZ_LOG="$T/az.log" GH_REPO=o/r STORAGE_ACCOUNT=acct \
    SIGN_EXE="$T/bin/sign" BASE_URL=https://acct.blob.core.windows.net/installers \
    bash "$SCRIPT" "$@" 2>&1)"
  RC=$?
  set -e
}
export DIGEST="sha256:$SHA"
export HEAD="HTTP/1.1 200 OK\r\nContent-Length: $SIZE\r\n\r\n"

run v0.36.1
if [ "$RC" = 0 ] && grep -q 'published: https://acct.blob.core.windows.net/installers/windows/v0.36.1/unitill-pos-setup-0.36.1.exe' <<<"$OUT"; then pass "happy path prints the final URL"; else fail "happy path ($RC): $OUT"; fi
if grep -q -- '--auth-mode login' "$T/az.log" && grep -q -- '--overwrite false' "$T/az.log" && ! grep -q -- '--overwrite true' "$T/az.log"; then pass "uploads with AAD login, never overwrite"; else fail "az args: $(cat "$T/az.log")"; fi

DIGEST="sha256:$(printf 'f%.0s' $(seq 64))" run v0.36.1
[ "$RC" != 0 ] && pass "digest mismatch fails" || fail "digest mismatch passed"
[ ! -s "$T/az.log" ] && pass "digest mismatch uploads nothing" || fail "uploaded despite digest mismatch"

SIGN_RC=1 run v0.36.1
[ "$RC" != 0 ] && [ ! -s "$T/az.log" ] && pass "bad signature fails before upload" || fail "bad signature"

AZ_FAIL=1 run v0.36.1
[ "$RC" != 0 ] && pass "other az failure fails" || fail "az failure passed"

AZ_FAIL=exists run v0.36.1
[ "$RC" = 0 ] && pass "existing blob with identical public bytes: re-run succeeds" || fail "re-run on identical blob ($RC): $OUT"

AZ_FAIL=exists SERVED="$T/other.exe" run v0.36.1
[ "$RC" != 0 ] && pass "existing blob with different bytes fails" || fail "different bytes at existing blob passed"

HEAD="HTTP/1.1 404 Not Found\r\nContent-Length: $SIZE\r\n\r\n" run v0.36.1
[ "$RC" != 0 ] && pass "non-200 with matching length fails" || fail "404 passed"

HEAD="HTTP/1.1 302 Found\r\nLocation: https://x\r\n\r\n" run v0.36.1
[ "$RC" != 0 ] && pass "redirect fails" || fail "redirect passed"

HEAD="HTTP/1.1 200 OK\r\nLocation: https://x\r\nContent-Length: $SIZE\r\n\r\n" run v0.36.1
[ "$RC" != 0 ] && pass "200 with Location fails" || fail "200+Location passed"

HEAD="HTTP/1.1 200 OK\r\nContent-Length: 1\r\n\r\n" run v0.36.1
[ "$RC" != 0 ] && pass "Content-Length mismatch fails" || fail "length mismatch passed"

SERVED="$T/other.exe" run v0.36.1
[ "$RC" != 0 ] && pass "served bytes differ fails" || fail "served-bytes mismatch passed"

run v0.36.1 "$(printf '0%.0s' $(seq 64))"
[ "$RC" != 0 ] && pass "expected sha256 mismatch fails" || fail "expected sha mismatch passed"

run '../evil'
[ "$RC" = 2 ] && pass "bad tag rejected" || fail "bad tag accepted"

# Workflow wiring guards.
WF="${ROOT_DIR}/.github/workflows/publish-installer.yml"
grep -q 'environment: release-signing' "$WF" && grep -q 'id-token: write' "$WF" && pass "workflow: environment + OIDC" || fail "workflow env/oidc"
grep -q 'workflow_call' "$WF" && grep -q 'workflow_dispatch' "$WF" && pass "workflow: callable and dispatchable" || fail "workflow triggers"
grep -Eq '(storage-account-key|--account-key|--connection-string|--sas-token)' "$WF" "$SCRIPT" && fail "a storage secret is referenced" || pass "no storage key/SAS/connection string"
grep -q 'publish-installer.yml' "${ROOT_DIR}/.github/workflows/release.yml" && pass "release.yml calls it" || fail "release.yml does not call it"

[ "$fails" = 0 ] || { echo "$fails failure(s)" >&2; exit 1; }
echo "all ok"
