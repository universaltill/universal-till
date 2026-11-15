# Review: a restored copy's restore cut-off never precedes its newest pre-restore entry (ut-docs#5185)

**Date:** 2026-10-29 · **Card:** universaltill/ut-docs#5185 (follow-up of #5177, review finding 1)
**Branch:** `fix/5185-restored-since-newest-entry` · **Built by:** Sonnet · **Reviewed by:** Opus (independent subagent, separate worktree)

## What shipped
- `data.POSRepo.NewestLocalJournalEntryAt`: the greatest `created_at` across
  `no_sale_events` and `cleared_baskets` rows with an empty `till_id` (the
  filter the replica's push loops and the re-join replay use).
- `enroll.CheckRestoredInstall` takes that lookup as a `NewestJournalEntry`
  func (enroll keeps no dependency on `internal/data`); `internal/app` wires
  the repo method. On a restored replica, `sync.restored_since` becomes
  `max(now, newest + 1s)`. The +1s is needed because the replay counts
  `created_at == since` as made by the copy. A lookup error or an unreadable
  value falls back to now with a warning; boot never blocks. An already
  recorded value is kept and the lookup is not called; a main till never
  calls it.
- A warning when the cut-off lands more than 24h ahead of the copy's clock.
- Contract `pos-lan-sync-journal` 1.13.2 (ut-docs, replica-side only, no
  wire change).

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| L1 | low | Trade-off undocumented: with original clock skew larger than the backup-to-restore gap, the copy's own no-sale opens/cleared baskets in that window keep the original's register | Fixed: stated in `restoredSince`'s doc comment and here. Accepted: it errs toward never moving the original's entries |
| L2 | low | One far-future row (a clock set to 2099) pins `restored_since` there, with no log | Fixed: warning when the cut-off is more than 24h ahead. Not capped: any cap would move the original's entries again |
| L3 | low | `app.go` wiring has no test (passing `nil` would compile and pass) | Accepted: the same as the other boot steps. The end-to-end pages test drives the real repo method |
| N1 | nit | `_ = calls` in the keep-existing test | Fixed: asserts the lookup is not called |
| N2 | nit | Repo doc said "this till's OWN"; a replica also holds its main till's rows inherited at join (empty `till_id`) | Fixed: doc corrected. Those rows err the same safe way |

The reviewer also checked:
- Every writer formats `created_at` as `time.RFC3339` UTC seconds, so a text `MAX` is chronological.
- Journaled rows always carry a `till_id`.
- Sales stay matched by receipt prefix.
- `CheckRestoredInstall` has no other callers, including build-tagged ones.
- The pages test's global enroll state is reset in cleanup.
- No new file writes.

## Verified
- TDD, re-verified by the reviewer in its own worktree:
  - With `restoredSince` returning now, `TestRestoreCheck_RestoredSinceAfterFutureNewestEntry` and `TestReplay_OriginalEntriesStampedAheadOfTheCopyKeepTheirRegister` fail (`ns-ahead sent with register "reg-provisioned", want "reg-original"`).
  - Without the +1s, both also fail.
  - Restored, both pass.
- Full `go test ./...`, `go vet`, `gofmt`, `golangci-lint` (0 issues) and the `ci.yml` build-job guards were run locally.
  - The deadcode guard was run with `GOTOOLCHAIN=go1.27.1`. It flags only functions reachable from `cmd/unitill-desktop`, which it skipped for lack of GTK headers here; CI analyzes that build.
  - `guard-shellcheck-version` could not run (no shellcheck in the container); no shell script changed.
- Backend only; no UI surface, no locale keys, no help topic affected.

## Verdict
Safe to merge.
