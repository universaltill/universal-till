package pages

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
)

// A channel order is not a till sale (ADR-0169 §9, owner question 6): it
// must leave the drawer, the Z report and the fiscal journal exactly as
// they were. ut-docs#4768.

func tableCounts(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		names = append(names, n)
	}
	rows.Close()
	out := map[string]int{}
	for _, n := range names {
		var c int
		if err := db.QueryRow(`SELECT COUNT(*) FROM "` + n + `"`).Scan(&c); err != nil {
			t.Fatal(err)
		}
		out[n] = c
	}
	return out
}

func TestCloudApplyChannelOrder_LeavesZReportDrawerAndFiscalJournalUnchanged(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	x := func(q string, a ...any) {
		t.Helper()
		if _, err := dp.Db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	x(`INSERT INTO tax_codes(id,name,rate_basis_points,is_active) VALUES('tc-co','Std',2000,1)`)
	x(`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('co-itm','CO-1','Shirt',3500,'tc-co',1)`)
	x(`INSERT INTO inventory(id,item_id,location_id,quantity) VALUES('co-inv','co-itm','loc_main',10)`)
	x(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('co-sale','CO-R1','completed','sale','GBP',3500,0,583,3500,?)`,
		time.Now().UTC().Format(time.RFC3339))

	pos := data.NewPOSRepo(dp.Db)
	from, to := time.Now().Add(-48*time.Hour), time.Now().Add(48*time.Hour)
	zBefore, err := pos.EndOfDayInstant(t.Context(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	journalBefore, err := pos.ListRecentSales(t.Context(), 50)
	if err != nil {
		t.Fatal(err)
	}
	countsBefore := tableCounts(t, dp.Db)

	o, bad := decodeFixtureForHook(t)
	if bad != "" {
		t.Fatal(bad)
	}
	o.Lines[0].CloudItemID, o.Lines[0].SKU = "co-itm", "CO-1"
	msg, err := cloudApplyChannelOrder(t.Context(), dp, o)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(msg, "#1001") {
		t.Fatalf("message = %q", msg)
	}

	zAfter, err := pos.EndOfDayInstant(t.Context(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	// GeneratedAt is time.Now at second resolution: not Z content (ut-docs#5137).
	zBefore.GeneratedAt, zAfter.GeneratedAt = "", ""
	if !reflect.DeepEqual(zBefore, zAfter) {
		t.Errorf("Z report changed:\nbefore %+v\nafter  %+v", zBefore, zAfter)
	}
	journalAfter, err := pos.ListRecentSales(t.Context(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(journalBefore, journalAfter) {
		t.Errorf("sales journal changed")
	}
	// Every table a till sale, the drawer or the fiscal journal writes must
	// have the same row count; only the channel tables, stock and audit move.
	moves := map[string]bool{"channel_orders": true, "channel_order_lines": true, "stock_movements": true, "inventory": true, "audit_log": true}
	for tbl, n := range tableCounts(t, dp.Db) {
		if !moves[tbl] && n != countsBefore[tbl] {
			t.Errorf("table %s: %d -> %d rows; a channel order must not write it", tbl, countsBefore[tbl], n)
		}
	}
	var q float64
	_ = dp.Db.QueryRow(`SELECT quantity FROM inventory WHERE id='co-inv'`).Scan(&q)
	if q != 8 {
		t.Errorf("stock = %v, want 8", q)
	}
	// Redelivery: no second movement.
	if _, err := cloudApplyChannelOrder(t.Context(), dp, o); err != nil {
		t.Fatal(err)
	}
	_ = dp.Db.QueryRow(`SELECT quantity FROM inventory WHERE id='co-inv'`).Scan(&q)
	if q != 8 {
		t.Errorf("stock after redelivery = %v, want 8", q)
	}
}

func TestCloudApplyChannelOrder_RaisesProblemsButNeverRefuses(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	logging.ResetRecent()
	for _, q := range []string{
		`INSERT INTO tax_codes(id,name,rate_basis_points,is_active) VALUES('tc-co','Std',2000,1)`,
		`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('co-itm','CO-1','Shirt',3500,'tc-co',1)`,
		`INSERT INTO inventory(id,item_id,location_id,quantity) VALUES('co-inv','co-itm','loc_main',1)`,
	} {
		if _, err := dp.Db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	o, _ := decodeFixtureForHook(t)
	o.Lines[0].CloudItemID, o.Lines[0].SKU = "co-itm", "CO-1" // 2 sold, 1 on hand; line 2 has no SKU
	o.TotalMismatch = true
	o.Currency = "EUR" // the test shop sells in GBP
	if err := dp.Settings.Set(t.Context(), entitlement.KeyShopCurrency, "GBP"); err != nil {
		t.Fatal(err)
	}
	if _, err := cloudApplyChannelOrder(t.Context(), dp, o); err != nil {
		t.Fatalf("a Problem must not refuse the directive: %v", err)
	}
	var all []string
	for _, p := range logging.OpenProblems(time.Now(), 0) {
		all = append(all, p.Msg)
	}
	text := strings.Join(all, "\n")
	for _, want := range []string{"negative", "matches no item", "does not match its lines", "differs from the shop currency"} {
		if !strings.Contains(text, want) {
			t.Errorf("no Problem containing %q in:\n%s", want, text)
		}
	}
	var mismatch int
	_ = dp.Db.QueryRow(`SELECT currency_mismatch FROM channel_orders`).Scan(&mismatch)
	if mismatch != 1 {
		t.Errorf("currency_mismatch = %d", mismatch)
	}
}

func decodeFixtureForHook(t *testing.T) (cloudsync.ChannelOrderDirective, string) {
	t.Helper()
	return cloudsync.ChannelOrderDirective{ChannelOrderInput: data.ChannelOrderInput{
		DirectiveID: "6f1f8f0e-2b7a-4a37-9d0c-5d1d5f3a0a01", ConnectionID: "0b3b9c34-5c1e-4a3e-8f55-3f7f3a9a7c02",
		PluginKey: "com.universaltill.channel-demo", ExternalOrderID: "o-9001", Revision: 1, OrderNumber: "#1001",
		PlacedAt: "2026-10-22T10:12:00Z", Currency: "GBP", Status: "new",
		Shipping: money.FromMinor(399), Total: money.FromMinor(7599), CustomerCountry: "GB",
		Lines: []data.ChannelOrderLineInput{
			{LineID: "l-1", CloudItemID: "itm1", SKU: "LS-M-BLU", Name: "Shirt", Qty: 2, UnitPrice: money.FromMinor(3500), TaxBP: 2000, TaxIncluded: true},
			{LineID: "l-2", Name: "Gift wrap", Qty: 1, UnitPrice: money.FromMinor(200), TaxBP: 2000, TaxIncluded: true},
		},
	}}, ""
}
