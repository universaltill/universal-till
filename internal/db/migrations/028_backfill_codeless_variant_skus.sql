-- 028_backfill_codeless_variant_skus.sql — ut-docs#2230.
--
-- item_variants.sku is nullable (001_init.sql); CatalogRepo.CreateVariant
-- has only auto-generated a SKU for a blank input since ut-docs#1900. No
-- migration ever backfilled the variant rows created BEFORE that fix
-- landed, so a variant can exist today with neither a barcode (in
-- variant_barcodes) nor a sku — "codeless" — nothing to resolve it by at
-- sale time. sellableVariants (internal/pages/pos_modifiers_api.go) and
-- CatalogRepo.ItemIDsWithVariants already filter these out of the
-- sale-screen picker (ut-docs#2209) so nothing mis-prices, but the
-- merchant side stays silently wrong: they configured a size and it just
-- never appears, with no explanation anywhere. Same failure class as
-- ut-docs#1459 ("never an item without a SKU") applied to variants: the
-- fix is to generate-and-surface, not hide.
--
-- Scope: ACTIVE variants only (is_active = 1), matching every other
-- "sellable" check in this codebase (ItemIDsWithVariants, sellableVariants)
-- — an inactive/retired variant is never offered at sale time regardless
-- of its code, so it is deliberately left alone here (and would also just
-- get reactivated with a real code already assigned, in the case a
-- deactivated variant already had one).
--
-- Convention: identical to generatedVariantSKU() (catalog_repo.go) —
-- "VAR-" followed by 8 uppercase hex characters. SQLite's hex(randomblob(4))
-- already returns exactly 8 uppercase hex characters (4 random bytes); the
-- explicit upper() is defensive belt-and-braces only, matching the Go side's
-- own explicit strings.ToUpper call.
--
-- Collision safety (checked BOTH directions, ut-docs#2230's acceptance
-- criterion): a plain per-row `'VAR-' || upper(hex(randomblob(4)))` UPDATE
-- would generate a fresh random value per row, but nothing would stop two
-- rows landing on the same 4 random bytes (however unlikely), or one
-- landing on a SKU some other, already-existing row already carries — and
-- the sku column's UNIQUE constraint would then abort the whole migration
-- rather than silently allow a duplicate. Instead: build a pool of
-- candidate codes up front (a recursive CTE — always materialized once by
-- SQLite, never re-evaluated per row, so its randomness is stable for the
-- rest of the statement), throw out any candidate that collides with a
-- SKU already in the table, de-duplicate what's left, and hand out exactly
-- one surviving candidate per codeless variant via a stable row-number
-- pairing. Every codeless variant therefore gets a code that is (a) in the
-- CreateVariant format, (b) distinct from every pre-existing SKU, and (c)
-- distinct from every OTHER variant fixed by this same run. The pool is
-- sized at "however many are needed, plus 200 spares" — for the tiny
-- backlog this migration actually targets (rows only ever created before
-- ut-docs#1900), the odds of the spare margin ever being exhausted are the
-- same astronomically-small odds CreateVariant's own bounded-retry comment
-- already accepts for a single insert.
--
-- Idempotent / safe to re-run: the `codeless` CTE's WHERE clause only ever
-- matches a variant that STILL has neither a sku nor a barcode, so a
-- variant this migration already fixed (or one CreateVariant already gave
-- a real SKU, or one that simply has a barcode) is never touched again —
-- re-running finds nothing to do and changes nothing. No CREATE TABLE/INDEX
-- here, so the IF NOT EXISTS concern noted in earlier CREATE-TABLE
-- migrations (e.g. 027's header) does not apply: this file is a pure
-- UPDATE, naturally re-appliable against an already-migrated schema, the
-- same as every later migration this repo's replay tests re-run in place.
WITH RECURSIVE codeless(id, rn) AS (
    SELECT v.id, ROW_NUMBER() OVER (ORDER BY v.id)
    FROM item_variants v
    WHERE v.is_active = 1
      AND (v.sku IS NULL OR TRIM(v.sku) = '')
      AND v.id NOT IN (SELECT variant_id FROM variant_barcodes)
),
pool(n, code) AS (
    SELECT 1, 'VAR-' || upper(hex(randomblob(4)))
    UNION ALL
    SELECT n + 1, 'VAR-' || upper(hex(randomblob(4)))
    FROM pool
    WHERE n < (SELECT COUNT(*) FROM codeless) + 200
),
fresh_codes(rn, code) AS (
    SELECT ROW_NUMBER() OVER (), code
    FROM (SELECT DISTINCT code FROM pool)
    WHERE code NOT IN (SELECT sku FROM item_variants WHERE sku IS NOT NULL)
)
UPDATE item_variants
SET sku = (
    SELECT fc.code FROM fresh_codes fc
    WHERE fc.rn = (SELECT c.rn FROM codeless c WHERE c.id = item_variants.id)
)
WHERE id IN (SELECT id FROM codeless);
