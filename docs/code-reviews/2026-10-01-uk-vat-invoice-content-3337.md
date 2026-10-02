# Code review — UK VAT-invoice content on the receipt and the full invoice (ut-docs#3337)

- **Date:** 2026-10-01 / 2026-10-02
- **Ticket:** ut-docs#3337 (`complexity:medium`), part of ut-docs#3265 (UK
  statutory obligations register) and ut-docs#1017.
- **Branch:** `feat/3337-uk-vat-invoice-content`
- **Author:** Opus 5.5 subagent (this cycle's build model for
  `complexity:medium`).
- **Reviewer:** independent pass, fresh-context Fable subagent in an
  isolated worktree (no visibility into the implementation reasoning), per
  `MODEL-ROUTING.md`'s medium tier (Fable, never the author's model).
- **Verdict: SAFE TO MERGE**, after fixing the two should-fix findings the
  review turned up. No blockers.

## The gap

VAT Regulations 1995 (SI 1995/2518), Part III, restated in HMRC VAT Notice
700/21 and 700 §16 (sourcing caveat: gov.uk/legislation.gov.uk were not
fetchable when the card was filed; the content is from search-engine
extracts, UNCONFIRMED first-hand — see the card and `reference/uk-compliance.md`
§4): a simplified VAT invoice needs the seller's VAT number and, per VAT
rate, the VAT-inclusive total and the rate; a full VAT invoice additionally
needs unit price excl. VAT, rate per line, and the time of supply where it
differs from the issue date.

- `internal/pages/print_api.go`'s `buildReceiptDoc` printed no seller VAT
  number and no per-rate VAT breakdown; `receipt.show_tax=false` hid even
  the lump Subtotal/Tax rows.
- `internal/pages/invoice_page.go`'s `buildInvoiceDoc` (thermal) and
  `web/ui/pages/invoice.html` (on-screen) printed each line with only
  Name/Qty/gross Amount, and only the invoice's issue date — never the
  sale's own date.

## What shipped

Core only (`universal-till`), no plugin involvement — per ADR-0050 and the
card's own scoping, this is document *shape*, not jurisdiction-specific
content; any shop with `invoice.seller_vat_no` configured gets it.

- `internal/print/escpos.go`: `Line` gained an optional `Sub string` field
  (a second, word-wrapped row under Name/Amount; empty = unchanged output),
  rendered by `layoutLine` for both `Render` and `RenderText`.
- `internal/pages/print_api.go` `buildReceiptDoc`: when `invoice.seller_vat_no`
  is set, appends the seller address (deduped against the receipt header)
  and `"VAT No: <no>"` to Meta, and one `doc.Totals` row per VAT band from
  `vatBreakdown(detail)` (reusing the existing invoice/EOD banding
  function) labelled `"Total incl. VAT X%"` — printed regardless of
  `receipt.show_tax`, which keeps gating only the pre-existing lump
  Subtotal/Tax rows. No VAT number configured → byte-identical to before
  (regression-tested).
- `internal/pages/invoice_page.go` `buildInvoiceDoc`: new
  `invoiceLineUnitNet` (`(LineTotal-TaxAmount)/Qty`, rounded half away from
  zero, the same rule `money.Money.MulQty` uses) and `invoiceLineSub` (the
  "unit price excl. VAT · VAT rate%" text, shared by the thermal and
  on-screen paths — " - " separator on paper since not every ESC/POS
  charset carries "·", " · " on screen) set per-line `Sub`. New
  `timeOfSupply` prints the sale's own date only when its formatted value
  differs from the invoice's issued-at date. `GET /invoice/{display_no}`
  now passes a `Lines` (with `Sub`) list and `TimeOfSupply` to the template.
- `web/ui/pages/invoice.html`: mirrors both additions — a muted per-line
  sub-row and a time-of-supply paragraph next to the issued-at date.
- `web/locales/{en,ar,fa,tr}.json`: new keys `invoice.unit_price`,
  `invoice.time_of_supply`, and (added during review, see S2 below)
  `invoice.after_discount` — real translations, not machine-literal.
