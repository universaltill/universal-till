# Review — flaky TestSalesMirrorPromote_AdoptsTheCopyWithoutMovingStock (ut-docs#5811)

Date: 2026-11-07 · Lane: lane:cloud-41 · Complexity: easy
Built by: Sonnet (Dev subagent) · Tested + reviewed by: Opus 5.5 (fresh subagent, separate worktree, a different model from the author) · Orchestrator: Opus 5.5 (root cause, one review nit applied)

## What shipped

Test-only. `internal/pages/sales_mirror_promote_test.go`: after A's sales are
journaled to M, the fixture sets M's `completed_at` (the apply time) to the
sales' origin `created_at` (two hours back), so they arrive on M before M's
backdated close — as the test's own comment already intended ("the 3 M sales
and 4 A sales (all before M's close) are out of B's next Z"), and as the
existing `fromM` loop does for M's own sales. No production change.

## Root cause — the test, not the code

Since #5769 (ADR-0066 amendment, rule 1) a journaled sale counts in the Z
whose window holds the main till's recording time,
`COALESCE(completed_at, created_at)` (`saleArrivalCol`, `internal/data/pos_repo.go`),
and a promote copies `completed_at` verbatim. The test archived M's close at
now−1min *after* A's sales had been applied at ~now — a scenario that cannot
happen (the close is atomic against apply). B's next Z window
`[floor, now)` has a second-truncated upper bound, so A's sales were excluded
only when their apply fell in the same wall-clock second as `to`: pass/fail
on timing. A debug dump on `main` showed `completed_at` = apply time and
gross 1000 / 750 / 500 depending on how many applies straddled the bound.

## Tests

- Before (main 4be613a, test file reverted): `go test -race -count=8 ./internal/pages/ -run 'TestSalesMirrorPromote_AdoptsTheCopyWithoutMovingStock$'` → 3 FAIL / 5 PASS at :155 (re-verified by the Tester in its own worktree; Dev saw 4/8 with the backdate neutralised).
- After: `go test -race -count=20 ./internal/pages/ -run 'TestSalesMirrorPromote_'` → 80/80 (Tester); re-run after the nit below; full `go test ./internal/pages/` ok.
- `go vet ./internal/pages/`, `gofmt -l`, `scripts/ci/guard-fixed-dates.sh` clean.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | low (coverage) | No pages-level end-to-end test drives promote → adopt → `generateEOD` with a copied sale applied on M after its close landing in B's next Z. | Accepted: covered at data level by `TestEODInstant_PromoteFloor_CopiedSalesCountedAtMostOnce` (`internal/data/eod_late_tester_5769_test.go`) and end to end without promote by `TestEOD_LateJournaledRowsCountInNextZ`; a pages-level variant would reintroduce the same-second bound unless `to` is injected. |
| 2 | nit | The first version set `completed_at` to now−2h+i s, about one second before each sale's origin. | Fixed: `completed_at = created_at`. |

Pre-flight: help/docs/i18n/UI/screenshots n/a (test fixture only, no shop-visible behaviour); ADR n/a (follows the ADR-0066 amendment); review record — this file.

Verdict: ship.
