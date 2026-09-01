package pos

import (
	"context"
	"testing"
)

// ut-docs#1181 / ADR-0073 Decision 1 & 6: CompleteSale persists each line's
// own order type and DERIVES the header summary from the lines — a caller
// cannot label a mixed sale "takeaway", and cannot put "mixed" on a line.
func TestCompleteSale_PersistsLineOrderTypeAndDerivesHeader(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Coffee', 1000, 1)`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	line := func(ot string, rate int) SaleLineInput {
		return SaleLineInput{ItemID: "itm1", SKU: "SKU1", Name: "Coffee", Qty: 1, UnitPrice: 1000, TaxRateBasisPoints: rate, LocationID: "loc1", OrderType: ot}
	}
	cases := []struct {
		name       string
		header     string
		lines      []SaleLineInput
		wantHeader string
		wantLines  []string
	}{
		{"mixed derives mixed even if caller says takeaway", OrderTypeTakeaway, []SaleLineInput{line("", 1900), line(OrderTypeTakeaway, 700)}, OrderTypeMixed, []string{"", OrderTypeTakeaway}},
		{"all takeaway", "", []SaleLineInput{line(OrderTypeTakeaway, 700), line(OrderTypeTakeaway, 700)}, OrderTypeTakeaway, []string{OrderTypeTakeaway, OrderTypeTakeaway}},
		{"all dine-in", OrderTypeMixed, []SaleLineInput{line("", 1900)}, "", []string{""}},
		{"legacy header-only takeaway fills omitted lines", OrderTypeTakeaway, []SaleLineInput{line("", 700), line("", 700)}, OrderTypeTakeaway, []string{OrderTypeTakeaway, OrderTypeTakeaway}},
		{"mixed on a line is clamped to dine-in", "", []SaleLineInput{line(OrderTypeMixed, 1900), line(OrderTypeTakeaway, 700)}, OrderTypeMixed, []string{"", OrderTypeTakeaway}},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := SaleInput{SaleType: "sale", Currency: "GBP", OrderType: c.header, Lines: c.lines,
				Payments: []PaymentInput{{MethodID: "cash", Amount: 100000, Currency: "GBP"}}, AllowNegativeInventory: true, ReceiptNo: "R" + string(rune('A'+i))}
			saleID, err := CompleteSale(ctx, db, in)
			if err != nil {
				t.Fatalf("CompleteSale: %v", err)
			}
			var header string
			if err := db.QueryRow(`SELECT order_type FROM sales WHERE id=?`, saleID).Scan(&header); err != nil {
				t.Fatal(err)
			}
			if header != c.wantHeader {
				t.Fatalf("header = %q, want %q", header, c.wantHeader)
			}
			rows, err := db.Query(`SELECT order_type FROM sale_lines WHERE sale_id=? ORDER BY line_no`, saleID)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var got []string
			for rows.Next() {
				var ot string
				_ = rows.Scan(&ot)
				got = append(got, ot)
			}
			if len(got) != len(c.wantLines) {
				t.Fatalf("lines = %v, want %v", got, c.wantLines)
			}
			for j := range got {
				if got[j] != c.wantLines[j] {
					t.Fatalf("line %d = %q, want %q (all: %v)", j, got[j], c.wantLines[j], got)
				}
			}
		})
	}
}
