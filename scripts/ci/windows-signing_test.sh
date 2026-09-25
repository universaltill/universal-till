#!/usr/bin/env bash
#
# Regression test for Windows code signing (ut-docs#2480, ut-docs#2610):
#
#   1. Wiring: the portable zip's binaries are signed by .goreleaser.yaml's
#      post-build hooks (so checksums.txt covers the signed files), the
#      goreleaser job turns those hooks on and prepares signing, and the
#      installer build passes the signer to makensis for uninstall.exe.
#   2. packaging/windows/sign-exe.sh fails closed (missing inputs, unsigned
#      file, wrong publisher, no timestamp, untrusted root) and passes a
#      correctly signed + timestamped file. Real Artifact Signing needs the
#      release identity, so `java` (jsign) is stubbed by an osslsigncode
#      signer with a throwaway test CA and osslsigncode's built-in TSA.
#   3. installer.nsi's `!uninstfinalize` runs the signer on the uninstaller
#      and aborts makensis when signing fails.
#
# (2) needs osslsigncode + openssl + go, (3) needs makensis. Missing tools
# skip those parts locally; CI sets UT_REQUIRE_SIGNING_TOOLS=1 so a missing
# tool fails instead of silently skipping.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

SIGN="${ROOT_DIR}/packaging/windows/sign-exe.sh"
fails=0
pass() { echo "ok   - $1"; }
fail() { echo "FAIL - $1" >&2; fails=$((fails + 1)); }

# --- 1. wiring --------------------------------------------------------------

# The literal hook line, $-expressions included (they are for sh, not us).
# shellcheck disable=SC2016
hook_count="$(grep -cF 'if [ "${UT_WINDOWS_SIGN:-}" = 1 ]; then packaging/windows/sign-exe.sh "{{ .Path }}"; fi' .goreleaser.yaml || true)"
if [ "$hook_count" = 2 ]; then
  pass "both windows goreleaser builds carry the signing hook"
else
  fail ".goreleaser.yaml: expected the signing post-hook on the windows AND desktop-windows builds, found ${hook_count}"
fi

# The goreleaser job block, up to the next top-level job.
gr_job="$(awk '/^  goreleaser:$/{on=1; print; next} on && /^  [a-z][a-z0-9-]*:$/{exit} on{print}' .github/workflows/release.yml)"
for want in 'environment: release-signing' 'id-token: write' 'packaging/windows/setup-signing.sh' 'UT_WINDOWS_SIGN: "1"' 'sign-exe.sh --verify-only'; do
  if grep -qF -- "$want" <<<"$gr_job"; then
    pass "goreleaser job: ${want}"
  else
    fail "release.yml goreleaser job is missing: ${want}"
  fi
done
if grep -qE 'go test' <<<"$gr_job"; then
  fail "release.yml goreleaser job runs go test — tests must not run in the job that can mint the signing token"
else
  pass "goreleaser job runs no tests"
fi
if grep -E '^\s*(- )?uses:' <<<"$gr_job" | grep -vqE '@[0-9a-f]{40}( |$)'; then
  fail "release.yml goreleaser job has an action not pinned to a commit SHA (it holds id-token: write)"
else
  pass "goreleaser job actions are SHA-pinned"
fi
if grep -qF -- '-DUNINST_SIGN_CMD=' .github/workflows/release.yml; then
  pass "makensis gets the uninstaller signer"
else
  fail "release.yml: makensis is not passed -DUNINST_SIGN_CMD (uninstall.exe would ship unsigned)"
fi
if grep -qE '^\s*!uninstfinalize .*UNINST_SIGN_CMD.*= 0' packaging/windows/installer.nsi; then
  pass "installer.nsi signs the uninstaller and checks the result"
else
  fail "installer.nsi: no '!uninstfinalize ... = 0' using UNINST_SIGN_CMD"
fi

# --- 2 + 3. behaviour ---------------------------------------------------------

