# Test coverage batch 9: refund page — found and fixed a real double-refund-guard bug

2026-07-29

`internal/pages/refund_page.go` — `refundableLines` (display) and the
`POST /api/refund` enforcement loop. Writing tests against real intended
behavior (not just current behavior) surfaced a genuine bug in the
double-refund guard.

## The bug

When an original sale has the SAME item/variant/price rung up as **two or
more separate sale lines** (normal in practice — e.g. scanning the same
product twice rather than bumping a quantity), both the display function
and the POST handler's enforcement computed each line's remaining
refundable quantity as `line.Qty - returned[key]`, where `returned[key]`
is the TOTAL already-returned quantity for that key (aggregated across all
prior partial returns via `ReturnedQuantities`). This formula only holds
for a single line per key, or when nothing has been returned yet.

**Concrete failure** (also the regression test's fixture): two lines of
qty 2 each sharing a key (4 sold total), 1 unit already returned — true
remaining pool = 3. The old algorithm: line 0's remaining = 2 − 1 = 1
(looks right in isolation), then incremented the running tally by that
*offered* amount (not the line's full quantity), so line 1 saw
`returned[key]` already at 2 and computed remaining = 2 − 2 = 0. Total
displayed/enforced across both lines: 1, not the true 3.

**Real-world impact**: the display page under-reported available refund
quantity, and — more seriously — the enforcement logic **actively
rejected legitimate refund requests**. A customer entitled to return up
to 3 more units, requesting 2 on line 0 and 1 on line 1 (exactly the true
remaining), got a 409 "only 1 left to refund" on line 0 alone, even
though the combined request was entirely valid.

## The fix

Added `refundLinePool(lines, returned) map[string]float64`: computes, per
key, the true pool as (total quantity originally sold under that key,
summed across every line sharing it) minus (total already returned for
that key), clamped at zero. Both call sites now allocate against this
shared, correctly-computed pool instead of each maintaining their own
(buggy) running subtraction.

## Verification (TDD-style, both directions)

Wrote the regression tests first, confirmed they reproduce the bug
against the pre-fix code (`git stash` on just `refund_page.go`, rerun,
both new tests fail with exactly the predicted wrong numbers — 1 instead
of 3, and a 409 instead of 200), then applied the fix and confirmed both
pass.

## Independent review (opus) — thorough, given the financial stakes

Explicitly verified the fix cannot make the guard MORE permissive than
correct (the most important property for a double-refund guard): the new
bound is exactly `sum(requested across lines) ≤ total sold − total
already returned` — the true maximum, no more, no less. Re-derived every
test's expected numbers independently from the fixture data rather than
trusting the assertions. Confirmed the schema additions
(`sale_line_modifiers`, needed for `GetSaleDetail` to work at all in the
shared test fixture) match the real migration exactly.

**Found, unrelated to this fix**: `internal/pages` tests are intermittently
flaky under `-shuffle=on` (~4/25 runs) due to a pre-existing global
`plugins.SharedBus` singleton whose DB handle gets overwritten across
tests — confirmed present on the base branch before this session's
changes too, not introduced by this batch. Noted as a real test-
infrastructure gap worth a dedicated fix (reset the shared bus per test,
or stop sharing a global bus in tests) — not fixed here, out of scope for
a coverage/bug-fix commit; flagged for a future session.

## Verification

`go build ./...`, `go test ./...`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass. `go test ./internal/pages/... -run
Refund -count=5` — reliably passes (the refund tests themselves are not
flaky; the pre-existing shared-bus issue affects other, unrelated tests
in the same package under shuffle).

## Coverage delta

`internal/pages/refund_page.go`: 0% → covered (refundableLines,
computeRefundTotal, saleIsTaxInclusive, the full POST /api/refund
handler including validation, manager-PIN gating, and the double-refund
guard).
