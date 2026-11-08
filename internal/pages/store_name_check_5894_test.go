package pages

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5894: every till writer of store.name runs config.CheckStoreName.

// Names the old Normalize-only / placeholder-only paths stored.
func badStoreNames5894() map[string]string {
	return map[string]string{
		"rlo":                "Shop‮",
		"variation selector": "Shop\U000E0100",
		"line separator":     "Shop x",
	}
}

func invalidCharsMsg5894() string {
	return html.EscapeString(httpx.T("en", "settings.store_name.error_invalid_chars"))
}

// W1: the Shop name card, as a cashier who would otherwise be asked for an
// approver PIN - validation runs before the elevation gate.
func TestStoreNameSave_RefusesSingleLineBreakers(t *testing.T) {
	for label, bad := range badStoreNames5894() {
		t.Run(label, func(t *testing.T) {
			mux, _, d := newFullAuthDeps(t)
			seedElevationUsers(t, d)
			if err := d.Settings.Set(t.Context(), common.KeyStoreName, "Kept Name"); err != nil {
				t.Fatal(err)
			}
			cashier := auth.User{ID: "cashier-x", Role: "cashier"}
			rec := postForm(mux, "/api/settings/store-name", url.Values{"store_name": {bad}}, &cashier)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Fatalf("Content-Type = %q, want text/html", ct)
			}
			if !strings.Contains(rec.Body.String(), invalidCharsMsg5894()) {
				t.Fatalf("body lacks the invalid-characters message: %s", rec.Body.String())
			}
			if got := storeNameSetting(t, d); got != "Kept Name" {
				t.Fatalf("store.name = %q, want unchanged", got)
			}
			if n := auditActions(t, d, "store_name_changed"); n != 0 {
				t.Fatalf("%d audit rows for a refused save", n)
			}
		})
	}
}

// W2: the raw editor refuses the same names.
func TestSettingsUpsert_StoreNameSingleLineBreakersRefused_5894(t *testing.T) {
	for label, bad := range badStoreNames5894() {
		t.Run(label, func(t *testing.T) {
			mux, _, d := newFullAuthDeps(t)
			if err := d.Settings.Set(t.Context(), "store.name", sn5874Seed); err != nil {
				t.Fatal(err)
			}
			rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.name"}, "value": {bad}}, &cashUser)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			tcWantMsg(t, rec.Body.String())
			if got := settingValue(t, d, "store.name"); got != sn5874Seed {
				t.Fatalf("store.name = %q, want unchanged", got)
			}
		})
	}
}

