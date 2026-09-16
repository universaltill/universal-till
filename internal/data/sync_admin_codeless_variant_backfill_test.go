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
	// the row genuinely codeless again. ut-docs#2246: unlike every other
	// adminTables column, item_variants.sku is sticky against a blank
	// incoming value once it has a real one — the generic upsert's
	// COALESCE(NULLIF(excluded.sku, ''), sku) (mirroring CatalogRepo.
	// UpdateVariant's own long-standing "blank never means clear" rule)
	// leaves the already-backfilled SKU untouched, so the value must be
	// STABLE across repeats, not just ever-present: a shelf label printed
	// from the replica between two polls must keep scanning.
	first := *sku
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	var again *string
	if err := replica.DB.QueryRow(`SELECT sku FROM item_variants WHERE id = 'v-codeless'`).Scan(&again); err != nil {
		t.Fatal(err)
	}
	if again == nil || *again != first {
		got := "<nil>"
		if again != nil {
			got = *again
		}
		t.Fatalf("v-codeless sku after re-applying a still-stale bundle = %q, want unchanged %q (a backfilled SKU must survive repeated blank polls, or a printed/scanned label breaks)", got, first)
	}
}

// TestApplyAdmin_RealSKUStillOverwritesBackfilledOne covers the other half
// of ut-docs#2246's contract: stickiness must never become permanent
// resistance to the primary — once the lagging primary itself boots the
// fix (or otherwise starts sending a real SKU for a variant this replica
// already backfilled), that real value must win, exactly like every other
// synced field ("primary always wins").
func TestApplyAdmin_RealSKUStillOverwritesBackfilledOne(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary-codeless-catchup.db")
	replica := openMigratedDB(t, "replica-codeless-catchup.db")

	mustExec(t, primary, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm1', 'ITEM-1', 'Coffee', 300)`)
	mustExec(t, primary, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-codeless', 'itm1', NULL, 'Small', 250, 1)`)

	repo := NewSyncAdminRepo(primary.DB)
	bundle, err := repo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var backfilled *string
	if err := replica.DB.QueryRow(`SELECT sku FROM item_variants WHERE id = 'v-codeless'`).Scan(&backfilled); err != nil {
		t.Fatalf("synced variant missing: %v", err)
	}
	if backfilled == nil || !generatedSyncSKUPattern.MatchString(*backfilled) {
		t.Fatalf("v-codeless sku on replica after first apply = %v, want a generated SKU", backfilled)
	}

	// The primary now catches up (boots ut-docs#1900's fix, or the sku is
	// otherwise assigned for real) and sends a real, non-blank sku.
	mustExec(t, primary, `UPDATE item_variants SET sku = 'REAL-001' WHERE id = 'v-codeless'`)
	bundle2, err := repo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump 2: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle2)); err != nil {
		t.Fatalf("apply 2: %v", err)
	}

	var final string
	if err := replica.DB.QueryRow(`SELECT sku FROM item_variants WHERE id = 'v-codeless'`).Scan(&final); err != nil {
		t.Fatalf("synced variant missing: %v", err)
	}
	if final != "REAL-001" {
		t.Fatalf("v-codeless sku after primary caught up = %q, want REAL-001 (primary must still win over a stale backfilled value)", final)
	}
}

// TestApplyAdmin_BackfillsRevivedRetireMangledVariant covers ut-docs#2273:
// deleteMissing's FK-blocked retire-in-place mangles item_variants.sku to
// "<sku>~<id>" (a non-blank value) on retire. ut-docs#2246's
// stickyNonBlankCols then treats that mangled value as "real" forever — if
// the same variant ID is later revived by a bundle from a primary still
// behind on ut-docs#1900/#2230 (so it sends a blank sku), the sticky
// COALESCE freezes the mangled garbage in place instead of the pre-#2246
// behaviour of overwriting to NULL and letting backfillCodelessSyncedVariants
// generate a fresh, real-looking SKU. A revived variant must never surface a
// "~<id>"-suffixed sku to staff.
func TestApplyAdmin_BackfillsRevivedRetireMangledVariant(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary-revived-mangled.db")
	replica := openMigratedDB(t, "replica-revived-mangled.db")

	mustExec(t, primary, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm1', 'ITEM-1', 'Coffee', 300)`)
	// The primary is still behind on ut-docs#1900/#2230, so the revived row
	// travels with a blank sku — the version-skewed-primary shape the card
	// describes.
	mustExec(t, primary, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v1', 'itm1', NULL, 'Small', 250, 1)`)

	// The replica already has this row locally, in the state a prior
	// FK-blocked retire-in-place (deleteMissing) would have left it:
	// inactive, sku mangled to "<sku>~<id>" — the exact suffix the generic
	// retire-in-place CASE in sync_admin_repo.go produces.
	mustExec(t, replica, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm1', 'ITEM-1', 'Coffee', 300)`)
	mustExec(t, replica, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v1', 'itm1', 'REAL-001~v1', 'Small', 250, 0)`)

	repo := NewSyncAdminRepo(primary.DB)
	bundle, err := repo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}

	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var sku *string
	var isActive int
	if err := replica.DB.QueryRow(`SELECT sku, is_active FROM item_variants WHERE id = 'v1'`).Scan(&sku, &isActive); err != nil {
		t.Fatalf("revived variant missing: %v", err)
	}
	if isActive != 1 {
		t.Fatalf("v1 is_active on replica = %d, want 1 (revived by the primary)", isActive)
	}
	if sku == nil || !generatedSyncSKUPattern.MatchString(*sku) {
		got := "<nil>"
		if sku != nil {
			got = *sku
		}
		t.Fatalf("v1 sku on replica after revival = %q, want to match %s (a revived variant must never keep a retire-mangled sku frozen forever)", got, generatedSyncSKUPattern.String())
	}
}
