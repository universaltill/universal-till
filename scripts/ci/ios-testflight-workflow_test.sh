#!/usr/bin/env bash
#
# Wiring guard for .github/workflows/ios-testflight.yml (ut-docs#3069): the
# job that signs the iOS/iPadOS app and uploads it to TestFlight holds the
# App Store Connect API key, in a PUBLIC repo. Pins:
#   - the only trigger is workflow_dispatch — never push, release (never
#     fires from release.yml's GITHUB_TOKEN publish) or any pull_request*
#     (a fork PR must never reach the signing secrets); release.yml's
#     publish-release job dispatches it with the release version;
#   - every action pinned to a 40-hex commit SHA;
#   - the archive is unsigned (no -allowProvisioningUpdates, CODE_SIGNING_
#     ALLOWED=NO) so only the export cloud-signs — a signed archive mints a
#     new development certificate on every fresh runner;
#   - the job is guarded to universaltill/universal-till (forks skip);
#   - GitHub-hosted macos-26, never self-hosted; a job timeout; App Store
#     Connect refuses builds made with an iOS SDK older than 26, so a step
#     fails fast (before the 20-minute build) when the runner's iphoneos SDK
#     is older, and ios-ci.yml builds on the same image so a PR sees the
#     new SDK's compile errors first;
#   - permissions: contents: read; a non-cancelling concurrency group;
#   - the .p8 key is written under $RUNNER_TEMP with umask 077 / chmod 600
#     and removed (with ExportOptions.plist) in an `if: always()` step;
#   - secrets reach scripts only through env: — no `${{ secrets.` inside
#     a run: block (script injection / accidental echo);
#   - gomobile bind stamps buildinfo.Version=$MARKETING_VERSION, resolved
#     before the bind, so the app never reports "dev" (ut-docs#3221);
#   - the upload is TestFlight only: export method app-store-connect,
#     destination upload, and no App Store submission tooling.
#   - after the upload, scripts/asc-testflight-check waits for the build to
#     be VALID and fails unless an internal group with testers can see it
#     (ut-docs#3217: a green upload once reached nobody for 6 h); it runs
#     before the key is removed and is pinned to the app's bundle ID.
# Self-tested against broken fixtures so each check can actually fail.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

fails=0
pass() { echo "ok   - $1"; }
fail() { echo "FAIL - $1" >&2; fails=$((fails + 1)); }

# The top-level `on:` block (up to the next top-level key).
on_block() { awk '/^on:/{f=1; print; next} f && /^[a-z]/{f=0} f{print}' "$1"; }

# Lines inside `run:` scripts: a `run: |` block is every following line
# indented deeper than the `run:` key; a one-line `run: x` is itself.
run_lines() {
  awk '
    inrun { match($0, /^ */); if ($0 ~ /^ *$/ || RLENGTH > ind) { print; next } inrun=0 }
    /^ *(- )?run: *[|>]/ { match($0, /^ */); ind=RLENGTH; if ($0 ~ /- run:/) ind+=2; inrun=1; next }
    /^ *(- )?run: / { print }
  ' "$1"
}

