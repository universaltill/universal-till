#!/usr/bin/env bash
# Sign one Windows PE file with Azure Artifact Signing (jsign on Linux — no
# Windows runner) and verify it, failing closed (ut-docs#2480, ut-docs#2610).
#
#   sign-exe.sh <file.exe>                # sign, then verify
#   sign-exe.sh --verify-only <file.exe>  # verify only
#
# Callers (all in the `release-signing` environment of release.yml):
#   - .goreleaser.yaml: the windows builds' post hook, so the portable zip and
#     checksums.txt carry the signed binaries;
#   - packaging/windows/installer.nsi: `!uninstfinalize`, for uninstall.exe;
#   - release.yml windows-installer: the Setup.exe itself.
#
# Inputs come from packaging/windows/setup-signing.sh (via $GITHUB_ENV):
# JSIGN_JAR, SIGN_ENDPOINT, SIGN_ALIAS, SIGN_TOKEN_FILE (sign mode), and
# SIGN_PUBLISHER, SIGN_CA_BUNDLE (both modes); optional SIGN_TSA_URL (default:
# Microsoft's RFC 3161 service — the timestamp must still chain to
# SIGN_CA_BUNDLE, so this cannot weaken verification). The short-lived token is read
# by jsign from SIGN_TOKEN_FILE — it is never on a command line or in the
# environment, and this script never prints it.
#
# Tools: java (sign mode), osslsigncode >= 2.9, openssl, awk. The signer check
# reads the leaf certificate of the Authenticode signature itself (openssl),
# never osslsigncode's text output (ut-docs#6636).
set -euo pipefail

mode=sign
if [ "${1:-}" = "--verify-only" ]; then
  mode=verify
  shift
fi
if [ "$#" -ne 1 ]; then
  echo "usage: $0 [--verify-only] <file.exe>" >&2
  exit 2
fi
f=$1
if [ ! -f "$f" ]; then
  echo "::error::sign-exe.sh: no such file: $f" >&2
  exit 1
fi

need() {
  local v
  for v in "$@"; do
    if [ -z "${!v:-}" ]; then
      echo "::error::sign-exe.sh: $v is not set — run packaging/windows/setup-signing.sh first" >&2
      exit 1
    fi
  done
}
need SIGN_PUBLISHER SIGN_CA_BUNDLE

# osslsigncode before 2.9 can't read a timestamp token without a signingTime
# attribute, which Microsoft's service sometimes sends, and fails a valid
# file. Install it with install-osslsigncode.sh, not apt (ut-docs#3867).
ver="$(osslsigncode --version 2>/dev/null | sed -n '1s/^osslsigncode \([0-9][0-9]*\)\.\([0-9][0-9]*\).*/\1 \2/p')" || true
read -r ver_major ver_minor <<<"${ver:-0 0}"
if [ "$ver_major" -lt 2 ] || { [ "$ver_major" -eq 2 ] && [ "$ver_minor" -lt 9 ]; }; then
  echo "::error::sign-exe.sh needs osslsigncode 2.9 or newer (found: $(osslsigncode --version 2>&1 | head -1)) — run packaging/windows/install-osslsigncode.sh" >&2
  exit 1
fi

if [ "$mode" = sign ]; then
  need JSIGN_JAR SIGN_ENDPOINT SIGN_ALIAS SIGN_TOKEN_FILE
  if [ ! -s "$SIGN_TOKEN_FILE" ]; then
    echo "::error::sign-exe.sh: SIGN_TOKEN_FILE is empty or missing" >&2
    exit 1
  fi
  java -jar "$JSIGN_JAR" --storetype TRUSTEDSIGNING \
    --keystore "$SIGN_ENDPOINT" --storepass "file:$SIGN_TOKEN_FILE" --alias "$SIGN_ALIAS" \
    --tsaurl "${SIGN_TSA_URL:-http://timestamp.acs.microsoft.com}" --tsmode RFC3161 \
    --replace "$f" >&2
fi

