# Code review — migration 028 backfill hardening (ut-docs#2247)

- **Date:** 2026-09-18
- **Ticket:** ut-docs#2247 (`complexity:medium`, follow-up from the
  ut-docs#2230 review of `028_backfill_codeless_variant_skus.sql`)
- **Branch:** `fix/2247-migration-028-hardening`
- **Reviewer:** independent pass, Opus subagent (per this card's
  `complexity:medium` routing, `MODEL-ROUTING.md`), isolated in its own
  git worktree.
- **Verdict: SAFE TO MERGE**, after fixing the two blocking findings the
  independent review raised (both addressed below, with their own
  TDD-verified regression tests).

## The task

`028_backfill_codeless_variant_skus.sql` (ut-docs#2230) backfills a
generated SKU onto any ACTIVE `item_variants` row that has neither a SKU
nor a barcode ("codeless"), so it can be resolved at sale time. Its own
follow-up review (ut-docs#2247) bundled four non-blocking findings; three
were implemented here:

1. **Loud failure on pool exhaustion** — 028's candidate-code pool
   (codeless count + 200 spares) makes exhaustion astronomically unlikely
   (~1.3M codeless variants by birthday-bound math), but a correlated
   `UPDATE` that silently left some target rows untouched would still
   record the migration as successfully applied, no error, no log.
2. **Codeless-definition drift** — migration 028 and
   `sync_admin_repo.go`'s `backfillCodelessSyncedVariants` treat a
   whitespace-only SKU as codeless (`TRIM(sku) = ''`); every read path
   that decides "is this variant sellable/resolvable" used a plain
   `COALESCE(v.sku, '')`, disagreeing on a whitespace-only value.
   `CatalogRepo.CreateVariant`'s own write-side
   `strings.TrimSpace(in.SKU) == ""` check confirms TRIM is the intended
   canonical definition.
3. **Help text** — `catalog.variant_codeless_hint` (the "won't sell"
   badge's tooltip) didn't mention the badge is active-variant-only
   scoped.

**Non-goal, explicitly out of scope**: item 4, the migration's own O(n²)
correlated-subquery cost. `internal/db/migrations/` files are
append-only/frozen the moment they merge to `main` (ADR-0100) —
`028_backfill_codeless_variant_skus.sql` already merged, so its SQL
literally cannot be rewritten; a statement-level edit fails
`TestShippedMigrationsUnchanged` in CI. Documented in a comment on
ut-docs#2247 rather than attempted here.

## The fix

### 1. Loud failure on pool exhaustion

- New `internal/db/migration_028_postcheck.go`:
  `verifyMigration028BackfillTx(tx *sql.Tx) error` queries for any ACTIVE
  `item_variants` row still lacking both a SKU and a barcode, and returns
  a descriptive error naming the count if any remain.
- Wired into `internal/db/db.go`'s `applyMigration` (not `migrateUpTo`'s
  loop) — **inside migration 028's own transaction**, right after
  `execMigrationStatements` and before the `schema_migrations` ledger
  insert. Also wired into `reapplyMigration` for the (currently unused)
  case 028 is ever allowlisted for idempotent re-apply.
- `postApplyMigration028Check` is a package var (not a direct call)
  specifically so a test can prove the wiring itself without reproducing
  real pool exhaustion.

### 2. Codeless-definition drift — converged on TRIM

Changed four read paths in `internal/data/catalog_repo.go` from
`COALESCE(v.sku, '')` to `TRIM(COALESCE(v.sku, ''))`:
`CatalogRepo.ItemVariantsFor`, `ItemVariantsForSale`,
`ItemIDsWithVariants`, `VariantsForItem` (the query the catalog-admin
"won't sell" badge actually renders from), and `GetVariantLabel` (the
shelf-label print path's barcode-else-SKU fallback).

### 3. Help text

`web/locales/{en,fa,tr,ar}.json`'s `catalog.variant_codeless_hint` now
states the badge only appears for active variants; the corresponding
prose in `web/help/{en,de,fa,tr,ar}/catalog.md` updated to match (the
manual-ships-with-the-feature standing rule). A comment on
`CatalogRepo.DeleteBarcode` notes it can produce a fresh codeless variant
post-migration — the badge already covers this case.

## What the independent review found, and what changed as a result

**Blocking, both fixed:**

- **B1 — `VariantsForItem` was missed in the first pass.** The
  catalog-admin badge renders from `VariantEditView`
  (`VariantsForItem`'s return type), not `VariantView` — the three
  originally-converged methods never feed the badge at all. Net effect
  before the fix: a whitespace-SKU variant would stop being offered at
  the till (`ItemIDsWithVariants` now excludes it) while the admin badge
  that's supposed to warn about exactly that stayed silent, reproducing
  the original ut-docs#2209 money-defect shape (parent-base-price
  fallback) with no visible cause. Fixed by converging `VariantsForItem`
  too, with `TestVariantsForItem_TreatsWhitespaceOnlySKUAsCodeless`
  (`internal/data/catalog_repo_crud_test.go`) — confirmed failing
  pre-fix, passing post-fix. The incorrect doc-comment claim that
  `VariantView.SKU` covers "the catalog-admin codeless badge" was also
  corrected (`catalog_repo.go`'s `VariantView.SKU` comment and the
  matching test comment).
- **B2 — the post-check ran after 028's transaction committed.** A
  failure would fail one boot loudly, but the ledger row already existed
  by the time the check ran, so a restart just skipped 028 forever with
  the partial backfill never revisited — the opposite of what "loud
  failure" was supposed to buy. Fixed by moving the check inside 028's
  own transaction (see above): a failure now rolls back both the
  backfill's `UPDATE` and the ledger insert together, so the next boot
  retries 028 from scratch with a fresh candidate pool (self-healing).
  `TestApplyMigration028_RollsBackTheWholeMigrationWhenThePostCheckFails`
  (`internal/db/migration_028_postcheck_test.go`) proves both the wiring
  and the rollback property, and that a retry after rollback succeeds and
  records exactly one ledger row. Confirmed this test itself fails
  against the pre-fix (post-commit) placement.

**Non-blocking, addressed:**

- Redundant wording in the EN tooltip ("This **active** variant… This
  warning only appears for **active** variants") — simplified to state
  the scoping once.
- A stray smart-quote character in a doc comment — fixed to a plain `''`.
- `GetVariantLabel`'s barcode-else-SKU fallback was the one remaining
  unconverged read path (the shelf-label print path) — converged too,
  with `TestGetVariantLabel_TreatsWhitespaceOnlySKUAsCodeless`, TDD
  reverted/confirmed.
- `verifyMigration028BackfillTx` switched from `NOT IN (SELECT
  variant_id FROM variant_barcodes)` to `NOT EXISTS (...)` — the more
  NULL-safe idiom, matching `backfillCodelessSyncedVariants`'s own
  convention (safe either way here since `variant_barcodes.variant_id`
  is `NOT NULL`, but the robust idiom either way).
- `web/help/{en,de,fa,tr,ar}/catalog.md` updated alongside the tooltip —
  confirmed via `guard-help-drift.sh` that no new structural drift was
  introduced.

**Noted, not actioned this card:**

- The external `ut-plugin-language-{de,es}` packs now carry a stale
  translation of the changed `catalog.variant_codeless_hint` value.
  Neither `lang-pack-drift` (key-presence only) nor
  `locale-render-audit` (matches current EN value) will catch this.
  Those repos are outside this session's scope — flagged for a follow-up
  Backlog card rather than actioned here.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l .` (empty), `golangci-lint
  run ./internal/db/... ./internal/data/...` (0 issues) all clean.
- `go test ./internal/db/... ./internal/data/... ./internal/pages/...`
  and the independent reviewer's own additional
  `./internal/pos/... ./internal/ui/... ./internal/httpx/...` sweep (the
  real consumers of the changed queries) all pass.
- `guard-i18n.sh`, `guard-data-access.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh`, `guard-help-drift.sh` all pass.
- Every TDD claim in this diff (checker pass/fail, the migration-028
  wiring, the transactional-rollback property, and all five
  whitespace-only-SKU convergence tests) was personally
  reverted-then-restored — each failed with its exact claimed error
  against the unfixed code, then passed once fixed.
- Backend-only change; no UI surface touched (existing tooltip text
  only) — no e2e/Playwright run needed.

## Deferred

- The O(n²) migration cost (item 4 of the original bundle) — genuinely
  frozen per ADR-0100, documented on ut-docs#2247 as won't-fix unless a
  future card proves an install actually hits the ~1.3M-row boundary.
- Translating the changed locale string into `ut-plugin-language-de`/`-es`
  — outside this session's repo scope; flagged for a follow-up card.
