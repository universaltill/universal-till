# Review — demo seed survives an operator brand-name clash (ut-docs#2697)

- **Date:** 2026-09-27
- **Branch:** `fix/2697-demo-seed-brand-clash`
- **Author:** Sonnet dev subagent (lane:cloud-24 cycle, `complexity:easy`)
- **Reviewer:** Opus, fresh context, independent of the author

## What shipped

`brands.name` is UNIQUE, so an operator brand named like a demo brand
("Coca-Cola", "Generic", …) made that demo brand's `INSERT OR IGNORE` a
no-op, every demo item pointing at it FK-failed, and the whole catalogue
rolled back (0 sample items; the setup wizard only logged it).

Both items INSERTs in `internal/data/seeddata/demo_catalogue.sql` now use
the `INSERT OR IGNORE … SELECT … FROM (VALUES …)` shape the café items
already had, with `brand_id = (SELECT b.id FROM brands b WHERE b.id =
column6)`. A demo brand that didn't land → the item seeds with
`brand_id NULL`. Architect's choice (the card offered two): NULL, not
re-pointing sample data at the operator's own same-named brand — brand is
optional metadata and sample data stays unlinked from operator rows.
Row literals are unchanged, so the drift guard
`TestDemoSeedItemsPristineValuesMatchCatalogue` still parses all 52 rows.
The "known gap" comments in `demo_catalogue.sql`, `seeddata.go` and
`demo_seed_repo.go` now describe the fallback.

Tests (`internal/data/demo_seed_brand_clash_test.go`): clash on
"Coca-Cola" (52 items, itm001 NULL brand, itm002 keeps br_pepsi, operator
brand untouched); clash on "Generic" (café items keep `tax_demo_cafe`,
NULL brand); seed-then-remove after a clash (52 removed, operator brand
kept); no clash → no demo item has a NULL brand.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | No test asserted brand_id is *set* without a clash; replacing the café lookup with `NULL` passed every test. | Fixed: `TestSeedDemoCatalogueWithoutBrandClashKeepsEveryBrand`; mutation (café lookup → `NULL`) re-run and it fails with "2 demo items have brand_id NULL". |
| 2 | nit | Re-seeding after the operator deletes their clashing brand lands `br_coca` but itm001 keeps NULL (its INSERT OR IGNORE is skipped). | Accepted: harmless, consistent with idempotent re-seed; the unused demo brand is swept by the next removal. |
| 3 | process | Review record missing, WIP commit message. | Fixed by this record and the final commit. |

## Verified beyond the unit tests

- TDD re-verified by the reviewer in a throwaway worktree: with `main`'s
  SQL all three clash tests fail with `FOREIGN KEY constraint failed
  (787)`; with the fix they pass.
- `typeof()` over all seeded items: money/flag columns stay INTEGER through
  `SELECT … FROM (VALUES …)`; `pragma foreign_key_check` clean.
- Only `SeedDemoCatalogue` inserts demo items (setup wizard,
  `e2e/seed_demo`); readers scan `brand_id` as nullable; removal's
  pristine check ignores brand_id.
- Gate: `gofmt -l .` empty, `go build ./...`, `go vet ./...`,
  `go test ./...` all green; `guard-data-access.sh` passes.
- No UI surface, locale key, or help topic touched.

## Verdict

Safe to merge.
