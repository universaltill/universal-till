package db

import (
	"regexp"
	"testing"
)

// backfillCodelessVariantSkusMigrationVersion is
// 028_backfill_codeless_variant_skus.sql (ut-docs#2230). If the file is
// ever renumbered, this constant moves with it.
const backfillCodelessVariantSkusMigrationVersion = 28

// generatedSKUPattern matches generatedVariantSKU()'s own convention
// ("VAR-" + 8 uppercase hex chars) exactly — the migration must produce
// codes indistinguishable from ones CreateVariant would have generated.
var generatedSKUPattern = regexp.MustCompile(`^VAR-[0-9A-F]{8}$`)

func skuOf(t *testing.T, d *DB, variantID string) string {
	t.Helper()
	var sku *string
	if err := d.DB.QueryRow(`SELECT sku FROM item_variants WHERE id = ?`, variantID).Scan(&sku); err != nil {
		t.Fatalf("read sku for %s: %v", variantID, err)
	}
	if sku == nil {
		return ""
	}
	return *sku
}

// TestMigration028_BackfillsCodelessActiveVariants pins ut-docs#2230's
// backfill migration against every case its acceptance criteria call out:
// (a) a pre-existing codeless ACTIVE variant gets a generated SKU; (b) a
// variant that already has a SKU, or already has a barcode, is left
// completely untouched; (c) an INACTIVE codeless variant is deliberately
// left alone (never offered at sale time regardless of its code); (d) two
// codeless variants in the same run get two DIFFERENT generated codes; and
// (e) running the migration twice changes nothing the second time
// (idempotent / safe to re-run).
func TestMigration028_BackfillsCodelessActiveVariants(t *testing.T) {
	d, path := openAtPreMigrationSchema(t, backfillCodelessVariantSkusMigrationVersion, "codeless-variants.db")
	_ = path
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.DB.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	exec(`INSERT INTO items (id, sku, name, base_price) VALUES ('itm-a', 'ITEM-A', 'Coffee', 300)`)

	// (a) codeless + active: must be backfilled.
	exec(`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-codeless-a', 'itm-a', NULL, 'Small', 250, 1)`)
	// (d) a second codeless + active variant, to prove distinct codes.
	exec(`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-codeless-b', 'itm-a', '', 'Large', 350, 1)`)
	// (b) already has a SKU: must be untouched.
	exec(`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-has-sku', 'itm-a', 'REG-001', 'Regular', 300, 1)`)
	// (b) blank SKU but HAS a barcode: must be untouched (resolvable by barcode already).
	exec(`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-has-barcode', 'itm-a', NULL, 'XL', 400, 1)`)
	exec(`INSERT INTO variant_barcodes (barcode, variant_id, is_primary) VALUES ('5012345678900', 'v-has-barcode', 1)`)
	// (c) codeless but INACTIVE: must be left alone.
	exec(`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-codeless-inactive', 'itm-a', NULL, 'Discontinued', 300, 0)`)

	m := loadMigrationVersion(t, backfillCodelessVariantSkusMigrationVersion)
	if err := d.applyMigration(m); err != nil {
		t.Fatalf("apply 028: %v", err)
	}

	// (a) + convention check.
	skuA := skuOf(t, d, "v-codeless-a")
	if !generatedSKUPattern.MatchString(skuA) {
		t.Fatalf("v-codeless-a sku = %q, want to match %s", skuA, generatedSKUPattern.String())
	}
	skuB := skuOf(t, d, "v-codeless-b")
	if !generatedSKUPattern.MatchString(skuB) {
		t.Fatalf("v-codeless-b sku = %q, want to match %s", skuB, generatedSKUPattern.String())
	}

	// (d) distinct codes.
	if skuA == skuB {
		t.Fatalf("both codeless variants got the SAME generated sku %q — collision not avoided", skuA)
	}

	// (b) untouched cases.
	if got := skuOf(t, d, "v-has-sku"); got != "REG-001" {
		t.Fatalf("v-has-sku sku = %q, want unchanged REG-001", got)
	}
	if got := skuOf(t, d, "v-has-barcode"); got != "" {
		t.Fatalf("v-has-barcode sku = %q, want still blank (it already has a barcode, generating a SKU too is not this migration's job)", got)
	}

	// (c) inactive left alone.
	if got := skuOf(t, d, "v-codeless-inactive"); got != "" {
		t.Fatalf("v-codeless-inactive sku = %q, want still blank (inactive variants are out of scope)", got)
	}

	// (e) idempotency: rewind only the ledger row (rows/table stay, exactly
	// what a renumbered/pre-merge build leaves behind on a device) and
	// re-apply — no error, no changed/duplicated SKUs.
	exec(`DELETE FROM schema_migrations WHERE version = ?`, backfillCodelessVariantSkusMigrationVersion)
	if err := d.applyMigration(m); err != nil {
		t.Fatalf("replaying 028 against an already-backfilled database: %v", err)
	}
	if got := skuOf(t, d, "v-codeless-a"); got != skuA {
		t.Fatalf("v-codeless-a sku changed on replay: %q -> %q", skuA, got)
	}
	if got := skuOf(t, d, "v-codeless-b"); got != skuB {
		t.Fatalf("v-codeless-b sku changed on replay: %q -> %q", skuB, got)
	}
	if got := skuOf(t, d, "v-has-sku"); got != "REG-001" {
		t.Fatalf("v-has-sku sku changed on replay: %q", got)
	}
	if got := skuOf(t, d, "v-codeless-inactive"); got != "" {
		t.Fatalf("v-codeless-inactive sku changed on replay: %q", got)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, backfillCodelessVariantSkusMigrationVersion).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("schema_migrations has version %d %d time(s), want 1", backfillCodelessVariantSkusMigrationVersion, n)
	}
}

