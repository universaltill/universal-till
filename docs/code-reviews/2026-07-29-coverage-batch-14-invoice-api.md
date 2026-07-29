# Test coverage batch 14: invoice / credit-note API

2026-07-29

`internal/pages/invoice_page.go` — VAT-compliant invoice issuing, automatic
credit-note issuing on refund of an already-invoiced sale, the invoice
list/export/detail HTTP handlers.

## What's covered

- `formatQty`, `sellerConfig`'s off-until-name-set gate.
- `issueInvoice`: rejects when seller isn't configured, computes
  net/tax/gross correctly from the VAT breakdown (independently re-derived
  by review: net=100, tax=20, gross=120 — correct).
- `maybeIssueCreditNote`: auto-issues exactly once for a sale that was
  invoiced and is now refunded; no-ops for a refund of a sale that was
  never invoiced. Exercised with the original sale id passed directly, the
  same way the real caller does it (`refund_page.go`'s
  `POST /api/refund → maybeIssueCreditNote(ctx, d, newReceipt, detail.ID,
  actorID)`).
- `POST /api/invoices/issue`: validation (missing receipt/customer name,
  non-sale or incomplete sale rejected), success path, and idempotency on
  retry (second issue for the same sale returns the existing invoice, no
  duplicate row — backed by both an app-level check and the DB's
  `UNIQUE(sale_id, kind)` constraint).
- `GET /api/invoices`: redirect when seller unconfigured, manager-gating,
  listing with credit notes shown negated.
- `GET /api/invoices/export`: manager-gating, CSV correctness.
- `GET /api/invoices/{displayNo}`: not-found, and rendering an issued
  invoice.

`vatBreakdown` itself already has thorough coverage in `invoice_test.go`
(`TestVATBreakdownGroupsByRecordedRate`,
`TestVATBreakdownProratesSaleDiscount`, both tax-inclusive and -exclusive
modes) — not duplicated here.

## Schema gap fixed

`seedForPages`'s shared minimal test schema
(`internal/pages/ui_smoke_test.go`) had no `invoices` table. Added one
matching `internal/db/migrations/016_invoices.sql` column-for-column,
including both `UNIQUE(series, invoice_no)` and `UNIQUE(sale_id, kind)` as
inline table-level constraints (functionally and error-text equivalent to
the migration's separate `CREATE UNIQUE INDEX` statements — confirmed by
review).

## Independent review (opus) — two findings, both fixed before commit

1. **Misleading dead-weight seed row.** The credit-note test seeded a
   `sale_links` row and updated `sale_type` to `'return'`, implying
   `maybeIssueCreditNote` reads sale linkage to find the original sale.
   It doesn't — it takes the original sale id as a direct function
   argument, matching the real caller. Removed the unused `sale_links`
   insert and added a comment explaining why no linkage row is needed to
   exercise this path.
2. **Weak CSV assertion.** `TestGetInvoicesExport_CSVHasCreditNoteAsNegative`
   only substring-matched `"-1.20"` somewhere in the response body — a
   regression that negated *every* row (including the invoice row) would
   still have contained that substring and passed. Rewrote to parse the
   response with `encoding/csv` and assert the exact Gross column per row:
   invoice row `"1.20"`, credit_note row `"-1.20"`, both rows present.

Everything else — the idempotency guarantees (app-level + DB constraint),
the manager-gating, the VAT compute path — was verified line-for-line
against production and confirmed correct as originally written.

## Verification

`go build ./...`, `go test ./...`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass.
