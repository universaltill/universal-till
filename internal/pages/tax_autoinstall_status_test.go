package pages

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// ADR-0172 §5 (ut-docs#6287): the status chip, the "installed
// automatically" note, the uninstall that declines, and the cashier
// refusals. Real install path against the fake marketplace.

// autoInstalledTaxDeps installs the clean fixture plugin through the real
// automatic path and returns the deps with auth wired.
func autoInstalledTaxDeps(t *testing.T) (*common.Deps, *fakeMarketplace) {
	t.Helper()
	t.Setenv("UT_AUTH", "")
	dp := newBasePluginTestDeps(t)
	dp.AuthSvc = auth.NewService(dp.Db)
	mkt := newFakeMarketplace(t, map[string]string{})
	mkt.publishCustomTaxManifest(t, autoTaxListing, autoTaxManifest(), buildFiscalGuest(t, "taxdecline_guest"))
	mkt.setCatalog(autoTaxCatalogEntry())
	dp.Cfg.Marketplace = mkt.config()
	plugins.SharedBus(dp.Db).ResetSubscribers()
	return dp, mkt
}

func chipBody(t *testing.T, dp *common.Deps, role string) string {
	t.Helper()
	mux := http.NewServeMux()
	registerTaxAutoInstallChip(mux, dp)
	req := httptest.NewRequest(http.MethodGet, "/ui/tax-autoinstall-chip", nil)
	req = auth.WithUser(req, auth.User{ID: "u-" + role, Role: role})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("chip: %d %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestTaxAutoInstallChip_StatesAndErrorClasses(t *testing.T) {
	spec := basePluginSpec{CanonicalType: "tax", Market: autoTaxCountry}
	tests := []struct {
		name        string
		setup       func(t *testing.T, dp *common.Deps, mkt *fakeMarketplace)
		wantState   string
		wantError   string // "" = none
		wantPolling bool
	}{
		{"installed", func(t *testing.T, dp *common.Deps, mkt *fakeMarketplace) {}, "installed", "", false},
		{"offline", func(t *testing.T, dp *common.Deps, mkt *fakeMarketplace) {
			dp.Cfg.Marketplace.EndpointURL = unreachableURL()
		}, "retrying", "offline", true},
		{"listed but no longer served", func(t *testing.T, dp *common.Deps, mkt *fakeMarketplace) {
			mkt.unpublish(autoTaxListing)
		}, "retrying", "not_listed", true},
		{"refused", func(t *testing.T, dp *common.Deps, mkt *fakeMarketplace) {
			mkt.publishCustomTaxManifest(t, autoTaxListing, autoTaxManifest(), buildFiscalGuest(t, "taxtrap_guest"))
		}, "retrying", "refused", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dp, mkt := autoInstalledTaxDeps(t)
			tc.setup(t, dp, mkt)
			_ = resolveAndInstallBasePlugin(t.Context(), dp, spec)

			body := chipBody(t, dp, "manager")
			if !strings.Contains(body, `data-state="`+tc.wantState+`"`) {
				t.Fatalf("want state %s, got:\n%s", tc.wantState, body)
			}
			if tc.wantError == "" && strings.Contains(body, "data-error") {
				t.Fatalf("no error class expected:\n%s", body)
			}
			if tc.wantError != "" && !strings.Contains(body, `data-error="`+tc.wantError+`"`) {
				t.Fatalf("want error class %s:\n%s", tc.wantError, body)
			}
			if polls := strings.Contains(body, `hx-get="/ui/tax-autoinstall-chip"`); polls != tc.wantPolling {
				t.Fatalf("polling = %v, want %v (it polls only while installing or retrying):\n%s", polls, tc.wantPolling, body)
			}
			if tc.wantState == "retrying" && !strings.Contains(body, "Sales are not affected") {
				t.Fatalf("a retrying chip must say sales are not affected:\n%s", body)
			}
		})
	}
}

