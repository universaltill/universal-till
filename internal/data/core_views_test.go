package data

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// ADR-0121 §5 (ut-docs#3158): the first core read views are today's Ask
// tools, each gated by a view:<class> permission.
func TestCoreViewRegistry_FirstSet(t *testing.T) {
	want := map[string]string{
		"sales.by_day.v1":       "view:sales",
		"items.top.v1":          "view:sales",
		"payments.breakdown.v1": "view:sales",
		"stock.levels.v1":       "view:inventory",
		"audit.summary.v1":      "view:audit",
		// ADR-0149 §6 (ut-docs#3698) extends ADR-0121 §5's first set.
		"catalog.items.v1": "view:inventory",
		// ADR-0149 §6 (ut-docs#3976).
		"users.list.v1":     "view:users",
		"sales.receipts.v1": "view:sales",
	}
	if len(coreViews) != len(want) {
		t.Fatalf("%d views registered, want the %d of ADR-0121 §5 + ADR-0149 §6", len(coreViews), len(want))
	}
	for name, perm := range want {
		v, ok := LookupCoreView(name)
		if !ok {
			t.Errorf("view %s not registered", name)
			continue
		}
		if v.Name != name || v.Permission != perm {
			t.Errorf("view %s = {%s %s}, want permission %s", name, v.Name, v.Permission, perm)
		}
		if v.Run == nil {
			t.Errorf("view %s has no Run", name)
		}
	}
	if _, ok := LookupCoreView("sales.by_day.v2"); ok {
		t.Error("an unregistered version resolved")
	}
	if _, ok := LookupCoreView(""); ok {
		t.Error("an empty name resolved")
	}
}

func TestCoreViewArgs(t *testing.T) {
	top, _ := LookupCoreView("items.top.v1")
	stock, _ := LookupCoreView("stock.levels.v1")

	ok := []struct {
		name string
		view CoreView
		raw  string
		want map[string]int
	}{
		{"empty means defaults", top, "", map[string]int{"days": 14, "limit": 10}},
		{"empty object means defaults", top, "{}", map[string]int{"days": 14, "limit": 10}},
		{"null means defaults", top, "null", map[string]int{"days": 14, "limit": 10}},
		{"lower bounds", top, `{"days":1,"limit":1}`, map[string]int{"days": 1, "limit": 1}},
		{"upper bounds", top, `{"days":365,"limit":50}`, map[string]int{"days": 365, "limit": 50}},
		{"one given, one default", top, `{"limit":5}`, map[string]int{"days": 14, "limit": 5}},
		{"integral float", top, `{"days":7.0}`, map[string]int{"days": 7, "limit": 10}},
		{"no-arg view", stock, `{}`, map[string]int{}},
	}
	for _, c := range ok {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.view.ParseArgs([]byte(c.raw))
			if err != nil {
				t.Fatalf("ParseArgs(%q): %v", c.raw, err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("ParseArgs(%q) = %v, want %v", c.raw, got, c.want)
			}
			for k, v := range c.want {
				if got[k] != v {
					t.Fatalf("ParseArgs(%q)[%s] = %d, want %d", c.raw, k, got[k], v)
				}
			}
		})
	}

	bad := []struct {
		name string
		view CoreView
		raw  string
	}{
		{"days below range", top, `{"days":0}`},
		{"days above range", top, `{"days":366}`},
		{"limit above range", top, `{"limit":51}`},
		{"negative", top, `{"days":-3}`},
		{"fraction", top, `{"days":1.5}`},
		{"string", top, `{"days":"7"}`},
		{"bool", top, `{"days":true}`},
		{"null value", top, `{"days":null}`},
		{"unknown key", top, `{"days":7,"store":"x"}`},
		{"arg on a no-arg view", stock, `{"days":7}`},
		{"array", top, `[7]`},
		{"not json", top, `{days:7}`},
		{"trailing garbage", top, `{"days":7} {}`},
		{"huge", top, `{"days":1e30}`},
		{"exponent", top, `{"days":1e2}`},
		{"exponent fraction", top, `{"days":0.7e1}`},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if got, err := c.view.ParseArgs([]byte(c.raw)); err == nil {
				t.Fatalf("ParseArgs(%q) = %v, want an error", c.raw, got)
			}
		})
	}
}

