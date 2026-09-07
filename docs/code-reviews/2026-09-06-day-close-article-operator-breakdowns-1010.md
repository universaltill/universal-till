# Code review: day-close per-article-group/article/operator breakdowns (ut-docs#1010)

- **Card:** ut-docs#1010, "Day-close: breakdown by article group, by article, and by operator"
- **PR:** universaltill/universal-till (branch `feat/1010-day-close-article-operator-breakdowns`)
- **Complexity:** medium (Sonnet built, Opus reviewed)
- **Date:** 2026-09-06

## What shipped

Three new day-close (Z-report) breakdowns, alongside the existing per-department/per-till ones:

- **BY ARTICLE GROUP** — revenue by an item's own *immediate* category
  (`items.category_id` → `categories.name`), deliberately **not** rolled up
  to the root the way the existing department breakdown is — a subcategory
  like "Phones" reports on its own line instead of folding into its parent
  ("Electronics").
- **BY ARTICLE** — every article sold that day, no top/bottom-N limit
  (grouped by `sale_lines.name_snapshot`, the same key `TopItems` already
  uses).
- **BY OPERATOR** — revenue and sale count per cashier
  (`sales.cashier_id`), with a resolved display name.

New repo methods in `internal/data/pos_repo.go`: `ArticleGroupsForDay`,
`ArticleSalesForDay`, `OperatorSalesForDay` (the scheduler-tick/ad-hoc-range
path, `EndOfDay`/`EndOfDayRange` → `dateRangeSummary`) **and**
`ArticleGroupsForInstantWindow`, `ArticleSalesForInstantWindow`,
`OperatorSalesForInstantWindow` (the close-to-close path,
`EndOfDayInstant` → `dateRangeSummaryInstant` — **this is the one the live
"Run end-of-day" button actually calls**, see "What the first draft got
wrong" below). All three new types use `money.Money`, not the legacy raw
`int64` `EODReport`/`DeptSales`/`TillSales` siblings use — new code
following the current standard rather than the pre-money-type convention
those older structs predate.

Wired into: `EODReport` (new `ArticleGroups`/`Articles`/`Operators`
fields, gated behind the same `from == to` single-day check
`Departments`/`Tills` already use), the printed Z-report
(`buildEODDoc`'s three new footer sections), the on-screen archived-report
list (`web/ui/partials/reports_tab_eod.html` — article-group/operator as
plain tables, per-article behind a closed-by-default `<details>` since a
busy day can list dozens), i18n (7 new keys × 4 locales), and the Reports
help topic (all 4 locales) + regenerated screenshots (`make docs-shots`).

## What the first draft got wrong, and how it was caught

The initial implementation (a Dev subagent) wired the three breakdowns into
`dateRangeSummary`/`EndOfDay` only — the scheduler-tick and ad-hoc-range
path — and flagged the gap itself rather than silently shipping it: "the
archived/printed 'eod' reports a real shop actually generates today go
through `dateRangeSummaryInstant`, not `dateRangeSummary`... real
day-closes won't show these three sections until `dateRangeSummaryInstant`
is separately updated." I verified this by tracing `generateEOD`
(`internal/pages/eod_api.go`) → `repo.EndOfDayInstant(ctx, from, to)` — the
call the live "Run end-of-day" endpoint actually makes — and confirmed the
new fields were unreachable from it. This would have shipped a card marked
Done whose headline feature never once appeared on a real day-close.

Fixed by adding the three `*ForInstantWindow` methods (mirroring
`DepartmentsForInstantWindow`'s existing pattern exactly: same
`instantWindow(s.created_at, from, to)` helper, same best-effort
swallow-the-error convention) and wiring them into
`dateRangeSummaryInstant` alongside the existing `Departments` population.
Added `TestEndOfDayInstant_PopulatesArticleBreakdowns`, which exercises the
exact path `generateEOD` uses and would have failed against the
first-draft code.

## Independent review (Opus, isolated worktree) — findings and disposition

Full independent review ran build/vet/full test suite/lint/all relevant
guards from scratch, re-derived the dual-attribution guarantee from the
actual `checkOrElevate`/`InsertAuditElevated` code rather than trusting
the doc comment, and re-verified the TDD claim with **behavioural
mutations** (not just a revert-and-recompile): reintroducing a
root-category rollup, a `LIMIT`, an `audit_log`-based attribution, and an
`instantWindow` bypass each independently made the relevant test fail with
a real assertion error. All four caught.

| # | Finding | Severity | Disposition |
|---|---|---|---|
| 1 | `BY ARTICLE`'s `fmt.Sprintf("%-20s %s", name, amount)` doesn't truncate — an article name over ~34 runes pushes the amount past the printer's own 42-column line-clip, **silently deleting the revenue figure** from the printed Z-report with no error. `sale_lines.name_snapshot` is free-text product text where this is routine, unlike the short department/till names the shared `%-20s` convention was written for. | **Fixed (high)** | Added `footerRow`, a rune-based right-align-with-clip helper (reimplementing `internal/print`'s own unexported `kvRow` algorithm, which `internal/pages` has no access to) for the three new sections only. New test `TestBuildEODDoc_ArticleSection_LongNameDoesNotSwallowAmount` — see "A false-pass caught in my own regression test" below for how this was actually verified. |
| 2 | The printed `BY ARTICLE` section has no length cap — a high-SKU shop (200–400 distinct articles/day) gets a correspondingly long roll on every close, with no way to turn it off. The card's acceptance criterion only named the on-screen constraint. | **Deferred — genuine product question, not an engineering bug either way** | Filed as ut-docs#1650 with three concrete options (print all / cap-with-"+N more" / a setting) and a note to check how other POS systems handle it, per the standing research-before-escalating rule. Noted in README. |
| 3 | `SalesByArticleGroup`/`SalesByArticle`/`SalesByOperator` (general `[from,to)`-window siblings, ~90 lines) had zero callers and zero tests — added "in case", unlike `SalesByDepartment`, which the Reports page actually consumes. | **Fixed** | Removed. Nothing needs them; re-add alongside a real consumer if one shows up (e.g. a Reports-page window view of these breakdowns). |
| 4 | Money/qty columns in the three new on-screen tables were left-aligned, unlike every other money column on the Reports page (`text-align:end`). | **Fixed (nit)** | Added `style="text-align:end"` to the numeric header/data cells, matching `reports_tab_items.html`/`reports_tab_payments.html`. |
| 5 | Empty `<th></th>` for the name column in the new `<thead>` rows — unlabelled for screen readers (the existing report tables elsewhere have no `<thead>` at all, a different, internally-consistent choice; adding headers for the numeric columns but not this one read as an oversight). | **Fixed (nit)** | Added a `visually-hidden` labelled `<th>` (existing utility class, already used in `basket.html`/`help.html`), reusing the section-title key rather than adding a new one. |
| 6 | `OperatorSales` had no "unattributed" fallback for a blank cashier, unlike `ArticleGroupSales`'s "Uncategorized" — a narrow edge (kiosk sales carry a real seeded `cashier_id`) but inconsistent, and `GROUP BY s.cashier_id` bucketed `NULL` and `''` separately even though both scan to `CashierID: ""`. | **Fixed (nit)** | Added `reports.eod.unattributed` (4 locales), used in both the printed footer and the on-screen table for a blank display name; `GROUP BY` normalised to `COALESCE(s.cashier_id, '')` in both `OperatorSalesForDay` and `OperatorSalesForInstantWindow`. |
| 7 | `SUM(sl.quantity)` has no `COALESCE` next to `net`/`gross`, which do. | Nit, accepted | Matches `DepartmentsForDay`'s existing shape exactly (a group only exists when it has ≥1 row, so this is harmless) — not a regression introduced here. |
| 8 | Two categories with the same name under different parents merge (`GROUP BY COALESCE(c.name,'')` keys on name, not id) — collides more often for immediate categories ("Drinks" under Food and under Bar) than `DepartmentsForDay`'s root-name grouping does. | Accepted, known limitation | Same structural shape as the pre-existing `DepartmentsForDay`; fixing the general case (group by id, resolve display name separately) is a bigger change than this card's scope and would need the same treatment applied to `DepartmentsForDay` for consistency. Left as-is, documented here rather than silently present. |
| 9 | The regenerated `reports.png` screenshots are genuinely fresh (verified: recomputed sha256 of all four `reports.md` files against `manifest.json`, byte-for-byte match) but don't actually *show* the new sections — the docs-shots fixture has no archived EOD report with breakdown data, so the picture doesn't demonstrate what the updated prose describes. | Accepted, known limitation | `guard-docs-shots.sh` only requires freshness (hash match), which this satisfies; the manual prose is accurate on its own. Not fixed here — would need a docs-shots fixture change, a larger and separate piece of test infrastructure. |
| 10 | No README bullet for the new feature, unlike sibling day-close cards (#1008, #1012). | **Fixed** | Added, in the same style/location as the #1012 (cancellations) bullet, noting the print-length caveat from finding 2. |
| 11 | No review record yet. | **Fixed** | This file. |
| 12 | The `<summary>` toggle for the collapsed per-article table was a bare text line with no extra tap-target padding on a touchscreen till. | **Fixed (nit)** | Added `padding-block:.4rem`. |

## Explicitly re-verified, not just trusted

- **SQL correctness**: the JOIN chain (`sale_lines → sales`, `LEFT JOIN
  item_variants`/`items`) is copied verbatim from the existing
  `DepartmentsForDay`; only the final hop differs (joining `categories`
  directly instead of the root-rolling `dept_roots` CTE), which *is* the
  immediate-vs-root distinction the card asked for. Every LEFT JOIN target
  is matched on a primary key, so there's no row fan-out — the structural
  reason all three breakdowns provably sum to the same base.
- **Day/instant window boundaries**: day queries use
  `date(s.created_at,'localtime') = date(?)`, the same local-calendar-day
  convention `DepartmentsForDay` uses (ut-docs#869); instant queries go
  through the shared `instantWindow` helper, inheriting its half-open
  `[from, to)` semantics and zero-`from` (first-ever close) handling.
- **Dual attribution, re-derived from code, not the doc comment**:
  `checkOrElevate` (`internal/pages/elevation.go`) never rewrites the
  request's auth context — `saleInput.CashierID` still resolves to the
  actual cashier regardless of an elevation. The elevation is recorded
  **only** through `InsertAuditElevated` into `audit_log`, a separate
  table the three `OperatorSales*` queries never join. Confirmed by
  behavioural mutation (see above): attributing to `audit_log.actor_id`
  instead breaks the test immediately.
- **Money boundaries**: `money.Money` is `type Money int64`, so it
  marshals/unmarshals correctly through the archived report's
  `content_json` round-trip; `money.FromMinor` at the row-scan boundary,
  `.Minor()` at the print boundary, and the template `money` func already
  handles `money.Money` natively (`httpx.minorUnits`).
- **Permission gating**: the new fields populate only inside
  `if canRunEOD`; a role without `eod_report` gets nil slices and the
  entire breakdown row is skipped by the template — correctly extending
  the existing gate to strictly-more-sensitive per-article/per-operator
  data.
- **No real client/shop name, no secret-shaped literal** anywhere in the
  diff. Test fixtures use role identifiers only (`cashier-a`, `manager-b`).
- **Kiosk isolation / offline-first / plugin signing**: N/A — read-only
  reporting, no `/self-order` route, no filesystem writes, no plugin code
  touched. `guard-kiosk-engine.sh` and the rest of the CI-blocking guard
  list all pass.

## A false-pass caught in my own regression test

Writing the regression test for finding 1, my first draft asserted the
rendered output contained `"£1,500.00"` — but I'd set both the report's
overall `Gross`/`Net` total **and** the test article's `Gross` to
`150000`, so the substring matched the pre-existing Sales/NET totals lines
regardless of whether the `BY ARTICLE` row itself preserved its amount.
Reintroducing the pre-fix `%-20s` formatting and re-running the test still
passed — a genuine false-pass, the exact class of bug this pipeline's
review process exists to catch. Fixed by giving the test article a
distinct amount (`£777.77`) from the report total and asserting
specifically on the line immediately following the `"BY ARTICLE"` heading,
not the whole document. Re-verified both ways afterward: passes with
`footerRow`, fails with `%-20s` reinstated (real `t.Fatalf`, not a
compile error).

## Verified beyond automated tests

- `gofmt -l .` — empty
- `go build ./...`, `go vet ./...` — clean
- `go test ./...` — full suite green (not just the new/touched packages)
- `golangci-lint run ./...` — 0 issues
- `scripts/ci/guard-data-access.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-docs-shots.sh`, `guard-compliance-claims.sh`,
  `guard-kiosk-engine.sh`, `guard-page-http-error.sh` — all pass
- `make docs-shots` actually run (this sandbox has a pre-installed
  Chromium at `/opt/pw-browsers`, resolved via the existing
  `e2e/scripts/resolve-chromium.sh` fallback, ut-docs#622) — 100/100
  screenshot tests passed, manifest regenerated. Several unrelated PNGs
  (`invoices.png`, `till-designer.png`, `multitill.png`, `sell.png`)
  re-rendered with byte-only diffs across two separate `make docs-shots`
  runs in this session — confirmed as pure capture noise (their manifest
  topic hashes never changed) and reverted each time, keeping the diff to
  only the `reports` topic's genuinely-changed images.
- TDD claims re-verified with **behavioural mutations**, not just
  revert-and-recompile — see "Explicitly re-verified" above and the
  independent review's own table.

## Safe-to-merge verdict

Yes. One high-severity finding (money truncation) fixed and covered by a
regression test that was itself caught giving a false pass and corrected;
dead code removed; consistency/accessibility nits fixed; one genuine
product question deferred to a tracked follow-up (ut-docs#1650) rather
than decided unilaterally either way; one narrow, pre-existing-pattern
limitation documented rather than silently left. Full gate green, both
real EOD code paths (`EndOfDay` and the live `EndOfDayInstant`) covered by
tests that exercise them directly.
