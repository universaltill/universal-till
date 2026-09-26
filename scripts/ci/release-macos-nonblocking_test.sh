#!/usr/bin/env bash
#
# Wiring test for ut-docs#2917: macOS notarization must never hold a till
# release. v0.25.0 sat in an unbounded `notarytool submit --wait` for 100+
# minutes while the release stayed a draft for every platform, because
# publish-release (and checksums/verify-versions before it) needed macos-app.
#
# Pins, in .github/workflows/release.yml:
#   - macos-app has a timeout-minutes bound;
#   - publish-release, checksums and verify-versions do not need macos-app,
#     so Linux/Windows/Android publish without waiting for Apple;
#   - macos-dmg-attach attaches the .dmg AFTER publish-release, only when
#     macos-app succeeded, and still folds + verifies its checksums.txt entry.
# And that the bounded notarytool wait is in packaging/macos/notary-args.sh.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"
WF=".github/workflows/release.yml"

fails=0
pass() { echo "ok   - $1"; }
fail() { echo "FAIL - $1" >&2; fails=$((fails + 1)); }

job_block() { # job_block <job-name>
  awk -v j="  ${1}:" '$0==j{on=1; print; next} on && /^  [a-zA-Z_][a-zA-Z0-9_-]*:$/{exit} on{print}' "$WF"
}
needs_of() { # needs_of <job-name> -- job names in its needs:, one per line
  job_block "$1" | awk '/^    needs:/{f=1} f{print} f && (/\]/ || /needs: [a-z]/){exit}' \
    | sed 's/needs://' | tr -d '[],' | tr -s ' \n' '\n' | sed '/^$/d'
}

mac_job="$(job_block macos-app)"
if grep -qE '^    timeout-minutes: [0-9]+$' <<<"$mac_job"; then
  pass "macos-app has timeout-minutes"
else
  fail "macos-app has no job-level timeout-minutes (GitHub's default is 6 h)"
fi

for j in publish-release checksums verify-versions; do
  if [ -z "$(job_block "$j")" ]; then
    fail "release.yml has no ${j} job"
  elif needs_of "$j" | grep -qx 'macos-app'; then
    fail "${j} needs macos-app — a slow notarization would hold the whole release"
  else
    pass "${j} does not need macos-app"
  fi
  if job_block "$j" | grep -qF 'needs.macos-app.result'; then
    fail "${j}'s if: still gates on needs.macos-app.result"
  fi
done

attach="$(job_block macos-dmg-attach)"
if [ -z "$attach" ]; then
  fail "release.yml has no macos-dmg-attach job"
else
  for want in macos-app publish-release; do
    if needs_of macos-dmg-attach | grep -qx "$want"; then
      pass "macos-dmg-attach needs ${want}"
    else
      fail "macos-dmg-attach does not need ${want}"
    fi
  done
  for want in "needs.macos-app.result == 'success'" "needs.publish-release.result == 'success'" \
    'packaging/macos/update-checksums.sh' 'scripts/ci/check-release-checksums.sh' \
    'needs.macos-app.outputs.dmg_sha256'; do
    if grep -qF -- "$want" <<<"$attach"; then
      pass "macos-dmg-attach: ${want}"
    else
      fail "macos-dmg-attach is missing: ${want}"
    fi
  done
fi

# The .dmg reaches the release only through macos-dmg-attach.
if grep -qE 'gh release upload[^#]*macOS-arm64\.dmg' <<<"$mac_job"; then
  fail "macos-app uploads the .dmg itself (must be macos-dmg-attach, after publish)"
else
  pass "macos-app does not upload the .dmg to the release"
fi

if grep -qE 'notarytool wait .*--timeout' packaging/macos/notary-args.sh; then
  pass "notarytool wait is bounded by --timeout"
else
  fail "packaging/macos/notary-args.sh has no bounded 'notarytool wait … --timeout'"
fi

if [ "$fails" -ne 0 ]; then
  echo "${fails} check(s) failed" >&2
  exit 1
fi
echo "all release macOS non-blocking checks passed"
