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

# A file counts as signed only if its signature and its timestamp both verify
# against the pinned root, and the signer is us.
if ! out="$(osslsigncode verify -CAfile "$SIGN_CA_BUNDLE" -TSA-CAfile "$SIGN_CA_BUNDLE" -in "$f" 2>&1)"; then
  echo "::error::$f: signature does not verify" >&2
  echo "$out" >&2
  exit 1
fi
if ! grep -qiF "$SIGN_PUBLISHER" <<<"$out"; then
  echo "::error::$f is not signed by $SIGN_PUBLISHER" >&2
  echo "$out" >&2
  exit 1
fi
if ! grep -qF "Timestamp Server Signature verification: ok" <<<"$out"; then
  echo "::error::$f: timestamp does not verify" >&2
  echo "$out" >&2
  exit 1
fi
echo "signed OK: $(basename "$f")" >&2