- `web/help/{en,ar,fa,tr}/invoices.md`: a paragraph describing what the
  documents now show, explicitly not claiming this makes a shop's
  invoicing legally sufficient (card's 4th acceptance criterion) — "it is
  still for you or your accountant to check" (and locale equivalents).
- `web/help/img/manifest.json`: regenerated via `make docs-shots` — no PNG
  changed (the `invoices` topic's shot is of `/invoices`, the register
  page, not `/invoice/{no}`), only the content hash for the `invoices`
  topic across all four locales.
- Tests: `internal/print/escpos_test.go`, `internal/pages/print_api_test.go`
  (incl. a `seedMixedRateSale` helper: 0%-rate + 20%-rate lines),
  `internal/pages/invoice_page_test.go`.

## Review findings

**S1 (SHOULD-FIX, fixed) — the seller VAT number printed twice when the
receipt header already carried it.** `headerHasLine` deduped the *address*
against the header (exact-line match) but the VAT-number Meta line was
appended unconditionally. A UK VAT-registered shop almost certainly already
has its VAT number in a header line today (the only way to print it before
this card) — on upgrade, every receipt would show it twice. Reproduced live
in the review's own driven probe. **Fix:** new `headerContains` helper
(case-insensitive substring match against `seller.VATNo` itself, not a
whole-line match — the header line is free text like `"VAT No: GB123456789"`
or `"VAT GB123456789"`, never guaranteed byte-identical to the new Meta
line). New regression test
`TestBuildReceiptDoc_SellerVATNoNotDuplicatedWhenInHeader`; reverted and
confirmed it fails with the real duplicated line, restored, confirmed green.

**S2 (SHOULD-FIX, fixed) — "unit price (excl. VAT)" silently showed the
post-discount effective price, not the catalogue unit price.** `(LineTotal-
TaxAmount)/Qty` on a discounted line is net ÷ qty *after* the line discount
— reproduced in review: 3× £12.00 incl. 20% VAT with a £6.00 line discount
shows `£8.33`, while the undiscounted catalogue unit excl. VAT is £10.00,
with no discount marker anywhere on the invoice. Not a money error (totals
still reconcile), but a figure labelled "unit price" on a VAT-invoice
document that is really "net ÷ qty after discount" is exactly the kind of
thing an accountant queries. **Fix:** `invoiceLineSub` appends a localized
`" (after discount)"` marker (new key `invoice.after_discount`, en/ar/fa/tr)
whenever `l.LineDiscount != 0`. New test `TestInvoiceLineSub_AfterDiscountMarker`
(discounted line gets the marker; undiscounted line does not — regression
guard against always appending it); reverted and confirmed it fails with
the real unmarked text, restored, confirmed green.

## Deferred (not this card, logged for follow-through)

- A **whole-sale** discount is prorated into the VAT bands (pre-existing
  `vatBreakdown` behaviour, correct for the bands) but the invoice has no
  discount row at all, so the per-line totals and the new per-line unit
  prices don't visibly reconcile with the discounted VAT table a reader
  sees below them. Pre-existing, not introduced by this card, not fixed
  here to avoid widening it — worth a Backlog card.
- N1: `web/help/en/designer.md`'s "Show subtotal and tax lines" description
  doesn't mention that the new per-rate rows print regardless once a VAT
  number is set (only `invoices.md` says so) — a one-sentence addition in
  all four locales would help, not filed as a card (cosmetic doc nit).
- N2: the receipt's new VAT block gates on `seller.VATNo != ""` alone,
  while `invoice.md`'s help text implies the whole feature turns off when
  the seller *name* is cleared (invoicing's own gate). Minor
  inconsistency, not acted on this cycle.
- Playwright e2e: no spec exists for `/invoice/{display_no}` at all,
  before or after this card (`grep -rl "/invoice" e2e/tests/*.ts` → none).
  Tester did a real, ad hoc browser verification (screenshots, DOM text
  extraction) instead of a committed spec. A follow-up card for a
  geometric/visual e2e spec (precedent `e2e/tests/sale-screen-213.spec.ts`)
  would be reasonable.
- Incidental, unrelated finding noticed by Tester while rigging a
  file-device printer for the driven run: `internal/print/transport.go`'s
  `deviceTransport.Print` opens the device path without `O_APPEND`/
  `O_TRUNC` despite its own doc comment claiming append semantics — not
  touched by this card, not investigated further.
- `guard-deadcode-baseline.sh` fails (flagging `internal/logging/file.go`'s
  `Stderr`/`timestampWriter.Write`) — confirmed independently by the
  Reviewer via a disposable worktree against clean `origin/main`:
  identical failure, pre-existing (no GTK/WebKit headers in this sandbox),
  unrelated to this change.

## Verified beyond the automated tests

- **TDD re-verified independently, twice** — once by Tester (4 core logic
  sites: unit-price math, show_tax-independence, time-of-supply same-day
  check, `Line.Sub` rendering), once by the Fable reviewer (same 4 sites,
  separate worktree, separate probe tests) — every revert produced a real,
  specific assertion failure, never a compile error or a vacuous pass; the
  two review-round fixes (S1, S2) were reverted and re-verified the same
  way by this orchestrating session before commit.
- **Real driven run** (Tester): actual binary, real SQLite DB, real HTTP
  (`/api/settings/invoice`, `/api/settings/printer`, `/api/print/receipt/
  {no}`, `/api/invoices/issue`), real headless Chromium. Confirmed live:
  seller VAT no. + per-rate rows in the raw ESC/POS byte stream, including
  with `receipt.show_tax=false`; the on-screen invoice at desktop, phone
  (390px) and RTL (ar, fa) widths, screenshotted and read, not just
  rendered-HTML-asserted; time-of-supply present for a backdated sale and
  absent for a same-day one, over real HTTP. A Playwright `fullPage`
  capture artifact (sticky status bar stitched onto content at a phone
  scroll boundary) was identified and correctly ruled a capture artifact,
  not a real clipping bug, via a scrolled-viewport re-capture.
- **Money/rounding correctness probed empirically** (Fable reviewer): half-
  away-from-zero rounding confirmed against several numerator/divisor
  pairs; fractional (weighed) quantities; a refund line (negative qty/
  amounts, signs cancel correctly); qty=0 (Sub omitted, no divide-by-zero).
  `SaleDetailLine.LineTotal` traced to `sale_lines.total_after_tax`
  (tax-inclusive in both pricing modes per `pos/vat_breakdown.go`), so
  `(LineTotal-TaxAmount)/Qty` is valid excl-VAT in both modes.
- **Timezone/#3300-class check**: `timeOfSupply` compares two
  `.Local()`-formatted dates derived from fixed instants (not a live clock
  read), so the decision can't flicker near midnight the way ut-docs#3300's
  lexicographic-UTC-vs-local-calendar bug did; confirmed this is not the
  same trap.
- **Charset check**: probed `·` encoding across every supported ESC/POS
  charset — only `ascii` folds it to `?`; the "- " paper separator vs "· "
  screen separator is correctly wired per path, not just claimed.
- `go build ./...`, `go vet ./...`, `gofmt -l .` clean; `go test -count=1
  ./internal/print/... ./internal/httpx/...` and the full `./internal/
  pages/...` tree all green (`make test-race-pages` for the `-race`, longer-
  timeout run); `golangci-lint run ./internal/print/... ./internal/pages/...`
  0 issues; `guard-i18n.sh`, `guard-help-topics.sh`, `guard-help-drift.sh`,
  `guard-data-access.sh`, `guard-core-neutral.sh`, `guard-docs-shots.sh`,
  `guard-compliance-claims.sh` all green — run independently by Dev, Tester,
  the Fable reviewer, and this orchestrating session after the S1/S2 fixes.
- No real client/shop name in any seed/test data ("Task Runner Ltd" /
  generic Bread/Wine items); no secrets introduced.
- Language-pack PRs owed: `invoice.unit_price`, `invoice.time_of_supply`
  and `invoice.after_discount` are brand-new core keys — per `reviewer`
  rule 4, core merges first (`main` goes red on `lang-pack-drift`,
  expected), and the `ut-plugin-language-de` / `ut-plugin-language-es`
  catch-up PRs land in this same cycle, before anything else.
