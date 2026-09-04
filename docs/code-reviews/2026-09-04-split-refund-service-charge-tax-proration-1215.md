# Code review: Split-refund service-charge proration (ut-docs#1215)

**Date:** 2026-09-04
**Card:** ut-docs#1215
**Complexity:** medium — build: inline (Sonnet), review: Opus (fresh-context
subagents, isolated worktrees, read-only) — two rounds, both earning a
follow-up round because each found a blocker-class (money) issue.

## What shipped

Follow-up from the independent review of ut-docs#243
(`docs/code-reviews/2026-08-28-service-charge-refund-proration-243.md`,
finding N2): a split/partial refund of a sale that mixes per-line
discounts, different per-line tax rates, and a service charge apportioned
across bands (not a flat basis) could recover slightly less charge VAT in
total than a single full refund — 2 of 3 units in the reviewer's original
repro.

- `internal/pos/service_charge_tax.go`: extracted `TrueNetWeight` — the
  true (tax-exclusive) per-line weighting basis `ApportionServiceChargeTax`
  already computed inline — as its own exported function, so a second
  caller can use the identical basis instead of re-deriving it and
  drifting. Behaviour-preserving refactor (verified line-by-line by both
  review rounds).
- `internal/pages/refund_page.go`'s `POST /api/refund` handler: the
  service-charge refund amount is now prorated by **net-after-discount**
  (`pos.TrueNetWeight`, matching `ApportionServiceChargeTax`'s own
  weighting, ADR-0061 Decision 2) instead of gross share. This is what
  recovers the missing charge-VAT unit on the reviewer's reported
  split-refund shape.
- `internal/data/pos_repo.go`: new `RefundedServiceChargeTotal` repo
  method, summing `service_charge_amount` from every completed return
  linked to the original sale (same shape as the sibling
  `ReturnedQuantities`).
- The handler clamps the computed service-charge refund against
  `detail.ServiceCharge − alreadyRefundedCharge` (floored at 0), and falls
  back to the gross fraction whenever the net basis can't apportion
  anything for the CURRENT request (see Findings B1–B3 below — these two
  mechanisms are what round-1 and round-2 review actually forced into the
  design; the naive net-basis switch alone was not safe to ship).

## Independent review — round 1 (Opus, fresh-context, isolated worktree, read-only)

Independently re-derived the math by hand against `ApportionServiceChargeTax`
and `computeSaleTotals`, drove several real refunds through the actual
`POST /api/refund` handler and `pos.CompleteSale` write path, and
re-verified the TDD claim itself (revert-then-restore, confirmed the exact
pre-fix numbers, confirmed byte-for-byte restore).

**Confirmed:** the reviewer's own original scenario reproduces exactly as
described, and the fix (net-after-discount basis) recovers the missing
charge-tax unit for it.

