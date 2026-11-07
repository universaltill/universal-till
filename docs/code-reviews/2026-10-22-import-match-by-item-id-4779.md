# Review: import job matches a my. export row by its item id (ut-docs#4779)

**Date:** 2026-10-22 · **Lane:** `lane:cloud-41` · **Card:** universaltill/ut-docs#4779
**Built by:** Sonnet (Dev subagent) · **Reviewed by:** Opus 5.5 (independent subagent, fresh context)

## What shipped
- **universal-till `catimport` (tag `catimport/v0.5.0`):** `ImportItem.ItemID` comes from the `id` column of our own my. export (source `my-export`, CSV). The cell is trimmed and CSV-defused, and dropped above 64 bytes or when it holds a control character or whitespace. It is not carried into the ADR-0051 `Row`.
- **ut-cloud:** `parsedRow.ItemID` → `claims.TillImportRow.ItemID`. The till-master planner matches the id first (ADR-0168 §3), then barcode, then SKU, then name.
  - An id that names one item while the row's barcode or SKU belongs to another is `conflicting_match`.
  - An id the read copy doesn't hold is ignored.
  - `markDuplicates` treats a repeated id as `duplicate_in_file` across the whole file, with no field, because no cell can clear it.
  - The pin is bumped to `catimport/v0.5.0`.
- **ut-docs:** ADR-0168 §2 is amended: the source is read from the header row, `source_choice` is display-only, and the `?source=` hint is dropped. §3 gains the id rules. Contract `cloud-import-jobs` goes to 1.3.0.

## Findings (independent review)
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Blocker | A repeated id in a later 100-row chunk updated the same item twice, with no issue raised. The planner's `p.targets` check lives for one chunk only. | **Fixed:** an id key in `markDuplicates`, plus test `TestMarkDuplicatesItemID` (the repeat sits past `ChunkRows`). The test failed before the fix and passes after. |
| 2 | Major | A re-imported `.xlsx` my. export carries no id; the XLSX path never runs the export extras. | **Deferred** to ut-docs#4847, filed this cycle. Every doc and comment now says "CSV". |
| 3 | Minor | Rows with distinct ids and the same name, both without SKU or barcode, were marked as name duplicates. | **Fixed** in the same change: the name key is skipped when the row has an id. |
| 4 | Minor | The id is not a correctable cell. | **Accepted and documented** (api-reference, contract): such a row is skipped, not corrected. |

Checked and fine (reviewer):
- CSV record ↔ item alignment in `readExportExtras`.
- `ItemID` survives the corrections overlay.
- `byID` is scoped to the store, so another store's id falls through.
- An id together with the same item's own barcode or SKU updates that item.
- The cloud-master path is untouched.
- `RowFor` stays as it was.

Accepted residual risk: two rows with *different unknown* ids and the same name, both with SKU and barcode cleared, can fall through to the same item by name in different chunks. That needs a member to blank both cells on two copied rows, and the id is unknown to this store in both, so it is not worth new code.

## Verified beyond the automated tests
- TDD re-verified by the orchestrator. With the planner's id case disabled, `TestTillImportMatchesByItemID` and `TestTillImportSameItemIDTwiceIsDuplicateInFile` fail ("created", not updated or duplicate). With it restored, they pass.
- `TestMarkDuplicatesItemID` fails before the `markDuplicates` fix (`""` code) and passes after.
- `TestTillMasterMatchesMyExportRowByID` runs a my. export CSV end to end through the job: 1 updated, 0 new.
- No UI surface changed, so there is no UX gate and no help-topic change.

## Verdict
Safe to merge once CI is green. The order is universal-till → tag `catimport/v0.5.0` → ut-cloud (pin) → ut-docs.
