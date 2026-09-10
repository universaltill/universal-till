# Code review: Reports "Avg sale" KPI tile (ut-docs#1974)

**Branch:** `feat/1974-reports-avg-sale-kpi`
**Author:** Farshid Mirza (pipeline, `lane:cloud-54`)
**Reviewer:** independent Sonnet subagent, fresh context, isolated worktree
(`complexity:easy` — per the pipeline's model-routing rules, an easy card's
review relaxes "different model" to "different instance": a clean-context
Sonnet that never saw the dev reasoning still gives a genuinely independent
read).

## What shipped

`/reports`' KPI header (`web/ui/pages/reports.html`) showed Revenue and
Sales (count) but no average sale value — table stakes every mainstream
competitor (SumUp's Insights tab included, per the source audit
ut-docs#1952) already surfaces. `GrandTotal`/`GrandCount` were already
computed in `internal/pages/reports_page.go`, so this is a pure derived
display value:

- `internal/pages/reports_page.go`: a new `grandAvg int64` (minor units,
  matching the existing `grandTotal`/`grandTax`/`grandNet` sibling fields —
  not `money.Money`, since this handler works in raw minor units and
  formats through the `money` template func at render time), computed as
  `grandTotal / int64(grandCount)` guarded by `grandCount > 0` — zero-safe
  by construction, never divides by zero.
- `web/ui/pages/reports.html`: one new `.card.kpi` tile, positioned between
  Sales and Tax (matching SumUp's Revenue/Sales/Avg ordering), reusing the
  existing tile markup verbatim — no new CSS, no new hardcoded
  colors/spacing.
- `web/locales/{en,ar,fa,tr}.json`: new `reports.avg_sale` key, same
  position (alphabetically sorted) in all four, matching key sets
  confirmed by `guard-i18n.sh`.
- `web/help/{en,ar,de,fa,tr}/reports.md`: the "how to use it" step's KPI
  list updated to mention the new tile in every shipped manual locale
  (`de` is a manual-only locale — no `web/locales/de.json` exists, the
  product's UI German comes from the external `ut-plugin-language-de`
  pack, but the *manual* ships German prose from this repo).
- `web/help/img/{en,ar,fa,tr}/reports.png` + `manifest.json`: regenerated
  via `make docs-shots` (the screen changed, so the screenshot had to).
  Two incidental nondeterministic-render diffs on unrelated screenshots
  (`sell.png`, `till-designer.png` — the same known flake documented in
  ut-docs#2015's review) were reverted back to `main`'s bytes rather than
  committed as noise.
- Two new regression tests in `internal/pages/reports_page_test.go`:
  `TestReportsPage_AvgSaleKPI` (two sales, £3.60+£2.40 → avg £3.00) and
  `TestReportsPage_AvgSaleKPIZeroSafeWithNoSales` (empty window → £0.00,
  no crash).

## Independent review

Fresh-context Sonnet subagent, isolated worktree. **Verdict: SAFE TO
MERGE.** No blockers found; nothing needed fixing.

Checks performed and confirmed clean: `go build`/`go vet`/`gofmt`, the two
new tests plus the full `go test ./...` suite (55 packages, 0 failures),
`golangci-lint run ./internal/pages/...` (0 issues), `guard-i18n.sh`,
`guard-data-access.sh`, `guard-kiosk-engine.sh` (confirms N/A — no
`/self-order` route touched), `guard-compliance-claims.sh`,
`guard-help-topics.sh`, `guard-help-drift.sh` (no new drift introduced —
pre-existing baselined entries for `reports` unchanged), `guard-docs-shots.sh`.

Specific findings:
- **Money correctness**: `grandAvg` is `int64` minor units, matching the
  handler's existing `grandTotal`/`grandTax`/`grandNet` pattern exactly —
  not an inconsistency. The `money` template func natively handles `int64`
  minor units via `minorUnits()`/`FormatMoney`, the same path already used
  for the sibling KPIs.
- **Divide-by-zero guard is real**: traced the `grandCount > 0` branch —
  the division only executes inside it; `TestReportsPage_AvgSaleKPIZeroSafeWithNoSales`
  exercises the empty-window path and asserts `£0.00`.
- **i18n**: `reports.avg_sale` present with identical key sets across
  en/ar/fa/tr. The external `ut-plugin-language-{de,es}` packs will get an
  advisory (non-blocking) `lang-pack-drift` note on push — expected for a
  brand-new key with no prior core existence, per this repo's own
  documented convention (core merges first; the pack follow-up is this
  lane's own responsibility, tracked below).
- **UX**: new tile reuses `.card.kpi` verbatim (no new tokens), RTL
  visually confirmed correct in both `ar`/`fa` screenshots (mirrored order
  matches LTR order), Turkish label fits without truncation, no new modal.
- **Manual**: all five locale topics' prose actually updated, not just the
  screenshot.
- **Screenshots looked at directly** (not just guard-passed): `en` — tile
  sits correctly third (Revenue, Sales, Avg sale, Tax, Refunds), no
  clipping. `ar` — RTL mirrors correctly, no clipping. `fa`/`tr`
  spot-checked, both correct.
- **No real client/shop name or secret-shaped literal** anywhere in the diff.
- Also independently verified at 360px width (manual `/run`-style check,
  outside the automated docs-shots harness which is fixed at the
  1024×600 kiosk floor): single-column stack, tile fully visible and
  correctly styled, no clipping.

### TDD re-verification (independently reproduced, not taken on the
implementer's word)

Reverted only the new KPI-tile line in `web/ui/pages/reports.html`,
re-ran the two new tests:

```
--- FAIL: TestReportsPage_AvgSaleKPI (0.15s)
    reports_page_test.go:155: expected the avg sale KPI £3.00 ..., got: <!DOCTYPE html>...
--- FAIL: TestReportsPage_AvgSaleKPIZeroSafeWithNoSales (0.08s)
    reports_page_test.go:173: expected an 'Avg sale' KPI tile even with zero sales, got: <!DOCTYPE html>...
FAIL
```

Real assertion failures (not compile errors) — confirms the tests
actually exercise the new tile. Restored, re-ran:

```
--- PASS: TestReportsPage_AvgSaleKPI (0.14s)
--- PASS: TestReportsPage_AvgSaleKPIZeroSafeWithNoSales (0.08s)
ok  	github.com/universaltill/universal-till/internal/pages	0.277s
```

Working tree confirmed clean afterward — nothing left reverted.

## Verified beyond automated tests

- Real driven run: booted the throwaway till binary (`e2e/run-till.sh`
  server), loaded `/reports` live, confirmed the tile renders with real
  zero-sale data (`£0.00`, zero-safe) — not just asserted via a rendered-
  HTML-string test.
- Screenshots at both the 1024×600 kiosk floor (`make docs-shots`, en +
  ar RTL) and manually at 360px (phone width) — both looked at directly,
  not just passed through a guard.

## Deferred / out of scope

- The `ut-plugin-language-{de,es}` pack follow-up for `reports.avg_sale`
  is this same lane's responsibility per `scrum-master/SKILL.md`'s "Work
  that has no card is not covered" rule — tracked as a follow-up in the
  close-out comment on ut-docs#1974, to be landed in the same cycle after
  this PR merges.

## Verdict

**Safe to merge.** No blockers. Merged via `merge_method: "merge"` (never
squash/rebase — see `reviewer` skill's own note on real-email
misattribution).
