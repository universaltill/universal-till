# Review: receipt and Z numbers jump after a backup restore (ut-docs#5068)

- **Branch:** `fix/5068-restore-number-jump`
- **Card:** ut-docs#5068 (investigation item B6 of #5058)
- **Built by:** Sonnet (Dev subagent).
- **Reviewed by:** an independent Opus 5.5 subagent (round 1), then the orchestrator, also Opus, re-reviewed the fix round.
- **Legal note:** ut-docs `legal-reviews/2026-10-26-restore-number-jump-5068.md`.

## What shipped

A restored backup lacks the receipts and Zs the till issued after the backup. `MAX()+1` used to hand those numbers out again. Now:

- **Marker file.** `db.ApplyPendingRestore` writes `restore-numbers-pending` (a unique token) before the swap. It does this for a backup restore only; a LAN join (replica identity staged) writes none. A failed swap removes the file.
- **The jump.** After Open, `app.jumpNumbersAfterRestore` calls `POSRepo.JumpNumbersAfterRestore`. One transaction:
  - sets the receipt floor: last + max(10 000, 2 × peak/day × days), rounded up to 1 000;
  - sets a Z floor per kind: last + days, where days = ceil(time since the last activity) + 1;
  - stores the token and writes a `restore.marker` JSON and a `restore_numbers_jumped` audit row.
  
  A token already stored changes nothing, so a crash cannot jump twice.
- **Numbering honours the floors.**
  - `NextReceiptNo` uses max(live, floor) when the floor's prefix matches.
  - `ArchiveReport` (both variants, inside the same atomic INSERT…SELECT) does the same. `prev_z_number` stays the real predecessor, so the gap is visible.
- **The first Z after a restore** embeds `restored_from_backup` and prints two lines: receipts continue at X, last in the backup Y; Z continues at N, last in the backup M. The marker is then cleared.
- **`restore.*` stays per-till.** It is added to `PerTillSettingPrefixes`, stripped from the join snapshot, and stripped in `ApplyReplicaIdentity`.
- **i18n and help.** Two keys in en/ar/fa/tr plus metadata. A paragraph in `web/help/*/backups.md`. The packs (de/es/pt) follow in their own PRs.

## Findings (round 1, independent Opus review)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | A LAN join copied the main till's `restore.*` keys. The replica's first Z would claim a restore and jump. | **Fixed**: stripped in `RedactedJoinSnapshot` and `ApplyReplicaIdentity`, with tests. |
| 2 | Medium | The jump was not idempotent. A crash or audit failure between commit and file removal re-jumped on every boot. | **Fixed**: the token in the same tx as the jump and the audit row. Test: same token gives no change; a new token jumps. |
| 3 | Low | The marker file was left behind when the swap failed, so a restore that never happened was jumped. | **Fixed**: the file is removed on both rename errors. No test: a rename failure cannot be forced portably as root. |
| 4 | Low | The Z jump was one larger than specified. | **Fixed**: floor = last + days. |
| 5 | Low | The data test did not prove the closedAt `ArchiveReport` variant. | **Fixed**: that variant runs first on a fresh floor. Reverting only its floor fails the test. |
| 6 | Low | The marker is cleared outside the archive insert, so a crash re-prints the note. | **Deferred** to ut-docs#5096. |
| 7 | Low | A jump failing on every boot is only logged. | **Deferred** to ut-docs#5096 (needs a manager-visible notice). |
| — | Scope | Invoice numbers repeat the same way. | **Deferred** to ut-docs#5094. |

Fix round, re-reviewed: the audit goes through `exec(tx)`, so it really is in the jump's transaction. The token check runs before any write. I reverted a stray timestamp-format change to `markRestoreRefused`.

## Verified beyond unit tests

- **TDD claims re-run by the reviewer.** Reverting `pos_repo.go`/`eod_api.go` fails `TestJumpNumbersAfterRestore_ExactFloorsAndContinuation` (next receipt `000000016`), `TestGenerateEOD_CarriesAndClearsRestoreMarker` and `TestBuildEODDoc_PrintsRestoredLinesOnlyWithMarker`. The dev reverted each fix-round change once, and each new test failed.
- **Driven run on the real binary.**
  - Seed: S1–S5 and Z1, then a backup, then S6–S7 and Z2 after it. Stage the restore and boot `./ut` (auth off).
  - Log: "receipts continue at 000011001, Z numbers at 5". `POST /api/reports/eod/run` gave "Report generated".
  - Then: 5 sales; next receipt `000011001` (000000006/7 never reused). The new Z carries the marker in its JSON with prev 1. Marker cleared, one audit row, marker file gone.
  - This was before finding 4 changed the Z bound by one.
