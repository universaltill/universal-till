#!/usr/bin/env bash
#
# Release-asset checksums completeness check (ut-docs#2863).
#
# WHY THIS EXISTS: checksums.txt is goreleaser-generated and only ever
# covers goreleaser's OWN artifacts. Every asset uploaded by a LATER
# release.yml job — the macOS .dmg (macos-app), the Windows setup .exe
# (windows-installer) and both Android .apk names (android-app) — never
# lands in it on its own. The .dmg already gets folded in by
# packaging/macos/update-checksums.sh (internal/selfupdate's mac update path
# verifies it before mounting and fails closed if the entry is missing), but
# before this script existed nothing extended that same protection to the
# setup .exe or either .apk — a missing/wrong entry for any of them would
# only ever be discovered by a real device failing to verify a real update.
# This script is release.yml's `checksums` job's actual gate: it proves
# checksums.txt on the release accounts for every file that is actually on
# it, byte for byte, or fails loudly and names exactly what's wrong.
#
# Usage: check-release-checksums.sh <checksums-file> <assets-dir>
#   checksums-file  path to a goreleaser-style checksums.txt
#                   ("<sha256>  <filename>", two spaces)
#   assets-dir      directory holding every file actually published on the
#                   release, including checksums.txt itself (never
#                   checksummed against its own file)
#
# Every check below runs before any exit, so one run reports every problem
# at once rather than stopping at the first:
#   - every regular file in assets-dir other than checksums.txt has EXACTLY
#     ONE line in checksums-file naming it, and that line's sha256 matches
#     the file's real sha256 (a missing entry, a wrong hash, and a
#     duplicated entry for the same file are each their own failure);
#   - no checksums-file line names a file absent from assets-dir (a stale
#     or phantom entry — e.g. a renamed asset whose old line never got
#     dropped);
#   - assets-dir holds at least one asset besides checksums.txt (almost certainly means a release-download step
#     upstream silently got nothing, not that the release genuinely ships
#     zero files).
set -euo pipefail

CHECKSUMS="${1:?usage: check-release-checksums.sh <checksums-file> <assets-dir>}"
ASSETS_DIR="${2:?usage: check-release-checksums.sh <checksums-file> <assets-dir>}"

[ -d "$ASSETS_DIR" ] || { echo "::error::check-release-checksums: assets dir not found: ${ASSETS_DIR}"; exit 1; }

errors=0
problem() {
  echo "::error::check-release-checksums: $1"
  errors=$((errors + 1))
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    echo "::error::check-release-checksums: neither sha256sum nor shasum found"
    exit 1
  fi
}

# The actual published assets that must each have their own checksums line —
# checksums.txt is never checksummed against itself. None at all means a
# release-download step upstream got nothing (a dir holding only
# checksums.txt counts as empty too).
mapfile -d '' -t assets < <(find "$ASSETS_DIR" -mindepth 1 -maxdepth 1 -type f ! -name checksums.txt -print0)
if [ "${#assets[@]}" -eq 0 ]; then
  problem "assets dir has no assets besides checksums.txt: ${ASSETS_DIR} — a release-download step upstream likely got nothing"
fi

checksums_basename="$(basename "$CHECKSUMS")"
if [ ! -f "$CHECKSUMS" ]; then
  problem "checksums file not found: ${CHECKSUMS}"
  CHECKSUMS=/dev/null
fi

for f in "${assets[@]}"; do
  name="$(basename "$f")"
  mapfile -t hits < <(awk -v n="$name" '$2 == n {print $1}' "$CHECKSUMS")
  case "${#hits[@]}" in
    0)
      problem "asset '${name}' has no entry in ${checksums_basename}"
      ;;
    1)
      want="${hits[0]}"
      got="$(sha256_of "$f")"
      if [ "$want" != "$got" ]; then
        problem "asset '${name}' sha256 mismatch: ${checksums_basename} says ${want}, actual file is ${got}"
      fi
      ;;
    *)
      problem "asset '${name}' has ${#hits[@]} entries in ${checksums_basename} (want exactly 1)"
      ;;
  esac
done

if [ -s "$CHECKSUMS" ]; then
  while read -r _ name; do
    [ -n "$name" ] || continue
    [ "$name" = checksums.txt ] && continue
    if [ ! -f "${ASSETS_DIR}/${name}" ]; then
      problem "${checksums_basename} names '${name}', which is not in ${ASSETS_DIR} (stale/phantom entry)"
    fi
  done < "$CHECKSUMS"
fi

if [ "$errors" -ne 0 ]; then
  echo "::error::check-release-checksums: ${errors} problem(s) found" >&2
  exit 1
fi

echo "check-release-checksums: ${#assets[@]} asset(s) verified against ${checksums_basename}"
