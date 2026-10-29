#!/usr/bin/env bash
#
# Wiring guard for the iOS app's backup exclusion (ut-docs#5140): the data
# directory lives in Application Support, which iCloud backs up, so the
# secrets/ folder (directive key, approval key, device token) must be marked
# isExcludedFromBackup before the Go server starts — otherwise a restore
# clones the directive/approval/plugin-settings keys (secrets/) and the LAN
# TLS identity key (tls/) onto a second device. The device id/token live in
# the database and are a separate follow-up.
#   - ios/UniversalTill/TillServer.swift creates till/secrets, sets
#     isExcludedFromBackup = true on it, and dataDirectory() calls that
#     before returning (so MobileStart never runs without it);
#   - the folder name matches the Go side's paths.Data("secrets", ...).
# The Swift compiles only on ios-ci's macOS runner. Self-tested against
# broken fixtures so each check can actually fail.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

check() {
  local swift="$1" gokey="$2" bad=0 code
  code="$(grep -v '^ *//' "$swift")"
  grep -q '\["secrets", "tls"\]' <<<"$code" || { echo "secrets/ and tls/ are not both covered"; bad=1; }
  grep -q 'NSLog(' <<<"$code" || { echo "exclusion failure is not logged-and-continued"; bad=1; }
  grep -q 'isExcludedFromBackup = true' <<<"$code" || { echo "secrets folder is not excluded from backup"; bad=1; }
  grep -q 'setResourceValues(' <<<"$code" || { echo "exclusion flag is never applied"; bad=1; }
  grep -q 'posixPermissions: 0o700' <<<"$code" || { echo "secrets folder is not created 0700"; bad=1; }
  # dataDirectory() must apply it before handing the path to MobileStart.
  awk '/static func dataDirectory/,/^    }$/' "$swift" | grep -v '^ *//' | grep -q 'excludeSecretsFromBackup(in: dir)' \
    || { echo "dataDirectory() does not call excludeSecretsFromBackup"; bad=1; }
  grep -q '"secrets"' "$gokey" || { echo "Go side no longer keeps keys under secrets/"; bad=1; }
  return "$bad"
}

swift="$ROOT/ios/UniversalTill/TillServer.swift"
gokey="$ROOT/internal/directivekey/directivekey.go"  # guard-sandbox:allow read-only; mutations go to copies in $tmp

check "$swift" "$gokey" || { echo "FAIL: real tree"; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
expect_fail() {
  local name="$1" sedexpr="$2"
  sed "$sedexpr" "$swift" >"$tmp/$name.swift"
  if check "$tmp/$name.swift" "$gokey" >/dev/null; then
    echo "FAIL: broken fixture '$name' passed"; exit 1
  fi
}
expect_fail no-flag 's/isExcludedFromBackup = true/isExcludedFromBackup = false/'
expect_fail no-apply 's/setResourceValues(/noop(/'
expect_fail no-call 's/try excludeSecretsFromBackup(in: dir)/\/\/ removed/'
expect_fail wide-perms 's/posixPermissions: 0o700/posixPermissions: 0o755/'
expect_fail wrong-name 's/\["secrets", "tls"\]/["keys"]/'
expect_fail fatal 's/NSLog(/print(/'

echo "ios-secrets-backup: OK"