// W3: the wizard's final POST.
func TestSetupWizardRefusesInvalidCharStoreName(t *testing.T) {
	for label, bad := range badStoreNames5894() {
		t.Run(label, func(t *testing.T) {
			mux, svc, d := newFullAuthDeps(t)
			rec := postForm(mux, "/api/setup", url.Values{
				"pin": {"2468"}, "pin_confirm": {"2468"},
				"country": {"GB"}, "currency": {"GBP"}, "tax_rate_pct": {"20"},
				"store_name": {bad}, "till_name": {"Front counter"},
			}, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("code=%d, want a 200 wizard re-render", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, invalidCharsMsg5894()) {
				t.Errorf("re-render lacks the invalid-characters message: %s", body)
			}
			if !strings.Contains(body, "step: 4,") {
				t.Error("re-render must open on the shop-name step (4)")
			}
			if strings.Contains(body, `value="`+bad+`"`) {
				t.Error("re-render echoes the refused name")
			}
			if !strings.Contains(body, `value="Front counter"`) {
				t.Error("re-render must echo the till name")
			}
			if sessionCookie(rec) != "" {
				t.Fatal("a refused submit must not sign anyone in")
			}
			if fb, _ := svc.NeedsFirstBoot(t.Context()); !fb {
				t.Fatal("must stay first boot")
			}
			if done, _, _ := d.Settings.Get(t.Context(), "setup.completed"); done == "true" {
				t.Fatal("setup must not complete")
			}
			if tn, ok, _ := d.Settings.Get(t.Context(), "till.name"); ok && tn == "Front counter" {
				t.Fatal("till name saved on a refused submit")
			}
		})
	}
}

// W4: the bare first-boot POST.
func TestAuthSetupRefusesInvalidCharStoreName(t *testing.T) {
	for label, bad := range badStoreNames5894() {
		t.Run(label, func(t *testing.T) {
			withOSLocale(t, "", "")
			mux, svc, d := newAuthTestMux(t)
			rec := postForm(mux, "/api/auth/setup", url.Values{"pin": {"2468"}, "pin_confirm": {"2468"}, "store_name": {bad}}, nil)
			body := rec.Body.String()
			if rec.Code != http.StatusOK || !strings.Contains(body, `action="/api/auth/setup"`) {
				t.Fatalf("want the setup form re-rendered, got code=%d", rec.Code)
			}
			if !strings.Contains(body, invalidCharsMsg5894()) {
				t.Errorf("body lacks the invalid-characters message")
			}
			if strings.Contains(body, `value="`+bad+`"`) {
				t.Error("refused name echoed back")
			}
			if first, err := svc.NeedsFirstBoot(t.Context()); err != nil || !first {
				t.Fatalf("must stay first boot (first=%v err=%v)", first, err)
			}
			if _, ok, _ := d.Settings.Get(t.Context(), "store.name"); ok {
				if v, _, _ := d.Settings.Get(t.Context(), "store.name"); v == bad {
					t.Fatal("refused name stored")
				}
			}
		})
	}
}

// R4: a cloud shop name the check refuses is asked locally, never prefilled.
func TestCloudShopNameWithHiddenCharsAsksForALocalName(t *testing.T) {
	for label, bad := range badStoreNames5894() {
		t.Run(label, func(t *testing.T) {
			mux, d, stub, nonce := cloudWizard(t)
			stub.set(200, strings.Replace(cloudStoreAnswer, "Cloud Cafe", bad, 1))
			doCloudPair(mux, nonce, nil)
			if html := getSetup(mux, "?lang=en", "en").Body.String(); strings.Contains(html, bad) {
				t.Error("the refused cloud name is shown")
			}
			rec := finalCloudPost(mux, nonce, func(f url.Values) { f.Set("store_name", "Local Name") })
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("code=%d body=%.300s", rec.Code, rec.Body.String())
			}
			if v, _, _ := d.Settings.Get(t.Context(), "store.name"); v != "Local Name" {
				t.Errorf("store.name = %q, want the locally typed name", v)
			}
		})
	}
	// A valid cloud name is still taken.
	mux, d, stub, nonce := cloudWizard(t)
	stub.set(200, strings.Replace(cloudStoreAnswer, "Cloud Cafe", "Corner Cafe", 1))
	doCloudPair(mux, nonce, nil)
	if rec := finalCloudPost(mux, nonce, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d", rec.Code)
	}
	if v, _, _ := d.Settings.Get(t.Context(), "store.name"); v != "Corner Cafe" {
		t.Errorf("store.name = %q, want the cloud's Corner Cafe", v)
	}
}

// W6: the main till's settings-apply refuses what the card refuses.
func TestSyncSettingsApply_RefusesInvalidStoreName(t *testing.T) {
	cases := []struct{ label, value, code string }{
		{"rlo", "Shop‮", "store_name_invalid_chars"},
		{"variation selector", "Shop\U000E0100", "store_name_invalid_chars"},
		{"placeholder", "My Store", "store_name_required"},
		{"too long", strings.Repeat("é", 81), "store_name_too_long"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			m := newSyncSettingsTestDeps(t)
			rec := postSyncSettingsApply(m.mux, settingsApplyBody(t, syncSettingsApplyRequest{Settings: one(common.KeyStoreName, tc.value), ActorID: "m-1"}), syncSettingsBearer)
			wantSyncSettingsError(t, rec, http.StatusBadRequest, tc.code)
			if got := mustSetting(t, m.dp, common.KeyStoreName); got == tc.value {
				t.Fatal("refused name stored")
			}
		})
	}
	// A batch with a bad shop name is refused whole.
	m := newSyncSettingsTestDeps(t)
	rec := postSyncSettingsApply(m.mux, settingsApplyBody(t, syncSettingsApplyRequest{
		Settings: []syncSettingKV{{Key: common.KeyStoreName, Value: "Shop‮"}, {Key: "payments.default_method", Value: "card"}},
		ActorID:  "m-1",
	}), syncSettingsBearer)
	wantSyncSettingsError(t, rec, http.StatusBadRequest, "store_name_invalid_chars")
	if got := mustSetting(t, m.dp, "payments.default_method"); got != "" {
		t.Fatalf("payments.default_method = %q, want nothing written", got)
	}
	assertNoSettingSyncAudit5894(t, m.dp)
}

