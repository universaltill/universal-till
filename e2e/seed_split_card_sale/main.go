// Command seed_split_card_sale writes ONE completed sale paid with two traced
// `card` payments (a split bill) for the refund payment-picker spec
// (ut-docs#6648). `card` has no refund subscriber, so no plugin is needed: the
// per-leg logic, the refund_leg_refunds rows and the return all still happen.
// The receipt number goes to $UT_DATA_DIR/e2e-split-card-receipt.txt.
// Idempotent: the fixed sale id is left alone when present.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pos"
)

const saleID = "E2E-SPLIT-CARD"

func main() {
	dataDir := os.Getenv("UT_DATA_DIR")
	if dataDir == "" {
		fatalf("UT_DATA_DIR must be set")
	}
	conn, err := db.Open(filepath.Join(dataDir, "unitill-pos.db"))
	if err != nil {
		fatalf("open db: %v", err)
	}
	defer conn.Close()
	ctx := context.Background()
	repo := data.NewPOSRepo(conn.DB)
	if err := repo.EnsurePaymentMethod(ctx, "card"); err != nil {
		fatalf("ensure card method: %v", err)
	}
	var exists int
	if err := conn.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sales WHERE id = ?`, saleID).Scan(&exists); err != nil {
		fatalf("look up sale: %v", err)
	}
	if exists == 0 {
		// Demo catalogue prices (internal/data/seeddata): itm001 at 20%, itm005 at 0%.
		in := pos.SaleInput{
			SaleType: "sale", SaleID: saleID, Currency: "GBP", TaxInclusive: true,
			CashierID: "system", ActorID: "system", AllowNegativeInventory: true,
			Lines: []pos.SaleLineInput{
				{ItemID: "itm001", Name: "Cola", Qty: 1, UnitPrice: money.FromMinor(120), TaxRateBasisPoints: 2000, LocationID: "loc_main"},
				{ItemID: "itm005", Name: "Orange", Qty: 1, UnitPrice: money.FromMinor(220), TaxRateBasisPoints: 0, LocationID: "loc_main"},
			},
			Payments: []pos.PaymentInput{
				{MethodID: "card", Amount: money.FromMinor(120), TraceID: "e2e_pi_1", MaskedPAN: "VISA •••• 4242"},
				{MethodID: "card", Amount: money.FromMinor(220), TraceID: "e2e_pi_2", MaskedPAN: "MC •••• 4444"},
			},
		}
		if _, err := pos.CompleteSale(ctx, conn.DB, in); err != nil {
			fatalf("complete sale: %v", err)
		}
	}
	receipt, ok, err := repo.GetReceiptNo(ctx, saleID)
	if err != nil || !ok {
		fatalf("receipt of %s: ok=%v err=%v", saleID, ok, err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "e2e-split-card-receipt.txt"), []byte(receipt), 0o600); err != nil {
		fatalf("write receipt file: %v", err)
	}
	fmt.Println("seeded split card sale", receipt)
}

func fatalf(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "seed_split_card_sale: "+f+"\n", a...)
	os.Exit(1)
}
