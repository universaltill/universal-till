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
# shellcheck disable=SC2016 # the stub expands SIGN_RC when it runs
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
if [ "$RC" != 0 ]; then pass "digest mismatch fails"; else fail "digest mismatch passed"; fi
if [ ! -s "$T/az.log" ]; then pass "digest mismatch uploads nothing"; else fail "uploaded despite digest mismatch"; fi

SIGN_RC=1 run v0.36.1
if [ "$RC" != 0 ] && [ ! -s "$T/az.log" ]; then pass "bad signature fails before upload"; else fail "bad signature"; fi

AZ_FAIL=1 run v0.36.1
if [ "$RC" != 0 ]; then pass "other az failure fails"; else fail "az failure passed"; fi

AZ_FAIL=exists run v0.36.1
if [ "$RC" = 0 ]; then pass "existing blob with identical public bytes: re-run succeeds"; else fail "re-run on identical blob ($RC): $OUT"; fi

AZ_FAIL=exists SERVED="$T/other.exe" run v0.36.1
if [ "$RC" != 0 ]; then pass "existing blob with different bytes fails"; else fail "different bytes at existing blob passed"; fi

HEAD="HTTP/1.1 404 Not Found\r\nContent-Length: $SIZE\r\n\r\n" run v0.36.1
if [ "$RC" != 0 ]; then pass "non-200 with matching length fails"; else fail "404 passed"; fi

HEAD="HTTP/1.1 302 Found\r\nLocation: https://x\r\n\r\n" run v0.36.1
if [ "$RC" != 0 ]; then pass "redirect fails"; else fail "redirect passed"; fi

HEAD="HTTP/1.1 200 OK\r\nLocation: https://x\r\nContent-Length: $SIZE\r\n\r\n" run v0.36.1
if [ "$RC" != 0 ]; then pass "200 with Location fails"; else fail "200+Location passed"; fi

HEAD="HTTP/1.1 200 OK\r\nContent-Length: 1\r\n\r\n" run v0.36.1
if [ "$RC" != 0 ]; then pass "Content-Length mismatch fails"; else fail "length mismatch passed"; fi

SERVED="$T/other.exe" run v0.36.1
if [ "$RC" != 0 ]; then pass "served bytes differ fails"; else fail "served-bytes mismatch passed"; fi

run v0.36.1 "$(printf '0%.0s' $(seq 64))"
if [ "$RC" != 0 ]; then pass "expected sha256 mismatch fails"; else fail "expected sha mismatch passed"; fi

run '../evil'
if [ "$RC" = 2 ]; then pass "bad tag rejected"; else fail "bad tag accepted"; fi

# Workflow wiring guards.
WF="${ROOT_DIR}/.github/workflows/publish-installer.yml"
if grep -q 'environment: release-signing' "$WF" && grep -q 'id-token: write' "$WF"; then pass "workflow: environment + OIDC"; else fail "workflow env/oidc"; fi
if grep -q 'workflow_call' "$WF" && grep -q 'workflow_dispatch' "$WF"; then pass "workflow: callable and dispatchable"; else fail "workflow triggers"; fi
if grep -Eq '(storage-account-key|--account-key|--connection-string|--sas-token)' "$WF" "$SCRIPT"; then fail "a storage secret is referenced"; else pass "no storage key/SAS/connection string"; fi
if grep -q 'publish-installer.yml' "${ROOT_DIR}/.github/workflows/release.yml"; then pass "release.yml calls it"; else fail "release.yml does not call it"; fi

[ "$fails" = 0 ] || { echo "$fails failure(s)" >&2; exit 1; }
echo "all ok"