# check_wf <file> -- prints one "FAIL:<reason>" line per violation.
check_wf() {
  local wf="$1" on runs
  on="$(on_block "$wf")"
  runs="$(run_lines "$wf")"
  grep -qE '^  workflow_dispatch:' <<<"$on" || echo "FAIL:no workflow_dispatch trigger"
  if grep -qE '^  (push|release|pull_request|pull_request_target|workflow_run|issue_comment|schedule):' <<<"$on"; then
    echo "FAIL:forbidden trigger (push/release/pull_request*/workflow_run/issue_comment/schedule)"
  fi
  local uses
  uses="$(grep -E '^ *(- )?uses: ' "$wf" || true)"
  if [ -n "$uses" ] && grep -vqE 'uses: [^@ ]+@[0-9a-f]{40}( |$)' <<<"$uses"; then
    echo "FAIL:an action is not pinned to a commit SHA"
  fi
  # The archive step (from its `- name:` to the next) must be unsigned.
  local archive
  archive="$(awk '/^      - name: /{p = ($0 ~ /^      - name: Archive/)} p' "$wf")"
  if [ -z "$archive" ] || ! grep -qF 'CODE_SIGNING_ALLOWED=NO' <<<"$archive" || grep -qF -- '-allowProvisioningUpdates' <<<"$archive"; then
    echo "FAIL:archive step is not unsigned (CODE_SIGNING_ALLOWED=NO, no -allowProvisioningUpdates)"
  fi
  if grep -qE '^on: *\[|^on: *[a-z]' "$wf"; then echo "FAIL:inline on: form not allowed"; fi
  grep -qF "if: github.repository == 'universaltill/universal-till'" "$wf" || echo "FAIL:no repository guard"
  grep -qE '^    runs-on: macos-26$' "$wf" || echo "FAIL:job does not run on GitHub-hosted macos-26"
  # The SDK step (from its `- name:` to the next) must actually refuse an
  # old SDK, and come before the archive.
  local sdkstep
  sdkstep="$(awk '/^      - name: /{p = ($0 ~ /^      - name: Refuse an iOS SDK/)} p' "$wf")"
  if [ -z "$sdkstep" ] || ! grep -qF 'xcrun --sdk iphoneos --show-sdk-version' <<<"$sdkstep" \
    || ! grep -qE -- '-lt 26 \]' <<<"$sdkstep" || ! grep -qF 'exit 1' <<<"$sdkstep" \
    || [ "$(awk '/^      - name: Refuse an iOS SDK/{print NR; exit}' "$wf")" -gt "$(awk '/^      - name: Archive/{print NR; exit} END{print 999999}' "$wf" | head -1)" ]; then
    echo "FAIL:no iOS SDK version check (refusing < 26) before the archive"
  fi
  if grep -qiE 'runs-on:.*(self-hosted|homelab)' "$wf"; then echo "FAIL:self-hosted runner in a public repo"; fi
  grep -qE '^    timeout-minutes: [0-9]+$' "$wf" || echo "FAIL:no job timeout-minutes"
  if ! grep -qE '^permissions:$' "$wf" || ! grep -qE '^  contents: read$' "$wf"; then
    echo "FAIL:permissions is not contents: read"
  fi
  if grep -qE '^  [a-z-]+: write$' "$wf"; then echo "FAIL:a write permission is granted"; fi
  if ! grep -qE '^  group: ios-testflight$' "$wf" || ! grep -qE '^  cancel-in-progress: false$' "$wf"; then
    echo "FAIL:no non-cancelling ios-testflight concurrency group"
  fi
  # shellcheck disable=SC2016
  if grep -qF '${{ secrets.' <<<"$runs"; then echo "FAIL:secrets expanded inside a run: script"; fi
  # shellcheck disable=SC2016
  grep -qF 'RUNNER_TEMP/private_keys/AuthKey_' <<<"$runs" || echo "FAIL:API key not written under RUNNER_TEMP/private_keys"
  grep -qE 'umask 077' <<<"$runs" || echo "FAIL:no umask 077 before writing the key"
  grep -qE 'chmod 600' <<<"$runs" || echo "FAIL:key file not chmod 600"
  # The key's only writer is `printf … | base64 --decode > file`; any other
  # echo/printf/cat/tee of it could land it in the log.
  local p8
  p8="$(grep -E '(echo|printf|cat|tee)[^#]*\$\{?ASC_KEY_P8' <<<"$runs" || true)"
  if [ -n "$p8" ] && grep -vqE '\| *base64 (-d|--decode) *>' <<<"$p8"; then
    echo "FAIL:ASC_KEY_P8 printed other than piped into base64 --decode"
  fi
  # A step (from its `- name:` to the next) that runs if: always() and
  # removes both the key directory and ExportOptions.plist.
  local cleanup
  cleanup="$(awk '
    function done() { if (blk ~ /\n        if: always\(\)/ && blk ~ /rm -/ && blk ~ /private_keys/ && blk ~ /ExportOptions/) ok=1 }
    /^      - name: / { done(); blk=$0; next }
    { blk = blk "\n" $0 }
    END { done(); if (ok) print "yes" }
  ' "$wf")"
  [ -n "$cleanup" ] || echo "FAIL:no if: always() step removing the key and ExportOptions.plist"
  grep -qF -- '-allowProvisioningUpdates' <<<"$runs" || echo "FAIL:no -allowProvisioningUpdates"
  grep -qF -- '-authenticationKeyPath' <<<"$runs" || echo "FAIL:no -authenticationKeyPath"
  grep -qF -- '-exportArchive' <<<"$runs" || echo "FAIL:no xcodebuild -exportArchive"
  grep -qF 'app-store-connect' "$wf" || echo "FAIL:export method is not app-store-connect"
  if ! grep -qE '<key>destination</key>' "$wf" || ! grep -qE '<string>upload</string>' "$wf"; then
    echo "FAIL:export destination is not upload"
  fi
  if grep -qiE 'altool|fastlane|deliver |submit-for-review|submitForReview' "$wf"; then
    echo "FAIL:App Store submission tooling present (TestFlight upload only)"
  fi
  for s in ASC_KEY_ID ASC_KEY_P8 ASC_ISSUER_ID MACOS_NOTARY_TEAM_ID; do
    grep -qF "secrets.${s} }}" "$wf" || echo "FAIL:secret ${s} not passed via env"
  done
  # ut-docs#3221: the bind step must stamp buildinfo.Version with the
  # resolved marketing version, or the app's footer and updater read "dev".
  local bind
  bind="$(awk '/^      - name: /{p = ($0 ~ /^      - name: Bind the Go till server/)} p' "$wf")"
  # shellcheck disable=SC2016
  if [ -z "$bind" ] || ! grep -qF -- '-ldflags "-X github.com/universaltill/universal-till/internal/buildinfo.Version=${MARKETING_VERSION}"' <<<"$bind"; then
    echo "FAIL:gomobile bind does not stamp buildinfo.Version with MARKETING_VERSION (expected exactly -ldflags \"-X github.com/universaltill/universal-till/internal/buildinfo.Version=\${MARKETING_VERSION}\")"
  fi
  # ut-docs#3217: a step after the export/upload (and before the key is
  # removed) runs the audience check against the app's bundle ID.
  local aud aud_at export_at cleanup_at
  aud="$(awk '/^      - name: /{p = ($0 ~ /^      - name: Check an internal tester can see the build/)} p' "$wf")"
  aud_at="$(awk '/^      - name: Check an internal tester can see the build/{print NR; exit}' "$wf")"
  export_at="$(awk '/^      - name: Export and upload/{print NR; exit}' "$wf")"
  cleanup_at="$(awk '/^      - name: Remove the API key/{print NR; exit}' "$wf")"
  if [ -z "$aud" ] || [ -z "$export_at" ] || [ -z "$cleanup_at" ] \
    || ! grep -qF 'go run ./scripts/asc-testflight-check' <<<"$aud" \
    || ! grep -qF -- '-bundle-id com.universaltill.pos' <<<"$aud" \
    || [ "$aud_at" -lt "$export_at" ] || [ "$aud_at" -gt "$cleanup_at" ]; then
    echo "FAIL:no TestFlight audience check (scripts/asc-testflight-check) between the upload and the key cleanup"
  fi
  # ...and the version must be resolved before the bind runs.
  local resolve_at bind_at
  resolve_at="$(awk '/^      - name: Resolve marketing and build versions/{print NR; exit}' "$wf")"
  bind_at="$(awk '/^      - name: Bind the Go till server/{print NR; exit}' "$wf")"
  if [ -z "$resolve_at" ] || [ -z "$bind_at" ] || [ "$resolve_at" -gt "$bind_at" ]; then
    echo "FAIL:MARKETING_VERSION is resolved after the gomobile bind"
  fi
}