func TestParseBusinessDayStart(t *testing.T) {
	cases := map[string][2]int{
		"":      {0, 0},
		"06:30": {6, 30},
		"23:59": {23, 59},
		"24:00": {0, 0},
		"6:30":  {0, 0},
		"ab:cd": {0, 0},
	}
	for in, want := range cases {
		h, m := ParseBusinessDayStart(in)
		if h != want[0] || m != want[1] {
			t.Errorf("ParseBusinessDayStart(%q) = %d:%d, want %d:%d", in, h, m, want[0], want[1])
		}
	}
}

// RunCoreView returns a JSON array — "[]", never "null", when nothing
// matches — and refuses a result over its byte cap instead of truncating.
func TestRunCoreView_EmptyIsArrayAndCapRefuses(t *testing.T) {
	db := newAuditTestDB(t)
	ctx := context.Background()
	v, _ := LookupCoreView("audit.summary.v1")

	out, err := RunCoreView(ctx, db, v, map[string]int{"days": 14}, 1<<10)
	if err != nil {
		t.Fatalf("RunCoreView on an empty log: %v", err)
	}
	if string(out) != "[]" {
		t.Fatalf("empty result = %s, want []", out)
	}

	for i := 0; i < 40; i++ {
		if _, err := db.Exec(`INSERT INTO audit_log (id, actor_id, entity_type, entity_id, action) VALUES (?, NULL, 'sale', 'x', ?)`,
			"a"+strings.Repeat("x", i), "action_"+strings.Repeat("y", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := RunCoreView(ctx, db, v, map[string]int{"days": 14}, 256); err != ErrCoreViewTooLarge {
		t.Fatalf("over-cap result: err = %v, want ErrCoreViewTooLarge", err)
	}
	out, err = RunCoreView(ctx, db, v, map[string]int{"days": 14}, 1<<20)
	if err != nil || !strings.HasPrefix(string(out), "[{") {
		t.Fatalf("under-cap result = %.80s…, %v", out, err)
	}
}

// TestCoreViewRowShapesArePinned guards the contract (ut-docs
// reference/contracts/plugin-views.md): the v1 views marshal these repo
// structs, so a JSON field added to one of them for a page (LowStockItem
// gains them often) would silently change a published view. Adding a key
// here means the contract doc changes too — and a changed or removed key
// is a new .v2 view, never an edit to v1.
func TestCoreViewRowShapesArePinned(t *testing.T) {
	pinned := []struct {
		view string
		row  any
		keys string
	}{
		{"sales.by_day.v1", DailySales{}, "day count total tax_total"},
		{"items.top.v1", TopItem{}, "name qty revenue"},
		{"payments.breakdown.v1", MethodTotal{}, "method count amount"},
		{"stock.levels.v1", LowStockItem{}, "item_id name sku location_id location_name current_qty reorder_level lead_time_days variant_id? variant_name? category_id? not_stocked_here?"},
		{"audit.summary.v1", AuditActionCount{}, "actor_id entity_type action count"},
		{"catalog.items.v1", CatalogViewItem{}, "id sku name category_id category price_minor unit weighed active"},
		{"users.list.v1", UserViewRow{}, "id display_name active"},
		{"sales.receipts.v1", ReceiptViewRow{}, "id receipt_no sale_type business_date created_at till_id cashier_id currency subtotal_minor discount_minor tax_minor total_minor tip_minor fiscal_signed lines payments"},
		{"sales.receipts.v1 lines[]", ReceiptViewLine{}, "line_no item_id variant_id name sku quantity unit_price_minor discount_minor tax_minor total_minor"},
		{"sales.receipts.v1 payments[]", ReceiptViewPayment{}, "method_id amount_minor tip_minor"},
	}
	for _, p := range pinned {
		var keys []string
		rt := reflect.TypeOf(p.row)
		for i := 0; i < rt.NumField(); i++ {
			tag := rt.Field(i).Tag.Get("json")
			name, opts, _ := strings.Cut(tag, ",")
			if name == "" || name == "-" {
				t.Errorf("%s: field %s has no JSON name", p.view, rt.Field(i).Name)
				continue
			}
			if strings.Contains(opts, "omitempty") {
				name += "?"
			}
			keys = append(keys, name)
		}
		if got := strings.Join(keys, " "); got != p.keys {
			t.Errorf("%s row keys changed:\n got  %s\n want %s\nupdate reference/contracts/plugin-views.md, or publish a .v2 view", p.view, got, p.keys)
		}
	}
}
