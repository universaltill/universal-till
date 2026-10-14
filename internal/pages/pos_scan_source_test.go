package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#4342: a hardware scanner or the camera delivers a barcode, never
// a search term. On the owner's tablet a Bluetooth scanner's dropped
// keystrokes sent "9" instead of the voucher 5000000000029, and the
// name-substring tier matched the "9" in "Pfand zurück 5,00 EUR (19.0%)"
// and rang it up. A device scan (src=scanner|camera) resolves items by exact
// code only; typed entry keeps the name search.

func postScanFrom(t *testing.T, mux *http.ServeMux, code, src string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"code": {code}, "qty": {"1"}}
	if src != "" {
		form.Set("src", src)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/scan", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scan %q (src=%q): want 200, got %d: %s", code, src, rec.Code, rec.Body.String())
	}
	return rec
}

func TestScanAPI_DeviceScanSkipsNameSearch(t *testing.T) {
	for _, src := range []string{"scanner", "camera"} {
		t.Run(src, func(t *testing.T) {
			mux, dp, d := setupScanBarcodeDeps(t)
			if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-pfand','PF','Pfand zurück 5,00 EUR (19.0%)',0,1)`); err != nil {
				t.Fatal(err)
			}
			postScanFrom(t, mux, "9", src)
			if n := len(dp.Engine.Basket().Lines); n != 0 {
				t.Fatalf("a %s read of \"9\" must not ring up an item by name, got %+v", src, dp.Engine.Basket().Lines)
			}
		})
	}
}

func TestScanAPI_TypedEntryKeepsNameSearch(t *testing.T) {
	mux, dp, d := setupScanBarcodeDeps(t)
	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-pfand','PF','Pfand zurück 5,00 EUR (19.0%)',0,1)`); err != nil {
		t.Fatal(err)
	}
	postScanFrom(t, mux, "Pfand", "")
	if b := dp.Engine.Basket(); len(b.Lines) != 1 || b.Lines[0].ItemID != "itm-pfand" {
		t.Fatalf("typed entry must still find an item by name, got %+v", b.Lines)
	}
}

func TestScanAPI_DeviceScanExactCodesStillWork(t *testing.T) {
	mux, dp, d := setupScanBarcodeDeps(t)
	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-cola','COLA9','Cola 0.33 (19.0%)',250,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO item_barcodes (item_id, barcode, is_primary) VALUES ('itm-cola','4000000000006',1)`); err != nil {
		t.Fatal(err)
	}
	postScanFrom(t, mux, "4000000000006", "scanner") // item barcode
	postScanFrom(t, mux, "COLA9", "camera")          // exact SKU
	if b := dp.Engine.Basket(); len(b.Lines) != 1 || b.Lines[0].Qty != 2 {
		t.Fatalf("barcode and SKU device scans must both ring up the item, got %+v", b.Lines)
	}
}

// The owner's exact case: an item whose NAME contains the voucher number's
// digits must not shadow a scanned voucher.
func TestScanAPI_DeviceScanVoucherNotShadowedByName(t *testing.T) {
	mux, dp, d := setupScanBarcodeDeps(t)
	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-x','X1','Gutschein 5000000000029 alt',0,1)`); err != nil {
		t.Fatal(err)
	}
	if err := data.NewPOSRepo(d.DB).CreateVoucher(t.Context(), nil, data.Voucher{
		ID: "5000000000029", OriginalAmountMinor: 500, BalanceMinor: 500, Currency: "EUR",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed voucher: %v", err)
	}
	postScanFrom(t, mux, "5000000000029", "scanner")
	b := dp.Engine.Basket()
	if len(b.Lines) != 0 || b.VoucherID != "5000000000029" {
		t.Fatalf("a scanned voucher number must be offered as payment, got lines=%+v voucher=%q", b.Lines, b.VoucherID)
	}
}
