package catalog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// The item cost-price field ("what the shop pays", feeds the margin report)
// is entered in the shop's display currency, exactly like modifier-option
// price deltas — and must honor the currency's minor-unit exponent the same
// way. A 0-decimal currency (IRR/IRT/IQD/AFN/JPY, all supported — see
// httpx.currencies) has no subdivision at all: 1 major = 1 minor. A
// hardcoded *100 stores every cost 100x too high for those shops, and the
// panel's hardcoded /100 "%.2f" then displays a stored cost 100x too low —
// the margin report would be garbage either way. The modifier-option
// handler in this same file already fixed exactly this class of bug; the
// cost field predates that fix.
func TestItemCost_RespectsZeroDecimalCurrency_OnSave(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "RICE", Name: "Basmati 5kg", BasePrice: 900000, IsActive: true})

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default", Currency: "IRR"}, Menu: []common.MenuItem{}})

	form := "panelItem=itm1&cost=650000"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/item-cost", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var minor int64
	if err := db.QueryRow(`SELECT cost_price FROM items WHERE id = 'itm1'`).Scan(&minor); err != nil {
		t.Fatal(err)
	}
	if minor != 650000 {
		t.Fatalf("IRR has 0 decimals (1 major = 1 minor): want cost_price 650000, got %d (looks like a hardcoded *100 was applied)", minor)
	}
}

func TestItemCost_RespectsZeroDecimalCurrency_OnDisplay(t *testing.T) {
	chdirToRepoRoot(t)
	// The {{ currency }} template func reads the process-global currency
	// (kept in sync with settings via httpx.InitCurrency in production —
	// see internal/pages/init.go), so the template-side assertions need it
	// set too, not just Deps.State.Currency.
	httpx.InitCurrency("IRR")
	t.Cleanup(func() { httpx.InitCurrency("GBP") })
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "RICE", Name: "Basmati 5kg", BasePrice: 900000, IsActive: true})
	if _, err := db.Exec(`UPDATE items SET cost_price = 650000 WHERE id = 'itm1'`); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default", Currency: "IRR"}, Menu: []common.MenuItem{}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/item-variants?item_id=itm1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `value="650000"`) {
		t.Fatalf("panel must show the stored 650000-rial cost as 650000 (0-decimal currency), body has: %s",
			excerptAround(body, `name="cost"`))
	}
	if strings.Contains(body, `value="6500.00"`) {
		t.Fatal("panel shows the rial cost divided by a hardcoded 100")
	}
	// The input's step must also follow the currency, matching the sibling
	// variant/modifier price fields ("1" for a 0-decimal currency, "0.01"
	// otherwise) — a step="0.01" field would let a rial shop type an
	// impossible fractional rial.
	if strings.Contains(body, `name="cost" step="0.01"`) {
		t.Fatal(`cost input step is hardcoded to "0.01" for a 0-decimal currency`)
	}
}

// Control: the 2-decimal path (the overwhelmingly common case) keeps
// working — 12.34 GBP stores as 1234 pence and renders back as 12.34.
func TestItemCost_TwoDecimalCurrencyRoundTrip(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "TEA", Name: "Earl Grey", BasePrice: 300, IsActive: true})

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default", Currency: "GBP"}, Menu: []common.MenuItem{}})

	form := "panelItem=itm1&cost=12.34"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/item-cost", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var minor int64
	if err := db.QueryRow(`SELECT cost_price FROM items WHERE id = 'itm1'`).Scan(&minor); err != nil {
		t.Fatal(err)
	}
	if minor != 1234 {
		t.Fatalf("want cost_price 1234 (12.34 GBP), got %d", minor)
	}
	// And it renders back in major units in the panel.
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/catalog/item-variants?item_id=itm1", nil))
	if !strings.Contains(rec2.Body.String(), `value="12.34"`) {
		t.Fatalf("panel must render 1234 pence back as 12.34, body has: %s",
			excerptAround(rec2.Body.String(), `name="cost"`))
	}
}

// excerptAround returns a short window of s around the first occurrence of
// marker, for readable failure messages against full-page HTML bodies.
func excerptAround(s, marker string) string {
	i := strings.Index(s, marker)
	if i < 0 {
		return "(marker " + marker + " not found)"
	}
	start := i - 120
	if start < 0 {
		start = 0
	}
	end := i + 200
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}
