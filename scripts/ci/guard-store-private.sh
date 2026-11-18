#!/usr/bin/env bash
#
# Guard: app stores stay private until launch (ut-docs#3052, owner decision
# 2026-09-27). Every store-distributed build -- App Store (TestFlight),
# Google Play and Microsoft Store -- reaches only the owner + developers:
# TestFlight INTERNAL testing, the Play INTERNAL testing track, and a
# Microsoft Store private audience / package flight. Going public is a
# separate owner call on its own card, never an edit to a workflow input.
# This guard makes "no automated path to a public release" a check, not
# prose (ut-docs reference/store-distribution.md).
#
# Scans the files a release can run from: .github/workflows/*.yml|yaml,
# .github/actions/** (composite actions), scripts/** (.sh .go .rb .py .js
# .mjs .ps1; *_test.* and this guard's own files excluded), android/**
# Gradle files, and any Makefile / *.mk / fastlane Fastfile, Appfile,
# Supplyfile, Deliverfile or Pilotfile (.git, .claude, node_modules and
# vendor trees pruned). Comments do not count: `#` (yml/sh/rb/py/ps1/make)
# and `//` (go/kts/gradle/js) comments outside quotes are stripped first, so
# prose explaining the rule never trips it.
#
# Fails on:
#   app-store-review    an App Store review submission (fastlane deliver /
#                       appstore / upload_to_app_store, submit_for_review,
#                       the ASC reviewSubmissions / appStoreVersionSubmissions
#                       API)
#   testflight-external external TestFlight (betaAppReviewSubmissions,
#                       distribute_external: true, publicLinkEnabled: true)
#   play-track          a Play track that is not literally `internal`, in any
#                       key/flag/call form: track: x, "track": x, track = x,
#                       --track x, --track=x, track("x"), track.set("x"),
#                       .../tracks/x. Allow-list, so a custom closed track or
#                       a variable (`${{ inputs.track }}`, `trackName`) is
#                       refused too. The bare word in prose ("track data")
#                       is not a key and never counts.
#   play-promote        promoting a Play release to another track (fastlane
#                       track_promote_to / promote_track, Gradle Play
#                       Publisher promote*Artifact / --promote-track)
#   play-default-track  a Play upload that never names the internal track
#                       (fastlane supply defaults to production): a call or
#                       action form (upload_to_play_store(, supply(,
#                       upload-google-play) within its own step -- its line
#                       and the lines at or right of its key column; a
#                       command line (fastlane supply, gradlew publish*) on
#                       that logical line, `\` continuations included
#   msstore-public      a Microsoft Store submission (msstore publish,
#                       store-submission / apppublisher actions, StoreBroker
#                       *-ApplicationSubmission, the Partner Center
#                       applications/<id>/submissions API) without a package
#                       flight (--flightId / -f / flight_id / flights/).
#                       Also the Store submission API forms: manage.devcenter
#                       .microsoft.com, api.store.microsoft.com, the
#                       `ingestion` submissions API, `msstore submission` /
#                       `msstore package`, Get-/Update-/Commit-
#                       ApplicationSubmission (ut-docs#6405)
#   release-asset-clobber `gh release upload ... --clobber` (also across `\`
#                       continuations): a published release asset must never
#                       be overwritten (the Store submission and self-update
#                       point at its bytes); upload through
#                       scripts/ci/release-upload.sh. Exception: a same-line
#                       `release-asset-clobber:allow <why>` marker
#   msstore-submit-switch the Microsoft Store submit path stays shut until the
#                       owner's opening card (ut-docs#6408): the real-submit
#                       script must check store-release/microsoft.opened, and
#                       that file may exist only if it names its card
#                       (ut-docs#N) AND .github/workflows/msstore-submit.yml
#                       carries a `store-release:allow ut-docs#N` marker
#
# Reviewed exception: a same-line `store-release:allow ut-docs#<N>` marker
# naming the owner-approved card that opens that store to the public. On a
# multi-line upload step, a marker on the upload line or on any line of its
# step covers the step.
#
# Known gaps, accepted (this is a grep, not a parser): an unbalanced quote
# in prose can keep a comment in (fails safe); a release that builds its
# command or API path at runtime (string concatenation, a track read from
# an env var inside an SDK call with no key form) is not seen; files outside
# the scanned set (a script in another repo, a hand-run command) are not
# seen. Store-side permissions are the second fence: the Play publisher
# account may release to testing tracks only (ut-infra google-play README).
#
# Explicit-args form for fixture-based testing (guard-store-private_test.sh):
# the single arg is the repo root to scan.
set -euo pipefail
export LC_ALL=C # awk tolower() aborts on a multibyte byte under a UTF-8 locale

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCAN_ROOT="${1:-$ROOT_DIR}"

if [ ! -d "${SCAN_ROOT}/.github/workflows" ]; then
  echo "❌ store-private guard: ${SCAN_ROOT}/.github/workflows does not exist" >&2
  exit 1
fi

