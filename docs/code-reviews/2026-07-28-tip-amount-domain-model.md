# 2026-07-28 — Tip amount on the core sale/payment domain model

## Context
`docs/germany-pos-parity-backlog.md` ("Tip flow, confirmed from the video"
and "🟠 Tips: SumUp reader → till auto-sync") confirmed Universal Till's
core domain model has **zero tip concept** anywhere in `internal/`. This
blocks `ut-plugin-payment-sumup` (external repo): SumUp's Solo reader
prompts the customer for a tip and the Cloud API returns it on the
transaction result, but there was nowhere in the till's data model to put
that value. This change adds the missing `tip_amount` concept end to end —
persistence, tender-flow input, receipt display (screen + thermal print) —
scoped exactly to that gap. No tip-splitting, no tip-pooling, no cash-tip
flow, no reporting/export line (that's a follow-up, flagged but
deliberately out of scope here).

## Where the field lives, and why
`tip_amount` was added to **`payments`**, not `sales`. A sale can have
split/multiple payments (cash + card), and tipping is inherently a
per-card-transaction thing (the SumUp reader returns a tip for *that*
transaction). Mirroring the existing `change_given` column (also
per-payment, also metadata riding on top of `amount`) keeps the same
shape the codebase already uses rather than inventing a new pattern.

**Judgment call, the one most likely to matter for review**: `tip_amount`
is purely additive metadata. It does **not** participate in
`netPayments`'s payment-coverage check, and it does **not** touch
`computeSaleTotals` (subtotal/tax/total math) anywhere. A card charge of
"total + tip" is expected to arrive as `Amount = total + tip`,
`TipAmount = tip`; the sale's own `total` stays merchandise-only. This was
a deliberate choice to avoid touching the sale-total codepath at all,
per the instruction that this may need to be reconciled with a parallel
PR touching the same area — the diff to `computeSaleTotals`/`netPayments`
is exactly one added validation line (`TipAmount` must be `>= 0`), nothing
that changes what a payment is judged to cover. It also matches the
backlog note that tips are "often handled separately for German
payroll/tax purposes" — i.e. they're not meant to be folded into revenue
totals in the first place.

## What changed
- **`internal/db/migrations/019_payment_tip_amount.sql`** (new, append-only):
  `ALTER TABLE payments ADD COLUMN tip_amount INTEGER NOT NULL DEFAULT 0`.
- **`internal/pos/sales.go`**: `PaymentInput.TipAmount money.Money`;
  `netPayments` rejects a negative tip; `CompleteSale` passes it through
  to `InsertPayment`. Sale-total computation (`computeSaleTotals`)
  untouched.
- **`internal/data/pos_repo.go`**: `InsertPayment` takes a `tipAmount
  int64` param (positional — its one call site in `internal/pos/sales.go`
  was updated); `SaleDetailPayment.TipAmount`; `GetSaleDetail` selects
  `tip_amount`.
- **`internal/pages/pos_api.go`**: `/api/pos/tender`'s JSON payment
  payload gains an optional `tip` field (same shape as the existing cash
  `change` field) wired to `PaymentInput.TipAmount`; `receiptPayment` view
  struct and `renderReceipt` carry it to the on-screen receipt template.
- **`internal/pages/print_api.go`**: `buildReceiptDoc` (thermal ESC/POS
  path) adds a `Tip` line under a payment when `TipAmount > 0` — same
  hardcoded-English style as the sibling `Change` line in that function
  (this doc is built with a fixed `locale := "en"`, not `T`-wrapped, by
  existing design — see the comment already in that file about RTL
  needing bitmap mode).
- **`internal/pages/sync_sales.go`**: the LAN replica→primary sale-journal
  replay (`ADR-0011`) now carries `TipAmount` through when re-applying a
  synced sale via `CompleteSale`, so an offline sale with a tip replicates
  correctly — same treatment as the existing `ChangeGiven` passthrough
  right next to it.
- **`web/ui/partials/receipt.html`**: a `{{ if gt .Tip 0 }}` line next to
  the existing change line, through `{{ T "receipt.tip" }}`.
- **`web/locales/{en,ar,fa,tr}.json`**: added `receipt.tip` to all four
  locales (en: "Tip", ar: "الإكرامية", fa: "انعام", tr: "Bahşiş" —
  ar/fa/tr are reasonable machine translations, not verified by a native
  speaker; flagging per this repo's i18n rule that an imperfect
  translation is acceptable but a missing key is not).
- Five inline test-schema `payments` table definitions (`internal/pos/{sales,shifts,offline_resilience,performance}_test.go`,
  `internal/pages/ui_smoke_test.go`) gained the `tip_amount` column so
  `InsertPayment`'s new bound parameter doesn't break them — these tests
  build their own SQLite schema rather than running real migrations.

## Tests added
- `internal/pos/sales_test.go`:
  `TestCompleteSale_PersistsTipAmountWithoutAffectingTotal` (tip persists,
  sale `total` stays merchandise-only) and
  `TestCompleteSale_RejectsNegativeTip`.
- `internal/data/pos_repo_tip_amount_test.go` (new file, repository
  round-trip, same pattern as `pos_repo_sale_detail_test.go` — real
  `db.Open` + real migrations, not a hand-rolled schema):
  `TestPOSRepo_TipAmount_RoundTrips` (420 charged = 370 total + 50 tip;
  confirms `InsertPayment` → `GetSaleDetail` round-trips `tip_amount` and
  the sale total is unaffected) and
  `TestPOSRepo_TipAmount_DefaultsToZero` (no-tip payment defaults cleanly,
  not NULL).

## Verification
- `go build ./...` — clean.
- `go test ./...` — all packages pass, including the two new tip tests
  and every pre-existing payment-related test (change/discount/offline/
  split-payment/receipt-conflict-retry suites in `internal/pos` and
  `internal/data`) unaffected.
- `gofmt -l` on every changed Go file — clean.
- `go vet ./...` — clean.
- `bash scripts/ci/guard-data-access.sh` — clean (all new SQL is in
  `internal/data`/`internal/db`, none introduced elsewhere).
- `bash scripts/ci/guard-i18n.sh` — clean (772 template keys resolve, all
  four locales match `en.json`'s key set exactly).

## Explicitly not done (by scope)
- Nothing in `ut-plugin-payment-sumup` itself (external repo) — this PR
  only makes the field exist for that plugin to eventually write to via
  `/api/pos/tender`'s new `tip` field.
- No reporting/export line for tips (backlog flags this as a likely need,
  "often handled separately for German payroll/tax purposes" — worth its
  own accountant-verified spike, not guessed at here).
- No richer feedback loop from a blocking `payment.<method>.authorize`
  plugin event back into the tender call (the event bus's `Publish`
  return value is currently discarded at the one call site in
  `completeTender`) — the tip amount flows through the tender endpoint's
  existing request/response shape only, mirroring how cash `change`
  already works. A tighter reader-driven live-tip callback is a separate,
  larger change to the plugin event bus, out of scope here.
