#!/usr/bin/env bash
# Resolve the Ed25519 release-signing public keys a release build compiles
# into internal/buildinfo.ReleasePublicKeys (ADR-0178). With keys stamped the
# till's self-updater refuses a release whose checksums.txt.sig is missing or
# not signed by one of them; with none, the signing path is dormant.
#
# Input (env):
#   UT_RELEASE_PUBLIC_KEYS_PIN   workflows pass ${{ vars.UT_RELEASE_PUBLIC_KEYS }}:
#                                1..4 comma-separated, distinct, 64-lowercase-hex
#                                public keys (current first, then next); empty =
#                                release signing not configured
#
# Rules:
#   empty      -> ::warning::, UT_RELEASE_KEYS_HEX is exported EMPTY (goreleaser
#                 errors on an unset Env, so the variable must always exist)
#   malformed  -> ::error::, exit 1
#   one key    -> ::warning:: (ADR-0178 section 6 wants current,next), still exported
#   otherwise  -> UT_RELEASE_KEYS_HEX=<list>
#
# Output: appends UT_RELEASE_KEYS_HEX=<list> to $GITHUB_ENV when set,
# otherwise prints the list on stdout. Messages go to stderr.
set -euo pipefail

pin="${UT_RELEASE_PUBLIC_KEYS_PIN:-}"

if [ -z "$pin" ]; then
  echo "::warning::release signing is not configured (ADR-0178): repository variable UT_RELEASE_PUBLIC_KEYS is empty, so this build's self-updater checks SHA-256 only" >&2
else
  if ! [[ "$pin" =~ ^[0-9a-f]{64}(,[0-9a-f]{64}){0,3}$ ]]; then
    echo "::error::UT_RELEASE_PUBLIC_KEYS must be 1-4 comma-separated 64-character lowercase hex Ed25519 public keys (no spaces)" >&2
    exit 1
  fi
  dupes="$(tr ',' '\n' <<<"$pin" | sort | uniq -d)"
  if [ -n "$dupes" ]; then
    echo "::error::UT_RELEASE_PUBLIC_KEYS lists a key twice" >&2
    exit 1
  fi
  if [[ "$pin" != *,* ]]; then
    echo "::warning::UT_RELEASE_PUBLIC_KEYS lists one key: ADR-0178 section 6 wants A,B (current and next) from the first armed release, so a key rotation does not need a till update" >&2
  fi
fi

if [ -n "${GITHUB_ENV:-}" ]; then
  echo "UT_RELEASE_KEYS_HEX=$pin" >>"$GITHUB_ENV"
  echo "resolve-release-keys: UT_RELEASE_KEYS_HEX exported (${#pin} chars)" >&2
else
  echo "$pin"
fi
