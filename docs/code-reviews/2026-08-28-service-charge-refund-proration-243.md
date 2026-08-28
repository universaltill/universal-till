# Code review: Refund a prorated share of the service charge (ut-docs#243)

**Date:** 2026-08-28
**Card:** ut-docs#243
**Complexity:** medium — build: inline (Sonnet), review: Opus (fresh-context
subagent, same checkout, read-only)

## What shipped

`internal/pages/refund_page.go`'s `POST /api/refund` handler built a
`pos.SaleInput` for the "return" sale but never set
`ServiceCharge`/`ServiceChargeTaxBasisBP` (both stayed zero-value), and
`computeRefundTotal()` never included any service-charge component at all —
so refunding a sale silently let the shop keep the original sale's service
charge instead of returning the customer's share of it. E.g. a £110 sale
(£100 goods + £10 service charge) fully refunded returned only £100.

- `computeRefundTotal` now takes `serviceCharge money.Money` and
  `chargeTaxBasisBP int`, and mirrors `pos.CompleteSale`'s own
  `computeSaleTotals` ordering exactly (`internal/pos/sales.go`): the
  discount reduces subtotal *before* the charge is added (a sale discount
  never eats into the service charge), and the charge's own tax
  (`pos.ServiceChargeTax`/`pos.ChargeTaxLinesFromSale`) is folded into
  `tax` the same way a line's tax is — added on top only for exclusive
  pricing, already embedded in the charge amount for inclusive.
- The handler prorates the refunded service charge by the **same**
  `refundGross/origGross` fraction already used for `SaleDiscount`'s own
  proration two lines above it — a full refund (`refundGross == origGross`)
  returns the exact original amount back (integer division is exact in
  that case), a partial refund prorates.
- The prorated amount is threaded into both `computeRefundTotal` (so the
  required payment reflects it) and the return's `pos.SaleInput` (so the
  persisted return row, journal and receipt actually carry it —
  previously silently dropped).
- `web/help/{en,fa,tr,ar}/sell.md`'s refund step (step 6, the topic
  claiming `/refund/{receipt}`) updated to say the service charge comes
  back proportionally — required by `universal-till/CLAUDE.md`'s "manual
  ships with the feature" rule, flagged as blocking by the independent
  review (see F(blocking) below).
- Tests (`internal/pages/refund_page_test.go`): unit-level
  `computeRefundTotal` exclusive/inclusive/discount-ordering/zero-charge
  cases; HTTP-level full-refund, partial-refund, and zero-service-charge
  regression cases through the real `POST /api/refund` handler and real
  `pos.CompleteSale`; two-sequential-partial-refunds and an explicit
  uneven-three-way-split residual case (added after review, see N1 below).

## Independent review (Opus, fresh-context, same checkout, read-only)

Read the full diff cold, re-derived `computeSaleTotals`'s math by hand
against `internal/pos/sales.go` and `internal/pos/service_charge_tax.go`,
independently re-verified the TDD claim (not taken on trust), and drove
several real refunds through the actual write path rather than trusting
the unit math alone.

**Math confirmed correct, and for a non-obvious reason.** `computeSaleTotals`
derives `taxTotal` through `VATBandsForSale` (which apportions the sale
discount across bands), while `computeRefundTotal` sums tax per line
directly. These look like they could drift — they don't: in exclusive mode
`VATBandsForSale` discounts the net base and leaves each band's `Tax`
untouched, so `Σ band.Tax == Σ ComputeTaxBasisPoints`; in inclusive mode
`taxTotal` never enters `total` at all. Confirmed empirically with driven
refunds: an inclusive sale round-tripped 169/50/27 → 169/50/27; an
exclusive sale with a sale discount round-tripped 226/30/46 → 226/30/46 —
exact, both modes.

**Payment coverage can't desync** — `refundTotal` and the `SaleInput` are
built from the same `serviceChargeRefund` variable and the same `lines`,
so `CompleteSale`'s `netPaid < total` rejection can't fire spuriously.

**No cumulative over-refund** — `refundLinePool` caps
`Σ refundGross ≤ origGross`, and the proration floors, so
`Σ chargeRefund ≤ charge` always (see N1 for the exact residual shape).

**Downstream correctness confirmed, not just "unbroken":**
`invoice_page.go`'s `vatBreakdown` already passes `sale.ServiceCharge` into
`pos.VATBandsForSale`, so credit notes now correctly reverse the charge's
VAT; `eod_tax_bands.go` sign-flips returns and includes `ServiceCharge`, so
the Z-report identity still holds and now nets the refunded charge out
correctly. Grepped for `SUM(service_charge_amount)` across `internal/` —
no aggregate exists that could double-count a return positively.