files="$(
  {
    find "${SCAN_ROOT}/.github/workflows" -type f \( -name '*.yml' -o -name '*.yaml' \)
    if [ -d "${SCAN_ROOT}/.github/actions" ]; then
      find "${SCAN_ROOT}/.github/actions" -type f \( -name '*.yml' -o -name '*.yaml' -o -name '*.sh' -o -name '*.ps1' \)
    fi
    if [ -d "${SCAN_ROOT}/scripts" ]; then
      find "${SCAN_ROOT}/scripts" -type f \( -name '*.sh' -o -name '*.go' -o -name '*.rb' -o -name '*.py' -o -name '*.js' -o -name '*.mjs' -o -name '*.ps1' \) \
        ! -name '*_test.*' ! -name 'guard-store-private*'
    fi
    if [ -d "${SCAN_ROOT}/android" ]; then
      find "${SCAN_ROOT}/android" -type f \( -name '*.gradle' -o -name '*.gradle.kts' \)
    fi
    find "${SCAN_ROOT}" \( -name .git -o -name .claude -o -name node_modules -o -name vendor \) -prune -o -type f \
      \( -name Makefile -o -name '*.mk' -o -name Fastfile -o -name Appfile -o -name Supplyfile -o -name Deliverfile -o -name Pilotfile \) -print
  } | LC_ALL=C sort -u
)"

if [ -z "$files" ]; then
  echo "❌ store-private guard: found no workflow files under ${SCAN_ROOT}/.github/workflows" >&2
  exit 1
fi

