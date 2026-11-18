#!/usr/bin/env bash
#
# ut-docs#6025: a release .dmg is Developer ID signed and notarized, or it is
# not built. The release page promises that (#6024); this makes it true by
# failing the macos-app job in seconds, before the release wait and the ~15
# min build, naming every missing secret.
#
# Forks (RELEASE_REPO != universaltill/universal-till) pass: they build ad-hoc
# as before. Applies to tag pushes and workflow_dispatch alike.
#
# Reads only env. Prints secret NAMES, never values. Never enable `set -x`.
set -euo pipefail

UPSTREAM_REPO="universaltill/universal-till"
REPO="${RELEASE_REPO:-}"

if [ -z "$REPO" ]; then
  echo "::error::RELEASE_REPO is not set — cannot tell upstream from a fork"
  exit 1
fi
if [ "$REPO" != "$UPSTREAM_REPO" ]; then
  echo "::notice::fork build (${REPO}) — Developer ID signing/notarization not required; the .dmg is ad-hoc signed unless secrets are set"
  exit 0
fi

# A value of only whitespace (a pasted blank line) counts as unset.
is_set() {
  local v="${!1:-}"
  [ -n "${v//[[:space:]]/}" ]
}

# trio_state <a> <b> <c>: complete | empty | partial (by variable NAME).
trio_state() {
  local n set_count=0
  for n in "$@"; do
    if is_set "$n"; then set_count=$((set_count + 1)); fi
  done
  if [ "$set_count" -eq "$#" ]; then
    echo complete
  elif [ "$set_count" -eq 0 ]; then
    echo empty
  else
    echo partial
  fi
}

# missing_of <a> <b> <c>: names of the empty ones, space separated.
missing_of() {
  local n out=""
  for n in "$@"; do
    if ! is_set "$n"; then out="${out:+$out }$n"; fi
  done
  echo "$out"
}

errors=0

signing=(MACOS_CERT_P12 MACOS_CERT_PASSWORD MACOS_SIGN_IDENTITY)
api=(MACOS_NOTARY_KEY_P8 MACOS_NOTARY_KEY_ID MACOS_NOTARY_ISSUER_ID)
aid=(MACOS_NOTARY_APPLE_ID MACOS_NOTARY_TEAM_ID MACOS_NOTARY_PASSWORD)

sign_missing="$(missing_of "${signing[@]}")"
if [ -n "$sign_missing" ]; then
  echo "::error::release ${REPO}: missing macOS signing secret(s): ${sign_missing} — refusing to build an ad-hoc .dmg for a release (ut-docs#6025). Set them in repo secrets (see docs/arch/desktop-app.md), then \"Re-run failed jobs\"."
  errors=1
fi

api_state="$(trio_state "${api[@]}")"
aid_state="$(trio_state "${aid[@]}")"
method=""
if [ "$api_state" = complete ]; then
  method="API key"
elif [ "$aid_state" = complete ]; then
  method="Apple ID"
else
  errors=1
  if [ "$api_state" = empty ] && [ "$aid_state" = empty ]; then
    echo "::error::no notarization credentials — set MACOS_NOTARY_KEY_P8, MACOS_NOTARY_KEY_ID, MACOS_NOTARY_ISSUER_ID (or the Apple-ID trio MACOS_NOTARY_APPLE_ID, MACOS_NOTARY_TEAM_ID, MACOS_NOTARY_PASSWORD)"
  else
    if [ "$api_state" = partial ]; then
      echo "::error::notarization API key set is incomplete — missing: $(missing_of "${api[@]}")"
    fi
    if [ "$aid_state" = partial ]; then
      echo "::error::notarization Apple ID set is incomplete — missing: $(missing_of "${aid[@]}")"
    fi
  fi
fi

if [ "$errors" -ne 0 ]; then
  exit 1
fi
echo "macOS release secrets present: Developer ID signing + ${method} notarization"
