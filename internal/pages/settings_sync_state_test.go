package pages

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#2948, part 1: the Settings handlers that persist through
// common.SaveState write through to the main till on an additional till,
// like #2791's single-key handlers. SaveState rewrites every key from the
// in-memory state, so only the fields the handler actually changed may
// travel -- a per-till change must never call the main till, and never push
// this till's (possibly stale) copy of the shop-wide keys over the main
// till's.

// A shop-wide SaveState field (idle lock) lands on the main till in one
// call and is mirrored, and the live state follows.
func TestSettingsWriteThrough_SaveStateFieldLandsOnMain(t *testing.T) {
	main := newSettingsSyncMain(t)
	mux, dp := newSettingsSyncReplica(t, main.srv.URL)

	rec := postForm(mux, "/api/settings/idle-lock", url.Values{"minutes": {"17"}}, &mgrUser)
	if rec.Code >= 300 {
		t.Fatalf("replica idle-lock = %d %q", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 1 {
		t.Fatalf("main till calls = %d, want 1", main.calls.Load())
	}
	if got := mustSetting(t, main.dp, common.KeyIdleLock); got != "17" {
		t.Fatalf("main till %s = %q, want 17 — the change must land on the main till", common.KeyIdleLock, got)
	}
	if got := mustSetting(t, dp, common.KeyIdleLock); got != "17" {
		t.Fatalf("replica %s = %q, want the mirrored 17", common.KeyIdleLock, got)
	}
	if got := dp.CurrentState().IdleLockMinutes; got != 17 {
		t.Fatalf("replica live idle lock = %d, want 17", got)
	}
	assertSettingSyncAudit(t, main.dp, "m1", common.KeyIdleLock, "17", "Till 2")
}

// Only the changed field travels: a stale shop-wide value in this till's
// in-memory state (here its currency) is never pushed over the main till's.
func TestSettingsWriteThrough_SaveStateSendsOnlyChangedKeys(t *testing.T) {
	main := newSettingsSyncMain(t)
	mux, dp := newSettingsSyncReplica(t, main.srv.URL)
	if err := main.dp.Settings.Set(t.Context(), common.KeyCurrency, "EUR"); err != nil {
		t.Fatal(err)
	}
	st := dp.CurrentState()
	st.Currency = "GBP" // stale on this till
	dp.SetState(st)

	rec := postForm(mux, "/api/settings/browsing-mode", url.Values{"mode": {common.BrowsingModeAllFilterChips}}, &mgrUser)
	if rec.Code >= 300 {
		t.Fatalf("replica browsing-mode = %d %q", rec.Code, rec.Body.String())
	}
	if got := mustSetting(t, main.dp, common.KeyBrowsingMode); got != common.BrowsingModeAllFilterChips {
		t.Fatalf("main till %s = %q, want %s", common.KeyBrowsingMode, got, common.BrowsingModeAllFilterChips)
	}
	if got := mustSetting(t, main.dp, common.KeyCurrency); got != "EUR" {
		t.Fatalf("main till %s = %q, want EUR — an unchanged field must not travel", common.KeyCurrency, got)
	}
}

// A per-till SaveState field saves locally and never calls the main till,
// even when it is unreachable.
func TestSettingsWriteThrough_SaveStatePerTillStaysLocal(t *testing.T) {
	mux, dp := newSettingsSyncReplica(t, deadPrimaryURL())
	t.Cleanup(func() { httpx.InitOSKMode("auto") })

	rec := postForm(mux, "/api/settings/osk", url.Values{"mode": {"on"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("replica osk = %d %q, want 204 — a per-till setting never needs the main till", rec.Code, rec.Body.String())
	}
	if got := mustSetting(t, dp, common.KeyOSK); got != "on" {
		t.Fatalf("replica %s = %q, want on", common.KeyOSK, got)
	}
}

// Main till unreachable: refused with the localized message, nothing
// written locally, live state unchanged.
func TestSettingsWriteThrough_SaveStateUnreachableRefuses(t *testing.T) {
	mux, dp := newSettingsSyncReplica(t, deadPrimaryURL())
	if err := dp.Settings.Set(t.Context(), common.KeyAllowNegativeInventory, "false"); err != nil {
		t.Fatal(err)
	}
	before := dp.CurrentState()

	rec := postForm(mux, "/api/settings/allow-negative-inventory", url.Values{"enabled": {"true"}}, &mgrUser)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), settingsUnreachableEN) {
		t.Fatalf("allow-negative-inventory = %d %q, want 502 with the unreachable message", rec.Code, rec.Body.String())
	}
	rec = postForm(mux, "/api/settings/save", url.Values{"currency": {"CHF"}}, &mgrUser)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), settingsUnreachableEN) {
		t.Fatalf("settings/save = %d %q, want 502 with the unreachable message", rec.Code, rec.Body.String())
	}
	if got := mustSetting(t, dp, common.KeyAllowNegativeInventory); got != "false" {
		t.Fatalf("refused change wrote locally: %s = %q", common.KeyAllowNegativeInventory, got)
	}
	for _, k := range []string{common.KeyCurrency, common.KeyCurrencyConfirmed} {
		if got := mustSetting(t, dp, k); got == "CHF" || (k == common.KeyCurrencyConfirmed && got == "true") {
			t.Fatalf("refused change wrote locally: %s = %q", k, got)
		}
	}
	after := dp.CurrentState()
	if after.AllowNegativeInventory != before.AllowNegativeInventory || after.Currency != before.Currency {
		t.Fatalf("refused change reached the live state: %+v -> %+v", before, after)
	}
}

