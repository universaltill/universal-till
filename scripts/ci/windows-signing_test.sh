#!/usr/bin/env bash
#
# Regression test for Windows code signing (ut-docs#2480, ut-docs#2610):
#
#   1. Wiring: the portable zip's binaries are signed by .goreleaser.yaml's
#      post-build hooks (so checksums.txt covers the signed files), the
#      goreleaser job turns those hooks on and prepares signing, and the
#      installer build passes the signer to makensis for uninstall.exe.
#   2. packaging/windows/sign-exe.sh fails closed (missing inputs, unsigned
#      file, wrong publisher, no timestamp, untrusted root, osslsigncode too
#      old) and passes a correctly signed + timestamped file, including a
#      timestamp token without a signingTime attribute, which Microsoft's
#      service sometimes sends (ut-docs#3867). Real Artifact Signing needs
#      the release identity, so `java` (jsign) is stubbed by an osslsigncode
#      signer with a throwaway test CA and osslsigncode's built-in TSA (or a
#      local TSA when STUB_TSA_URL is set).
#   3. installer.nsi's `!uninstfinalize` runs the signer on the uninstaller
#      and aborts makensis when signing fails.
#
# (2) needs osslsigncode (packaging/windows/install-osslsigncode.sh, built
# with curl) + openssl + go + python3, (3) needs makensis. Missing tools
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
gr_uses="$(grep -E '^\s*(- )?uses:' <<<"$gr_job" || true)"
if [ -n "$gr_uses" ] && grep -vqE '@[0-9a-f]{40}( |$)' <<<"$gr_uses"; then
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

# osslsigncode comes from the pinned source build, never Ubuntu's 2.8, and
# is built before the signing identity logs in (ut-docs#3867).
ss_code="$(grep -vE '^[[:space:]]*(#|echo )' packaging/windows/setup-signing.sh || true)"
if grep -qE 'apt-get install.*osslsigncode|install-osslsigncode' <<<"$ss_code"; then
  fail "setup-signing.sh installs osslsigncode; it runs after azure/login, so the build belongs in an earlier step"
else
  pass "setup-signing.sh installs no osslsigncode"
fi
wi_job="$(awk '/^  windows-installer:$/{on=1; print; next} on && /^  [a-z][a-z0-9-]*:$/{exit} on{print}' .github/workflows/release.yml)"
for job in goreleaser windows-installer; do
  if [ "$job" = goreleaser ]; then body="$gr_job"; else body="$wi_job"; fi
  build_at="$(grep -nF 'packaging/windows/install-osslsigncode.sh' <<<"$body" | sed -n 1p | cut -d: -f1 || true)"
  login_at="$(grep -nF 'azure/login@' <<<"$body" | sed -n 1p | cut -d: -f1 || true)"
  if [ -n "$build_at" ] && [ -n "$login_at" ] && [ "$build_at" -lt "$login_at" ]; then
    pass "${job} job builds osslsigncode before azure/login"
  else
    fail "release.yml ${job} job must run install-osslsigncode.sh in a step before azure/login"
  fi
done
# The literal comparison text, $-expression included.
# shellcheck disable=SC2016
if grep -qE '^OSSLSIGNCODE_COMMIT=[0-9a-f]{40}$' packaging/windows/install-osslsigncode.sh \
  && grep -qF '"$OSSLSIGNCODE_COMMIT" ]' packaging/windows/install-osslsigncode.sh; then
  pass "install-osslsigncode.sh pins and checks a full commit hash"
else
  fail "install-osslsigncode.sh: OSSLSIGNCODE_COMMIT must be a full 40-hex commit, compared after the clone"
fi
if grep -qE 'apt-get install.*osslsigncode' .github/workflows/ci.yml; then
  fail "ci.yml apt-installs osslsigncode; the signing test must run the verifier the release uses"
else
  pass "ci.yml takes osslsigncode from install-osslsigncode.sh"
fi

# --- 2 + 3. behaviour ---------------------------------------------------------

have() { command -v "$1" >/dev/null 2>&1; }
missing=()
for t in osslsigncode openssl go python3; do have "$t" || missing+=("$t"); done
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
tsa_pid=""
trap '[ -n "$tsa_pid" ] && kill "$tsa_pid" 2>/dev/null; rm -rf "$T"' EXIT

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
[ -n "\${STUB_TSA_URL:-}" ] && tsa=(-ts "\$STUB_TSA_URL")
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

