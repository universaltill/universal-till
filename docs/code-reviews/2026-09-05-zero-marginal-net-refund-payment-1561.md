# Code review: Zero-marginal-net partial refund payment (ut-docs#1561)

**Date:** 2026-09-05
**Card:** ut-docs#1561
**Complexity:** medium — build: inline (Sonnet), review: Opus (fresh-context
subagent, isolated worktree, read-only). One round; no blocker-class finding,
so no second round.

## What shipped

Follow-up from the independent review of ut-docs#1531 (finding F2): a
heavily-discounted line refunded across several sequential partial requests
can have one request's own marginal (per-request) net compute to EXACTLY
zero — mathematically correct for that specific partial slice, since
ut-docs#1531's running per-key discount clamp targets the *cumulative*
discount owed by each request's end. Before this fix, `internal/pages/
refund_page.go`'s `POST /api/refund` handler always built exactly one
payment record for the whole refund total, and `internal/pos/sales.go`'s
`netPayments` rejected any payment whose `Amount` was not `> 0` — so that
request (and every later one for the same line) was rejected with a
generic 400, permanently blocking part of an otherwise-valid refund.

Repro from the ticket: 3 units @ 100, a 299 line discount (net 1 total),
refunded one unit at a time. Request 1 gives back net 1; requests 2 and 3
each compute net exactly 0 (the running clamp gives back the remaining
discount before the remaining gross) and were rejected.

- `internal/pos/sales.go`: `netPayments` now allows `len(payments) == 0`
  when the sale/refund's `total` is exactly zero (returns `(0, nil)`) — a
  genuinely zero total needs no payment at all (a stock-only return). Every
  NONZERO total still requires at least one payment; the per-payment
  `amount must be > 0` rule for a multi-payment list is untouched.
- `internal/pages/refund_page.go`: extracted a `refundPayments(method,
  refundTotal, currency)` helper — returns `nil` (no payment row) when
  `refundTotal.IsZero()`, else the same single-payment slice as before this
  card. `refundTotal` is the literal value already computed by
  `computeRefundTotal` — no second, potentially-diverging basis.
- `internal/pages/eod_method_tax_bands.go`: comment-only fix (review
  finding F2) — the invariant it asserted ("`CompleteSale` always records
  >=1 payment for a completed sale") is exactly what this card changes for
  the zero-total case; corrected to say so.

## Tests

- `internal/pos/sales_test.go`: `TestNetPayments_ZeroTotalAllowsNoPayments`
  (zero total + no payments succeeds) and
  `TestNetPayments_NonzeroTotalStillRequiresAPayment` (pins the untouched
  general rule).
- `internal/pages/refund_page_test.go`:
  `TestPostRefund_ZeroMarginalNetPartialRefundSucceeds` — the ticket's exact
  repro (3×100 gross, 299 discount, refunded 1 unit at a time via 3
  sequential `POST /api/refund` calls), asserting all 3 succeed and the
  cumulative refunded net equals exactly 1.

**TDD verified twice, independently**: the implementer reverted just the
two production files and confirmed both new refund_page_test.go/sales_test.go
cases fail with the exact error class the ticket describes (`payment 1
amount must be > 0` / `400 Sale could not be completed`), then restored the
fix and confirmed green. The reviewing Opus subagent, in its own isolated
worktree, independently repeated the same revert → fail → restore → pass
cycle before trusting the claim, and separately drove the repro out-of-band
(temporary probe tests, since deleted) to confirm the real persisted
outcome — not just a 200 — matches: 3 return sales, 3 return lines, all 3
units restored to `inventory`/`stock_movements`, full 299 discount given
back, cumulative return total exactly 1.

## What was verified beyond automated tests

- `gofmt`, `go build ./...`, `go vet ./...` clean.
- Full `go test ./...` green (no regressions anywhere in the suite).
- All 20 CI `build`-job guards pass, including `guard-data-access` (no SQL
  added outside `internal/data`), `guard-i18n` (no new/changed user-facing
  strings), `guard-page-http-error`, `guard-help-topics`, `guard-docs-shots`,
  `guard-compliance-claims`, `guard-kiosk-engine`.
- Reviewer checked every `SaleInput.Payments` construction site in the repo
  (`pos_api.go`, `self_order_shop.go`, `inventory_api.go`) to confirm no
  *other* path can now slip a real, nonzero-total sale through with zero
  payments — none can; each either defaults/rewrites to a positive amount
  or explicitly rejects a non-positive total before this code is reached.
- Reviewer checked every consumer of `SaleInput.Payments`/a persisted zero-
  payment sale (ESC/POS and HTML receipt rendering, `fiscal.sign.ask`
  payload construction, sync replay, exports) for an unguarded assumption
  of `len(Payments) >= 1` — none found; all range or explicitly guard.
- No SQL, no filesystem writes, no UI/template/locale/migration change, no
  offline-first impact — backend validation-layer fix only, confined to
  `internal/pos` and `internal/pages`.
- No real client/shop name or secret-shaped literal in the diff (test data
  is generic: "Widget", "itm-1561", "R-REFUND-1561").

## Findings (all non-blocking; nothing required before merge)

- **F2 (low, fixed in this branch)** — see "What shipped" above: a stale
  invariant comment in `eod_method_tax_bands.go` that this very card makes
  false. Cheap, fixed same-session.
- **F1 (low, deferred — ut-docs#1579)** — `deriveTenderType`'s
  `len(payments) == 0 -> "unknown"` branch was dead code before this fix
  (nothing could reach it); it's now reachable, and the journal templates
  render `TenderType` raw (untranslated), so a zero-marginal-net return
  shows the literal English word "unknown" in every locale including
  RTL. Not money-wrong — presentation only, and consistent with the
  existing pattern where "cash"/"card"/"split" already render raw the same
  way. Filed as its own card rather than folded into this one, since fixing
  it properly means deciding a locale-key scheme for the whole tender-type
  display, not a one-line change.
- **F3 (informational, deferred — ut-docs#1580)** — the new regression test
  asserts HTTP 200 and the summed return total, but not that the units/
  stock actually came back (the actual harm the ticket describes). Real
  behaviour is correct (verified above); filed as a test-hardening
  follow-up.

## Safe-to-merge verdict

**Yes.** Independent review found no blocker-class (money/tax/data-loss/
security) issue; the two nonzero-total invariants (a real refund/sale still
requires a positive payment; the fungible-pool discount clamp's own
correctness) are unchanged and covered by pre-existing tests plus the new
sibling test pinning the negative case.
