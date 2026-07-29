# Test coverage batch 15: POS API core checkout handlers

2026-07-29

`internal/pages/pos_api.go` — the core checkout flow: basket line
mutation (`/api/pos/remove`, `/api/pos/line`, `/api/pos/discount`,
`/api/pos/order-type`, `/api/pos/reset`), and the tender/completion
pipeline (`/api/pos/tender`, `completeTender`).

Pre-existing coverage in the same package (`pos_scan_test.go`,
`pos_promo_test.go`, `pos_status_test.go`, `journal_test.go`) already
exercised `/api/pos/scan`, promo barcodes, `/api/pos/sale/status`
(park/void), and one online offline-tender happy path — not duplicated
here.

## What's covered (new)

- `/api/pos/remove`: by line key, by legacy code, missing-both 400.
- `/api/pos/line`: qty update, discount update, missing-both 400.
- `/api/pos/discount`: sale-level discount.
- `/api/pos/order-type`: takeaway toggle and reset back to dine-in.
- `/api/pos/reset`, `/ui/clear-toast`.
- `/api/pos/tender`: `Accept: application/json` response (asserting the
  DB-read `saleId`/`receiptNo`/`total`, plus the stored `subtotal`/
  `tax_total` split, not just an echoed input); malformed-JSON-body 400;
  quick-tender form-encoded fallback (`method`/`amount` fields); empty
  basket rejected; `simulateFailure` (502 + `payment_failed` audit row +
  basket survives for retry); insufficient stock (in-place toast, HTTP
  200 not an error status, zero sale rows written, basket survives).
- Pure helpers: `looksLikeCustomerCode`, `normalizeLegalLines`,
  `stockMovementReason`.

## Independent review (opus) — one real false-positive fixed, two cheap gaps closed

1. **False-positive in the quick-tender fallback test.** The original
   version tendered `method=cash` and asserted a `cash` payment row
   exists — but `/api/pos/tender` falls back to a default **cash**
   payment whenever no payment method parses at all (pos_api.go's own
   comment: a prior real bug where "every quick-tender button silently
   recorded cash whatever method was tapped"). A `cash` tender in the
   test would pass even if form-method parsing were broken again.
   Fixed: tender `method=card` and assert a `card` row, which can only
   exist if the form value was actually read.
2. **Added a stored subtotal/tax-total assertion** to the JSON-accept
   tender test — it previously only checked `total`, which a mis-split
   subtotal/tax bug could still add up to correctly.
3. **Added a malformed-JSON-body 400 test** for `/api/pos/tender`
   (previously untested).

Also independently verified: the tax-inclusive-false money math behind
every hardcoded `120` total (£1.00 item, 20% VAT, exclusive: `100 + 20 =
120`); that the insufficient-stock test's exact-total payment really
reaches the stock-check branch inside `CompleteSale`'s transaction
(rather than being rejected earlier by the payment-coverage check); and
that each test's fresh DB/engine (`t.Cleanup`) means no state leaks
between them.

Nitpicks fixed: the order-type reset assertion now pins the exact
value (`== ""`) instead of only checking it isn't takeaway; a
request/recorder variable naming swap in the reset-handler test was
un-swapped for consistency with every other test in the file; a
one-line comment was added explaining why the insufficient-stock test's
payment amount must exactly cover the requested (oversized) total.

The `paymentDeclined` → 402 path was scoped out: it requires a plugin
subscribed to `payment.<method>.authorize`, which isn't cheap to stand
up in this fixture — left as a known gap, not a regression risk today.

## Verification

`go build ./...`, `go test ./...`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass.
