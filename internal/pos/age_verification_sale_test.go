package pos

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#3340: the age_verifications rows ride in the SAME transaction as
// the sale (pos.CompleteSale), keyed by the real new sales.id — never
// written before the sale exists, and never left behind by a sale that
// rolled back. Real migrated schema (setupVoucherDB), same reasoning as
// voucher_sale_test.go.

func ageSaleInput(checks ...AgeCheck) SaleInput {
	return SaleInput{
		SaleType:               "sale",
		Currency:               "GBP",
		TaxInclusive:           true,
		Lines:                  []SaleLineInput{articleLine()},
		Payments:               []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1000)}},
		CashierID:              "system",
		ActorID:                "system",
		AllowNegativeInventory: true,
		AgeVerifications:       checks,
	}
}

func TestCompleteSale_PersistsAgeVerificationsWithRealSaleID(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)

	saleID, err := CompleteSale(ctx, sqlDB, ageSaleInput(
		AgeCheck{ItemID: "itm1", ItemName: "Coffee Beans", Outcome: AgeVerificationAccepted, CashierID: "system"},
		// No verifier recorded: falls back to the sale's own cashier.
		AgeCheck{ItemID: "", ItemName: "Removed cider", Outcome: AgeVerificationRefused},
	))
	if err != nil {
		t.Fatalf("CompleteSale: %v", err)
	}
	rows, err := sqlDB.Query(`SELECT sale_id, COALESCE(item_id,''), item_name, outcome, cashier_id, created_at FROM age_verifications ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type row struct{ sale, item, name, outcome, cashier, at string }
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.sale, &r.item, &r.name, &r.outcome, &r.cashier, &r.at); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 age_verifications rows, got %d: %+v", len(got), got)
	}
	if got[0].sale != saleID || got[1].sale != saleID {
		t.Fatalf("every row must carry the real new sale id %s: %+v", saleID, got)
	}
	if got[0].item != "itm1" || got[0].name != "Coffee Beans" || got[0].outcome != "accepted" || got[0].cashier != "system" {
		t.Fatalf("row 0: %+v", got[0])
	}
	if got[1].outcome != "refused" || got[1].cashier != "system" || got[1].name != "Removed cider" {
		t.Fatalf("row 1 (cashier fallback): %+v", got[1])
	}
	var saleCreated string
	if err := sqlDB.QueryRow(`SELECT created_at FROM sales WHERE id = ?`, saleID).Scan(&saleCreated); err != nil {
		t.Fatal(err)
	}
	if got[0].at != saleCreated {
		t.Fatalf("verification created_at %q must equal the sale's own %q", got[0].at, saleCreated)
	}
}

// An invalid outcome is refused before anything is written — no sale, no
// verification row.
func TestCompleteSale_RejectsInvalidAgeVerificationOutcome(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)
	_, err := CompleteSale(ctx, sqlDB, ageSaleInput(AgeCheck{ItemID: "itm1", ItemName: "Coffee Beans", Outcome: "maybe"}))
	if err == nil {
		t.Fatal("an outcome outside accepted|refused must fail the sale")
	}
	var sales, checks int
	_ = sqlDB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&sales)
	_ = sqlDB.QueryRow(`SELECT COUNT(*) FROM age_verifications`).Scan(&checks)
	if sales != 0 || checks != 0 {
		t.Fatalf("nothing may persist: sales=%d checks=%d", sales, checks)
	}
}

// A sale that fails inside the transaction leaves no orphan verification:
// the rows share the sale's transaction. Forced here with a verification
// whose item_id breaks its FOREIGN KEY — the whole sale must roll back.
func TestCompleteSale_AgeVerificationRollsBackWithSale(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)
	_, err := CompleteSale(ctx, sqlDB, ageSaleInput(AgeCheck{ItemID: "no-such-item", ItemName: "Ghost", Outcome: AgeVerificationAccepted, CashierID: "system"}))
	if err == nil {
		t.Fatal("a verification row that cannot be written must fail the sale")
	}
	var sales, checks int
	_ = sqlDB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&sales)
	_ = sqlDB.QueryRow(`SELECT COUNT(*) FROM age_verifications`).Scan(&checks)
	if sales != 0 || checks != 0 {
		t.Fatalf("sale and verification must roll back together: sales=%d checks=%d", sales, checks)
	}
}
