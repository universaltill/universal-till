#!/usr/bin/env bash
# Regression test for guard-macos-min-version.sh (ut-docs#3595).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

mk() { # $1=dir $2=go version $3=plist min
  mkdir -p "$1/packaging/macos"
  printf 'module x\n\ngo %s\n' "$2" > "$1/go.mod"
  printf '<plist><dict>\n  <key>LSMinimumSystemVersion</key>\n  <string>%s</string>\n</dict></plist>\n' "$3" > "$1/packaging/macos/Info.plist"
}
fail=0
mk "$tmp/stale" 1.27.1 11.0
if bash "$here/guard-macos-min-version.sh" "$tmp/stale" >/dev/null 2>&1; then echo "FAIL: go 1.27 with macOS 11.0 passed"; fail=1; fi
mk "$tmp/ok" 1.27.1 13.0
bash "$here/guard-macos-min-version.sh" "$tmp/ok" >/dev/null || { echo "FAIL: go 1.27 with macOS 13.0 failed"; fail=1; }
mk "$tmp/old" 1.25.0 12.0
bash "$here/guard-macos-min-version.sh" "$tmp/old" >/dev/null || { echo "FAIL: go 1.25 with macOS 12.0 failed"; fail=1; }
[ $fail -eq 0 ] && echo "✅ guard-macos-min-version_test: 3/3"
exit $fail
