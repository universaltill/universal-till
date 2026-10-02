package plugins

import (
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
