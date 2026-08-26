package pages

// Per-country settings admin page (universaltill/ut-docs#659): manager
// gating, a real render of web/ui/pages/country_settings.html, and the two
// behaviours that must survive the HTTP layer — the retention floor is
// refused, and "delete" on a builtin country restores it rather than
// removing it.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

func newCountrySettingsTestMux(t *testing.T) (*http.ServeMux, *data.CountrySettingsRepo, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	dbase, err := db.Open(filepath.Join(t.TempDir(), "country-settings-page.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbase.Close() })
	d := &common.Deps{Db: dbase.DB, Settings: settings.NewStore(dbase.DB), Menu: []common.MenuItem{{Href: "/", Label: "Home"}}, AuthSvc: auth.NewService(dbase.DB)}
	mux := http.NewServeMux()
	registerCountrySettings(mux, d)
	return mux, data.NewCountrySettingsRepo(dbase.DB), d
}

// ut-docs#902: GET /country-settings must be reachable under UT_AUTH=off
// with no session, matching every other admin page's canPerform(...,
// "settings") escape hatch (ut-docs#901's precedent). Before this fix,
// requireManager read auth.FromContext directly and failed closed
// permanently under UT_AUTH=off, since no session is ever set in that mode.
func TestCountrySettingsPage_ReachableUnderAuthOff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, _ := newCountrySettingsTestMux(t)

	req := httptest.NewRequest(http.MethodGet, "/country-settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /country-settings under UT_AUTH=off = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// Mutating handlers, not just the GET page, must also pick up canPerform's
// UT_AUTH=off bypass — independent review finding on ut-docs#901, applied
// here too (the GET-only regression test above only pins the read path).
func TestCountrySettingsPageCreate_ReachableUnderAuthOff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, _ := newCountrySettingsTestMux(t)

	rec := postForm(mux, "/api/country-settings", url.Values{
		"code":             {"ZZ"},
		"currency":         {"GBP"},
		"currency_symbol":  {"£"},
		"tax_rate_pct":     {"20"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays, 10)},
	}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/country-settings" {
		t.Fatalf("create under UT_AUTH=off: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestCountrySettingsPagePermissions(t *testing.T) {
	mux, _, _ := newCountrySettingsTestMux(t)
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/country-settings", nil), cashier)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier GET /country-settings = %d, want 403", rec.Code)
	}
	if rec := postForm(mux, "/api/country-settings", url.Values{"code": {"GB"}}, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier save = %d, want 403", rec.Code)
	}
	if rec := postForm(mux, "/api/country-settings/GB/delete", url.Values{}, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier delete = %d, want 403", rec.Code)
	}
}

// TestCountrySettingsPageDefaultShowsOnlyShopCountry pins ut-docs#1024's
// core requirement: a single-shop merchant sees their own country's row by
// default, not all 14 seeded jurisdictions. Replaces the old
// TestCountrySettingsPageRendersSeededCountries, which asserted the
// unfiltered-by-design render this card explicitly changes.
func TestCountrySettingsPageDefaultShowsOnlyShopCountry(t *testing.T) {
	mux, _, d := newCountrySettingsTestMux(t)
	d.SetState(common.RuntimeState{Country: "DE"})
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/country-settings", nil), mgr)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager GET = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "DE") {
		t.Errorf("rendered page missing shop's own country %q", "DE")
	}
	if strings.Contains(body, "FR") {
		t.Errorf("default view rendered another seeded country (%q) — should show only the shop's own", "FR")
	}
}

// TestCountrySettingsPageShowAllCountries pins the explicit "show all
// countries" affordance (?all=1): every seeded country renders, same as
// the pre-#1024 unconditional behavior.
func TestCountrySettingsPageShowAllCountries(t *testing.T) {
	mux, _, d := newCountrySettingsTestMux(t)
	d.SetState(common.RuntimeState{Country: "DE"})
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/country-settings?all=1", nil), mgr)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager GET ?all=1 = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"DE", "FR", "GBP", "Country settings"} {
		if !strings.Contains(body, want) {
			t.Errorf("?all=1 rendered page missing %q — expected the full seeded list", want)
		}
	}
}

// TestCountrySettingsPageUnknownShopCountry_ShowsAllWithExplanation pins
// the review-found fallback: when the shop's configured country matches no
// row (shouldn't happen in practice, but defensively handled), the page
// shows every country with an explanation — and must NOT offer a "show
// only my country" link, since that link would just re-enter this same
// fallback (the review's finding: it was rendered but permanently inert).
func TestCountrySettingsPageUnknownShopCountry_ShowsAllWithExplanation(t *testing.T) {
	mux, _, d := newCountrySettingsTestMux(t)
	d.SetState(common.RuntimeState{Country: "ZZ"}) // not a seeded/custom code
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/country-settings", nil), mgr)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager GET = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"DE", "FR", "GBP"} {
		if !strings.Contains(body, want) {
			t.Errorf("unknown-shop-country fallback missing %q — expected the full seeded list", want)
		}
	}
	if !strings.Contains(body, "in this list") {
		t.Errorf("unknown-shop-country fallback missing its explanation text")
	}
	if strings.Contains(body, "Show only my country") {
		t.Errorf("unknown-shop-country fallback rendered a 'show only my country' link — it would just re-enter this same fallback")
	}
}

