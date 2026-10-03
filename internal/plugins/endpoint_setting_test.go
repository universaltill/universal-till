package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ADR-0121 §2 (ut-docs#3328): `type: "endpoint"` is the second non-empty
// ManifestSetting.Type — an operator-entered http(s) URL.
func TestParseManifest_SettingTypeEndpoint(t *testing.T) {
	ok := `{"id":"t.p","name":"T","version":"1.0.0","runtime":"none",
		"settings":[{"key":"erp_url","type":"endpoint"},{"key":"api_key","type":"secret"}]}`
	m, err := ParseManifest(strings.NewReader(ok))
	if err != nil {
		t.Fatalf("type endpoint should be valid: %v", err)
	}
	if m.Settings[0].Type != SettingTypeEndpoint {
		t.Fatalf("Type = %q, want %q", m.Settings[0].Type, SettingTypeEndpoint)
	}
	if !m.SettingDeclaredEndpoint("erp_url") || m.SettingDeclaredEndpoint("api_key") || m.SettingDeclaredEndpoint("missing") {
		t.Fatal("SettingDeclaredEndpoint must be true only for the key declared type endpoint")
	}
	var nilM *Manifest
	if nilM.SettingDeclaredEndpoint("erp_url") {
		t.Fatal("nil manifest declares nothing")
	}

	bad := `{"id":"t.p","name":"T","version":"1.0.0","runtime":"none",
		"settings":[{"key":"erp_url","type":"url"}]}`
	_, err = ParseManifest(strings.NewReader(bad))
	if err == nil {
		t.Fatal("expected error for unknown setting type")
	}
	for _, want := range []string{"erp_url", `"url"`, "secret", "endpoint"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q should name the key, the bad type and both allowed types (%q)", err, want)
		}
	}
}

// ut-docs#3552: ParseManifest must reject an "endpoint"-typed setting whose
// default_value is not a valid http(s) URL — PersistManifest otherwise
// persists it into plugin_settings verbatim at install time, unchecked.
func TestParseManifest_EndpointDefaultValue(t *testing.T) {
	manifestWith := func(settingsJSON string) string {
		return `{"id":"t.p","name":"T","version":"1.0.0","runtime":"none","settings":[` + settingsJSON + `]}`
	}

	valid := []string{
		`{"key":"erp_url","type":"endpoint","default_value":"https://erp.example.com/webhook"}`,
		// "" is the established no-default-yet convention for a typed
		// setting (the Stripe/SumUp secret-typed fixtures use the same
		// empty-string default) — must not be rejected as an invalid URL.
		`{"key":"erp_url","type":"endpoint","default_value":""}`,
		// No default_value field at all must still parse.
		`{"key":"erp_url","type":"endpoint"}`,
		// Non-endpoint settings are unaffected by this check regardless of
		// shape.
		`{"key":"webhook_count","type":"secret","default_value":8080}`,
		`{"key":"webhook_count","default_value":8080}`,
	}
	for _, s := range valid {
		if _, err := ParseManifest(strings.NewReader(manifestWith(s))); err != nil {
			t.Errorf("settings=%s: expected no error, got %v", s, err)
		}
	}

	invalidStrings := []string{
		"not-a-url", "javascript:alert(1)", "ftp://erp.example.com", " https://erp.example.com",
	}
	for _, v := range invalidStrings {
		s := `{"key":"erp_url","type":"endpoint","default_value":"` + v + `"}`
		_, err := ParseManifest(strings.NewReader(manifestWith(s)))
		if err == nil {
			t.Fatalf("settings=%s: expected error for invalid endpoint default", s)
		}
		if !strings.Contains(err.Error(), "erp_url") {
			t.Fatalf("error %q should name the key", err)
		}
	}

	nonString := []string{
		`{"key":"erp_url","type":"endpoint","default_value":8080}`,
		`{"key":"erp_url","type":"endpoint","default_value":true}`,
		`{"key":"erp_url","type":"endpoint","default_value":{"a":1}}`,
	}
	for _, s := range nonString {
		if _, err := ParseManifest(strings.NewReader(manifestWith(s))); err == nil {
			t.Fatalf("settings=%s: expected error for non-string endpoint default", s)
		}
	}
}

// The marketplace install path verifies with VerifyManifest, which never
// goes through ParseManifest (same split as provides/markets, ABI-3 and the
// permission allow-list) — it must refuse the same invalid endpoint
// defaults (ut-docs#3552).
func TestVerifyManifest_RefusesInvalidEndpointDefault(t *testing.T) {
	mv, err := NewManifestVerifier("")
	if err != nil {
		t.Fatal(err)
	}
	base := `{"id":"t.p","name":"T","version":"1.0.0","runtime":"none","canonical_type":"tax","device_arch":"any","settings":[`
	p := filepath.Join(t.TempDir(), "manifest.json")

	if err := os.WriteFile(p, []byte(base+`{"key":"erp_url","type":"endpoint","default_value":"javascript:alert(1)"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mv.VerifyManifest(p); err == nil || !strings.Contains(err.Error(), "erp_url") {
		t.Fatalf("VerifyManifest should refuse an invalid endpoint default, got %v", err)
	}

	if err := os.WriteFile(p, []byte(base+`{"key":"erp_url","type":"endpoint","default_value":"https://erp.example.com"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mv.VerifyManifest(p); err != nil {
		t.Fatalf("valid endpoint default refused: %v", err)
	}
}

func TestValidEndpointURL(t *testing.T) {
	valid := []string{
		"https://10.0.0.5:8080/path",
		"http://erp.lan",
		"http://erp.lan/",
		"https://api.example.com/v1/hooks",
		"HTTPS://Example.com:443/x",
		"http://[fe80::1]:8080/a",
	}
	for _, v := range valid {
		if !ValidEndpointURL(v) {
			t.Errorf("ValidEndpointURL(%q) = false, want true", v)
		}
	}
	invalid := []string{
		"", "ftp://x", "not a url", "javascript:alert(1)", "http://", "https:///path",
		"//erp.lan/path", "erp.lan:8080", "http://erp.lan:0/", "http://erp.lan:99999/",
		"http://erp.lan:abc/", "http://user:pass@erp.lan/", "http://erp.lan/p?token=x",
		"http://erp.lan/p#frag", "http://erp lan/", " https://erp.lan", "file:///etc/passwd",
		"data:text/html,hi", "http:erp.lan",
		// url.Parse splits the port at the last colon: host "erp.lan:8080"
		// would otherwise pass (independent review, ut-docs#3328).
		"http://erp.lan:8080:80/", "http://[v1.fe80::1]/",
	}
	for _, v := range invalid {
		if ValidEndpointURL(v) {
			t.Errorf("ValidEndpointURL(%q) = true, want false", v)
		}
	}
}
