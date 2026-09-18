package db

import (
	"database/sql"
	"fmt"
)

// backfillCodelessVariantSkusMigrationVersion is
// 028_backfill_codeless_variant_skus.sql (ut-docs#2230). If the file is
// ever renumbered, this constant moves with it. Lives in a non-test file
// (not the migration's own _test.go, where it used to live) because
// applyMigration (db.go) and verifyMigration028BackfillTx below both need
// it at runtime, not just in tests.
const backfillCodelessVariantSkusMigrationVersion = 28

// verifyMigration028BackfillTx is ut-docs#2247's loud-failure backstop for
// 028_backfill_codeless_variant_skus.sql. That migration's candidate-code
// pool is sized "however many codeless rows exist, plus 200 spares" — for
// the pool to run dry (leaving some rows with sku = NULL rather than a
// generated code) needs roughly 1.3M codeless active variants by
// birthday-bound math, not reachable in practice today. But 028's SQL is
// frozen the moment it merges to main (ADR-0100) — it can never be
// rewritten to check its own work — and a correlated UPDATE that silently
// leaves some target rows untouched still records the migration as
// successfully applied, no error, no log.
//
// Runs INSIDE migration 028's own transaction (applyMigration, db.go) —
// not after it commits — so a failure rolls back both the backfill's
// UPDATE and the schema_migrations ledger insert together (independent
// review finding, ut-docs#2247: an earlier version ran this post-commit,
// which meant a failed boot recorded 028 as applied anyway, so restarting
// just skipped it forever with the partial backfill never revisited). With
// the check inside the transaction, a genuine exhaustion leaves 028
// entirely unapplied — the next boot retries it from scratch with a fresh
// randomblob candidate pool, self-healing, instead of a one-time loud
// failure that silently reverts to a quiet pass on restart.
func verifyMigration028BackfillTx(tx *sql.Tx) error {
	var n int
	if err := tx.QueryRow(`
SELECT COUNT(*)
FROM item_variants v
WHERE v.is_active = 1
  AND (v.sku IS NULL OR TRIM(v.sku) = '')
  AND NOT EXISTS (SELECT 1 FROM variant_barcodes b WHERE b.variant_id = v.id)`).Scan(&n); err != nil {
		return fmt.Errorf("verify migration %d backfill: %w", backfillCodelessVariantSkusMigrationVersion, err)
	}
	if n > 0 {
		return fmt.Errorf("migration %d (backfill codeless variant skus) left %d active variant(s) with neither a SKU nor a barcode — the candidate-code pool was likely exhausted; see ut-docs#2247", backfillCodelessVariantSkusMigrationVersion, n)
	}
	return nil
}

// postApplyMigration028Check is a var, not a direct call, purely so a test
// can prove applyMigration (db.go) actually invokes the real check inside
// migration 028's own transaction — real pool exhaustion needs ~1.3M rows
// and can't be reproduced in a test, so black-box testing the call site
// otherwise has no way to fail if it were ever deleted. Every real boot
// always calls through to verifyMigration028BackfillTx; only a test swaps
// this.
var postApplyMigration028Check = verifyMigration028BackfillTx
