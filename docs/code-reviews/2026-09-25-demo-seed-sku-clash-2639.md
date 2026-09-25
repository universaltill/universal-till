# Code review — demo seed: a SKU clash skips only that item (ut-docs#2639)

**Date:** 2026-09-25 · **Lane:** `lane:cloud-54` · **Complexity:** easy
(built by Sonnet, reviewed independently by Opus 5.5 in a fresh context)

## What shipped

`internal/data/seeddata/demo_catalogue.sql` is executed verbatim in one
transaction by `DemoSeedRepo.SeedDemoCatalogue`. Items use `INSERT OR
IGNORE`, so an operator item already on a demo SKU (e.g. `SKU-0001`) made
the demo item skip — but its dependent rows still referenced the missing
id, FK-failed, and rolled back the whole catalogue (the setup wizard then
finished with no sample data).

- Every dependent insert (`item_barcodes`, `item_images`, `item_variants`,
  `variant_barcodes`, `inventory`, `price_history`, `shortcut_buttons`) is
  now `INSERT OR IGNORE … SELECT … FROM (VALUES …) WHERE EXISTS (parent)` —
  the shape the café `price_history` rows already used. `inventory` and
  `price_history` gate on both item and variant (whichever is non-NULL).
  The café rows `ph063`/`ph064` were folded into the main `price_history`
  list. Row values are byte-identical; the `items`/`tax_codes` inserts are
  untouched (`TestDemoSeedItemsPristineValuesMatchCatalogue` still parses).
- Header comment in the SQL, `SeedDemoCatalogue`'s doc comment and
  `seeddata.DemoCatalogueSQL`'s doc comment now describe the real
  behaviour per clash kind, and name the remaining gap (ut-docs#2697).
- New `internal/data/demo_seed_sku_clash_test.go`: operator item on
  `SKU-0001` (51 of 52 seed, no itm001 dependents), operator item on
  `SKU-0006` (itm006 skipped with its variants and all variant
  dependents), operator variant on `SKU-0006-6P` (52 items, var001 and its
  dependents skipped, var002 present).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major (pre-existing, out of scope) | `brands.name` is UNIQUE: an operator brand named "Coca-Cola"/"Generic" makes the demo brand skip and the items insert FK-fails the whole seed. Reproduced by the reviewer. | Filed **ut-docs#2697** (Backlog); comments name it as the known gap. |
| 2 | minor | `SeedDemoCatalogue` doc comment overclaimed: a barcode clash skips only the barcode row, a tax-code-name clash skips nothing (café items fall back to `tax_std`). | Fixed. |
| 3 | minor | `seeddata.go` still said the seed "never fails on an operator's clashing row". | Fixed. |
| 4 | nit | Test comment pointed at the café `price_history` block that was folded away. | Fixed. |
| 5 | nit (unchanged behaviour) | Dependents gate on "parent id exists", not "parent is sample data": after Keep-as-own + deleting that item's own barcode, a re-seed restores the demo barcode on it. Same as `main`. | Accepted — unchanged, rare, harmless. |

## Verified beyond the unit tests

- Reviewer restored `demo_catalogue.sql` from `origin/main` in a separate
  worktree: all three new tests fail with `seed demo catalogue: constraint
  failed: FOREIGN KEY constraint failed (787)`; with the fix they pass.
  (Dev observed the same failure before writing the fix.)
- Reviewer seeded one DB with `main`'s SQL and one with the branch's and
  compared `typeof(col)||quote(col)` for every column of 11 tables: 0
  differences across 334 rows (the `SELECT … FROM (VALUES …)` wrapping
  does not change stored types or NULLs).
- Enumerated every FK onto `items`/`item_variants`: none of the other
  tables are seeded by this file; all 7 dependent inserts are gated.
- 12 other clash scenarios (barcode vs barcode, item SKU vs variant SKU,
  shortcut barcode, image id, inactive `tax_std`, …) seed all 52 items.
- Idempotency: double seed identical; Keep-as-own → Remove sample data →
  re-seed gives 51 sample items without error.
- Gate: `gofmt`, `go build ./...`, `go vet`, `golangci-lint run ./...` (0
  issues), full `go test ./...`, `go test -race` for `internal/data`,
  `internal/db`, `internal/pages`, and every guard in `ci.yml`'s `build`
  job (`guard-shellcheck-version.sh` can't run here — no `shellcheck`
  binary in the container; no shell script changed).

No UI surface, no migration, no locale keys, no help-topic change.

**Verdict:** safe to merge.