func assertNoSettingSyncAudit5894(t *testing.T, dp *common.Deps) {
	t.Helper()
	if n := auditActions(t, dp, "setting_changed_via_till"); n != 0 {
		t.Fatalf("%d settings audit rows after a refusal", n)
	}
}

func TestSyncSettingsApply_StoreNameNormalised(t *testing.T) {
	m := newSyncSettingsTestDeps(t)
	rec := postSyncSettingsApply(m.mux, settingsApplyBody(t, syncSettingsApplyRequest{Settings: one(common.KeyStoreName, "  Corner Cafe  "), ActorID: "m-1"}), syncSettingsBearer)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %q", rec.Code, rec.Body.String())
	}
	if got := mustSetting(t, m.dp, common.KeyStoreName); got != "Corner Cafe" {
		t.Fatalf("stored %q, want trimmed", got)
	}
	if got := answeredSettings(t, rec)[common.KeyStoreName]; got != "Corner Cafe" {
		t.Fatalf("answered %q, want trimmed", got)
	}
}

// W6, additional till, first line: refused locally, the main till never called.
func TestSettingsWriteThrough_StoreNameRefusedLocallyBeforeForwarding(t *testing.T) {
	main := newSettingsSyncMain(t)
	mux, dp := newSettingsSyncReplica(t, main.srv.URL)
	if err := dp.Settings.Set(t.Context(), common.KeyStoreName, "Kept Name"); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/settings/store-name", url.Values{"store_name": {"Shop\U000E0100"}}, &mgrUser)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), invalidCharsMsg5894()) {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 0 {
		t.Fatalf("main till called %d times for a refused name", main.calls.Load())
	}
	if got := mustSetting(t, dp, common.KeyStoreName); got != "Kept Name" {
		t.Fatalf("local store.name = %q", got)
	}
}

// W6, additional till, second line (version skew): the main till's
// store_name_* refusal reaches the operator translated.
func TestSettingsWriteThrough_MainStoreNameRefusalTranslated(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"data":null,"error":{"code":"store_name_invalid_chars","message":"x"}}`))
	}))
	t.Cleanup(stub.Close)
	mux, dp := newSettingsSyncReplica(t, stub.URL)
	if err := dp.Settings.Set(t.Context(), common.KeyStoreName, "Kept Name"); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/settings/store-name", url.Values{"store_name": {"Corner Cafe"}}, &mgrUser)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), invalidCharsMsg5894()) {
		t.Fatalf("code=%d body=%s, want 409 with the invalid-characters text", rec.Code, rec.Body.String())
	}
	if got := mustSetting(t, dp, common.KeyStoreName); got != "Kept Name" {
		t.Fatalf("local store.name = %q", got)
	}
}

func TestSettingsSyncMessageKey_StoreNameCodes(t *testing.T) {
	for code, want := range map[string]string{
		"store_name_invalid_chars": "settings.store_name.error_invalid_chars",
		"store_name_too_long":      "settings.store_name.error_too_long",
		"store_name_required":      "settings.store_name.error_required",
	} {
		if got := settingsSyncMessageKey(&errSettingsSync{Code: code}); got != want {
			t.Errorf("settingsSyncMessageKey(%q) = %q, want %q", code, got, want)
		}
	}
}

// W7: a cloud set_setting is applied unchanged (the #5860 policy: never
// refuse at directive time what my. accepted). Follow-up F1 flips this on
// purpose, together with my.
func TestCloudSetSetting_StoreNameAppliedUnchanged_5894(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	hooks := buildCloudHooks(d, nil)
	const odd = "Shop\U000E0100"
	if _, err := hooks.SetSetting(t.Context(), "store.name", odd); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if got := storeNameSetting(t, d); got != odd {
		t.Fatalf("store.name = %q, want %q byte-identical", got, odd)
	}
}
