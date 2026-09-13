package data

import (
	"context"
	"regexp"
	"testing"
)

// generatedSyncSKUPattern mirrors generatedVariantSKU()'s own convention
// ("VAR-" + 8 uppercase hex chars) — a variant this backfill fixes must be
// indistinguishable from one CreateVariant generated directly.
var generatedSyncSKUPattern = regexp.MustCompile(`^VAR-[0-9A-F]{8}$`)

// TestApplyAdmin_BackfillsCodelessSyncedVariant covers ut-docs#2230's
// second write path: ApplyAdmin's generic upsert (execUpsertBatch) copies
// whatever sku value the primary sent, entirely bypassing
// CatalogRepo.CreateVariant's blank-SKU generation — the same class of gap
// this file's own adminTables comment already calls out ("two tills
// mid-rollout on different migration versions"). A primary behind on this
// fix (or on ut-docs#1900 itself) can hand a satellite a genuinely codeless
// item_variants row this way, even after the satellite's own local 028
// migration has already run once at boot and has nothing left of its own
// to catch. ApplyAdmin must leave no active, barcode-less, sku-less variant
// behind once the bundle lands — a defensive backfill after the generic
// upsert, mirroring 028's own exact convention and scope.
func TestApplyAdmin_BackfillsCodelessSyncedVariant(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary-codeless.db")
	replica := openMigratedDB(t, "replica-codeless.db")

	mustExec(t, primary, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm1', 'ITEM-1', 'Coffee', 300)`)
	// Written directly, bypassing CatalogRepo.CreateVariant entirely — the
	// exact shape a version-skewed primary (or any other non-CreateVariant
	// insert path) could produce: active, no sku, no barcode.
	mustExec(t, primary, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-codeless', 'itm1', NULL, 'Small', 250, 1)`)
	// A second variant, already fine, must be left exactly as sent.
	mustExec(t, primary, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-fine', 'itm1', 'REG-001', 'Regular', 300, 1)`)
	// An inactive codeless variant travels too, but is out of scope — same
	// as 028's own migration scope.
	mustExec(t, primary, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-inactive', 'itm1', NULL, 'Retired', 300, 0)`)

	repo := NewSyncAdminRepo(primary.DB)
	bundle, err := repo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}

	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var sku *string
	if err := replica.DB.QueryRow(`SELECT sku FROM item_variants WHERE id = 'v-codeless'`).Scan(&sku); err != nil {
		t.Fatalf("synced variant missing: %v", err)
	}
	if sku == nil || !generatedSyncSKUPattern.MatchString(*sku) {
		got := "<nil>"
		if sku != nil {
			got = *sku
		}
		t.Fatalf("v-codeless sku on replica = %q, want to match %s (ApplyAdmin must backfill a synced codeless variant)", got, generatedSyncSKUPattern.String())
	}

	var fineSKU string
	if err := replica.DB.QueryRow(`SELECT sku FROM item_variants WHERE id = 'v-fine'`).Scan(&fineSKU); err != nil {
		t.Fatalf("synced variant missing: %v", err)
	}
	if fineSKU != "REG-001" {
		t.Fatalf("v-fine sku on replica = %q, want unchanged REG-001", fineSKU)
	}

	var inactiveSKU *string
	if err := replica.DB.QueryRow(`SELECT sku FROM item_variants WHERE id = 'v-inactive'`).Scan(&inactiveSKU); err != nil {
		t.Fatalf("synced variant missing: %v", err)
	}
	if inactiveSKU != nil {
		t.Fatalf("v-inactive sku on replica = %q, want still NULL (inactive variants are out of scope)", *inactiveSKU)
	}

	// Re-applying the identical bundle (a repeat poll from a primary that is
	// STILL on the old, pre-fix state) must not error, and must never leave
	// the row genuinely codeless again: the generic upsert re-writes
	// sku=NULL from the stale bundle first (primary wins), so the backfill
	// re-fires and hands out a fresh code. The exact value need not be
	// STABLE across repeats — only ever-present — until the lagging primary
	// itself boots the fix and the version skew resolves for good.
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	var again *string
	if err := replica.DB.QueryRow(`SELECT sku FROM item_variants WHERE id = 'v-codeless'`).Scan(&again); err != nil {
		t.Fatal(err)
	}
	if again == nil || !generatedSyncSKUPattern.MatchString(*again) {
		got := "<nil>"
		if again != nil {
			got = *again
		}
		t.Fatalf("v-codeless sku after re-applying a still-stale bundle = %q, want to match %s (must never go back to codeless)", got, generatedSyncSKUPattern.String())
	}
}
