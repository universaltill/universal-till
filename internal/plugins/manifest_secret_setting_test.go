package plugins

import (
	"strings"
	"testing"
)

// ADR-0082 (ut-docs#1739): a manifest setting may declare `type: "secret"`
// so its value is masked on the settings page and sealed at rest regardless
// of the key's name. The only valid non-empty type today is "secret"; an
// unknown one fails at parse time with a clear message, the same way an
// unknown entry type does — not at persist time, and never silently.
func TestParseManifest_SettingTypeSecret(t *testing.T) {
	ok := `{"id":"t.p","name":"T","version":"1.0.0","runtime":"none",
		"settings":[{"key":"merchant_code","type":"secret"},{"key":"endpoint"}]}`
	m, err := ParseManifest(strings.NewReader(ok))
	if err != nil {
		t.Fatalf("type secret should be valid: %v", err)
	}
	if m.Settings[0].Type != SettingTypeSecret {
		t.Fatalf("Type = %q, want %q", m.Settings[0].Type, SettingTypeSecret)
	}
	if m.Settings[1].Type != "" {
		t.Fatalf("undeclared Type = %q, want empty", m.Settings[1].Type)
	}

	bad := `{"id":"t.p","name":"T","version":"1.0.0","runtime":"none",
		"settings":[{"key":"merchant_code","type":"password"}]}`
	_, err = ParseManifest(strings.NewReader(bad))
	if err == nil {
		t.Fatal("expected error for unknown setting type")
	}
	for _, want := range []string{"merchant_code", "password", "secret"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q should name the key, the bad type and the allowed type (%q)", err, want)
		}
	}
}

// Both the manifest declaration and the key-name heuristic make a setting
// secret; the manifest is the lookup table the settings page consults.
func TestManifest_SettingDeclaredSecret(t *testing.T) {
	m := &Manifest{Settings: []ManifestSetting{
		{Key: "merchant_code", Type: SettingTypeSecret},
		{Key: "endpoint"},
	}}
	if !m.SettingDeclaredSecret("merchant_code") {
		t.Fatal("merchant_code is declared secret")
	}
	if m.SettingDeclaredSecret("endpoint") {
		t.Fatal("endpoint is not declared secret")
	}
	if m.SettingDeclaredSecret("nope") {
		t.Fatal("an undeclared key is not declared secret")
	}
	var nilManifest *Manifest
	if nilManifest.SettingDeclaredSecret("merchant_code") {
		t.Fatal("nil manifest must be safe and report false")
	}
}

// plugins.IsSecretSettingKey is the name the settings page (and any other
// plugin-side caller) uses; it must be the very same rule internal/data
// seals on (secrets.IsSecretSettingKey), or a key could be masked in the UI
// yet stored in cleartext, or vice versa.
func TestIsSecretSettingKeyDelegatesToSecrets(t *testing.T) {
	for _, k := range []string{"stripe_secret_key", "sumup_api_key", "auth_value", "password", "key"} {
		if !IsSecretSettingKey(k) {
			t.Errorf("IsSecretSettingKey(%q) = false, want true", k)
		}
	}
	for _, k := range []string{"endpoint", "reader_id", "takeaway_rate_overrides"} {
		if IsSecretSettingKey(k) {
			t.Errorf("IsSecretSettingKey(%q) = true, want false", k)
		}
	}
}
