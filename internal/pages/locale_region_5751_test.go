package pages

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#5751: a defaulted GB till carries store.locale=en-US (the compiled
// UT_DEFAULT_LOCALE), so dates print month-first. These tests pin the boot
// backfill (R1) and the Language card's region-keeping (R2).

type syncBuf struct{ sb strings.Builder }

func (b *syncBuf) Write(p []byte) (int, error) { return b.sb.Write(p) }

// seedRegionTill persists country+locale (and optionally the confirmed flag)
// and points the process default locale at it, restoring on cleanup.
func seedRegionTill(t *testing.T, country, locale string, confirmed bool) *common.Deps {
	t.Helper()
	dp := newBasePluginTestDeps(t)
	prev := httpx.DefaultLocale()
	t.Cleanup(func() { httpx.SetDefaultLocale(prev) })
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = country; s.Locale = locale })
	if err := common.SaveState(t.Context(), dp.Settings, dp.CurrentState()); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	if confirmed {
		if err := dp.Settings.Set(t.Context(), common.KeyLocaleConfirmed, "true"); err != nil {
			t.Fatal(err)
		}
	}
	httpx.SetDefaultLocale(locale)
	return dp
}

func storedLocale(t *testing.T, d *common.Deps) string {
	t.Helper()
	v, _, err := d.Settings.Get(t.Context(), common.KeyLocale)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRegionBackfill_DefaultedGBTillMovesToEnGB(t *testing.T) {
	dp := seedRegionTill(t, "GB", "en-US", false)
	genBefore, _, _ := dp.Settings.Get(t.Context(), common.KeyLocaleGeneration)

	backfillRegionalLocaleFromCountry(t.Context(), dp)

	if got := storedLocale(t, dp); got != "en-GB" {
		t.Fatalf("store.locale = %q, want en-GB", got)
	}
	if got := dp.CurrentState().Locale; got != "en-GB" {
		t.Fatalf("state locale = %q, want en-GB", got)
	}
	if got := httpx.DefaultLocale(); got != "en-GB" {
		t.Fatalf("DefaultLocale = %q, want en-GB", got)
	}
	if v, ok, _ := dp.Settings.Get(t.Context(), common.KeyLocaleConfirmed); ok && v == "true" {
		t.Fatal("a derivation must not set locale_confirmed")
	}
	if genAfter, _, _ := dp.Settings.Get(t.Context(), common.KeyLocaleGeneration); genAfter != genBefore {
		t.Fatalf("generation %q -> %q; a derivation must not bump it", genBefore, genAfter)
	}
	d := time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC) // fixed-date:ok formatting only, no clock read
	if got := httpx.FormatDateLatin(d, "en"); got != "06/01/2026" {
		t.Fatalf("date = %q, want 06/01/2026", got)
	}
}

func TestRegionBackfill_ConfirmedLocaleUntouched(t *testing.T) {
	dp := seedRegionTill(t, "GB", "en-US", true)
	backfillRegionalLocaleFromCountry(t.Context(), dp)
	if got := storedLocale(t, dp); got != "en-US" {
		t.Fatalf("confirmed locale changed to %q", got)
	}
	if got := dp.CurrentState().Locale; got != "en-US" {
		t.Fatalf("state locale = %q", got)
	}
}

func TestRegionBackfill_CountryDefaultAlreadyApplied_NoWrite(t *testing.T) {
	var buf syncBuf
	restore := logging.CaptureForTest(&buf)
	defer restore()

	dp := seedRegionTill(t, "US", "en-US", false)
	backfillRegionalLocaleFromCountry(t.Context(), dp)
	if strings.Contains(buf.sb.String(), "locale region backfill") {
		t.Fatalf("US/en-US logged a backfill: %s", buf.sb.String())
	}

	dp2 := seedRegionTill(t, "GB", "en-US", false)
	backfillRegionalLocaleFromCountry(t.Context(), dp2)
	if !strings.Contains(buf.sb.String(), "locale region backfill") {
		t.Fatalf("first GB run did not log: %q", buf.sb.String())
	}
	buf.sb.Reset()
	backfillRegionalLocaleFromCountry(t.Context(), dp2)
	if out := buf.sb.String(); strings.Contains(out, "locale region backfill") || strings.Contains(out, "settings.set") {
		t.Fatalf("second run wrote or logged a backfill: %q", out)
	}
	if got := storedLocale(t, dp2); got != "en-GB" {
		t.Fatalf("store.locale = %q", got)
	}
}

func TestRegionBackfill_DifferentLanguageNeverSwitches(t *testing.T) {
	for _, c := range []struct{ country, locale string }{{"DE", "en-US"}, {"GB", "de-DE"}} {
		dp := seedRegionTill(t, c.country, c.locale, false)
		backfillRegionalLocaleFromCountry(t.Context(), dp)
		if got := storedLocale(t, dp); got != c.locale {
			t.Fatalf("%s/%s: locale became %q", c.country, c.locale, got)
		}
	}
}