// TestMigration028_ManyCodelessVariantsAllGetDistinctCodes stresses the
// candidate-pool sizing (count + 200 spares) with more rows than the
// hand-picked pair above, to catch a pool-exhaustion regression that a
// 2-row test could miss.
func TestMigration028_ManyCodelessVariantsAllGetDistinctCodes(t *testing.T) {
	d, _ := openAtPreMigrationSchema(t, backfillCodelessVariantSkusMigrationVersion, "codeless-variants-many.db")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.DB.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO items (id, sku, name, base_price) VALUES ('itm-many', 'ITEM-MANY', 'Mug', 300)`)

	const n = 40
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := "v-many-" + string(rune('a'+i))
		ids = append(ids, id)
		exec(`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES (?, 'itm-many', NULL, ?, 300, 1)`, id, id)
	}

	m := loadMigrationVersion(t, backfillCodelessVariantSkusMigrationVersion)
	if err := d.applyMigration(m); err != nil {
		t.Fatalf("apply 028 against %d codeless variants: %v", n, err)
	}

	seen := map[string]bool{}
	for _, id := range ids {
		sku := skuOf(t, d, id)
		if !generatedSKUPattern.MatchString(sku) {
			t.Fatalf("variant %s sku = %q, want to match %s", id, sku, generatedSKUPattern.String())
		}
		if seen[sku] {
			t.Fatalf("duplicate generated sku %q across codeless variants", sku)
		}
		seen[sku] = true
	}
	if len(seen) != n {
		t.Fatalf("got %d distinct skus, want %d", len(seen), n)
	}
}

// TestMigration028_IsOnDisk guards the number this file's sibling tests
// hardcode: version 028 must be THIS file, so a concurrent card that lands
// a 028 of its own is caught here rather than by a confusing checksum
// mismatch on a till (see 025's identical guard).
func TestMigration028_IsOnDisk(t *testing.T) {
	m := loadMigrationVersion(t, backfillCodelessVariantSkusMigrationVersion)
	if m.Name != "028_backfill_codeless_variant_skus.sql" {
		t.Fatalf("migration %d on disk is %q, want 028_backfill_codeless_variant_skus.sql", backfillCodelessVariantSkusMigrationVersion, m.Name)
	}
}
