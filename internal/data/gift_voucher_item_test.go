package data_test

import (
	"context"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
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

// ut-docs#4399: an import that brings its own gift voucher item retires the
// one setup seeded, but only while nothing has used it.

func seedMarkedGiftVoucherItem(t *testing.T, repo *data.CatalogRepo) string {
	t.Helper()
	ctx := context.Background()
	if created, err := repo.SeedDefaultGiftVoucherItem(ctx, "Gutschein"); err != nil || !created {
		t.Fatalf("seed: created=%v err=%v", created, err)
	}
	id, ok, err := repo.SeededGiftVoucherItemID(ctx)
	if err != nil || !ok || id == "" {
		t.Fatalf("marker after seed: id=%q ok=%v err=%v", id, ok, err)
	}
	return id
}

func TestSeedDefaultGiftVoucherItem_RecordsTheSeededItem(t *testing.T) {
	d, _, _ := resetTestDB(t, "seed-gift-voucher-marker.db")
	repo := data.NewCatalogRepo(d.DB)
	id := seedMarkedGiftVoucherItem(t, repo)
	var itemType string
	if err := d.DB.QueryRow(`SELECT item_type FROM items WHERE id = ?`, id).Scan(&itemType); err != nil || itemType != catalogtypes.ItemTypeGiftVoucher {
		t.Fatalf("marker %q points at item_type=%q (%v)", id, itemType, err)
	}
}

func TestRetireSeededGiftVoucherItem_RemovesAnUnusedSeededItem(t *testing.T) {
	d, x, _ := resetTestDB(t, "retire-seeded-voucher.db")
	ctx := context.Background()
	id := seedMarkedGiftVoucherItem(t, data.NewCatalogRepo(d.DB))
	seedGiftVoucherItem(x, "imported-gv", 1)

	res, err := data.NewPOSRepo(d.DB).RetireSeededGiftVoucherItem(ctx)
	if err != nil || !res.Removed || res.Name != "Gutschein" || res.ID != id {
		t.Fatalf("retire = %+v, %v; want removed Gutschein", res, err)
	}
	if itemExists(t, d.DB, id) {
		t.Fatal("seeded item still exists")
	}
	if !itemExists(t, d.DB, "imported-gv") {
		t.Fatal("the imported voucher item was removed")
	}
	if _, ok, _ := data.NewCatalogRepo(d.DB).SeededGiftVoucherItemID(ctx); ok {
		t.Fatal("marker not cleared after removal")
	}
}

func TestRetireSeededGiftVoucherItem_KeepsItWithNoOtherVoucherItem(t *testing.T) {
	d, x, _ := resetTestDB(t, "retire-seeded-voucher-alone.db")
	ctx := context.Background()
	id := seedMarkedGiftVoucherItem(t, data.NewCatalogRepo(d.DB))
	// An inactive voucher item is no replacement the sale screen can use.
	seedGiftVoucherItem(x, "inactive-gv", 0)

	res, err := data.NewPOSRepo(d.DB).RetireSeededGiftVoucherItem(ctx)
	if err != nil || res.Removed {
		t.Fatalf("retire = %+v, %v; want nothing removed", res, err)
	}
	if !itemExists(t, d.DB, id) {
		t.Fatal("the shop's only usable voucher item was removed")
	}
	if _, ok, _ := data.NewCatalogRepo(d.DB).SeededGiftVoucherItemID(ctx); !ok {
		t.Fatal("marker cleared although nothing was decided")
	}
}

func TestRetireSeededGiftVoucherItem_KeepsAUsedSeededItemAndForgetsIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		use  func(x func(string, ...any), id string)
	}{
		{"open vouchers", func(x func(string, ...any), _ string) { seedVoucher(x, "1000000000017", "active", 2500) }},
		// Edited by the shop (review finding, ut-docs#4399): its own now.
		{"renamed", func(x func(string, ...any), id string) {
			x(`UPDATE items SET name = 'Gift card 25' WHERE id = ?`, id)
		}},
		{"priced", func(x func(string, ...any), id string) {
			x(`UPDATE items SET base_price = 2500 WHERE id = ?`, id)
		}},
		{"quick button", func(x func(string, ...any), id string) {
			x(`INSERT INTO shortcut_buttons (barcode, label, item_id) VALUES ('sb-1', 'Gutschein', ?)`, id)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, x, _ := resetTestDB(t, "retire-seeded-voucher-used.db")
			ctx := context.Background()
			id := seedMarkedGiftVoucherItem(t, data.NewCatalogRepo(d.DB))
			seedGiftVoucherItem(x, "imported-gv", 1)
			tc.use(x, id)

			res, err := data.NewPOSRepo(d.DB).RetireSeededGiftVoucherItem(ctx)
			if err != nil || res.Removed {
				t.Fatalf("retire = %+v, %v; want kept", res, err)
			}
			if !itemExists(t, d.DB, id) {
				t.Fatal("a seeded item in use was removed")
			}
			// Decided: the shop uses it, so no later import may remove it.
			if _, ok, _ := data.NewCatalogRepo(d.DB).SeededGiftVoucherItemID(ctx); ok {
				t.Fatal("marker kept for an item the shop uses")
			}
		})
	}
}

