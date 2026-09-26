# Review — macOS notarization never holds a till release (ut-docs#2917)

**Why:** v0.25.0 (run 36212999572) sat in `macos-app` → notarization for
100+ min: `notarytool submit --wait` had no timeout, the job no
`timeout-minutes` (6 h default), and `publish-release`/`checksums`/
`verify-versions` all needed `macos-app`, so the release stayed a Draft for
every platform.

**Change:** `notary_submit_and_wait` (packaging/macos/notary-args.sh) submits
without `--wait`, records the submission id in the job summary, waits with
`notarytool wait --timeout ${NOTARY_TIMEOUT:-45m}`, passes only on
`Accepted`, prints the notary log on failure (never argv). `macos-app`:
`timeout-minutes: 90`, dmg version check moved here, dmg handed off as a
workflow artifact (sha output), no release writes. New `macos-dmg-attach`
(needs macos-app + publish-release, both success): sha check, upload dmg,
fold its checksums.txt line, re-verify every published asset.
`publish-release`, `checksums`, `verify-versions` no longer need `macos-app`.
Tests: `packaging/macos_notary_submit_test.go` (fake xcrun: accepted, custom
timeout, bad timeout, invalid/timed-out submissions, no id, make-dmg wiring),
`scripts/ci/release-macos-nonblocking_test.sh` (job graph + timeouts) in ci.yml.
Author: Opus 5.5. Reviewer: Fable.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `upload-artifact@v4` without `overwrite` → a re-run of a green macos-app 409s | `overwrite: true` |
| 2 | minor | Two stale comments said macos-app writes checksums.txt | updated |
| 3 | minor | Recovery text implied the submission is reused; a re-run submits a new .dmg | error text + docs corrected |
| 4 | note | Mac updater during publish→attach window: plain error, nightly retry next day, no Problems entry | documented (fails closed) |
| 5 | note | cleanup-orphaned-tag still waits on macos-app (now bounded 90 min) | accepted, pre-existing |

**Checked, no issue (reviewer):** a release can't publish without every
non-mac asset or with an incomplete checksums.txt; checksums.txt writers are
strictly serialised (checksums → publish-release → macos-dmg-attach; macos-app
writes nothing); `if:` semantics for failed/skipped/re-run macos-app; a failed
macos-app after publish never deletes the tag; least-privilege token;
artifact sha compared before upload; timeout regex rejects injection.

**Verification:** `go test ./packaging/...`, the non-blocking wiring test,
`check-release-checksums_test.sh`, `windows-signing_test.sh`, shellcheck,
actionlint (only pre-existing SC2046). Not exercised end to end until the
next release run.

**Verdict:** safe to merge. The in-flight v0.25.0 run keeps the old workflow.
