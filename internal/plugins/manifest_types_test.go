package plugins

import (
	"strings"
	"testing"
)

// The taxonomy is the contract: every canonical type parses, anything else fails fast.
func TestParseManifest_EntryTypeTaxonomy(t *testing.T) {
	for _, typ := range CanonicalTypes {
		m := `{"id":"t.p","name":"T","version":"1.0.0","runtime":"none",
			"entries":[{"type":"` + typ + `","key":"k","label":"L"}]}`
		if _, err := ParseManifest(strings.NewReader(m)); err != nil {
			t.Fatalf("type %q should be valid: %v", typ, err)
		}
	}
	bad := `{"id":"t.p","name":"T","version":"1.0.0","runtime":"none",
		"entries":[{"type":"gadget","key":"k","label":"L"}]}`
	if _, err := ParseManifest(strings.NewReader(bad)); err == nil {
		t.Fatal("expected error for unknown entry type")
	}
}

func TestCanonicalTypesMatchTaxonomy(t *testing.T) {
	want := "page|button|popup|payment|device|integration|report|pricing|tax|import|export|hardware|background_job|scheduler|receipt_template|customer_facing|auth|notification|delivery|theme"
	if got := strings.Join(CanonicalTypes, "|"); got != want {
		t.Fatalf("taxonomy drifted:\n got %s\nwant %s", got, want)
	}
}