**ADR compliance confirmed** — ADR-0061 Decision 2 requires every call
site to route through the shared `ApportionServiceChargeTax` rather than
re-deriving; the fix calls `pos.ServiceChargeTax`, exactly right. No ADR
contradicted.

**The "no template change needed" claim verified, not assumed** —
`web/ui/pages/journal_detail.html:82` (`{{ if .Sale.ServiceCharge }}`,
properly localized) and `internal/pages/print_api.go:180`
(`if detail.ServiceCharge != 0`) are both unconditional on sale type, read
directly by the reviewer. The return's charge renders automatically once
persisted.

### TDD re-verification — actually run, twice, at two different strengths

1. **Signature-only revert** (`git checkout -- internal/pages/refund_page.go`,
   tests kept): `go test ./internal/pages/... -run '...'` → build failure,
   9× "too many arguments in call to computeRefundTotal". A compile error
   is a weak signal on its own (proves the signature changed, not that
   behaviour is checked), so the reviewer went further:
2. **Behaviour-only revert** (kept the new signature, neutered the
   proration to a constant 0 and dropped `ServiceChargeTax`/`.Add` from
   `computeRefundTotal` and the `SaleInput` fields): real assertion
   failures with the exact "shop kept the charge" symptom —
   `TestPostRefund_FullRefundReturnsFullServiceCharge` wanted 20, got 0;
   `TestPostRefund_PartialRefundProratesServiceCharge` wanted 10, got 0;
   etc. — while the zero-service-charge regression guard correctly passed
   in *both* states. Restored, confirmed all green again, md5 of the
   restored file matched the pre-revert original byte-for-byte.

TDD claim confirmed both ways — the tests are not false-passing.

## Findings, and what happened to each

