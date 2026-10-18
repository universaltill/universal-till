# Review — sales-aggregate fingerprint gate (ut-docs#2559)

- **Date:** 2026-10-18
- **Card:** universaltill/ut-docs#2559, a follow-up to #2535's review finding 4 (contract ADR-0111)
- **Author:** Opus 5.5 (inline, lane:cloud-41). **Reviewer:** Sonnet 5.5, independent, in a detached worktree at `5736a50`. Fable was the mapped reviewer, but its call was refused (out of usage credits), so Sonnet stood in per MODEL-ROUTING.
- **Branch:** `perf/2559-sales-aggregate-fingerprint`

## What shipped
- Migration `085_sales_aggregate_fingerprint.sql` adds `sales_aggregate_uploads.source_fingerprint` (`''` = unknown). Its checksum is pinned.
- `POSRepo.SalesAggregateFingerprints(from, to, selfTill)` is one query. It returns `SalesAggregateKeys`' exact key set and order. Each key carries a digest of its completed sales (count, Σ total, latest `created_at`, returns), its voids on the void day (count, latest `voided_at`) and its no-sale opens (count, latest `created_at`).
- `POSRepo.SalesAggregateUploads(from, to)` reads the window's ledger in one query. `RecordSalesAggregateUpload` now also stores the fingerprint. `SetSalesAggregateFingerprint` stamps a day whose rebuilt hash matched, leaving `content_hash` and `uploaded_at` unchanged.
- `pushSalesAggregates` gates each key before rebuilding it:
  - The stored fingerprint is sha256 of: build version, store id, self till, staff toggle, the round's local date, and the source digest.
  - A past day whose fingerprint matches is skipped. Today is always rebuilt.
  - The content hash still decides whether anything is sent.
  - The fingerprint is read **before** the build. A row that lands in between leaves an old fingerprint beside newer content, which costs one more rebuild next round. Reading it after the build could pin stale content.
- The salt includes today's date, so every window day is rebuilt once a day. Anything the source digest misses is stale for at most a day.
- **Ledger-only keys are revisited.** A day the cloud holds a rollup for, but whose source rows are all gone, is rebuilt with an empty source. Examples: a past day whose only sale was later voided, or sales that were deleted or archived. The cloud then gets the emptied rollup.
  - This closes a gap that existed before this change: the old key list dropped such a day, so the cloud kept the voided sale's figures. Found by this card's own test.
  - The same mechanism zeroes "self" keys left behind by a device-id change. That removes the double count #2535 review finding 8 documented, within the 14-day window.
- `docs/data-model.md` is updated.

## Write-path audit (what can change a past day's rollup)
- Sale, lines, payments, modifiers and discounts are written in one `CompleteSale` transaction, and no code updates or deletes the child rows later. The sale count therefore covers the children.
- Of the `UPDATE sales` statements, only `UpdateSaleStatus` (a void) and `SetSaleProvenance` (a journaled sale moved to its origin day and till) touch rollup inputs. Both move the counts on the affected keys.
- The other `sales` updates touch columns the rollup never reads: `order_status`, print-failed timestamps, `tracking_token`, `customer_id` and the sync fields. A test asserts they leave every fingerprint alone.
- `item_variants.item_id` is never updated. Voucher issue amounts are set at insert.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| — | major (author, during TDD) | A past day whose only sale was voided later dropped out of the key list, so the cloud kept its old figures. This predates the change, and the gate made it visible | **Fixed**: ledger-only keys are revisited with an empty source. Covered by the "late void" step of `TestPushSalesAggregates_FingerprintGateSkipsUnchangedPastDays`, which failed before the fix (built only today, want yesterday + today) |
| 1 | minor | A failed `SetSalesAggregateFingerprint` stamp aborted the round, although the stamp is only an optimisation | **Fixed**: log and carry on |
| 2 | minor | If the cloud rejected an empty rollup with a final 4xx, that key would rebuild every round until it left the window | **Not reachable**: ut-cloud's `salesAggregateUpload.validate` accepts zero-figure rollups (it only checks date, till id and non-negative fields). A rejected non-empty day already behaved this way (bounded by the window) |
| 3 | minor | Deleting or archiving in-window sales (or a self-till change) now sends zeroed rollups for those days | **Accepted, intended**: the cloud should mirror what the till holds, and this also removes a double count. Documented above |
| 4 | nit | The ledger read had no upper bound, so a row dated after `to` (clock skew) was revisited once | **Fixed**: `SalesAggregateUploads(from, to)`, and the test plants a future row |

## Verification
- **TDD.**
  - The data tests ran against the old repo file and failed to compile (`RecordSalesAggregateUpload` arity).
  - The cloudsync tests failed to compile before the gate existed, then failed behaviourally on the late-void step before the ledger-only revisit.
  - The reviewer independently reverted both implementation files in its worktree, saw the compile failures, restored them and saw everything pass.
- **New tests:**
  - `TestSalesAggregateFingerprints_SameKeysAsSalesAggregateKeys`
  - `TestSalesAggregateFingerprints_MoveOnlyWithSourceRows` (late sale, void, no-sale, delete, unrelated columns)
  - `TestSalesAggregateUploads_LedgerAndFingerprint`
  - `TestPushSalesAggregates_FingerprintGateSkipsUnchangedPastDays`, which counts builds through `buildSalesAggregateFn`. It covers an unchanged round, a late void, a late no-sale, the staff toggle, and a backfill of empty fingerprints.
  - `TestSalesAggregateFingerprint_EverySaltInputMovesIt`
- **Gates:**
  - `gofmt` clean, `go build ./...`, `go vet`, `golangci-lint` 0 issues.
  - Every build-job guard passes. The exceptions are `guard-shellcheck-version.sh` (no shellcheck binary in the container) and two non-guard helpers the extraction picked up.
  - Full `go test ./...` on the final tree.
- **Not driven in a running app.** This is backend-only with no UI surface, and the push is exercised end to end against an `httptest` cloud on a real migrated SQLite DB.

## Verdict
Safe to merge.
