package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ADR-0129 §2/§3 (ut-docs#3176): `provides` and `markets` are closed-set,
// validated at install, and must not change the canonical bytes of a
// manifest that doesn't declare them (old signatures keep verifying).

func manifestWith(extra string) string {
	return `{"id":"t.p","name":"T","version":"1.0.0","runtime":"none"` + extra + `}`
}

func TestParseManifest_ProvidesAcceptsClosedSet(t *testing.T) {
	var provides []string
	provides = append(provides, CapabilityFiscalDevice, CapabilityFiscalRegister, CapabilityAI)
	for _, st := range ShopTypes() {
		provides = append(provides, CapabilityLayoutShopTypePrefix+st)
	}
	for _, p := range provides {
		m, err := ParseManifest(strings.NewReader(manifestWith(`,"provides":["` + p + `"]`)))
		if err != nil {
			t.Fatalf("provides %q should be valid: %v", p, err)
		}
		if len(m.Provides) != 1 || m.Provides[0] != p {
			t.Fatalf("provides %q not kept: %v", p, m.Provides)
		}
	}
}

func TestParseManifest_ProvidesRefusesUnknown(t *testing.T) {
	for _, p := range []string{
		"",
		"fiscal",
		"fiscal.signer",
		"Fiscal.register",
		"fiscal.register ",
		"AI",
		"layout.shop_type:",
		"layout.shop_type:salon",
		"layout.shop_type:Service",
		"layout.shop_type",
	} {
		_, err := ParseManifest(strings.NewReader(manifestWith(`,"provides":[` + mustJSON(t, p) + `]`)))
		if err == nil {
			t.Fatalf("provides %q should be refused", p)
		}
		if !strings.Contains(err.Error(), "provides") {
			t.Fatalf("provides %q: error should name the field, got %v", p, err)
		}
	}
}

func TestParseManifest_ProvidesRefusesDuplicate(t *testing.T) {
	_, err := ParseManifest(strings.NewReader(manifestWith(`,"provides":["ai","ai"]`)))
	if err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("duplicate provides should be refused, got %v", err)
	}
}

func TestParseManifest_MarketsAcceptsAlpha2(t *testing.T) {
	m, err := ParseManifest(strings.NewReader(manifestWith(`,"markets":["DE","AT","TR"]`)))
	if err != nil {
		t.Fatalf("markets should be valid: %v", err)
	}
	if strings.Join(m.Markets, ",") != "DE,AT,TR" {
		t.Fatalf("markets not kept in order: %v", m.Markets)
	}
}

func TestParseManifest_MarketsRefusesBadCodes(t *testing.T) {
	for _, c := range []string{"", "de", "De", "DEU", "D", "D1", " DE", "UK "} {
		_, err := ParseManifest(strings.NewReader(manifestWith(`,"markets":[` + mustJSON(t, c) + `]`)))
		if err == nil {
			t.Fatalf("market %q should be refused", c)
		}
		if !strings.Contains(err.Error(), "markets") {
			t.Fatalf("market %q: error should name the field, got %v", c, err)
		}
	}
	if _, err := ParseManifest(strings.NewReader(manifestWith(`,"markets":["DE","DE"]`))); err == nil {
		t.Fatal("duplicate market should be refused")
	}
}

// Absent fields must marshal exactly as before the fields existed: the
// signature covers the re-marshalled struct (manifest_verifier.go), so any
// new key appearing here would break every already-signed plugin.
func TestManifest_ProvidesMarketsAbsentMarshalUnchanged(t *testing.T) {
	src := manifestWith(`,"permissions":["ui:page"]`)
	m, err := ParseManifest(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "provides") || strings.Contains(string(b), "markets") {
		t.Fatalf("absent provides/markets must not be marshalled: %s", b)
	}
	// An explicitly empty list is dropped too (omitempty) — the signer does
	// the same, so both sides agree on the canonical bytes.
	m2, err := ParseManifest(strings.NewReader(manifestWith(`,"permissions":["ui:page"],"provides":[],"markets":[]`)))
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := json.Marshal(m2)
	if string(b2) != string(b) {
		t.Fatalf("empty provides/markets changed canonical bytes:\n%s\n%s", b, b2)
	}
}

func TestManifest_ProvidesMarketsRoundTrip(t *testing.T) {
	m, err := ParseManifest(strings.NewReader(manifestWith(`,"permissions":["ui:page"],"provides":["fiscal.register"],"markets":["DE"]`)))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(m)
	if !strings.Contains(string(b), `"permissions":["ui:page"],"provides":["fiscal.register"],"markets":["DE"]`) {
		t.Fatalf("provides/markets not marshalled after permissions in declaration order: %s", b)
	}
}

func mustJSON(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The marketplace install path verifies with VerifyManifest, not
// ParseManifest, so the closed set must hold there too (review of
// ut-docs#3176): #3281 persists provides from that path.
func TestVerifyManifest_RefusesUnknownProvidesAndBadMarkets(t *testing.T) {
	mv, err := NewManifestVerifier("")
	if err != nil {
		t.Fatal(err)
	}
	base := `{"id":"t.p","name":"T","version":"1.0.0","runtime":"none","canonical_type":"tax","device_arch":"any"`
	for name, extra := range map[string]string{
		"provides": `,"provides":["fiscal.signer"]`,
		"markets":  `,"markets":["de"]`,
	} {
		p := filepath.Join(t.TempDir(), "manifest.json")
		if err := os.WriteFile(p, []byte(base+extra+`}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := mv.VerifyManifest(p); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s: VerifyManifest should refuse, got %v", name, err)
		}
	}
	p := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(p, []byte(base+`,"provides":["fiscal.register"],"markets":["DE"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mv.VerifyManifest(p); err != nil {
		t.Fatalf("valid provides/markets refused: %v", err)
	}
}
