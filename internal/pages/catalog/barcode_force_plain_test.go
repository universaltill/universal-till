package catalog

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// seedSettingsTable creates the generic key/value settings table
// (see internal/data's own copy for why testsupport doesn't own this).
func seedSettingsTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("create settings table: %v", err)
	}
}

// ean13c appends the GS1 mod-10 check digit to a 12-digit body (same
// algorithm as internal/barcode's own gs1CheckDigit, reimplemented locally
// since it's unexported).
func ean13c(t *testing.T, body string) string {
	t.Helper()
	if len(body) != 12 {
		t.Fatalf("ean13c body must be 12 digits, got %q", body)
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
	check := byte((10-sum%10)%10) + '0'
	return body + string(check)
}

// TestCatalogBarcodeForcePlain_EscapesEmbeddedInference is ut-docs#948 F1's
// acceptance criterion at the handler layer: /api/catalog/barcode's
// forcePlainBarcode checkbox makes AddBarcode take the explicit-type path
// (raw code stored, no registry inference) instead of the untyped path
// that would otherwise classify a 20-29-prefixed check-digit-valid code as
// embedded-data once the shop has that symbology enabled.
func TestCatalogBarcodeForcePlain_EscapesEmbeddedInference(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	seedSettingsTable(t, db)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})
	if err := data.NewSettingsRepo(db).SetEnabledBarcodeSymbologies(context.Background(),
		[]string{"EAN13", "EAN13_WEIGHT_PREFIX2X"}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	// Without the checkbox: untyped inference classifies this 23-prefixed,
	// check-digit-valid code as embedded-data, storing the zeroed key.
	inferred := ean13c(t, "231234501234")
	form := strings.NewReader("itemId=itm1&barcode=" + inferred)
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/barcode", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("inferred add: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var rawExists int
	_ = db.QueryRow(`SELECT COUNT(*) FROM item_barcodes WHERE barcode = ?`, inferred).Scan(&rawExists)
	if rawExists != 0 {
		t.Fatal("without forcePlainBarcode: expected the RAW code NOT stored (should be zeroed-key embedded-data)")
	}
	var zeroedType string
	if err := db.QueryRow(`SELECT barcode_type FROM item_barcodes WHERE barcode = '2312345000000'`).Scan(&zeroedType); err != nil {
		t.Fatalf("expected the zeroed embedded-data row: %v", err)
	}
	if zeroedType != "EAN13_WEIGHT_PREFIX2X" {
		t.Fatalf("barcode_type = %q, want EAN13_WEIGHT_PREFIX2X", zeroedType)
	}

	// With the checkbox: a DIFFERENT 24-prefixed, check-digit-valid code
	// (would also infer to embedded-data) is stored as typed, raw.
	escaped := ean13c(t, "241234500000")
	form2 := strings.NewReader("itemId=itm1&barcode=" + escaped + "&forcePlainBarcode=1")
	req2 := httptest.NewRequest(http.MethodPost, "/api/catalog/barcode", form2)
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("escaped add: want 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var escapedType string
	if err := db.QueryRow(`SELECT barcode_type FROM item_barcodes WHERE barcode = ?`, escaped).Scan(&escapedType); err != nil {
		t.Fatalf("expected the RAW code stored (forcePlainBarcode must escape inference): %v", err)
	}
	if escapedType != "EAN13" {
		t.Fatalf("barcode_type = %q, want EAN13 (explicit type, not inferred)", escapedType)
	}
	var zeroedRowForEscaped int
	_ = db.QueryRow(`SELECT COUNT(*) FROM item_barcodes WHERE barcode = '2412345000000'`).Scan(&zeroedRowForEscaped)
	if zeroedRowForEscaped != 0 {
		t.Fatal("forcePlainBarcode: the zeroed embedded-data key must never have a row")
	}
}

// TestCatalogItemCreateForcePlain_EscapesEmbeddedInference is the same
// escape hatch on /api/catalog/item's auto-fill-on-create flow.
func TestCatalogItemCreateForcePlain_EscapesEmbeddedInference(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	seedSettingsTable(t, db)
	if err := data.NewSettingsRepo(db).SetEnabledBarcodeSymbologies(context.Background(),
		[]string{"EAN13", "EAN13_PRICE_PREFIX02"}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	// A 02-prefixed, check-digit-valid code that would otherwise infer to
	// the enabled price-embedded symbology.
	escaped := ean13c(t, "025432100000")
	form := strings.NewReader("name=Plain Item&price=150&isActive=1&barcode=" + escaped + "&forcePlainBarcode=1")
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/item", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create with forcePlainBarcode: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var storedType string
	if err := db.QueryRow(`SELECT barcode_type FROM item_barcodes WHERE barcode = ?`, escaped).Scan(&storedType); err != nil {
		t.Fatalf("expected the raw code stored: %v", err)
	}
	if storedType != "EAN13" {
		t.Fatalf("barcode_type = %q, want EAN13", storedType)
	}
}

// TestCatalogBarcodeForcePlain_NonEAN13CodesStillAccepted is the ut-docs#948
// F-2 review regression: ticking "plain code" must NOT reject a perfectly
// valid non-EAN-13 barcode. Forcing BarcodeType:"EAN13" unconditionally
// would make AddBarcode assert an EAN-13 check digit and 400 an EAN-8 /
// UPC-A / internal-PLU code. plainBarcodeTypeFor only forces the type for a
// genuine EAN-13, so these fall through to untyped inference and attach.
func TestCatalogBarcodeForcePlain_NonEAN13CodesStillAccepted(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	seedSettingsTable(t, db)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})
	// Default enabled set (no explicit SetEnabledBarcodeSymbologies) — the
	// permissive catch-alls accept any shape, same as a real fresh shop.

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	cases := []struct {
		name     string
		code     string
		wantType string
	}{
		{"EAN-8", "96385074", "EAN8"},             // valid EAN-8 check digit
		{"UPC-A", "036000291452", "UPCA"},         // valid 12-digit UPC-A check digit
		{"internal-PLU", "PLU-BANANA", "CODE128"}, // non-numeric → not EAN-13, catch-all
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form := strings.NewReader("itemId=itm1&barcode=" + tc.code + "&forcePlainBarcode=on")
			req := httptest.NewRequest(http.MethodPost, "/api/catalog/barcode", form)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s with forcePlainBarcode: want 200, got %d: %s", tc.name, rec.Code, rec.Body.String())
			}
			var storedType string
			if err := db.QueryRow(`SELECT barcode_type FROM item_barcodes WHERE barcode = ?`, tc.code).Scan(&storedType); err != nil {
				t.Fatalf("%s: expected the code stored, got: %v", tc.name, err)
			}
			if storedType != tc.wantType {
				t.Fatalf("%s: barcode_type = %q, want %q (untyped inference, not forced EAN13)", tc.name, storedType, tc.wantType)
			}
		})
	}
}
