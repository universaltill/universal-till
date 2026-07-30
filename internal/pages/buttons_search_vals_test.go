package pages

import (
	"encoding/json"
	"html"
	"net/url"
	"regexp"
	"testing"
)

// hxValsRe extracts the hx-vals attribute the Designer's search results
// carry — htmx parses that attribute's value with JSON.parse before
// posting it to /api/buttons/add.
var hxValsRe = regexp.MustCompile(`hx-vals='([^']*)'`)

// TestButtonsSearchHxValsSurvivesQuotedNames guards the add-as-button flow
// for items whose name (or barcode/image path) contains a double quote:
// the template interpolates the raw name into a JSON literal inside an
// HTML attribute, html/template escapes `"` as `&#34;`, and the browser
// decodes that back to a literal `"` INSIDE the JSON string before htmx
// calls JSON.parse — invalid JSON, so tapping the search result silently
// posts no fields and the add fails with 400. The assertion below does
// exactly what the browser+htmx do: decode HTML entities, then JSON-parse.
func TestButtonsSearchHxValsSurvivesQuotedNames(t *testing.T) {
	mux, d := newButtonsMux(t)

	if _, err := d.Db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itmq','BLADE5','5" Blade', 700, 1)`); err != nil {
		t.Fatalf("seed quoted item: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES('QB123','itmq',1)`); err != nil {
		t.Fatalf("seed barcode: %v", err)
	}

	rec := postForm(mux, "/api/buttons/search", url.Values{"q": {"Blade"}}, nil)
	if rec.Code != 200 {
		t.Fatalf("search = %d (%s)", rec.Code, rec.Body.String())
	}
	m := hxValsRe.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("no hx-vals attribute in search results: %s", rec.Body.String())
	}
	decoded := html.UnescapeString(m[1]) // what the browser hands htmx
	var vals map[string]string
	if err := json.Unmarshal([]byte(decoded), &vals); err != nil {
		t.Fatalf("hx-vals is not valid JSON after browser decoding (htmx JSON.parse would fail, add-button breaks for quoted names): %v\nattr: %s", err, decoded)
	}
	if vals["label"] != `5" Blade` {
		t.Fatalf("label = %q, want %q", vals["label"], `5" Blade`)
	}
	if vals["itemId"] != "itmq" || vals["code"] != "QB123" {
		t.Fatalf("unexpected vals: %#v", vals)
	}
}