- **Gate.**
  - `go build`, `gofmt` and `golangci-lint` (0 issues) pass.
  - `go test ./...` passes, with one exception: `internal/data` `TestStockUploadPendingAndHasSendable_FastWithRandomCloudIDsAndUnlinkedMarks`, a wall-clock bound (120 ms against its limit) that tripped while the guards ran in parallel. It passes 3 out of 3 runs alone, and this diff does not touch stock upload.
  - 53 of 55 build-job guards pass. Not runnable here (CI runs them): `guard-deadcode-baseline` (toolchain mismatch in this container) and `guard-shellcheck-version` (no shellcheck).
  - A `-race` full run hit the 10-minute timeout in `internal/catalogpull` (`TestPushStaleDecidedAtSendTime`), a package this diff does not touch. That test passes on `main` alone, with and without `-race`, so it is load-induced. CI runs without `-race`.

## Verdict

Safe to merge once CI is green. The deferred lows are on ut-docs#5096, invoices on ut-docs#5094.

## Round 2 — rebase onto main (2026-11-15, PR sweep, lane:cloud-41)

The PR sat unmerged while `main` moved 429 commits, so it was rebased. Six files conflicted: `sync_admin_repo.go`, `backup.go`, `join_snapshot.go`, both db test files and `en.meta.json`. Both sides were kept in every hunk.

`main` had gained three things this change interacts with:
- the restore swap journal and `RecoverInterruptedRestore` (ut-docs#5089);
- the live-DB keep list `RestoreKeepsLivePrefixes` (ut-docs#5069);
- the sales-copy bookkeeping reset in the join snapshot (ut-docs#5074).

**Built and reviewed by:**
- the resolution and fixes were written by Opus 5.5 (orchestrator);
- an independent Fable review checked them against `git range-diff`, then ran build, vet and the affected package tests.

The textual resolution was confirmed correct, with nothing lost from either side.

| # | Sev | Finding | Outcome |
|---|---|---|---|
| R1 | High | The second rename failing cleared the marker, but under #5089 the journal then *finishes* that restore on the next attempt. The till would reuse numbers. | **Fixed.** The marker stays on that failure. `RecoverInterruptedRestore`'s finish path now re-owes the jump for a backup restore (never a LAN join) when the marker is missing. Test: `TestRecoverInterruptedRestoreReowesLostNumberJump`; it fails without the fix (`token ""`). |
| R2 | Medium | An undone interrupted restore kept the marker. The live data would jump for a restore that never landed. | **Fixed** in the undo path. Test: `TestRecoverInterruptedRestoreKeepsNumberJumpOnlyWhenRestoreLands`; the reviewer re-ran it, and it fails without the fix. |
| R3 | High/Medium | A second restore of the same or an older backup computes the same floor, so numbers issued between the two restores repeat. The floors live only in the replaced DB. This already happens on `main` on every restore; it is not a regression of this PR. | **Deferred → ut-docs#6293** (carry the live floors through `keepLiveCloudIdentity`). |
| R4 | Low | The marker was not fsynced, and an empty marker gave an empty token, so the jump was not idempotent. | **Fixed.** `writeNumberJumpPending` syncs like the journal. An empty marker's token falls back to its mtime. Test: `TestNumberJumpTokenFallsBackForEmptyMarker`. |
| R5 | Low | A failed journal write left the marker behind. | **Fixed.** It is cleared there, since nothing has moved yet. |
| R6 | Low | A folder-level restore of a main till (device clone, iCloud) never passes `ApplyPendingRestore`, so it gets no jump. | **Deferred → ut-docs#6294**. |

**Gate:**
- `go build ./...`, `gofmt` and `golangci-lint` on the touched packages: 0 issues.
- `go test ./...`: green except `internal/plugins`, which hit the 10-minute package timeout in wazero compilation under parallel load. This diff does not touch it; it was re-run alone (see the PR).
- Every non-self-test guard in `ci.yml`'s build job passes. Not runnable here: `guard-deadcode-baseline` (toolchain mismatch) and `guard-shellcheck-version` (no shellcheck).

Pre-flight: help `web/help/*/backups.md` updated (round 1); `en.meta.json` updated for both new keys; `reference/` n/a (investigation-5058 is a historical record); README n/a (no claim changed); review record updated; follow-ups → ut-docs#6293, ut-docs#6294.