# Pull the one signer certificate out of the Authenticode signature and compare
# its subject CN, exactly and case-sensitively, with $2 (ut-docs#6636). The
# human-readable `osslsigncode verify` output is not parsed for this: a "/" in
# a name value would spoof its one-line subject. Fails closed on any surprise.
# `openssl pkcs7 -print` writes a serial below 2^128 in decimal and a larger one
# as 0x<hex>; `openssl x509 -serial` always writes hex. Both normalise to
# uppercase hex without leading zeros. The decimal is converted by long
# division, so the serial may exceed bash's 64-bit integers.
dec2hex() { # dec2hex <digits>
  local n=$1 out="" q r d i
  while [ -n "$n" ]; do
    q=""; r=0
    for ((i = 0; i < ${#n}; i++)); do
      d=$((r * 10 + ${n:i:1}))
      q+=$((d / 16)); r=$((d % 16))
    done
    out="$(printf '%X' "$r")${out}"
    n="${q#"${q%%[!0]*}"}"
  done
  printf '%s' "${out:-0}"
}
norm_serial() { # norm_serial <0xHEX | DECIMAL>; refuse anything else
  local v=$1
  if [[ "$v" =~ ^[0-9]+$ ]]; then
    v="${v#"${v%%[!0]*}"}"
    dec2hex "$v"
    return
  fi
  [[ "$v" =~ ^0[xX][0-9A-Fa-f]+$ ]] || return 1
  v="${v#0[xX]}"
  v="$(tr '[:lower:]' '[:upper:]' <<<"$v" | sed 's/^0*//')"
  printf '%s' "${v:-0}"
}
leaf_cn_is() { # leaf_cn_is <file> <expected-cn>
  local f=$1 want=$2 tmp n sig_serial c cs cn found=0 cnt
  tmp="$(mktemp -d)"
  if ! osslsigncode extract-signature -in "$f" -out "$tmp/sig.der" >/dev/null 2>&1 \
    || ! openssl pkcs7 -inform DER -in "$tmp/sig.der" -print >"$tmp/print.txt" 2>/dev/null; then
    echo "::error::$f: cannot read the Authenticode signature (ut-docs#6636)" >&2
    rm -rf -- "${tmp:?}"; return 1
  fi
  n="$(grep -c '^ *signer_info:' "$tmp/print.txt" || true)"
  if [ "$n" != 1 ]; then
    echo "::error::$f: expected exactly one signer, found ${n}" >&2
    rm -rf -- "${tmp:?}"; return 1
  fi
  # Anchored and scoped to the one signer_info: a certificate's subject in the
  # bag is printed earlier on one line and may carry the text "serial:".
  sig_serial="$(awk '/^ *signer_info:/{si=1} si && /^ *issuer_and_serial:/{on=1; next} on && /^ *serial:/{print $2; exit}' "$tmp/print.txt")"
  if ! sig_serial="$(norm_serial "$sig_serial")" \
    || ! openssl pkcs7 -inform DER -in "$tmp/sig.der" -print_certs >"$tmp/certs.pem" 2>/dev/null; then
    echo "::error::$f: cannot read the Authenticode signature (ut-docs#6636)" >&2
    rm -rf -- "${tmp:?}"; return 1
  fi
  awk -v d="$tmp" '/^-----BEGIN CERTIFICATE-----$/{k++; on=1} on{print > (d "/cert-" k ".pem")} /^-----END CERTIFICATE-----$/{on=0}' "$tmp/certs.pem"
  for c in "$tmp"/cert-*.pem; do
    [ -f "$c" ] || continue
    cs="$(openssl x509 -noout -serial -in "$c" 2>/dev/null | sed -n 's/^serial=//p')"
    cs="$(norm_serial "0x${cs}")" || continue
    if [ "$cs" = "$sig_serial" ]; then
      found=$((found + 1)); cn="$c"
    fi
  done
  if [ "$found" != 1 ]; then
    echo "::error::$f: the signer certificate is not in the signature" >&2
    rm -rf -- "${tmp:?}"; return 1
  fi
  cnt="$(openssl x509 -noout -subject -nameopt sep_multiline,sname,esc_ctrl -in "$cn" | grep -c '^    CN=' || true)"
  cn="$(openssl x509 -noout -subject -nameopt sep_multiline,sname,esc_ctrl -in "$cn" | sed -n 's/^    CN=//p')"
  rm -rf -- "${tmp:?}"
  if [ "$cnt" != 1 ] || [ "$cn" != "$want" ]; then
    echo "::error::$f is not signed by ${want}: leaf subject CN is '${cn}' (exact, case-sensitive match required)" >&2
    return 1
  fi
}

# A file counts as signed only if its signature and its timestamp both verify
# against the pinned root, and the signer is us.
if ! out="$(osslsigncode verify -CAfile "$SIGN_CA_BUNDLE" -TSA-CAfile "$SIGN_CA_BUNDLE" -in "$f" 2>&1)"; then
  echo "::error::$f: signature does not verify" >&2
  echo "$out" >&2
  exit 1
fi
if ! leaf_cn_is "$f" "$SIGN_PUBLISHER"; then
  echo "$out" >&2
  exit 1
fi
if ! grep -qF "Timestamp Server Signature verification: ok" <<<"$out"; then
  echo "::error::$f: timestamp does not verify" >&2
  echo "$out" >&2
  exit 1
fi
echo "signed OK: $(basename "$f")" >&2
