package plugins

import (
	"encoding/json"
	"strings"
	"testing"
)

// ut-docs#7246: manifest `shop_types` names the ADR-0026 shop types a plugin
// is built for. Signed and optional; ut-cloud's signing.CanonicalManifest
// mirrors it (same position, same tag), so the canonical bytes must match.

func TestManifestShopTypes_Validation_7246(t *testing.T) {
	for name, extra := range map[string]string{
		"one":      `,"shop_types":["service"]`,
		"several":  `,"shop_types":["service","retail"]`,
		"all":      `,"shop_types":["cafe","retail","service","hospitality","market_stall","other"]`,
		"empty":    `,"shop_types":[]`,
		"absent":   ``,
		"with rf":  `,"permissions":["records:sync"],"records_forward":{"collections":["a"]},"shop_types":["service"]`,
		"explicit": `,"shop_types":["other"]`,
	} {
		if err := parseRecordsManifest(t, extra); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
	for name, extra := range map[string]string{
		"unknown":    `,"shop_types":["restaurant"]`,
		"empty str":  `,"shop_types":[""]`,
		"upper case": `,"shop_types":["Service"]`,
		"padded":     `,"shop_types":[" service"]`,
		"duplicate":  `,"shop_types":["service","service"]`,
		"capability": `,"shop_types":["layout.shop_type:service"]`,
	} {
		err := parseRecordsManifest(t, extra)
		if err == nil {
			t.Errorf("%s: manifest accepted, want refusal", name)
			continue
		}
		if !strings.Contains(err.Error(), "shop_types") {
			t.Errorf("%s: error %q does not name shop_types", name, err)
		}
	}
}

// Absent or empty marshals without the key (old signatures stay valid), and
// a declared list is the LAST key, after records_forward: ut-cloud's
// CanonicalManifest has it in the same place.
func TestManifestShopTypes_CanonicalPosition_7246(t *testing.T) {
	m, err := ParseManifest(strings.NewReader(manifestWith(`,"shop_types":[]`)))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	if strings.Contains(string(raw), "shop_types") {
		t.Fatalf("empty shop_types must marshal without the key: %s", raw)
	}
	m, err = ParseManifest(strings.NewReader(manifestWith(`,"permissions":["records:sync"],"records_forward":{"collections":["a"]},"shop_types":["service"]`)))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(m)
	s := string(raw)
	if !strings.HasSuffix(s, `"records_forward":{"collections":["a"]},"shop_types":["service"]}`) {
		t.Fatalf("shop_types must be the last key, right after records_forward: %s", s)
	}
}
