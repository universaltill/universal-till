# Code review — Refunds/Net KPIs on `/reports`

- **Date:** 2026-07-30
- **Branch/PR:** `feat/reports-refunds-kpi`
- **Author:** pipeline (sonnet)
- **Independent reviewer:** opus subagent (different model)
- **Scope:** `universal-till` only — `internal/data/pos_repo.go`,
  `internal/pages/reports_page.go`, `web/ui/pages/reports.html`,
  `web/locales/{en,ar,fa,tr}.json`, plus tests.

## Backlog item

`ut-docs/QUEUE.md`: "Refunds have no representation on `/reports`" (found
2026-07-30, PR #100's review). Window reports on `/reports` are
gross-of-returns by deliberate design (PR #100: sales reports exclude
completed returns, matching `DayTotal`/`SlowItems`/`busyBuckets`), but
refunds appeared nowhere on `/reports` or `/backoffice` — only the
Z-report's `EndOfDay` showed Gross/Refund/Net. Task: add a Refunds/Net
pair to `/reports`, i18n keys in every locale.

## What shipped

- `POSRepo.RefundsByWindow(ctx, days) (total int64, count int, err error)`
  — new repo method, a byte-for-byte mirror of the already-battle-tested
  `SalesByDay` window predicate (`status='completed' AND sale_type=…
  AND created_at >= datetime('now', ?)`), just filtered to
  `sale_type='return'` instead of `'sale'`.
- `/reports`' handler sums it into the render map as `GrandRefunds` and
  `grandNet := grandTotal - grandRefunds` as `GrandNet`, alongside the
  pre-existing `GrandTotal`/`GrandTax`/`GrandCount`.
- Two new KPI tiles (Refunds, Net) next to the existing Revenue/Sales/Tax
  tiles; `reports.refunds`/`reports.net` i18n keys added to all four
  locales (en/ar/fa/tr).
- Negative Net (refunds exceeding sales in the window) gets the same
  `stock-low` red treatment the YoY tile already uses for a negative
  percentage, so an owner doesn't misread a negative figure as normal.

## Independent review (opus) — verified, not just read

The reviewer ran the full gate itself rather than trusting the claim:
`go build`/`go vet`/`guard-data-access.sh`/`guard-i18n.sh` and the
`internal/data`+`internal/pages` suites, all green. It also
**independently re-verified the TDD claim** by breaking the new query
three ways (flip `sale_type`, drop the `status` filter, neutralise the
window) and confirming `TestPOSRepo_RefundsByWindow_SumsReturnsFiltersOthers`
failed with the exact predicted mismatch each time, then restored and
re-confirmed green — same for the page-level test (deleted the new
template lines / flipped the subtraction sign / forced `GrandRefunds` to
0, all three killed the test).

Correctness checks that came back clean: the new query shares the same
UTC window boundary as `SalesByDay` (no drift between the two figures
combined into one KPI row); `GrandNet` matches `EndOfDay`'s own
`Gross - RefundTotal` definition; refund totals are stored positive
(`refund_page.go`'s `computeRefundTotal`) so the subtraction has the
right sign, no double-count; raw `int64` (not `money.Money`) matches
every other report method in this file, not a money-rule violation; no
file I/O in the diff, so the `os.MkdirAll`/`paths.Data` bug classes this
pipeline keeps finding elsewhere don't apply; no real client/shop name or
secret-shaped literal anywhere.

**One SHOULD-FIX, fixed pre-merge:** `web/locales/fa.json`'s
`reports.refunds` used بازپرداخت‌ها ("repayment", a loan-sense word),
inconsistent with the *same page's* pre-existing `reports.tax_summary_hint`,
which already calls returns مرجوعی‌ها (the retail-return sense that
actually matches `sale_type='return'`) — ar/tr were already internally
consistent, fa was the sole outlier. Changed to مرجوعی‌ها.

**Three NITs, triaged:**
- `reports.net` is byte-identical to the pre-existing `reports.eod.net`
  in all four locales — flagged as redundant, not wrong (both genuinely
  mean the same "Net"); left as a separate key rather than merged, since
  reusing an EOD-scoped key for a window-scoped KPI would couple two
  unrelated concerns for no behavioural gain.
- `RefundsByWindow`'s `count` return is tested but unused by the handler
  (Refunds shows only an amount, unlike Sales which shows a count).
  Genuine future enhancement ("£1.00 across 4 refunds"), but out of this
  item's scope (BA note: the task asked for a Refunds/Net *pair*, not a
  count) — not logged as new backlog debt since it's a two-line addition
  next time someone touches this tile, not real risk.
- **Fixed, not just noted:** negative Net (refunds with no matching sales
  in the window) was untested and unstyled. Added
  `TestReportsPage_NetGoesNegativeWhenRefundsExceedSales` (a refund with
  no sale in the window; asserts `£-1.00` renders and carries the
  `stock-low` class) and the corresponding template change. Mutation-
  probed personally: reverting the `stock-low` conditional fails the new
  test with the exact predicted mismatch; restored and re-confirmed
  green.

## Verdict

**SAFE TO MERGE** — independent reviewer's verdict, no BLOCKER found. The
one SHOULD-FIX (fa terminology) and the negative-Net test/styling gap
were both fixed pre-merge and gate re-run clean afterward (`go build`,
`go vet`, both guards, `internal/data`+`internal/pages` suites). Full
`go test ./...` has exactly one failure —
`TestSaveCleansUpDirectoryOnWriteFailure` in `internal/issuereport` —
confirmed pre-existing and unrelated (fails identically on `main`; this
sandbox runs as root, which defeats the test's read-only-directory setup
and is an environment artifact, not a regression from this change).

Also driven for real: `e2e/tests/pages.spec.ts`'s `/reports` smoke spec
(`Slow sellers` text) run against a real Chromium against a real running
till server — passed, confirming the page still renders end-to-end with
the new tiles present, not just at the Go-httptest layer.

## Explicitly deferred

- Surfacing the refund count on the tile (see NIT above) — small, real,
  not urgent; not added to `ut-docs/QUEUE.md` as its own item since it's
  a trivial follow-on to this same tile whenever it's next touched.
