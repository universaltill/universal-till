package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// seedCompletedMultiChargeSaleForRefund seeds a completed, tax-exclusive sale
// carrying TWO itemized charges with different tax bases (ADR-0062,
// ut-docs#1216): 2 units @ 1000 at 20% (net 2000, line tax 400), a 100
// service charge apportioned at the line rates (basis 0 -> 20% -> tax 20)
// and a 40 tourism levy at a flat 7% basis (tax 3). Tax 423, total 2563.
// The sales row carries what CompleteSale derives for a 2+-charge sale:
// service_charge_amount = the sum (140) and service_charge_tax_basis_bp = 0.
func seedCompletedMultiChargeSaleForRefund(t *testing.T, dp *common.Deps) (saleID, receiptNo string) {
	t.Helper()
	ctx := context.Background()
	saleID, receiptNo = "sale-refund-1216", "R-REFUND-1216"
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO items (id, name, base_price, is_active) VALUES ('itm-1216', 'itm-1216', 1000, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, service_charge_amount, service_charge_tax_basis_bp, created_at, completed_at)
VALUES(?, ?, 'completed', 'sale', 'GBP', 2000, 0, 423, 2563, 140, 0, datetime('now'), datetime('now'))`, saleID, receiptNo); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('line-refund-1216', ?, 1, 'itm-1216', 'Room', 'ROOM', 2, 1000, 2000, 400, 2000, 2400)`, saleID); err != nil {
		t.Fatal(err)
	}
	repo := data.NewPOSRepo(dp.Db)
	if err := repo.InsertSaleCharges(ctx, nil, saleID, []data.SaleCharge{
		{Key: "service_charge", Amount: 100, TaxBasisBP: 0, Base: "net_lines"},
		{Key: "tourism_tax", Label: "Tourism tax", Amount: 40, TaxBasisBP: 700, Base: "net_lines"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES('pay-refund-1216', ?, 'cash', 2563, 'GBP', 0, datetime('now'))`, saleID); err != nil {
		t.Fatal(err)
	}
	return saleID, receiptNo
}

type refundChargeRow struct {
	key, label string
	amount     int64
	basis      int
}

func returnChargeRows(t *testing.T, dp *common.Deps, returnSaleID string) []refundChargeRow {
	t.Helper()
	rows, err := dp.Db.Query(`SELECT key, label, amount_minor, tax_basis_bp FROM sale_charges WHERE sale_id = ? ORDER BY seq`, returnSaleID)
	if err != nil {
		t.Fatalf("query return sale_charges: %v", err)
	}
	defer rows.Close()
	var out []refundChargeRow
	for rows.Next() {
		var r refundChargeRow
		if err := rows.Scan(&r.key, &r.label, &r.amount, &r.basis); err != nil {
			t.Fatalf("scan return sale_charges: %v", err)
		}
		out = append(out, r)
	}
	return out
}

func postMultiChargeRefund(t *testing.T, mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s failed: %d %s", path, rec.Code, rec.Body.String())
	}
	return rec
}

// ut-docs#1216: a partial refund of a sale with two charges at different
// tax bases must prorate EACH charge on its own and tax each at its own
// basis. Before the fix the refund collapsed both into one service_charge
// item of 70 at the sale's scalar basis (0 for any 2+-charge sale), taxing
// the flat-7% levy's share at the line's 20% instead: tax 214, total 1284.
//
// Half of each: service 50 (basis 0 -> 20% = 10), tourism 20 (7% = 1.4 ->
// 1). Tax 200 + 10 + 1 = 211; total 1000 + 70 + 211 = 1281.
func TestPostRefund_MultiChargeSaleProratesEachChargeAtItsOwnBasis(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	_, receiptNo := seedCompletedMultiChargeSaleForRefund(t, dp)

	postMultiChargeRefund(t, mux, "/api/refund", "receipt="+receiptNo+"&qty_0=1")

	var returnID string
	var total, tax, charge int64
	var basis int
	if err := dp.Db.QueryRow(`SELECT id, total, tax_total, service_charge_amount, service_charge_tax_basis_bp FROM sales WHERE sale_type = 'return'`).
		Scan(&returnID, &total, &tax, &charge, &basis); err != nil {
		t.Fatalf("read return sale: %v", err)
	}
	if tax != 211 || total != 1281 {
		t.Fatalf("return tax/total = %d/%d, want 211/1281 (each charge taxed at its own basis)", tax, total)
	}
	if charge != 70 || basis != 0 {
		t.Fatalf("return service_charge_amount/basis = %d/%d, want the sum 70 at basis 0 (2 charges)", charge, basis)
	}
	want := []refundChargeRow{
		{"service_charge", "", 50, 0},
		{"tourism_tax", "Tourism tax", 20, 700},
	}
	got := returnChargeRows(t, dp, returnID)
	if len(got) != len(want) {
		t.Fatalf("return sale_charges = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("return sale_charges[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	var paid int64
	if err := dp.Db.QueryRow(`SELECT p.amount FROM payments p WHERE p.sale_id = ?`, returnID).Scan(&paid); err != nil {
		t.Fatalf("read return payment: %v", err)
	}
	if paid != 1281 {
		t.Fatalf("return payment = %d, want 1281", paid)
	}
}

// The preview must show the same per-charge figure the real refund charges
// (ut-docs#1217's never-disagree rule, now for a multi-charge sale).
func TestRefundPreview_MultiChargeSaleMatchesPostRefund(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	_, receiptNo := seedCompletedMultiChargeSaleForRefund(t, dp)

	rec := postMultiChargeRefund(t, mux, "/api/refund/preview", "receipt="+receiptNo+"&qty_0=1")
	if want := httpx.FormatMoney(1281, "en"); !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("preview = %q, want it to contain %q", rec.Body.String(), want)
	}
}

// Two sequential halves return each charge in full, per key — never more,
// and never one charge's remainder paid out as another's.
func TestPostRefund_MultiChargeSequentialRefundsReturnEachChargeExactly(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	saleID, receiptNo := seedCompletedMultiChargeSaleForRefund(t, dp)

	for i := 0; i < 2; i++ {
		postMultiChargeRefund(t, mux, "/api/refund", "receipt="+receiptNo+"&qty_0=1")
	}
	perKey := map[string]int64{}
	rows, err := dp.Db.Query(`SELECT c.key, SUM(c.amount_minor) FROM sale_charges c JOIN sales s ON s.id = c.sale_id WHERE s.sale_type = 'return' GROUP BY c.key`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var v int64
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		perKey[k] = v
	}
	if perKey["service_charge"] != 100 || perKey["tourism_tax"] != 40 || len(perKey) != 2 {
		t.Fatalf("refunded per key = %v, want service_charge 100 and tourism_tax 40", perKey)
	}
	// The guard the next request would load reads the same per-key figures.
	guard, err := loadRefundGuardState(context.Background(), data.NewPOSRepo(dp.Db), saleID)
	if err != nil {
		t.Fatalf("load guard: %v", err)
	}
	if g := guard.refundedChargeByKey; g["service_charge"] != 100 || g["tourism_tax"] != 40 || len(g) != 2 {
		t.Fatalf("guard.refundedChargeByKey = %v, want service_charge 100 and tourism_tax 40", g)
	}
	var totals int64
	if err := dp.Db.QueryRow(`SELECT SUM(total) FROM sales WHERE sale_type = 'return'`).Scan(&totals); err != nil {
		t.Fatal(err)
	}
	// 1281 + 1281: the levy's 3 tax is 1 + 1 per half (each half rounds its
	// own 1.4) — the same per-request tax rounding every partial refund has.
	if totals != 2562 {
		t.Fatalf("two halves' totals = %d, want 2562", totals)
	}
}

// refundCharges' clamps (ut-docs#1216, extending ut-docs#1215's B1 guard to
// itemized charges): per key against what that key still has unrefunded,
// and on the summed remainder, so a prior return recorded without
// sale_charges rows (a legacy or replayed one) still counts.
func TestRefundCharges_ClampsPerKeyAndOnTheSummedRemainder(t *testing.T) {
	detail := data.SaleDetail{
		ServiceCharge: 140,
		Charges: []data.SaleCharge{
			{Key: "service_charge", Amount: 100, Base: "net_lines"},
			{Key: "tourism_tax", Label: "Tourism tax", Amount: 40, TaxBasisBP: 700, Base: "net_lines"},
		},
	}
	amounts := func(cs []pos.ChargeInput) map[string]int64 {
		out := map[string]int64{}
		for _, c := range cs {
			out[c.Key] = c.Amount.Minor()
		}
		return out
	}
	for _, tc := range []struct {
		name  string
		guard refundGuardState
		num   int64
		want  map[string]int64
	}{
		{"untouched sale, full refund", refundGuardState{}, 1, map[string]int64{"service_charge": 100, "tourism_tax": 40}},
		{"levy already fully refunded", refundGuardState{alreadyRefundedCharge: 40, refundedChargeByKey: map[string]int64{"tourism_tax": 40}}, 1,
			map[string]int64{"service_charge": 100}},
		{"first-listed key already fully refunded", refundGuardState{alreadyRefundedCharge: 100, refundedChargeByKey: map[string]int64{"service_charge": 100}}, 1,
			map[string]int64{"tourism_tax": 40}},
		{"prior return collapsed into one service_charge row", refundGuardState{alreadyRefundedCharge: 70, refundedChargeByKey: map[string]int64{"service_charge": 70}}, 1,
			map[string]int64{"service_charge": 30, "tourism_tax": 40}},
		{"prior return without rows eats the summed remainder", refundGuardState{alreadyRefundedCharge: 120}, 1,
			map[string]int64{"service_charge": 20}},
		{"everything already back", refundGuardState{alreadyRefundedCharge: 140}, 1, map[string]int64{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := amounts(refundCharges(detail, tc.guard, tc.num, 1))
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
	// Each item keeps its own label and basis.
	cs := refundCharges(detail, refundGuardState{}, 1, 2)
	if len(cs) != 2 || cs[1].Label != "Tourism tax" || cs[1].TaxBasisBP != 700 || cs[1].Amount != 20 || cs[0].Amount != 50 {
		t.Fatalf("half refund = %+v, want service 50 and tourism 20 @700 with its label", cs)
	}
}

// A sale with no sale_charges rows (completed before ADR-0062's step 2)
// keeps the scalar path: one service_charge item at the persisted basis.
func TestRefundCharges_LegacySaleWithoutRowsUsesTheScalarPair(t *testing.T) {
	detail := data.SaleDetail{ServiceCharge: 20, ServiceChargeTaxBasisBP: 1000}
	cs := refundCharges(detail, refundGuardState{}, 1, 2)
	if len(cs) != 1 || cs[0].Key != pos.ServiceChargeKey || cs[0].Amount != 10 || cs[0].TaxBasisBP != 1000 {
		t.Fatalf("legacy half refund = %+v, want one service_charge of 10 at basis 1000", cs)
	}
	if cs := refundCharges(detail, refundGuardState{alreadyRefundedCharge: 15}, 1, 1); len(cs) != 1 || cs[0].Amount != 5 {
		t.Fatalf("legacy clamp = %+v, want the 5 still unrefunded", cs)
	}
}
