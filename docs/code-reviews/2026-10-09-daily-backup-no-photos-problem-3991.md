# Review — daily auto-backup without photos becomes a keyed Problem (ut-docs#3991)

Date: 2026-10-09 · Branch: `fix/3991-daily-backup-no-photos-problem` ·
Author: Sonnet (Dev subagent) · Reviewer: Opus 5.5, fresh context (independent)

## What shipped

- `internal/server/server.go` `runDailyBackup`: `db.SnapshotWithAssets` can
  return a path and an error together, meaning the DB snapshot was written
  but the photos couldn't be added. The daily backup used to only
  `log.Printf` that case. `log.Printf` never reaches `logging.OpenProblems`,
  which feeds both the back-office Problems panel and the cloud heartbeat, so
  nobody saw it. Now the case is raised with
  `WarnProblemf(ProblemKeyDailyBackupNoPhotos = "backup.daily_no_photos")`
  and resolved by the next complete daily backup. A test seam,
  `snapshotWithAssets`, was added, using the same pattern as #3948's in
  `internal/pages`.
- Uninstall (`cmd/unitill-uninstall`): no behaviour change. It already
  aborts on that partial error before apt-get runs; the card read this as
  "ignored". The new test `TestRunPhotoLessSnapshotAborts` pins the abort.
  It checks that the error names the photos, nothing is removed, the till is
  restarted, and the photo-less file is not copied to the destination.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | The Problems ring is in memory. After a restart, the fresh-skip (<24h) path never raises the problem again, so it disappears while the newest backup still lacks photos. | **Deferred** to ut-docs#3993: re-deriving it needs a snapshot inspection in `internal/db`. |
| 2 | Low/Med | Each failing day added another open entry under the key. Keyed entries never age out, and the panel shows only 5, so this one condition could hide every other problem. | **Fixed**: `ResolveProblems(key)` before each raise keeps one open entry. The test runs two photo-less days and asserts exactly one open entry. |
| 3 | Low | The message carried the full snapshot path ahead of the cause. The heartbeat cuts problems at 200 chars, which cut off the actionable cause. No sensitive leak. | **Fixed**: the message now leads with the cause and uses only the snapshot's file name. The test asserts the cause and name are present and the directory is not. |
| 4 | Low | A total daily snapshot failure is still only `log.Printf`, so it never shows in Problems. | **Deferred** to ut-docs#3993 (out of this card's scope). |
| 6 | Gate | The full `go test ./...` caught a problem: `internal/data` `TestSettingScope_EveryUsedKeyIsClassified` read the first name, `DailyBackupNoPhotosProblemKey`, as a settings-key constant (its name ends in `Key`). | **Fixed**: renamed it `ProblemKeyDailyBackupNoPhotos`, the same shape as `selfupdate.ProblemKeyRestartPending`. No scanner exception was needed. |
| 5 | Nit | A manual "Back up now" with photos doesn't resolve the daily problem; the next daily run does (≤24h). | Accepted. |

## Verification

- TDD:
  - The server test first failed with `undefined: ProblemKeyDailyBackupNoPhotos`. With only the constant and the seam added, it failed with `want 1 open … got []`. It then passed.
  - The reviewer re-verified this independently in a separate worktree: with the old `log.Printf` restored the test fails, and with `ResolveProblems` dropped it fails at the resolve assertion.
  - For finding 2, removing only the pre-raise `ResolveProblems` makes the test fail with two open entries; with it restored the test passes. I checked this myself.
- Uninstall test: mutating `makeVerifiedBackup` to `err != nil && snap == ""` makes it fail ("must not be copied to the destination"). The Dev ran this check and the reviewer confirmed it.
- Gates:
  - `gofmt -l` clean, `go build ./...`, `go vet`.
  - `go test -race` passes for `internal/server`, `cmd/unitill-uninstall` and `internal/logging`, plus a full `go test ./...`.
  - Every `ci.yml` build-job guard passes locally except two that are tool-version gaps in the container, not code: `guard-deadcode-baseline` (its binary is built with Go 1.26 and the repo targets 1.27) and `guard-shellcheck-version` (no shellcheck installed). CI runs both. `golangci-lint` was likewise not runnable locally (old Go build), so CI covers it.
- Not driven in a running app: the change is a background job's log/Problems entry, with no UI surface or template change and no new user-facing string. No e2e or screenshot applies, and the help text is unaffected.

## Verdict

Safe to merge.
