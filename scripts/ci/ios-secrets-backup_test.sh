#!/usr/bin/env bash
#
# Wiring guard for the iOS app's backup exclusion (ut-docs#5140): the data
# directory lives in Application Support, which iCloud backs up, so the
# secrets/ folder (directive key, approval key, device token) must be marked
# isExcludedFromBackup before the Go server starts — otherwise a restore
# clones the directive/approval/plugin-settings keys (secrets/) and the LAN
# TLS identity key (tls/) onto a second device. The device id/token live in
# the database, so internal/enroll/restored.go keeps an install marker under
# secrets/ (ut-docs#5157): a DB whose marker is missing was restored and has
# its cloud identity reset. That only works while the marker stays inside the
# excluded folder.
#   - ios/UniversalTill/TillServer.swift creates till/secrets, sets
#     isExcludedFromBackup = true on it, and dataDirectory() calls that
#     before returning (so MobileStart never runs without it);
#   - the folder name matches the Go side's paths.Data("secrets", ...);
#   - the install marker's path (installMarkerRel) starts with "secrets".
# ut-docs#6918: keys/ (off-site backup key) and fiscal/ (signing-device
# credential) are excluded too, and their Go paths are pinned.
# The Swift compiles only on ios-ci's macOS runner. Self-tested against
# broken fixtures so each check can actually fail.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

check() {
  local swift="$1" gokey="$2" restored="$3" keystore="$4" cred="$5" bad=0 code datadir
  code="$(grep -v '^ *//' "$swift")"
  grep -q '\["secrets", "tls", "keys", "fiscal"\]' <<<"$code" || { echo "secrets/, tls/, keys/ and fiscal/ are not all covered"; bad=1; }
  grep -q 'NSLog(' <<<"$code" || { echo "exclusion failure is not logged-and-continued"; bad=1; }
  grep -q 'isExcludedFromBackup = true' <<<"$code" || { echo "secrets folder is not excluded from backup"; bad=1; }
  grep -q 'setResourceValues(' <<<"$code" || { echo "exclusion flag is never applied"; bad=1; }
  grep -q 'posixPermissions: 0o700' <<<"$code" || { echo "secrets folder is not created 0700"; bad=1; }
  # dataDirectory() must apply it before handing the path to MobileStart.
  datadir="$(awk '/static func dataDirectory/,/^    }$/' "$swift" | grep -v '^ *//')"
  grep -q 'excludeSecretsFromBackup(in: dir)' <<<"$datadir" \
    || { echo "dataDirectory() does not call excludeSecretsFromBackup"; bad=1; }
  grep -q '"secrets"' "$gokey" || { echo "Go side no longer keeps keys under secrets/"; bad=1; }
  # A marker outside secrets/ would be backed up with the DB and never differ.
  grep -Eq 'installMarkerRel *= *\[\]string\{"secrets"' "$restored" \
    || { echo "install marker is not kept under secrets/"; bad=1; }
  # The LAN sync bearer (ut-docs#5172) is kept in a file; outside secrets/ it
  # would be backed up and restored with the DB.
  grep -Eq 'KeepSyncBearerOutOfDB\(.*paths\.Data\("secrets", "sync-bearer"\)' "$ROOT/internal/app/app.go" \
    || { echo "sync bearer file is not kept under secrets/"; bad=1; }
  # ut-docs#6918: the off-site backup key and the signing-device credential
  # must stay inside the excluded keys/ and fiscal/ folders.
  # Comment lines are ignored, so an old path left in a comment cannot pass.
  ks="$(grep -v '^ *//' "$keystore")"
  cs="$(grep -v '^ *//' "$cred")"
  if ! { grep -q 'paths.Data("keys")' <<<"$ks" && grep -q 'keyFileName *= *"offsite-backup.key"' <<<"$ks"; }; then
    echo "off-site backup key is no longer kept under keys/"; bad=1
  fi
  if ! { grep -Eq 'signingDeviceCredentialRelPath *= *\[\]string\{"fiscal",' <<<"$cs" \
    && grep -Eq 'paths\.Data\(signingDeviceCredentialRelPath\.\.\.\)' <<<"$cs"; }; then
    echo "signing-device credential is no longer kept under fiscal/"; bad=1
  fi
  if ! { grep -Eq 'legacyTSECredentialRelPath *= *\[\]string\{"fiscal",' <<<"$cs" \
    && grep -Eq 'paths\.Data\(legacyTSECredentialRelPath\.\.\.\)' <<<"$cs"; }; then
    echo "legacy TSE credential is no longer kept under fiscal/"; bad=1
  fi
  return "$bad"
}

