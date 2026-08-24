package ui

import (
	"database/sql"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ean13ui appends the GS1 mod-10 check digit to a 12-digit body — test-local
// helper for building valid scale labels.
func ean13ui(t *testing.T, body string) string {
	t.Helper()
	if len(body) != 12 {
		t.Fatalf("ean13ui body must be 12 digits, got %q", body)
	}
	sum := 0
	weight := 3
	for i := len(body) - 1; i >= 0; i-- {
		sum += int(body[i]-'0') * weight
		if weight == 3 {
			weight = 1
		} else {
			weight = 3
		}
	}
	return body + string(byte((10-sum%10)%10)+'0')
}

// seedEmbeddedFixture builds a catalog with a weighed scale item and a
// price-labelled item, each keyed by its ZEROED template row (the
// convention AddBarcode stores per ADR-0059 §3), plus the settings table
// the enabled-symbology set lives in.
func seedEmbeddedFixture(t *testing.T) (*ButtonStore, *sql.DB) {
	t.Helper()
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })
	mustExec(t, db, `CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`)

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, is_weighed) VALUES('itm-ban','BAN','Bananas', 200, 1, 1)`)
	mustExec(t, db, `INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES('2312345000000','itm-ban',1)`)

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, is_weighed) VALUES('itm-ham','HAM','Ham', 100, 1, 0)`)
	mustExec(t, db, `INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES('0254321000000','itm-ham',1)`)

	return NewButtonStore(db), db
}

func setEnabledSymbologies(t *testing.T, db *sql.DB, ids []string) {
	t.Helper()
	if err := data.NewSettingsRepo(db).SetEnabledBarcodeSymbologies(t.Context(), ids); err != nil {
		t.Fatal(err)
	}
}

// TestResolve_WeightEmbeddedLabel: the adapter must decode a scale label
// into Qty (kilograms) with QtyFromCode set, leaving PriceCents the item's
// per-unit rate — the existing weighed-item math prices the line.
func TestResolve_WeightEmbeddedLabel(t *testing.T) {
	store, db := seedEmbeddedFixture(t)
	setEnabledSymbologies(t, db, []string{"EAN13", "EAN13_WEIGHT_PREFIX2X"})
	r := PriceResolverAdapter{Store: store}

	label := ean13ui(t, "231234501234") // 1.234 kg
	line, ok := r.Resolve(label)
	if !ok {
		t.Fatal("scale label did not resolve via the zeroed template")
	}
	if line.ItemID != "itm-ban" {
		t.Fatalf("resolved %+v, want itm-ban", line)
	}
	if line.Qty != 1.234 || !line.QtyFromCode {
		t.Fatalf("qty = %v (fromCode=%v), want 1.234 decoded from the label", line.Qty, line.QtyFromCode)
	}
	if line.PriceCents.Minor() != 200 {
		t.Fatalf("per-unit rate = %d, want 200 (untouched)", line.PriceCents.Minor())
	}
	if line.NoMerge {
		t.Fatal("weight-embedded lines merge by summing Qty — NoMerge must be false")
	}
	if !line.IsWeighed {
		t.Fatal("IsWeighed must carry through")
	}
}

// TestResolve_PriceEmbeddedLabel: the adapter must set the label's absolute
// price with Qty fixed at 1 and NoMerge set (ADR-0059 §3's no-merge rule).
func TestResolve_PriceEmbeddedLabel(t *testing.T) {
	store, db := seedEmbeddedFixture(t)
	setEnabledSymbologies(t, db, []string{"EAN13", "EAN13_PRICE_PREFIX02"})
	r := PriceResolverAdapter{Store: store}

	label := ean13ui(t, "025432100350") // €3.50
	line, ok := r.Resolve(label)
	if !ok {
		t.Fatal("price label did not resolve via the zeroed template")
	}
	if line.ItemID != "itm-ham" {
		t.Fatalf("resolved %+v, want itm-ham", line)
	}
	if line.PriceCents.Minor() != 350 {
		t.Fatalf("price = %d, want the label's 350, not the item's base price", line.PriceCents.Minor())
	}
	if line.Qty != 1 || !line.QtyFromCode {
		t.Fatalf("qty = %v (fromCode=%v), want fixed 1", line.Qty, line.QtyFromCode)
	}
	if !line.NoMerge {
		t.Fatal("price-embedded lines must be NoMerge")
	}
}

// TestResolve_EmbeddedDisabledByDefault: with no settings row the default
// set applies — the two embedded symbologies are OFF, so a scale label is
// treated as a plain EAN-13 (its full code isn't in the catalog) and does
// not resolve. Opt-in only, per ADR-0059 §2.
func TestResolve_EmbeddedDisabledByDefault(t *testing.T) {
	store, _ := seedEmbeddedFixture(t)
	r := PriceResolverAdapter{Store: store}

	if line, ok := r.Resolve(ean13ui(t, "231234501234")); ok {
		t.Fatalf("embedded decode must be opt-in; resolved %+v under the default set", line)
	}
}
