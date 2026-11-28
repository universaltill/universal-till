package marketplace

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
)

// ut-docs#7246 (ADR-0149 item 5): the catalogue's shop_types array reaches the
// till, so the setup wizard can suggest a plugin by the neutral field, and
// the till can ask the catalogue for one shop type via `shop_type`.

func TestPluginSummary_DecodesShopTypes(t *testing.T) {
	for name, raw := range map[string]string{
		"camelCase (REST gateway)": `{"id":"l1","type":"page","shopTypes":["service","retail"]}`,
		"snake_case (snapshot)":    `{"id":"l1","type":"page","shop_types":["service","retail"]}`,
	} {
		var p PluginSummary
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		if len(p.ShopTypes) != 2 || p.ShopTypes[0] != "service" || p.ShopTypes[1] != "retail" {
			t.Errorf("%s: ShopTypes = %v, want [service retail]", name, p.ShopTypes)
		}
	}
	var none PluginSummary
	if err := json.Unmarshal([]byte(`{"id":"l2","type":"page"}`), &none); err != nil {
		t.Fatal(err)
	}
	if len(none.ShopTypes) != 0 {
		t.Fatalf("a listing naming no shop type must decode to none, got %v", none.ShopTypes)
	}
}

func TestListPlugins_SendsShopTypeFilter(t *testing.T) {
	withHostVersion(t, "0.9.4")
	var q url.Values
	c := compatTestClient(t, nil, &q)
	if _, err := c.ListPlugins(context.Background(), &ListPluginsRequest{ShopType: " service "}); err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if got := q.Get("shop_type"); got != "service" {
		t.Errorf("shop_type = %q, want service (query: %v)", got, q)
	}
	var q2 url.Values
	c2 := compatTestClient(t, nil, &q2)
	if _, err := c2.ListPlugins(context.Background(), &ListPluginsRequest{}); err != nil {
		t.Fatal(err)
	}
	if q2.Has("shop_type") {
		t.Errorf("no shop type asked: the key must be absent (query: %v)", q2)
	}
}
