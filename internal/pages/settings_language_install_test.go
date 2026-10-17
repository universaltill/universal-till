package pages

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/netreach"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#4477 (ADR-0160 §6): Settings → Language offers the catalog's
// language packs, and picking one that isn't installed queues it on the
// ut-docs#591 pending list (the background retry installs it) while the
// Language card's chip waits for it and then applies it.

var deLanguageSpec = basePluginSpec{CanonicalType: "language", Locale: "de"}

// newSettingsLanguageDeps is a real-DB till whose catalog offers the de pack.
func newSettingsLanguageDeps(t *testing.T) (*http.ServeMux, *common.Deps, *fakeMarketplace) {
	t.Helper()
	resetLangCatalogForTest(t)
	mux, d := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-lang-de": "ut-plugin-language-de"})
	mkt.setCatalog(deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.0.0"))
	d.Cfg.Marketplace = mkt.config()
	return mux, d, mkt
}

// makeLocaleInstalled stands in for the de pack's locales/ reaching the
// translator (plugins.Manager.syncLocales after an install).
func makeLocaleInstalled(t *testing.T, locale string) {
	t.Helper()
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	i18n.SetPluginLocales(config.PluginLocales{FirstPartyStale: map[string]map[string]string{
		locale: {"nav.sell": "Verkaufen"},
	}})
	httpx.InitI18n(i18n, "en")
}

func getLanguageChip(t *testing.T, mux *http.ServeMux, u *auth.User) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/ui/settings/language-install", nil)
	if u != nil {
		req = auth.WithUser(req, *u)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/settings/language-install = %d", rec.Code)
	}
	return rec.Body.String()
}

func pendingSwitch(t *testing.T, d *common.Deps) string {
	t.Helper()
	v, _, err := d.Settings.Get(t.Context(), common.KeyLocaleSwitchPending)
	if err != nil {
		t.Fatalf("read %s: %v", common.KeyLocaleSwitchPending, err)
	}
	locale, _ := parseLanguageSwitchPending(v)
	return locale
}

func TestSettingsLanguageListsCatalogLanguagesForDownload(t *testing.T) {
	mux, d, mkt := newSettingsLanguageDeps(t)

	// GET /settings never waits on the catalog: a cold cache renders the
	// installed locales only and refreshes in the background (review of
	// ut-docs#4477 — every Settings tab renders this page).
	body := getSettingsAs(t, mux, mgrUser)
	if strings.Contains(body, `data-testid="settings-language-downloads"`) {
		t.Fatal("a cold catalog cache must not be fetched in the render itself")
	}
	d.WaitForAsyncWork()
	if hits := mkt.catalogHitsFor("language"); hits != 1 {
		t.Fatalf("background catalog refresh: %d language-catalog fetches, want 1", hits)
	}

	body = getSettingsAs(t, mux, mgrUser)
	if !strings.Contains(body, `data-testid="settings-language-downloads"`) {
		t.Fatal("Settings → Language has no download group although the catalog offers de")
	}
	if !strings.Contains(body, `<option value="de" data-install-locale="de">Deutsch</option>`) {
		t.Fatal("the download group should offer de under its native name")
	}
	if !strings.Contains(body, httpx.T("en", "settings.language.downloads_on_select")) {
		t.Error("the card should say that a download language installs in the background")
	}
}

// Offline: installed locales only, even with a catalog cached from earlier.
func TestSettingsLanguageOfflineListsInstalledOnly(t *testing.T) {
	mux, d, _ := newSettingsLanguageDeps(t)
	if langs := settingsInstallableLanguages(t.Context(), d); len(langs) != 1 {
		t.Fatalf("priming the catalog cache: %+v", langs)
	}
	d.NetReach = unreachableMonitor(t)

	body := getSettingsAs(t, mux, mgrUser)
	if strings.Contains(body, `data-testid="settings-language-downloads"`) {
		t.Fatal("offline, the language list must show installed locales only")
	}
	if !strings.Contains(body, `<option value="en"`) {
		t.Fatal("offline, the installed locales must still be listed")
	}
}