// TestCountrySettingsPageSave_FromAllView_RedirectsBackToAllView pins the
// review-found redirect gap: a save/delete made from the "show all
// countries" view (?all=1) must land back on that same view, not silently
// drop to the filtered default where the edited row — or a newly-added
// custom country — would be invisible and read as "nothing happened."
func TestCountrySettingsPageSave_FromAllView_RedirectsBackToAllView(t *testing.T) {
	mux, _, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	rec := postForm(mux, "/api/country-settings?all=1", url.Values{
		"code":             {"FR"},
		"currency":         {"EUR"},
		"tax_rate_pct":     {"20"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays, 10)},
	}, &mgr)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save from all view = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/country-settings?all=1" {
		t.Errorf("save redirect = %q, want /country-settings?all=1 (must carry the view through)", loc)
	}

	rec = postForm(mux, "/api/country-settings/FR/delete?all=1", url.Values{}, &mgr)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete from all view = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/country-settings?all=1" {
		t.Errorf("delete redirect = %q, want /country-settings?all=1 (must carry the view through)", loc)
	}
}

// TestCountrySettingsPageSave_FromDefaultView_RedirectsToDefaultView proves
// the "all=1" carry-through is opt-in, not always-on: a save from the
// default filtered view must NOT pick up all=1 out of nowhere.
func TestCountrySettingsPageSave_FromDefaultView_RedirectsToDefaultView(t *testing.T) {
	mux, _, d := newCountrySettingsTestMux(t)
	d.SetState(common.RuntimeState{Country: "GB"})
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	rec := postForm(mux, "/api/country-settings", url.Values{
		"code":             {"GB"},
		"currency":         {"GBP"},
		"tax_rate_pct":     {"20"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays, 10)},
	}, &mgr)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save from default view = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/country-settings" {
		t.Errorf("save redirect = %q, want plain /country-settings", loc)
	}
}

func TestCountrySettingsPageSaveAndFloorRefusal(t *testing.T) {
	mux, repo, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}
	ctx := t.Context()

	// A legitimate edit: raise retention and change the tax rate.
	raised := data.GlobalArchiveMinDays + 100
	rec := postForm(mux, "/api/country-settings", url.Values{
		"code":             {"DE"},
		"currency":         {"EUR"},
		"currency_symbol":  {"€"},
		"tax_rate_pct":     {"7"},
		"tax_inclusive":    {"1"},
		"archive_min_days": {strconv.FormatInt(raised, 10)},
	}, &mgr)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("manager save = %d, want 303", rec.Code)
	}
	de, _, err := repo.Get(ctx, "DE")
	if err != nil {
		t.Fatal(err)
	}
	if de.TaxRateBP != 700 {
		t.Errorf("tax rate = %d bp, want 700 (7%% entered as percent)", de.TaxRateBP)
	}
	if de.ArchiveMinDays != raised {
		t.Errorf("archive_min_days = %d, want %d", de.ArchiveMinDays, raised)
	}

	// Below the floor: refused, redirected to the specific error, and the
	// stored value is untouched.
	rec = postForm(mux, "/api/country-settings", url.Values{
		"code":             {"DE"},
		"currency":         {"EUR"},
		"tax_rate_pct":     {"7"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays-1, 10)},
	}, &mgr)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "below_floor") {
		t.Errorf("redirect = %q, want the below_floor error", loc)
	}
	after, _, _ := repo.Get(ctx, "DE")
	if after.ArchiveMinDays != raised {
		t.Errorf("refused save changed retention to %d, want it left at %d", after.ArchiveMinDays, raised)
	}
}

func TestCountrySettingsPageDeleteRestoresBuiltin(t *testing.T) {
	mux, repo, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}
	ctx := t.Context()

	if rec := postForm(mux, "/api/country-settings", url.Values{
		"code":             {"DE"},
		"currency":         {"XXX"},
		"tax_rate_pct":     {"1"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays+500, 10)},
	}, &mgr); rec.Code != http.StatusSeeOther {
		t.Fatalf("save = %d", rec.Code)
	}

	if rec := postForm(mux, "/api/country-settings/DE/delete", url.Values{}, &mgr); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete = %d, want 303", rec.Code)
	}

	de, ok, err := repo.Get(ctx, "DE")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("deleting a builtin country through the page removed it; it must restore defaults")
	}
	if de.Currency != "EUR" || de.TaxRateBP != 1900 || de.ArchiveMinDays != data.GlobalArchiveMinDays {
		t.Errorf("DE not restored: {%q %d %d}", de.Currency, de.TaxRateBP, de.ArchiveMinDays)
	}
}

// Percent → basis points is the one lossy-looking conversion in this page;
// 8.5% must not land on 849 through float truncation.
func TestParsePercentAsBPRounding(t *testing.T) {
	cases := map[string]int64{"0": 0, "": 0, "7": 700, "19": 1900, "8.5": 850, "2.25": 225, "0.01": 1}
	for in, want := range cases {
		got, err := parsePercentAsBP(in)
		if err != nil {
			t.Errorf("parsePercentAsBP(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parsePercentAsBP(%q) = %d, want %d", in, got, want)
		}
	}
	if _, err := parsePercentAsBP("-1"); err == nil {
		t.Error("negative percent should error")
	}
	if _, err := parsePercentAsBP("abc"); err == nil {
		t.Error("non-numeric percent should error")
	}
}

func TestFormatBPAsPercent(t *testing.T) {
	cases := map[int64]string{0: "0", 700: "7", 1900: "19", 850: "8.50", 225: "2.25"}
	for in, want := range cases {
		if got := formatBPAsPercent(in); got != want {
			t.Errorf("formatBPAsPercent(%d) = %q, want %q", in, got, want)
		}
	}
}
