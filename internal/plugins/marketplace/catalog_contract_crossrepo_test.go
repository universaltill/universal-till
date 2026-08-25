package marketplace

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// TestCloudListPluginsResponseDecodes is a cross-repo contract test (same
// pattern as internal/plugins/marketplace_signature_crossrepo_test.go): the
// fixture in testdata/cloud_list_plugins_response.json was captured from
// ut-cloud's real ListPlugins response — its regenerated proto type marshaled
// through protojson with the same options the deployed grpc-gateway uses
// (protojson.MarshalOptions{EmitUnpopulated: true}, hence lowerCamelCase keys
// and explicit ""/false/[] zero values on the wire). This proves the real POS
// PluginSummary.UnmarshalJSON — the exact production decode path ListPlugins
// runs — decodes that wire format correctly, most importantly the per-listing
// `availableLocales` array the country base-plugin auto-install matches on
// (ut-docs#591 / #1055: the old decode expected a singular `locale` field the
// real server never sent, so the match silently never succeeded).
//
// If this breaks, ut-cloud's catalog wire format and this decoder have
// drifted; recapture the fixture from ut-cloud and reconcile the two sides.
func TestCloudListPluginsResponseDecodes(t *testing.T) {
	fixtureBytes, err := os.ReadFile("testdata/cloud_list_plugins_response.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var resp ListPluginsResponse
	if err := json.Unmarshal(fixtureBytes, &resp); err != nil {
		t.Fatalf("decode real ut-cloud ListPlugins response: %v", err)
	}
	if len(resp.Plugins) != 1 {
		t.Fatalf("expected 1 plugin in the fixture, got %d", len(resp.Plugins))
	}

	p := resp.Plugins[0]
	if got, want := p.AvailableLocales, []string{"de"}; !reflect.DeepEqual(got, want) {
		t.Errorf("AvailableLocales = %#v, want %#v (the real server's availableLocales array must survive decode — the base-plugin locale match depends on it)", got, want)
	}
	if got, want := p.CanonicalType, "language"; got != want {
		t.Errorf("CanonicalType = %q, want %q (via the wire `type` compat fallback)", got, want)
	}
	if got, want := p.ID, "8f14c7c2-3b6a-4f1d-9e2a-5d7b1c0a9e42"; got != want {
		t.Errorf("ID = %q, want %q", got, want)
	}
	if got, want := p.ListingID, "8f14c7c2-3b6a-4f1d-9e2a-5d7b1c0a9e42"; got != want {
		t.Errorf("ListingID = %q, want %q (the catalog id IS the listing id)", got, want)
	}
	if got, want := p.Name, "German Language Pack"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
	if got, want := p.Version, "1.1.17"; got != want {
		t.Errorf("Version = %q, want %q", got, want)
	}
	if got, want := p.Vendor, "universaltill"; got != want {
		t.Errorf("Vendor = %q, want %q", got, want)
	}
	if got, want := p.TrustTier, "verified"; got != want {
		t.Errorf("TrustTier = %q, want %q (via the wire trustLevel fallback)", got, want)
	}
	if got, want := p.Description, "German interface language pack"; got != want {
		t.Errorf("Description = %q, want %q", got, want)
	}
	if p.PaidListing {
		t.Error("PaidListing = true, want false")
	}
}
