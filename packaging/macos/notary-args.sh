#!/usr/bin/env bash
# shellcheck disable=SC2034 # NOTARY/NOTARY_KEYFILE are read by the sourcing script
# Picks the notarytool credentials for make-dmg.sh (ut-docs#2870). Sourced,
# not run. Sets NOTARY (argv for `xcrun notarytool submit`, empty when
# nothing is configured) and, for an API key, NOTARY_KEYFILE — call
# notary_cleanup when done.
#
# Order of preference:
#  1. App Store Connect API key: MACOS_NOTARY_KEY_P8 (the .p8 contents),
#     MACOS_NOTARY_KEY_ID, MACOS_NOTARY_ISSUER_ID. Scoped (role Developer),
#     revocable, not tied to a person — what CI uses. The key is written to a
#     0600 temp file; its contents never go on argv.
#  2. A stored keychain profile (MACOS_NOTARY_PROFILE) — local builds.
#  3. Apple ID + app-specific password (MACOS_NOTARY_APPLE_ID,
#     MACOS_NOTARY_TEAM_ID, MACOS_NOTARY_PASSWORD) — legacy fallback.
# A partially set API key is ignored rather than half-used.

notary_args() {
  NOTARY=()
  NOTARY_KEYFILE=""
  if [ -n "${MACOS_NOTARY_KEY_P8:-}" ] && [ -n "${MACOS_NOTARY_KEY_ID:-}" ] && [ -n "${MACOS_NOTARY_ISSUER_ID:-}" ]; then
    NOTARY_KEYFILE="$(umask 077 && mktemp "${TMPDIR:-/tmp}/notary-key.XXXXXX")"
    chmod 600 "$NOTARY_KEYFILE"
    printf '%s\n' "$MACOS_NOTARY_KEY_P8" > "$NOTARY_KEYFILE"
    NOTARY=(--key "$NOTARY_KEYFILE" --key-id "$MACOS_NOTARY_KEY_ID" --issuer "$MACOS_NOTARY_ISSUER_ID")
  elif [ -n "${MACOS_NOTARY_PROFILE:-}" ]; then
    NOTARY=(--keychain-profile "$MACOS_NOTARY_PROFILE")
  elif [ -n "${MACOS_NOTARY_APPLE_ID:-}" ] && [ -n "${MACOS_NOTARY_TEAM_ID:-}" ] && [ -n "${MACOS_NOTARY_PASSWORD:-}" ]; then
    NOTARY=(--apple-id "$MACOS_NOTARY_APPLE_ID" --team-id "$MACOS_NOTARY_TEAM_ID" --password "$MACOS_NOTARY_PASSWORD")
  fi
}

notary_cleanup() {
  if [ -n "${NOTARY_KEYFILE:-}" ]; then
    rm -f "$NOTARY_KEYFILE"
    NOTARY_KEYFILE=""
  fi
}