# --- self-test: a broken workflow must trip the checks -----------------------
fixture="$(mktemp)"
trap 'rm -f "$fixture"' EXIT
cat >"$fixture" <<'YAML'
name: bad
on:
  pull_request_target:
  release:
    types: [published]
  workflow_dispatch:
permissions:
  contents: write
jobs:
  upload:
    runs-on: [self-hosted, homelab]
    steps:
      - uses: actions/checkout@v4
      - name: Archive
        run: xcodebuild archive -allowProvisioningUpdates
      - name: key
        run: |
          echo "${{ secrets.ASC_KEY_P8 }}" > /tmp/AuthKey.p8
          echo "$ASC_KEY_P8"
YAML
bad="$(check_wf "$fixture")"
for want in 'not pinned to a commit SHA' 'archive step is not unsigned' 'forbidden trigger' 'no repository guard' 'macos-26' 'iOS SDK version check' 'self-hosted' \
  'timeout-minutes' 'write permission' 'concurrency' 'secrets expanded inside a run' 'RUNNER_TEMP' \
  'umask 077' 'if: always()' 'ASC_KEY_P8 printed' 'stamp buildinfo.Version' 'resolved after the gomobile bind' \
  'no TestFlight audience check'; do
  if grep -qF -- "$want" <<<"$bad"; then
    pass "self-test: broken fixture trips '${want}'"
  else
    fail "self-test: broken fixture did NOT trip '${want}'"
  fi