swift="$ROOT/ios/UniversalTill/TillServer.swift"
gokey="$ROOT/internal/directivekey/directivekey.go"  # guard-sandbox:allow read-only; mutations go to copies in $tmp

restored="$ROOT/internal/enroll/restored.go"  # guard-sandbox:allow read-only; mutations go to copies in $tmp

keystore="$ROOT/internal/offsitebackup/keystore.go"  # guard-sandbox:allow read-only; mutations go to copies in $tmp
cred="$ROOT/internal/fiscal/tse_credential_store.go"  # guard-sandbox:allow read-only; mutations go to copies in $tmp

check "$swift" "$gokey" "$restored" "$keystore" "$cred" || { echo "FAIL: real tree"; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
expect_fail() {
  local name="$1" sedexpr="$2"
  sed "$sedexpr" "$swift" >"$tmp/$name.swift"
  if check "$tmp/$name.swift" "$gokey" "$restored" "$keystore" "$cred" >/dev/null; then
    echo "FAIL: broken fixture '$name' passed"; exit 1
  fi
}
expect_fail no-flag 's/isExcludedFromBackup = true/isExcludedFromBackup = false/'
expect_fail no-apply 's/setResourceValues(/noop(/'
expect_fail no-call 's/try excludeSecretsFromBackup(in: dir)/\/\/ removed/'
expect_fail wide-perms 's/posixPermissions: 0o700/posixPermissions: 0o755/'
expect_fail wrong-name 's/\["secrets", "tls", "keys", "fiscal"\]/["keys"]/'
expect_fail no-keys 's/, "keys"//'
expect_fail no-fiscal 's/, "fiscal"//'
expect_fail fatal 's/NSLog(/print(/'

# The marker's path must stay under secrets/ (a broken copy of restored.go).
sed 's/\[\]string{"secrets", "install-id"}/[]string{"install-id"}/' "$restored" >"$tmp/restored.go"
if check "$swift" "$gokey" "$tmp/restored.go" "$keystore" "$cred" >/dev/null; then
  echo "FAIL: broken fixture 'marker-outside-secrets' passed"; exit 1
fi

# The key and credential paths must stay under keys/ and fiscal/ (ut-docs#6918).
sed 's/paths\.Data("keys")/paths.Data("state")/' "$keystore" >"$tmp/keystore.go"
if check "$swift" "$gokey" "$restored" "$tmp/keystore.go" "$cred" >/dev/null; then
  echo "FAIL: broken fixture 'keystore-moved' passed"; exit 1
fi
for var in signingDeviceCredentialRelPath legacyTSECredentialRelPath; do
  sed "s/$var = \\[\\]string{\"fiscal\",/$var = []string{\"state\",/" "$cred" >"$tmp/cred-$var.go"
  if check "$swift" "$gokey" "$restored" "$keystore" "$tmp/cred-$var.go" >/dev/null; then
    echo "FAIL: broken fixture 'credential-moved: $var' passed"; exit 1
  fi
done

# The old path surviving only in a comment must not satisfy the pins.
{ echo '// paths.Data("keys") keyFileName = "offsite-backup.key"'; sed 's/paths\.Data("keys")/paths.Data("state")/' "$keystore"; } >"$tmp/keystore-comment.go"
if check "$swift" "$gokey" "$restored" "$tmp/keystore-comment.go" "$cred" >/dev/null; then
  echo "FAIL: broken fixture 'keystore-moved-comment-remains' passed"; exit 1
fi
{ echo '// paths.Data(signingDeviceCredentialRelPath...)'; sed 's/paths\.Data(signingDeviceCredentialRelPath\.\.\.)/paths.Data("state")/' "$cred"; } >"$tmp/cred-comment.go"
if check "$swift" "$gokey" "$restored" "$keystore" "$tmp/cred-comment.go" >/dev/null; then
  echo "FAIL: broken fixture 'credential-use-moved-comment-remains' passed"; exit 1
fi

echo "ios-secrets-backup: OK"
