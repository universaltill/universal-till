# Code review — demo café items with dine-in/takeaway VAT (ut-docs#167)

- **Date:** 2026-09-24
- **Card:** universaltill/ut-docs#167 (complexity: medium, bumped from easy — see card)
- **Author:** pipeline build lane `lane:cloud-54` (Opus 5.5)
- **Reviewer:** independent subagent on Fable (different model from the author)

## What shipped

- `internal/data/seeddata/demo_catalogue.sql`: new demo tax code `tax_demo_cafe`
  ("Café dine-in 20% / takeaway 5%", 2000/500 bp — the model a catalog import
  uses for split rates, ut-docs#512) and two café items, `itm051` Caffè Latte
  and `itm052` Ham & Cheese Sandwich. Both are made to order (`stock_untracked = 1`, no
  barcode/image/stock rows). If an operator's same-named tax code swallows
  the tax-code insert, the items fall back to `tax_std` rather than
  FK-failing the whole seed. Their price-history rows are only inserted when the item
  actually landed (an operator's own `SKU-0051/0052`).
- `demo_ids.sql` + `remove_demo.sql` / `remove_demo_relaxed.sql`: "Remove
  sample data" drops the demo tax code only while it is pristine (name +
  both rates) and no item uses it. That pristine rule is not relaxed, because it is
  shop tax configuration.
- `seeddata.ItemIDs` → 52 and new `TaxCodeIDs`. There is a new drift guard,
  `TestDemoSeedTaxCodesPristineValuesMatchCatalogue`.
- Setup wizard: the sample-data step is now `seedDemoDataForSetup`. After a
  successful catalogue seed it calls `reconcileTaxDeTakeawayOverridesIfActive`.
  That call is needed because base plugins are installed *before* the seed, so a synchronous
  tax-de activation had already reconciled without the café code.
- Help: `users.md` step 5 (en/de/ar/fa/tr) mentions the café items and that
  the rate only switches with a takeaway-capable tax plugin installed and
  enabled. `make docs-shots` was regenerated (sell/catalog/categories/tax-codes/
  till-designer show the new rows).

## Findings (Fable review) and triage

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Low/Med | New `price_history` rows FK-fail the whole seed when an operator already owns `SKU-0051/0052` (pre-existing for SKU-0001..0050) | **Fixed** for the new rows (`WHERE EXISTS`), test `TestSeedDemoCatalogueCafeSKUClashSkipsOnlyThatItem`. Pre-existing rows → follow-up card |
| 2 | Low | Wizard wiring untested (deleting the call left tests green) | **Fixed**: extracted `seedDemoDataForSetup`; the real-chain test drives it; mutation (remove the reconcile) now fails the test |
| 3 | Low | Per-item "Remove anyway" leaves the pristine demo tax code | **Accepted**, documented in both removal scripts, same as categories/brands; the next bulk removal sweeps it |
| 4 | Low | ar used Western digits + a different takeaway term than the page; tr used «» quotes | **Fixed** |
| 5 | Note | ut-docs `reference/universal-interchange-format.md` calls `takeaway_rate_basis_points` dead although `main` already uses it | Pre-existing doc drift → follow-up card |

## Verified beyond unit tests

- Real-chain test (signed wasm tax plugin with the tax-de id, real `settings_get`,
  plugin-backed asker, POS handlers): a demo latte rings with 53 minor units of tax dine-in
  and 15 takeaway inside a €3.20 tax-inclusive gross. The tendered sale's line is
  recorded at 500 bp / 15 tax.
- TDD: the new data tests failed first with the expected errors (missing tax code, FK
  failure); the reconcile-helper and wizard mutations each fail the real-chain test.
- Tender first failed with "Not enough stock". That finding led to `stock_untracked = 1`
  on the café items, since made-to-order items would otherwise be unsellable on a demo till.
- `go build ./...`, `go test ./...`, `golangci-lint run ./...` (0 issues), every
  guard in `ci.yml`'s build job, 19 demo-catalogue Playwright specs (80 passed),
  `make docs-shots` + `guard-docs-shots.sh`.
- Looked at the regenerated `en/tax-codes.png`, `fa/tax-codes.png` (RTL) and
  `en/sell.png`. Not looked at: ar/tr variants, dark theme, kiosk sizes. No
  template/CSS changed.

## Out of scope

- `ut-plugin-tax-uk` uses the inverse model (eat-in raised via
  `eatin_standard_rate_by_tax_code`), and core has no reconcile for it. The demo latte
  stays at 20% on a UK till, which is correct for UK hot drinks anyway.

## Verdict

Safe to merge.
