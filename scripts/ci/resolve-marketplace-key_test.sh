#!/usr/bin/env bash
#
# Self-test for resolve-marketplace-key.sh (ADR-0158, ut-docs#4292): the key
# compiled into every release build. Behaviour scenarios run the script with
# a stub `curl` first on PATH (fixtures in a fresh mktemp -d, never this
# checkout); wiring checks then pin that every release-ish build resolves the
# key and passes it to the linker, and that release verification checks it.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

SCRIPT="${ROOT_DIR}/scripts/ci/resolve-marketplace-key.sh"
fails=0
pass() { echo "ok   - $1"; }
fail() { echo "FAIL - $1" >&2; fails=$((fails + 1)); }

T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT

KEY_A="$(printf 'a%.0s' $(seq 64))"
KEY_B="$(printf 'b%.0s' $(seq 64))"

# Stub curl: STUB_CURL=ok:<hex> | fail | garbage. Records its argv.
mkdir -p "$T/bin"
cat >"$T/bin/curl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$@" >"$STUB_ARGS"
case "$STUB_CURL" in
  ok:*) printf '{"data":{"algorithm":"ed25519","public_key_hex":"%s"},"error":null}' "${STUB_CURL#ok:}" ;;
  garbage) printf '<html>captive portal</html>' ;;
  *) echo "curl: (6) Could not resolve host" >&2; exit 6 ;;
esac
EOF
chmod +x "$T/bin/curl"

# run <pin> <stub-mode> [github_env]  -> OUT (stdout), ERR (stderr), RC
run() {
  local genv="${3:-}"
  set +e
  OUT="$(env PATH="$T/bin:$PATH" STUB_CURL="$2" STUB_ARGS="$T/args" \
    UT_MARKETPLACE_PUBLIC_KEY_PIN="$1" GITHUB_ENV="$genv" \
    bash "$SCRIPT" 2>"$T/err")"
  RC=$?
  set -e
  ERR="$(cat "$T/err")"
}

# (a) pin + live agree -> pin, no warning/error.
run "$KEY_A" "ok:$KEY_A"
if [ "$RC" = 0 ] && [ "$OUT" = "$KEY_A" ] && ! grep -q '::' <<<"$ERR"; then pass "pin matches live: key printed, no annotation"; else fail "pin matches live (rc=$RC out=$OUT err=$ERR)"; fi

# (b) pin + live differ -> ::error::, exit 1.
run "$KEY_A" "ok:$KEY_B"
if [ "$RC" != 0 ] && grep -q '::error::.*mismatch' <<<"$ERR" && [ -z "$OUT" ]; then pass "pin differs from live: fails with ::error::"; else fail "pin differs from live (rc=$RC out=$OUT err=$ERR)"; fi

# (c) pin + live unreachable -> ::warning::, pin used.
run "$KEY_A" "fail"
if [ "$RC" = 0 ] && [ "$OUT" = "$KEY_A" ] && grep -q '::warning::' <<<"$ERR"; then pass "pin + live unreachable: warns, uses pin"; else fail "pin + live unreachable (rc=$RC out=$OUT err=$ERR)"; fi

# (c2) pin + live answers garbage -> treated as unreachable.
run "$KEY_A" "garbage"
if [ "$RC" = 0 ] && [ "$OUT" = "$KEY_A" ] && grep -q '::warning::' <<<"$ERR"; then pass "pin + live garbage: warns, uses pin"; else fail "pin + live garbage (rc=$RC out=$OUT err=$ERR)"; fi

# (d) no pin + live ok -> ::warning:: asking to pin, live used.
run "" "ok:$KEY_B"
if [ "$RC" = 0 ] && [ "$OUT" = "$KEY_B" ] && grep -q '::warning::.*UT_MARKETPLACE_PUBLIC_KEY' <<<"$ERR"; then pass "no pin: uses live key with a pin-me warning"; else fail "no pin + live ok (rc=$RC out=$OUT err=$ERR)"; fi

# (e) no pin + live fails -> ::error::, exit 1.
run "" "fail"
if [ "$RC" != 0 ] && grep -q '::error::' <<<"$ERR" && [ -z "$OUT" ]; then pass "no pin + live unreachable: fails"; else fail "no pin + live unreachable (rc=$RC out=$OUT err=$ERR)"; fi

# (e2) no pin + live returns an invalid key -> fails.
run "" "ok:ABCD"
if [ "$RC" != 0 ] && grep -q '::error::' <<<"$ERR"; then pass "no pin + invalid live key: fails"; else fail "no pin + invalid live key (rc=$RC out=$OUT err=$ERR)"; fi

# (f) invalid pin (uppercase / short) -> fails before trusting it.
run "${KEY_A^^}" "ok:$KEY_A"
if [ "$RC" != 0 ] && grep -q '::error::' <<<"$ERR"; then pass "invalid pin: fails"; else fail "invalid pin (rc=$RC out=$OUT err=$ERR)"; fi

