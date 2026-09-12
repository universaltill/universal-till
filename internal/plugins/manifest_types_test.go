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

// TestCanonicalTypesMatchTaxonomy used to live here, diffing CanonicalTypes
// against a second hardcoded copy of the same list in this file — which
// meant it pinned the code against itself and could never catch ADR-0002's
// own text drifting out of sync (ut-docs#2134: `language` and `layout` were
// both added to CanonicalTypes with a real accepted ADR each, this test
// stayed green throughout, and ADR-0002 still said "20 types" a cycle
// later). The real cross-repo check now lives in
// scripts/ci/guard-adr-plugin-taxonomy.sh, which reads ADR-0002's actual
// taxonomy line for real instead of a second literal in this package.