// The store card: currency and an explicit language travel in ONE batch
// with the keys they imply (both confirmations and the bumped locale
// generation), so the main till applies them together.
func TestSettingsWriteThrough_StoreSaveImpliedKeysOneBatch(t *testing.T) {
	main := newSettingsSyncMain(t)
	mux, dp := newSettingsSyncReplica(t, main.srv.URL)
	for _, s := range []*common.Deps{main.dp, dp} {
		if err := s.Settings.Set(t.Context(), common.KeyLocaleGeneration, "3"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { httpx.InitCurrency("GBP"); httpx.SetDefaultLocale("en") })

	rec := postForm(mux, "/api/settings/save", url.Values{"currency": {"EUR"}, "locale": {"tr"}}, &mgrUser)
	if rec.Code >= 300 {
		t.Fatalf("replica settings/save = %d %q", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 1 {
		t.Fatalf("main till calls = %d, want 1 (one batch)", main.calls.Load())
	}
	for k, want := range map[string]string{
		common.KeyCurrency:          "EUR",
		common.KeyCurrencyConfirmed: "true",
		common.KeyLocale:            "tr",
		common.KeyLocaleConfirmed:   "true",
		common.KeyLocaleGeneration:  "4",
	} {
		if got := mustSetting(t, main.dp, k); got != want {
			t.Errorf("main till %s = %q, want %q", k, got, want)
		}
		if got := mustSetting(t, dp, k); got != want {
			t.Errorf("replica %s = %q, want the mirrored %q", k, got, want)
		}
	}
	if got := dp.CurrentState().Currency; got != "EUR" {
		t.Fatalf("replica live currency = %q, want EUR", got)
	}
}

// store.country from the store card on an additional till: pointed at the
// main till before any local fiscal reset, and never sent.
func TestSettingsWriteThrough_StoreSaveCountryPointsAtMainTill(t *testing.T) {
	main := newSettingsSyncMain(t)
	mux, dp := newSettingsSyncReplica(t, main.srv.URL)
	adm := auth.User{ID: "m1", Role: "admin"}
	before := mustSetting(t, dp, common.KeyCountry)

	rec := postForm(mux, "/api/settings/save", url.Values{"country": {"DE"}}, &adm)
	if rec.Code < 400 || !strings.Contains(rec.Body.String(), "Change this setting on the main till.") {
		t.Fatalf("settings/save country = %d %q, want the change-on-main-till message", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 0 {
		t.Fatalf("main till calls = %d, want 0", main.calls.Load())
	}
	if got := mustSetting(t, dp, common.KeyCountry); got != before {
		t.Fatalf("replica %s = %q, want unchanged %q", common.KeyCountry, got, before)
	}
}

// Review finding 1: the diff baseline is the snapshot the handler edited.
// An admin pull that replaces the live state between the handler's read and
// its save must not turn the pulled values into "changes" that push this
// till's pre-pull copies back over the main till.
func TestSettingsWriteThrough_SaveStateDiffsAgainstHandlerSnapshot(t *testing.T) {
	main := newSettingsSyncMain(t)
	_, dp := newSettingsSyncReplica(t, main.srv.URL)
	if err := main.dp.Settings.Set(t.Context(), common.KeyCurrency, "EUR"); err != nil {
		t.Fatal(err)
	}
	base := dp.CurrentState()
	base.Currency = "GBP"
	dp.SetState(base)
	st := base
	st.BrowsingMode = common.BrowsingModeAllFilterChips

	// The pull lands now: the live state has the main till's currency.
	pulled := dp.CurrentState()
	pulled.Currency = "EUR"
	dp.SetState(pulled)

	elev := elevationCheck{Outcome: allowed, ActorID: mgrUser.ID}
	if err := saveStateThrough(t.Context(), dp, elev, base, st, nil); err != nil {
		t.Fatalf("saveStateThrough: %v", err)
	}
	if got := mustSetting(t, main.dp, common.KeyBrowsingMode); got != common.BrowsingModeAllFilterChips {
		t.Fatalf("main till %s = %q, want %s", common.KeyBrowsingMode, got, common.BrowsingModeAllFilterChips)
	}
	if got := mustSetting(t, main.dp, common.KeyCurrency); got != "EUR" {
		t.Fatalf("main till %s = %q, want EUR — a value the pull changed is not this handler's change", common.KeyCurrency, got)
	}
}