have() { command -v "$1" >/dev/null 2>&1; }
missing=()
for t in osslsigncode openssl go; do have "$t" || missing+=("$t"); done
if [ "${#missing[@]}" -gt 0 ]; then
  if [ "${UT_REQUIRE_SIGNING_TOOLS:-}" = 1 ]; then
    fail "missing tools: ${missing[*]}"
  else
    echo "skip - sign-exe.sh behaviour (missing: ${missing[*]})"
  fi
  [ "$fails" -eq 0 ] || exit 1
  exit 0
fi

T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT

# Throwaway PKI: root CA, a code-signing leaf, a timestamping leaf.
pki() {
  cd "$T"
  openssl req -x509 -newkey rsa:2048 -nodes -keyout ca.key -out ca.pem -days 2 \
    -subj "/CN=UT Test Root" -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" 2>/dev/null
  openssl req -x509 -newkey rsa:2048 -nodes -keyout other-ca.key -out other-ca.pem -days 2 \
    -subj "/CN=Someone Else Root" -addext "basicConstraints=critical,CA:TRUE" 2>/dev/null
  openssl req -newkey rsa:2048 -nodes -keyout leaf.key -out leaf.csr \
    -subj "/CN=UT TEST SIGNER LTD/O=UT TEST SIGNER LTD" 2>/dev/null
  printf 'basicConstraints=CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=codeSigning\n' > leaf.ext
  openssl x509 -req -in leaf.csr -CA ca.pem -CAkey ca.key -CAcreateserial -out leaf.pem -days 2 -extfile leaf.ext 2>/dev/null
  openssl req -newkey rsa:2048 -nodes -keyout tsa.key -out tsa.csr -subj "/CN=UT Test TSA" 2>/dev/null
  printf 'basicConstraints=CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=critical,timeStamping\n' > tsa.ext
  openssl x509 -req -in tsa.csr -CA ca.pem -CAkey ca.key -CAcreateserial -out tsa.pem -days 2 -extfile tsa.ext 2>/dev/null
  cat ca.pem > bundle.pem
  cd "${ROOT_DIR}"
}
pki

# A real (tiny) PE to sign.
mkdir -p "$T/pe"
printf 'package main\nfunc main() {}\n' > "$T/pe/main.go"
(cd "$T/pe" && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 GOFLAGS="" go build -o "$T/app.exe" main.go)

# Stub jsign: `java -jar <jar> ... --storepass file:<path> ... <file>` →
# osslsigncode with the test leaf (+ built-in TSA unless STUB_NO_TSA=1).
mkdir -p "$T/bin"
cat > "$T/bin/java" <<EOF
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "\$*" >> "$T/java-argv.log"
[ "\${STUB_FAIL:-}" = 1 ] && exit 1
pass=""; prev=""
for a in "\$@"; do [ "\$prev" = --storepass ] && pass=\$a; prev=\$a; done
case "\$pass" in file:*) test -s "\${pass#file:}" ;; *) echo "stub: storepass must be file:" >&2; exit 1 ;; esac
f="\${!#}"
tsa=(-TSA-certs "$T/tsa.pem" -TSA-key "$T/tsa.key")
[ "\${STUB_NO_TSA:-}" = 1 ] && tsa=()
osslsigncode sign -certs "$T/leaf.pem" -key "$T/leaf.key" "\${tsa[@]}" -in "\$f" -out "\$f.signed" >/dev/null
mv "\$f.signed" "\$f"
EOF
chmod +x "$T/bin/java"

TOKEN_VALUE="not-a-real-token-$$"
printf '%s' "$TOKEN_VALUE" > "$T/token"
run_sign() { # run_sign <extra env...> -- <args...>
  local envs=()
  while [ "$1" != -- ]; do envs+=("$1"); shift; done
  shift
  env PATH="$T/bin:$PATH" JSIGN_JAR="$T/jsign.jar" SIGN_ENDPOINT=example.invalid SIGN_ALIAS=acct/profile \
    SIGN_TOKEN_FILE="$T/token" SIGN_PUBLISHER="UT TEST SIGNER LTD" SIGN_CA_BUNDLE="$T/bundle.pem" \
    "${envs[@]}" "$SIGN" "$@" >"$T/out.log" 2>&1
}
fresh() { cp "$T/app.exe" "$T/$1"; echo "$T/$1"; }

