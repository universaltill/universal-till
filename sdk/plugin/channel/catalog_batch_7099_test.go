package channel

import (
	"encoding/json"
	"strings"
	"testing"
)

// ut-docs#7099/#7101: the additive listing fields round-trip and are omitted when unset.
func TestListingAdditiveFields_7099(t *testing.T) {
	f := false
	idx := 1
	b, err := json.Marshal(Listing{Kind: "gift_card", VariantImageIndex: &idx,
		Variants: []Variant{{ExternalID: "v", Taxable: &f}}, Images: []Image{{ExternalID: "i", URL: "u", Alt: "a"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"kind":"gift_card"`, `"variant_image_index":1`, `"taxable":false`, `"alt":"a"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing %s in %s", want, b)
		}
	}
	b, _ = json.Marshal(Listing{Variants: []Variant{{ExternalID: "v"}}, Images: []Image{{ExternalID: "i", URL: "u"}}})
	for _, no := range []string{"kind", "taxable", "alt", "variant_image_index"} {
		if strings.Contains(string(b), no) {
			t.Fatalf("%s must be omitted: %s", no, b)
		}
	}
}
