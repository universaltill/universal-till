#!/usr/bin/env bash
#
# ut-docs#3595: packaging/macos/Info.plist's LSMinimumSystemVersion must not be
# lower than the macOS the Go toolchain in go.mod can run on. Otherwise macOS
# opens the app on an unsupported release and the Go runtime aborts at start,
# where it should show the system's own "requires macOS N" dialog.
# When Go raises its macOS floor, add a row to go_macos_min below.
set -euo pipefail

root="${1:-.}"
gomod="$root/go.mod"
plist="$root/packaging/macos/Info.plist"

go_minor=$(sed -nE 's/^go 1\.([0-9]+).*/\1/p' "$gomod")
[ -n "$go_minor" ] || { echo "❌ guard-macos-min-version: no 'go 1.N' line in $gomod" >&2; exit 1; }

# Go release minor -> minimum macOS major (go.dev/doc/go1.N "Ports" notes).
go_macos_min() {
  local m=$1
  if   [ "$m" -ge 27 ]; then echo 13
  elif [ "$m" -ge 25 ]; then echo 12
  elif [ "$m" -ge 23 ]; then echo 11
  else echo 10; fi
}
need=$(go_macos_min "$go_minor")

have=$(awk '/<key>LSMinimumSystemVersion<\/key>/{getline; gsub(/.*<string>|<\/string>.*/, ""); print; exit}' "$plist")
[ -n "$have" ] || { echo "❌ guard-macos-min-version: no LSMinimumSystemVersion in $plist" >&2; exit 1; }
have_major=${have%%.*}

if [ "$have_major" -lt "$need" ]; then
  echo "❌ guard-macos-min-version: $plist says LSMinimumSystemVersion $have, but go 1.$go_minor needs macOS $need+ — raise it to $need.0" >&2
  exit 1
fi
echo "✅ guard-macos-min-version: LSMinimumSystemVersion $have >= macOS $need (go 1.$go_minor)"
