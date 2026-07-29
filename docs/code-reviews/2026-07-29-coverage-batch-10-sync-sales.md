# Test coverage batch 10: LAN sync sales journal (ADR-0011)

2026-07-29

`internal/pages/sync_sales.go` — the primary/replica sales-journal
mechanism: a replica journals its local sales to the primary, which
replays them through the same `pos.CompleteSale` engine using the
ORIGINAL sale ids/receipts, so replaying a journal batch twice is a
no-op. This idempotency is what makes it safe to retry a sync push after
a dropped connection without double-counting stock or double-charging a
customer — high business risk if wrong.

## What changed

`internal/pages/sync_sales_test.go` (new): `buildJournal` (including
return-linkage via a real `sale_links` row, not mocked), `applyJournal`'s
idempotency (first apply succeeds and creates the sale with correct
till-provenance stamping; a second apply of the identical journal entry
is a genuine no-op — verified by `SELECT COUNT(*)` staying at 1, not just
checking the return value), and the `POST /api/sync/sales` handler
(unauthorized rejection, batch-size cap, and the same idempotency
guarantee at the HTTP layer — a retried push reports `applied:0,
skipped:1`, not an error or a duplicate).

`internal/pages/ui_smoke_test.go`: added `tills` and `sales.till_id` to
the shared fixture (both missing until now, needed for sync provenance).

## Independent review (opus)

Traced `applyJournal`'s code path to confirm the `SaleExists` idempotency
check happens BEFORE any side effect (stock movements, audit, links,
provenance stamping) — a duplicate replay does literally nothing, not a
partial re-application. Confirmed the return-linkage test exercises the
real `OriginalSaleIDFor` SQL (via a genuine `sale_links` row), not a
mock. Confirmed both schema additions match the real migrations exactly.
Agreed that leaving `StartSyncPush` (the replica-side background polling
loop, real HTTP client) untested is a reasonable scope decision — its
core building block (`buildJournal`) is directly tested, and per ADR-0003
checkout never depends on it. No blocking findings; two minor polish
nitpicks noted (an explicit stock-movement-count assertion would make
the "no double-counting" claim more self-evident, though it's already
guaranteed by construction; the unauthorized-401 test doesn't enrol a
till first, which is fine but slightly less targeted).

## Verification

`go build ./...`, `go test ./...`, `go test ./internal/pages/... -run
"TestBuildJournal|TestApplyJournal|TestSyncSalesAPI" -count=5` (no
flakiness), `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass.

## Coverage delta

`internal/pages/sync_sales.go`: `buildJournal` 100%, `applyJournal`
65.2%, `registerSyncSales` 88%. `StartSyncPush` remains 0% (background
loop, deferred per the scope decision above).
