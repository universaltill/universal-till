# Review — till-side sales-aggregate producer (ut-docs#2535)

- **Date:** 2026-09-24
- **Card:** universaltill/ut-docs#2535 (epic #2493; contract ADR-0111)
- **Author:** Opus 5.5 (Dev subagent); **Reviewer:** Fable (independent, worktree at `72047d4`)
- **Branch:** `feat/2535-sales-aggregate-upload`

## What shipped
- `internal/cloudsync/sales_aggregates.go` builds one rollup per `(business_date, till)` and POSTs it to `/v1/stores/sales-aggregates`. It runs from `Tick`'s registered, primary-only block, is throttled to 10 minutes and covers a 14-day lookback. It is off the sale path.
- Buckets: `hourly`, `by_payment_method`, `by_vat_rate` (the Z-report's own banding), `by_item_category` (per item, with the top modifier), `by_cashier` (staff **id** only, sent only when `reports.cloud_staff_breakdown` == `"true"`, default off per ADR-0111 §4), and the counters `refund_count`, `void_count` and `discount_count`. `no_sale_count` and `hours_on_till` are 0 for now (follow-ups #2558 and below).
- Till key = `till_id`, else `register_id`, else this till's marketplace device id.
- Migration `038_sales_aggregate_uploads.sql` adds a per-`(business_date, till_id)` content-hash ledger. A hash is recorded only on HTTP 200. On 402, 5xx or an auth/transport error the round stops and a later tick retries. On any other 4xx that day is skipped, so it can't wedge newer days. The ledger is excluded from replica sync.
- The pure EOD tax-banding helpers moved from `internal/pages` to `internal/pos`, because `pages` imports `cloudsync`. Pages delegates to them and behaviour is unchanged. `SalesForTaxBands` now shares its body with a till-scoped variant.
- `post()` returns a typed `*statusError`. Its message is unchanged for existing callers.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| — | major (orchestrator, pre-review) | Every error stopped the round, so a permanently rejected rollup (400) blocked all newer days for 14 days | **Fixed**: non-retryable 4xx skips that day (`rejectedRollup`). Test `TestPushSalesAggregates_RejectedDaySkippedNotBlocking` was written first and seen failing (1 post, want 2) |
| 1 | minor | `expected_cash_minor` included cash tips, unlike the Z-report's `CashSales` (#1046) | **Fixed**: `ExpectedCash = Amount − Tips`. Test `TestSalesAggregateForTill_ExpectedCashHoldsTipsOut` was written first and seen failing (1100, want 1000) |
| 2 | minor | `net_sales_minor` semantics unspecified (VAT-inclusive, returns not netted) | **Documented** on the DTO and in ADR-0111's wire notes (ut-docs PR) |
| 3 | minor | `amount_minor` includes tips and `tips_minor` is sent too | **Documented** that tips are a subset, not additive (matches EOD Methods) |
| 4 | minor | Every round rebuilds 14 days × tills before the hash check (~9 queries each) | **Deferred**: Backlog card (a cheap per-day fingerprint gate). Off the sale path |
| 5 | nit | Code cited ADR-0110 (the manage-shop SPA ADR) instead of ADR-0111 | **Fixed** throughout the till diff |
| 6 | nit | Throttle was Load-then-Store | **Fixed**: CompareAndSwap |
| 7 | nit | No UI for the staff-breakdown toggle | **Deferred**: #2557 |
| 8 | nit | A device-id change would re-key "self" days and double-count in the cloud | **Documented** in ADR-0111's notes. Edge case, since the device id is persisted at enrolment |

The reviewer's clean checks cover till scoping (a sale lands under exactly one key and is never dropped), local date vs hour, refund/void/tip consistency with EOD, VAT parity (`TestSalesAggregateVATBands_MatchEODForSingleTill` runs through the real engine), a byte-for-byte hash-over-payload check, privacy (`users` is never joined, logs carry only date/till/status), and a field-for-field contract match.

## TDD re-verification (reviewer, in its own worktree)
- `TestPushSalesAggregates_RejectedDaySkippedNotBlocking`: `continue`→`return` made it fail, and restoring it made it pass.
- `TestSalesAggregateForTill_Buckets`: breaking the returns netting in the payments query made it fail (cash 11.90 vs 6.55), and restoring it made it pass.

## Verified beyond the unit tests
- The till's JSON tag set equals the ut-cloud handler and schema tag set (34 tags, no `omitempty`), so empty dimensions encode as `[]`.
- Full gate after the last edit: `gofmt -l` clean; `go build ./...` and `go vet ./internal/...` pass; `golangci-lint` reports 0 issues; `go test ./...` passes; `go test -race ./internal/cloudsync/` passes; the data-access, migration-collision, i18n, kiosk-engine, compliance, competitor-naming, help-topics and deadcode guards all pass.
- Not verified: an end-to-end upload against a live ut-cloud with an active subscription. That was only tested against an `httptest` cloud. There is no UI surface, so there was nothing to screenshot.

## Verdict
Safe to merge.
