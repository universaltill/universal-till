# Review: a restored copy's entries reach the new main till under its own register (ut-docs#5177)

**Date:** 2026-10-29 · **Branch:** `fix/5177-restored-copy-register` · **Lane:** `lane:cloud-41`
**Author model:** Sonnet (Dev), from an Opus Architect note on the card · **Reviewer:** Opus 5.5 (independent subagent, a different model from the author)

## Finding that rescoped the card

The main till's `applyJournal` never writes a replayed sale's `register_id`: the sale is booked under the enrolled till's `till_id`, and the Z report and rollups key on `till_id` first. So a re-joined copy's sales never landed in the original's register day at the main till. The original's register id still decided two things there:

- the **stock location** a replayed sale draws down (`pos.ResolveStockLocationID`);
- the `register_id` stored on the copy's replayed no-sale opens and cleared baskets.

## What shipped

- **`internal/enroll/restored.go`**
  - On a replica restore, `clearClonedIdentity` records the restore instant in `sync.restored_since` (UTC RFC3339). An existing value is kept, so a retried reset never moves it.
  - A main-till restore does not record it.
  - New `RestoredCopyMarks(ctx, kv)` returns the provisional `R<hex>-` prefix (only when it is valid) and `restored_since`.
- **`internal/pages/rejoin_replay.go`**
  - `replayUnsentJournal(…, registerID)` sends three kinds of entry under the register the new main till provisioned for this till (`register_id` from `/api/sync/enroll`; `""` from an older main till, which falls back to Main):
    - the copy's post-restore sales, identified by the receipt prefix;
    - its no-sale opens created at or after `restored_since`;
    - its cleared baskets created at or after `restored_since`.
  - Pre-restore entries (the original's unsent ones in the clone) keep their register.
  - A till that is not a restored copy sends exactly what it sent before.
  - Both live-join passes pass `out.Data.RegisterID`, and `replayStagedJoin` passes `id.RegisterID`.
- **`internal/db/replica.go`:** `ApplyReplicaIdentity` deletes `sync.restored_since`.
- **Checkout path untouched.** `ResolveTillRegisterID` still answers the original's register on the copy until it joins, so selling never blocks.
- **ut-docs:** journal contract 1.13.1 (replica-side changelog row, plus a note under `sale.register_id`).

## Tests

- **enroll**
  - `TestRestoreCheck_ReplicaRecordsRestoredSince`
  - `…RepeatedResetKeepsRestoredSince`
  - `…MainTillDoesNotRecordRestoredSince`
  - `TestRestoredCopyMarks_IgnoresARealPrefix`
- **db:** `TestApplyReplicaIdentityClearsRestoredSince`
- **pages:** four `TestReplayUnsentJournal_*` unit tests on the wire payload, and `TestRejoin_RestoredCopyEntriesLandOnTheNewRegister`. That one goes through the real join route and checks the main till's `no_sale_events` and `cleared_baskets` rows.

**Red/green.** The reviewer re-verified it independently in a separate worktree. With the fix reverted and minimal stubs added to compile, six behaviour tests fail. The four "must not change" guards pass both ways, as intended. All pass with the fix restored.

## Review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The cut-off is the copy's clock. If the original's clock ran ahead of the copy's and the restore followed the backup within that skew, a pre-backup no-sale or cleared basket could be moved to the new register. | **Accepted, follow-up card ut-docs#5185.** Local entries carry no `till_id` to tell them apart. A sound boundary needs the newest pre-restore `created_at` from the DB, which `enroll` has no access to. The window is a few minutes of clock skew at the moment of a restore, and only no-sale/cleared rows (not sales or money) are affected. |
| 2 | process | Review record and doc update missing | Fixed: this file; contract 1.13.1 in ut-docs. |
| 3 | nit | A copy restored by an older build has only the prefix, so its no-sale and cleared rows are not moved | Documented in the `rejoin_replay.go` comment block. |
| 4 | nit | A copy of a copy shares the R prefix (pre-existing from #5162) | Accepted; not caused here. |
| 5 | nit | The end-to-end test doesn't assert the sale's resolved stock location | Accepted. The register on the wire is asserted, and location resolution from a register id is covered by ut-docs#2067's tests. |
| 6 | nit | `restored_since` was re-parsed for every entry | Fixed (parsed once). |

The reviewer also checked:
- `created_at` formats match: both writers use UTC RFC3339 whole seconds.
- Receipt numbers really start with the prefix (`NextReceiptNo`).
- The marker never leaks: `sync.` is per-till, and it is cleared on join, promote and leave.
- Startup replay reads the pre-join DB.
- New SQL is only in `internal/db`.

## Not changed

- **Help (`web/help/*/multitill.md`).** The restored-device paragraph is still accurate ("its sales are sent first, under their own receipt numbers"). Which register the copy's entries count against is not something the owner acts on.
- **Out of scope (noted on the card):**
  - the copy's local shift continuing the cloned open shift;
  - its local Z over cloned same-day sales (numbering after a restore is ut-docs#5068).

## Verdict

Safe to merge once the full gate is green.
