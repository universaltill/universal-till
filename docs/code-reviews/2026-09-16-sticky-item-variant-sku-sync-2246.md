# Code review — sticky item_variants.sku on LAN admin-sync (ut-docs#2246)

- **Date:** 2026-09-16
- **Ticket:** ut-docs#2246 (`complexity:easy`)
- **Branch:** `fix/2246-sticky-synced-variant-sku`
- **Reviewer:** independent pass, fresh-context Sonnet subagent (per this
  card's `complexity:easy` routing — different context from the
  implementation, never saw the dev reasoning).
- **Verdict: SAFE TO MERGE AS-IS.** No blocking findings.

## The bug

`backfillCodelessSyncedVariants` (ut-docs#2230) assigns a fresh generated
SKU to a synced `item_variants` row when the primary till is still behind
on ut-docs#1900/#2230 and keeps sending a blank sku. It was documented as
"deliberately NOT sticky across repeated polls" — a still-behind primary
regenerates a *different* SKU on every single `ApplyAdmin` poll, because
the generic upsert engine (`execUpsertBatch`/`resolveUpsertRow`) writes
whatever the primary sent, including a repeated blank, before the backfill
re-fires. `GetVariantLabel` prints this SKU as the scannable code when a
variant has no barcode, and `pos_repo.go` resolves a scan by exact `sku`
match — so a shelf label printed from the replica between two polls could
stop scanning the moment the next poll regenerated a different code.

## What shipped

- `internal/data/sync_admin_repo.go`: new `stickyNonBlankCols []string`
  field on `adminTable`, same shape/idiom as the existing `skipCols`/
  `redactCols`. Set to `[]string{"sku"}` for the `item_variants` entry
  only. `resolveUpsertRow` now emits
  `SET col = COALESCE(NULLIF(excluded.col, ''), col)` for a sticky column
  instead of the default `col = excluded.col` — a blank (NULL or `''`)
  incoming value no longer overwrites an existing non-blank local value; a
  real non-blank incoming value still overwrites normally, so "primary
  always wins" is preserved for every actual value. Mirrors
  `CatalogRepo.UpdateVariant`'s existing, long-standing
  `SET sku = COALESCE(NULLIF(?, ''), sku)` — blank has never meant "clear
  it" anywhere else in this codebase; this brings the one path that didn't
  follow that rule into line with it.
- `internal/data/sync_admin_codeless_variant_backfill_test.go`: the
  existing `TestApplyAdmin_BackfillsCodelessSyncedVariant`'s final
  assertion, which previously documented and enforced the bug as intended
  ("need not be STABLE across repeats"), now asserts the SKU is unchanged
  across a repeated poll from a still-stale primary. New
  `TestApplyAdmin_RealSKUStillOverwritesBackfilledOne` proves a primary
  that later sends a real, non-blank sku still overwrites the previously
  backfilled one.

## What the independent review found

No blocker-class findings. Ran the full gate (`go build ./...`,
`go vet ./internal/data/...`, `go test ./internal/data/...` verbose on
both touched tests) — all green — and went further than reading the diff:

- **Verified the SQL semantics directly against SQLite's actual upsert
  behavior**, not just by reading it: confirmed an unqualified column name
  inside `ON CONFLICT ... DO UPDATE SET` refers to the pre-update
  (existing) row, matching the file's own established `col = excluded.col`
  usage elsewhere — not a "wrong row" risk. Confirmed `NULLIF(x, '')`
  collapses identically whether the incoming value was SQL `NULL` or `''`.
- **TDD re-verification, done for real**: reverted only the
  `resolveUpsertRow` SQL change (keeping the new/modified tests) —
  `TestApplyAdmin_BackfillsCodelessSyncedVariant` failed with the exact
  "want unchanged" error the test names; restored, passed again.
  Separately confirmed `TestApplyAdmin_RealSKUStillOverwritesBackfilledOne`
  is a real regression guard (not vacuous against the fix itself, since
  plain `col = excluded.col` already satisfies it) by swapping the sticky
  SET clause for a never-overwrite stub and confirming that test fails.
- **Checked scoping**: `stickyNonBlankCols` only populates from
  `t.stickyNonBlankCols`, which only `item_variants` sets, and only for
  `sku` — no other table or column is affected.
- **Checked interaction with `backfillCodelessSyncedVariants` itself**:
  runs after all upserts in the same transaction and only re-fires on
  `sku IS NULL OR TRIM(sku) = ''`; since the sticky COALESCE now preserves
  the previously-backfilled non-blank value, the backfill's own WHERE
  clause no longer matches it — correctly stops re-generating, without
  affecting detection of a genuinely-codeless row on first sync (a plain
  `INSERT`, which the `ON CONFLICT` SET clause never touches).
- **Considered whether the generic list-based abstraction was
  over-engineered** for a single-column, single-table use case (vs. a
  hardcoded special-case matching the `archive_min_days`/`country_settings`
  precedent already in the file) — concluded the list-based approach is
  the better fit here, since it matches the existing `skipCols`/
  `redactCols` idiom (declarative, handled once in `resolveUpsertRow`)
  rather than adding a third ad hoc special-case branch.
- Verified the `CatalogRepo.UpdateVariant` precedent claim in the new doc
  comment against `internal/data/catalog_repo.go:2311` directly — exact
  match, no exaggeration.

## Non-blocker — not fixed in this diff, filed as a follow-up

- **Retire-mangle interaction under primary version skew** (ut-docs#2273):
  `deleteMissing`'s retire-in-place mangle rewrites a FK-blocked row's
  `sku` to `"<sku>~<id>"` — non-blank, so now sticky. `stripRetireMangle`
  is never applied to `item_variants.sku`. If a retired variant ID is
  later revived by a bundle from a primary still sending a blank sku, the
  sticky COALESCE now freezes the mangled garbage value in place instead
  of the pre-fix behavior of overwriting to NULL and letting the backfill
  generate a fresh real-looking SKU. Narrow (three conditions must all
  hold at once), not confirmed in production, filed rather than fixed
  here since it's outside this card's scope.

## Verified beyond automated tests

- Grepped the diff for `os.Create|WriteFile|MkdirAll|ioutil|filepath.Join|
  paths.` — zero hits; no file I/O introduced, neither of this pipeline's
  two recurring bug classes (missing `MkdirAll`, cwd-relative path vs.
  `paths.Data(...)`) applies.
- No money values, no user-facing strings, no i18n keys, no plugin
  surface, no UI surface touched — `guard-i18n.sh` and `guard-data-access.sh`
  both re-run clean; backend-only sync-engine change.
- No secrets; no real client/shop name as test/demo data.

## Explicitly deferred (not this card)

- The retire-mangle follow-up above, filed as ut-docs#2273.
