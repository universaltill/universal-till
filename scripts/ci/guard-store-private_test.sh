#!/usr/bin/env bash
#
# Regression test for guard-store-private.sh (ut-docs#3052): runs the guard
# against fixture repo trees in a fresh mktemp dir and proves it rejects
# every public-store path it names (App Store review, external TestFlight,
# a non-internal or variable Play track in each spelling, a Play promotion,
# a Play upload that never names the internal track, a Microsoft Store
# submission without a flight), ignores comments and prose, honours
# `store-release:allow ut-docs#<N>` (and only with a card number), and
# passes on the real tree.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-store-private.sh"
FAIL_COUNT=0

WORK="$(mktemp -d)"
# Invoked indirectly via `trap ... EXIT` (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() { rm -rf -- "${WORK:?}"; }
trap cleanup EXIT

REPO="${WORK}/repo"

reset_tree() {
  rm -rf -- "${REPO:?}"
  mkdir -p "${REPO}/.github/workflows" "${REPO}/scripts/release" "${REPO}/android/app" "${REPO}/fastlane"
  cat >"${REPO}/.github/workflows/ci.yml" <<'EOF'
name: ci
# Card data: no BIN/IIN, CVV/CVC, track data or PIN block.
on:
  push:
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo "nothing here submits for review or goes to production"
EOF
}

run_guard() { bash "${GUARD}" "${REPO}" >"${WORK}/out" 2>&1; }

# expect_fail <rule> <what> -- the guard must fail, citing [<rule>].
expect_fail() {
  if run_guard; then
    echo "❌ FAIL: expected the guard to reject $2, but it passed" >&2
    cat "${WORK}/out" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  elif ! grep -qF "[$1]" "${WORK}/out"; then
    echo "❌ FAIL: the guard rejected $2, but not as [$1]" >&2
    cat "${WORK}/out" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard rejected $2 [$1]"
  fi
}

