package pages

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5349 slice 1: the whitelist is the registry, set_till_setting has
// compare-and-set, and the report is registry-driven.

func TestAllowedRemoteTillSettingKeys_IsTheRegistry_5349(t *testing.T) {
	want := data.RemoteWritableKeys()
	if len(allowedRemoteTillSettingKeys) != len(want) {
		t.Fatalf("whitelist has %d keys, registry %d", len(allowedRemoteTillSettingKeys), len(want))
	}
	for k := range want {
		if !allowedRemoteTillSettingKeys[k] {
			t.Errorf("%s is remote-writable in the registry but not whitelisted", k)
		}
	}
	// The registry's names are the constants the settings pages write.
	for _, k := range []string{keyPrinterReceiptPolicy, keyReceiptHeader1, keyReceiptHeader2, keyReceiptHeader3, keyReceiptFooter,
		keyInvoiceSellerName, keyInvoiceSellerAddress, keyInvoiceSellerVATNo, common.KeyKioskIdleReset, common.KeyBrowsingMode,
		common.KeyIdleLock, common.KeyKioskPaymentMode, common.KeyLocale, common.KeyStaffLocales, common.KeyAllowNegativeInventory,
		common.KeyReportRetentionMode, common.KeyCurrency, common.KeyShopType, common.KeyStoreName, common.KeyAutoRegisterOptIn} {
		if _, ok := data.LookupAgentSetting(k); !ok {
			t.Errorf("%s has no registry entry", k)
		}
	}
}

// The registry's bounds and options are what the validators enforce.
func TestRegistryBoundsMatchValidators_5349(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	for _, s := range data.AgentSettings {
		if !s.RemoteWritable {
			continue
		}
		switch s.Type {
		case data.SettingTypeInt:
			for _, bad := range []int{s.Min - 1, s.Max + 1} {
				if _, err := cloudSetTillSetting(ctx, dp, nil, s.Key, strconv.Itoa(bad), nil); err == nil {
					t.Errorf("%s=%d outside the registry bounds was accepted", s.Key, bad)
				}
			}
			for _, ok := range []int{s.Min, s.Max} {
				if _, err := cloudSetTillSetting(ctx, dp, nil, s.Key, strconv.Itoa(ok), nil); err != nil {
					t.Errorf("%s=%d inside the registry bounds refused: %v", s.Key, ok, err)
				}
			}
		case data.SettingTypeEnum:
			if len(s.Options) == 0 {
				continue // dynamic (installed languages)
			}
			for _, o := range s.Options {
				if _, err := cloudSetTillSetting(ctx, dp, nil, s.Key, o, nil); err != nil {
					t.Errorf("%s=%q is a registry option but was refused: %v", s.Key, o, err)
				}
			}
			if _, err := cloudSetTillSetting(ctx, dp, nil, s.Key, "not-an-option", nil); err == nil {
				t.Errorf("%s accepted a value outside its options", s.Key)
			}
		}
	}
}

func TestCloudSetTillSetting_ExpectCompareAndSet_5349(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	str := func(s string) *string { return &s }
	key := common.KeyKioskIdleReset
	get := func() string { v, _, _ := dp.Settings.Get(ctx, key); return v }

	// Unset, expect "" (unset): applies.
	if _, err := cloudSetTillSetting(ctx, dp, nil, key, "30", str("")); err != nil {
		t.Fatalf("expect unset on an unset key: %v", err)
	}
	if get() != "30" {
		t.Fatalf("stored %q", get())
	}
	// Stale expect: refused, nothing written.
	_, err := cloudSetTillSetting(ctx, dp, nil, key, "40", str("20"))
	if err == nil || !strings.HasPrefix(err.Error(), "changed since") {
		t.Fatalf("stale expect: err %v, want a 'changed since' refusal", err)
	}
	if get() != "30" {
		t.Fatalf("refused write changed the value to %q", get())
	}
	// Expect "" but a value is stored: refused.
	if _, err := cloudSetTillSetting(ctx, dp, nil, key, "40", str("")); err == nil || !strings.HasPrefix(err.Error(), "changed since") {
		t.Fatalf("expect unset on a set key: %v", err)
	}
	// Matching expect applies; absent expect is the old unconditional write.
	if _, err := cloudSetTillSetting(ctx, dp, nil, key, "40", str("30")); err != nil || get() != "40" {
		t.Fatalf("matching expect: err %v stored %q", err, get())
	}
	if _, err := cloudSetTillSetting(ctx, dp, nil, key, "50", nil); err != nil || get() != "50" {
		t.Fatalf("no expect: err %v stored %q", err, get())
	}
	// The proposer saw the report, which shows the default for an unset key:
	// that counts as unchanged.
	dp2 := newCloudSyncTestDeps(t)
	if _, err := cloudSetTillSetting(ctx, dp2, nil, key, "10", str("60")); err != nil {
		t.Fatalf("expect = the reported default of an unset key: %v", err)
	}
}

func TestCloudSetTillSetting_ExpectOnBoolUsesStoredSpelling_5349(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	s := func(v string) *string { return &v }
	if _, err := cloudSetTillSetting(ctx, dp, nil, data.CatalogPrePackUnitPriceEnabledKey, "true", s("1")); err == nil {
		t.Fatal("expect 1 on an unset (=0) toggle must be refused")
	}
	if _, err := cloudSetTillSetting(ctx, dp, nil, data.CatalogPrePackUnitPriceEnabledKey, "true", s("0")); err != nil {
		t.Fatalf("expect the default spelling 0: %v", err)
	}
}