f="$(fresh unsigned.exe)"
if run_sign -- --verify-only "$f"; then fail "verify-only accepted an unsigned file"; else pass "unsigned file is rejected"; fi

f="$(fresh a.exe)"
if run_sign SIGN_TOKEN_FILE= -- "$f"; then fail "signed with SIGN_TOKEN_FILE unset"; else pass "missing SIGN_TOKEN_FILE fails closed"; fi
: > "$T/empty-token"
if run_sign SIGN_TOKEN_FILE="$T/empty-token" -- "$f"; then fail "signed with an empty token file"; else pass "empty token file fails closed"; fi
if run_sign SIGN_PUBLISHER= -- "$f"; then fail "ran with SIGN_PUBLISHER unset"; else pass "missing SIGN_PUBLISHER fails closed"; fi

f="$(fresh good.exe)"
if run_sign -- "$f"; then pass "sign + verify succeeds"; else fail "sign + verify failed: $(cat "$T/out.log")"; fi
if run_sign -- --verify-only "$f"; then pass "signed file passes --verify-only"; else fail "signed file failed --verify-only: $(cat "$T/out.log")"; fi
if run_sign SIGN_PUBLISHER="SOMEONE ELSE LTD" -- --verify-only "$f"; then fail "accepted the wrong publisher"; else pass "wrong publisher is rejected"; fi
if run_sign SIGN_CA_BUNDLE="$T/other-ca.pem" -- --verify-only "$f"; then fail "accepted a signature from an untrusted root"; else pass "untrusted root is rejected"; fi
if grep -qF "$TOKEN_VALUE" "$T/java-argv.log"; then fail "the token appeared on jsign's command line"; else pass "token never on the command line"; fi

f="$(fresh nots.exe)"
if run_sign STUB_NO_TSA=1 -- "$f"; then fail "accepted a signature without a timestamp"; else pass "missing timestamp is rejected"; fi

f="$(fresh jsignfail.exe)"
if run_sign STUB_FAIL=1 -- "$f"; then fail "succeeded although jsign failed"; else pass "jsign failure fails closed"; fi

# --- 3. installer.nsi uninstaller hook ---------------------------------------

if ! have makensis; then
  if [ "${UT_REQUIRE_SIGNING_TOOLS:-}" = 1 ]; then
    fail "missing tool: makensis"
  else
    echo "skip - installer.nsi uninstaller signing (makensis missing)"
  fi
else
  mkdir -p "$T/nsis/stage"
  cp LICENSE "$T/nsis/stage/"
  cp "$T/app.exe" "$T/nsis/stage/unitill-pos.exe"
  cp packaging/windows/installer.nsi "$T/nsis/"
  : > "$T/java-argv.log"
  mk() {
    (cd "$T/nsis" && env PATH="$T/bin:$PATH" JSIGN_JAR="$T/jsign.jar" SIGN_ENDPOINT=example.invalid \
      SIGN_ALIAS=acct/profile SIGN_TOKEN_FILE="$T/token" SIGN_PUBLISHER="UT TEST SIGNER LTD" \
      SIGN_CA_BUNDLE="$T/bundle.pem" "$@" makensis -V1 -DVERSION=1.2.3 -DSRCDIR="$T/nsis/stage" \
      -DUNINST_SIGN_CMD="$SIGN" installer.nsi) >"$T/nsis.log" 2>&1
  }
  if mk; then
    if [ -s "$T/java-argv.log" ]; then pass "makensis signed the uninstaller"; else fail "makensis succeeded without calling the signer"; fi
  else
    fail "makensis with a working signer failed: $(cat "$T/nsis.log")"
  fi
  if mk STUB_FAIL=1; then fail "makensis succeeded although signing the uninstaller failed"; else pass "uninstaller signing failure aborts makensis"; fi
fi

if [ "$fails" -ne 0 ]; then
  echo "${fails} check(s) failed" >&2
  exit 1
fi
echo "all Windows signing checks passed"