func TestRegionBackfill_RTLSameLanguageKeepsDirectionAndDigits(t *testing.T) {
	for _, c := range []struct{ country, from, want string }{{"AE", "ar", "ar-AE"}, {"IR", "fa", "fa-IR"}} {
		dp := seedRegionTill(t, c.country, c.from, false)
		rtlBefore, digitsBefore := httpx.IsRTL(c.from), httpx.LocalizeDigits("123", c.from)
		backfillRegionalLocaleFromCountry(t.Context(), dp)
		if got := storedLocale(t, dp); got != c.want {
			t.Fatalf("%s: locale = %q, want %q", c.country, got, c.want)
		}
		if httpx.IsRTL(c.want) != rtlBefore || httpx.LocalizeDigits("123", c.want) != digitsBefore {
			t.Fatalf("%s: direction or digits changed", c.country)
		}
	}
}

func TestRegionBackfill_UnderscoreAndCase(t *testing.T) {
	var buf syncBuf
	restore := logging.CaptureForTest(&buf)
	defer restore()
	for _, l := range []string{"en_US", "EN-us"} {
		dp := seedRegionTill(t, "US", l, false)
		backfillRegionalLocaleFromCountry(t.Context(), dp)
		if got := storedLocale(t, dp); got != l {
			t.Fatalf("US/%s rewritten to %q", l, got)
		}
	}
	if out := buf.sb.String(); strings.Contains(out, "locale region backfill") {
		t.Fatal("a case/underscore-only difference caused a write")
	}
	dp := seedRegionTill(t, "GB", "en_US", false)
	backfillRegionalLocaleFromCountry(t.Context(), dp)
	if got := storedLocale(t, dp); got != "en-GB" {
		t.Fatalf("GB/en_US -> %q, want en-GB", got)
	}
}

func TestRegionBackfill_NoCountryRowOrBlankDefault(t *testing.T) {
	dp := seedRegionTill(t, "ZZ", "en-US", false)
	if err := data.NewCountrySettingsRepo(dp.Db).Upsert(t.Context(), data.CountrySetting{
		Code: "ZZ", NameKey: "setup.country.other", Currency: "GBP", ArchiveMinDays: 3650,
	}); err != nil {
		t.Fatalf("upsert custom country: %v", err)
	}
	backfillRegionalLocaleFromCountry(t.Context(), dp)
	if got := storedLocale(t, dp); got != "en-US" {
		t.Fatalf("blank default changed locale to %q", got)
	}
	dp = seedRegionTill(t, "QQ", "en-US", false) // no row at all
	backfillRegionalLocaleFromCountry(t.Context(), dp)
	if got := storedLocale(t, dp); got != "en-US" {
		t.Fatalf("missing row changed locale to %q", got)
	}
	dp = seedRegionTill(t, "IN", "en-US", false)
	backfillRegionalLocaleFromCountry(t.Context(), dp)
	if got := storedLocale(t, dp); got != "en-IN" {
		t.Fatalf("IN/en-US -> %q, want en-IN", got)
	}
}

func TestRegionBackfill_FollowsOperatorEditedCountryRow(t *testing.T) {
	dp := seedRegionTill(t, "GB", "en-US", false)
	repo := data.NewCountrySettingsRepo(dp.Db)
	cs, ok, err := repo.Get(t.Context(), "GB")
	if err != nil || !ok {
		t.Fatalf("get GB: %v ok=%v", err, ok)
	}
	cs.DefaultLocale = "en-IE"
	if err := repo.Upsert(t.Context(), cs); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	backfillRegionalLocaleFromCountry(t.Context(), dp)
	if got := storedLocale(t, dp); got != "en-IE" {
		t.Fatalf("locale = %q, want en-IE", got)
	}
}

func TestRegionBackfill_SurvivesProductionBootOrdering(t *testing.T) {
	dp := seedRegionTill(t, "GB", "en-US", false)
	settings.NewStore(dp.Db).LoadRuntimeConfig(t.Context(), dp.Cfg)
	backfillRegionalLocaleFromCountry(t.Context(), dp)
	backfillLocaleConfirmedForDivergedPendingTills(t.Context(), dp)
	if got := storedLocale(t, dp); got != "en-GB" {
		t.Fatalf("locale = %q, want en-GB", got)
	}
	if v, _, _ := dp.Settings.Get(t.Context(), common.KeyLocaleConfirmed); v == "true" {
		t.Fatal("boot ordering confirmed the locale")
	}
}

