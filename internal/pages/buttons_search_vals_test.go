package pages

import (
	"encoding/json"
	"html"
	"net/url"
	"regexp"
	"strings"
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

// TestButtonsSearchHxValsFallsBackToSKUForBarcodeLessItem (ut-docs#1220):
// the Designer's search finds a SKU-only item (loose produce, services — no
// item_barcodes row) fine, since SearchItemsForShortcuts' WHERE clause
// already matches "i.sku LIKE ?" -- but the search-result button's hx-vals
// used to post code="" for exactly this item (Barcode was the only source
// for "code", and it's empty), and ButtonStore.Add rejects an empty code as
// a 400, silently breaking "add as button" for any item found only by SKU.
func TestButtonsSearchHxValsFallsBackToSKUForBarcodeLessItem(t *testing.T) {
	mux, d := newButtonsMux(t)

	if _, err := d.Db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm-sku-only','SKU-ONLY-1','Loose Screw', 5, 1)`); err != nil {
		t.Fatalf("seed sku-only item: %v", err)
	}
	// Deliberately no item_barcodes row for itm-sku-only.

	rec := postForm(mux, "/api/buttons/search", url.Values{"q": {"Loose Screw"}}, nil)
	if rec.Code != 200 {
		t.Fatalf("search = %d (%s)", rec.Code, rec.Body.String())
	}
	m := hxValsRe.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("no hx-vals attribute in search results: %s", rec.Body.String())
	}
	decoded := html.UnescapeString(m[1])
	var vals map[string]string
	if err := json.Unmarshal([]byte(decoded), &vals); err != nil {
		t.Fatalf("hx-vals is not valid JSON: %v\nattr: %s", err, decoded)
	}
	if vals["code"] != "SKU-ONLY-1" {
		t.Fatalf("code = %q, want SKU fallback %q (barcode-less item must still post an add-able code)", vals["code"], "SKU-ONLY-1")
	}
	if vals["itemId"] != "itm-sku-only" {
		t.Fatalf("itemId = %q, want itm-sku-only", vals["itemId"])
	}

	// End-to-end: posting that exact code succeeds and the tile appears —
	// this is the actual acceptance criterion, not just the JSON shape.
	addRec := postForm(mux, "/api/buttons/add", url.Values{
		"label":  {vals["label"]},
		"code":   {vals["code"]},
		"itemId": {vals["itemId"]},
	}, nil)
	if addRec.Code != 200 {
		t.Fatalf("add SKU-only item = %d, want 200 (%s)", addRec.Code, addRec.Body.String())
	}
	if !strings.Contains(addRec.Body.String(), "Loose Screw") {
		t.Fatalf("admin grid response missing added SKU-only tile: %s", addRec.Body.String())
	}
}
