package pages

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	appdb "github.com/universaltill/universal-till/internal/db"
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
	d, err := appdb.Open(filepath.Join(t.TempDir(), "suggestions.db"))
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", Locale: "en", TaxRate: 20}}
	pm, err := plugins.Init(t.Context(), cfg, d.DB)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(d.DB), cfg)
	return &common.Deps{
		Cfg:      cfg,
		Db:       d.DB,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Engine:   engine,
		Pm:       pm,
		Settings: settings.NewStore(d.DB),
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