expect_pass() {
  if run_guard; then
    echo "✓ guard ignored $1"
  else
    echo "❌ FAIL: expected the guard to ignore $1 (false positive), but it rejected it" >&2
    cat "${WORK}/out" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

# wf <body line(s)> -- a release workflow whose one step runs the given text.
wf() {
  {
    printf '%s\n' 'name: release' 'on:' '  workflow_dispatch:' 'jobs:' '  up:' '    runs-on: ubuntu-latest' '    steps:'
    printf '%s\n' "$@"
  } >"${REPO}/.github/workflows/release.yml"
}

reset_tree
expect_pass "a clean fixture tree (prose about track data / review in comments and strings)"

# --- App Store ---------------------------------------------------------------
reset_tree; wf '      - run: bundle exec fastlane deliver --submit_for_review'
expect_fail app-store-review "fastlane deliver (App Store review)"
reset_tree; printf '%s\n' 'lane :release do' '  upload_to_app_store(force: true)' 'end' >"${REPO}/fastlane/Fastfile"
expect_fail app-store-review "upload_to_app_store in a Fastfile"
reset_tree; printf '%s\n' 'package main' 'const p = "/v1/reviewSubmissions"' >"${REPO}/scripts/release/asc.go"
expect_fail app-store-review "the ASC reviewSubmissions API from a Go script"
reset_tree
# Fixture text: the $-expression must stay literal.
# shellcheck disable=SC2016
printf '%s\n' 'curl -X POST "$ASC/v1/appStoreVersionSubmissions"' >"${REPO}/scripts/release/asc.sh"
expect_fail app-store-review "appStoreVersionSubmissions from a shell script"

# --- External TestFlight -----------------------------------------------------
reset_tree; printf '%s\n' 'lane :beta do' '  upload_to_testflight(distribute_external: true, groups: ["Public"])' 'end' >"${REPO}/fastlane/Fastfile"
expect_fail testflight-external "upload_to_testflight with distribute_external: true"
reset_tree
# Fixture text: the $-expression must stay literal.
# shellcheck disable=SC2016
printf '%s\n' 'curl -X POST "$ASC/v1/betaAppReviewSubmissions"' >"${REPO}/scripts/release/beta.sh"
expect_fail testflight-external "betaAppReviewSubmissions (external beta review)"
reset_tree; printf '%s\n' 'body = {"publicLinkEnabled": true}' >"${REPO}/scripts/release/link.py"
expect_fail testflight-external "a TestFlight public link"
reset_tree; printf '%s\n' 'lane :beta do' '  upload_to_testflight(distribute_external: false)' 'end' >"${REPO}/fastlane/Fastfile"
expect_pass "upload_to_testflight with distribute_external: false (internal)"

# --- Google Play -------------------------------------------------------------
reset_tree; wf '      - uses: r0adkll/upload-google-play@0000000000000000000000000000000000000000' '        with:' '          track: production'
expect_fail play-track "upload-google-play with track: production"
reset_tree; wf '      - uses: r0adkll/upload-google-play@0000000000000000000000000000000000000000' '        with:' '          track: "beta"'
expect_fail play-track "upload-google-play with a quoted beta track"
reset_tree
# Fixture text: the $-expression must stay literal.
# shellcheck disable=SC2016
wf '      - uses: r0adkll/upload-google-play@0000000000000000000000000000000000000000' '        with:' '          track: ${{ inputs.track }}'
expect_fail play-track "a Play track taken from a workflow input"
reset_tree; wf '      - run: bundle exec fastlane supply --aab app.aab --track production'
expect_fail play-track "fastlane supply --track production"
reset_tree; wf '      - run: bundle exec fastlane supply --aab app.aab --track=alpha'
expect_fail play-track "fastlane supply --track=alpha"
reset_tree; wf '      - run: bundle exec fastlane supply --aab app.aab'
expect_fail play-default-track "fastlane supply with no track (defaults to production)"
reset_tree; printf '%s\n' 'play {' '    track.set("production")' '}' >"${REPO}/android/app/build.gradle.kts"
expect_fail play-track "Gradle Play Publisher track.set(\"production\")"
reset_tree
# Fixture text: the $-expression must stay literal.
# shellcheck disable=SC2016
printf '%s\n' 'curl -X PUT "$API/edits/$EDIT/tracks/production"' >"${REPO}/scripts/release/play.sh"
expect_fail play-track "the androidpublisher tracks/production endpoint"
reset_tree; printf '%s\n' 'lane :promote do' '  upload_to_play_store(track: "internal", track_promote_to: "production")' 'end' >"${REPO}/fastlane/Fastfile"
expect_fail play-promote "a Play promotion (track_promote_to)"
reset_tree; wf '      - uses: r0adkll/upload-google-play@0000000000000000000000000000000000000000' '        with:' '          track: internal'
expect_pass "upload-google-play with track: internal"
reset_tree; wf '      - run: bundle exec fastlane supply --aab app.aab --track internal'
expect_pass "fastlane supply --track internal"
reset_tree
# Fixture text: the $-expression must stay literal.
# shellcheck disable=SC2016
printf '%s\n' 'curl -X PUT "$API/edits/$EDIT/tracks/internal"' >"${REPO}/scripts/release/play.sh"
expect_pass "the androidpublisher tracks/internal endpoint"
reset_tree; wf '      # never: fastlane supply --track production' '      - run: echo ok'
expect_pass "a non-internal track named only in a comment"

# --- Microsoft Store ---------------------------------------------------------
reset_tree; wf '      - run: msstore publish ./out -id 9N000000'
expect_fail msstore-public "msstore publish without a flight"
reset_tree; wf '      - uses: microsoft/store-submission@0000000000000000000000000000000000000000'
expect_fail msstore-public "microsoft/store-submission (no flight support)"
reset_tree; wf '      - run: msstore publish ./out -id 9N000000 --flightId 0000-1111'
expect_pass "msstore publish to a package flight"

# --- Reviewed exception ------------------------------------------------------
reset_tree; wf '      - run: bundle exec fastlane supply --track production # store-release:allow ut-docs#9999 owner opened Play'
expect_pass "a same-line store-release:allow ut-docs#<N> marker"
reset_tree; wf '      - run: bundle exec fastlane supply --track production # store-release:allow because'
expect_fail play-track "a store-release:allow marker with no card number"

# --- Review findings (ut-docs#3052 review) ------------------------------------
# S1: "track internal" in prose (a step name) or in another step never
# satisfies an upload's own step.
reset_tree; wf '      - name: Upload to Play (track internal)' '        run: bundle exec fastlane supply --aab app.aab'
expect_fail play-default-track "a supply upload whose step name only says track internal"
reset_tree; wf '      - run: bundle exec fastlane supply --aab a.aab --track internal' '      - run: bundle exec fastlane supply --aab b.aab'
expect_fail play-default-track "a second supply upload with no track after an internal one"
reset_tree; printf '%s\n' 'lane :beta do' '  upload_to_play_store(' '    aab: "app.aab",' '    track: "internal"' '  )' 'end' >"${REPO}/fastlane/Fastfile"
expect_pass "a multi-line upload_to_play_store naming the internal track"
# S2: allow-list -- custom closed tracks, JSON keys and SDK variables.
reset_tree; wf '      - uses: r0adkll/upload-google-play@0000000000000000000000000000000000000000' '        with:' '          track: partners'
expect_fail play-track "a custom closed-testing track"
reset_tree; printf '%s\n' "curl -X PUT \"\$API/x\" -d '{\"track\":\"production\"}'" >"${REPO}/scripts/release/play.sh"
expect_fail play-track "a JSON \"track\":\"production\" body"
reset_tree; printf '%s\n' 'package main' 'var t = androidpublisher.Track{Track: trackName}' >"${REPO}/scripts/release/play.go"
expect_fail play-track "a Go SDK Track taken from a variable"
reset_tree; printf '%s\n' 'svc.edits().tracks().update(editId=e, track=track_name, body=b)' >"${REPO}/scripts/release/play.py"
expect_fail play-track "a Python SDK track= variable"
# S3: the marker covers the step of the action / multi-line form.
reset_tree; wf '      - uses: r0adkll/upload-google-play@0000000000000000000000000000000000000000' '        with:' '          track: production # store-release:allow ut-docs#9999 owner opened Play'
expect_pass "a marker on the track line of an upload-google-play step"
reset_tree; printf '%s\n' 'lane :release do' '  upload_to_play_store(' '    track: "production" # store-release:allow ut-docs#9999 owner opened Play' '  )' 'end' >"${REPO}/fastlane/Fastfile"
expect_pass "a marker on the track line of a multi-line upload_to_play_store"
# S4: files a workflow can release from.
reset_tree; mkdir -p "${REPO}/.github/actions/play-upload"
printf '%s\n' 'runs:' '  using: composite' '  steps:' '    - run: fastlane supply --track production' '      shell: bash' >"${REPO}/.github/actions/play-upload/action.yml"
expect_fail play-track "a composite action under .github/actions"
reset_tree; printf '%s\n' 'play-release:' '	fastlane supply --track production' >"${REPO}/Makefile"
expect_fail play-track "a Makefile target"
reset_tree; printf '%s\n' 'track("production")' >"${REPO}/fastlane/Supplyfile"
expect_fail play-track "a fastlane Supplyfile track(\"production\")"
reset_tree; mkdir -p "${REPO}/.claude/worktrees/x/fastlane"; printf '%s\n' 'track("production")' >"${REPO}/.claude/worktrees/x/fastlane/Supplyfile"
expect_pass "a stale agent worktree under .claude (pruned)"
# S5: Microsoft Store.
reset_tree
# Fixture text: the $-expression must stay literal.
# shellcheck disable=SC2016
printf '%s\n' 'Complete-ApplicationSubmission -AppId $appId -SubmissionId $sub' >"${REPO}/scripts/release/store.ps1"
expect_fail msstore-public "StoreBroker Complete-ApplicationSubmission in a .ps1"
reset_tree
# Fixture text: the $-expression must stay literal.
# shellcheck disable=SC2016
printf '%s\n' 'curl -X POST "$PC/v1.0/my/applications/$APP/submissions"' >"${REPO}/scripts/release/store.sh"
expect_fail msstore-public "the Partner Center applications/<id>/submissions API"
reset_tree; wf '      - run: msstore publish ./out -id 9N000000; echo "no flight"'
expect_fail msstore-public "msstore publish with only the word flight on its line"
reset_tree
# Fixture text: the $-expression must stay literal.
# shellcheck disable=SC2016
printf '%s\n' 'curl -X POST "$PC/v1.0/my/applications/$APP/flights/$F/submissions"' >"${REPO}/scripts/release/store.sh"
expect_pass "the Partner Center flight submissions API"
# S6: Gradle Play Publisher promotion.
reset_tree; wf '      - run: ./gradlew promoteReleaseArtifact --from-track internal --promote-track production'
expect_fail play-promote "Gradle Play Publisher promoteReleaseArtifact"
# N1/N2.
reset_tree; printf '%s\n' 'lane :release do' '  appstore(force: true)' 'end' >"${REPO}/fastlane/Fastfile"
expect_fail app-store-review "fastlane appstore (alias of deliver)"
reset_tree; wf '      - run: echo "see #123" && fastlane supply --track production'
expect_fail play-track "a # inside a quoted string before the command"

# --- Review round 2 ------------------------------------------------------------
reset_tree; printf '%s\n' 'lane :beta do' '  supply(aab: "app.aab")' 'end' >"${REPO}/fastlane/Fastfile"
expect_fail play-default-track "the fastlane supply(...) alias with no track"
reset_tree
# Fixture text: the $-expression must stay literal.
# shellcheck disable=SC2016
printf '%s\n' 'curl -f -X POST "$PC/v1.0/my/applications/$APP/submissions"' >"${REPO}/scripts/release/store.sh"
expect_fail msstore-public "curl -f to the Partner Center submissions API (-f is not a flight)"
reset_tree
# Fixture text: the $-expression must stay literal.
# shellcheck disable=SC2016
wf '      - run: |' '          bundle exec fastlane supply --aab app.aab' '          echo "Uploaded to Play (track: internal)" >> "$GITHUB_STEP_SUMMARY"'
expect_fail play-default-track "a CLI upload followed by an echo naming the internal track"
reset_tree
# Fixture text: the $-expression must stay literal.
# shellcheck disable=SC2016
printf '%s\n' 'fastlane supply --aab app.aab' 'curl "$API/edits/$E/tracks/internal"' >"${REPO}/scripts/release/play.sh"
expect_fail play-default-track "a column-0 CLI upload followed by a curl to tracks/internal"
reset_tree
# Fixture text: a literal trailing backslash, not an escaped quote.
# shellcheck disable=SC1003
wf '      - run: |' '          bundle exec fastlane supply \' '            --aab app.aab \' '            --track internal'
expect_pass "a CLI upload whose continuation lines name the internal track"
reset_tree; printf '%s\n' 'lane :release do' '  deliver' 'end' >"${REPO}/fastlane/Fastfile"
expect_fail app-store-review "a bare fastlane deliver action"

# --- Real tree ---------------------------------------------------------------
if bash "${GUARD}" >"${WORK}/out" 2>&1; then
  echo "✓ guard passes on the real tree"
else
  echo "❌ FAIL: guard rejects the real tree" >&2
  cat "${WORK}/out" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

if [ "${FAIL_COUNT}" -gt 0 ]; then
  echo "❌ guard-store-private_test: ${FAIL_COUNT} failure(s)" >&2
  exit 1
fi
echo "✓ guard-store-private_test: all cases passed"