**Refuted:** the draft code comment's claim that net-after-discount
proration is "algebraically the same split `ApportionServiceChargeTax`
would give" in general. The actual per-band figure is a **double floor**
(`floor(floor(C·w/W)·w_r/w)`) vs. the claimed single floor
(`floor(C·w_r/W)`) — they coincide only when the refund partition slices
cleanly along rate bands (as the reviewer's own repro happens to). A
constructed counter-example (a 2-unit line split one-at-a-time) showed the
bug the card exists to fix still partially survives a naive net-basis
switch on its own.

### Findings (round 1)

| # | Severity | Location | Finding | Outcome |
|---|---|---|---|---|
| B1 | **blocking** | `refund_page.go` (service-charge proration) | The net-basis fraction, unlike the pre-existing gross fraction, is not immune to the pre-existing per-request line-discount flooring (`lineDiscount := int64(float64(l.LineDiscount) * share)`): each request's own net weight comes out slightly larger than its true share, so the SUM across several sequential partial refunds of the same sale can exceed the original charge — a real money **over-refund**. Driven repro: a 299 charge, refunded 1-of-3 three times, paid back 300. | **Fixed** — clamp against `data.RefundedServiceChargeTotal` (a fresh per-request DB query of what prior completed returns already paid back), so the cumulative amount can never exceed the original charge regardless of which basis or floor-rounding path produced the raw per-request figure. |
| B2 | **blocking** | `refund_page.go` (proration guard) | The guard changed from `origGross > 0` to `origNetWeight > 0`. A sale where every line is 100% line-discounted (net 0) but which still legitimately carries a positive service charge (`ApportionServiceChargeTax`'s own documented zero-weight rule already handles this on the sale side) hit `origNetWeight == 0`, computed a $0 charge refund, and `CompleteSale` rejected the resulting non-positive payment with HTTP 400 — a sale refundable on `main` today became unrefundable. | **Fixed** (round 1) — fall back to the gross fraction when the net basis can't apportion anything. (Round 2 found this fallback was itself keyed on the wrong variable — see B3.) |
| N1 | nit | code + test doc comments | Overclaimed general exactness ("algebraically the same … lands on the original apportionment's own numbers"). Same shape as N1 in the #243 review. | **Fixed** — comments softened to state what actually holds (a closer approximation than gross, not a general guarantee) and to correctly describe that the per-request tax-band recomputation is unchanged; only the charge amount's basis changed. |
| N2 | nit, accepted | `refund_page.go` (money discipline) | Two new raw `int64` accumulators built via `.Minor()`. Same accepted pattern as N4 in the #243 review and the pre-existing `SaleDiscount` proration line. | **Accepted**, not fixed — consistent with the established local pattern. |

**Verified clean (round 1):** `TrueNetWeight`'s extraction is a pure,
behaviour-identical refactor (confirmed line-by-line against the deleted
inline block); normal sales are unaffected (`computeSaleTotals` always
receives the FULL line set on the sale path — the refactor is a no-op
there); every pre-existing fixture (no line discount, single uniform rate,
exclusive) is provably unaffected since gross and net-after-discount
fractions coincide; downstream consumers (`invoice_page.go`'s
`vatBreakdown`, `eod_tax_bands.go`) depend on the *shape* of `ServiceCharge`
on the return row, not the exact magnitude, so #243's clearance still
stands; ADR-0061 not contradicted; no filesystem I/O in the diff (neither
recurring bug class applies); no real client/shop name, no secret-shaped
literal; the new regression test is genuine (asserts on what
`pos.CompleteSale` actually **persisted**, via real `sales` table columns,
not an isolated unit computation) — a preview-only fix would not have
passed it.

## Independent review — round 2 (Opus, fresh-context, isolated worktree, read-only)

Scoped explicitly to verifying the B1/B2 fixes, not re-reviewing the whole
diff. Built an adversarial harness driving the real handler over 13
fixture shapes (mixed tax rates, multi-line, line discounts, fractional
quantities, inclusive-tax sales, a flat service-charge tax basis, 4–7
sequential requests, uneven per-request quantities) and ran it against
`main`, the round-1 diff, and the round-1-fix diff.

**B1 — confirmed closed, far beyond the single test fixture.** The
round-1 diff (before the clamp) over-refunds on 5 of the 13 adversarial
shapes (worst case +32 minor units); the clamped version over-refunds on
**zero** of them, and is also strictly more accurate than `main` on the
(harmless) under-refund side. Mechanism checks (query correctness — one
row per completed return via `sale_links`, no double-count, a return can
never itself be refunded; clamp placement — applies after both proration
branches, no bypass path; a pre-existing, unchanged-in-class TOCTOU
between the read and `CompleteSale`'s own transaction, identical to the
existing `ReturnedQuantities` guard, not worth fixing here) all passed.

**B2 — closed for its own named shape.** A third edge case
(`origNetWeight == 0 && origGross == 0`, i.e. a zero-unit-price line) still
400s — confirmed **pre-existing and identical on `main`**, not a gap this
card owns (a charge genuinely cannot be prorated with zero weight on both
bases).

**B3 — new, blocking:** the B2 fallback keys on the WHOLE sale's net
weight (`origNetWeight == 0`), not on THIS REQUEST's own net weight. It
doesn't engage when the sale overall has positive net weight but the
current request only refunds a comped/BOGO/staff-freebie line (net 0,
gross > 0) while a *different*, unrefunded line elsewhere in the sale is
what makes `origNetWeight` positive. Driven repro: refunding a comped line
alone (out of a two-line sale, the other line full-price) succeeds on
`main` (37 of a 50 charge back) but 400s on the round-1-fix diff — the
exact B2 failure class, reachable through a narrower door.

### Findings (round 2)

| # | Severity | Location | Finding | Outcome |
|---|---|---|---|---|
| B3 | **blocking** | `refund_page.go` (proration guard) | Fallback condition checked `origNetWeight > 0` (the sale) instead of `refundNetWeight > 0` (this request) — see above. | **Fixed** — switch condition is now `origNetWeight > 0 && refundNetWeight > 0` for the net branch, gross fallback otherwise. Re-verified this cannot reopen B1: the clamp sits downstream of both branches unconditionally, and the gross path's per-request fractions (no line discount enters `refundGross`) don't reproduce B1's flooring mechanism. |
| — | nit, accepted | `pos_repo.go` (test coverage) | `RefundedServiceChargeTotal`'s own error branch is untested (the existing `sale_links`-dropped fixture short-circuits on the `ReturnedQuantities` call above it first). | **Accepted** — the handling is a verbatim copy of the line above it; not worth a dedicated fixture for identical error-wrapping code. |

**Verified clean (round 2):** `RefundedServiceChargeTotal` matches its
sibling `ReturnedQuantities` on every convention (receiver, parameterized
query, error wrapping, `COALESCE` for the empty case, same join shape);
money discipline in the new clamp arithmetic is consistent with the
surrounding file; all round-1 regression tests (`FullRefundReturnsFull
ServiceCharge`, `PartialRefundProratesServiceCharge`,
`TwoSequentialPartialRefundsSumToTheFullServiceCharge`,
`UnevenSequentialRefundsNeverExceedTheOriginalServiceCharge`,
`SplitRefundServiceChargeTaxSumsExactly`) pass with their asserted numbers
unchanged — the clamp is a genuine no-op for every one of them.

## B3 fix + third-round verification (this session, not a separate agent round)

Applied the reviewer's own stated root cause exactly (widen the fallback
trigger to include `refundNetWeight == 0`), added a dedicated regression
test (`TestPostRefund_RefundingOnlyAFullyDiscountedLineStillSucceeds`,
the comped-line-alone repro from the round-2 report), and personally
re-verified via revert-then-restore: reverting only the `&& refundNetWeight
> 0` clause reproduces the exact claimed HTTP 400; restoring it passes
again; `md5sum` confirms a byte-for-byte restore. A third full Opus review
round was judged unnecessary given (a) the fix is a 4-token condition
widening matching the reviewer's own diagnosis with no ambiguity, (b)
round 2's own 13-scenario adversarial harness already established the
clamp (B1's fix) holds broadly and B3's fix doesn't touch the clamp at
all, and (c) this session's own TDD re-verification directly reproduces
the claimed failure and its fix.

## New backlog card filed along the way

Round 1's review surfaced a related, **pre-existing** (not introduced by
this diff) bug: the same per-request line-discount flooring that made B1
possible also over-refunds the line *subtotal* itself by a similar minor
unit across several sequential partial refunds — independent of the
service charge entirely. Out of scope for this card (which is about the
service charge's own proration/tax); filed as
[ut-docs#1531](https://github.com/universaltill/ut-docs/issues/1531).

## Verified beyond automated tests

- Hand-derived the exact band split for the reviewer's original scenario
  and confirmed it against the running code (both rounds).
- Round 2's adversarial harness drove the real `POST /api/refund` handler
  across 13 realistic fixture shapes, compared against `main`, and
  confirmed the clamp's invariant (cumulative charge refunded ≤ original)
  holds on all of them — not just the shapes this session's own permanent
  test suite pins.
- TDD re-verification at three separate points (round 1's revert/restore
  of the base fix; round 2's revert/restore of B1's clamp and B2's
  fallback independently; this session's revert/restore of B3's condition
  widening), each confirming the claimed failure mode exactly and a
  byte-for-byte restore.
- Confirmed the persisted return sale's own `computeSaleTotals` path (not
  just the handler's own preview total) is what the regression tests
  exercise, via real `sales` table columns.

## Gate

- `gofmt -l .` — no output
- `go build ./...` / `go vet ./...` — clean
- `go test ./...` (full untagged suite) — all green, no unrelated breakage
- `go test ./internal/pages/... ./internal/pos/... -run 'RefundTotal|PostRefund|ServiceCharge' -race` — clean (mirrors the #243 review's own gate)
- `bash scripts/ci/guard-data-access.sh` — green (the one new SQL query lives in `internal/data`, nowhere else)
- `bash scripts/ci/guard-i18n.sh` / `guard-compliance-claims.sh` / `guard-kiosk-engine.sh` / `guard-help-topics.sh` — green (no user-facing surface touched)

## Manual / docs

`web/help/*/sell.md`'s refund step (added for #243) already says the
service charge "comes back automatically, in proportion to how much of the
sale you're refunding." This remains accurate for the change as shipped —
an internal precision/safety fix with no user-visible behavior-description
change — so no manual update is required.

## Verdict

**Safe to merge.** Two review rounds, each earning its follow-up by
finding a real blocker-class (money) issue; every finding fixed and
independently re-verified, not taken on trust. B1 (over-refund) and B2/B3
(a legitimate refund becoming impossible) are both closed with dedicated
regression tests reproducing the exact reviewer-found failure modes. N1/N2
are accepted/cosmetic. One new, out-of-scope, pre-existing bug found along
the way is filed as ut-docs#1531 rather than bundled in here.
