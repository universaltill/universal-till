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

func TestOfflineTenderUpdatesJournal(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	resolver := stubResolver{
		"ABC": {SKU: "ABC", Name: "Test Item", Qty: 1, PriceCents: 250, ItemID: "itm1", TaxRateBP: 2000},
	}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)
	if _, err := engine.Scan("ABC"); err != nil {
		t.Fatalf("scan seed item: %v", err)
	}

	setStore := settings.NewStore(db)
	state := common.LoadState(t.Context(), setStore, &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}})
	pm, err := plugins.Init(t.Context(), &config.Config{}, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	dp := &common.Deps{
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Engine:   engine,
		Pm:       pm,
		Settings: setStore,
	}

	// Deferred AFTER db.Close above, so LIFO order drains this FIRST: the
	// offline tender below fires printReceiptAsync (ut-docs#425, #514),
	// which must finish touching db before Close and TempDir removal.
	defer dp.WaitForAsyncWork()

	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	registerJournal(mux, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(`{"payments":[{"method":"cash","amount":300}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("offline tender failed: code %d body %s", rec.Code, rec.Body.String())
	}

	var receipt string
	if err := db.QueryRow(`SELECT receipt_no FROM sales LIMIT 1`).Scan(&receipt); err != nil {
		t.Fatalf("read receipt: %v", err)
	}
	if receipt == "" {
		t.Fatalf("expected receipt number to be set")
	}

	journalReq := httptest.NewRequest(http.MethodGet, "/ui/journal", nil)
	journalRec := httptest.NewRecorder()
	mux.ServeHTTP(journalRec, journalReq)
	if journalRec.Code != http.StatusOK {
		t.Fatalf("journal render failed: code %d body %s", journalRec.Code, journalRec.Body.String())
	}
	if !strings.Contains(journalRec.Body.String(), receipt) {
		t.Fatalf("journal missing receipt %s", receipt)
	}
}
