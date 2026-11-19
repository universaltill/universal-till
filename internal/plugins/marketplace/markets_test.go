package marketplace

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
)

// ADR-0172 §2 / ADR-0129 §3 (ut-docs#6285): the catalogue's `markets` array
// reaches the till (it decides an auto-install, so a till that drops it can
// never tell "explicitly for this market" from "every market"), and the
// till can ask the catalogue for one market via the `markets` query key.

func TestPluginSummary_DecodesMarkets(t *testing.T) {
	var p PluginSummary
	raw := `{"id":"l1","type":"tax","trustLevel":"official","markets":["GB","IE"]}`
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(p.Markets) != 2 || p.Markets[0] != "GB" || p.Markets[1] != "IE" {
		t.Fatalf("Markets = %v, want [GB IE]", p.Markets)
	}
	var none PluginSummary
	if err := json.Unmarshal([]byte(`{"id":"l2","type":"tax"}`), &none); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(none.Markets) != 0 {
		t.Fatalf("a listing with no markets must decode to none (= every market), got %v", none.Markets)
	}
}

func TestListPlugins_SendsMarketsFilter(t *testing.T) {
	withHostVersion(t, "0.9.4")
	var q url.Values
	c := compatTestClient(t, nil, &q)
	if _, err := c.ListPlugins(context.Background(), &ListPluginsRequest{Capability: []string{"tax"}, Market: "GB"}); err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if got := q.Get("markets"); got != "GB" {
		t.Errorf("markets = %q, want GB (query: %v)", got, q)
	}
	var q2 url.Values
	c2 := compatTestClient(t, nil, &q2)
	if _, err := c2.ListPlugins(context.Background(), &ListPluginsRequest{}); err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if q2.Has("markets") {
		t.Errorf("no market asked: the key must be absent (query: %v)", q2)
	}
}
