package registrydesc

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ---- ut-docs#6823 slice 2 + the shop configuration descriptor (design-B RC1) ----

// Every registry entry is classified shop-wide or per-till: the descriptor's
// scope is derived from data.SettingScope, never hand-kept a second time.
func TestSettingsRegistry_EveryEntryHasAScope(t *testing.T) {
	for _, s := range data.AgentSettings {
		switch DescriptorScope(s) {
		case SettingDescriptorScopeShop, SettingDescriptorScopeDevice:
		default:
			t.Errorf("%s: SettingScope is unclassified; add its prefix to ShopWide/PerTillSettingPrefixes", s.Key)
		}
	}
}

func TestSettingsRegistry_DescriptorScopeFollowsSettingScope(t *testing.T) {
	if got := DescriptorScope(data.AgentSetting{Key: "store.name"}); got != "shop" {
		t.Errorf("store.name scope = %q, want shop", got)
	}
	if got := DescriptorScope(data.AgentSetting{Key: "printer.mode"}); got != "device" {
		t.Errorf("printer.mode scope = %q, want device", got)
	}
	if got := DescriptorScope(data.AgentSetting{Key: "no_such_family.x"}); got != "" {
		t.Errorf("unclassified scope = %q, want empty", got)
	}
}

// Money-, legal- or retention-changing keys are read-only mirrors on my.: the
// owner sets them on the till (ADR-0175 s15.3 never list, BA S1 AC3).
func TestRestorableFalseForCurrencyShopTypeRetentionFiscal(t *testing.T) {
	for _, k := range []string{"store.currency", "shop.type", data.ReportRetentionModeKey} {
		s, ok := data.LookupAgentSetting(k)
		if !ok {
			t.Fatalf("%s missing from the registry", k)
		}
		if Restorable(s) {
			t.Errorf("%s must not be restorable from my.", k)
		}
		if s.NotRestorableReason == "" {
			t.Errorf("%s: a non-restorable entry states its reason", k)
		}
	}
	// Fiscal registration and TSE enrolment are not registry keys at all.
	for _, s := range data.AgentSettings {
		if strings.HasPrefix(s.Key, "fiscal.") {
			t.Errorf("%s: fiscal keys are tier never and must not be registry entries", s.Key)
		}
	}
}

func TestRestorable_NeedsRemoteWritableAndAVisibleTierAndNoReason(t *testing.T) {
	for _, c := range []struct {
		name string
		s    data.AgentSetting
		want bool
	}{
		{"normal remote-writable", data.AgentSetting{Tier: data.SettingTierNormal, RemoteWritable: true}, true},
		{"sensitive remote-writable", data.AgentSetting{Tier: data.SettingTierSensitive, RemoteWritable: true}, true},
		{"not remote-writable", data.AgentSetting{Tier: data.SettingTierNormal}, false},
		{"secret", data.AgentSetting{Tier: data.SettingTierSecret, RemoteWritable: true}, false},
		{"never", data.AgentSetting{Tier: data.SettingTierNever, RemoteWritable: true}, false},
		{"secret flag", data.AgentSetting{Tier: data.SettingTierNormal, Secret: true, RemoteWritable: true}, false},
		{"stated reason", data.AgentSetting{Tier: data.SettingTierNormal, RemoteWritable: true, NotRestorableReason: "x"}, false},
		{"tierless", data.AgentSetting{RemoteWritable: true}, false},
	} {
		if got := Restorable(c.s); got != c.want {
			t.Errorf("%s: Restorable = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNeverAndSecretTiersNotRemoteWritable(t *testing.T) {
	for _, s := range data.AgentSettings {
		if s.Tier == data.SettingTierNever || s.Tier == data.SettingTierSecret || s.Secret {
			if data.RemoteWritableKeys()[s.Key] || Restorable(s) {
				t.Errorf("%s: tier %s/secret=%v must be neither remote-writable nor restorable", s.Key, s.Tier, s.Secret)
			}
		}
	}
}

// The sensitive-tier gate (ADR-0175 s15.3): owner only, step-up, own switch.
func TestTierGate_SensitiveIsOwnerOnlyStepUpAndItsOwnSwitch(t *testing.T) {
	n := TierGateFor(data.SettingTierNormal)
	if n.ConfirmBy != ConfirmByOwnerOrManager || n.StepUp || n.Switch != "allow_till_settings" {
		t.Errorf("normal gate = %+v", n)
	}
	s := TierGateFor(data.SettingTierSensitive)
	if s.ConfirmBy != ConfirmByOwner || !s.StepUp || s.Switch != "allow_sensitive_settings" {
		t.Errorf("sensitive gate = %+v", s)
	}
	for _, tier := range []string{data.SettingTierSecret, data.SettingTierNever, "", "bogus"} {
		g := TierGateFor(tier)
		if g.ConfirmBy != "" || g.Switch != "" || g.StepUp {
			t.Errorf("tier %q must have no gate (nobody confirms it), got %+v", tier, g)
		}
	}
}

func TestSettingsRegistry_OnlyOwnedReasonsAreNonRestorable(t *testing.T) {
	for _, s := range data.AgentSettings {
		if s.NotRestorableReason != "" && s.RemoteWritable {
			t.Errorf("%s: a restorable-false reason on a remote-writable key is contradictory", s.Key)
		}
	}
}
