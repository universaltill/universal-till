# Code review: voucher-issue count cap at the pos_api.go HTTP boundary (ut-docs#1137)

**Date:** 2026-08-27
**Author:** pipeline (Dev at Sonnet, complexity:easy)
**Reviewer:** independent fresh-context Sonnet subagent, isolated git worktree
**Branch:** fix/1137-voucher-issue-count-cap-api-boundary

## What shipped

Follow-up from ut-docs#1052's independent review (2026-08-26), logged there
as an explicitly deferred Backlog item. `internal/pos/sales.go`'s
`computeSaleTotals` already enforces `MaxVoucherIssuesPerSale` (50) before
its voucher-summing loop, but `internal/pages/pos_api.go`'s own
voucher-issue loop (pre-existing, untouched by #1052) only checked each
item's *amount*, not the *count* — so an over-count request did a full
basket-total pass, including `EnsureStockLocation`'s DB round-trip, before
`computeSaleTotals` correctly rejected it deeper in the stack, instead of
failing fast at the HTTP boundary the way `MaxVoucherIssueAmount` already
does.

`internal/pages/pos_api.go`, the tender handler's voucher-issue validation:

- One new boundary check, `if len(in.IssueVouchers) > pos.MaxVoucherIssuesPerSale`,
  placed immediately before the existing per-item amount/code/label loop —
  same `http.StatusBadRequest` + `"invalid voucher issue"` response the
  amount check already uses, so client-visible behaviour for an over-cap
  request is unchanged (still a 400), just reached without the wasted
  basket-total work.
- One new regression test,
  `TestPOSTender_VoucherIssueCountCapFailsFastAtAPIBoundary`
  (`internal/pages/voucher_tender_test.go`) — proves the fail-fast claim
  behaviourally, not just by code inspection: it drops the `stock_locations`
  table (the same technique
  `TestTenderHandler_EnsureStockLocationFailureShowsLocalizedMessageNotRawError`
  already uses to force `EnsureStockLocation` to error), sends
  `MaxVoucherIssuesPerSale+1` voucher issues, and asserts a 400 "invalid
  voucher issue" — not the 500 "could not be completed" that would surface
  if the request reached `EnsureStockLocation` at all.

Not exploitable before this fix and not a security fix now — nothing
persisted either way. Purely a fail-fast/consistency improvement, exactly as
scoped in the ticket. Two files touched, nothing else.

## Independent review

Fresh-context Sonnet subagent, isolated git worktree, no visibility into the
implementer's reasoning.

**Commands run, all PASS:** `gofmt -l .`, `go build ./...`, `go vet ./...`,
`go test ./internal/pages/... ./internal/pos/...`, full `go test ./...`,
`scripts/ci/guard-data-access.sh`.

**TDD claim independently re-verified**, not taken on faith: the reviewer
manually stripped just the new count-check block back out of `pos_api.go`
(leaving the new test as committed), re-ran the new test, and confirmed it
fails with the exact predicted symptom — a 500 "Sale could not be
completed…" instead of the boundary's 400, because without the fix the
over-cap request reaches the (deliberately broken) `EnsureStockLocation`
call. Restored the fix, confirmed the test — and the full suite — pass
again.

**Correctness:** threshold checked directly against `internal/pos/sales.go`
— both `sales.go`'s `len(in.VoucherIssues) > MaxVoucherIssuesPerSale` and
the new `pos_api.go` check use the same operator (`>`, not `>=`) and the
same constant (50), so the two enforcement points are genuinely equivalent:
exactly 50 succeeds, 51 is rejected, on both sides of the boundary.

**CLAUDE.md conformance:** no SQL/data-access changes (`guard-data-access.sh`
green, confirmed no `internal/data`/`internal/db` files touched); no money
arithmetic involved (`len()` comparison against an untyped int constant);
no new user-facing string (the reused `"invalid voucher issue"` literal is
the same plain API-boundary error string the existing amount check already
returns at this same call site — not template markup, no locale key
needed); no file-I/O bug-class hits (nothing here touches the filesystem).

**Scope:** contained exactly to `internal/pages/pos_api.go` +
`internal/pages/voucher_tender_test.go`. No scope creep — nothing else in
the handler was touched.

## Findings

None — blocker or non-blocker. Reviewer's report: "small, correct,
well-tested fix consistent with the existing `MaxVoucherIssueAmount`
boundary-check pattern."

## Verified beyond automated tests

- Reviewer's own revert-then-restore TDD re-verification (above), including
  reading the exact failure output to confirm it fails for the *right*
  reason (reaches `EnsureStockLocation`), not an unrelated one.
- Threshold equivalence manually cross-checked against `sales.go`'s own
  constant and operator, not assumed from the ticket description alone.

## Safe-to-merge verdict

**Yes**, merged as-is. No blockers, no should-fix items, no deferred
follow-up beyond this ticket's own scope.
