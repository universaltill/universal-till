package pages

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#3390: the remaining till Settings cards become editable from my.
// (set_till_setting) or are reported read-only. Each editable key is
// validated exactly as its local Settings form validates it, refused with a
// reason otherwise, and refused on an additional till (all are shop-wide).

// withRealLocales installs the shipped translator (en, ar, fa, tr) so the
// language keys validate against real installed locales, and restores the
// shop default locale and generation afterwards.
func withRealLocales(t *testing.T) {
	t.Helper()
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
	httpx.SetDefaultLocale("en")
	t.Cleanup(func() {
		httpx.SetDefaultLocale("en")
		httpx.SetLocaleGeneration(0)
	})
}

func TestCloudSetTillSetting_3390_ValidValuesApply(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	withRealLocales(t)
	ctx := t.Context()

	cases := []struct{ key, value, want string }{
		// Barcode types: the JSON id list the checklist stores, kept in
		// registry order, duplicates of the same id never stored twice.
		{data.BarcodeEnabledSymbologiesKey, `["CODE128","EAN13"]`, `["EAN13","CODE128"]`},
		{data.BarcodeEnabledSymbologiesKey, `[" EAN8 "]`, `["EAN8"]`},
		// The two "1"/"0" checkboxes: any strconv.ParseBool spelling, like
		// the local form; stored as the form stores it.
		{data.CatalogImportBarcodeFromSKUDefaultKey, "true", "1"},
		{data.CatalogImportBarcodeFromSKUDefaultKey, "0", "0"},
		{data.CatalogPrePackUnitPriceEnabledKey, "1", "1"},
		{data.CatalogPrePackUnitPriceEnabledKey, "false", "0"},
		// Stock tracking lives in RuntimeState: stored as SaveState does.
		{common.KeyAllowNegativeInventory, "1", "true"},
		{common.KeyAllowNegativeInventory, "false", "false"},
		// Invoice seller details: free text, trimmed, blank clears.
		{keyInvoiceSellerName, " Corner Shop Ltd ", "Corner Shop Ltd"},
		{keyInvoiceSellerAddress, "1 High Street", "1 High Street"},
		{keyInvoiceSellerVATNo, "GB123456789", "GB123456789"},
		{keyInvoiceSellerVATNo, "", ""},
		// Auto-lock: 0..480 minutes, like /api/settings/idle-lock.
		{common.KeyIdleLock, "0", "0"},
		{common.KeyIdleLock, "480", "480"},
		{common.KeyIdleLock, " 15 ", "15"},
		{common.KeyKioskPaymentMode, common.KioskPaymentModeCounter, common.KioskPaymentModeCounter},
		{common.KeyKioskPaymentMode, common.KioskPaymentModeKiosk, common.KioskPaymentModeKiosk},
		// Staff languages: matched like the form ("TR" = tr), stored in
		// installed order, comma separated.
		{common.KeyStaffLocales, "tr,EN", "en,tr"},
		{common.KeyStaffLocales, "en", "en"},
		// Shop language last: it moves the default the staff check uses.
		{common.KeyLocale, "tr", "tr"},
	}
	for _, c := range cases {
		msg, err := cloudSetTillSetting(ctx, dp, nil, c.key, c.value)
		if err != nil {
			t.Fatalf("%s=%q: %v", c.key, c.value, err)
		}
		if !strings.Contains(msg, c.key) {
			t.Fatalf("%s: result %q should name the key", c.key, msg)
		}
		got, _, err := dp.Settings.Get(ctx, c.key)
		if err != nil || got != c.want {
			t.Fatalf("%s=%q: stored %q (err %v), want %q", c.key, c.value, got, err, c.want)
		}
	}
}

