#!/usr/bin/env bash
# Fixtures write literal $(VAR) / ${{ }} text on purpose.
# shellcheck disable=SC2016
#
# Regression test for guard-ios-tap-to-pay-default-off.sh (ut-docs#3049):
# proves the guard rejects each way Tap to Pay could reach a CI or
# TestFlight build — base flag on, Release flag or entitlement not pinned
# off, a workflow/script turning it on, an extra entitlement — rather than
# merely passing on today's tree. Runs against throwaway fixtures only.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-ios-tap-to-pay-default-off.sh"
FIX="$(mktemp -d -t guard-ios-ttp-XXXXXX)"
OUT="$(mktemp -t guard-ios-ttp-out-XXXXXX)"
cleanup() { rm -rf "${FIX}"; rm -f "${OUT}"; }
trap cleanup EXIT

FAIL_COUNT=0

# A good fixture: the default-off project.yml shape, a clean entitlements
# file, a workflow and a README that documents the opt-in.
fixture() {
  rm -rf "${FIX:?}/ios" "${FIX:?}/.github" "${FIX:?}/scripts" "${FIX:?}/Makefile"
  mkdir -p "${FIX}/ios/UniversalTill" "${FIX}/.github/workflows"
  cat > "${FIX}/ios/project.yml" <<'YML'
name: UniversalTill
targets:
  UniversalTill:
    type: application
    dependencies:
      - package: StripeTerminal
    settings:
      base:
        PRODUCT_BUNDLE_IDENTIFIER: com.universaltill.pos
        UT_TAP_TO_PAY: "NO" # default OFF
        UT_TAP_TO_PAY_INFO: $(UT_TAP_TO_PAY)
        UT_READER_MODE: "NO"
        UT_READER_MODE_INFO: $(UT_READER_MODE)
        UT_TTP_ENTITLEMENTS_YES: UniversalTill/UniversalTill.dev.entitlements
        UT_TTP_ENTITLEMENTS_NO: ""
      configs:
        Debug:
          CODE_SIGN_ENTITLEMENTS: $(UT_TTP_ENTITLEMENTS_$(UT_TAP_TO_PAY))
        Release:
          UT_TAP_TO_PAY: "NO"
          UT_TAP_TO_PAY_INFO: "NO"
          UT_READER_MODE: "NO"
          UT_READER_MODE_INFO: "NO"
          CODE_SIGN_ENTITLEMENTS: ""
    info:
      properties:
        UTTapToPayEnabled: $(UT_TAP_TO_PAY_INFO)
        UTReaderModeEnabled: $(UT_READER_MODE_INFO)
  UniversalTillTapToPayTests:
    type: bundle.unit-test
    settings:
      base:
        GENERATE_INFOPLIST_FILE: YES
YML
  cat > "${FIX}/ios/UniversalTill/UniversalTill.dev.entitlements" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>com.apple.developer.proximity-reader.payment.acceptance</key>
	<true/>
</dict>
</plist>
PLIST
  printf 'jobs:\n  build:\n    steps:\n      - run: xcodebuild build CODE_SIGNING_ALLOWED=NO\n' > "${FIX}/.github/workflows/ios-ci.yml"
  printf 'Build with `UT_TAP_TO_PAY=YES` on your own device.\n' > "${FIX}/ios/README.md"
}