# $files is a newline-separated list of paths without spaces.
# shellcheck disable=SC2086
violations="$(awk '
  function report(rule, line) { print FILENAME ":" FNR ": [" rule "] " line }
  # Strip a line comment that sits outside quotes: `#` (or `//` when
  # slash) at line start or after whitespace.
  function strip(line,    i, c, q, n) {
    q = ""; n = length(line)
    for (i = 1; i <= n; i++) {
      c = substr(line, i, 1)
      if (q != "") { if (c == q) q = ""; continue }
      if (c == "\"" || c == "\047") { q = c; continue }
      if (i > 1 && substr(line, i - 1, 1) !~ /[ \t]/) continue
      if (!slash && c == "#") return substr(line, 1, i - 1)
      if (slash && c == "/" && substr(line, i + 1, 1) == "/") return substr(line, 1, i - 1)
    }
    return line
  }
  function indent(line,    m) { match(line, /^[ \t]*(- )?/); return RLENGTH }
  # Close the pending Play upload: report it unless its step named the
  # internal track or carried the marker.
  function closeUpload() {
    if (upAt != "" && !upOK) print upAt
    upAt = ""; upOK = 0
  }
  # Every Play track value on the line, in key/flag/call/path form,
  # appended to `vals` (space-separated; "internal" for the internal one).
  function tracks(lc,    s, v, out) {
    out = ""
    s = lc
    while (match(s, /(^|[^a-z0-9_-])"?track"?[ \t]*[:=][ \t]*["\047]?[^ \t"\047,)}]+/)) {
      v = substr(s, RSTART, RLENGTH); s = substr(s, RSTART + RLENGTH)
      sub(/^.*track"?[ \t]*[:=][ \t]*["\047]?/, "", v); out = out " " v
    }
    s = lc
    while (match(s, /(^|[ \t])--track([ \t]+|=)["\047]?[^ \t"\047,)}]+/)) {
      v = substr(s, RSTART, RLENGTH); s = substr(s, RSTART + RLENGTH)
      sub(/^.*--track([ \t]+|=)["\047]?/, "", v); out = out " " v
    }
    s = lc
    while (match(s, /(^|[^a-z0-9_])track(\.set)?\([ \t]*["\047]?[^ \t"\047,)}]+/)) {
      v = substr(s, RSTART, RLENGTH); s = substr(s, RSTART + RLENGTH)
      sub(/^.*track(\.set)?\([ \t]*["\047]?/, "", v); out = out " " v
    }
    s = lc
    while (match(s, /tracks\/[^ \t"\047\/?]+/)) {
      v = substr(s, RSTART + 7, RLENGTH - 7); s = substr(s, RSTART + RLENGTH); out = out " " v
    }
    return out
  }
  FNR == 1 { closeUpload(); inUp = 0; slash = (FILENAME ~ /\.(go|kts|gradle|js|mjs)$/) }
  {
    raw = $0
    code = strip(raw)
    if (code ~ /^[ \t]*$/) next
    lc = tolower(code)
    match(code, /^[ \t]*/); lead = RLENGTH
    allowed = (raw ~ /store-release:allow (ut-docs)?#[0-9]+/)

    # A published release asset is never overwritten: `release upload` with
    # --clobber on the same logical line (continuations included).
    if (lc ~ /release[ \t]+upload/) inUp = 1
    if (inUp && lc ~ /--clobber/ && raw !~ /release-asset-clobber:allow [a-z]/) {
      report("release-asset-clobber", raw)
    }
    if (inUp && code !~ /\\[ \t]*$/) inUp = 0
    # Deleting a published asset (to upload a different one under the same
    # name), clobber held in a variable, and the softprops release action all
    # replace a published asset.
    if (lc ~ /release[ \t]+delete-asset|releases\/assets\/[^ ]*|softprops\/action-gh-release|--clobber/ && lc !~ /release[ \t]+upload/ \
        && lc ~ /delete-asset|-x[ \t]+delete|softprops\/action-gh-release|clobber[a-z_]*[ \t]*=/ && raw !~ /release-asset-clobber:allow [a-z]/) {
      report("release-asset-clobber", raw)
    }

    # A line left of the upload key column (the next list item, `end`,
    # the next lane) ends its step. A command-line upload (fastlane supply,
    # gradlew publish*) is one logical line: it ends where its `\`
    # continuations do, so a later echo or curl naming the internal track
    # never counts for it.
    if (upAt != "" && (lead < upInd || (upCli && !prevCont))) closeUpload()
    prevCont = (code ~ /\\[ \t]*$/)

    tv = tracks(lc)
    if (upAt != "" && (allowed || tv ~ /(^| )internal( |$)/)) upOK = 1

    isCall = (lc ~ /upload_to_play_store|(^|[^a-z_])supply[ \t]*\(|upload-google-play/)
    isCli = (lc ~ /fastlane[ \t]+supply|publish[a-z]*bundle|publish[a-z]*apk/)
    if (isCall || isCli) {
      closeUpload()
      upCli = !isCall
      upAt = FILENAME ":" FNR ": [play-default-track] " raw
      upInd = indent(code)
      upOK = (allowed || tv ~ /(^| )internal( |$)/)
    }

    if (allowed) next

    if (lc ~ /submit[-_]?for[-_]?review|(^|[^a-z])reviewsubmissions|appstoreversionsubmissions|upload_to_app_store|fastlane[ \t]+(deliver|appstore)|(^|[ \t])(deliver|appstore)[ \t]*(\(|$)/) {
      report("app-store-review", raw); next
    }
    if (lc ~ /betaappreviewsubmissions|distribute_external[ \t]*:[ \t]*true|publiclinkenabled["\047]?[ \t]*[:=][ \t]*true/) {
      report("testflight-external", raw); next
    }
    if (lc ~ /promote_track|track_promote_to|promote[a-z]*artifact|promotetrack|promotereleaseto|promote-track/) {
      report("play-promote", raw); next
    }
    n = split(tv, arr, " ")
    for (i = 1; i <= n; i++) if (arr[i] != "internal") { report("play-track", raw); next }

    if (lc ~ /msstore[ \t]+(publish|submission|package)|store-submission@|microsoft-store-apppublisher|storebroker|manage\.devcenter\.microsoft\.com|api\.store\.microsoft\.com|submission\/v1\/product\/|isaacrlevin\/windows-store-action|microsoft\/store-submission|\/ingestion\/|ingestion\.[a-z.]*microsoft|(new|get|update|complete|commit|submit)-applicationsubmission|applications\/[^\/ ]+\/submissions/ \
        && lc !~ /--flightid|flight_?id|flights\// && !(lc ~ /msstore/ && lc ~ /(^|[ \t])-f[ \t]/)) {
      report("msstore-public", raw); next
    }
  }
  END { closeUpload() }
' $files)"

# msstore-submit-switch (ut-docs#6408): the Store submit path stays shut until
# the owner's opening card flips store-release/microsoft.opened.
sw="$(
  script="${SCAN_ROOT}/scripts/ci/msstore-submit.sh"
  wf="${SCAN_ROOT}/.github/workflows/msstore-submit.yml"
  opened="${SCAN_ROOT}/store-release/microsoft.opened"
  if [ -f "$script" ] && ! grep -v '^[[:space:]]*#' "$script" | grep -qE '\[ -f "\$opened" \]'; then
    echo "scripts/ci/msstore-submit.sh: [msstore-submit-switch] the submit script does not check store-release/microsoft.opened"
  fi
  if [ -f "$opened" ]; then
    card="$(grep -oE 'ut-docs#[0-9]+' "$opened" | sed -n 1p || true)"
    if [ -z "$card" ]; then
      echo "store-release/microsoft.opened: [msstore-submit-switch] the opening file must name its owner-approved card (ut-docs#<N>)"
    elif [ ! -f "$wf" ] || ! grep -qE "store-release:allow ${card}( |\$)" "$wf"; then
      echo ".github/workflows/msstore-submit.yml: [msstore-submit-switch] store-release/microsoft.opened names ${card} but the workflow carries no 'store-release:allow ${card}' marker"
    fi
  fi
)"
if [ -n "$sw" ]; then
  violations="${violations:+${violations}$'\n'}${sw}"
fi

if [ -n "$violations" ]; then
  echo "❌ store-private guard: a release path can reach a PUBLIC store audience (ut-docs#3052)" >&2
  echo "   Until launch every store build stays private: TestFlight internal testing, the" >&2
  echo "   Play internal track, a Microsoft Store package flight. Going public is an owner" >&2
  echo "   call on its own card; a reviewed exception carries a same-line" >&2
  echo "   'store-release:allow ut-docs#<card>' marker (ut-docs reference/store-distribution.md)." >&2
  echo "$violations" >&2
  exit 1
fi

echo "✓ store-private guard: no release path reaches a public store audience"