func TestCloudSetTillSetting_3390_InvalidValuesRefusedWithReason(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	withRealLocales(t)
	ctx := t.Context()

	cases := []struct{ key, value, reason string }{
		{data.BarcodeEnabledSymbologiesKey, `[]`, "at least one"},
		{data.BarcodeEnabledSymbologiesKey, `[""]`, "at least one"},
		{data.BarcodeEnabledSymbologiesKey, `["EAN13","QR"]`, "unknown barcode type"},
		{data.BarcodeEnabledSymbologiesKey, `EAN13`, "JSON list"},
		{data.BarcodeEnabledSymbologiesKey, `["EAN13","EAN13"]`, "more than once"},
		{data.CatalogImportBarcodeFromSKUDefaultKey, "yes", "true or false"},
		{data.CatalogPrePackUnitPriceEnabledKey, "", "true or false"},
		{common.KeyAllowNegativeInventory, "maybe", "true or false"},
		{common.KeyIdleLock, "-1", "0 and 480"},
		{common.KeyIdleLock, "481", "0 and 480"},
		{common.KeyIdleLock, "ten", "0 and 480"},
		{common.KeyKioskPaymentMode, "card", "kiosk or counter"},
		{common.KeyKioskPaymentMode, "", "kiosk or counter"},
		{common.KeyLocale, "xx", "not installed"},
		{common.KeyLocale, "", "not installed"},
		{common.KeyStaffLocales, "", "at least one"},
		{common.KeyStaffLocales, "en,xx", "not installed"},
		// The shop default (en) can't be left out.
		{common.KeyStaffLocales, "tr", "default language"},
	}
	for _, c := range cases {
		before, beforeOK, _ := dp.Settings.Get(ctx, c.key)
		_, err := cloudSetTillSetting(ctx, dp, nil, c.key, c.value)
		if err == nil {
			t.Fatalf("%s=%q: want a refusal, got nil", c.key, c.value)
		}
		if !strings.Contains(err.Error(), c.reason) {
			t.Fatalf("%s=%q: refusal %q should say %q", c.key, c.value, err, c.reason)
		}
		after, afterOK, _ := dp.Settings.Get(ctx, c.key)
		if after != before || afterOK != beforeOK {
			t.Fatalf("%s=%q: refused value was written (%q → %q)", c.key, c.value, before, after)
		}
	}
}

// The read-only keys of the decision table are never remote-writable, even
// though they are reported.
func TestCloudSetTillSetting_3390_ReadOnlyKeysRefused(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	for _, key := range []string{common.KeyReportRetentionMode, common.KeyCurrency, common.KeyShopType} {
		if allowedRemoteTillSettingKeys[key] {
			t.Fatalf("%s is read-only from my. (ut-docs#3390) but whitelisted", key)
		}
		if _, err := cloudSetTillSetting(ctx, dp, nil, key, "x"); err == nil {
			t.Fatalf("%s: want a refusal", key)
		}
	}
}

// Every new key is shop-wide: an additional till refuses it with nothing
// written (its main till's next admin pull would revert a local write).
func TestCloudSetTillSetting_3390_AdditionalTillRefuses(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	withRealLocales(t)
	ctx := t.Context()
	followMainTill(t, dp)
	valid := map[string]string{
		data.BarcodeEnabledSymbologiesKey:          `["EAN13"]`,
		data.CatalogImportBarcodeFromSKUDefaultKey: "1",
		data.CatalogPrePackUnitPriceEnabledKey:     "1",
		common.KeyAllowNegativeInventory:           "true",
		keyInvoiceSellerName:                       "Shop",
		keyInvoiceSellerAddress:                    "Street",
		keyInvoiceSellerVATNo:                      "GB1",
		common.KeyIdleLock:                         "5",
		common.KeyKioskPaymentMode:                 "counter",
		common.KeyLocale:                           "tr",
		common.KeyStaffLocales:                     "en,tr",
	}
	for key, v := range valid {
		if data.SettingScope(key) == data.SettingPerTill {
			t.Fatalf("%s: expected shop-wide", key)
		}
		before, _, _ := dp.Settings.Get(ctx, key)
		if _, err := cloudSetTillSetting(ctx, dp, nil, key, v); err == nil {
			t.Fatalf("%s on an additional till: want a refusal", key)
		}
		if after, _, _ := dp.Settings.Get(ctx, key); after != before {
			t.Fatalf("%s refused but written (%q → %q)", key, before, after)
		}
	}
}

