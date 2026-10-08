package pages

import (
	"encoding/json"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ADR-0121 §5 / ut-docs#3158 AC: each core read view returns what today's
// Ask tool returns on the same fixture DB, with the same arguments — the
// AI plugin (#2851) swaps one for the other without the model noticing.
// The only intended difference: an empty result is "[]" from a view where
// the tool's nil slice marshals to "null".
func TestCoreViewsMatchAskTools(t *testing.T) {
	_, dp, db := newAskAPITestDeps(t)
	ctx := t.Context()
	for _, q := range []string{
		`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('itm2','PEAR','Pear',100,'tax_std',1)`,
		`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('itm3','PLUM','Plum',50,'tax_std',1)`,
		`INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,20,120,datetime('now'))`,
		`INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s2','R002','completed','sale','GBP',300,0,60,360,datetime('now','-3 days'))`,
		`INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s3','R003','completed','sale','GBP',50,0,10,60,datetime('now','-40 days'))`,
		// 02:00 yesterday (local): before the 04:30 business-day start, so
		// it belongs to the day before yesterday — only if the view reads
		// the setting.
		`INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s4','R004','completed','sale','GBP',70,0,14,84,datetime('now','localtime','start of day','-1 day','+2 hours','utc'))`,
		`INSERT INTO sale_lines(id,sale_id,line_no,name_snapshot,quantity,unit_price,tax_rate_bp,tax_amount,total_before_tax,total_after_tax,item_id) VALUES('l1','s1',1,'Apple',1,100,2000,20,100,120,'itm1')`,
		`INSERT INTO sale_lines(id,sale_id,line_no,name_snapshot,quantity,unit_price,tax_rate_bp,tax_amount,total_before_tax,total_after_tax,item_id) VALUES('l2','s2',1,'Pear',3,100,2000,60,300,360,'itm2')`,
		`INSERT INTO sale_lines(id,sale_id,line_no,name_snapshot,quantity,unit_price,tax_rate_bp,tax_amount,total_before_tax,total_after_tax,item_id) VALUES('l3','s3',1,'Plum',1,50,2000,10,50,60,'itm3')`,
		`INSERT INTO payments(id,sale_id,method_id,amount,currency,paid_at) VALUES('p1','s1','cash',120,'GBP',datetime('now'))`,
		`INSERT INTO payments(id,sale_id,method_id,amount,currency,paid_at) VALUES('p2','s2','card',360,'GBP',datetime('now','-3 days'))`,
		`INSERT INTO audit_log(id,actor_id,entity_type,entity_id,action,created_at) VALUES('a1',NULL,'sale','s1','void',datetime('now'))`,
		`INSERT INTO audit_log(id,actor_id,entity_type,entity_id,action,created_at) VALUES('a2',NULL,'till','-','no_sale',datetime('now','-20 days'))`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	// A non-midnight business day start: the view must read the same
	// setting the Ask handler passes to askTools.
	if err := dp.Settings.Set(ctx, keyReportsBusinessDayStart, "04:30"); err != nil {
		t.Fatal(err)
	}

	tools := map[string]func(map[string]any) (any, error){}
	for _, tool := range askTools(data.NewPOSRepo(db), 4, 30) {
		tools[tool.Name] = func(args map[string]any) (any, error) { return tool.Run(ctx, args) }
	}

	cases := []struct {
		view, tool string
		args       map[string]int
	}{
		{"sales.by_day.v1", "sales_by_day", map[string]int{"days": 14}},
		{"sales.by_day.v1", "sales_by_day", map[string]int{"days": 365}},
		{"sales.by_day.v1", "sales_by_day", map[string]int{"days": 1}},
		{"items.top.v1", "top_items", map[string]int{"days": 14, "limit": 10}},
		{"items.top.v1", "top_items", map[string]int{"days": 365, "limit": 1}},
		{"payments.breakdown.v1", "payment_breakdown", map[string]int{"days": 14}},
		{"payments.breakdown.v1", "payment_breakdown", map[string]int{"days": 1}},
		{"stock.levels.v1", "stock_levels", map[string]int{}},
		{"audit.summary.v1", "till_activity_summary", map[string]int{"days": 14}},
		{"audit.summary.v1", "till_activity_summary", map[string]int{"days": 30}},
	}
	for _, c := range cases {
		t.Run(c.view, func(t *testing.T) {
			v, ok := data.LookupCoreView(c.view)
			if !ok {
				t.Fatalf("view %s not registered", c.view)
			}
			// The view takes its raw JSON args through ParseArgs, the tool
			// takes decoded JSON (numbers as float64).
			raw, _ := json.Marshal(c.args)
			parsed, err := v.ParseArgs(raw)
			if err != nil {
				t.Fatalf("ParseArgs(%s): %v", raw, err)
			}
			got, err := data.RunCoreView(ctx, db, v, parsed, data.CoreViewMaxResult)
			if err != nil {
				t.Fatalf("view %s: %v", c.view, err)
			}
			var toolArgs map[string]any
			_ = json.Unmarshal(raw, &toolArgs)
			res, err := tools[c.tool](toolArgs)
			if err != nil {
				t.Fatalf("tool %s: %v", c.tool, err)
			}
			want, _ := json.Marshal(res)
			if string(want) == "null" {
				want = []byte("[]")
			}
			if string(got) != string(want) {
				t.Fatalf("view %s%s\n got  %s\n want %s (tool %s)", c.view, raw, got, want, c.tool)
			}
			if string(got) == "[]" {
				t.Fatalf("view %s%s returned nothing; the fixture should give it rows", c.view, raw)
			}
		})
	}
}
