package pages

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#2649: the sell screen's green Pay button showed the total in Latin
// digits under fa/ar even though the basket beside it showed the locale's
// own digit shapes. Root cause: index.html's page-load render goes through
// the locale-aware {{ money }} template func (httpx.FormatMoney), but every
// #basket htmx swap afterward re-labelled the button from
// window.utCurrency.format(total) (web/public/app.js), which has no digit
// shaping. The fix threads the server-formatted amount through basket.html's
// existing data-attribute-bridge pattern (.total already carries
// data-minor) as a new data-label attribute, so the button's refresh script
// can reuse the exact server-rendered string instead of reformatting it in
// JS.
//
// These tests pin the SERVER half of that fix: basket.html's .total element
// carries a data-label matching httpx.FormatMoney(total, locale) with the
// locale's own digit shapes, and the payment-open button (already correct
// at page load, per the bug's own root-cause analysis) keeps showing that
// same formatted amount. The bridge actually being READ after a swap is a
// browser-JS behaviour and is covered by the e2e spec instead
// (e2e/tests/pay-button-locale-digits-2649.spec.ts).

// totalDataLabelRE captures the .total element's data-minor and data-label
// attributes off a rendered #basket fragment, in the order basket.html
// renders them -- deliberately pinned to THAT order so a data-label added
// before data-minor (or spelled differently) fails this test rather than
// silently not matching.
var totalDataLabelRE = regexp.MustCompile(`(?s)class="total" data-minor="(\d+)" data-label="([^"]*)"`)

// paymentOpenButtonTextRE captures the payment-open button's rendered inner
// text off a rendered index page.
var paymentOpenButtonTextRE = regexp.MustCompile(`(?s)data-testid="payment-open".*?>(.*?)</button>`)

// newPayButtonLocaleDigitsTestDeps wires a Deps with a real scan-resolving
// Engine AND both the sale screen ("/") and the basket fragment
// ("/ui/basket") registered on one mux sharing that engine, so a POST scan
// against one route is visible to a GET against the other -- exactly the
// two surfaces ut-docs#2649 is about (the basket panel and the Pay button
// that lives outside it).
func newPayButtonLocaleDigitsTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)
	// ut-docs#2227: seedForPages' itm1/ABC carries a real sellable variant
	// (var1/VAR), so scanning "ABC" alone asks for a variant instead of
	// adding a line directly -- a variant-FREE item is seeded locally here,
	// the same shape newPOSTestDeps (pos_api_test.go) uses for its own
	// "PLAIN" fixture, so a plain scan actually produces a non-empty total.
	if _, err := db.Exec(`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('itm-plain','PLAIN','Plain Item',100,'tax_std',1)`); err != nil {
		t.Fatalf("seed plain item: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO item_barcodes(barcode,item_id,is_primary) VALUES('PLAIN','itm-plain',1)`); err != nil {
		t.Fatalf("seed plain item barcode: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO inventory(id,item_id,variant_id,location_id,quantity,updated_at) VALUES('inv-plain','itm-plain',NULL,'loc_main',50,datetime('now'))`); err != nil {
		t.Fatalf("seed plain item inventory: %v", err)
	}

	resolver := stubResolver{
		"PLAIN": {SKU: "PLAIN", Name: "Plain Item", Qty: 1, PriceCents: 100, ItemID: "itm-plain", TaxRateBP: 2000},
	}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	setStore := settings.NewStore(db)
	state := common.LoadState(t.Context(), setStore, cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Engine:   engine,
		Pm:       pm,
		Settings: setStore,
	}
	t.Cleanup(dp.WaitForAsyncWork)

	// The active currency is a process global (httpx.InitCurrency), not
	// something this lightweight harness's cfg/state wiring publishes on its
	// own (that only happens in production's internal/pages/init.go) --
	// pinned explicitly here so the test is hermetic regardless of what
	// other tests in this binary last set it to, matching
	// TestFuncsForExposesMoneyAndI18n's own approach in internal/httpx.
	httpx.InitCurrency("GBP")

	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	registerBasket(mux, dp)
	registerIndex(mux, dp)
	return mux, dp
}

func posGet(t *testing.T, mux *http.ServeMux, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestBasketTotal_DataLabelCarriesLocaleFormattedAmount(t *testing.T) {
	mux, dp := newPayButtonLocaleDigitsTestDeps(t)

	// Non-empty basket, required by the card: one real line so Total > 0.
	rec := posPostForm(mux, "/api/pos/scan", "code=PLAIN")
	if rec.Code != http.StatusOK {
		t.Fatalf("seed scan: %d: %s", rec.Code, rec.Body.String())
	}
	total := dp.Engine.Basket().Total
	if total.Minor() == 0 {
		t.Fatalf("test setup: basket total is zero, can't tell locale digit shape apart from an empty amount")
	}

	for _, tc := range []struct {
		locale    string
		wantDigit string // one digit from that locale's own numeral set
	}{
		// LocalizeDigits' own table (internal/httpx/currency_test.go):
		// fa -> Persian digits (۰-۹), ar -> Arabic-Indic digits (٠-٩).
		{"fa", "۱"},
		{"ar", "١"},
	} {
		t.Run(tc.locale, func(t *testing.T) {
			body := posGet(t, mux, "/ui/basket?lang="+tc.locale)
			m := totalDataLabelRE.FindStringSubmatch(body)
			if m == nil {
				t.Fatalf("%s: .total element with data-minor+data-label not found in rendered basket:\n%s", tc.locale, body)
			}
			gotLabel := m[2]
			want := httpx.FormatMoney(total.Minor(), tc.locale)
			if gotLabel != want {
				t.Errorf("%s: .total data-label = %q, want %q (httpx.FormatMoney)", tc.locale, gotLabel, want)
			}
			if !strings.Contains(gotLabel, tc.wantDigit) {
				t.Errorf("%s: .total data-label = %q does not contain expected locale digit %q -- still Latin digits?", tc.locale, gotLabel, tc.wantDigit)
			}
			for _, d := range "0123456789" {
				if strings.ContainsRune(gotLabel, d) {
					t.Errorf("%s: .total data-label = %q contains ASCII digit %q, want locale digit shapes only", tc.locale, gotLabel, d)
				}
			}
		})
	}
}

func TestPaymentOpenButton_ServerRenderUsesLocaleFormattedAmount(t *testing.T) {
	mux, dp := newPayButtonLocaleDigitsTestDeps(t)

	rec := posPostForm(mux, "/api/pos/scan", "code=PLAIN")
	if rec.Code != http.StatusOK {
		t.Fatalf("seed scan: %d: %s", rec.Code, rec.Body.String())
	}
	total := dp.Engine.Basket().Total
	if total.Minor() == 0 {
		t.Fatalf("test setup: basket total is zero")
	}

	for _, locale := range []string{"fa", "ar"} {
		t.Run(locale, func(t *testing.T) {
			body := posGet(t, mux, "/?lang="+locale)
			m := paymentOpenButtonTextRE.FindStringSubmatch(body)
			if m == nil {
				t.Fatalf("%s: payment-open button not found in rendered index page", locale)
			}
			text := m[1]
			want := httpx.FormatMoney(total.Minor(), locale)
			if !strings.Contains(text, want) {
				t.Errorf("%s: payment-open button text = %q, want it to contain %q", locale, text, want)
			}
			for _, d := range "0123456789" {
				if strings.ContainsRune(text, d) {
					t.Errorf("%s: payment-open button text = %q contains ASCII digit %q", locale, text, d)
				}
			}
		})
	}
}