// Side effects the local forms trigger happen for a remote write too.
func TestCloudSetTillSetting_3390_SideEffects(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	withRealLocales(t)
	ctx := t.Context()
	rederived := 0
	rederive := func(context.Context) { rederived++ }

	// Barcode set: the cached set is dropped, so scans see the new set.
	repo := data.NewSettingsRepo(dp.Db)
	if _, err := repo.EnabledBarcodeSymbologies(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := cloudSetTillSetting(ctx, dp, rederive, data.BarcodeEnabledSymbologiesKey, `["CODE39"]`); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.EnabledBarcodeSymbologies(ctx); !slices.Equal(got, []string{"CODE39"}) {
		t.Fatalf("enabled set after a remote write = %v, want [CODE39]", got)
	}

	// Shop language: marked an explicit choice (no country re-derive may
	// override it) and every browser's ut_lang override is retired — the
	// local Language card's two side effects.
	if err := dp.Settings.Set(ctx, common.KeyLocaleGeneration, "4"); err != nil {
		t.Fatal(err)
	}
	if _, err := cloudSetTillSetting(ctx, dp, rederive, common.KeyLocale, "fa"); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := dp.Settings.Get(ctx, common.KeyLocaleConfirmed); v != "true" {
		t.Fatalf("%s = %q, want true", common.KeyLocaleConfirmed, v)
	}
	if v, _, _ := dp.Settings.Get(ctx, common.KeyLocaleGeneration); v != "5" {
		t.Fatalf("%s = %q, want 5 (bumped)", common.KeyLocaleGeneration, v)
	}
	// The state-backed keys reach the live state through the re-derive.
	if rederived != 2 {
		t.Fatalf("rederive called %d times, want 2", rederived)
	}
}

// The idle-lock and stock switches live in RuntimeState: through the real
// re-derive the live state, and the auth service's idle window, follow.
func TestCloudSetTillSetting_3390_StateKeysTakeLiveEffect(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	ctx := t.Context()
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatal(err)
	}
	rederive := newRederiveSettings(d, true, i18n)
	for key, v := range map[string]string{
		common.KeyIdleLock:               "7",
		common.KeyAllowNegativeInventory: "true",
		common.KeyKioskPaymentMode:       "counter",
	} {
		if _, err := cloudSetTillSetting(ctx, d, rederive, key, v); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	st := d.CurrentState()
	if st.IdleLockMinutes != 7 || !st.AllowNegativeInventory || st.KioskPaymentMode != "counter" {
		t.Fatalf("live state not re-derived: idle=%d negInv=%v kiosk=%q", st.IdleLockMinutes, st.AllowNegativeInventory, st.KioskPaymentMode)
	}
}

// The heartbeat reports every editable key AND the read-only ones, with the
// value the till actually applies (a default where nothing is stored), so
// my. can show the real state.
func TestRemoteTillSettingsReport_3390_NewKeysAndReadOnly(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	withRealLocales(t)
	ctx := t.Context()
	for k, v := range map[string]string{
		common.KeyReportRetentionMode: "till",
		common.KeyShopType:            "cafe",
		keyInvoiceSellerName:          "Corner Shop",
	} {
		if err := dp.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	got := remoteTillSettingsReport(ctx, dp)
	for key := range allowedRemoteTillSettingKeys {
		if _, ok := got[key]; !ok {
			t.Fatalf("editable key %q missing from the report", key)
		}
	}
	for _, key := range reportedReadOnlyTillSettingKeys {
		if _, ok := got[key]; !ok {
			t.Fatalf("read-only key %q missing from the report", key)
		}
	}
	if len(got) != len(allowedRemoteTillSettingKeys)+len(reportedReadOnlyTillSettingKeys) {
		t.Fatalf("report carries %d keys, want only the editable + read-only ones: %v", len(got), got)
	}
	var ids []string
	if err := json.Unmarshal([]byte(got[data.BarcodeEnabledSymbologiesKey]), &ids); err != nil || !slices.Equal(ids, data.DefaultEnabledBarcodeSymbologyIDs()) {
		t.Fatalf("barcode types report %q, want the default set", got[data.BarcodeEnabledSymbologiesKey])
	}
	want := map[string]string{
		data.CatalogImportBarcodeFromSKUDefaultKey: "0",
		data.CatalogPrePackUnitPriceEnabledKey:     "0",
		common.KeyAllowNegativeInventory:           "false",
		common.KeyIdleLock:                         strconv.Itoa(common.DefaultIdleLockMinutes),
		common.KeyKioskPaymentMode:                 "kiosk",
		common.KeyLocale:                           "en",
		common.KeyStaffLocales:                     "en",
		common.KeyCurrency:                         "GBP",
		common.KeyReportRetentionMode:              "till",
		common.KeyShopType:                         "cafe",
		keyInvoiceSellerName:                       "Corner Shop",
		keyInvoiceSellerVATNo:                      "",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("report[%s] = %q, want %q", k, got[k], v)
		}
	}
}

// The languages the till can offer travel with the report, so my. lists
// exactly what is installed on the till.
func TestRemoteTillSettingOptions_3390(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	withRealLocales(t)
	opts := remoteTillSettingOptions()
	for _, key := range []string{common.KeyLocale, common.KeyStaffLocales} {
		if !slices.Equal(opts[key], httpx.AvailableLocales()) {
			t.Fatalf("options[%s] = %v, want %v", key, opts[key], httpx.AvailableLocales())
		}
	}
	extra := buildCloudHooks(dp, nil).DeviceExtra(t.Context())
	if _, ok := extra["till_setting_options"].(map[string][]string); !ok {
		t.Fatalf("till_setting_options missing from DeviceExtra: %#v", extra["till_setting_options"])
	}
}

// tillSettingAudits returns the cloud_till_setting_set audit rows, oldest
// first, with the row's actor/entity folded into the payload map.
func tillSettingAudits(t *testing.T, dp *common.Deps) []map[string]any {
	t.Helper()
	rows, err := dp.Db.QueryContext(t.Context(),
		`SELECT actor_id, entity_type, entity_id, data_json FROM audit_log WHERE action = 'cloud_till_setting_set' ORDER BY rowid`)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var actor, typ, id, js string
		if err := rows.Scan(&actor, &typ, &id, &js); err != nil {
			t.Fatalf("scan audit: %v", err)
		}
		row := map[string]any{}
		if err := json.Unmarshal([]byte(js), &row); err != nil {
			t.Fatalf("audit payload %q: %v", js, err)
		}
		row["_actor"], row["_entity_type"], row["_entity_id"] = actor, typ, id
		out = append(out, row)
	}
	return out
}

