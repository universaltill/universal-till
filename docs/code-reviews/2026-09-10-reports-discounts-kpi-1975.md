# Code review: Reports Discounts KPI (ut-docs#1975)

**Date:** 2026-09-10
**Card:** ut-docs#1975 — "Reports: aggregate Discounts total on /reports"
(filed from the SumUp feature audit, `reference/sumup-feature-audit.md`)
**Author:** Dev phase, this pipeline (Sonnet, `complexity:medium`)
**Reviewer:** independent Opus subagent, fresh context, no dev reasoning
carried over (per `scrum-master`'s "Model routing by complexity")

## What shipped

`sales.discount_total` was written per-sale at insert time but never
summed across a report window — `SalesByDay` (the query `/reports`' KPI
header reads) selected only `count`/`total`/`tax_total`. This card adds a
**Discounts** KPI tile to `/reports`, matching the existing Refunds tile's
pattern, with the new `reports.discounts` locale key in every in-repo
locale (`en`/`ar`/`fa`/`tr`), an updated KPI-row description in
`web/help/*/reports.md` (all five locales, `de` included), and regenerated
docs screenshots (`make docs-shots`).

`POSRepo.DiscountsByWindow` (new, `internal/data/pos_repo.go`, next to
`RefundsByWindow`) sums `sale_discounts.amount` for completed sales in the
window; `reports_page.go` wires it into `GrandDiscount`.

## What the first draft got wrong, and what the review changed

The first draft summed `sales.discount_total` directly — added as a column
alongside `SalesByDay`'s existing aggregates. The independent review (see
below) found this **under-reports**: `discount_total` is written only for
a whole-sale/coupon discount (`internal/pos/sales.go:886`,
`in.SaleDiscount.Minor()`). A per-line discount — a live, first-class
basket feature (`web/ui/partials/basket.html`'s per-row discount input →
`hx-post /api/pos/line` → `UpdateLineByKey`) — is subtracted into the
line's net amount and folded into `subtotal`; it never reaches the sale's
own `discount_total` column at all. A shop that discounts individual lines
rather than the whole sale would have seen **Discounts £0.00** under a
tile labelled generically "Discounts" — correct for some shops, silently
wrong for others, which is worse than an obviously-broken number.

`sale_discounts` is the canonical ledger for both kinds
(`InsertSaleDiscountsBatch`, `internal/pos/sales.go`): a `sale_discount`
reason row for the whole-sale case, a `line_discount` reason row per
discounted line, with no overlap between the two. Fix: replace the
`SalesByDay` column with a new `DiscountsByWindow` method that sums
`sale_discounts.amount` joined to `sales`, filtered by
`status='completed' AND sale_type='sale'` and the window — mirroring
`RefundsByWindow`'s existing shape (a separate query over the same window)
rather than a join folded into `SalesByDay`'s own `GROUP BY`, which would
have fanned out `Count`/`Total`/`TaxTotal` (multiplying them by the line
count). `DailySales`/`SalesByDay` were reverted to their pre-change shape;
no other caller is affected (`ask_api.go`'s `sales_by_day` tool
description needed no change, since it never gained the new field).

## Independent review — findings and resolution

- **F1 (major, resolved)** — whole-sale-only aggregate under-reports
  per-line discounts, as above. Fixed by summing the complete
  `sale_discounts` ledger instead.
- **F2 (moot after F1's fix)** — `ask_api.go`'s `sales_by_day` tool
  description would have gone stale if `DailySales` had kept the new
  field; moot once `DailySales` was reverted.
- **F3 (resolved)** — doc comment now on `DiscountsByWindow` explains both
  discount kinds it sums and why it's a separate query, not folded into
  `SalesByDay`.
- **F4 (resolved)** — `reference/sumup-feature-audit.md` (ut-docs repo)
  still described "no aggregate Discounts tile" and `SalesByDay` selecting
  only `count`/`total`/`tax_total`. Appended a status note (the doc's own
  convention is append-only, not rewritten) recording both this card and
  #1974 as shipped, and the wider-than-filed fix this review found —
  ut-docs PR `docs/1975-sumup-audit-discounts-status`.
- **Nits (accepted, no code change needed):** the `DiscountsByWindow`
  test's amounts (150 sale-level + 30 + 20 line-level = 200) discriminate
  against every neighbouring figure in the fixture; the page-level test
  renders through the real template/locale/`money` funcmap end-to-end.
  Two unrelated screenshot files (`ar/sell.png`, `fa/till-designer.png`)
  changed by a few bytes from the same `make docs-shots` run — pre-existing
  capture nondeterminism (documented in prior `docs-shots-determinism`
  review records), not caused by this change.
- **Follow-up owned by this cycle**, not deferred: `reports.discounts`
  needs a PR in `ut-plugin-language-de` and `ut-plugin-language-es`
  (`lang-pack-drift` is advisory on this PR, blocking on push to `main`).

## Verification beyond automated tests

- TDD verified genuinely: both `TestPOSRepo_DiscountsByWindow_SumsSaleAndLineDiscountsFiltersOthers`
  and `TestReportsPage_DiscountsKPI` confirmed to **fail** (compile error /
  assertion failure) with the fix reverted, then pass with it restored.
- Full gate: `gofmt -l .` (clean), `go build ./...`, `go vet ./...`,
  `go test ./...` (all green, `internal/data` + `internal/pages` include
  the new/changed tests), `golangci-lint run ./...` (0 issues),
  `guard-i18n.sh`, `guard-data-access.sh`, `guard-help-topics.sh`,
  `guard-help-drift.sh`, `guard-docs-shots.sh` — all green.
- `shellcheck` unavailable in this session (no shell scripts touched by
  this change, so not a gap this PR introduces — CI will still run it).

## Files

- `internal/data/pos_repo.go` — new `DiscountsByWindow`.
- `internal/data/pos_repo_batch8_reports_test.go` — `b8Discount` helper +
  `TestPOSRepo_DiscountsByWindow_SumsSaleAndLineDiscountsFiltersOthers`.
- `internal/pages/reports_page.go` — wire `GrandDiscount`.
- `internal/pages/reports_page_test.go` — `TestReportsPage_DiscountsKPI`
  (both discount kinds).
- `web/ui/pages/reports.html` — new KPI tile.
- `web/locales/{en,ar,fa,tr}.json` — `reports.discounts`.
- `web/help/{en,de,ar,fa,tr}/reports.md` — KPI-row description.
- `web/help/img/{en,ar,fa,tr}/reports.png`, `web/help/img/manifest.json` —
  regenerated via `make docs-shots`.
