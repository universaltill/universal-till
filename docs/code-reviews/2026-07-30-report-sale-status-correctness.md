# Code review — owner-report & sale-status correctness fixes

- **Date**: 2026-07-30
- **Branch**: `fix/report-sale-status-correctness`
- **Scope**: the four deferred correctness gaps from coverage batch 8's
  independent review (see `2026-07-30-coverage-data-batch8.md` and the
  QUEUE item), fixed TDD-style — every fix written failing-first with the
  exact predicted failure, then fixed, then passing.
- **Independent review**: different-model (opus) subagent, findings below.

## The semantic decision (documented, deliberate)

Sales reports **exclude** completed returns rather than netting them.
Rationale: `DayTotal` (the "today/yesterday" cards) already excludes
returns and renders on the SAME back-office dashboard as `SalesByDay`'s
7-day chart — after this change the chart's today-row equals the today
card by construction. `SlowItems` and `busyBuckets` also already
excluded returns; `SalesByDay`, `TopItems`, and `DeadStock`'s has-sold
subquery were the stragglers. `TaxSummary` deliberately KEEPS netting
returns — that is the fiscal view, where a return genuinely reduces tax
owed. `EndOfDay` reports sales and returns as separate columns, also
unchanged.

## What changed (six production hunks)

1. `SalesByDay` + `AND sale_type = 'sale'` — a completed return no
   longer counts as positive daily revenue on the reports page, back
   office, or the AI Q&A tool (`ask_api.go`), and no longer disagrees
   with `TaxSummary` on the same page.
2. `TopItems` + `AND s.sale_type = 'sale'` — a returned item's line no
   longer inflates its "top seller" revenue (aligns with `SlowItems`).
3. `DeadStock` has-sold subquery + `AND s.sale_type = 'sale'` — a
   customer bringing an item back no longer counts as it "selling"; the
   capital-on-the-shelf report shows it again.
4. `data.UpdateSaleStatus` now checks `RowsAffected` and returns
   "sale not found" for unknown ids. Because `pos.UpdateSaleStatus` runs
   the repo update FIRST inside `WithTx`, the transaction now rolls back
   before the audit insert — a void of a nonexistent sale no longer
   returns 204 while writing a "voided" audit row for a sale that was
   never voided (audit-log poisoning).
5. `/api/pos/sale/status` now records the logged-in operator
   (`getSessionUserID`) as the audit actor instead of a hardcoded `""` —
   voids/parks/refund-status changes are attributable again.
6. `GetLowStockItems` with a location filter now includes never-stocked
   items (`OR inv.location_id IS NULL` — safe because the column is
   `NOT NULL`, so NULL can only mean "no inventory row"); and
   `SaleCompletedAt` scans `sql.NullString`, so a NULL `completed_at`
   reads as "not completed" like blank, instead of a scan error the
   refund-window caller silently swallowed.

Two batch-8 pinned-behavior assertions were consciously flipped, as
those tests' own comments demanded (`UpdateSaleStatus` unknown-id no-op;
`SaleCompletedAt` NULL scan error).

## Verification

- Nine tests written/flipped first, all confirmed failing against the
  unfixed code, all passing after — the failing-first runs double as
  revert-mutation evidence for these one-line fixes:
  - `internal/data/pos_repo_report_semantics_test.go` (new):
    SalesByDay/TopItems/DeadStock return-exclusion, location-filtered
    never-stocked low stock.
  - `pos_repo_batch8_sales_test.go`: the two conscious flips.
  - `internal/pos/sales_test.go`: phantom void errors AND leaves zero
    audit rows.
  - `internal/pages/pos_status_test.go` (real mux, `auth.WithUser`):
    actor attribution end-to-end; unknown sale → error status + no
    audit row.
- Full `go build ./...`, `go vet ./...`, `go test ./...`,
  `guard-data-access.sh`, `guard-i18n.sh` — all green (no user-facing
  template strings added; the new error is API-level).

## Independent (opus) review findings

**No blockers.** The reviewer revert-probed every production hunk
individually (7/7): each revert failed exactly the matching test with the
predicted message — every line proven load-bearing. It also verified: the
repo-update-before-audit ordering claim in `internal/pos/sales.go`
empirically; `data.UpdateSaleStatus` has exactly one caller so the new
error breaks nothing else; re-voiding an already-voided sale still
succeeds (SQLite counts same-value updates as affected — no idempotency
regression); no OTHER endpoint passes a hardcoded `""` actor for
audit-relevant actions (the remaining `""` actors are pre-auth login
events, and `"kiosk"`/`"cloud"` are deliberate sentinels); and no UI
label implies netting — the one label promising "returns already
deducted" sits on `TaxSummary`, which keeps netting. **It endorsed
exclusion over netting** (the QUEUE item's "netting seems right" hint
notwithstanding): returns carry positive totals so netting would be a
different, bigger change; `COUNT(*)` can't be netted coherently; and
`TopItems` mirrors `SlowItems` which already excludes.

**Its main catch — fixed before commit (failing-first, 700→500):** fixing
`SalesByDay` alone made `/reports` SELF-contradictory — `SalesByDepartment`,
`SalesByTill`, `PaymentBreakdown` and `DepartmentsForDay` still counted
returns (headline £5.00 next to breakdowns showing £7.00), and `EndOfDay`
was internally inconsistent (its till query filtered `sale_type`, its
departments call didn't). All four got the same one-line filter, covered
by `TestWindowReports_ExcludeReturns_DeptTillPayments` (confirmed failing
pre-fix: `Revenue:700, want 500`).

**Other findings fixed before commit:**
- Low-stock location-filter test had no negative case — now also pins
  that another location's row stays excluded.
- Unknown-sale voids now return **404** via a `data.ErrSaleNotFound`
  sentinel (was 400 like validation errors); handler test asserts 404.
- The AI Q&A tool descriptions (`ask_api.go`) now say returns are
  excluded, so the self-hosted model doesn't present gross-of-refunds
  figures as complete (Go string, no i18n obligation — confirmed).
- `RowsAffected` error no longer shadows `err`; the two "writes no
  audit" tests no longer ignore their count query's error.

**Accepted as-is / queued:**
- Refunds now appear NOWHERE on the window reports (only `EndOfDay`
  shows Gross/Refund/Net) — queued: a Refunds/Net pair for `/reports`.
- Per-location low-stock still hides an item stocked only at another
  location (qty 0 here) — genuine product-semantics question (could
  flood branch lists in warehouse-pattern shops); queued for a decision,
  reviewer's view (show it) recorded.
- `SaleCompletedAt`'s only caller discards `ok`/`err`, so the NULL fix
  is a repo-contract fix with no behavior change at that caller —
  correct framing, noted.
- `setupStatusDB`'s hand-rolled schema lacks the `audit_log.actor_id`
  FK; reviewer chased the chain and confirmed no live risk
  (`nullIfEmpty` + seeded `system` user). Pre-existing helper weakness.
- The endpoint has no role gate on voiding — pre-existing, out of scope.

## Final gate (after review fixes)

`go build`, `go vet`, full `go test ./...`, both CI guards — green.
`git diff main --stat` = exactly the intended change set; the three
pre-existing untracked planning docs stay uncommitted (own QUEUE item).
