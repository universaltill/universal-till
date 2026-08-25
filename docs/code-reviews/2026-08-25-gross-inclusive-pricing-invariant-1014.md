# 2026-08-25 — Gross-inclusive pricing invariant (ut-docs#1014)

## What shipped

Real German trading data showed the same article sold at two VAT rates on
the same day (dine-in 19% / takeaway 7%) at an unchanged gross price (e.g.
4.80 both ways — only net/tax moved). The card asked for this to be an
explicit, tested invariant rather than an emergent property that a later
refactor could silently break.

**Investigation found the arithmetic was already correct** —
`internal/pos/money.go`'s `ComputeTaxBasisPoints` already holds gross fixed
for a tax-inclusive catalog and only re-derives net/tax when the rate
changes, and every consumer (basket preview, tender, receipt, day-close,
invoice, fiscal signing) shares this one function. So this was scoped as a
**documentation + test-coverage change, not a bug fix**:

- `internal/pos/money.go`: doc comment on `ComputeTaxBasisPoints` stating
  the invariant explicitly, and naming the actual guarantor
  (`tax := subtotal.Sub(net)`, not merely "returns subtotal verbatim" —
  see "What the independent review found" below).
- `internal/pos/service.go`: doc comment on `SetOrderType` stating the same
  invariant at the basket level.
- `internal/pos/gross_inclusive_invariant_test.go` (new): 8 tests covering
  the rate matrix, the card's exact DE dine-in/takeaway numbers, the
  tax-exclusive contrast case, an end-to-end `Service`/`SetOrderType` path,
  a multi-line rounding-sum identity, a discount+service-charge case, and a
  cross-surface `VATBandsForSale` check (the function shared by the
  invoice VAT table and the day-close Z-report).

No production behaviour changed — `git diff` on the two non-test files is
comments only.

## What the independent review found

Independent review (Opus, fresh context — this card is `complexity:medium`)
ran the full gate itself, then adversarially mutated the code to check each
test would actually fail against real bugs (per this pipeline's TDD
discipline). Findings, all addressed before merge:

1. **MAJOR — a mutation I'd used for my own TDD verification (Tester step)
   was a mathematical no-op**, not a real bug: since `tax` was already
   defined as `subtotal.Sub(net)`, returning `net.Add(tax)` instead of
   `subtotal` is algebraically identical to `subtotal` for any input — it
   can never fail regardless of correctness. My actual sabotage test (which
   added a further `+1`) did catch a real perturbation, but the underlying
   point stands and generalizes: the doc comment I'd written attributed the
   invariant's safety to *returning `subtotal` verbatim* rather than to
   *`tax` being defined as `subtotal`'s complement of `net`* — a
   distinction that matters because a refactor could keep the letter of my
   original comment ("return subtotal verbatim") while computing `tax`
   independently (e.g. `net.MulDiv(rate, ...)`), which **would** silently
   understate VAT and none of the original tests caught it, because they
   only checked the returned gross, not the tax figure against an
   independently-derived expectation. **Fixed**: reworded the
   `money.go` doc comment to name the real guarantor, and reworked
   `TestComputeTaxBasisPoints_InclusiveGrossInvariant_RateMatrix` to assert
   `tax` against an independently-computed expected value (not a
   tautological identity) across the whole rate matrix — verified this
   now fails when `tax` is computed via `net.MulDiv(rate, basisPointsDen)`
   instead of as `subtotal.Sub(net)` (see "Re-verification" below).
2. **MAJOR — `TestVATBandsForSale_InclusiveGrossSumMatchesLineSum` proved
   nothing**: called with `discountTotal=0, serviceCharge=0`, both
   non-trivial branches of `VATBandsForSale` (discount proration,
   ADR-0061 service-charge apportionment) are unreachable, so the
   assertion reduced to a tautology. **Fixed**: renamed to
   `..._SumMatchesSaleTotal` and given a real whole-sale discount and
   service charge, so both branches actually run; verified it now checks a
   genuine identity (sum of bands' gross = sum of line gross − discount +
   service charge).