func TestRegionBackfill_PersistFailureLeavesStateUncommitted(t *testing.T) {
	dp := seedRegionTill(t, "GB", "en-US", false)
	// Tester fix (ut-docs#5751): closing the DB made the first read fail, so
	// the function returned before the persist step and this test passed even
	// with commit-before-persist. Fail only the store.locale write instead.
	for _, op := range []string{"INSERT", "UPDATE"} {
		if _, err := dp.Db.Exec(`CREATE TRIGGER fail_locale_` + op + ` BEFORE ` + op + ` ON settings WHEN NEW.key = '` + common.KeyLocale + `' BEGIN SELECT RAISE(ABORT, 'test: locale write refused'); END`); err != nil {
			t.Fatalf("plant write-failure trigger: %v", err)
		}
	}
	backfillRegionalLocaleFromCountry(t.Context(), dp)
	if got := storedLocale(t, dp); got != "en-US" {
		t.Fatalf("store.locale = %q despite the refused write", got)
	}
	if got := dp.CurrentState().Locale; got != "en-US" {
		t.Fatalf("state committed despite failed persist: %q", got)
	}
	if got := httpx.DefaultLocale(); got != "en-US" {
		t.Fatalf("DefaultLocale moved despite failed persist: %q", got)
	}
}

func TestShopLocaleForPick(t *testing.T) {
	for _, c := range []struct {
		picked, current, countryDefault string
		confirmed                       bool
		want                            string
	}{
		{"en", "en-US", "en-US", true, "en-US"},
		{"en", "en-GB", "en-GB", true, "en-GB"},
		{"en", "en-US", "en-GB", true, "en-US"},
		{"en", "en-US", "en-GB", false, "en-GB"},
		{"en", "tr", "en-GB", true, "en-GB"},
		{"en", "tr", "", true, "en"},
		{"tr", "en-US", "en-US", true, "tr"},
		{"en", "en-US", "de-DE", false, "en-US"},
		{"ar", "en-US", "ar-AE", true, "ar-AE"},
		{"en-GB", "en-US", "en-US", true, "en-GB"},
		{"en", "", "en-US", false, "en-US"},
	} {
		if got := shopLocaleForPick(c.picked, c.current, c.countryDefault, c.confirmed); got != c.want {
			t.Errorf("shopLocaleForPick(%q,%q,%q,%v) = %q, want %q", c.picked, c.current, c.countryDefault, c.confirmed, got, c.want)
		}
	}
}

func TestSaveSettings_EnglishPickKeepsShopRegion(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	prev := httpx.DefaultLocale()
	t.Cleanup(func() { httpx.SetDefaultLocale(prev) })
	d.UpdateState(func(s *common.RuntimeState) { s.Country = "US"; s.Locale = "en-US" })
	if err := common.SaveState(t.Context(), d.Settings, d.CurrentState()); err != nil {
		t.Fatal(err)
	}
	if err := d.Settings.Set(t.Context(), common.KeyLocaleConfirmed, "true"); err != nil {
		t.Fatal(err)
	}
	httpx.SetDefaultLocale("en-US")

	if rec := postForm(mux, "/api/settings/save", url.Values{"locale": {"en"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("save = %d", rec.Code)
	}
	if got := storedLocale(t, d); got != "en-US" {
		t.Fatalf("store.locale = %q, want en-US", got)
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyLocaleConfirmed); v != "true" {
		t.Fatalf("confirmed = %q", v)
	}
	if g, _, _ := d.Settings.Get(t.Context(), common.KeyLocaleGeneration); g == "" {
		t.Fatal("generation not bumped")
	}
	body := renderSettingsWith(t, mux)
	if !strings.Contains(body, `<option value="en" selected`) {
		t.Fatalf("Language card does not pre-select English for en-US")
	}
}

func TestSaveSettings_DifferentLanguagePickStoredAsGiven(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	prev := httpx.DefaultLocale()
	t.Cleanup(func() { httpx.SetDefaultLocale(prev) })
	d.UpdateState(func(s *common.RuntimeState) { s.Country = "US"; s.Locale = "en-US" })
	if err := common.SaveState(t.Context(), d.Settings, d.CurrentState()); err != nil {
		t.Fatal(err)
	}
	if rec := postForm(mux, "/api/settings/save", url.Values{"locale": {"tr"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("save = %d", rec.Code)
	}
	if got := storedLocale(t, d); got != "tr" {
		t.Fatalf("store.locale = %q, want tr", got)
	}
}

func TestCloudSetShopLocale_KeepsRegion(t *testing.T) {
	d := seedRegionTill(t, "GB", "en-GB", true)
	msg, err := cloudSetShopLocale(context.Background(), d, nil, "en")
	if err != nil {
		t.Fatal(err)
	}
	if got := storedLocale(t, d); got != "en-GB" {
		t.Fatalf("store.locale = %q, want en-GB", got)
	}
	if !strings.Contains(msg, "en-GB") {
		t.Fatalf("message %q does not name the stored tag", msg)
	}
}