# ut-docs#3867: an RFC 3161 token whose SignerInfo has no signingTime
# attribute (RFC 3161 doesn't require one; Microsoft's service sometimes
# leaves it out). This local TSA answers with exactly that shape: openssl's
# TSTInfo, re-signed by `openssl cms -noattr`. That drops every signed
# attribute, not just signingTime (Microsoft's tokens keep messageDigest and
# ESSCertIDv2), but it reaches the same osslsigncode 2.8 code path: 2.8
# printed "Timestamp is not available" for it, as for the failed release.
cat > "$T/tsa.cnf" <<CNF
[ tsa ]
default_tsa = t
[ t ]
serial = $T/tsa.serial
signer_digest = sha256
default_policy = 1.2.3.4.1
digests = sha256
ess_cert_id_alg = sha256
CNF
echo 01 > "$T/tsa.serial"
cat > "$T/tsa.py" <<'PY'
import http.server, os, subprocess, sys, tempfile
cert, key, cnf = sys.argv[1:4]
def der_len(n):
    if n < 0x80:
        return bytes([n])
    b = n.to_bytes((n.bit_length() + 7) // 8, "big")
    return bytes([0x80 | len(b)]) + b
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        req = self.rfile.read(int(self.headers["Content-Length"]))
        with tempfile.TemporaryDirectory() as d:
            p = lambda n: os.path.join(d, n)
            run = lambda *a: subprocess.run(a, check=True, capture_output=True)
            with open(p("req"), "wb") as f:
                f.write(req)
            run("openssl", "ts", "-reply", "-config", cnf, "-queryfile", p("req"),
                "-signer", cert, "-inkey", key, "-token_out", "-out", p("tok"))
            run("openssl", "cms", "-verify", "-noverify", "-inform", "DER", "-in", p("tok"), "-out", p("tst"))
            run("openssl", "cms", "-sign", "-binary", "-nodetach", "-noattr",
                "-econtent_type", "id-smime-ct-TSTInfo", "-md", "sha256",
                "-signer", cert, "-inkey", key, "-in", p("tst"), "-outform", "DER", "-out", p("tok2"))
            with open(p("tok2"), "rb") as f:
                tok = f.read()
        body = b"\x30\x03\x02\x01\x00" + tok  # PKIStatusInfo{granted}, token
        resp = b"\x30" + der_len(len(body)) + body
        self.send_response(200)
        self.send_header("Content-Type", "application/timestamp-reply")
        self.send_header("Content-Length", str(len(resp)))
        self.end_headers()
        self.wfile.write(resp)
    def log_message(self, *a):
        pass
s = http.server.HTTPServer(("127.0.0.1", 0), H)
print(s.server_address[1], flush=True)
s.serve_forever()
PY
python3 "$T/tsa.py" "$T/tsa.pem" "$T/tsa.key" "$T/tsa.cnf" > "$T/tsa.port" 2>"$T/tsa.err" &
tsa_pid=$!
for _ in $(seq 50); do [ -s "$T/tsa.port" ] && break; sleep 0.1; done
f="$(fresh nosigningtime.exe)"
if [ ! -s "$T/tsa.port" ]; then
  fail "local TSA did not start: $(cat "$T/tsa.err")"
elif run_sign STUB_TSA_URL="http://127.0.0.1:$(cat "$T/tsa.port")" -- "$f"; then
  ts_out="$(osslsigncode verify -CAfile "$T/bundle.pem" -TSA-CAfile "$T/bundle.pem" -in "$f" 2>&1 || true)"
  if grep -qF 'Signing time: N/A' <<<"$ts_out"; then
    pass "timestamp without signingTime is accepted"
  else
    fail "the local TSA's token carried a signingTime, so this case tests nothing"
  fi
else
  fail "a valid timestamp without signingTime was rejected: $(cat "$T/out.log")"
fi

# The version floor: an osslsigncode that reports 2.8 is refused outright.
mkdir -p "$T/oldbin"
real_osslsigncode="$(command -v osslsigncode)"
cat > "$T/oldbin/osslsigncode" <<OLD
#!/usr/bin/env bash
if [ "\${1:-}" = --version ]; then echo "osslsigncode 2.8, using:"; exit 0; fi
exec "$real_osslsigncode" "\$@"
OLD
chmod +x "$T/oldbin/osslsigncode"
f="$(fresh old.exe)"
if run_sign PATH="$T/oldbin:$T/bin:$PATH" -- "$f"; then
  fail "sign-exe.sh ran with osslsigncode 2.8"
elif grep -qF 'needs osslsigncode 2.9 or newer' "$T/out.log"; then
  pass "osslsigncode older than 2.9 is refused"
else
  fail "osslsigncode 2.8 failed for the wrong reason: $(cat "$T/out.log")"
fi

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
  # The copied script can't reach the repo's logo by its default relative
  # path (ut-docs#2786), so hand it the absolute one.
  ROOT_ICON="$PWD/web/public/assets/logo/ut-logo.ico"
  : > "$T/java-argv.log"
  mk() {
    (cd "$T/nsis" && env PATH="$T/bin:$PATH" JSIGN_JAR="$T/jsign.jar" SIGN_ENDPOINT=example.invalid \
      SIGN_ALIAS=acct/profile SIGN_TOKEN_FILE="$T/token" SIGN_PUBLISHER="UT TEST SIGNER LTD" \
      SIGN_CA_BUNDLE="$T/bundle.pem" "$@" makensis -V1 -DVERSION=1.2.3 -DSRCDIR="$T/nsis/stage" -DAPP_ICON="$ROOT_ICON" \
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