done

# --- the real workflow ------------------------------------------------------
WF=".github/workflows/ios-testflight.yml"
if [ ! -f "$WF" ]; then
  fail "${WF} does not exist"
else
  real="$(check_wf "$WF")"
  if [ -z "$real" ]; then
    pass "${WF} passes every check"
  else
    while IFS= read -r line; do fail "${WF}: ${line#FAIL:}"; done <<<"$real"
  fi
fi

# ios-ci.yml must build on the image TestFlight uploads from, or a PR stays
# green while the release upload fails on the newer SDK.
if grep -qE '^    runs-on: macos-26$' .github/workflows/ios-ci.yml; then
  pass "ios-ci.yml builds on macos-26 (same image as the upload)"
else
  fail "ios-ci.yml does not build on macos-26 (same image as the upload)"
fi

# release.yml's publish-release job is what starts the upload for a release.
publish_job="$(awk '/^  publish-release:/{f=1} f && /^  [a-z-]+:$/ && !/publish-release/{f=0} f' .github/workflows/release.yml)"
# shellcheck disable=SC2016
if grep -qE 'gh workflow run ios-testflight\.yml --ref "\$TAG" -f version=' .github/workflows/release.yml \
  && grep -qE '^      actions: write$' <<<"$publish_job"; then
  pass "release.yml publish-release dispatches ios-testflight.yml with the release version"
else
  fail "release.yml publish-release does not dispatch ios-testflight.yml (with actions: write)"
fi

# The workflow sets MARKETING_VERSION / CURRENT_PROJECT_VERSION on the
# xcodebuild command line; they only reach the bundle if Info.plist takes
# them from the build settings. XcodeGen otherwise writes literal "1.0" / "1",
# and every upload after the first would be rejected as a duplicate build.
# shellcheck disable=SC2016
for want in 'CFBundleShortVersionString: $(MARKETING_VERSION)' 'CFBundleVersion: $(CURRENT_PROJECT_VERSION)'; do
  if grep -qF -- "$want" ios/project.yml; then
    pass "ios/project.yml Info.plist: ${want}"
  else
    fail "ios/project.yml Info.plist lacks '${want}' (version overrides would not reach the bundle)"
  fi
done

if [ "$fails" -ne 0 ]; then
  echo "${fails} check(s) failed" >&2
  exit 1
fi
echo "all iOS TestFlight workflow checks passed"
