#!/usr/bin/env bash
# shellcheck disable=SC2034 # NOTARY/NOTARY_KEYFILE are read by the sourcing script
# Picks the notarytool credentials for make-dmg.sh (ut-docs#2870). Sourced,
# not run. Sets NOTARY (argv for `xcrun notarytool submit`, empty when
# nothing is configured) and, for an API key, NOTARY_KEYFILE — call
# notary_cleanup when done. notary_submit_and_wait (below) is the bounded
# submit + wait make-dmg.sh uses (ut-docs#2917).
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

# notary_submit_and_wait <file> (ut-docs#2917): notarize with a bound. The
# v0.25.0 release sat in an unbounded `notarytool submit --wait` for 100+
# minutes. Instead: submit WITHOUT --wait (returns the submission id at once),
# record that id in the log and the GitHub job summary so a human can run
# `xcrun notarytool info <id>` later, then `notarytool wait --timeout`. Returns
# 0 only when Apple's status is "Accepted"; a timeout, "Invalid" or any other
# outcome returns 1 (after printing the notary log) so the caller never ships
# an un-notarized file. Needs NOTARY set by notary_args. NOTARY_TIMEOUT
# overrides the bound (default 45m; digits plus an optional s/m/h unit).
notary_submit_and_wait() {
  local file="$1" timeout="${NOTARY_TIMEOUT:-45m}" out id status
  if ! [[ "$timeout" =~ ^[0-9]+[smh]?$ ]]; then
    echo "::error::NOTARY_TIMEOUT '${timeout}' is not a duration like 45m" >&2
    return 1
  fi
  echo "==> submitting ${file} for notarization"
  out="$(xcrun notarytool submit "$file" "${NOTARY[@]}" --output-format json)" || {
    echo "::error::notarytool submit failed: ${out}" >&2
    return 1
  }
  id="$(_notary_json_field id "$out")"
  if ! [[ "$id" =~ ^[0-9A-Fa-f-]{8,64}$ ]]; then
    echo "::error::notarytool submit returned no submission id: ${out}" >&2
    return 1
  fi
  echo "notarization submission id: ${id}"
  _notary_summary "### macOS notarization" "" \
    "- Submission id: \`${id}\`" \
    "- Check it: \`xcrun notarytool info ${id} <credentials>\` (log: \`xcrun notarytool log ${id} <credentials>\`)"
  echo "==> waiting for Apple (bounded: ${timeout})"
  out="$(xcrun notarytool wait "$id" "${NOTARY[@]}" --timeout "$timeout" --output-format json)" || true
  status="$(_notary_json_field status "$out")"
  if [ "$status" = "Accepted" ]; then
    _notary_summary "- Status: **Accepted**"
    return 0
  fi
  echo "::error::notarization ${id} did not reach Accepted within ${timeout} (status: '${status:-none}') — the .dmg is NOT attached. Use the id with 'notarytool info/log' to diagnose; re-running macos-app builds and submits a NEW .dmg (it does not reuse this submission)" >&2
  _notary_summary "- Status: **${status:-not finished within ${timeout}}** — .dmg not attached"
  xcrun notarytool log "$id" "${NOTARY[@]}" || true
  return 1
}

# notarytool's --output-format json is a single-line object of flat strings.
_notary_json_field() {
  sed -n "s/.*\"$1\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" <<<"$2" | head -n1
}

_notary_summary() {
  [ -n "${GITHUB_STEP_SUMMARY:-}" ] || return 0
  printf '%s\n' "$@" >> "$GITHUB_STEP_SUMMARY"
}
