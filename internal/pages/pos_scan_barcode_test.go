package pages

import (
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/ui"
)

// ean13pg appends the GS1 mod-10 check digit to a 12-digit body.
func ean13pg(t *testing.T, body string) string {
	t.Helper()
	if len(body) != 12 {
		t.Fatalf("ean13pg body must be 12 digits, got %q", body)
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

// setupScanBarcodeDeps wires the REAL resolution chain — migrated DB,
// ui.PriceResolverAdapter, pos.Service — behind /api/pos/scan, mirroring
// setupSelfOrderShopDeps: these are integration tests for ADR-0059's
// scan-path wiring (ut-docs#934), so a stub resolver would prove nothing.
func setupScanBarcodeDeps(t *testing.T) (*http.ServeMux, *common.Deps, *db.DB) {
	t.Helper()
	chdirRoot(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "scan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	resolver := ui.PriceResolverAdapter{Store: ui.NewButtonStore(d.DB)}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)

	dp := &common.Deps{
		Cfg:      &config.Config{Theme: "default", StoreName: "Scale Test Shop"},
		Db:       d.DB,
		State:    common.RuntimeState{Currency: "EUR", TaxRatePct: 20},
		Settings: settings.NewStore(d.DB),
		Engine:   engine,
	}
	t.Cleanup(dp.WaitForAsyncWork)

	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	return mux, dp, d
}

func postScanCode(t *testing.T, mux *http.ServeMux, code string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"code": {code}, "qty": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/scan", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestScanAPI_WeightEmbeddedLabel is acceptance criterion 1, end to end:
// with EAN13 and EAN13_WEIGHT_PREFIX2X both enabled, scanning a scale label
// through /api/pos/scan resolves the right item AND sets the basket line's
// quantity from the embedded digits — and must NOT match plain EAN13 first
// (a decoy item owns the FULL label code as a plain barcode; if plain EAN13
// were tried first its LookupKey — the full code — would resolve the decoy).
func TestScanAPI_WeightEmbeddedLabel(t *testing.T) {
	mux, dp, d := setupScanBarcodeDeps(t)
	ctx := t.Context()

	if err := data.NewSettingsRepo(d.DB).SetEnabledBarcodeSymbologies(ctx,
		[]string{"EAN13", "EAN13_WEIGHT_PREFIX2X"}); err != nil {
		t.Fatal(err)
	}

	// The scale item: €5.99/kg, catalog row is the zeroed template.
	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed) VALUES ('itm-cheese','CHEESE','Bergkäse',599,1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO item_barcodes (item_id, barcode, is_primary) VALUES ('itm-cheese','2312345000000',1)`); err != nil {
		t.Fatal(err)
	}

	label := ean13pg(t, "231234501234") // 1.234 kg
	// Decoy: another item owning the FULL label code as a plain barcode —
	// this is what would (wrongly) resolve if plain EAN13 matched first.
	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-decoy','DECOY','Decoy',100,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO item_barcodes (item_id, barcode, is_primary) VALUES ('itm-decoy',?,1)`, label); err != nil {
		t.Fatal(err)
	}

	if rec := postScanCode(t, mux, label); rec.Code != http.StatusOK {
		t.Fatalf("scan: want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	b := dp.Engine.Basket()
	if len(b.Lines) != 1 {
		t.Fatalf("expected 1 basket line, got %+v", b.Lines)
	}
	line := b.Lines[0]
	if line.ItemID != "itm-cheese" {
		t.Fatalf("resolved item %q — the WEIGHT symbology must win over plain EAN13's full-code match (decoy)", line.ItemID)
	}
	if math.Abs(line.Qty-1.234) > 1e-9 {
		t.Fatalf("qty = %v, want 1.234 decoded from the label digits", line.Qty)
	}
	if line.PriceCents.Minor() != 599 {
		t.Fatalf("per-unit rate = %d, want 599", line.PriceCents.Minor())
	}
	// 599 * 1.234 = 739.166 -> the weighed-item rounding path.
	want := pos.AmountForQuantity(line.PriceCents, 1.234)
	if line.LineTotal != want {
		t.Fatalf("line total = %d, want %d", line.LineTotal.Minor(), want.Minor())
	}
}

// TestScanAPI_TwoPriceLabelsStayTwoLines is acceptance criterion 2, end to
// end: two different price-embedded labels of the SAME item produce TWO
// separate basket lines with the labels' individual prices — never one
// merged line with a wrong total.
func TestScanAPI_TwoPriceLabelsStayTwoLines(t *testing.T) {
	mux, dp, d := setupScanBarcodeDeps(t)
	ctx := t.Context()

	if err := data.NewSettingsRepo(d.DB).SetEnabledBarcodeSymbologies(ctx,
		[]string{"EAN13", "EAN13_PRICE_PREFIX02"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-ham','HAM','Schinken',100,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO item_barcodes (item_id, barcode, is_primary) VALUES ('itm-ham','0254321000000',1)`); err != nil {
		t.Fatal(err)
	}

	labelA := ean13pg(t, "025432100350") // €3.50
	labelB := ean13pg(t, "025432100720") // €7.20
	if rec := postScanCode(t, mux, labelA); rec.Code != http.StatusOK {
		t.Fatalf("scan A: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postScanCode(t, mux, labelB); rec.Code != http.StatusOK {
		t.Fatalf("scan B: want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	b := dp.Engine.Basket()
	if len(b.Lines) != 2 {
		t.Fatalf("expected TWO separate lines, got %d: %+v", len(b.Lines), b.Lines)
	}
	if b.Lines[0].PriceCents.Minor() != 350 || b.Lines[0].Qty != 1 {
		t.Fatalf("first label line = %+v, want qty 1 at 350", b.Lines[0])
	}
	if b.Lines[1].PriceCents.Minor() != 720 || b.Lines[1].Qty != 1 {
		t.Fatalf("second label line = %+v, want qty 1 at 720", b.Lines[1])
	}
	if b.Subtotal.Minor() != 1070 {
		t.Fatalf("subtotal = %d, want 1070 (350+720) — a merged 2×720=1440 is the exact money bug ADR-0059 §3 forbids", b.Subtotal.Minor())
	}

	// A duplicate scan of the same label stays visible as its own line.
	if rec := postScanCode(t, mux, labelA); rec.Code != http.StatusOK {
		t.Fatalf("scan A again: want 200, got %d", rec.Code)
	}
	b = dp.Engine.Basket()
	if len(b.Lines) != 3 || b.Subtotal.Minor() != 1420 {
		t.Fatalf("expected 3 lines / subtotal 1420 after duplicate scan, got %d lines / %d", len(b.Lines), b.Subtotal.Minor())
	}
}

// TestScanAPI_UnmatchedCodeShowsItemNotFoundToast: once the catch-alls are
// disabled, a code no enabled symbology admits renders the existing
// localized not-found toast (the reuse the dev brief prescribes — no new
// locale keys), never a silent miss or a raw error.
func TestScanAPI_UnmatchedCodeShowsItemNotFoundToast(t *testing.T) {
	mux, dp, d := setupScanBarcodeDeps(t)
	ctx := t.Context()

	if err := data.NewSettingsRepo(d.DB).SetEnabledBarcodeSymbologies(ctx, []string{"EAN13"}); err != nil {
		t.Fatal(err)
	}
	// The row exists, but no enabled symbology admits the (bad check digit)
	// scan once CODE128/INTERNAL_PLU are off.
	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-x','X1','Mystery',100,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO item_barcodes (item_id, barcode, is_primary) VALUES ('itm-x','5449000000995',1)`); err != nil {
		t.Fatal(err)
	}

	rec := postScanCode(t, mux, "5449000000995")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 with a toast, got %d: %s", rec.Code, rec.Body.String())
	}
	if want := httpx.T("en", "pos.toast.item_not_found"); !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("expected the %q toast in the response, got: %s", want, rec.Body.String())
	}
	if b := dp.Engine.Basket(); len(b.Lines) != 0 {
		t.Fatalf("no line may be added for an unmatched code, got %+v", b.Lines)
	}
}
