#!/usr/bin/env bash
# Prepare a job to VERIFY a signed Windows file with packaging/windows/
# sign-exe.sh --verify-only (ut-docs#6407): no Azure login, no token, no
# signing identity. Builds the CA bundle (system roots + the pinned Microsoft
# root, same pin as setup-signing.sh) and exports SIGN_CA_BUNDLE and
# SIGN_PUBLISHER through $GITHUB_ENV. Needs osslsigncode from
# install-osslsigncode.sh.
set -euo pipefail

MS_ROOT_URL="https://www.microsoft.com/pkiops/certs/Microsoft%20Identity%20Verification%20Root%20Certificate%20Authority%202020.crt"
MS_ROOT_SHA256=5367f20c7ade0e2bca790915056d086b720c33c1fa2a2661acf787e3292e1270

: "${RUNNER_TEMP:?setup-verify.sh runs in GitHub Actions}"
: "${GITHUB_ENV:?setup-verify.sh runs in GitHub Actions}"
command -v osslsigncode >/dev/null || { echo "::error::osslsigncode missing: run install-osslsigncode.sh first" >&2; exit 1; }
dir="$RUNNER_TEMP/win-verify"
mkdir -p "$dir"
curl -fsSL -o "$dir/msroot.crt" "${MS_ROOT_URL}"
echo "${MS_ROOT_SHA256}  $dir/msroot.crt" | sha256sum -c -
{ cat /etc/ssl/certs/ca-certificates.crt; openssl x509 -inform DER -in "$dir/msroot.crt"; } >"$dir/ca-bundle.pem"
{
  echo "SIGN_CA_BUNDLE=$dir/ca-bundle.pem"
  echo "SIGN_PUBLISHER=TASK RUNNER TECHNOLOGY LTD"
} >>"$GITHUB_ENV"
