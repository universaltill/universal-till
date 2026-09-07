package catalog

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// TestCatalogItemMutations_RefusedOnReplica guards ut-docs#1689: items,
// item_variants, item_barcodes and variant_barcodes have all been synced
// shop-wide via sync_admin_repo.go's adminTables for a long time — but,
// unlike the item_modifier_groups/item_modifier_options gate ut-docs#1667
// added (see TestCatalogModifiersPanel_MutationsRefusedOnReplica, this
// package's sibling test), their own mutation handlers never got a
// requirePrimary check. Without it, a manager editing an item/variant/
// barcode directly on a satellite gets no refusal — the edit is accepted,
// then silently reverted on the very next admin pull. One shared DB/mux
// fixture; each route gets exercised against a replica, confirming both the
// HTTP response and that nothing actually changed in the DB.
//
// Assertions use t.Errorf, not t.Fatalf (review finding, ut-docs#1689): a
// regression in one route must not mask the other nine — a single run
// should report every broken route, not just the first one alphabetically.
func TestCatalogItemMutations_RefusedOnReplica(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "var1", ItemID: "itm1", SKU: "COFFEE-L", Name: "Large", Price: 380, IsActive: true})
	testsupport.SeedBarcode(t, db, "5000000000000", "itm1", true)

	// Same settings-table + sync.primary_url convention as
	// TestCatalogModifiersPanel_MutationsRefusedOnReplica: NewCatalogTestDB's
	// schema omits "settings" for handler groups that don't otherwise need
	// it, so this replica-detection test creates it itself.
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("create settings table: %v", err)
	}
	st := settings.NewStore(db)
	if err := st.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("set primary_url: %v", err)
	}

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}, Settings: st})

	// The item/variant/barcode routes deliberately use their OWN message key
	// (catalog.error.item_replica_use_primary) rather than reusing
	// catalog.error.replica_use_primary's "manage customization options"
	// wording, which doesn't fit these routes — see handlers.go's
	// requirePrimary comment.
	const wantMsg = "manage the catalog on the primary till"

	post := func(t *testing.T, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	assertRefused := func(t *testing.T, label string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusConflict {
			t.Errorf("%s on replica: want 409, got %d: %s", label, rec.Code, rec.Body.String())
			return
		}
		if !strings.Contains(rec.Body.String(), wantMsg) {
			t.Errorf("%s on replica: body missing the localized item_replica_use_primary message, got %q", label, rec.Body.String())
		}
	}

	// items: create
	assertRefused(t, "create item", post(t, "/api/catalog/item", "name=New+Item&price=100"))
	var itemCount int
	if err := db.QueryRow(`SELECT count(*) FROM items`).Scan(&itemCount); err != nil || itemCount != 1 {
		t.Errorf("item must not be created on a replica: count=%d err=%v", itemCount, err)
	}

	// items: update
	assertRefused(t, "update item", post(t, "/api/catalog/item/update", "id=itm1&name=Renamed&price=999"))
	var itemName string
	if err := db.QueryRow(`SELECT name FROM items WHERE id = 'itm1'`).Scan(&itemName); err != nil || itemName != "Flat White" {
		t.Errorf("item must not be renamed on a replica: name=%q err=%v", itemName, err)
	}

	// items: deactivate
	assertRefused(t, "deactivate item", post(t, "/api/catalog/item/deactivate", "id=itm1"))
	var itemActive int
	if err := db.QueryRow(`SELECT is_active FROM items WHERE id = 'itm1'`).Scan(&itemActive); err != nil || itemActive != 1 {
		t.Errorf("item must not be deactivated on a replica: is_active=%d err=%v", itemActive, err)
	}

	// items: cost price
	assertRefused(t, "set item cost", post(t, "/api/catalog/item-cost", "panelItem=itm1&cost=1.50"))
	var itemCost sql.NullInt64
	if err := db.QueryRow(`SELECT cost_price FROM items WHERE id = 'itm1'`).Scan(&itemCost); err != nil {
		t.Errorf("read item cost: %v", err)
	} else if itemCost.Valid {
		t.Errorf("item cost must not be set on a replica, got %v", itemCost.Int64)
	}

	// items: lead time
	assertRefused(t, "set item lead time", post(t, "/api/catalog/item-lead-time", "panelItem=itm1&leadTimeDays=5"))
	var leadTime int
	if err := db.QueryRow(`SELECT lead_time_days FROM items WHERE id = 'itm1'`).Scan(&leadTime); err != nil || leadTime != 0 {
		t.Errorf("item lead time must not be set on a replica: got %d err=%v", leadTime, err)
	}

	// item_variants: create
	assertRefused(t, "create variant", post(t, "/api/catalog/variant", "itemId=itm1&name=Small&price=250"))
	var variantCount int
	if err := db.QueryRow(`SELECT count(*) FROM item_variants`).Scan(&variantCount); err != nil || variantCount != 1 {
		t.Errorf("variant must not be created on a replica: count=%d err=%v", variantCount, err)
	}

	// item_variants: update
	assertRefused(t, "update variant", post(t, "/api/catalog/variant", "itemId=itm1&id=var1&name=Renamed&price=999"))
	var variantName string
	if err := db.QueryRow(`SELECT name FROM item_variants WHERE id = 'var1'`).Scan(&variantName); err != nil || variantName != "Large" {
		t.Errorf("variant must not be renamed on a replica: name=%q err=%v", variantName, err)
	}

	// item_variants: deactivate
	assertRefused(t, "deactivate variant", post(t, "/api/catalog/variant/deactivate", "id=var1"))
	var variantActive int
	if err := db.QueryRow(`SELECT is_active FROM item_variants WHERE id = 'var1'`).Scan(&variantActive); err != nil || variantActive != 1 {
		t.Errorf("variant must not be deactivated on a replica: is_active=%d err=%v", variantActive, err)
	}

	// item_barcodes: attach to an item
	assertRefused(t, "attach barcode to item", post(t, "/api/catalog/barcode", "itemId=itm1&barcode=5000000000099"))
	var itemBarcodeCount int
	if err := db.QueryRow(`SELECT count(*) FROM item_barcodes`).Scan(&itemBarcodeCount); err != nil || itemBarcodeCount != 1 {
		t.Errorf("barcode must not be attached to an item on a replica: count=%d err=%v", itemBarcodeCount, err)
	}

	// variant_barcodes: attach to a variant — named in the ticket title
	// alongside item_barcodes, and a real, separate write path (AddBarcode
	// routes to variant_barcodes, not item_barcodes, when variantId is set)
	// — covering it only via route identity (the item_barcodes case above)
	// would leave this table's own write path unverified.
	assertRefused(t, "attach barcode to variant", post(t, "/api/catalog/barcode", "variantId=var1&barcode=5000000000098"))
	var variantBarcodeCount int
	if err := db.QueryRow(`SELECT count(*) FROM variant_barcodes`).Scan(&variantBarcodeCount); err != nil || variantBarcodeCount != 0 {
		t.Errorf("barcode must not be attached to a variant on a replica: count=%d err=%v", variantBarcodeCount, err)
	}

	// item_barcodes: detach
	assertRefused(t, "detach barcode", post(t, "/api/catalog/barcode/delete", "barcode=5000000000000"))
	if err := db.QueryRow(`SELECT count(*) FROM item_barcodes WHERE barcode = '5000000000000'`).Scan(&itemBarcodeCount); err != nil || itemBarcodeCount != 1 {
		t.Errorf("barcode must not be detached on a replica: count=%d err=%v", itemBarcodeCount, err)
	}

	// item_barcodes/variant_barcodes: the bulk backfill commit is a write
	// path too (loops AddBarcode) — the GET preview beside it is a dry run
	// and deliberately NOT gated.
	assertRefused(t, "barcode backfill commit", post(t, "/api/catalog/barcode-backfill", ""))

	// The GET preview must keep working on a replica — it's a read-only dry
	// run (computeBarcodeBackfillPlan, no writes), so gating it too would be
	// over-broad and would block a manager from even seeing what a backfill
	// WOULD do before deciding whether it's worth asking the primary till to
	// run it.
	getReq := httptest.NewRequest(http.MethodGet, "/api/catalog/barcode-backfill", nil)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Errorf("GET barcode-backfill preview on replica: want 200 (dry run, never gated), got %d: %s", getRec.Code, getRec.Body.String())
	}
}
