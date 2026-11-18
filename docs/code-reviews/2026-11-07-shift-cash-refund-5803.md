# Review — a cash refund lowers the shift's expected cash (ut-docs#5803)

Date: 2026-11-07 · Lane: lane:cloud-41 · Branch: `fix/5803-shift-cash-refund`
Author model: Sonnet (Dev, two rounds). Reviewer: Opus 5.5, an independent subagent with a fresh context, working in a separate worktree.

## What shipped
- **The SQL fix.** `SumCashPaymentsForShift` (`internal/data/pos_repo.go`) now returns cash In minus cash Out: sales (`sale_type='sale'`) add `amount - change_given` and returns (`'return'`) subtract it. This is the end-of-day rule. Before, it summed every cash payment with no `sale_type` split, so a cash refund was **added** to `shifts.expected_cash` while the EOD `CashSales` subtracted it.
- **One shared definition.** The In/Out expressions are now the consts `tenderInSQL` / `tenderOutSQL`. Both EOD summaries (`dateRangeSummary`, `dateRangeSummaryInstant`), the cloud sales aggregate (`sales_aggregate_repo.go`) and the shift sum all use them, so the shift and the Z report can't drift apart again. The Reviewer checked that the EOD and aggregate queries are textually identical to before once the consts are substituted.
- **The production root cause, found by the Tester.** The refund screen (`internal/pages/refund_page.go`) built the return `SaleInput` with no `RegisterID`. Every real return was stored with `sales.register_id` NULL, so the shift's `s.register_id = ?` window never saw refunds at all. A real close showed Expected £106.00 for float 100.00 + sale 6.00 − refund 2.40. The return now carries the till's register: `tillRegisterIDBestEffort`, then a `repo.EnsureRegister` fallback, the same as `completeTender`. If the register can't be resolved the refund still completes and the column stays NULL, so it is never blocked (offline-first).
- **Help.** `web/help/en/reports.md` ("Counting the drawer at close") now says what expected cash is made of: float + cash sales − cash refunds + cash tips + pay-ins − pay-outs. Card refunds don't change it. `web/help/en/inventory.md` now says a refund rung up on a joined till restocks at its register's location on the main till too (Reviewer L1). Manifest topic hashes are updated; there are no screenshot changes.
- **ut-docs.** `architecture/end-of-day-report.md` now says CashSales is net of cash refunds (separate PR).

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| T1 | high (Tester round 1) | The repo-level fix was a no-op in the running app, because refunds were stored with a NULL register | Fixed in Dev round 2 (`refund_page.go` sets `RegisterID`). Handler-level test `TestPostRefund_CashRefundReducesShiftExpectedCash` added |
| T2 | medium (Tester round 1) | Editing `reports.md` turned the docs-shots guards red | Fixed: topic hash updated in `web/help/img/manifest.json`. Unrelated PNG render drift from `make docs-shots` was reverted, not committed |
| L1 | low | On the main till, a refund synced from a joined till now restocks at the refunding register's location (`sync_sales.go:300`), not Main. This is a correct consequence of the fix, but it was undocumented | Help line updated (`inventory.md`) |
| L2 | low | In the cloud sales aggregate, refunds now fall in the same register bucket as sales instead of a separate device-id bucket (`sales_aggregate_repo.go:36`) | Accepted. This is the correct attribution, and it is recorded here and in the PR body |
| L3 | low | The `ShiftCash.CashSales` doc comment was stale | Fixed |
| L4 | low (optional) | `refund_page.go` resolves the till register twice (lines ~798 and ~1008). The `EnsureRegister` fallback has no test | Accepted. Same pattern as `completeTender`, and a refactor is out of scope |

Not done, by design: returns stored before this fix keep `register_id` NULL. No migration rewrites them, because we can't infer the register reliably and there are no real shops yet. Already-closed shifts keep the `expected_cash` they stored.

## Verification
- **TDD, Dev.** T1 `TestShiftCashBreakdown_SubtractsCashRefunds` failed with sum 6500, want 3500. T2 failed with CashSales 41500 / Expected 52000, want 38500 / 49000. T3 `TestCashRefund_ShiftBreakdownMatchesEOD` failed with 41500 vs 38500 and identity 50500 vs Calculated 53500. The handler test failed with `return sales.register_id = <nil>, want "reg-5803"`. All four are green after the fix.
- **TDD, re-verified by the Tester and again by the Reviewer**, each reverting the fix hunks: the same failures, then green on restore.
- **Tester driven run, real till build** with a throwaway data dir and the register set through the real setting. Open shift £100.00, cash sale £6.00, cash refund £2.40 via `POST /api/refund`, close. Expected £103.60, counted £103.60, variance £0.00; round 1 had shown £106.00. Z#1: Calculated 10360 = float 10000 + cash sales 360, counted 10360, variance 0.
- **Gates.** `gofmt -l .` is clean, `go build ./...` and `go vet` pass, and `golangci-lint` reports 0 issues. `go test` passes for data, pos, pages and cloudsync. Every guard step in the `ci.yml` build job passes (Tester ran all 143) except `windows-signing_test` (needs apt packages; CI runs it). `go test ./... -race` hits the local 10-minute timeout in five packages; this is unrelated and unchanged by this branch, and each passes without `-race`.

Pre-flight: help topic updated (`reports.md`, `inventory.md`); docs-shots n/a (prose only, no screen changed; manifest hashes updated); en.json n/a (no new key); reference/ n/a; architecture doc updated in ut-docs (`architecture/end-of-day-report.md`); README n/a (no claim changed); ADR n/a (brings the shift in line with the documented EOD rule); follow-ups: none filed (L4 accepted).
