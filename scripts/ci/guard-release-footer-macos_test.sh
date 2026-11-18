#!/usr/bin/env bash
# Regression test for guard-release-footer-macos.sh (ut-docs#6024).
# Fixtures live only in a mktemp -d, passed to the guard as a file argument.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

good='  footer: |
    **macOS:** open unitill-pos-1.0.0-macOS-arm64.dmg. Portable copy: choose **Open Anyway** in System Settings.
'
fail=0
expect_fail() { # $1=name $2=content
  printf '%s' "$2" > "$tmp/$1.yaml"
  if bash "$here/guard-release-footer-macos.sh" "$tmp/$1.yaml" >/dev/null 2>&1; then echo "FAIL: $1 passed"; fail=1; fi
}

printf '%s' "$good" > "$tmp/ok.yaml"
bash "$here/guard-release-footer-macos.sh" "$tmp/ok.yaml" >/dev/null || { echo "FAIL: good wording failed"; fail=1; }
bash "$here/guard-release-footer-macos.sh" >/dev/null || { echo "FAIL: real .goreleaser.yaml failed"; fail=1; }

expect_fail old "$good    right-click the file, choose **Open**, then **Open** again (a one-time step until we ship a signed build).
"
expect_fail rightclick "$good    Right click the file and choose Open.
"
expect_fail controlclick "$good    Control-click the app, then choose Open.
"
expect_fail signed "$good    one-time step until we  ship a signed build.
"
expect_fail nodmg '    **macOS:** choose **Open Anyway**.
'
expect_fail noanyway '    **macOS:** open unitill-pos-1.0.0-macOS-arm64.dmg.
'
[ $fail -eq 0 ] && echo "✅ guard-release-footer-macos_test: 8/8"
exit $fail
