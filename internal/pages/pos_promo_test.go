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

func TestPromoBarcodeSetsDiscount_FromDB(t *testing.T) {
	chdirRoot(t)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	_, _ = db.Exec(`CREATE TABLE promotions (code TEXT PRIMARY KEY, amount INTEGER NOT NULL, description TEXT, starts_at TEXT, ends_at TEXT, customer_id TEXT, is_active INTEGER NOT NULL DEFAULT 1);`)
	_, _ = db.Exec(`INSERT INTO promotions(code, amount, description, is_active) VALUES('PROMO50',50,'50p off',1)`)

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

func TestPromoBarcodeSetsDiscount_FromPrefix(t *testing.T) {
	chdirRoot(t)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	_, _ = db.Exec(`CREATE TABLE promotions (code TEXT PRIMARY KEY, amount INTEGER NOT NULL, description TEXT, starts_at TEXT, ends_at TEXT, customer_id TEXT, is_active INTEGER NOT NULL DEFAULT 1);`)
	dp := &common.Deps{Db: db, State: common.RuntimeState{Currency: "GBP", TaxRatePct: 20}, Engine: pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, nil)}
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/pos/scan", bytes.NewReader([]byte("code=PROMO5.00")))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if dp.Engine.SaleDiscount() != 500 {
		t.Fatalf("expected discount 500 (minor units), got %d", dp.Engine.SaleDiscount())
	}
}