func TestTillSettingsReport_SecretsAreSetNotSetNeverValues_5349(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	entries := []data.AgentSetting{
		{Key: "x.api_key", Type: data.SettingTypeText, Tier: data.SettingTierSecret, Secret: true, ReadValue: true},
		{Key: "x.token", Type: data.SettingTypeText, Tier: data.SettingTierSecret, Secret: true},
		{Key: "x.visible", Type: data.SettingTypeText, Tier: data.SettingTierNormal, ReadValue: true},
		{Key: "x.never", Type: data.SettingTypeText, Tier: data.SettingTierNever, ReadValue: true},
		{Key: "x.notier", Type: data.SettingTypeText, ReadValue: true},
	}
	_ = dp.Settings.Set(ctx, "x.api_key", "hunter2-super-secret")
	_ = dp.Settings.Set(ctx, "x.visible", "shown")
	_ = dp.Settings.Set(ctx, "x.never", "n")
	_ = dp.Settings.Set(ctx, "x.notier", "n")
	got := tillSettingsReportFor(ctx, dp, entries)
	if got["x.api_key"] != data.SettingSecretSet || got["x.token"] != data.SettingSecretNotSet {
		t.Fatalf("secrets must report set / not set, got %v", got)
	}
	for k, v := range got {
		if strings.Contains(v, "hunter2") {
			t.Fatalf("secret value leaked under %s", k)
		}
	}
	if got["x.visible"] != "shown" {
		t.Fatalf("visible key: %v", got)
	}
	if _, ok := got["x.never"]; ok {
		t.Fatal("a never-tier key must not be reported")
	}
	if _, ok := got["x.notier"]; ok {
		t.Fatal("an entry without a tier must not be reported (fail closed)")
	}
}

func TestTillSettingsReport_RealRegistryHasNoSecretValues_5349(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	got := remoteTillSettingsReport(t.Context(), dp)
	for _, s := range data.AgentSettings {
		v, in := got[s.Key]
		if s.Secret && in && v != data.SettingSecretSet && v != data.SettingSecretNotSet {
			t.Errorf("secret %s reported a value", s.Key)
		}
		if (s.Tier == data.SettingTierNever || s.Tier == data.SettingTierSecret && !s.Secret) && in {
			t.Errorf("%s (tier %s) must not be reported", s.Key, s.Tier)
		}
		if !s.RemoteWritable && !s.ReadValue && in {
			t.Errorf("%s is neither writable nor readable but is reported", s.Key)
		}
	}
}

// The registry's hand-copied defaults and bounds are the ones the code uses
// (the expect rule "unset reads as the default" and the artefact rely on them).
func TestRegistryDefaultsAndBoundsMatchCode_5349(t *testing.T) {
	check := func(key, wantDefault string, min, max int) {
		t.Helper()
		s, ok := data.LookupAgentSetting(key)
		if !ok {
			t.Fatalf("%s missing", key)
		}
		if s.Default != wantDefault {
			t.Errorf("%s: registry default %q, code default %q", key, s.Default, wantDefault)
		}
		if s.Type == data.SettingTypeInt && (s.Min != min || s.Max != max) {
			t.Errorf("%s: registry bounds %d..%d, local handler %d..%d", key, s.Min, s.Max, min, max)
		}
	}
	check(common.KeyIdleLock, strconv.Itoa(common.DefaultIdleLockMinutes), 0, 480)
	check(common.KeyKioskIdleReset, strconv.Itoa(common.DefaultKioskIdleResetSeconds), 0, 600)
	check(common.KeyKioskPaymentMode, common.KioskPaymentModeKiosk, 0, 0)
	check(common.KeyBrowsingMode, common.DefaultBrowsingMode, 0, 0)
	check(data.OrderTypePromptModeKey, data.OrderTypePromptModeTop, 0, 0)
}

// The read-only reported keys are pinned: widening what rides on every
// heartbeat is a deliberate act (ut-docs#5349 added the last four).
func TestReportedReadOnlyTillSettingKeys_Pinned_5349(t *testing.T) {
	want := []string{
		"marketplace.auto_register_opt_in", "payments.default_method", common.KeyReportRetentionMode,
		"marketplace.telemetry_opt_in", common.KeyStoreName, common.KeyCurrency, common.KeyShopType,
	}
	got := append([]string(nil), reportedReadOnlyTillSettingKeys...)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("read-only reported keys %v, want %v", got, want)
	}
}

func TestTillSettingsReport_FixupsNeverOverwriteASecret_5349(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	_ = dp.Settings.Set(ctx, common.KeyCurrency, "GBP")
	got := tillSettingsReportFor(ctx, dp, []data.AgentSetting{
		{Key: common.KeyCurrency, Type: data.SettingTypeText, Tier: data.SettingTierSecret, Secret: true},
		{Key: "x.hidden", Type: data.SettingTypeText, Tier: data.SettingTierNever, Secret: true},
	})
	if got[common.KeyCurrency] != data.SettingSecretSet {
		t.Fatalf("a fix-up overwrote the secret marker: %v", got)
	}
	if _, ok := got["x.hidden"]; ok {
		t.Fatalf("a never-tier key must stay invisible even with Secret set: %v", got)
	}
}

func TestTillSettingsReport_ReadOnlyCutKeepsUTF8_5349(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	_ = dp.Settings.Set(ctx, "x.long", strings.Repeat("ğ", 150)) // 300 bytes
	got := tillSettingsReportFor(ctx, dp, []data.AgentSetting{
		{Key: "x.long", Type: data.SettingTypeText, Tier: data.SettingTierNormal, ReadValue: true},
	})
	if v := got["x.long"]; !utf8.ValidString(v) || len(v) > 200 || len(v) < 190 {
		t.Fatalf("cut value invalid: %d bytes valid=%v", len(v), utf8.ValidString(v))
	}
}
