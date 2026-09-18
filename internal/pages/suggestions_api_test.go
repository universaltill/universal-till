package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
)

// registerSuggestions reads the related_items co-occurrence table, which isn't
// in the simplified seedForPages schema, so these tests use a fully migrated
// database (same rationale as sync_admin_test.go).
func newSuggestionsTestDeps(t *testing.T, engine *pos.Service) *common.Deps {
	t.Helper()
	chdirRoot(t)
	d := openPagesTestDB(t)
	t.Cleanup(func() { d.Close() })

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", Locale: "en", TaxRate: 20}}
	pm, err := plugins.Init(t.Context(), cfg, d)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(d), cfg)
	return &common.Deps{
		Cfg:      cfg,
		Db:       d,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Engine:   engine,
		Pm:       pm,
		Settings: settings.NewStore(d),
	}
}

func TestSuggestions_EmptyBasketRendersHiddenStrip(t *testing.T) {
	engine := pos.NewServiceWithResolver(pos.Config{}, stubResolver{})
	dp := newSuggestionsTestDeps(t, engine)

	mux := http.NewServeMux()
	registerSuggestions(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/ui/suggestions", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/suggestions: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "suggest-strip") {
		t.Fatalf("expected the suggest strip container, got %s", body)
	}
	if !strings.Contains(body, "hidden") {
		t.Fatalf("expected an empty basket to render a hidden strip, got %s", body)
	}
	if strings.Contains(body, "suggest-chip") {
		t.Fatalf("expected no suggestion chips for an empty basket, got %s", body)
	}
}

func TestSuggestions_RendersChipsForBasketItems(t *testing.T) {
	// Resolve "BASK" to an item in the basket; the DB rows below make itmSug a
	// related item so it should surface as a chip.
	resolver := stubResolver{
		"BASK": {SKU: "BASK", Name: "Basket Item", Qty: 1, PriceCents: 100, ItemID: "itmBask"},
	}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000}, resolver)
	if _, err := engine.Scan("BASK"); err != nil {
		t.Fatalf("scan basket item: %v", err)
	}

	dp := newSuggestionsTestDeps(t, engine)

	// Seed the two items and a related_items co-occurrence edge.
	if _, err := dp.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES('itmBask','BASK','Basket Item',100,1)`); err != nil {
		t.Fatalf("seed basket item: %v", err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES('itmSug','SUGG','Suggested Item',250,1)`); err != nil {
		t.Fatalf("seed suggested item: %v", err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO related_items(item_id,related_item_id,support,score) VALUES('itmBask','itmSug',5,9.0)`); err != nil {
		t.Fatalf("seed related_items: %v", err)
	}

	mux := http.NewServeMux()
	registerSuggestions(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/ui/suggestions", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/suggestions: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "suggest-chip") {
		t.Fatalf("expected a suggestion chip, got %s", body)
	}
	if !strings.Contains(body, "Suggested Item") {
		t.Fatalf("expected the suggested item name, got %s", body)
	}
	// The chip adds by SKU through the scan path.
	if !strings.Contains(body, "SUGG") {
		t.Fatalf("expected the suggested item SKU in the chip, got %s", body)
	}
	// The strip must not be hidden when there are suggestions.
	if strings.Contains(body, `hidden>`) {
		t.Fatalf("expected a visible strip when suggestions exist, got %s", body)
	}
}

// TestSuggestions_ChipShowsCurrentPriceHistoryPrice is ut-docs#2258's
// review blocker: the suggestion chip posts to the SAME /api/pos/scan
// endpoint the fixed sale-screen tile does, so it must show the item's
// CURRENT effective price too — SuggestForBasket's own SELECT reads raw
// base_price, so without this the chip could show one price (e.g. £3.10)
// while tapping it charges another (e.g. £2.50), the exact quote-vs-charge
// hazard the ticket exists to close, one partial lower on the same screen.
func TestSuggestions_ChipShowsCurrentPriceHistoryPrice(t *testing.T) {
	resolver := stubResolver{
		"BASK": {SKU: "BASK", Name: "Basket Item", Qty: 1, PriceCents: 100, ItemID: "itmBask"},
	}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000}, resolver)
	if _, err := engine.Scan("BASK"); err != nil {
		t.Fatalf("scan basket item: %v", err)
	}

	dp := newSuggestionsTestDeps(t, engine)

	if _, err := dp.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES('itmBask','BASK','Basket Item',100,1)`); err != nil {
		t.Fatalf("seed basket item: %v", err)
	}
	// itmSug's configured price is 310, but an active price_history row
	// overrides it to 250 — the exact £3.10-vs-£2.50 shape from ut-docs#2228.
	if _, err := dp.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES('itmSug','SUGG','Suggested Item',310,1)`); err != nil {
		t.Fatalf("seed suggested item: %v", err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO related_items(item_id,related_item_id,support,score) VALUES('itmBask','itmSug',5,9.0)`); err != nil {
		t.Fatalf("seed related_items: %v", err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO price_history(id,item_id,price,starts_at) VALUES('ph1','itmSug',250,datetime('now','-1 hour'))`); err != nil {
		t.Fatalf("seed price_history: %v", err)
	}

	mux := http.NewServeMux()
	registerSuggestions(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/ui/suggestions", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/suggestions: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "£2.50") {
		t.Fatalf("expected the chip to show the active price_history override £2.50, got: %s", body)
	}
	if strings.Contains(body, "£3.10") {
		t.Fatalf("chip must not show the stale configured price £3.10: %s", body)
	}
}
