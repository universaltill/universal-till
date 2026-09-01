package pages

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	_ "modernc.org/sqlite"
)

func TestScanHandlerUpdatesBasketTotals(t *testing.T) {
	chdirRoot(t)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	_, _ = db.Exec(`CREATE TABLE customers (id TEXT PRIMARY KEY, name TEXT, loyalty_no TEXT, phone TEXT);`)
	_, _ = db.Exec(`CREATE TABLE promotions (code TEXT PRIMARY KEY, type TEXT NOT NULL, value INTEGER NOT NULL, description TEXT, starts_at TEXT, ends_at TEXT, customer_id TEXT, is_active INTEGER NOT NULL DEFAULT 1);`)

	resolver := stubResolver{
		"ABC": {SKU: "ABC", Name: "Test Item", Qty: 1, PriceCents: 100},
	}
	dp := &common.Deps{
		State:  common.RuntimeState{Currency: "GBP", TaxRatePct: 20},
		Engine: pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver),
		Db:     db,
	}
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/pos/scan", strings.NewReader("code=ABC&qty=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Test Item") {
		t.Fatalf("expected scanned item in response, got: %s", body)
	}
	// ut-docs#1284 review finding: a non-weighed line renders its
	// whole-number quantity as "1", not "1.00" -- the value must actually
	// match its own pattern="[0-9]+" (integer-only for a non-weighed
	// line), which "1.00" never did.
	if !strings.Contains(body, `name="qty" value="1"`) {
		t.Fatalf("expected qty 1 in response, got: %s", body)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/pos/scan", strings.NewReader("code=ABC&qty=1"))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	body2 := rec2.Body.String()
	// ut-docs#1284 review finding: see the same note above -- "2", not
	// "2.00".
	if !strings.Contains(body2, `name="qty" value="2"`) {
		t.Fatalf("expected qty 2 in response after rescan, got: %s", body2)
	}
	if !strings.Contains(body2, "Total:") {
		t.Fatalf("expected totals in response, got: %s", body2)
	}
}
