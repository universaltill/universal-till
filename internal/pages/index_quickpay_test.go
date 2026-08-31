package pages

// ut-docs#1336: one-tap quick pay — the sale screen's default view renders a
// full-width quick-pay button below Hold Sale/Payment that tenders the shop's
// preferred/default method directly. These tests cover the Go-side plumbing
// (the defaultPayMethod template var) at the level the e2e suite can't: the
// e2e till's demo seed has no payment_methods rows, so Playwright only ever
// exercises the zero-state cash fallback branch — the labeled branch (a real
// method row, preferred-first via payments.default_method) is proven here
// against the real handler + template instead.

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

func quickPayTestMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	seedForPages(t, db)

	resolver := stubResolver{}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)
	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Engine:   engine,
		Pm:       pm,
		Settings: settings.NewStore(db),
	}
	mux := http.NewServeMux()
	registerIndex(mux, dp)
	return mux, dp
}

// quickPayButtonSnippet isolates the quick-pay button's own opening tag from
// the full page body. The page also contains the overlay pay-grid's own Cash
// button, which independently renders an escaped hx-vals='{&#34;amount&#34;...}'
// for method=cash — a whole-page substring search can't tell that apart from
// quick-pay's own attribute, so scope the search to quick-pay's own tag.
func quickPayButtonSnippet(t *testing.T, home string) string {
	t.Helper()
	idx := strings.Index(home, `data-testid="quick-pay"`)
	if idx == -1 {
		t.Fatalf("quick-pay button missing from home page")
	}
	closeIdx := strings.Index(home[idx:], "</button>")
	if closeIdx == -1 {
		t.Fatalf("quick-pay button's closing tag not found")
	}
	return home[idx : idx+closeIdx+len("</button>")]
}

func getHome(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / failed: code %d body %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// With real payment_methods rows and no preference set, the quick-pay button
// tenders the FIRST active method (sort_order, id — the same head-of-list the
// overlay's pay-grid leads with) and shows its live DB name, not a locale key.
func TestIndexQuickPay_LabeledWithFirstActiveMethod(t *testing.T) {
	mux, _ := quickPayTestMux(t)
	home := getHome(t, mux)
	btn := quickPayButtonSnippet(t, home)

	// seedForPages seeds cash + card; ORDER BY sort_order, id puts 'card'
	// first when no payments.default_method preference exists.
	if !strings.Contains(btn, `data-method="card"`) {
		t.Fatalf("quick-pay should tender the first active method (card), tag lacks it: %s", btn)
	}
	if !strings.Contains(btn, "⚡ Card") {
		t.Fatalf("quick-pay label should be the method's live DB name (⚡ Card): %s", btn)
	}
	if !strings.Contains(btn, `hx-vals='{&#34;amount&#34;:0,&#34;method&#34;:&#34;card&#34;}'`) {
		t.Fatalf("quick-pay must dispatch the same amount:0/method tender POST as the pay-grid: %s", btn)
	}
}

// payments.default_method reorders payMethods preferred-first (ADR-0016), and
// the quick-pay button must follow that same head-of-list.
func TestIndexQuickPay_FollowsPreferredMethodSetting(t *testing.T) {
	mux, dp := quickPayTestMux(t)
	if err := dp.Settings.Set(t.Context(), "payments.default_method", "cash"); err != nil {
		t.Fatalf("set payments.default_method: %v", err)
	}
	home := getHome(t, mux)
	btn := quickPayButtonSnippet(t, home)

	if !strings.Contains(btn, `data-method="cash"`) {
		t.Fatalf("quick-pay should tender the preferred method (cash): %s", btn)
	}
	if !strings.Contains(btn, "⚡ Cash") {
		t.Fatalf("quick-pay label should be the preferred method's name (⚡ Cash): %s", btn)
	}
	// Independent review (ut-docs#1336, F1): the two assertions above alone
	// are a false-pass test — they can't tell "the preferred-method reorder
	// wired through" apart from "the hardcoded zero-state fallback
	// rendered," since both produce data-method="cash"/⚡ Cash. Only the
	// labeled branch's jsonVals action gets html/template-escaped; the
	// else-branch's static hx-vals text doesn't. Scoping to quick-pay's own
	// tag (quickPayButtonSnippet) also matters here: the page's separate
	// overlay pay-grid Cash button independently renders the same escaped
	// string for method=cash, so a whole-page Contains would false-pass
	// even with the escaped-form check. Assert the escaped form on
	// quick-pay's own tag specifically to prove this is really the labeled
	// (payMethods[0]-driven) branch, not the fallback.
	if !strings.Contains(btn, `hx-vals='{&#34;amount&#34;:0,&#34;method&#34;:&#34;cash&#34;}'`) {
		t.Fatalf("quick-pay should render the labeled (escaped jsonVals) branch, not the zero-state fallback: %s", btn)
	}
}

// With no payment_methods rows at all, the button falls back to a hardcoded
// cash tender — mirroring the pay-grid's own zero-state else-branch (this is
// the one branch the e2e till's demo seed actually exercises too).
func TestIndexQuickPay_ZeroStateFallsBackToCash(t *testing.T) {
	mux, dp := quickPayTestMux(t)
	if _, err := dp.Db.Exec(`DELETE FROM payment_methods`); err != nil {
		t.Fatalf("clear payment_methods: %v", err)
	}
	home := getHome(t, mux)
	btn := quickPayButtonSnippet(t, home)

	if !strings.Contains(btn, `data-method="cash"`) {
		t.Fatalf("zero-state quick-pay should fall back to method=cash: %s", btn)
	}
	// The zero-state hx-vals is literal template text, so html/template
	// leaves its quotes unescaped (unlike the labeled branch's jsonVals
	// output, which is an injected value and gets &#34;-escaped).
	if !strings.Contains(btn, `hx-vals='{"amount":0,"method":"cash"}'`) {
		t.Fatalf("zero-state quick-pay must still dispatch the amount:0 cash tender POST: %s", btn)
	}
}