// A cashier never sees this surface, and no chip at all when nothing is
// being installed.
func TestTaxAutoInstallChip_CashierAndEmpty(t *testing.T) {
	dp, _ := autoInstalledTaxDeps(t)
	if body := chipBody(t, dp, "manager"); strings.TrimSpace(body) != "" {
		t.Fatalf("no status recorded: no chip, got %q", body)
	}
	if err := resolveAndInstallBasePlugin(t.Context(), dp, basePluginSpec{CanonicalType: "tax", Market: autoTaxCountry}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if body := chipBody(t, dp, "cashier"); strings.TrimSpace(body) != "" {
		t.Fatalf("a cashier must not get the chip, got %q", body)
	}
}

// The core AC: an uninstalled auto-installed tax plugin is not reinstalled
// by the background retry, and the uninstall is admin/manager only (hidden
// AND server-refused).
func TestTaxAutoInstall_UninstallDeclinesAndRetryNeverReinstalls(t *testing.T) {
	dp, _ := autoInstalledTaxDeps(t)
	spec := basePluginSpec{CanonicalType: "tax", Market: autoTaxCountry}
	if err := addPendingBasePlugins(t.Context(), dp, []basePluginSpec{spec}); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := resolveAndInstallBasePlugin(t.Context(), dp, spec); err != nil {
		t.Fatalf("install: %v", err)
	}
	repo := data.NewPluginRepo(dp.Db)
	if active, _ := repo.PluginActive(t.Context(), autoTaxPluginID); !active {
		t.Fatal("precondition: installed")
	}

	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)
	uninstall := func(role string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/"+autoTaxPluginID+"/uninstall", nil)
		req = auth.WithUser(req, auth.User{ID: "u-" + role, Role: role})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := uninstall("cashier"); code != http.StatusForbidden {
		t.Fatalf("cashier uninstall = %d, want 403", code)
	}
	if active, _ := repo.PluginActive(t.Context(), autoTaxPluginID); !active {
		t.Fatal("a refused cashier request must change nothing")
	}
	if code := uninstall("manager"); code != http.StatusOK {
		t.Fatalf("manager uninstall = %d, want 200", code)
	}

	if got := taxAutoInstallDeclined(t.Context(), dp); got != autoTaxPluginID {
		t.Fatalf("declined = %q, want %q", got, autoTaxPluginID)
	}
	if pending, _ := loadPendingBasePlugins(t.Context(), dp); len(pending) != 0 {
		t.Fatalf("the pending spec must go with the uninstall, got %+v", pending)
	}
	if st := loadTaxAutoInstallStatus(t.Context(), dp); st != nil {
		t.Fatalf("the chip must clear, got %+v", st)
	}

	// Even a spec queued again (a stale race) does not bring it back, and
	// setup would not queue it again.
	if err := addPendingBasePlugins(t.Context(), dp, []basePluginSpec{spec}); err != nil {
		t.Fatalf("requeue: %v", err)
	}
	basePluginRetryTick(t.Context(), dp)
	if active, _ := repo.PluginActive(t.Context(), autoTaxPluginID); active {
		t.Fatal("StartBasePluginRetry must never reinstall a declined plugin")
	}
	if specs := taxPluginForCountry(t.Context(), dp, autoTaxCountry); len(specs) != 0 {
		t.Fatalf("setup must not queue a declined install again, got %+v", specs)
	}
}

// Dismissing the pending row is declining it too (same confirm, same
// record), so the retry never installs it.
func TestTaxAutoInstall_DismissPendingRowDeclines(t *testing.T) {
	dp, _ := autoInstalledTaxDeps(t)
	dp.Cfg.Marketplace.EndpointURL = unreachableURL()
	spec := basePluginSpec{CanonicalType: "tax", Market: autoTaxCountry}
	if err := addPendingBasePlugins(t.Context(), dp, []basePluginSpec{spec}); err != nil {
		t.Fatalf("queue: %v", err)
	}
	basePluginRetryTick(t.Context(), dp) // offline: stays pending, chip retrying/offline

	views := pendingBasePluginViews([]basePluginSpec{spec}, nil)
	if len(views) != 1 || !views[0].AutoTax || views[0].Market != autoTaxCountry {
		t.Fatalf("the row must carry its market and the auto flag: %+v", views)
	}

	declineTaxAutoInstall(t.Context(), dp, "")
	if pending, _ := loadPendingBasePlugins(t.Context(), dp); len(pending) != 0 {
		t.Fatalf("pending must be empty, got %+v", pending)
	}
	if got := taxAutoInstallDeclined(t.Context(), dp); got == "" {
		t.Fatal("a dismissed install must be recorded as declined")
	}
}

// §4: checkout never waits on the pending install: with the spec pending
// (offline), a line is rung up at the item's own rate, not blocked.
func TestTaxAutoInstall_CheckoutWorksWhileSpecPending(t *testing.T) {
	dp, _ := autoInstalledTaxDeps(t)
	dp.Cfg.Marketplace.EndpointURL = unreachableURL()
	if err := addPendingBasePlugins(t.Context(), dp, []basePluginSpec{{CanonicalType: "tax", Market: autoTaxCountry}}); err != nil {
		t.Fatalf("queue: %v", err)
	}
	basePluginRetryTick(t.Context(), dp)
	if pending, _ := loadPendingBasePlugins(t.Context(), dp); len(pending) != 1 {
		t.Fatalf("precondition: spec pending, got %+v", pending)
	}
	asker := &pluginTaxRateAsker{db: dp.Db}
	rate, ok, blocked := asker.AskTaxRateBP(pos.BasketLine{ItemID: "i1", TaxCodeID: "std", TaxRateBP: 2000}, "")
	if ok || rate != 0 || blocked {
		t.Fatalf("a pending install must not touch checkout: rate=%d ok=%v blocked=%v", rate, ok, blocked)
	}
}

// Settings > Plugins: the plugin says it was installed automatically, and
// its uninstall confirm says what changes.
func TestPluginsPage_AutoInstalledTaxPluginNoteAndConfirm(t *testing.T) {
	dp, _ := autoInstalledTaxDeps(t)
	if err := resolveAndInstallBasePlugin(t.Context(), dp, basePluginSpec{CanonicalType: "tax", Market: autoTaxCountry}); err != nil {
		t.Fatalf("install: %v", err)
	}
	mux := http.NewServeMux()
	registerPluginsPage(mux, dp)
	req := httptest.NewRequest(http.MethodGet, "/plugins", nil)
	req = auth.WithUser(req, auth.User{ID: "m1", Role: "manager"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /plugins = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Installed automatically for") {
		t.Fatalf("plugins page must carry the auto-install note:\n%s", body)
	}
	if !strings.Contains(body, "Eat-in sales will use each") {
		t.Fatalf("uninstall confirm must say what changes:\n%s", body)
	}
}

// Reviewer (ADR-0172 §5): a dismiss before the plugin id is known records
// only "market:<XX>". A stale spec (or an in-flight tick) holding the
// automatic spec must still never install it.
func TestTaxAutoInstall_MarketOnlyDeclineBlocksStaleSpec(t *testing.T) {
	dp, _ := autoInstalledTaxDeps(t)
	if err := dp.Settings.Set(t.Context(), common.KeyTaxAutoInstallDeclined, "market:"+autoTaxCountry); err != nil {
		t.Fatal(err)
	}
	spec := basePluginSpec{CanonicalType: "tax", Market: autoTaxCountry}
	if err := addPendingBasePlugins(t.Context(), dp, []basePluginSpec{spec}); err != nil {
		t.Fatal(err)
	}
	basePluginRetryTick(t.Context(), dp)
	if active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), autoTaxPluginID); active {
		t.Fatal("a declined automatic tax install must never run, whatever the declined record holds")
	}
	if pending, _ := loadPendingBasePlugins(t.Context(), dp); len(pending) != 0 {
		t.Fatalf("the stale spec must be dropped, got %+v", pending)
	}
}

// Reviewer (ADR-0172 §2.1): a PROMPTED install consents to the tile's
// listing, which names the country. A listing with no markets (every market)
// is never picked for it, even when it is the only tax listing.
func TestTaxPromptedInstall_NeverPicksNoMarketsListing(t *testing.T) {
	dp, mkt := autoInstalledTaxDeps(t)
	e := autoTaxCatalogEntry()
	e.Markets = nil
	mkt.setCatalog(e)
	err := resolveAndInstallBasePlugin(t.Context(), dp, basePluginSpec{CanonicalType: "tax", Market: autoTaxCountry, Prompted: true})
	if !errors.Is(err, errBasePluginNotPublished) {
		t.Fatalf("err = %v, want errBasePluginNotPublished", err)
	}
	if active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), autoTaxPluginID); active {
		t.Fatal("a no-markets listing must not be installed by a prompted tile")
	}
}
