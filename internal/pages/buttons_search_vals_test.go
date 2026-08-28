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

// hxOnRe extracts the hx-on attribute on the same search-result button.
var hxOnRe = regexp.MustCompile(`hx-on="([^"]*)"`)

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

// TestButtonsSearchResultHidesDropdownOnlyOnSuccess (ut-docs#1220) pins the
// client half of the fix, which no Go test otherwise reaches. The reported
// symptom was not the 400 itself but that the 400 looked like a success:
// the search-result button's hx-on hid the dropdown on htmx:afterRequest
// unconditionally, so a rejected add closed the dropdown, showed nothing,
// and read as a dead tap. The hide must now be gated on
// event.detail.successful (htmx sets it on the afterRequest detail — false
// for a 4xx/5xx, undefined on a transport failure, both falsy), so on a
// failure the dropdown stays open next to the error message.
func TestButtonsSearchResultHidesDropdownOnlyOnSuccess(t *testing.T) {
	mux, d := newButtonsMux(t)

	if _, err := d.Db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm-hide','HIDE-1','Hide Probe', 5, 1)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}

	rec := postForm(mux, "/api/buttons/search", url.Values{"q": {"Hide Probe"}}, nil)
	if rec.Code != 200 {
		t.Fatalf("search = %d (%s)", rec.Code, rec.Body.String())
	}
	m := hxOnRe.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("no hx-on attribute on the search result: %s", rec.Body.String())
	}
	handler := html.UnescapeString(m[1])
	gate := strings.Index(handler, "event.detail.successful")
	if gate == -1 {
		t.Fatalf("hx-on does not gate on event.detail.successful: %q", handler)
	}
	hide := strings.Index(handler, "display='none'")
	if hide == -1 {
		t.Fatalf("hx-on no longer hides the search dropdown at all: %q", handler)
	}
	if hide < gate {
		t.Fatalf("hx-on hides the dropdown before checking event.detail.successful — a failed add would close it silently again: %q", handler)
	}
}
