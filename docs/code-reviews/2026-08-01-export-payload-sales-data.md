# Code review: export/report dispatcher payload gains real sale/tax/payment data

**Date:** 2026-08-01
**Branch:** `feat/export-payload-sales-data`
**Closes:** universaltill/ut-docs#221 (split off ut-docs#41's DATEV/FiBu scoping pass; unblocks ut-docs#41 and, once ut-docs#38's TSE decision lands, ut-docs#39)
**Reviewer:** independent subagent, model override `opus` (different from the implementing session's model)

## What shipped

The `export.requested.ask` plugin dispatcher (ut-docs#189, universal-till#136) wired the trigger/response plumbing for an installed `export`/`report`-type plugin, but the request payload it sent was `{from, to, entry_key}` only — no sale data at all, so no plugin could ever build a real export file. This change closes that gap:

- **`internal/data/export_repo.go`** (new): `POSRepo.SalesForExport(ctx, from, to string) ([]ExportSaleRow, error)` — for an arbitrary `[from, to]` date range (inclusive of the whole final day, via the same `to += "￿"` lexicographic trick `InvoiceRepo.List` already uses), returns each completed, non-return sale's receipt number, timestamp, total, per-tax-band net/tax (grouped by `tax_rate_bp`), and per-payment-method amount (grouped by `method_id`, net of `change_given`). All monetary fields are `money.Money`, wrapped via `money.FromMinor` at the SQL-scan boundary; `RateBP` stays a plain `int64` (basis points, never money), consistent with the project's money rules.
- **`internal/pages/data_api.go`**: `exportRequestPayload` gains a `Sales []data.ExportSaleRow` field; the existing manager-gated `POST /api/data/export` handler populates it via the new repo method before calling `AskPlugin`, after its existing `from<=to` validation.
- **`internal/plugins/wasm_runtime.go`**: new `timeoutFor(pluginID, eventType)` helper gives `export.requested.ask` specifically a 30s floor (vs. the existing 2s default / 10s with network permission), since a real export payload is far larger than a `tax.rate.ask` answer. Every other event type, including `tax.rate.ask`, is unaffected — this is an additive floor, not a replacement of the existing net-permission widening.
- **`internal/plugins/testdata/export_guest/main.go`**: the real WASM test guest now decodes the actual `sales[]` payload shape and echoes a count/sum, so the existing real-wazero regression test proves genuine data arrives in a real compiled module, not just that Go structs compile.

## Independent review

Spawned a `general-purpose` subagent with `model: opus` — a different model from the one running this pipeline session — briefed with the exact diff scope, the relevant `CLAUDE.md` rules (repository pattern, money, offline-first, plugin signing), and told explicitly to run the full gate and mutation-test the new tests itself rather than trust the Dev/Tester steps' word.

**Real defect found and fixed (should-fix):** every fixture in the original `export_repo_test.go` seeded exactly one `sale_line` and one `payment` per sale, so both `GROUP BY` clauses (`tax_rate_bp`, `method_id`) and the `change_given` netting were never actually exercised — deleting both `GROUP BY`s, or turning `SUM(amount - change_given)` into `SUM(amount)`, passed the entire suite silently. The reviewer:
- Corrected a test comment that falsely claimed "two tax rates, split cash/card payment" coverage the fixture couldn't produce.
- Added `TestSalesForExport_GroupsBandsAndMethods`: a sale with 3 lines across 2 tax bands and 3 payments across cash/card (including a cash overpayment with `change_given`), asserting exact per-band and per-method totals. Confirmed (by re-deleting the `GROUP BY`s / netting after adding the test) that it now fails correctly where it previously passed silently.

**Everything else checked out clean**, independently re-verified (not taken on the Dev/Tester steps' word) via 5 additional mutations, each producing a genuine assertion failure and reverted afterward: the `sale_type='sale'` filter, the `to += "￿"` inclusive-day boundary, the handler actually populating `Sales`, `timeoutFor`'s exact-match (not suffix-match) event-type gating, and the WASM guest's real aggregation. Also confirmed: no raw SQL outside `internal/data`; return/refund rows structurally cannot leak into the tax-line or payment sub-queries (they're scoped by already-filtered `sale_id`s, not independently re-filtered); `payments.change_given` is `NOT NULL DEFAULT 0` so the netting `SUM` is null-safe; the new timeout logic doesn't widen any other event class; no file-write path exists in this diff (so the "missing `os.MkdirAll`"/"cwd-relative path instead of `paths.Data`" bug classes don't apply); the new payload is never logged in full (only stdout/stderr and an audit envelope with no payload field are logged); no real client/shop name anywhere in fixtures.

**Full gate, before and after the reviewer's fix:** `go build ./...`, `go vet ./...` — pass. `go test ./... -count=1` — one failure, `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`, confirmed via `git stash` to fail identically against the pre-existing base commit (container runs as uid 0, so the test's read-only-directory assumption doesn't hold) — pre-existing and unrelated to this diff, not a regression. `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh` — pass.

## Verified beyond automated tests

- Read `internal/db/migrations/001_init.sql` directly to confirm every column referenced (`sales.receipt_no/status/sale_type/created_at/total`, `sale_lines.tax_rate_bp/tax_amount/total_before_tax`, `payments.method_id/amount/change_given`) is real, not hallucinated, and that the joins match the actual foreign keys.
- Ran the full Playwright e2e suite (24/25 passed; the one failure, an unrelated pre-existing image-loading flake in `catalog-image-to-till.spec.ts`, reproduces identically on the pre-existing base commit) to confirm no UI regression, even though this change touches no UI/i18n files.
- Personally re-ran 5 of the reviewer's mutation tests to confirm the failure messages reported were genuine (not paraphrased) before writing this record.

## Deferred (real, out of scope for this card — tracked as new Backlog cards, not silently dropped)

- **`sales:read` permission not checked before handing over the ledger** (ut-docs#228) — `AskPlugin` today gates only on `events:receive`; before this change that was proportionate to a `{from,to,entry_key}` payload, but the payload now carries a full sale/tax/payment ledger for the range. A `sales:read` permission already exists in the manifest/permission system; wiring it in is an Architect-level call (it could break an already-installed export plugin that only declared `events:receive`) plus a `plugin-manifest.md` update, not something to slip into this diff unilaterally.
- **Unbounded payload + N+1 queries per date range** (ut-docs#229) — two extra queries per matched sale, all buffered in memory and JSON-marshalled whole into a WASM guest's stdin, with no cap on the requested date span. Fine for a small shop's month-end export; a year-long range on a busy till could mean ~100k queries and a very large buffer. Fix shape: range-scoped joined queries instead of per-sale sub-queries, plus a max-span/row cap on the handler.
- Nits noted, not acted on: an empty range marshals `sales: null` rather than `[]` (harmless for the Go guest here, could surprise a non-Go guest later); `tip_amount` is correctly excluded from payment totals per existing `PaymentBreakdown` precedent, but a real fiscal export will eventually need it surfaced; no `currency` field despite `sales.currency` existing per-sale, relevant once a multi-currency export plugin exists.

## Verdict

**Safe to merge.** No blocking findings; the one real defect (untested aggregation) was fixed and re-verified; all deferred items are genuinely separable and now tracked on the board.