// Review of ut-docs#3390: a remote set_till_setting changes shop
// configuration with no operator at the till, so — like rename_till and the
// quick-button layout — every applied one leaves an audit row (system actor,
// entity "settings", the key, old → new, via cloud), for the original keys,
// the ut-docs#3390 keys and the shop-language path alike. A refused one
// writes nothing.
func TestCloudSetTillSetting_3390_AppliedIsAuditedRefusedIsNot(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	withRealLocales(t)
	ctx := t.Context()

	refused := []struct{ key, value string }{
		{"not.a.remote.key", "x"},
		{common.KeyIdleLock, "999"},
		{common.KeyCurrency, "EUR"},
		{common.KeyLocale, "xx"},
	}
	for _, r := range refused {
		if _, err := cloudSetTillSetting(ctx, dp, nil, r.key, r.value); err == nil {
			t.Fatalf("%s=%q: want refusal", r.key, r.value)
		}
	}
	if got := tillSettingAudits(t, dp); len(got) != 0 {
		t.Fatalf("refused settings wrote audit rows: %+v", got)
	}

	applied := []struct{ key, value, old, new string }{
		{keyReceiptFooter, "Thanks!", "", "Thanks!"},
		{keyReceiptFooter, "Cheers", "Thanks!", "Cheers"},
		{common.KeyIdleLock, " 15 ", "", "15"},
		{common.KeyLocale, "tr", "", "tr"},
	}
	for _, a := range applied {
		if _, err := cloudSetTillSetting(ctx, dp, nil, a.key, a.value); err != nil {
			t.Fatalf("%s=%q: %v", a.key, a.value, err)
		}
	}
	got := tillSettingAudits(t, dp)
	if len(got) != len(applied) {
		t.Fatalf("audit rows = %d, want %d: %+v", len(got), len(applied), got)
	}
	for i, a := range applied {
		row := got[i]
		if row["_actor"] != "system" || row["_entity_type"] != "settings" || row["_entity_id"] != a.key ||
			row["via"] != "cloud" || row["key"] != a.key || row["new"] != a.new {
			t.Fatalf("audit row %d = %+v, want key %s new %q via cloud", i, row, a.key, a.new)
		}
		// The shop language's old value depends on the test DB's seed, so
		// only assert "old" where this test controlled it.
		if a.key != common.KeyLocale && row["old"] != a.old {
			t.Fatalf("audit row %d old = %v, want %q", i, row["old"], a.old)
		}
	}
}
