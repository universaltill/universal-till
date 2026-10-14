#!/usr/bin/env bash
# Resolve the marketplace Ed25519 plugin-signing public key that a release
# build compiles into internal/buildinfo.MarketplacePublicKey (ADR-0158,
# ut-docs#4292).
#
# Inputs (env):
#   UT_MARKETPLACE_PUBLIC_KEY_PIN   pinned key, 64 lowercase hex (workflows pass
#                                   ${{ vars.UT_MARKETPLACE_PUBLIC_KEY }});
#                                   empty = not pinned
#   UT_MARKETPLACE_SIGNING_KEY_URL  live key endpoint (default
#                                   https://cloud.universaltill.com/ui/api/signing-key);
#                                   an independent operator points it at its own plane
#
# Rules:
#   pin + live reachable + differ  -> ::error::, exit 1
#   pin + live unreachable/invalid -> ::warning::, use the pin
#   no pin + live ok               -> ::warning:: (pin the repo variable), use live
#   no pin + live fails            -> ::error::, exit 1
#
# Output: appends UT_MARKETPLACE_KEY_HEX=<hex> to $GITHUB_ENV when set,
# otherwise prints the hex on stdout. Messages go to stderr.
set -euo pipefail

url="${UT_MARKETPLACE_SIGNING_KEY_URL:-https://cloud.universaltill.com/ui/api/signing-key}"
pin="${UT_MARKETPLACE_PUBLIC_KEY_PIN:-}"

is_key() { [[ "$1" =~ ^[0-9a-f]{64}$ ]]; }

if [ -n "$pin" ] && ! is_key "$pin"; then
  echo "::error::UT_MARKETPLACE_PUBLIC_KEY is not 64 lowercase hex characters (an Ed25519 public key)" >&2
  exit 1
fi

# Verified TLS only (no -k), https only, bounded, 3 retries.
live=""
curl_err="$(mktemp)"
trap 'rm -f "$curl_err"' EXIT
if body="$(curl --fail --silent --show-error --proto '=https' --tlsv1.2 \
  --connect-timeout 10 --max-time 30 --retry 3 --retry-delay 2 --retry-all-errors \
  "$url" 2>"$curl_err")"; then
  if ! grep -Eq '"algorithm"[[:space:]]*:[[:space:]]*"ed25519"' <<<"$body"; then
    echo "resolve-marketplace-key: $url did not answer with an ed25519 key" >&2
  else
    live="$(sed -nE 's/.*"public_key_hex"[[:space:]]*:[[:space:]]*"([^"]*)".*/\1/p' <<<"$body")"
    live="$(sed -n 1p <<<"$live")"
    if ! is_key "$live"; then
      echo "resolve-marketplace-key: $url returned no valid 64-hex public_key_hex" >&2
      live=""
    fi
  fi
else
  echo "resolve-marketplace-key: could not fetch $url: $(tr '\n' ' ' <"$curl_err")" >&2
fi

key=""
if [ -n "$pin" ]; then
  if [ -n "$live" ] && [ "$live" != "$pin" ]; then
    echo "::error::marketplace signing key mismatch: repository variable UT_MARKETPLACE_PUBLIC_KEY=${pin:0:12}… but $url serves ${live:0:12}… — update the variable (key rotation) or fix the URL" >&2
    exit 1
  fi
  if [ -z "$live" ]; then
    echo "::warning::could not cross-check the pinned marketplace key against $url; building with the pinned UT_MARKETPLACE_PUBLIC_KEY" >&2
  fi
  key="$pin"
else
  if [ -z "$live" ]; then
    echo "::error::no marketplace signing key: UT_MARKETPLACE_PUBLIC_KEY is not set and $url gave no valid key — a release must not ship without one (ADR-0158)" >&2
    exit 1
  fi
  echo "::warning::UT_MARKETPLACE_PUBLIC_KEY repository variable is not set; using the live key from $url (${live:0:12}…). Pin it: set the repository variable to this value." >&2
  key="$live"
fi

if [ -n "${GITHUB_ENV:-}" ]; then
  echo "UT_MARKETPLACE_KEY_HEX=$key" >>"$GITHUB_ENV"
  echo "resolve-marketplace-key: UT_MARKETPLACE_KEY_HEX=${key:0:12}… exported" >&2
else
  echo "$key"
fi
