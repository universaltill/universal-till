package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
)

// TestInventoryPage_PickerExcludesStockUntrackedItems covers ut-docs#1850:
// the goods-in/adjustment item picker on the /inventory page must not offer
// a stock_untracked item — selecting it there would let someone record a
// movement (and thereby create the very inventory row the flag exists to
// prevent). Found via a live driven run (Tester), not the automated suite:
// ListStockLevels/GetLowStockItems were fixed to exclude untracked items,
// but this picker reads catRepo.ListItems directly and was missed.
func TestInventoryPage_PickerExcludesStockUntrackedItems(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	registerInventoryPage(mux, dp)

	repo := data.NewCatalogRepo(dp.Db)
	if _, err := repo.CreateItem(t.Context(), catalogtypes.ItemInput{Name: "Tracked Pick", SKU: "TP-1", BasePrice: 100, IsActive: true}); err != nil {
		t.Fatalf("CreateItem tracked: %v", err)
	}
	if _, err := repo.CreateItem(t.Context(), catalogtypes.ItemInput{Name: "Untracked Pick", SKU: "UP-1", BasePrice: 100, IsActive: true, StockUntracked: true}); err != nil {
		t.Fatalf("CreateItem untracked: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/inventory", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /inventory: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Tracked Pick") {
		t.Fatalf("expected tracked item in the picker, got: %s", body)
	}
	if strings.Contains(body, "Untracked Pick") {
		t.Fatalf("stock_untracked item must NOT appear in the goods-in/adjustment picker, got: %s", body)
	}
}
