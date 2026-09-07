# 2026-09-06 — Dine-in/takeaway breakdown on the end-of-day report (ut-docs#1015)

## Context

ut-docs#1015 ("Dine-in/takeaway switch must be obvious at the point of
sale") was filed 2026-08-25 against real production data: staff at a
certified German POS rang a fake `WARNING-ToGo-WARNING` marker article 14
times because the product's own consumption-mode switch wasn't
discoverable, and the marker did nothing to the tax rate.

**BA verification (this cycle) found most of the card already shipped.**
ADR-0073 (accepted 2026-09-01, after this card was filed) and the live
tablet iterations behind it (ut-docs#1379, ut-docs#1181) already made the
sale-screen toggle always-visible, one-tap, immediately re-rating,
accessible (WAI-ARIA radio pattern), and fully localized — confirmed by a
driven run against a freshly built binary with the demo catalogue seeded.
Two of the card's original acceptance criteria are **superseded** by
ADR-0073's later, deliberate design and are not built here:

- **Receipt marker for a uniform sale** — ADR-0073 §7 explicitly keeps
  "today's compact whole-sale presentation" for a uniform (all-dine-in or
  all-takeaway) sale; only a *mixed* sale gets an explicit marker. Adding
  one to every uniform receipt would contradict an accepted ADR with no
  superseding one.
- **Country-gated visibility** — ADR-0073 deliberately made consumption
  mode a generic core hospitality concept, not gated by the country tax
  plugin. No `ShowOrderType`/business-vertical concept exists in the
  product to gate on even if this were wanted; recorded on the issue as a
  possible fresh card, not built here.

**The one real, unaddressed gap**: the end-of-day (EOD/day-close) report
had zero dine-in/takeaway breakdown — `internal/pages/eod_api.go` /
`eod_tax_bands.go` / `eod_method_tax_bands.go` had no `OrderType`
reference anywhere. ADR-0073 §7 explicitly left a new report dimension
open ("not widened … in this slice") rather than deciding against it, so
this is in scope.

## What shipped

- `internal/data/pos_repo.go`: new `OrderTypeSales` struct and
  `OrderTypeSalesForDay`/`OrderTypeSalesForInstantWindow` repo methods,
  following `ArticleGroupsForDay`/`ArticleGroupsForInstantWindow`'s exact
  shape (ut-docs#1010) — same `WHERE s.status = 'completed' AND
  s.sale_type = 'sale'` exclusion of returns/voids, same nil-when-empty
  convention. Groups by `sale_lines.order_type` (the LINE's own
  normalized value), not the sale-header derived summary, so a mixed
  sale's revenue splits correctly across the dine-in/takeaway buckets
  instead of needing a third "mixed" bucket. Wired into **both**
  `dateRangeSummary`'s `from==to` gate and `EndOfDayInstant` (the path
  `generateEOD`/the live "Run end-of-day" endpoint actually calls) — a
  prior feature in this exact area (ut-docs#1010) first shipped wired
  into only the wrong path and never reached a real day-close, so this
  diff includes a dedicated test pinning each path independently.
- `internal/pages/eod_api.go`: a "BY ORDER TYPE" printed footer section,
  fixed English labels ("Dine in"/"Takeaway"), matching the existing
  "BY ARTICLE GROUP"/"BY OPERATOR"/"GUTSCHEINE" convention — this
  printed report is not localized.
- `internal/pages/reports_page.go`: wires `rep.OrderTypes` into the
  on-screen archived-report row.
- `web/ui/partials/reports_tab_eod.html`: the on-screen breakdown table,
  reusing the existing `basket.order_type.dine_in`/`.takeaway` keys plus
  one new heading key.
- `web/locales/{en,ar,fa,tr}.json`: the new `reports.eod.by_order_type`
  key in all four locales.
- `web/help/en/reports.md`: new "Dine in / takeaway breakdown" section.
- `web/help/img/**` + `manifest.json`: regenerated via `make docs-shots`
  (forces a full 25-topic × 4-locale re-shoot — the freshness hash is
  whole-surface, not per-topic; expected, pre-existing guard behaviour).
- Tests: `internal/data/pos_repo_order_type_breakdown_1015_test.go`
  (new file, 5 tests) + 2 new tests in `internal/pages/eod_test.go`.

## Independent review (Opus, worktree-isolated subagent)

**Verdict at first pass: needs fixes** — one real defect, two low-severity
nits, both addressed before merge.

1. **MEDIUM, fixed.** The manual's original prose claimed "a day with no
   takeaway activity at all still shows both rows — this is a fixed
   two-way split, not an optional section that disappears when one side
   is empty." This was **false**: `GROUP BY sl.order_type` only emits
   buckets with rows, and the diff's own test
   (`TestEndOfDay_PopulatesOrderTypes_SingleDayOnly`) asserts exactly
   that (`len(rep.OrderTypes) != 1` for a dine-in-only day) — the manual
   and the test disagreed, and the manual was wrong. Reworded to state
   the true "absent, not empty" behaviour, matching the sibling
   article-group/article/operator sections above it.
2. **LOW, fixed as hardening.** The DB grouped on the raw
   `sl.order_type` column while the print/HTML render layers fold
   *anything ≠ "takeaway"* to "Dine in" — a stray third value would have
   rendered as a duplicate "Dine in" row. Confirmed unreachable today
   (`CompleteSale`'s `NormalizeLineOrderType` clamps every persisted line
   to one of the two values, pinned by
   `TestCompleteSale_ClampsUnknownOrderTypeToDineIn`), but cheap to close
   at the source: both queries now `SELECT CASE WHEN sl.order_type =
   'takeaway' THEN 'takeaway' ELSE '' END … GROUP BY 1 ORDER BY 1 ASC`.
3. **INFORMATIONAL, not fixed (pre-existing, out of scope).** The
   breakdown is gross-of-returns/voids, matching
   `ArticleGroupsForDay`/`ArticleSalesForDay` byte-for-byte — consistent
   with its siblings, not a regression introduced here.
4. **INFORMATIONAL, not fixed (pre-existing, systemic).** The
   `ar`/`fa`/`tr` manuals already lag `en` by the ut-docs#1010 sections;
   this diff widens the gap by one more. `guard-help-topics.sh` only
   checks locale topic-completeness, not prose parity, so this passes
   CI; tracked as a known, systemic gap, not this card's to fix.
5. **INFORMATIONAL, not fixed (pre-existing).** `POSRepo.InsertSaleLine`
   omits `order_type` on insert (defaults to dine-in), but has no
   production callers — the live batch-insert path does carry it. Noted
   as a latent footgun for whoever adds a caller later, not a live bug.

## Verified beyond automated tests

- **TDD re-verification, independently, twice**, by the review subagent
  in its own isolated worktree (never touching the orchestrating
  checkout):
  - Reverted the `EndOfDayInstant` wiring (deleted the
    `OrderTypeSalesForInstantWindow` call) → `TestEndOfDayInstant_
    PopulatesOrderTypes` failed with a clear "got `[]`, want 1 takeaway
    row" message; the repo-level test stayed green (correctly isolating
    the wiring bug class, not the query itself). Restored, re-ran, green.
  - Reverted the line-level grouping to sale-header grouping (the naive
    implementation the card exists to avoid) →
    `TestOrderTypeSalesForDay_SplitsMixedSaleAcrossBuckets` failed with
    the mixed sale collapsing into one bucket instead of two. Restored,
    re-ran, green.
- **ADR-0073/CLAUDE.md compliance, explicitly checked and confirmed
  clean**: no reconnection of `tax_codes.takeaway_rate_basis_points`
  (zero grep hits across the diff); `sale.completed`/LAN journal/fiscal
  signing untouched (zero grep hits; `generateEOD`'s call chain traced —
  no signing/hashing over the EOD payload the additive field could
  perturb); repository pattern (`guard-data-access.sh` green, no SQL
  outside `internal/data`); money (`Qty` is `float64` matching
  `ArticleGroupSales.Qty`/`ArticleSales.Qty` exactly; `Net`/`Gross` are
  `money.Money`, converted only at the DB boundary); i18n
  (`guard-i18n.sh` green, new key present in all 4 locales); no
  `os.MkdirAll`/`paths.Data(...)` bug class (diff opens no files,
  constructs no paths — zero grep hits).
- SQL logic sanity-checked line by line against
  `ArticleGroupsForDay`/`ArticleSalesForDay` (character-identical
  `WHERE`) and `ArticleGroupsForInstantWindow` (identical `instantWindow`
  usage); `ORDER BY 1 ASC`'s "dine-in sorts first" claim confirmed
  against the schema (`order_type TEXT NOT NULL DEFAULT ''`, no NULL
  hazard) and SQLite's default BINARY collation.
- HTML template checked against `reference/ux-guidelines.md`: logical
  CSS properties throughout (RTL-safe), no hardcoded strings, no new
  interactive control, no focus/tab-order/accessibility regression —
  matches the `ArticleGroups` block's own pattern exactly.
- No real client/shop name in test fixtures (`"it"`, `"coffee"`, `"Test
  Shop"`, `s1`/`s2`).

## Gate run after the fixes above

`gofmt -l` (clean), `go build ./...`, `go vet ./...`,
`go test ./...` (49 packages, all `ok`, 0 `FAIL`), `golangci-lint run
./...` (0 issues), `guard-data-access.sh`, `guard-i18n.sh`,
`guard-docs-shots.sh`, `guard-help-topics.sh`, `guard-compliance-claims.sh`
— all green.

## Safe-to-merge verdict

**Yes.** The one real finding (manual prose vs. shipped behaviour) is
fixed; the low-severity hardening is applied; every other CI-blocking
guard and the full test suite pass; TDD claims independently
re-verified by reverting real production code, not just reading the diff.

## Explicitly deferred

- Per-country/per-vertical hiding of the sale-screen toggle (recorded on
  the issue; needs a fresh product decision, not this card's to invent).
- Translating the EOD breakdown sections (ut-docs#1010's and this one's)
  into the `ar`/`fa`/`tr` manuals — systemic, pre-existing gap.
- `POSRepo.InsertSaleLine`'s missing `order_type` column — latent, no
  live caller, not this card's to fix.
