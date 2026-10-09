package data

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// catalog.items.v1 (ADR-0149 §6, ut-docs#3698): the catalog list a plugin
// matches against — the AI plugin's camera identify (#2851) needs the SKU
// for add_to_basket, so the view carries it next to ADR-0149's fields.
func TestCatalogItemsView(t *testing.T) {
	dbo, err := db.Open(testsupport.MigratedDBFile(t, "catalog-view.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbo.Close()
	ctx := context.Background()

	mustExec(t, dbo, `INSERT INTO categories(id, name) VALUES('c1', 'Drinks')`)
	mustExec(t, dbo, `INSERT INTO items(id, sku, name, category_id, base_price, is_active, unit, is_weighed) VALUES
		('i1', 'OL-1', 'oat latte', 'c1', 350, 1, 'each', 0),
		('i2', NULL,   'Apple pie', NULL, 425, 0, 'kg', 1),
		('i3', 'BR-1', 'Brownie',   NULL, 300, 1, 'each', 0)`)

	v, ok := LookupCoreView("catalog.items.v1")
	if !ok {
		t.Fatal("catalog.items.v1 not registered")
	}
	if v.Permission != "view:inventory" {
		t.Fatalf("permission = %q, want view:inventory", v.Permission)
	}

	args, err := v.ParseArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if args["offset"] != 0 || args["limit"] != 500 {
		t.Fatalf("default args = %v, want offset 0, limit 500", args)
	}
	out, err := RunCoreView(ctx, dbo.DB, v, args, CoreViewMaxResult)
	if err != nil {
		t.Fatal(err)
	}
	// Name order is case-insensitive; inactive items are listed with
	// active=false (the plugin decides), a missing SKU or category is "",
	// and a weighed item's price is per its unit.
	want := `[` +
		`{"id":"i2","sku":"","name":"Apple pie","category_id":"","category":"","price_minor":425,"unit":"kg","weighed":true,"active":false},` +
		`{"id":"i3","sku":"BR-1","name":"Brownie","category_id":"","category":"","price_minor":300,"unit":"each","weighed":false,"active":true},` +
		`{"id":"i1","sku":"OL-1","name":"oat latte","category_id":"c1","category":"Drinks","price_minor":350,"unit":"each","weighed":false,"active":true}]`
	if string(out) != want {
		t.Fatalf("result:\n got  %s\n want %s", out, want)
	}

	// Paging: offset/limit walk the same order without overlap.
	var page []CatalogViewItem
	for off := 0; off < 3; off++ {
		out, err := RunCoreView(ctx, dbo.DB, v, map[string]int{"offset": off, "limit": 1}, CoreViewMaxResult)
		if err != nil {
			t.Fatal(err)
		}
		var rows []CatalogViewItem
		if err := json.Unmarshal(out, &rows); err != nil || len(rows) != 1 {
			t.Fatalf("offset %d: %s (%v)", off, out, err)
		}
		page = append(page, rows[0])
	}
	if got := fmt.Sprintf("%s %s %s", page[0].ID, page[1].ID, page[2].ID); got != "i2 i3 i1" {
		t.Fatalf("paged order = %s, want i2 i3 i1", got)
	}
	out, err = RunCoreView(ctx, dbo.DB, v, map[string]int{"offset": 3, "limit": 500}, CoreViewMaxResult)
	if err != nil || string(out) != "[]" {
		t.Fatalf("past the end = %s, %v; want []", out, err)
	}
}

func TestCatalogItemsViewArgs(t *testing.T) {
	v, _ := LookupCoreView("catalog.items.v1")
	for _, raw := range []string{`{"offset":1000000,"limit":1}`, `{"offset":0,"limit":500}`} {
		if _, err := v.ParseArgs([]byte(raw)); err != nil {
			t.Errorf("ParseArgs(%s): %v", raw, err)
		}
	}
	for _, raw := range []string{`{"offset":-1}`, `{"offset":1000001}`, `{"limit":0}`, `{"limit":501}`, `{"days":7}`} {
		if _, err := v.ParseArgs([]byte(raw)); err == nil {
			t.Errorf("ParseArgs(%s) accepted, want an error", raw)
		}
	}
}