# (g) GITHUB_ENV set -> appended there, nothing on stdout.
: >"$T/genv"
run "$KEY_A" "ok:$KEY_A" "$T/genv"
if [ "$RC" = 0 ] && [ -z "$OUT" ] && grep -qx "UT_MARKETPLACE_KEY_HEX=$KEY_A" "$T/genv"; then pass "GITHUB_ENV: key exported"; else fail "GITHUB_ENV export (rc=$RC out=$OUT genv=$(cat "$T/genv"))"; fi

# (h) curl is called over verified https with timeouts and retries.
run "$KEY_A" "ok:$KEY_A"
args="$(cat "$T/args")"
if ! grep -qxE -- '-k|--insecure' <<<"$args" && grep -qx -- '=https' <<<"$args" \
  && grep -qx -- '--max-time' <<<"$args" && grep -qx -- '--retry' <<<"$args"; then
  pass "curl: verified TLS, https only, bounded, retried"
else
  fail "curl flags: $args"
fi

# --- wiring --------------------------------------------------------------

LDX='internal/buildinfo.MarketplacePublicKey='

# Every goreleaser build injects the key.
builds="$(grep -c -- '- id: ' <(sed -n '/^builds:/,/^[a-z]/p' .goreleaser.yaml))"
keyed="$(grep -c -- "${LDX}{{ .Env.UT_MARKETPLACE_KEY_HEX }}" .goreleaser.yaml || true)"
if [ "$builds" -gt 0 ] && [ "$builds" = "$keyed" ]; then pass ".goreleaser.yaml: all $builds builds inject the key"; else fail ".goreleaser.yaml: $keyed of $builds builds inject the key"; fi

# Every workflow line that stamps buildinfo.Version also stamps the key.
for wf in .github/workflows/release.yml .github/workflows/nightly.yml .github/workflows/ios-testflight.yml; do
  # Literal ${ in the pattern: matches the workflow's own shell expansion.
  # shellcheck disable=SC2016
  bad="$(grep -n 'buildinfo.Version=\${' "$wf" | grep -v "$LDX" || true)"
  if [ -z "$bad" ]; then pass "$wf: every buildinfo.Version stamp also stamps the key"; else fail "$wf: Version stamped without the key: $bad"; fi
  # Literal ${{ }} in the pattern: a GitHub expression, not shell.
  # shellcheck disable=SC2016
  if grep -q 'scripts/ci/resolve-marketplace-key.sh' "$wf" && grep -q 'UT_MARKETPLACE_PUBLIC_KEY_PIN: \${{ vars.UT_MARKETPLACE_PUBLIC_KEY }}' "$wf"; then
    pass "$wf: resolves the key from the pinned repository variable"
  else
    fail "$wf: no resolve-marketplace-key.sh step fed by vars.UT_MARKETPLACE_PUBLIC_KEY"
  fi
  # A fork's own marketplace URL reaches every resolve step (ADR-0158 §2).
  pins="$(grep -c 'UT_MARKETPLACE_PUBLIC_KEY_PIN:' "$wf" || true)"
  # shellcheck disable=SC2016
  urls="$(grep -c 'UT_MARKETPLACE_SIGNING_KEY_URL: \${{ vars.UT_MARKETPLACE_SIGNING_KEY_URL }}' "$wf" || true)"
  if [ "$pins" = "$urls" ]; then pass "$wf: every resolve step passes vars.UT_MARKETPLACE_SIGNING_KEY_URL"; else fail "$wf: $urls of $pins resolve steps pass vars.UT_MARKETPLACE_SIGNING_KEY_URL"; fi
done

# Each job that builds must resolve the key first: count build jobs vs resolve steps.
for wf in .github/workflows/release.yml .github/workflows/nightly.yml .github/workflows/ios-testflight.yml; do
  missing="$(awk '
    /^  [A-Za-z0-9_-]+:$/ { if (job != "" && built && !resolved) print job; job=$1; built=0; resolved=0; next }
    /resolve-marketplace-key\.sh/ { resolved=1 }
    /buildinfo\.Version=/ || /gradlew assemble/ || /goreleaser-action@/ || /build-app\.sh/ { built=1 }
    END { if (job != "" && built && !resolved) print job }
  ' "$wf")"
  if [ -z "$missing" ]; then pass "$wf: every building job resolves the key"; else fail "$wf: jobs build without resolving the key: $missing"; fi
done

# Android: gradle appends the -X only when the env var is non-empty.
if grep -q 'UT_MARKETPLACE_KEY_HEX' android/app/build.gradle.kts && grep -q "$LDX" android/app/build.gradle.kts; then pass "android/app/build.gradle.kts injects the key"; else fail "android/app/build.gradle.kts does not inject the key"; fi
if grep -q "$LDX" packaging/macos/build-app.sh; then pass "packaging/macos/build-app.sh injects the key"; else fail "packaging/macos/build-app.sh does not inject the key"; fi

# Release verification refuses a binary / .so without the key.
if grep -q 'MarketplacePublicKey=\[0-9a-f\]{64}' .github/workflows/release.yml; then pass "release.yml verifies the compiled key"; else fail "release.yml does not verify the compiled key"; fi

if [ "$fails" -ne 0 ]; then
  echo "resolve-marketplace-key_test: $fails failure(s)" >&2
  exit 1
fi
echo "resolve-marketplace-key_test: all passed"
