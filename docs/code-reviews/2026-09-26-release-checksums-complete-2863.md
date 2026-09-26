# Review: every release asset in checksums.txt (ut-docs#2863)

- **Date:** 2026-09-26 · **Branch:** `fix/2863-release-checksums-complete`
- **Author:** Sonnet 5 (dev subagent, lane:cloud-54) · **Reviewer:** Opus 5.5 (independent, fresh context)

## What shipped
- `release.yml`: new `checksums` job, after every uploader
  (`windows-installer`, `macos-app`, `android-app`) and after
  `verify-versions` (which downloads checksums.txt, so the `--clobber`
  re-upload can't race it). It downloads every release asset, checks the
  published setup `.exe` and both APKs byte-match the hashes their build jobs
  emitted (new job outputs `exe_sha256` / `apk_sha256`), folds the three lines
  in with `packaging/macos/update-checksums.sh`, runs the new checker, then
  re-uploads checksums.txt. `publish-release` (needs + `if:`) and
  `cleanup-orphaned-tag` now depend on it, so a release never goes public with
  an asset missing from checksums.txt.
- `scripts/ci/check-release-checksums.sh`: every asset has exactly one line
  with the right sha256; phantom lines and an asset-less dir fail; reports all
  problems before exiting.
- `scripts/ci/check-release-checksums_test.sh` (wired into `ci.yml` build job):
  six behaviour cases + wiring checks on `release.yml`.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | low | Test (f) passed without exercising the empty-dir check (no checksums.txt in the fixture → failed earlier). | Fixed: fixture has an empty checksums.txt; mutation (empty-dir check removed) now fails (f). |
| 2 | low | A dir holding only checksums.txt passed as "0 assets verified". | Fixed: fail when there are no assets besides checksums.txt. |
| 3 | low | `unitill-pos-android-latest.apk` never compared with the signed APK hash. | Fixed: compared with `APK_SHA256`. |
| — | info | `*`-prefixed / CRLF checksum lines fail closed (confusing message); `mapfile -d ''` needs bash ≥ 4.4 (CI is ubuntu). | Accepted. |

actionlint: 4 findings, all pre-existing on HEAD (SC2046/SC2034 in unrelated steps); none added.

## Verified beyond unit tests
- TDD re-verified by the orchestrator: with the script and the release.yml
  change removed, the test fails (11 checks); restored → green.
- Real data: downloaded all 11 assets of release v0.24.1 and ran the checker —
  it flagged exactly `unitill-pos-setup-0.24.1.exe`, `unitill-pos_0.24.1_android.apk`
  and `unitill-pos-android-latest.apk`; after folding, all 10 assets verified,
  and the exe line (`1623c91c…5c52`) equals GitHub's own asset digest.
- `shellcheck scripts/ci/*.sh` clean; both workflows parse;
  `windows-signing_test.sh` still passes.
- Not verified: an end-to-end release run — the job first runs on the next
  `release.yml` dispatch; DevOps checks that run's checksums.txt.

## Verdict
Safe to merge. No UI, locale, Go or migration changes.

## Deferred
- #160 (Windows self-updater) verifies against the new exe line.
