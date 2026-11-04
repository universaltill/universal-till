package data

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ut-docs#5349 (ADR-0175 amendment 13): the registry is the one list.

func TestSettingsRegistry_EveryEntryHasATierAndType(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range AgentSettings {
		if seen[s.Key] {
			t.Errorf("duplicate registry key %s", s.Key)
		}
		seen[s.Key] = true
		if !ValidSettingTier(s.Tier) {
			t.Errorf("%s: tier %q is not normal|sensitive|secret|never", s.Key, s.Tier)
		}
		switch s.Type {
		case SettingTypeBool:
			if s.On == "" || s.Off == "" {
				t.Errorf("%s: bool needs On and Off", s.Key)
			}
		case SettingTypeInt:
			if s.Max <= s.Min {
				t.Errorf("%s: int needs Min < Max", s.Key)
			}
		case SettingTypeEnum, SettingTypeText, SettingTypeList:
		default:
			t.Errorf("%s: unknown type %q", s.Key, s.Type)
		}
		if s.Kind == AgentSettingBool != (s.Type == SettingTypeBool) {
			t.Errorf("%s: Kind %q disagrees with Type %q", s.Key, s.Kind, s.Type)
		}
		if s.RemoteWritable && s.Tier != SettingTierNormal && s.Tier != SettingTierSensitive {
			t.Errorf("%s: remote-writable but tier %s", s.Key, s.Tier)
		}
		if s.Secret && s.Tier != SettingTierSecret {
			t.Errorf("%s: Secret flag needs tier secret", s.Key)
		}
	}
}

func TestAssistantVisible_SecretNeverAndTierlessAreHidden(t *testing.T) {
	for _, c := range []struct {
		s    AgentSetting
		want bool
	}{
		{AgentSetting{Tier: SettingTierNormal}, true},
		{AgentSetting{Tier: SettingTierSensitive}, true},
		{AgentSetting{Tier: SettingTierSecret}, false},
		{AgentSetting{Tier: SettingTierNever}, false},
		{AgentSetting{Tier: SettingTierNormal, Secret: true}, false},
		{AgentSetting{}, false},
	} {
		if got := c.s.AssistantVisible(); got != c.want {
			t.Errorf("%+v: AssistantVisible %v, want %v", c.s, got, c.want)
		}
	}
}

func TestRemoteWritableKeys_OnlyNormalAndSensitiveAndNeverSecret(t *testing.T) {
	entries := []AgentSetting{
		{Key: "a", Tier: SettingTierNormal, RemoteWritable: true},
		{Key: "b", Tier: SettingTierSensitive, RemoteWritable: true},
		{Key: "c", Tier: SettingTierSecret, RemoteWritable: true},
		{Key: "d", Tier: SettingTierNever, RemoteWritable: true},
		{Key: "e", Tier: SettingTierNormal},
		{Key: "f", Tier: "", RemoteWritable: true},
	}
	got := RemoteWritableKeysOf(entries)
	if len(got) != 2 || !got["a"] || !got["b"] {
		t.Fatalf("got %v, want only a and b", got)
	}
}

func TestSettingsRegistry_ExistingRemoteSetIsPreserved(t *testing.T) {
	want := []string{
		"printer.receipt_policy", "receipt.header1", "receipt.header2", "receipt.header3", "receipt.footer",
		"kiosk.idle_reset_seconds", "sale.order_type_prompt", "sale.browsing_mode", "sale.display_no_scheme",
		BarcodeEnabledSymbologiesKey, CatalogImportBarcodeFromSKUDefaultKey, CatalogPrePackUnitPriceEnabledKey,
		"pos.allow_negative_inventory", "invoice.seller_name", "invoice.seller_address", "invoice.seller_vat_no",
		"auth.idle_lock_minutes", "kiosk.payment_mode", "store.locale", "store.staff_locales",
	}
	got := RemoteWritableKeys()
	for _, k := range want {
		if !got[k] {
			t.Errorf("%s dropped from the remote-writable set", k)
		}
	}
	if len(got) != len(want) {
		t.Errorf("remote-writable set has %d keys, want %d: %v", len(got), len(want), got)
	}
	for _, k := range []string{"store.currency", "shop.type", ReportRetentionModeKey, "payments.default_method"} {
		if got[k] {
			t.Errorf("%s must stay read-only", k)
		}
	}
}

func TestSettingsRegistry_NegativeInventoryUnchanged(t *testing.T) {
	s, ok := LookupAgentSetting(KeyAllowNegativeInventory)
	if !ok || s.On != "true" || s.Off != "false" || s.Default != "false" || !s.Writable || s.Tier != SettingTierNormal {
		t.Fatalf("pos.allow_negative_inventory entry changed: %+v", s)
	}
}

func registryJSONPath(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", SettingsRegistryJSONPath)
}

// The committed artefact must match the registry: regenerate with
// `make gen-settings-registry`.
func TestSettingsRegistryJSON_MatchesCommittedArtefact(t *testing.T) {
	want, err := SettingsRegistryJSON()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(registryJSONPath(t))
	if err != nil {
		t.Fatalf("read artefact: %v (run `make gen-settings-registry`)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale: run `make gen-settings-registry` and commit it", SettingsRegistryJSONPath)
	}
}

func TestSettingsRegistryJSON_Shape(t *testing.T) {
	raw, err := SettingsRegistryJSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema  int                          `json:"schema"`
		Entries []map[string]json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Schema != 1 || len(doc.Entries) != len(AgentSettings) {
		t.Fatalf("schema %d, %d entries", doc.Schema, len(doc.Entries))
	}
	for _, f := range []string{"key", "type", "tier", "secret", "default", "on", "off", "min", "max", "options",
		"label_key", "description_key", "region", "min_till_version", "audit", "writable_by_till_agent", "remote_writable"} {
		if _, ok := doc.Entries[0][f]; !ok {
			t.Errorf("entry lacks field %q", f)
		}
	}
	if strings.Contains(string(raw), `"value"`) {
		t.Error("the artefact must carry no values")
	}
}
