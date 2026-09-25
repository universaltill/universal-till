#!/usr/bin/env bash
# Prepare a release.yml job to sign Windows binaries with packaging/windows/
# sign-exe.sh (ut-docs#2480, ut-docs#2610). Run it after `azure/login` as the
# signing identity (`unitill-release-signer`, GitHub OIDC — no secret; that
# identity can only sign with this one certificate profile, and only from a
# job in the `release-signing` environment — ut-infra
# unitill-infra/release-signing/).
#
# Installs osslsigncode, fetches jsign and the Microsoft root (both pinned by
# SHA-256), fetches a short-lived (~1h) signing token into a 0600 file, and
# exports the paths + signing coordinates through $GITHUB_ENV. The token itself
# is masked and never written to $GITHUB_ENV.
set -euo pipefail

JSIGN_VERSION="7.5"
JSIGN_SHA256=602a51c3545a6dc4fb99bd2ea7152b26d1345916d0c93ddfbd5936cb735af91c
# Artifact Signing certificates chain to this Microsoft root, which is not in
# Ubuntu's CA bundle; pinned by its SHA-256 (valid to 2045).
MS_ROOT_URL="https://www.microsoft.com/pkiops/certs/Microsoft%20Identity%20Verification%20Root%20Certificate%20Authority%202020.crt"
MS_ROOT_SHA256=5367f20c7ade0e2bca790915056d086b720c33c1fa2a2661acf787e3292e1270

: "${RUNNER_TEMP:?setup-signing.sh runs in GitHub Actions}"
: "${GITHUB_ENV:?setup-signing.sh runs in GitHub Actions}"
dir="$RUNNER_TEMP/win-signing"
mkdir -p "$dir"
chmod 700 "$dir"

sudo apt-get update
sudo apt-get install -y osslsigncode

curl -fsSL -o "$dir/jsign.jar" "https://github.com/ebourg/jsign/releases/download/${JSIGN_VERSION}/jsign-${JSIGN_VERSION}.jar"
echo "${JSIGN_SHA256}  $dir/jsign.jar" | sha256sum -c -
curl -fsSL -o "$dir/msroot.crt" "${MS_ROOT_URL}"
echo "${MS_ROOT_SHA256}  $dir/msroot.crt" | sha256sum -c -
{ cat /etc/ssl/certs/ca-certificates.crt; openssl x509 -inform DER -in "$dir/msroot.crt"; } > "$dir/ca-bundle.pem"

token="$(az account get-access-token --resource https://codesigning.azure.net --query accessToken -o tsv)"
if [ -z "$token" ]; then
  echo "::error::no signing token from az — is azure/login the signing identity?"
  exit 1
fi
echo "::add-mask::${token}"
( umask 077; printf '%s' "$token" > "$dir/token" )

{
  echo "JSIGN_JAR=$dir/jsign.jar"
  echo "SIGN_CA_BUNDLE=$dir/ca-bundle.pem"
  echo "SIGN_TOKEN_FILE=$dir/token"
  echo "SIGN_ENDPOINT=neu.codesigning.azure.net" # account unitillcodesign is in North Europe
  echo "SIGN_ALIAS=unitillcodesign/unitillreleasespt"
  echo "SIGN_PUBLISHER=TASK RUNNER TECHNOLOGY LTD"
} >> "$GITHUB_ENV"
