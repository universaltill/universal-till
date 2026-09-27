#!/usr/bin/env bash
# Generate the Windows resource object (icon + VERSIONINFO) for one binary
# (ut-docs#2786). See packaging/windows/winres/README.md.
#
#   packaging/windows/winres.sh <server|desktop> <version>
#
# Writes rsrc_windows_amd64.syso into that binary's main package directory.
# Works from any cwd (it changes to the repo root). go-winres is pinned as a
# `tool` in go.mod, so this runs no unreviewed code in the release job.
set -euo pipefail
cd "$(dirname "$0")/../.."

kind="${1:-}"
version="${2:-}"
case "$kind" in
  server) dir="." ;;
  desktop) dir="cmd/unitill-desktop" ;;
  *) echo "usage: $0 <server|desktop> <version>" >&2; exit 2 ;;
esac
if [ -z "$version" ]; then
  echo "usage: $0 <server|desktop> <version>" >&2
  exit 2
fi

# VERSIONINFO's fixed fields hold four 16-bit numbers, and go-winres writes
# the same value into the FileVersion/ProductVersion strings. goreleaser
# snapshot versions look like 1.2.4-SNAPSHOT-abc1234 or 1.2.4-next, so a
# snapshot shows as 1.2.4 in the exe's Details tab; tagged releases are
# numeric already.
numeric="$(printf '%s' "${version#v}" | sed -E 's/^([0-9]+(\.[0-9]+){0,3}).*/\1/')"
case "$numeric" in
  [0-9]*) ;;
  *) numeric="0.0.0" ;;
esac

go tool go-winres make \
  --in "packaging/windows/winres/${kind}.json" \
  --arch amd64 \
  --out "${dir}/rsrc" \
  --product-version "${numeric}" \
  --file-version "${numeric}"
echo "winres: wrote ${dir}/rsrc_windows_amd64.syso (${kind}, ${version})"
