# Code review: refunds prorate each itemized charge (#1216)

- **Card:** universaltill/ut-docs#1216. It follows #985 (ADR-0062 step 2,
  PR #1421), whose review record deferred "multi-charge refund proration".
- **Design:** ADR-0062 Decision 2: "No reader may derive tax from the sum
  and this single basis". No ADR change.
- **Author:** Opus 5.5 (cloud lane `:24`). **Reviewer:** an independent Fable
  subagent in a detached worktree, with mutation testing.

## What shipped

- `refundCharges` (`internal/pages/refund_page.go`) builds the refund's
  charge list.
  - A sale with `sale_charges` rows (every sale since #985) prorates **each
    charge** by the refunded fraction. Each charge keeps its key, label, tax
    basis and base.
  - A sale with no rows (from before #985) takes the old path: one
    `service_charge` item from the scalar amount and basis. The arithmetic is
    identical to before.
- Before this fix, any 2+-charge sale had its charges folded into one
  `service_charge` item at basis 0. That taxed a flat-basis levy at the line
  rates. In the test fixture, a half refund came to 214/1284 instead of
  211/1281.
- Double-refund guard (from #1215 B1), now in two parts:
  - a per-key clamp, from the new `POSRepo.RefundedChargeTotalsByKey` (sums
    `sale_charges` over completed linked returns);
  - the existing clamp on the summed remainder (`RefundedServiceChargeTotal`),
    so a prior return recorded without rows still counts.
- `computeRefundTotal` now takes the charge list and uses
  `pos.ChargesTax` / `pos.SumCharges`, the functions `computeSaleTotals` uses.
  The preview and the real POST share it, so the demanded payment is exactly
  what `CompleteSale` computes.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| R1 | should-fix (test) | Mutation "drop the per-key clamp" survived the suite. The "levy already refunded" case only passed because of charge order. | Fixed. Added a "first-listed key already refunded" case and a "prior return collapsed into one service_charge row" case. The mutation is now caught (verified). |
| R2 | should-fix (pre-existing) | `sync_sales.go` replay rebuilds one `service_charge` item from the scalar pair and ignores `Charges`. So on a replica, a replayed 2-charge **return** would fail "payments do not cover total", the same as a replayed 2-charge sale. | Already in #986's scope (journal rebuild from `charges`), which gates any levy plugin. Noted on #986 that returns are itemized now too. Nothing is affected today: no plugin declares levies. |
| R3 | nit | Flooring per charge can lose up to 1 minor unit per charge on each partial refund (before: at most 1 in total). It never over-refunds. | Accepted. This is the same "never more" behaviour the single charge already had. |
| R4 | nit | One overlong line in the `computeRefundTotal` doc comment. | Re-wrapped. |
| R5 | nit | `web/help/*/sell.md` says "service charge". | Left as is. The text is still true and no levy can reach a till yet. Editing it would change all 5 translations for no user-visible gain. The manual change belongs with #986 or the first levy plugin. |

The reviewer also checked:
- SQL stays in `internal/data`.
- A nil per-key map is safe to read.
- Duplicate keys within one sale are aggregated.
- `computeRefundTotal` and `computeSaleTotals` differ only by
  single-purpose voucher lines, which a return never carries. That was
  already true before this change.
- Fiscal hooks: `fiscal_sign_hook` apportions per charge and
  `fiscal_device_hook` uses the sum. A 2-charge return goes through both
  unchanged.

## Verified beyond automated tests

- TDD: the reviewer ran the new handler tests against the `origin/main`
  handler and got 214/1284 (want 211/1281), preview £12.84, and a collapsed
  `service_charge:140`. With the fix they pass.
- Mutations:
  - Drop the total clamp: caught.
  - Use the sale's scalar basis instead of each charge's: caught by 4 tests.
  - Drop the per-key clamp: caught after R1.
- Gate on the final diff:
  - `gofmt`, `go build ./...`, `go vet`, full `go test ./...`: all pass.
  - `golangci-lint`: 0 issues.
  - Guards: data-access, kiosk-engine, core-neutral, i18n, page-http-error,
    price-history-sync, help-topics, help-drift.
  - Playwright refund specs: 6/6 (refund form, deposit-refund dialog and
    payout).
- No visual surface changed. The refund screen and its preview figure are
  identical for 0- and 1-charge sales, so there are no screenshots. A driven
  multi-charge refund in the real app isn't possible: no plugin declares a
  levy. The handler tests go through the real mux on a real migrated DB.

## Deferred

- R2: sync replay of itemized returns, covered by #986.

## Verdict

**Safe to merge.** No blockers. Refunds of 0- and 1-charge sales behave as
before.