3. **MINOR — several `net+tax == gross` assertions were always true by
   construction** (algebraic identities, not invariant checks). **Fixed**:
   removed/replaced with independently-derived expected values (pinned
   exact tax figures — 0.77 at 19%, 0.31 at 7% — in
   `..._DESwitch`, matching the card's own real trading numbers).
4. **MINOR — the most fragile corner (discount + service charge under an
   order-type switch) was untested.** `recomputeTotals` adds the service
   charge's own tax to `Basket.Total` only when the catalog is
   tax-*exclusive*; for inclusive it's folded entirely into `Basket.Tax`
   instead — the single line most likely to break the invariant under a
   future edit. **Fixed**: added
   `TestGrossInclusiveInvariant_WithDiscountAndServiceCharge`, configuring
   both and asserting `Basket.Total`/`ServiceCharge` stay invariant across
   the order-type switch.

No BLOCKER findings. No changes to the repository-pattern, i18n, or
plugin-signing rules were implicated (confirmed by both the Tester pass and
the independent review: no SQL added outside `internal/data`, no
user-facing strings added, nothing touches disk or plugin loading).

## Re-verification after the fixes

Deliberately broke the invariant two ways and confirmed the new/fixed
tests fail with the actual expected error, then restored and confirmed
green again (see terminal transcript for this session):

- `return tax, net.Add(tax).Add(1)` (perturbed gross) →
  `TestComputeTaxBasisPoints_InclusiveGrossInvariant_RateMatrix`,
  `..._DESwitch`, `TestGrossInclusiveInvariant_OrderTypeSwitch_German`,
  `..._MultiLineSumIdentity` all fail. Restored → all pass.
- `tax := net.MulDiv(rate, basisPointsDen)` (tax computed independently of
  `subtotal.Sub(net)`, gross still returned unchanged — the exact gap the
  independent review identified) →
  `TestComputeTaxBasisPoints_InclusiveGrossInvariant_RateMatrix` fails
  (`inclusive gross=1.00 rateBP=1000: tax=0.09, want 0.09` — caught across
  the matrix even though it happened not to move the two isolated DE-pair
  values in `..._DESwitch`). Restored → all pass.

Full gate after the fixes: `gofmt -l .` clean, `go build ./...` clean,
`go vet ./...` clean, `go test ./...` — all packages pass, `internal/pos`
specifically green in 4.87s. `guard-data-access.sh`, `guard-i18n.sh`,
`guard-compliance-claims.sh`, `guard-help-topics.sh` all pass (the other
CI-blocking guards are unaffected by this diff's scope — Go-only,
comments + tests — and were run once at the start of this change with the
same result).

## Verified beyond automated tests

Backend-only change, no UI/visible surface touched — no screenshot/visual
check applicable (per the `tester` skill's own scoping: that requirement
applies to form/dialog/page/visible-surface changes).

## Safe-to-merge verdict

**Yes.** Comment-only production diff (cannot alter runtime behaviour by
construction), full gate green, independent review's findings addressed
and re-verified with fresh sabotage-and-restore checks, no scope creep
beyond the card's acceptance criteria.

## Explicitly deferred / out of scope

- No ADR: this documents/tests an existing invariant, doesn't introduce a
  new architectural decision. ADR-0035 (tax-rate switching is a plugin
  hook) already governs the surrounding mechanism and is unaffected.
- `pos.BasketLine`/`money.Money` don't carry named Gross/Net/Tax fields —
  noted as a nice-to-have naming clarity improvement during investigation,
  not required by this card's acceptance criteria; not done here to avoid
  scope creep into a type change with no behavioural payoff.
- `fiscal.sign.ask`'s `tax_inclusive` visibility to the signer was already
  closed by ut-docs#834 (2026-08-19) — confirmed still correct, nothing
  further needed.
