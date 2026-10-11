package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ut-docs#2832: settings-form metadata — `type: "choice"` with options,
// label/help i18n keys and show_when.
func choiceManifest(settingsJSON string) string {
	return `{"id":"t.p","name":"T","version":"1.0.0","runtime":"none","canonical_type":"integration","device_arch":"any","settings":[` + settingsJSON + `]}`
}

const goodChoiceSettings = `{"key":"provider","type":"choice","default_value":"a","label":"t.provider","help":"t.provider.help",
	"options":[{"value":"a","label":"t.a"},{"value":"b","label":"t.b"}]},
	{"key":"api_key","type":"secret","label":"t.key","show_when":{"key":"provider","in":["b"]}}`

func TestParseManifest_SettingTypeChoice(t *testing.T) {
	m, err := ParseManifest(strings.NewReader(choiceManifest(goodChoiceSettings)))
	if err != nil {
		t.Fatalf("valid choice manifest refused: %v", err)
	}
	p := m.SettingDecl("provider")
	if p == nil || p.Type != SettingTypeChoice || p.Label != "t.provider" || p.Help != "t.provider.help" || len(p.Options) != 2 {
		t.Fatalf("provider decl = %+v", p)
	}
	if !p.ChoiceAllows("a") || !p.ChoiceAllows("b") || p.ChoiceAllows("A") || p.ChoiceAllows("") {
		t.Fatal("ChoiceAllows must match option values exactly")
	}
	k := m.SettingDecl("api_key")
	if k == nil || k.ShowWhen == nil || k.ShowWhen.Key != "provider" || k.ShowWhen.In[0] != "b" {
		t.Fatalf("api_key decl = %+v", k)
	}
	if m.SettingDecl("missing") != nil {
		t.Fatal("undeclared key must give nil")
	}
	var nilM *Manifest
	if nilM.SettingDecl("provider") != nil {
		t.Fatal("nil manifest declares nothing")
	}
}

func TestParseManifest_SettingFormRefusals(t *testing.T) {
	cases := map[string]string{
		"choice without options":        `{"key":"p","type":"choice"}`,
		"empty option value":            `{"key":"p","type":"choice","options":[{"value":"","label":"x"}]}`,
		"empty option label":            `{"key":"p","type":"choice","options":[{"value":"a","label":""}]}`,
		"duplicate option value":        `{"key":"p","type":"choice","options":[{"value":"a","label":"x"},{"value":"a","label":"y"}]}`,
		"options on non-choice":         `{"key":"p","options":[{"value":"a","label":"x"}]}`,
		"default not an option":         `{"key":"p","type":"choice","default_value":"z","options":[{"value":"a","label":"x"}]}`,
		"non-string default":            `{"key":"p","type":"choice","default_value":1,"options":[{"value":"1","label":"x"}]}`,
		"show_when unknown key":         `{"key":"p","show_when":{"key":"nope","in":["a"]}}`,
		"show_when on a secret":         `{"key":"k","type":"secret"},{"key":"q","show_when":{"key":"k","in":["a"]}}`,
		"show_when comma value":         `{"key":"p","type":"choice","options":[{"value":"a","label":"x"}]},{"key":"q","show_when":{"key":"p","in":["a,b"]}}`,
		"show_when on heuristic secret": `{"key":"auth_token_kind"},{"key":"q","show_when":{"key":"auth_token_kind","in":["a"]}}`,
		"choice on heuristic secret":    `{"key":"license_key","type":"choice","options":[{"value":"a","label":"x"}]}`,
		"show_when self":                `{"key":"p","show_when":{"key":"p","in":["a"]}}`,
		"show_when empty in":            `{"key":"p","type":"choice","options":[{"value":"a","label":"x"}]},{"key":"q","show_when":{"key":"p","in":[]}}`,
		"overlong label":                `{"key":"p","label":"` + strings.Repeat("x", 129) + `"}`,
		"too many options":              `{"key":"p","type":"choice","options":[` + manyOptions(33) + `]}`,
	}
	for name, s := range cases {
		if _, err := ParseManifest(strings.NewReader(choiceManifest(s))); err == nil {
			t.Errorf("%s: ParseManifest accepted %s", name, s)
		}
	}
}

func manyOptions(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = `{"value":"v` + strings.Repeat("x", i) + `","label":"l"}`
	}
	return strings.Join(parts, ",")
}

// The marketplace path (VerifyManifest) must refuse the same shapes.
func TestVerifyManifest_SettingForm(t *testing.T) {
	mv, err := NewManifestVerifier("")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(p, []byte(choiceManifest(`{"key":"p","type":"choice"}`)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mv.VerifyManifest(p); err == nil || !strings.Contains(err.Error(), `"p"`) {
		t.Fatalf("VerifyManifest should refuse a choice without options, got %v", err)
	}
	if err := os.WriteFile(p, []byte(choiceManifest(goodChoiceSettings)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mv.VerifyManifest(p); err != nil {
		t.Fatalf("valid choice manifest refused: %v", err)
	}
}