| # | Severity | Location | Finding | Outcome |
|---|---|---|---|---|
| B1 | **blocking** | `web/help/en/sell.md` (+ fa/tr/ar) | The refund step's manual prose never mentioned the service charge — required by CLAUDE.md's "manual ships with the feature" rule; not caught by `guard-help-topics.sh` (it checks route coverage, not prose). | **Fixed** — one sentence added to step 6 in all four shipped locales. `fa`/`tr`/`ar` translated directly (this session's homelab Ollama endpoint, `192.168.1.231:11434`, is unreachable from this cloud sandbox — confirmed by a timed-out probe, not assumed — so the three sentences were translated by hand rather than through the usual pipeline; worth a native-speaker spot-check, but low risk for one short, plain sentence). |
| N1 | nit | `refund_page_test.go` (test doc comment) | The two-sequential-refund test's comment and the original commit message claimed sequential partial refunds sum to the original charge "never more, never less" — empirically false in general: a 3-unit sale refunded 1-at-a-time truncates on each call (10×100/300=3, three times = 9, not 10). The 2-of-2 fixture happens to be the exact case where truncation is a no-op. | **Fixed** — comment softened to "never more" (the only actually-guaranteed direction), and a new `TestPostRefund_UnevenSequentialRefundsNeverExceedTheOriginalServiceCharge` added, asserting the exact known residual (9, not 10) so a future change to the proration basis has to consciously update this test rather than silently regress the guarantee either direction. |
| N2 | nit, deferred | `refund_page.go` (proration basis) | Service charge is prorated by gross while `ApportionServiceChargeTax` weighs by net-after-line-discount; a split refund of a sale mixing line discounts + different per-line rates + an apportioned (non-flat-basis) charge can be off by ~1 minor unit of the charge's VAT. A single full refund is exact. Pre-fix, this same sale lost the *entire* charge + its VAT on any refund — this narrows a 33-unit error to 1. | **Tracked**, not fixed here — [ut-docs#1215](https://github.com/universaltill/ut-docs/issues/1215): needs a real design decision (which basis to standardize on, or a running remainder across sequential refunds), disproportionate to bundle into this fix. |
| N3 | nit, deferred | `pos.SaleInput` / ADR-0062 | `sale_charges` (ADR-0062's future itemized multi-charge list) has a repo method (`InsertSaleCharges`) but nothing in production calls it yet — confirmed by grep, so today's scalar-only fix is correct. Once ADR-0062 step 2/3 lands, this refund path will need to prorate each itemized charge individually rather than the single scalar pair, which stops being reliable once 2+ charges exist on one sale (`service_charge_tax_basis_bp` becomes "meaningful only when a sale has exactly one charge" per its own doc comment). | **Tracked**, not fixed here — [ut-docs#1216](https://github.com/universaltill/ut-docs/issues/1216), explicitly scoped to land alongside/after ADR-0062 step 2/3, not now. |
| N4 | nit, accepted | `refund_page.go` (proration arithmetic) | `detail.ServiceCharge * refundGross / origGross` is raw `int64`, sidestepping `money.Money`'s compiler-enforced discipline — but it's byte-for-byte the same shape the pre-existing `SaleDiscount` proration two lines above already uses. | **Accepted, noted here** — consistent with existing code, not a regression introduced by this diff; not worth diverging from the established local pattern for this one line. |
| N5 | nit, deferred | `web/ui/pages/refund.html` | No refund-total preview on the refund screen at all (pre-existing gap, not caused by this diff) — more visible now since a refund can legitimately exceed the sum of the refunded lines' own prices. | **Tracked**, not fixed here — [ut-docs#1217](https://github.com/universaltill/ut-docs/issues/1217), a real UX/i18n-scoped task of its own. |
| N6 | not a finding | `print_api.go:181` | `"Service Charge"` receipt label is hardcoded English, same as `Subtotal`/`Discount`/`Tax`/`TOTAL`/`Change`/`Tip` in the same block — pre-existing whole-block pattern, just newly *reachable* on a return receipt now that the field is populated. | **No action** — out of scope, pre-existing. |
| N7 | not a finding | `refund_page.go` (fiscal signing) | The refund path calls `enforceFiscalGate` but never `dispatchFiscalSignAsk` (only `pos_api.go`'s `completeTender` does) — pre-existing, unaffected by this change. | **No action** — out of scope, pre-existing (tracked separately as ut-docs#1203/#999 already). |

Neither of this pipeline's two recurring bug classes applies (no
filesystem writes in the diff: no `os.MkdirAll` gap, no cwd-relative path
where `paths.Data(...)` belongs). No real client/shop name used as fixture
data; no secret-shaped literal.

## Verified beyond automated tests

- Hand-derived `computeSaleTotals`/`computeRefundTotal` math for both tax
  modes (exclusive/inclusive), confirmed the two independently-computed
  tax paths (`VATBandsForSale` vs. per-line sum) provably can't drift, not
  just checked against one fixture.
- Several real refunds driven through the actual `POST /api/refund`
  handler and `pos.CompleteSale` write path (not just the unit-level
  `computeRefundTotal` math) — inclusive round-trip, exclusive-with-
  discount round-trip, and the split-refund VAT case that surfaced N2.
- TDD re-verification at two strengths (signature-only and behaviour-only
  revert), independently, with byte-for-byte confirmation the working
  tree was restored exactly.
- `web/ui/pages/journal_detail.html` and `internal/pages/print_api.go`
  read directly to confirm the "no template change needed" claim, rather
  than assumed from the field already existing.
- Manual (`web/help/{en,fa,tr,ar}/sell.md`) read and updated in full —
  the one prose gap this card's diff actually created (a shop owner's
  refund behavior changed) is now covered in every shipped locale.
- No UI/layout was changed in this diff (no new screen elements — the
  existing conditional `.Sale.ServiceCharge` line in `journal_detail.html`
  is what surfaces the fix), so no screenshot/visual-check pass was
  performed; this is a backend money-calculation fix, not a rendered
  surface change.

## Gate

- `gofmt -l .` — no output
- `go build ./...` / `go vet ./...` — clean
- `go test ./internal/pages/... ./internal/pos/... ./internal/data/...` —
  all green
- `go test ./...` (full untagged suite) — all green, no unrelated breakage
- `go test ./internal/pages/... -run 'RefundTotal|PostRefund' -race` — clean
- `bash scripts/ci/guard-data-access.sh` — green
- `bash scripts/ci/guard-i18n.sh` — green (1299 keys resolve, all locales
  match `en.json`)
- `bash scripts/ci/guard-help-topics.sh` — green (route coverage
  unaffected by the prose-only manual edit)
- `bash scripts/ci/guard-compliance-claims.sh` — green

## Verdict

**Safe to merge.** The fix closes a real money-handling bug (a shop
silently keeping a customer's service charge on every refund) using the
exact math `pos.CompleteSale` already establishes as correct, with no new
architecture and no ADR contradicted. B1 (the missing manual update) was
the one blocking finding — fixed in this same branch, in every shipped
locale. N1 (an overclaiming test comment) is fixed with a new residual-case
test. N2/N3/N5 are real, non-blocking gaps narrower or pre-existing to
this diff, each tracked as its own Backlog card rather than bundled in
here. N4/N6/N7 are accepted/out-of-scope, noted so they aren't re-raised.