func TestRetireSeededGiftVoucherItem_NoMarkerOrGoneIsANoOp(t *testing.T) {
	d, x, _ := resetTestDB(t, "retire-seeded-voucher-nomarker.db")
	ctx := context.Background()
	pos := data.NewPOSRepo(d.DB)
	// A shop with no marker (set up before ut-docs#4399, or its voucher
	// item came from an import) never loses a voucher item here.
	seedGiftVoucherItem(x, "old-gv", 1)
	seedGiftVoucherItem(x, "imported-gv", 1)
	if res, err := pos.RetireSeededGiftVoucherItem(ctx); err != nil || res.Removed {
		t.Fatalf("no marker: %+v, %v", res, err)
	}
	if !itemExists(t, d.DB, "old-gv") || !itemExists(t, d.DB, "imported-gv") {
		t.Fatal("an unmarked voucher item was removed")
	}

	// The marked item was deleted by hand meanwhile: forget the marker.
	x(`INSERT INTO settings (key, value) VALUES (?, 'gone-gv')`, data.SeededGiftVoucherItemKey)
	if res, err := pos.RetireSeededGiftVoucherItem(ctx); err != nil || res.Removed {
		t.Fatalf("marker to a missing item: %+v, %v", res, err)
	}
	if _, ok, _ := data.NewCatalogRepo(d.DB).SeededGiftVoucherItemID(ctx); ok {
		t.Fatal("marker to a missing item not cleared")
	}
}

