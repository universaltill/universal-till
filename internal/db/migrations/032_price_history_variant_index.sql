-- ut-docs#2259: price_history's only index (idx_price_history_item, from
-- 001_init.sql) is item_id-leading, so a WHERE variant_id = ? predicate
-- (POSRepo.lookupPriceHistory, CatalogRepo.ItemVariantsForSale's
-- correlated subquery, and so ResolveCurrentPrice for a variant) can't
-- seek it — EXPLAIN QUERY PLAN against the real migrated schema shows
-- SCAN ph + USE TEMP B-TREE FOR ORDER BY. price_history is append-mostly
-- and never pruned, so this scan only grows, and ut-docs#2228's
-- ItemVariantsForSale multiplies it into one full scan per variant on the
-- touchscreen-interactive picker-open path.
--
-- IF NOT EXISTS: see 021_sales_local_date_index.sql's own comment for why
-- (fiscal_signing_keys_{rename,split}_test.go rewinds the schema_migrations
-- ledger and replays migrations above that boundary against a DB that
-- already physically has these objects from the first Open).
CREATE INDEX IF NOT EXISTS idx_price_history_variant
    ON price_history (variant_id, starts_at);