// unreachableMonitor is a netreach.Monitor whose probe has failed.
func unreachableMonitor(t *testing.T) *netreach.Monitor {
	t.Helper()
	m := netreach.New(netreach.Options{
		Endpoint: "https://cloud.example.invalid/api",
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("offline")
		})},
	})
	deadline := time.Now().Add(5 * time.Second)
	for m.Status() != netreach.Unreachable {
		if time.Now().After(deadline) {
			t.Fatal("netreach never reported Unreachable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return m
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The whole flow: pick → queued (no locale change yet) → chip "installing"
// → the background retry installs the pack → chip "ready" carrying the
// card's own save → that save applies the language and clears the switch.
func TestSettingsLanguagePickQueuesInstallThenChipAppliesIt(t *testing.T) {
	mux, d, _ := newSettingsLanguageDeps(t)

	rec := postForm(mux, "/api/settings/save", url.Values{"locale": {"de"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("save locale=de = %d %s", rec.Code, rec.Body.String())
	}
	if got := d.CurrentState().Locale; got == "de" {
		t.Fatal("the shop's language must not change before the pack has arrived")
	}
	pending, err := loadPendingBasePlugins(t.Context(), d)
	if err != nil || len(pending) != 1 || pending[0] != deLanguageSpec {
		t.Fatalf("pending list = %+v (err %v), want the de language spec", pending, err)
	}
	if got := pendingSwitch(t, d); got != "de" {
		t.Fatalf("pending switch = %q, want de", got)
	}

	body := getSettingsAs(t, mux, mgrUser)
	if !strings.Contains(body, `hx-get="/ui/settings/language-install"`) {
		t.Fatal("the Language card should mount its install chip while a switch is pending")
	}
	chip := getLanguageChip(t, mux, &mgrUser)
	if !strings.Contains(chip, `data-state="installing"`) || !strings.Contains(chip, `hx-trigger="every 3s"`) {
		t.Fatalf("chip while queued = %s, want a polling installing chip", chip)
	}
	if !strings.Contains(chip, "Deutsch") {
		t.Error("the chip should name the language in its own name")
	}

	// The existing background retry installs the pack.
	basePluginRetryTick(t.Context(), d)
	if active, _ := data.NewPluginRepo(d.Db).PluginActive(t.Context(), "ut-plugin-language-de"); !active {
		t.Fatal("the retry tick should have installed the de pack")
	}
	makeLocaleInstalled(t, "de")

	chip = getLanguageChip(t, mux, &mgrUser)
	if !strings.Contains(chip, `data-state="ready"`) || !strings.Contains(chip, `hx-post="/api/settings/save"`) ||
		!strings.Contains(chip, `name="locale" value="de"`) || !strings.Contains(chip, `hx-trigger="load"`) {
		t.Fatalf("chip once installed = %s, want a one-shot save of locale=de", chip)
	}
	if strings.Contains(chip, "every 3s") {
		t.Error("the ready chip must not keep polling")
	}

	// What the ready chip posts.
	rec = postForm(mux, "/api/settings/save", url.Values{"locale": {"de"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("apply locale=de = %d", rec.Code)
	}
	if got := d.CurrentState().Locale; got != "de" {
		t.Fatalf("shop language after apply = %q, want de", got)
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyLocaleConfirmed); v != "true" {
		t.Error("the applied language is the operator's explicit choice (locale_confirmed)")
	}
	if got := pendingSwitch(t, d); got != "" {
		t.Fatalf("pending switch after apply = %q, want cleared", got)
	}
	if chip := getLanguageChip(t, mux, &mgrUser); strings.TrimSpace(chip) != "" {
		t.Fatalf("chip after apply = %q, want empty", chip)
	}
}

// A locale the catalog doesn't offer is ignored, as before: nothing queued.
func TestSettingsLanguagePickNotInCatalogQueuesNothing(t *testing.T) {
	mux, d, _ := newSettingsLanguageDeps(t)
	if rec := postForm(mux, "/api/settings/save", url.Values{"locale": {"xx"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("save = %d", rec.Code)
	}
	if pending, _ := loadPendingBasePlugins(t.Context(), d); len(pending) != 0 {
		t.Fatalf("pending list = %+v, want nothing queued for a locale the catalog doesn't offer", pending)
	}
	if got := pendingSwitch(t, d); got != "" {
		t.Fatalf("pending switch = %q, want none", got)
	}
}

// Offline, a pick from a stale page queues nothing either.
func TestSettingsLanguagePickOfflineQueuesNothing(t *testing.T) {
	mux, d, _ := newSettingsLanguageDeps(t)
	d.NetReach = unreachableMonitor(t)
	if rec := postForm(mux, "/api/settings/save", url.Values{"locale": {"de"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("save = %d", rec.Code)
	}
	if got := pendingSwitch(t, d); got != "" {
		t.Fatalf("pending switch = %q, want none while offline", got)
	}
}

// Choosing an installed language while a download is pending settles it:
// the operator's newer choice wins and the chip goes away.
func TestSettingsLanguageInstalledPickClearsPendingSwitch(t *testing.T) {
	mux, d, _ := newSettingsLanguageDeps(t)
	if rec := postForm(mux, "/api/settings/save", url.Values{"locale": {"de"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("save de = %d", rec.Code)
	}
	if rec := postForm(mux, "/api/settings/save", url.Values{"locale": {"tr"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("save tr = %d", rec.Code)
	}
	if got := pendingSwitch(t, d); got != "" {
		t.Fatalf("pending switch = %q, want cleared by the installed pick", got)
	}
	if got := d.CurrentState().Locale; got != "tr" {
		t.Fatalf("shop language = %q, want tr", got)
	}
}

// The pack left the queue without arriving (no listing any more, or removed
// under Settings → Data): the chip says so once and stops.
func TestSettingsLanguageChipReportsPackThatNeverArrived(t *testing.T) {
	mux, d, _ := newSettingsLanguageDeps(t)
	if rec := postForm(mux, "/api/settings/save", url.Values{"locale": {"de"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("save de = %d", rec.Code)
	}
	if err := dismissPendingBasePlugin(t.Context(), d, "language", "de"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	chip := getLanguageChip(t, mux, &mgrUser)
	if !strings.Contains(chip, `data-state="unavailable"`) || strings.Contains(chip, "hx-trigger") {
		t.Fatalf("chip = %s, want a non-polling unavailable chip", chip)
	}
	if got := pendingSwitch(t, d); got != "" {
		t.Fatalf("pending switch = %q, want cleared after reporting it", got)
	}
	if chip := getLanguageChip(t, mux, &mgrUser); strings.TrimSpace(chip) != "" {
		t.Fatalf("second chip render = %q, want empty", chip)
	}
}

// The chip is for whoever may open Settings; a cashier gets nothing.
func TestSettingsLanguageChipHiddenFromCashier(t *testing.T) {
	mux, d, _ := newSettingsLanguageDeps(t)
	if err := d.Settings.Set(context.Background(), common.KeyLocaleSwitchPending, "de"); err != nil {
		t.Fatal(err)
	}
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}
	if chip := getLanguageChip(t, mux, &cashier); strings.TrimSpace(chip) != "" {
		t.Fatalf("cashier chip = %q, want empty", chip)
	}
}

// The pending switch is this till's own, like the queue it annotates.
func TestLocaleSwitchPendingKeyIsPerTill(t *testing.T) {
	if data.SettingScope(common.KeyLocaleSwitchPending) != data.SettingPerTill {
		t.Fatalf("%s must be per-till (it rides the setup.pending_base_plugins prefix)", common.KeyLocaleSwitchPending)
	}
}

// A pack that arrives long after the pick (the till was offline for a day)
// must not switch the whole shop when someone next opens Settings: the pick
// is forgotten and the language just sits in the list as installed.
func TestSettingsLanguageStaleSwitchIsNotApplied(t *testing.T) {
	mux, d, _ := newSettingsLanguageDeps(t)
	makeLocaleInstalled(t, "de")
	picked := time.Now().Add(-languageSwitchApplyWindow - time.Minute).Unix()
	if err := d.Settings.Set(t.Context(), common.KeyLocaleSwitchPending, "de@"+strconv.FormatInt(picked, 10)); err != nil {
		t.Fatal(err)
	}
	if chip := getLanguageChip(t, mux, &mgrUser); strings.TrimSpace(chip) != "" {
		t.Fatalf("chip for a pick older than the apply window = %s, want nothing", chip)
	}
	if got := pendingSwitch(t, d); got != "" {
		t.Fatalf("pending switch = %q, want forgotten", got)
	}
	// A value without a pick time is never applied by itself either.
	if err := d.Settings.Set(t.Context(), common.KeyLocaleSwitchPending, "de"); err != nil {
		t.Fatal(err)
	}
	if chip := getLanguageChip(t, mux, &mgrUser); strings.TrimSpace(chip) != "" {
		t.Fatalf("chip for a pick with no time = %s, want nothing", chip)
	}
}
