package data_test

import (
	"context"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#4259: a new shop starts with one gift voucher item, and no delete
// path removes a gift voucher item while the shop still has open vouchers.
// A voucher sold through the item is never a sale line (#4214), so the
// "never sold" rule alone would call that item unused.

func TestSeedDefaultGiftVoucherItem_CreatesOneUntrackedVoucherItem(t *testing.T) {
	d, _, _ := resetTestDB(t, "seed-gift-voucher.db")
	repo := data.NewCatalogRepo(d.DB)
	ctx := context.Background()

	created, err := repo.SeedDefaultGiftVoucherItem(ctx, "Gutschein")
	if err != nil || !created {
		t.Fatalf("seed: created=%v err=%v", created, err)
	}
	var (
		name, itemType, sku   string
		price                 int64
		active, untracked, sd int
		categoryNull          bool
	)
	if err := d.DB.QueryRow(`SELECT name, item_type, sku, base_price, is_active, stock_untracked, is_sample_data, category_id IS NULL FROM items`).
		Scan(&name, &itemType, &sku, &price, &active, &untracked, &sd, &categoryNull); err != nil {
		t.Fatalf("load seeded item: %v", err)
	}
	if name != "Gutschein" || itemType != catalogtypes.ItemTypeGiftVoucher || price != 0 ||
		active != 1 || untracked != 1 || sd != 0 || !categoryNull || sku == "" {
		t.Fatalf("seeded item = %q %q sku=%q price=%d active=%d untracked=%d sample=%d nocat=%v",
			name, itemType, sku, price, active, untracked, sd, categoryNull)
	}
	var inv int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM inventory`).Scan(&inv); err != nil || inv != 0 {
		t.Fatalf("inventory rows = %d (%v), want 0 for a stock-untracked item", inv, err)
	}

	// Idempotent: any gift voucher item already there means no second one.
	created, err = repo.SeedDefaultGiftVoucherItem(ctx, "Gift voucher")
	if err != nil || created {
		t.Fatalf("second seed: created=%v err=%v, want no-op", created, err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("items = %d (%v), want 1", n, err)
	}
}

func TestSeedDefaultGiftVoucherItem_SkipsWhenTheShopHasOne(t *testing.T) {
	d, x, _ := resetTestDB(t, "seed-gift-voucher-existing.db")
	x(`INSERT INTO items (id, sku, name, base_price, is_active, item_type) VALUES ('gv-old', 'GV-OLD', 'Gutscheine', 0, 0, 'gift_voucher')`)

	created, err := data.NewCatalogRepo(d.DB).SeedDefaultGiftVoucherItem(context.Background(), "Gutschein")
	if err != nil || created {
		t.Fatalf("seed: created=%v err=%v, want no-op (an inactive voucher item still counts)", created, err)
	}
}

func TestSeedDefaultGiftVoucherItem_RefusesBlankName(t *testing.T) {
	d, _, _ := resetTestDB(t, "seed-gift-voucher-blank.db")
	if _, err := data.NewCatalogRepo(d.DB).SeedDefaultGiftVoucherItem(context.Background(), "  "); err == nil {
		t.Fatal("blank name accepted")
	}
}

func seedGiftVoucherItem(x func(string, ...any), id string, active int) {
	x(`INSERT INTO items (id, sku, name, base_price, is_active, item_type, stock_untracked) VALUES (?, ?, 'Gift voucher', 0, ?, 'gift_voucher', 1)`, id, "SKU-"+id, active)
}

func seedVoucher(x func(string, ...any), code, status string, balance int64) {
	x(`INSERT INTO vouchers (id, original_amount, balance, status) VALUES (?, 2500, ?, ?)`, code, balance, status)
}

func TestDeleteUnusedItem_RefusesGiftVoucherItemWhileVouchersAreOpen(t *testing.T) {
	d, x, _ := resetTestDB(t, "delete-gift-voucher-item.db")
	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	seedGiftVoucherItem(x, "gv-1", 1)
	seedVoucher(x, "1000000000017", "active", 2500)

	_, err := repo.DeleteUnusedItem(ctx, "gv-1")
	var inUse *data.ItemInUseError
	if !errors.As(err, &inUse) || inUse.Reason != data.ItemUseOpenVouchers {
		t.Fatalf("err = %v, want ItemInUseError{%s}", err, data.ItemUseOpenVouchers)
	}
	if got, want := err.Error(), "the shop has open gift vouchers — deactivate it instead"; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
	if !itemExists(t, d.DB, "gv-1") {
		t.Fatal("a refused delete removed the item")
	}

	// A standard item is not held back by open vouchers.
	seedDeletableItem(x, "plain-1")
	if _, err := repo.DeleteUnusedItem(ctx, "plain-1"); err != nil {
		t.Fatalf("standard item with open vouchers in the shop: %v", err)
	}

	// Once every voucher is used up or void, the item can go.
	x(`UPDATE vouchers SET status = 'redeemed', balance = 0`)
	seedVoucher(x, "1000000000024", "void", 0)
	if _, err := repo.DeleteUnusedItem(ctx, "gv-1"); err != nil {
		t.Fatalf("no open vouchers left: %v", err)
	}
	if itemExists(t, d.DB, "gv-1") {
		t.Fatal("item still exists")
	}
}

func TestCleanupObsoleteItems_KeepsGiftVoucherItemWhileVouchersAreOpen(t *testing.T) {
	for _, includeActive := range []bool{false, true} {
		d, x, _ := resetTestDB(t, "cleanup-gift-voucher.db")
		repo := data.NewPOSRepo(d.DB)
		ctx := context.Background()
		seedGiftVoucherItem(x, "gv-off", 0)
		seedGiftVoucherItem(x, "gv-on", 1)
		x(`INSERT INTO items (id, name, base_price, is_active) VALUES ('obs', 'Old Test Product', 100, 0)`)
		seedVoucher(x, "1000000000017", "active", 2500)

		preview, err := repo.ListObsoleteItems(ctx, 100, includeActive)
		if err != nil {
			t.Fatalf("includeActive=%v preview: %v", includeActive, err)
		}
		for _, it := range preview {
			if it.ID == "gv-off" || it.ID == "gv-on" {
				t.Fatalf("includeActive=%v: gift voucher item %s previewed for removal with open vouchers", includeActive, it.ID)
			}
		}
		if _, err := repo.CleanupObsoleteItems(ctx, "", "", includeActive); err != nil {
			t.Fatalf("includeActive=%v cleanup: %v", includeActive, err)
		}
		if !itemExists(t, d.DB, "gv-off") || !itemExists(t, d.DB, "gv-on") {
			t.Fatalf("includeActive=%v: a gift voucher item was removed while vouchers are open", includeActive)
		}
		if itemExists(t, d.DB, "obs") {
			t.Fatalf("includeActive=%v: the obsolete standard item was not removed", includeActive)
		}

		// No open vouchers: the inactive voucher item is ordinary cleanup.
		x(`UPDATE vouchers SET status = 'void', balance = 0`)
		if _, err := repo.CleanupObsoleteItems(ctx, "", "", includeActive); err != nil {
			t.Fatalf("includeActive=%v second cleanup: %v", includeActive, err)
		}
		if itemExists(t, d.DB, "gv-off") {
			t.Fatalf("includeActive=%v: inactive voucher item kept with no open vouchers", includeActive)
		}
	}
}