assert_passes() {
  if bash "${GUARD}" "${FIX}" >"${OUT}" 2>&1; then
    echo "PASS (correctly accepted): $1"
  else
    echo "FAIL (should have been accepted but was rejected): $1"; cat "${OUT}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

assert_fails() {
  if bash "${GUARD}" "${FIX}" >"${OUT}" 2>&1; then
    echo "FAIL (should have been rejected but was accepted): $1"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "PASS (correctly rejected): $1"
  fi
}

edit() { python3 - "$@" <<'PY'
import sys
path, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
s = open(path).read()
assert old in s, (path, old)
open(path, "w").write(s.replace(old, new, 1))
PY
}

YML="${FIX}/ios/project.yml"

# 0. The default-off shape passes (README may document the opt-in).
fixture
assert_passes "default-off project.yml, README documenting UT_TAP_TO_PAY=YES"

# 1. Base flag on.
fixture; edit "${YML}" 'UT_TAP_TO_PAY: "NO" # default OFF' 'UT_TAP_TO_PAY: "YES"'
assert_fails "base UT_TAP_TO_PAY: \"YES\""
fixture; edit "${YML}" '        UT_TAP_TO_PAY: "NO" # default OFF
' ''
assert_fails "base UT_TAP_TO_PAY missing"

# 2. Release not pinned off.
fixture; edit "${YML}" '        Release:
          UT_TAP_TO_PAY: "NO"
' '        Release:
'
assert_fails "Release without UT_TAP_TO_PAY: \"NO\""
fixture; edit "${YML}" '          CODE_SIGN_ENTITLEMENTS: ""' '          CODE_SIGN_ENTITLEMENTS: UniversalTill/UniversalTill.dev.entitlements'
assert_fails "Release CODE_SIGN_ENTITLEMENTS set"
fixture; edit "${YML}" '          CODE_SIGN_ENTITLEMENTS: ""
' ''
assert_fails "Release CODE_SIGN_ENTITLEMENTS missing"

# 2b. Release's Info.plist switch is not pinned off: a command-line
#     UT_TAP_TO_PAY=YES would then register the bridge in a Release build.
fixture; edit "${YML}" '          UT_TAP_TO_PAY_INFO: "NO"
' ''
assert_fails "Release without UT_TAP_TO_PAY_INFO: \"NO\""
fixture; edit "${YML}" 'UTTapToPayEnabled: $(UT_TAP_TO_PAY_INFO)' 'UTTapToPayEnabled: $(UT_TAP_TO_PAY)'
assert_fails "UTTapToPayEnabled read from the raw flag, which a command line overrides in Release"
fixture; edit "${YML}" 'UTTapToPayEnabled: $(UT_TAP_TO_PAY_INFO)' 'UTTapToPayEnabled: "YES"'
assert_fails "UTTapToPayEnabled hard-coded YES"

# 3. Something under .github/, ios/, scripts/ or the Makefile turns it on.
fixture; printf '      - run: xcodebuild build UT_TAP_TO_PAY=YES\n' >> "${FIX}/.github/workflows/ios-ci.yml"
assert_fails "workflow passes UT_TAP_TO_PAY=YES"
fixture; mkdir -p "${FIX}/ios/scripts"; printf 'xcodebuild UT_TAP_TO_PAY="YES" build\n' > "${FIX}/ios/scripts/build.sh"
assert_fails "an ios/ script passes UT_TAP_TO_PAY=\"YES\""
fixture; printf 'UT_TAP_TO_PAY: "YES"\n' > "${FIX}/ios/extra.yml"
assert_fails "an ios/ yml sets UT_TAP_TO_PAY: \"YES\""
fixture; mkdir -p "${FIX}/scripts/ci"; printf 'xcodebuild build UT_TAP_TO_PAY=YES\n' > "${FIX}/scripts/ci/ios-sim-smoke.sh"
assert_fails "a scripts/ file a workflow runs passes UT_TAP_TO_PAY=YES"
fixture; printf 'ios:\n\txcodebuild build UT_TAP_TO_PAY=YES\n' > "${FIX}/Makefile"
assert_fails "the Makefile passes UT_TAP_TO_PAY=YES"
fixture; printf '      - run: xcodebuild build UT_TAP_TO_PAY=${{ vars.TTP }}\n' >> "${FIX}/.github/workflows/ios-ci.yml"
assert_fails "workflow passes the flag from a variable"
fixture; printf '      - run: xcodebuild build UT_TAP_TO_PAY_INFO=YES\n' >> "${FIX}/.github/workflows/ios-ci.yml"
assert_fails "workflow passes UT_TAP_TO_PAY_INFO=YES"
fixture; printf '      - run: xcodebuild build UT_TAP_TO_PAY=NO\n' >> "${FIX}/.github/workflows/ios-ci.yml"
assert_passes "workflow passes UT_TAP_TO_PAY=NO"
fixture; printf 'UT_TAP_TO_PAY_SIMULATED=YES\n' > "${FIX}/ios/notes.txt"
assert_passes "UT_TAP_TO_PAY_SIMULATED=YES is a different setting"

# 4. Extra entitlement.
fixture; edit "${FIX}/ios/UniversalTill/UniversalTill.dev.entitlements" '</dict>' '	<key>com.apple.developer.networking.multicast</key>
	<true/>
</dict>'
assert_fails "an extra key in the dev entitlements"

# 4b. Phone card-reader mode (UT_READER_MODE, ut-docs#6538) is pinned the same way.
fixture; edit "${YML}" '        UT_READER_MODE: "NO"
        UT_READER_MODE_INFO: $(UT_READER_MODE)' '        UT_READER_MODE: "YES"
        UT_READER_MODE_INFO: $(UT_READER_MODE)'
assert_fails "base UT_READER_MODE: \"YES\""
fixture; edit "${YML}" '          UT_READER_MODE: "NO"
          UT_READER_MODE_INFO: "NO"
' ''
assert_fails "Release without UT_READER_MODE pinned off"
fixture; edit "${YML}" '          UT_READER_MODE_INFO: "NO"' '          UT_READER_MODE_INFO: "YES"'
assert_fails "Release UT_READER_MODE_INFO: \"YES\""
fixture; edit "${YML}" 'UTReaderModeEnabled: $(UT_READER_MODE_INFO)' 'UTReaderModeEnabled: $(UT_READER_MODE)'
assert_fails "UTReaderModeEnabled read from the raw flag"
fixture; printf '      - run: xcodebuild build UT_READER_MODE=YES\n' >> "${FIX}/.github/workflows/ios-ci.yml"
assert_fails "workflow passes UT_READER_MODE=YES"
fixture; printf '      - run: xcodebuild build UT_READER_MODE=NO\n' >> "${FIX}/.github/workflows/ios-ci.yml"
assert_passes "workflow passes UT_READER_MODE=NO"

# 5. The real tree must pass.
if bash "${GUARD}" >"${OUT}" 2>&1; then
  echo "PASS (correctly accepted): real repository tree"
else
  echo "FAIL: real repository tree was rejected"; cat "${OUT}"
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "guard-ios-tap-to-pay-default-off_test: ${FAIL_COUNT} assertion(s) failed" >&2
  exit 1
fi
echo "guard-ios-tap-to-pay-default-off_test: all assertions passed"
