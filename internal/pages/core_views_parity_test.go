package pages

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// ADR-0121 §5 / ut-docs#3158: each core read view the AI plugin's "Ask
// your till" reads returns what the /reports repo method it wraps returns on
// the same fixture DB, with the same arguments and the shop's business-day
// start — so the plugin's answers agree with the Reports page. (The built-in
// Ask tools this used to compare against were deleted in ut-docs#2851; the
// repo calls below are exactly what they ran.) The only intended
// difference: an empty result is "[]" from a view where a nil slice
// marshals to "null".
func TestCoreViewsMatchReportRepos(t *testing.T) {
	dp, db := newSeededPagesDeps(t)
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
	// setting /reports groups by.
	if err := dp.Settings.Set(ctx, keyReportsBusinessDayStart, "04:30"); err != nil {
		t.Fatal(err)
	}

	repo := data.NewPOSRepo(db)
	// window is the rolling "last N days" the views use: [now-N days,
	// now+1s) — the +1s pad because SQL window comparisons truncate to
	// whole seconds (see reportNow in reports_page.go).
	window := func(days int) (time.Time, time.Time) {
		to := time.Now().Add(time.Second)
		return to.Add(-time.Duration(days) * 24 * time.Hour), to
	}
	tools := map[string]func(ctx context.Context, args map[string]int) (any, error){
		"sales_by_day": func(ctx context.Context, a map[string]int) (any, error) {
			from, to := window(a["days"])
			return repo.SalesByDay(ctx, from, to, 4, 30)
		},
		"top_items": func(ctx context.Context, a map[string]int) (any, error) {
			from, to := window(a["days"])
			return repo.TopItems(ctx, from, to, a["limit"])
		},
		"payment_breakdown": func(ctx context.Context, a map[string]int) (any, error) {
			from, to := window(a["days"])
			return repo.PaymentBreakdown(ctx, from, to)
		},
		"stock_levels": func(ctx context.Context, _ map[string]int) (any, error) {
			return repo.ListStockLevels(ctx)
		},
		"till_activity_summary": func(ctx context.Context, a map[string]int) (any, error) {
			return repo.AuditActionSummary(ctx, a["days"], 100)
		},
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
			raw, _ := json.Marshal(c.args)
			parsed, err := v.ParseArgs(raw)
			if err != nil {
				t.Fatalf("ParseArgs(%s): %v", raw, err)
			}
			got, err := data.RunCoreView(ctx, db, v, parsed, data.CoreViewMaxResult)
			if err != nil {
				t.Fatalf("view %s: %v", c.view, err)
			}
			res, err := tools[c.tool](ctx, c.args)
			if err != nil {
				t.Fatalf("repo %s: %v", c.tool, err)
			}
			want, _ := json.Marshal(res)
			if string(want) == "null" {
				want = []byte("[]")
			}
			if string(got) != string(want) {
				t.Fatalf("view %s%s\n got  %s\n want %s (repo %s)", c.view, raw, got, want, c.tool)
			}
			if string(got) == "[]" {
				t.Fatalf("view %s%s returned nothing; the fixture should give it rows", c.view, raw)
			}
		})
	}
}
