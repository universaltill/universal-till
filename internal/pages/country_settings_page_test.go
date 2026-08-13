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

func newCountrySettingsTestMux(t *testing.T) (*http.ServeMux, *data.CountrySettingsRepo) {
	t.Helper()
	chdirRoot(t)
	dbase, err := db.Open(filepath.Join(t.TempDir(), "country-settings-page.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbase.Close() })
	d := &common.Deps{Db: dbase.DB, Settings: settings.NewStore(dbase.DB), Menu: []common.MenuItem{{Href: "/", Label: "Home"}}}
	mux := http.NewServeMux()
	registerCountrySettings(mux, d)
	return mux, data.NewCountrySettingsRepo(dbase.DB)
}

func TestCountrySettingsPagePermissions(t *testing.T) {
	mux, _ := newCountrySettingsTestMux(t)
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

func TestCountrySettingsPageRendersSeededCountries(t *testing.T) {
	mux, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/country-settings", nil), mgr)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager GET = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"DE", "GBP", "Country settings"} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
}

func TestCountrySettingsPageSaveAndFloorRefusal(t *testing.T) {
	mux, repo := newCountrySettingsTestMux(t)
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
	mux, repo := newCountrySettingsTestMux(t)
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
