package db

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// TestVerifyMigration028BackfillTx_PassesWhenNoCodelessVariantsRemain proves
// the happy path: no active variant lacking both a SKU and a barcode passes
// the check silently.
func TestVerifyMigration028BackfillTx_PassesWhenNoCodelessVariantsRemain(t *testing.T) {
	d, _ := openAtPreMigrationSchema(t, backfillCodelessVariantSkusMigrationVersion, "postcheck-clean.db")
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	if err := verifyMigration028BackfillTx(tx); err != nil {
		t.Fatalf("verifyMigration028BackfillTx() on a clean database = %v, want nil", err)
	}
}

// TestVerifyMigration028BackfillTx_FailsLoudlyWhenCodelessVariantsRemain pins
// ut-docs#2247's loud-failure requirement: migration 028's own candidate
// pool (codeless count + 200 spares) makes exhaustion astronomically
// unlikely in real use (~1.3M codeless variants by birthday-bound math), so
// it can't be reproduced by seeding real data through the real CTE. This
// test instead proves the checker itself — the thing that would actually
// catch a real exhaustion, by inserting an active variant with neither a
// SKU nor a barcode directly (bypassing CreateVariant, the same shape a
// candidate pool running dry would leave behind) and confirming the
// post-condition check reports it loudly rather than letting boot succeed
// silently.
func TestVerifyMigration028BackfillTx_FailsLoudlyWhenCodelessVariantsRemain(t *testing.T) {
	d, _ := openAtPreMigrationSchema(t, backfillCodelessVariantSkusMigrationVersion, "postcheck-dirty.db")

	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price) VALUES ('itm-leftover', 'ITEM-LEFTOVER', 'Tea', 250)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	// Simulates a row the candidate pool ran dry before reaching — active,
	// neither sku nor barcode.
	if _, err := d.DB.Exec(`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-leftover', 'itm-leftover', NULL, 'Loose', 250, 1)`); err != nil {
		t.Fatalf("seed leftover codeless variant: %v", err)
	}

	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	err = verifyMigration028BackfillTx(tx)
	if err == nil {
		t.Fatal("verifyMigration028BackfillTx() = nil, want an error naming the leftover codeless variant")
	}
	if !strings.Contains(err.Error(), "1 active variant") {
		t.Fatalf("verifyMigration028BackfillTx() error = %q, want it to name the count (1)", err.Error())
	}
	if !strings.Contains(err.Error(), "neither a SKU nor a barcode") {
		t.Fatalf("verifyMigration028BackfillTx() error = %q, want it to explain what's missing", err.Error())
	}
}

// TestApplyMigration028_RollsBackTheWholeMigrationWhenThePostCheckFails is
// ut-docs#2247's independent-review fix: the post-check must run INSIDE
// migration 028's own transaction, not after it commits, or a genuine
// exhaustion fails loudly exactly once and then silently self-clears on
// the next boot (the ledger row already committed, so migrateUpTo skips
// 028 forever without ever revisiting the gap). This proves both halves at
// once: (a) applyMigration actually calls postApplyMigration028Check for
// migration 028 — proven with a stub, since real exhaustion can't be
// reproduced (~1.3M rows) — and (b) a failing check rolls back the ledger
// insert together with the backfill, so the migration is NOT recorded as
// applied and a later boot will retry it from scratch with a fresh
// candidate pool (self-healing) rather than getting stuck.
func TestApplyMigration028_RollsBackTheWholeMigrationWhenThePostCheckFails(t *testing.T) {
	d, _ := openAtPreMigrationSchema(t, backfillCodelessVariantSkusMigrationVersion, "postcheck-rollback.db")
	m := loadMigrationVersion(t, backfillCodelessVariantSkusMigrationVersion)

	orig := postApplyMigration028Check
	defer func() { postApplyMigration028Check = orig }()
	called := false
	stubErr := errors.New("stub post-apply check failure")
	postApplyMigration028Check = func(*sql.Tx) error {
		called = true
		return stubErr
	}

	err := d.applyMigration(m)
	if !called {
		t.Fatal("postApplyMigration028Check was never invoked — applyMigration is not wired to the post-condition check for migration 028")
	}
	if !errors.Is(err, stubErr) {
		t.Fatalf("applyMigration(28) = %v, want it to propagate the post-apply check's error", err)
	}

	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, backfillCodelessVariantSkusMigrationVersion).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("schema_migrations has %d row(s) for version 28 after a failed post-check, want 0 — the ledger insert must roll back together with the backfill, not commit anyway", n)
	}

	// Self-healing: since nothing committed, a retry with the real check
	// must succeed and record exactly one ledger row.
	postApplyMigration028Check = orig
	if err := d.applyMigration(m); err != nil {
		t.Fatalf("retry after rollback: apply 028 = %v, want success (self-healing)", err)
	}
	var n2 int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, backfillCodelessVariantSkusMigrationVersion).Scan(&n2); err != nil {
		t.Fatal(err)
	}
	if n2 != 1 {
		t.Fatalf("schema_migrations has %d row(s) for version 28 after the successful retry, want exactly 1", n2)
	}
}

// TestMigrateUpTo_StillSucceedsThroughMigration028WithTheNewCheckWired is
// the wiring regression test: a normal upgrade through a pre-028 schema
// carrying real codeless variants (which 028 itself backfills correctly)
// must still complete cleanly now that applyMigration calls the
// post-condition check inside 028's own transaction — the check must not
// false-positive on the ordinary, successful case.
func TestMigrateUpTo_StillSucceedsThroughMigration028WithTheNewCheckWired(t *testing.T) {
	d, path := openAtPreMigrationSchema(t, backfillCodelessVariantSkusMigrationVersion, "postcheck-wiring.db")
	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price) VALUES ('itm-wiring', 'ITEM-WIRING', 'Cocoa', 275)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-wiring', 'itm-wiring', NULL, 'Mug', 275, 1)`); err != nil {
		t.Fatalf("seed codeless variant: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close pre-028 db: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open (full migrate, including 028's new post-check): %v", err)
	}
	defer reopened.Close()

	var sku *string
	if err := reopened.DB.QueryRow(`SELECT sku FROM item_variants WHERE id = 'v-wiring'`).Scan(&sku); err != nil {
		t.Fatalf("read backfilled sku: %v", err)
	}
	if sku == nil || *sku == "" {
		t.Fatalf("v-wiring sku = %v, want a real backfilled value (028 should still have fixed it)", sku)
	}
}
