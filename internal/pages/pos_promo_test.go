package pages

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	_ "modernc.org/sqlite"
)

type promoStubResolver map[string]pos.BasketLine

func (m promoStubResolver) Resolve(code string) (pos.BasketLine, bool) {
	v, ok := m[code]
	return v, ok
}

func TestPromoBarcodeSetsDiscount_FromDB(t *testing.T) {
	chdirRoot(t)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	_, _ = db.Exec(`CREATE TABLE promotions (code TEXT PRIMARY KEY, type TEXT NOT NULL, value INTEGER NOT NULL, description TEXT, starts_at TEXT, ends_at TEXT, customer_id TEXT, is_active INTEGER NOT NULL DEFAULT 1);`)
	_, _ = db.Exec(`INSERT INTO promotions(code, type, value, description, is_active) VALUES('PROMO50','amount',50,'50p off',1)`)

	dp := &common.Deps{
		State:  common.RuntimeState{Currency: "GBP", TaxRatePct: 20},
		Engine: pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, nil),
		Db:     db,
	}
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/pos/scan", bytes.NewReader([]byte("code=PROMO50")))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if dp.Engine.SaleDiscount() != 50 {
		t.Fatalf("expected discount 50, got %d", dp.Engine.SaleDiscount())
	}
}

func TestPromoBarcodeSetsDiscount_Percent(t *testing.T) {
	chdirRoot(t)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	_, _ = db.Exec(`CREATE TABLE promotions (code TEXT PRIMARY KEY, type TEXT NOT NULL, value INTEGER NOT NULL, description TEXT, starts_at TEXT, ends_at TEXT, customer_id TEXT, is_active INTEGER NOT NULL DEFAULT 1);`)
	_, _ = db.Exec(`CREATE TABLE customers (id TEXT PRIMARY KEY);`)
	_, _ = db.Exec(`INSERT INTO promotions(code, type, value, description, is_active) VALUES('DISC10','percent',1000,'10% off',1)`)

	resolver := promoStubResolver{
		"100": {SKU: "100", Name: "Item", Qty: 1, PriceCents: 1000},
	}
	dp := &common.Deps{
		State:  common.RuntimeState{Currency: "GBP", TaxRatePct: 20},
		Engine: pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver),
		Db:     db,
	}
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)

	// add an item so percent has a base
	if _, err := dp.Engine.Scan("100"); err != nil {
		t.Fatalf("scan: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/scan", bytes.NewReader([]byte("code=DISC10")))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.SaleDiscount(); got != 100 {
		t.Fatalf("expected 10%% of 1000 = 100, got %d", got)
	}
}

func TestPromoBarcodeRequiresCustomerMatch(t *testing.T) {
	chdirRoot(t)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	_, _ = db.Exec(`CREATE TABLE promotions (code TEXT PRIMARY KEY, type TEXT NOT NULL, value INTEGER NOT NULL, description TEXT, starts_at TEXT, ends_at TEXT, customer_id TEXT, is_active INTEGER NOT NULL DEFAULT 1);`)
	_, _ = db.Exec(`CREATE TABLE customers (id TEXT PRIMARY KEY, loyalty_no TEXT, phone TEXT);`)
	_, _ = db.Exec(`INSERT INTO customers(id, loyalty_no) VALUES('cust-1','LOY123');`)
	_, _ = db.Exec(`INSERT INTO promotions(code, type, value, description, customer_id, is_active) VALUES('PROMO-CUST','amount',250,'£2.50 off','cust-1',1);`)

	dp := &common.Deps{
		State:  common.RuntimeState{Currency: "GBP", TaxRatePct: 20},
		Engine: pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, promoStubResolver{}),
		Db:     db,
	}
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)

	// scan promo without customer should not apply
	req := httptest.NewRequest(http.MethodPost, "/api/pos/scan", bytes.NewReader([]byte("code=PROMO-CUST")))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if dp.Engine.SaleDiscount() != 0 {
		t.Fatalf("expected no discount without customer, got %d", dp.Engine.SaleDiscount())
	}

	// scan customer barcode, then promo
	reqCust := httptest.NewRequest(http.MethodPost, "/api/pos/scan", bytes.NewReader([]byte("code=LOY123")))
	reqCust.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recCust := httptest.NewRecorder()
	mux.ServeHTTP(recCust, reqCust)

	req2 := httptest.NewRequest(http.MethodPost, "/api/pos/scan", bytes.NewReader([]byte("code=PROMO-CUST")))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	if dp.Engine.SaleDiscount() != 250 {
		t.Fatalf("expected customer-targeted promo applied, got %d", dp.Engine.SaleDiscount())
	}
}
