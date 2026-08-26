# Code review: harden voucher-issue overflow guard — count cap + explicit
overflow check (ut-docs#1052)

**Date:** 2026-08-26
**Author:** pipeline (Dev at Sonnet, complexity:easy)
**Reviewer:** independent fresh-context Sonnet subagent, isolated git worktree
**Branch:** fix/1052-voucher-issue-count-cap

## What shipped

Follow-up from the ut-docs#1008 independent review (2026-08-25). That
review's Finding 3 (int64 overflow via unbounded voucher amounts) was fixed
by adding `MaxVoucherIssueAmount` (1,000,000.00), enforced per voucher — but
the accumulation loop itself was not overflow-checked, and neither
`len(in.VoucherIssues)` nor the running sum was capped. `money.Money.Add` is
plain unchecked `int64` arithmetic, so the guarantee against overflow was
incidental (memory exhaustion in `json.Decode` long before a real request
could carry enough vouchers to wrap `int64`), not asserted.

`internal/pos/sales.go`, `computeSaleTotals`'s voucher-summing loop:

- **`MaxVoucherIssuesPerSale = 50`** — a sane per-sale voucher count cap,
  checked before the loop runs (`len(in.VoucherIssues) > 50` is rejected
  with a clear error, no DB work attempted).
- An **explicit running-total overflow check** inside the loop
  (`voucherIssueTotal > maxMoney-v.Amount || total > maxMoney-v.Amount`,
  `maxMoney = money.Money(math.MaxInt64)`) before each `.Add()` — this can't
  actually trip given the count/amount ceilings (50 × 1,000,000.00 is
  nowhere near 2⁶³-1), but the invariant is now checked, not assumed.
- One new test, `TestCompleteSale_VoucherIssueRejectsExcessiveCount`
  (`internal/pos/voucher_sale_test.go`), covering both sides of the
  boundary: exactly `MaxVoucherIssuesPerSale` succeeds, one more is
  rejected before any sale row is persisted.

Scoped exactly as the ticket asked: one function, one new test.

## Independent review

Fresh-context Sonnet subagent, isolated git worktree, no visibility into the
implementer's reasoning.

**Commands run, all PASS:** `gofmt -l internal/pos/`, `go build ./...`,
`go vet ./...`, `go test ./internal/pos/... -run VoucherIssue -v` (all 7
subtests including the new one), full `go test ./internal/pos/...`,
`scripts/ci/guard-data-access.sh`, `scripts/ci/guard-i18n.sh`.

**TDD claim independently re-verified**, not taken on faith: the reviewer
reverted just the count-cap and overflow-check blocks (keeping the new test
as committed), re-ran the new test, and confirmed it fails with a clear
signal (`err = <nil>, want an exceeding-the-maximum error`). Restored the
fix, confirmed the full package suite is green again.

**Correctness:** check order is right (positive → per-voucher ceiling →
overflow check → add), so the overflow check's subtraction can't itself
underflow. `total` and `voucherIssueTotal` are both monotonically
non-decreasing through the loop (only positive amounts, and `total` is
already floored at 0 before the loop), so there's no negative-operand edge
case for the guard to miss. `money.Money(math.MaxInt64)` is a valid explicit
conversion of the `int64`-based `Money` type; the direct `-`/`>` operators
work correctly on it.

**CLAUDE.md conformance:** money uses `money.Money` throughout (the one raw
`int64` touchpoint, `math.MaxInt64`, is an explicit conversion, not
implicit mixing); no SQL/data-access changes (`guard-data-access.sh` green,
confirmed no `internal/data`/`internal/db` files touched); no user-facing
strings (these are internal `fmt.Errorf`s, not template content — confirmed
`web/` is untouched by the diff); no file-I/O bug-class hits (no
`os.Mkdir/Create/Open/Write`, no cwd-relative path); no secret-shaped
literals or real client/shop names.

**Scope:** contained exactly to `internal/pos/sales.go` +
`internal/pos/voucher_sale_test.go`. Nothing unrelated included.

## Findings — all nits, none blocking

- The overflow-check branch is honestly unreachable through this code path
  given the 50 × 1,000,000.00 bound, and the new test can't drive it to
  true without bypassing the earlier per-voucher/count checks. Accepted as
  documented defense-in-depth (the comment on `MaxVoucherIssuesPerSale`
  says so explicitly) rather than silently-uncovered code.
- `internal/pages/pos_api.go`'s own voucher-issue loop (pre-existing, not
  part of this diff) sums `voucherIssueTotal` with only a per-item amount
  check — the new count cap wasn't extended to that boundary, so an
  over-count request does full basket-total work before
  `computeSaleTotals` correctly rejects it, rather than failing fast at the
  HTTP boundary the way `MaxVoucherIssueAmount` does. Not exploitable (the
  request is still rejected, nothing persists) and explicitly out of this
  ticket's "one function" scope — logged as a Backlog follow-up rather than
  expanded into this fix.
- The `MaxVoucherIssuesPerSale` doc comment's "verified by direct probe in
  the 2026-08-25 review round" is an unverifiable provenance claim in a
  code comment; harmless, not worth blocking on.

## Verified beyond automated tests

- Reviewer's own revert-then-restore TDD re-verification (above).
- Boundary condition manually confirmed both directions: cap exactly = 50
  succeeds, 51 fails with the new error message.

## Safe-to-merge verdict

**Yes**, merged as-is. No blockers, no should-fix items.

## Explicitly deferred

- Extend the count cap to `internal/pages/pos_api.go`'s own voucher-issue
  loop for fail-fast behaviour at the HTTP boundary (mirrors the existing
  `MaxVoucherIssueAmount` dual-enforcement pattern) — Backlog follow-up,
  not a blocker for this fix.
