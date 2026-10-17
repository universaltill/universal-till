# Review: cloud-master pull retires in-use option sets and retries moved options (ut-docs#4522)

- **Date:** 2026-10-17
- **Card:** universaltill/ut-docs#4522. It was found in the review of #4491 (findings 1 and 2 there).
- **Author:** Opus 5.5, lane `lane:cloud-41`.
- **Reviewer:** Sonnet 5.5, an independent subagent. The routed Fable review was refused (out of usage credits, HTTP 429), so the review fell back to Sonnet, as `MODEL-ROUTING.md` allows.

## What shipped

`internal/catalogpull` (ADR-0162 §4):

- **Option-set tombstones apply after the items and variants of their version group** (`orderGroup`).
  - A set the cloud deletes together with its last use is unused by the time its tombstone applies, so it is deleted outright.
- **An in-use set is retired, not just deactivated.** `OptionSetRepo.RetireOptionSetTx` deactivates the set and renames it to free the UNIQUE name, for example `Size (1a2b3c4d)`.
  - If that name is taken, the whole id is used. Retiring a set again keeps its name.
  - The set is recorded in the new table `cloud_retired` (migration 083, pinned, classified as never LAN-synced).
  - A later cloud set of the old name now applies.
- **The delete is retried on every chunk commit** (`runner.retryRetired`, which runs before the cursor write).
  - A retired set that is still inactive and no longer used by an active item is deleted, with audit `retired: true`.
  - A set reactivated on the till, or already gone, only loses its record.
  - A failure other than an infrastructure error is logged and kept for the next commit, so it never wedges the cursor.
- **A row refused on its first attempt is retried once at the end of its version group.** Only then is the refusal recorded and counted.
  - This covers an option or value moved between modifier groups or option sets in one cloud transaction, whichever side applies first.
  - The cursor moves to a version only once all of that version's rows, retries included, are applied. A chunk that commits while a retry is pending stores the previous version.
- **Docs:** `docs/data-model.md` (Cloud catalogue pull) and ut-docs `reference/contracts/cloud-catalog-feed.md`, which gets a till-side note.

No UI and no new strings. The freed name is data, not UI text. There is no `web/help` topic for the pull.

## Tests (TDD)

`internal/catalogpull/retire_retry_4522_test.go` has one test per scenario:

- the freed name (`…FreesItsName`)
- the deferred delete (`…DeletedOnceUnused`)
- a tombstone in the same version as its last user (`…WithItsLastUserInOneVersion`)
- an option moved between groups (`OptionMovedBetweenGroups…`)
- a value moved between sets (`ValueMovedBetweenOptionSets…`)
- the ordering (`OrderGroupPutsOptionSetTombstones…`)
- a reactivated set that is kept (`…ReactivatedLocallyIsKept`, from review finding 1)
- a crash between the first attempt and the retry (`MidGroupCommitWithRetryPending…`, from review finding 3)
- counting a twice-refused row once

The reviewer reverted the production change and saw the six scenario tests fail, then restored it and saw them pass. The author did the same check for the reactivation test.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | `retryRetired` deleted a retired set the shop had reactivated on the till, which is data loss. | Fixed: the delete runs only while the set is inactive (`OptionSetActiveTx`), and a regression test was added. |
| 2 | minor | A non-infrastructure error in `retryRetired` failed every chunk commit, which would wedge the cursor. | Fixed: each retry runs in its own savepoint. Only `isInfra` errors abort; anything else is logged and kept. |
| 3 | minor | No test covered a mid-group commit with a retry pending, or a crash at that point. | Fixed: `TestPullMidGroupCommitWithRetryPendingKeepsCursor_4522`. |
| 4 | minor | The idempotency check (`HasSuffix " (<id prefix>)"`) would not rename a set whose cloud name already ends in its own id prefix. | Accepted. It needs a name that ends in that set's own UUID prefix. |
| 5 | minor | Retired sets are only retried during a pull's commits, not when an item drops a set locally. | Accepted. This is bounded: the next pull cleans it up. |
| 6 | nit | Retried rows count twice toward the chunk size, so chunks commit slightly early. | Accepted. This is harmless. |

The reviewer also confirmed:
- Applied, Refused and Skipped are each counted once, and there is no double logging.
- A savepoint rollback leaves no partial state.
- A resync that re-sends the tombstone re-retires the set idempotently.
- The prune never touches `cloud_retired`.
- Raw SQL appears only in `internal/data`.

## Verification

- `gofmt` printed nothing. `go build ./...` passed. `golangci-lint run ./...` reported 0 issues.
- The guards passed: data-access, migration-version-collision, card-data-schema, i18n, core-neutral, deadcode-baseline-growth, and the rest of the `build` job's checks that apply.
- `go test -count=1 ./...` (CI's command) and CI's `-race` subset results are recorded in the PR.
- A repo-wide `-race` run on this 4-core container hit the 10-minute timeout in heavy packages while two test runs overlapped. Run alone with `-timeout 40m`, `catalogpull` and `data` passed under `-race`.

## Verdict

Safe to merge.

## Deferred

- Nothing new. #4491's finding 3, a cloud rename swap of two option sets in one version, is still refused on the UNIQUE name. A swap needs a temporary name, not a retry, and it is a rare edge. It is left as accepted in that record.
