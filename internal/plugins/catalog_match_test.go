package plugins

import (
	"testing"

	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// ut-docs#2131 review (Finding 3): the author+name fallback is a real
// name-collision risk when either side's author/developer_id is empty --
// plugins.author is a nullable column, and a catalog listing can likewise
// have no developer_id/vendor on file. Matching on name alone would treat
// two unrelated vendors' same-named plugin as the same one, and (via
// resolveListingViaCatalog in plugin_api.go) could point the "Update"
// button at an entirely different vendor's listing.
func TestCatalogIndexResolve_NeverMatchesOnNameAloneWithEmptyAuthor(t *testing.T) {
	idx := IndexCatalog(&marketplace.CatalogSnapshot{
		Plugins: []marketplace.PluginSummary{
			// No developer_id on file -- must never be indexed for the
			// author+name fallback at all.
			{ID: "listing-anon", Name: "Loyalty Plugin", Version: "9.9.9"},
			// A real, correctly-attributed listing sharing the same name.
			{ID: "listing-acme", Name: "Loyalty Plugin", DeveloperID: "Acme Inc", Version: "2.0.0"},
		},
	})

	// Installed plugin with no author recorded (e.g. a manifest that never
	// set one) must not match either listing by name alone.
	if p, ok := idx.Resolve("", "", "Loyalty Plugin"); ok {
		t.Fatalf("Resolve with empty author matched %+v, want no match", p)
	}

	// An installed plugin whose author does NOT match the anonymous
	// listing's (empty) developer_id must resolve to nothing, not the
	// anonymous listing.
	if p, ok := idx.Resolve("", "Someone Else", "Loyalty Plugin"); ok {
		t.Fatalf("Resolve for an unrelated author matched %+v, want no match", p)
	}

	// The correctly-attributed pair still matches -- this guard must narrow
	// the fallback, not disable it.
	p, ok := idx.Resolve("", "Acme Inc", "Loyalty Plugin")
	if !ok || p.Version != "2.0.0" {
		t.Fatalf("Resolve for the correctly-attributed author = %+v, ok=%v; want listing-acme v2.0.0", p, ok)
	}
}