// ut-docs#4252: a top-up from the voucher page needs a gift voucher item as
// its basket line's base — the first ACTIVE one; none (or only inactive
// ones) is a clear "no item" answer, not an error.
func TestActiveGiftVoucherItemID(t *testing.T) {
	d, x, _ := resetTestDB(t, "active-gift-voucher-item.db")
	repo := data.NewCatalogRepo(d.DB)
	ctx := context.Background()
	if id, ok, err := repo.ActiveGiftVoucherItemID(ctx); err != nil || ok || id != "" {
		t.Fatalf("empty catalog: id=%q ok=%v err=%v, want none", id, ok, err)
	}
	seedGiftVoucherItem(x, "gv-off", 0)
	x(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('plain', 'P1', 'Bread', 100, 1)`)
	if _, ok, err := repo.ActiveGiftVoucherItemID(ctx); err != nil || ok {
		t.Fatalf("only an inactive voucher item: ok=%v err=%v, want none", ok, err)
	}
	seedGiftVoucherItem(x, "gv-on", 1)
	if id, ok, err := repo.ActiveGiftVoucherItemID(ctx); err != nil || !ok || id != "gv-on" {
		t.Fatalf("id=%q ok=%v err=%v, want gv-on", id, ok, err)
	}
}

// ut-docs#4398: a shop set up before its language pack arrived got an item
// named in English; once the pack is live, the untouched seeded item takes
// the shop's word for it.

func seededVoucherName(t *testing.T, d *db.DB, id string) string {
	t.Helper()
	var name string
	if err := d.DB.QueryRow(`SELECT name FROM items WHERE id = ?`, id).Scan(&name); err != nil {
		t.Fatalf("load item %s: %v", id, err)
	}
	return name
}

func seededMarkerName(t *testing.T, d *db.DB) string {
	t.Helper()
	var name string
	if err := d.DB.QueryRow(`SELECT value FROM settings WHERE key = 'catalog.seeded_gift_voucher_item_name'`).Scan(&name); err != nil {
		t.Fatalf("load seeded-name marker: %v", err)
	}
	return name
}

func seedEnglishVoucherItem(t *testing.T, repo *data.CatalogRepo) string {
	t.Helper()
	ctx := context.Background()
	if created, err := repo.SeedDefaultGiftVoucherItem(ctx, "Gift voucher"); err != nil || !created {
		t.Fatalf("seed: created=%v err=%v", created, err)
	}
	id, ok, err := repo.SeededGiftVoucherItemID(ctx)
	if err != nil || !ok {
		t.Fatalf("seeded id: ok=%v err=%v", ok, err)
	}
	return id
}

func TestRenameSeededGiftVoucherItem_RenamesTheUntouchedItem(t *testing.T) {
	d, _, _ := resetTestDB(t, "rename-seeded-voucher.db")
	repo := data.NewCatalogRepo(d.DB)
	id := seedEnglishVoucherItem(t, repo)

	renamed, err := repo.RenameSeededGiftVoucherItem(context.Background(), "Gift voucher", "Geschenkgutschein")
	if err != nil || !renamed {
		t.Fatalf("rename: renamed=%v err=%v", renamed, err)
	}
	if got := seededVoucherName(t, d, id); got != "Geschenkgutschein" {
		t.Fatalf("item name = %q, want Geschenkgutschein", got)
	}
	// The marker follows, so an import still recognises the item as the
	// untouched seed (RetireSeededGiftVoucherItem) and a second run is a no-op.
	if got := seededMarkerName(t, d); got != "Geschenkgutschein" {
		t.Fatalf("seeded-name marker = %q, want Geschenkgutschein", got)
	}
	renamed, err = repo.RenameSeededGiftVoucherItem(context.Background(), "Gift voucher", "Geschenkgutschein")
	if err != nil || renamed {
		t.Fatalf("second rename: renamed=%v err=%v, want no-op", renamed, err)
	}
}

func TestRenameSeededGiftVoucherItem_RetireStillSeesItAsUntouched(t *testing.T) {
	d, x, _ := resetTestDB(t, "rename-seeded-voucher-retire.db")
	repo := data.NewCatalogRepo(d.DB)
	id := seedEnglishVoucherItem(t, repo)
	if _, err := repo.RenameSeededGiftVoucherItem(context.Background(), "Gift voucher", "Geschenkgutschein"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	seedGiftVoucherItem(x, "gv-imported", 1)

	res, err := data.NewPOSRepo(d.DB).RetireSeededGiftVoucherItem(context.Background())
	if err != nil || !res.Removed || res.ID != id {
		t.Fatalf("retire after rename = %+v, %v; want the renamed seed removed", res, err)
	}
}

func TestRenameSeededGiftVoucherItem_LeavesTheShopsOwnChoicesAlone(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, d *db.DB, x func(string, ...any), id string)
	}{
		{"renamed by the owner", func(t *testing.T, d *db.DB, x func(string, ...any), id string) {
			x(`UPDATE items SET name = 'Gutschein' WHERE id = ?`, id)
		}},
		{"a second gift voucher item", func(t *testing.T, d *db.DB, x func(string, ...any), id string) {
			seedGiftVoucherItem(x, "gv-own", 0)
		}},
		{"marker cleared (the shop made it its own)", func(t *testing.T, d *db.DB, x func(string, ...any), id string) {
			if err := data.NewPOSRepo(d.DB).ForgetSeededGiftVoucherItem(context.Background()); err != nil {
				t.Fatal(err)
			}
		}},
		{"seeded in another language", func(t *testing.T, d *db.DB, x func(string, ...any), id string) {
			x(`UPDATE items SET name = 'Hediye çeki' WHERE id = ?`, id)
			x(`UPDATE settings SET value = 'Hediye çeki' WHERE key = 'catalog.seeded_gift_voucher_item_name'`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, x, _ := resetTestDB(t, "rename-seeded-voucher-keep.db")
			repo := data.NewCatalogRepo(d.DB)
			id := seedEnglishVoucherItem(t, repo)
			tc.setup(t, d, x, id)
			before := seededVoucherName(t, d, id)

			renamed, err := repo.RenameSeededGiftVoucherItem(context.Background(), "Gift voucher", "Geschenkgutschein")
			if err != nil || renamed {
				t.Fatalf("rename: renamed=%v err=%v, want no-op", renamed, err)
			}
			if got := seededVoucherName(t, d, id); got != before {
				t.Fatalf("item name = %q, want %q kept", got, before)
			}
		})
	}
}

func TestRenameSeededGiftVoucherItem_NoSeedIsANoOp(t *testing.T) {
	d, x, _ := resetTestDB(t, "rename-seeded-voucher-none.db")
	seedGiftVoucherItem(x, "gv-imported", 1)

	renamed, err := data.NewCatalogRepo(d.DB).RenameSeededGiftVoucherItem(context.Background(), "Gift voucher", "Geschenkgutschein")
	if err != nil || renamed {
		t.Fatalf("rename: renamed=%v err=%v, want no-op without the setup marker", renamed, err)
	}
	if got := seededVoucherName(t, d, "gv-imported"); got != "Gift voucher" {
		t.Fatalf("imported item renamed to %q", got)
	}
}

func TestRenameSeededGiftVoucherItem_RefusesBlankName(t *testing.T) {
	d, _, _ := resetTestDB(t, "rename-seeded-voucher-blank.db")
	repo := data.NewCatalogRepo(d.DB)
	seedEnglishVoucherItem(t, repo)
	if _, err := repo.RenameSeededGiftVoucherItem(context.Background(), "Gift voucher", " "); err == nil {
		t.Fatal("blank name accepted")
	}
}
