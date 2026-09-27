package pages

import (
	"context"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#1068: store.country changed after setup must queue the new
// country's base plugins exactly as the wizard does — through every writer
// of store.country, not only POST /api/setup.

func wantPending(t *testing.T, d *common.Deps, want ...basePluginSpec) {
	t.Helper()
	got, err := loadPendingBasePlugins(t.Context(), d)
	if err != nil {
		t.Fatalf("loadPendingBasePlugins: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("pending = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pending = %+v, want %+v", got, want)
		}
	}
}

// drainBasePluginRetryNudge empties the package-level nudge buffer, so a
// token left by an earlier test cannot make a retry loop skip its initial
// delay.
func drainBasePluginRetryNudge() {
	for {
		select {
		case <-basePluginRetryNudge:
		default:
			return
		}
	}
}

var (
	specDE = basePluginSpec{CanonicalType: "language", Locale: "de"}
	specES = basePluginSpec{CanonicalType: "language", Locale: "es"}
)

func TestUpsertCountryChange_QueuesBasePlugins(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	// A spec another step already queued must survive (ut-docs#1110's merge rule).
	if err := savePendingBasePlugins(t.Context(), d, []basePluginSpec{specES}); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.country"}, "value": {"DE"}}, &mgrUser)
	if rec.Code != 204 {
		t.Fatalf("upsert = %d body=%s", rec.Code, rec.Body.String())
	}
	wantPending(t, d, specES, specDE)
}

func TestSaveCountryChange_QueuesBasePlugins(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	rec := postForm(mux, "/api/settings/save", url.Values{"country": {"ES"}}, &mgrUser)
	if rec.Code != 204 {
		t.Fatalf("save = %d body=%s", rec.Code, rec.Body.String())
	}
	wantPending(t, d, specES)
}

func TestCloudSetSettingCountryChange_QueuesBasePlugins(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	if _, err := buildCloudHooks(d, nil).SetSetting(t.Context(), common.KeyCountry, "DE"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	wantPending(t, d, specDE)
}

// Re-saving the country the shop already has (any case) is not a change: a
// language pack the merchant dismissed from Settings must not come back.
func TestCountryUnchanged_QueuesNothing(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	if err := d.Settings.Set(t.Context(), common.KeyCountry, "DE"); err != nil {
		t.Fatal(err)
	}
	d.UpdateState(func(s *common.RuntimeState) { s.Country = "DE" })

	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.country"}, "value": {"de"}}, &mgrUser); rec.Code != 204 {
		t.Fatalf("upsert = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec := postForm(mux, "/api/settings/save", url.Values{"country": {"DE"}}, &mgrUser); rec.Code != 204 {
		t.Fatalf("save = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := buildCloudHooks(d, nil).SetSetting(t.Context(), common.KeyCountry, " de "); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	wantPending(t, d)
}

func TestCountryChangeToUnmappedCountry_QueuesNothing(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.country"}, "value": {"GB"}}, &mgrUser); rec.Code != 204 {
		t.Fatalf("upsert = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, ok, _ := d.Settings.Get(t.Context(), common.KeyPendingBasePlugins); ok {
		t.Fatal("expected no pending list for a country with no base plugins")
	}
}

// The queue helper nudges the background retry, so a till that is online
// installs the new country's pack right away rather than on the next
// 5-minute tick — or, just after boot, the 30s initial delay.
func TestQueueBasePluginsForCountryChange_NudgesRetryLoop(t *testing.T) {
	dp := newBasePluginTestDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-lang-de": "ut-plugin-language-de"})
	mkt.setCatalog(deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	drainBasePluginRetryNudge()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	StartBasePluginRetry(ctx, dp, &wg)

	queueBasePluginsForCountryChange(t.Context(), dp, "DE")

	deadline := time.Now().Add(10 * time.Second)
	for {
		active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), "ut-plugin-language-de")
		if active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the nudged retry loop did not install the DE language pack")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
