# Code review — revived retire-mangled item_variants.sku (ut-docs#2273)

- **Date:** 2026-09-16
- **Ticket:** ut-docs#2273 (`complexity:easy`)
- **Branch:** `fix/2273-revived-retire-mangled-variant-sku`
- **Reviewer:** independent pass, fresh-context Sonnet subagent (per this
  card's `complexity:easy` routing — different context from the
  implementation, never saw the dev reasoning).
- **Verdict: SAFE TO MERGE.** No blocking findings; one nearby follow-up
  filed (ut-docs#2316), one nit addressed inline.

## The bug

This is the exact follow-up ut-docs#2246's own review record named and
deferred: `deleteMissing`'s FK-blocked retire-in-place mangles a
FK-blocked row's `item_variants.sku` to `"<sku>~<id>"` on retire — a
non-blank value. `stripRetireMangle` undoes that same mangle for every
*other* reader (`brands.name`, `tax_codes.name`, `payment_methods.name`,
`users.username`, `stock_locations.name`, `registers.name`) but was never
applied anywhere to `item_variants.sku`. Since ut-docs#2246 made `sku`
sticky (`COALESCE(NULLIF(excluded.sku, ''), sku)`), a revived variant —
same ID, later sent active again by a primary still behind on
ut-docs#1900/#2230 and therefore sending a blank sku — now has its local
mangled garbage value frozen in place forever, instead of the pre-#2246
behavior of overwriting to NULL and letting `backfillCodelessSyncedVariants`
generate a fresh, real-looking SKU.

## What shipped

- `internal/data/sync_admin_repo.go`: `backfillCodelessSyncedVariants`'s
  WHERE clause widened from `v.sku IS NULL OR TRIM(v.sku) = ''` to also
  match `v.sku LIKE '%~' || v.id` — the exact suffix shape the
  retire-in-place `CASE` (same file, generic per-table mangle) produces.
  Scope is otherwise unchanged: still `is_active = 1` and no
  `variant_barcodes` row, same as before.
- `internal/data/sync_admin_codeless_variant_backfill_test.go`: new
  `TestApplyAdmin_BackfillsRevivedRetireMangledVariant` — seeds the
  replica directly in the mangled+inactive shape a prior retire-in-place
  would leave (`sku='REAL-001~v1', is_active=0`), applies a primary bundle
  reviving the same id with a blank sku, and asserts the final sku matches
  the generated-SKU pattern and `is_active=1`.

## TDD, done for real

Confirmed red before the fix (`go test -run
TestApplyAdmin_BackfillsRevivedRetireMangledVariant -v`): failed with
`v1 sku on replica after revival = "REAL-001~v1"` — the mangled value,
frozen exactly as described. Applied the one-line WHERE-clause fix →
green. Then, as an independent mutation check, temporarily reverted only
the new `OR v.sku LIKE ...` clause (kept the test) and re-ran: failed with
the identical mangled-value error, confirming the test isn't a false-pass
and targets exactly this clause, not some other code path. Restored,
re-verified green.

## What the independent review found

No blocker-class findings. Read `backfillCodelessSyncedVariants`, the
`deleteMissing` retire-in-place mangle, `resolveUpsertRow`'s sticky-COALESCE
logic, `stripRetireMangle`, the `item_variants` `adminTables` entry, and the
new test, plus the diff itself.

- **Confirmed the mangle-shape match is exact**: the new `LIKE` pattern
  mirrors `deleteMissing`'s own `CASE WHEN %s LIKE '%%~' || %s THEN ...`
  byte-for-byte, and `item_variants` does carry `unique: []string{"sku"}`
  in `adminTables`, so the mangle path genuinely applies to this column.
- **Checked scope**: unchanged from the pre-existing function — still only
  `is_active = 1`, still excludes any row with a `variant_barcodes` entry.
  No new class of row becomes reachable.
- **Checked exploitability of the unescaped `LIKE` pattern**:
  `item_variants.id` is always `uuid.NewString()` (`catalog_repo.go`), never
  user-chosen, so `%`/`_` wildcard injection via `id` isn't reachable.
- **Verified the new test is a genuine regression guard**: traced
  `resolveUpsertRow`'s COALESCE by hand and confirmed reverting just the
  WHERE-clause change leaves the mangled value in place post-apply — the
  test would fail on a revert. Independently reproduced this same result
  via the mutation check above.
- Confirmed the one other `item_variants.sku`-mangle-adjacent existing test
  (`sync_admin_repo_test.go`'s FK-pinned-squatter retire test, asserting
  `sku == "COLA~itm-local"`) operates on the `items` table, not
  `item_variants`, and its row is `is_active = 0` — outside this fix's
  scope either way, so no conflict.

## Nearby findings

- **Follow-up filed, not fixed here (ut-docs#2316):**
  `CatalogRepo.VariantsForItem` (`internal/data/catalog_repo.go:732`) —
  the catalog admin per-item edit panel's data source — deliberately
  returns retired variants too, and reads `sku` via `COALESCE(sku, '')`
  with no `stripRetireMangle` call. `item_variants.sku` was never added to
  the ut-docs#1610 six-table sweep `stripRetireMangle`'s own doc comment
  names. A retired variant's raw `"<sku>~<id>"` mangle (independent of
  this card's revival scenario) can reach the catalog admin UI unstripped.
  Every other `item_variants.sku` reader checked
  (`ItemVariantsFor`/`ItemVariantsForSale`/`variantLowStockItems`/
  `variantStockForExport`) filters `is_active = 1` and is unaffected.
  Pre-existing gap, not a regression from this diff — filed as a narrow,
  single-function `complexity:easy` follow-up rather than widening this
  PR's scope.
- **Nit, addressed inline**: the suffix-based `LIKE` match can't
  distinguish a genuine mangle from a real sku that coincidentally ends in
  `"~"+its own row's id` — here that ambiguity would mean a persisted
  overwrite rather than `stripRetireMangle`'s display-only misread. Added
  a one-line doc-comment acknowledging this and why it's not reachable
  (`id` is always a generated UUID, never user-chosen).

## Verified beyond automated tests

- Full gate re-run after the doc-comment addition: `gofmt -l` clean,
  `go build ./...` clean, `go test ./internal/data/...` green (includes
  every sticky/retire-mangle/backfill sibling test in the package).
  `go vet ./...`, `bash scripts/ci/guard-data-access.sh`, and
  `bash scripts/ci/guard-i18n.sh` all clean, run earlier in the same
  session.
- No money values, no user-facing strings/i18n keys, no plugin surface, no
  UI surface touched — pure backend sync-engine SQL + a doc comment.
- No secrets; no real client/shop name as test/demo data.

## Explicitly deferred (not this card)

- The `VariantsForItem` display-leak follow-up above, filed as
  ut-docs#2316.
