package pos

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/money"
)

// Voucher liability tests (ut-docs#1008). Unlike this package's older
// setupSaleDB fixture (a hand-built schema subset), these open the REAL
// migrated schema via internal/db.Open — the vouchers/voucher_transactions
// tables under test are defined by migration 068, and asserting against a
// hand-copied twin of that schema would prove nothing about the migration
// itself. Every sale goes through the real pos.CompleteSale, never
// hand-inserted fixture rows (the sibling ut-docs#1003 card's review
// requirement).
func setupVoucherDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "voucher.db"))
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	seed := []string{
		`INSERT INTO stock_locations (id, name) VALUES ('loc1', 'Main')`,
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm1', 'SKU1', 'Coffee Beans', 1000, 1)`,
		`INSERT INTO inventory (id, item_id, variant_id, location_id, quantity, updated_at) VALUES ('inv1', 'itm1', NULL, 'loc1', 50, datetime('now'))`,
		// 001_init already seeds a 'cash' method; the 'voucher' id is what
		// the live till ensures at boot (index_page.go's defaults, via
		// EnsurePaymentMethod) — mirror that here.
		`INSERT OR IGNORE INTO payment_methods (id, name, type, is_active) VALUES ('cash', 'Cash', 'cash', 1)`,
		`INSERT OR IGNORE INTO payment_methods (id, name, type, is_active) VALUES ('voucher', 'Voucher', 'voucher', 1)`,
	}
	for _, s := range seed {
		if _, err := d.DB.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
	return d.DB
}

// articleLine is one taxable article line: 10.00 at 19% (inclusive pricing in
// these tests unless stated), the goods a voucher redemption pays for.
func articleLine() SaleLineInput {
	return SaleLineInput{
		ItemID:             "itm1",
		Name:               "Coffee Beans",
		Qty:                1,
		UnitPrice:          money.FromMinor(1000),
		TaxRateBasisPoints: 1900,
		LocationID:         "loc1",
	}
}

func saleRow(t *testing.T, sqlDB *sql.DB, saleID string) (subtotal, taxTotal, total int64) {
	t.Helper()
	if err := sqlDB.QueryRow(`SELECT subtotal, tax_total, total FROM sales WHERE id = ?`, saleID).
		Scan(&subtotal, &taxTotal, &total); err != nil {
		t.Fatalf("read sale %s: %v", saleID, err)
	}
	return subtotal, taxTotal, total
}

// TestCompleteSale_VoucherIssueGolden is the card's golden-file case:
// 1 voucher issued / 15.00 / 0% VAT / posted to liability — meaning the
// vouchers + voucher_transactions tables hold the outstanding balance
// (there is no DATEV posting in this codebase; that's ut-docs#1036).
// A voucher-only sale: no article lines at all.
func TestCompleteSale_VoucherIssueGolden(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)

	saleID, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType:     "sale",
		Currency:     "EUR",
		TaxInclusive: true,
		VoucherIssues: []VoucherIssueInput{{
			VoucherID:   "GS-0001",
			HolderLabel: "Sample Holder",
			Amount:      money.FromMinor(1500),
		}},
		Payments: []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1500)}},
	})
	if err != nil {
		t.Fatalf("CompleteSale (voucher-only): %v", err)
	}

	// The issue is a 0% liability, never revenue: subtotal and tax_total
	// exclude it entirely; the customer's payment obligation (total) carries it.
	subtotal, taxTotal, total := saleRow(t, sqlDB, saleID)
	if subtotal != 0 || taxTotal != 0 {
		t.Fatalf("voucher issue leaked into revenue/tax: subtotal=%d tax_total=%d, want 0/0", subtotal, taxTotal)
	}
	if total != 1500 {
		t.Fatalf("sale total = %d, want 1500 (the voucher amount the customer paid)", total)
	}

	// Posted to liability: one vouchers row with the full outstanding balance…
	var holder, currency, vtype, status, issuedSale string
	var original, balance int64
	if err := sqlDB.QueryRow(`SELECT holder_label, original_amount, balance, currency, voucher_type, status, issued_sale_id FROM vouchers WHERE id = 'GS-0001'`).
		Scan(&holder, &original, &balance, &currency, &vtype, &status, &issuedSale); err != nil {
		t.Fatalf("voucher row not posted to liability: %v", err)
	}
	if holder != "Sample Holder" || original != 1500 || balance != 1500 || currency != "EUR" || vtype != "multi_purpose" || status != "active" || issuedSale != saleID {
		t.Fatalf("voucher row = holder=%q original=%d balance=%d currency=%q type=%q status=%q sale=%q", holder, original, balance, currency, vtype, status, issuedSale)
	}

	// …and exactly one 'issue' transaction row tied to the sale.
	var txCount int
	var txType string
	var txAmount int64
	if err := sqlDB.QueryRow(`SELECT COUNT(*), MAX(type), MAX(amount) FROM voucher_transactions WHERE voucher_id = 'GS-0001' AND sale_id = ?`, saleID).
		Scan(&txCount, &txType, &txAmount); err != nil {
		t.Fatalf("read voucher_transactions: %v", err)
	}
	if txCount != 1 || txType != "issue" || txAmount != 1500 {
		t.Fatalf("voucher_transactions = count=%d type=%q amount=%d, want 1/'issue'/1500", txCount, txType, txAmount)
	}

	// NOT an article: the voucher must never appear as a sale_lines row
	// (that would put it into Artikelumsatz — the exact bug this card fixes).
	var lineCount int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM sale_lines WHERE sale_id = ?`, saleID).Scan(&lineCount); err != nil {
		t.Fatalf("count sale_lines: %v", err)
	}
	if lineCount != 0 {
		t.Fatalf("voucher-only sale wrote %d sale_lines rows, want 0", lineCount)
	}
}

// A mixed sale (article + voucher issue) keeps the article's own
// subtotal/tax exactly as a voucher-free control sale computes them —
// the voucher rides only on total.
func TestCompleteSale_VoucherIssueExcludedFromArticleFigures(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)

	controlID, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		Lines:    []SaleLineInput{articleLine()},
		Payments: []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1000)}},
	})
	if err != nil {
		t.Fatalf("control sale: %v", err)
	}
	mixedID, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		Lines:         []SaleLineInput{articleLine()},
		VoucherIssues: []VoucherIssueInput{{Amount: money.FromMinor(2500)}},
		Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(3500)}},
	})
	if err != nil {
		t.Fatalf("mixed sale: %v", err)
	}

	cSub, cTax, cTotal := saleRow(t, sqlDB, controlID)
	mSub, mTax, mTotal := saleRow(t, sqlDB, mixedID)
	if mSub != cSub || mTax != cTax {
		t.Fatalf("voucher issue changed article figures: subtotal %d->%d, tax_total %d->%d", cSub, mSub, cTax, mTax)
	}
	if mTotal != cTotal+2500 {
		t.Fatalf("mixed total = %d, want control %d + 2500", mTotal, cTotal)
	}

	// An issue with no VoucherID gets a generated stable identifier.
	var generatedID string
	if err := sqlDB.QueryRow(`SELECT voucher_id FROM voucher_transactions WHERE sale_id = ? AND type = 'issue'`, mixedID).Scan(&generatedID); err != nil {
		t.Fatalf("issue tx for mixed sale: %v", err)
	}
	if generatedID == "" {
		t.Fatalf("generated voucher id is empty")
	}
}

// Redemption debits the tracked voucher and records its own transaction row,
// while the goods being paid for keep their own rates — the redeemed sale's
// tax figures are identical to the same sale paid in cash.
func TestCompleteSale_VoucherRedemptionDebitsBalance(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)

	if _, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []VoucherIssueInput{{VoucherID: "GS-R1", Amount: money.FromMinor(1500)}},
		Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1500)}},
	}); err != nil {
		t.Fatalf("issue sale: %v", err)
	}

	cashID, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		Lines:    []SaleLineInput{articleLine()},
		Payments: []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1000)}},
	})
	if err != nil {
		t.Fatalf("cash control sale: %v", err)
	}
	redeemedID, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		Lines:    []SaleLineInput{articleLine()},
		Payments: []PaymentInput{{MethodID: "voucher", VoucherID: "GS-R1", Amount: money.FromMinor(1000)}},
	})
	if err != nil {
		t.Fatalf("redemption sale: %v", err)
	}

	// Redemption changes NOTHING about how the goods are taxed.
	cSub, cTax, cTotal := saleRow(t, sqlDB, cashID)
	rSub, rTax, rTotal := saleRow(t, sqlDB, redeemedID)
	if rSub != cSub || rTax != cTax || rTotal != cTotal {
		t.Fatalf("paying by voucher changed the goods' figures: subtotal %d/%d tax %d/%d total %d/%d", cSub, rSub, cTax, rTax, cTotal, rTotal)
	}

	var balance int64
	var status string
	if err := sqlDB.QueryRow(`SELECT balance, status FROM vouchers WHERE id = 'GS-R1'`).Scan(&balance, &status); err != nil {
		t.Fatalf("read voucher: %v", err)
	}
	if balance != 500 || status != "active" {
		t.Fatalf("voucher after partial redemption: balance=%d status=%q, want 500/'active'", balance, status)
	}

	var redAmount int64
	if err := sqlDB.QueryRow(`SELECT amount FROM voucher_transactions WHERE voucher_id = 'GS-R1' AND type = 'redemption' AND sale_id = ?`, redeemedID).Scan(&redAmount); err != nil {
		t.Fatalf("redemption tx row: %v", err)
	}
	if redAmount != 1000 {
		t.Fatalf("redemption tx amount = %d, want 1000", redAmount)
	}
}

// Draining a voucher to zero flips it to 'redeemed' and blocks further use.
func TestCompleteSale_VoucherFullyRedeemed(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)

	if _, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []VoucherIssueInput{{VoucherID: "GS-F1", Amount: money.FromMinor(1000)}},
		Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1000)}},
	}); err != nil {
		t.Fatalf("issue sale: %v", err)
	}
	if _, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		Lines:    []SaleLineInput{articleLine()},
		Payments: []PaymentInput{{MethodID: "voucher", VoucherID: "GS-F1", Amount: money.FromMinor(1000)}},
	}); err != nil {
		t.Fatalf("full redemption: %v", err)
	}
	var balance int64
	var status string
	if err := sqlDB.QueryRow(`SELECT balance, status FROM vouchers WHERE id = 'GS-F1'`).Scan(&balance, &status); err != nil {
		t.Fatalf("read voucher: %v", err)
	}
	if balance != 0 || status != "redeemed" {
		t.Fatalf("drained voucher: balance=%d status=%q, want 0/'redeemed'", balance, status)
	}

	if _, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		Lines:    []SaleLineInput{articleLine()},
		Payments: []PaymentInput{{MethodID: "voucher", VoucherID: "GS-F1", Amount: money.FromMinor(1000)}},
	}); err == nil {
		t.Fatalf("redeeming a fully-redeemed voucher succeeded, want error")
	}
}

// Overspend is rejected outright (no partial split logic in this card) and
// the whole sale rolls back — no sale row, no debit.
func TestCompleteSale_VoucherRedemptionRejectsOverspend(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)

	if _, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []VoucherIssueInput{{VoucherID: "GS-O1", Amount: money.FromMinor(500)}},
		Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(500)}},
	}); err != nil {
		t.Fatalf("issue sale: %v", err)
	}

	_, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		Lines:    []SaleLineInput{articleLine()},
		Payments: []PaymentInput{{MethodID: "voucher", VoucherID: "GS-O1", Amount: money.FromMinor(1000)}},
	})
	if err == nil {
		t.Fatalf("overspending a voucher succeeded, want error")
	}
	if !strings.Contains(err.Error(), "voucher") {
		t.Fatalf("overspend error should name the voucher problem, got: %v", err)
	}

	var balance int64
	if err := sqlDB.QueryRow(`SELECT balance FROM vouchers WHERE id = 'GS-O1'`).Scan(&balance); err != nil {
		t.Fatalf("read voucher: %v", err)
	}
	if balance != 500 {
		t.Fatalf("failed redemption debited the voucher: balance=%d, want 500", balance)
	}
	var saleCount int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM sales WHERE tender_type = 'voucher'`).Scan(&saleCount); err != nil {
		t.Fatalf("count sales: %v", err)
	}
	if saleCount != 0 {
		t.Fatalf("rejected redemption still persisted %d sale(s)", saleCount)
	}
}

// The pre-existing generic 'voucher' payment type (no VoucherID) keeps
// working exactly as before, with no voucher_transactions record — the new
// tracked redemption is recorded separately from it, and there is no
// retroactive change to how untracked voucher payments behave.
func TestCompleteSale_GenericVoucherPaymentUnchanged(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)

	saleID, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		Lines:    []SaleLineInput{articleLine()},
		Payments: []PaymentInput{{MethodID: "voucher", Amount: money.FromMinor(1000)}},
	})
	if err != nil {
		t.Fatalf("generic voucher payment: %v", err)
	}
	var txCount int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM voucher_transactions`).Scan(&txCount); err != nil {
		t.Fatalf("count voucher_transactions: %v", err)
	}
	if txCount != 0 {
		t.Fatalf("generic voucher payment wrote %d voucher_transactions rows, want 0", txCount)
	}
	var payCount int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM payments WHERE sale_id = ? AND method_id = 'voucher'`, saleID).Scan(&payCount); err != nil {
		t.Fatalf("count payments: %v", err)
	}
	if payCount != 1 {
		t.Fatalf("payments rows = %d, want 1", payCount)
	}
}

// Input validation: a tracked redemption must ride the 'voucher' method,
// never give change, and an issue amount must be positive. A sale with
// neither lines nor a voucher issue stays rejected.
func TestCompleteSale_VoucherValidation(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)

	if _, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []VoucherIssueInput{{VoucherID: "GS-V1", Amount: money.FromMinor(1500)}},
		Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1500)}},
	}); err != nil {
		t.Fatalf("issue sale: %v", err)
	}

	cases := []struct {
		name string
		in   SaleInput
	}{
		{"voucher_id on non-voucher method", SaleInput{
			SaleType: "sale", Currency: "EUR", TaxInclusive: true,
			Lines:    []SaleLineInput{articleLine()},
			Payments: []PaymentInput{{MethodID: "cash", VoucherID: "GS-V1", Amount: money.FromMinor(1000)}},
		}},
		{"change on tracked voucher payment", SaleInput{
			SaleType: "sale", Currency: "EUR", TaxInclusive: true,
			Lines:    []SaleLineInput{articleLine()},
			Payments: []PaymentInput{{MethodID: "voucher", VoucherID: "GS-V1", Amount: money.FromMinor(1100), ChangeGiven: money.FromMinor(100)}},
		}},
		{"non-positive issue amount", SaleInput{
			SaleType: "sale", Currency: "EUR", TaxInclusive: true,
			VoucherIssues: []VoucherIssueInput{{Amount: money.FromMinor(0)}},
			Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(100)}},
		}},
		{"unknown voucher id", SaleInput{
			SaleType: "sale", Currency: "EUR", TaxInclusive: true,
			Lines:    []SaleLineInput{articleLine()},
			Payments: []PaymentInput{{MethodID: "voucher", VoucherID: "GS-NOPE", Amount: money.FromMinor(1000)}},
		}},
		{"no lines and no voucher issue", SaleInput{
			SaleType: "sale", Currency: "EUR", TaxInclusive: true,
			Payments: []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(100)}},
		}},
	}
	for _, tc := range cases {
		if _, err := CompleteSale(ctx, sqlDB, tc.in); err == nil {
			t.Errorf("%s: succeeded, want error", tc.name)
		}
	}
}

// Exclusive pricing: the voucher amount still rides on top of the taxed
// article total without itself being taxed.
func TestCompleteSale_VoucherIssueTaxExclusive(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)

	saleID, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: false,
		Lines:         []SaleLineInput{articleLine()},
		VoucherIssues: []VoucherIssueInput{{Amount: money.FromMinor(1500)}},
		Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(2690)}},
	})
	if err != nil {
		t.Fatalf("exclusive sale: %v", err)
	}
	subtotal, taxTotal, total := saleRow(t, sqlDB, saleID)
	// 10.00 net + 1.90 tax (19% exclusive) + 15.00 voucher = 26.90.
	if subtotal != 1000 || taxTotal != 190 || total != 2690 {
		t.Fatalf("exclusive figures: subtotal=%d tax=%d total=%d, want 1000/190/2690", subtotal, taxTotal, total)
	}
}

// ---------------------------------------------------------------------------
// Independent-review fix pass (ut-docs#1008): regression tests for findings
// F1 (voucher issue broke InferTaxInclusive), F2 (void cascade + reporting),
// F3 (amount ceiling / int64 overflow), F4 (over-tender confiscation),
// F5 (same-sale issue+redeem) and F6 (redemption id validation).
// ---------------------------------------------------------------------------

// F1: an INCLUSIVE-priced sale that also issues a voucher must still be
// inferred as inclusive from its persisted header. Before the fix, the
// voucher's face value sat in `total` with nothing on the other side of
// InferTaxInclusive's identity, so exactly this sale shape was misread as
// exclusive — double-charging VAT on its refunds and mis-banding the
// day-close/invoice VAT tables.
func TestCompleteSale_VoucherIssueKeepsInclusiveInference(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)

	saleID, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		Lines:         []SaleLineInput{articleLine()},
		VoucherIssues: []VoucherIssueInput{{VoucherID: "GS-INF1", Amount: money.FromMinor(1500)}},
		Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(2500)}},
	})
	if err != nil {
		t.Fatalf("CompleteSale: %v", err)
	}

	var subtotal, discount, taxTotal, total, serviceCharge, voucherIssueTotal int64
	if err := sqlDB.QueryRow(`SELECT subtotal, discount_total, tax_total, total, service_charge_amount, voucher_issue_total FROM sales WHERE id = ?`, saleID).
		Scan(&subtotal, &discount, &taxTotal, &total, &serviceCharge, &voucherIssueTotal); err != nil {
		t.Fatalf("read sale header: %v", err)
	}
	if voucherIssueTotal != 1500 {
		t.Fatalf("persisted voucher_issue_total = %d, want 1500", voucherIssueTotal)
	}
	if !InferTaxInclusive(subtotal, discount, taxTotal, total, serviceCharge, voucherIssueTotal) {
		t.Fatalf("inclusive sale with a voucher issue misread as exclusive (subtotal=%d discount=%d tax=%d total=%d sc=%d voucher=%d)",
			subtotal, discount, taxTotal, total, serviceCharge, voucherIssueTotal)
	}

	// Control: the same header WITHOUT the voucher term must NOT read as
	// inclusive — proving the voucher term is what balances the identity,
	// i.e. this is exactly the sale shape the old code broke on.
	if InferTaxInclusive(subtotal, discount, taxTotal, total, serviceCharge, 0) {
		t.Fatalf("control: identity balanced without the voucher term — test would not have caught the regression")
	}
}

// localDayOf returns the shop-local calendar day of the given RFC3339
// timestamp exactly as the voucher range query derives it — so the
// assertions below hit the same window regardless of the host timezone.
func localDayOf(t *testing.T, sqlDB *sql.DB, createdAt string) string {
	t.Helper()
	var day string
	if err := sqlDB.QueryRow(`SELECT date(?, 'localtime')`, createdAt).Scan(&day); err != nil {
		t.Fatalf("derive local day: %v", err)
	}
	return day
}

// F2 (a)+(b): voiding the sale that issued a still-untouched voucher voids
// the voucher with it — no longer spendable — and the day-close GUTSCHEINE
// aggregation stops counting the issue.
func TestUpdateSaleStatus_VoidCascadesToUntouchedVoucher(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)

	saleID, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []VoucherIssueInput{{VoucherID: "GS-VOID1", Amount: money.FromMinor(2000)}},
		Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(2000)}},
	})
	if err != nil {
		t.Fatalf("issue sale: %v", err)
	}

	repo := data.NewPOSRepo(sqlDB)
	var txCreatedAt string
	if err := sqlDB.QueryRow(`SELECT created_at FROM voucher_transactions WHERE voucher_id = 'GS-VOID1'`).Scan(&txCreatedAt); err != nil {
		t.Fatalf("read issue tx: %v", err)
	}
	day := localDayOf(t, sqlDB, txCreatedAt)

	// Control before the void: the issue counts.
	sum, err := repo.VouchersIssuedRedeemedForRange(ctx, day, day)
	if err != nil {
		t.Fatalf("range before void: %v", err)
	}
	if sum.IssuedCount != 1 || sum.IssuedMinor != 2000 {
		t.Fatalf("before void: issued = %d/%d, want 1/2000", sum.IssuedCount, sum.IssuedMinor)
	}

	if err := UpdateSaleStatus(ctx, sqlDB, saleID, "voided", "", "test void"); err != nil {
		t.Fatalf("void sale: %v", err)
	}

	var status string
	var balance int64
	if err := sqlDB.QueryRow(`SELECT status, balance FROM vouchers WHERE id = 'GS-VOID1'`).Scan(&status, &balance); err != nil {
		t.Fatalf("read voucher: %v", err)
	}
	if status != "void" || balance != 0 {
		t.Fatalf("voucher after sale void: status=%q balance=%d, want 'void'/0 — a voided sale left a live, spendable voucher", status, balance)
	}

	// Not redeemable any more: a tender against it must fail and persist
	// nothing.
	if _, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		Lines:    []SaleLineInput{articleLine()},
		Payments: []PaymentInput{{MethodID: "voucher", VoucherID: "GS-VOID1", Amount: money.FromMinor(1000)}},
	}); !errors.Is(err, data.ErrVoucherNotActive) {
		t.Fatalf("redeeming a voided voucher: err = %v, want ErrVoucherNotActive", err)
	}

	// (b) Reporting: the GUTSCHEINE aggregation no longer counts the issue.
	sum, err = repo.VouchersIssuedRedeemedForRange(ctx, day, day)
	if err != nil {
		t.Fatalf("range after void: %v", err)
	}
	if sum.IssuedCount != 0 || sum.IssuedMinor != 0 {
		t.Fatalf("after void: issued = %d/%d, want 0/0 — a voided sale's voucher issue still reported", sum.IssuedCount, sum.IssuedMinor)
	}
}

// F2 (c): a voucher that has ALREADY been partly redeemed elsewhere blocks
// voiding its issuing sale outright — fail-closed, nothing changed.
func TestUpdateSaleStatus_VoidRefusedWhenVoucherAlreadyRedeemed(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)

	issueSaleID, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []VoucherIssueInput{{VoucherID: "GS-SPENT1", Amount: money.FromMinor(2000)}},
		Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(2000)}},
	})
	if err != nil {
		t.Fatalf("issue sale: %v", err)
	}
	if _, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		Lines:    []SaleLineInput{articleLine()},
		Payments: []PaymentInput{{MethodID: "voucher", VoucherID: "GS-SPENT1", Amount: money.FromMinor(1000)}},
	}); err != nil {
		t.Fatalf("redemption sale: %v", err)
	}

	err = UpdateSaleStatus(ctx, sqlDB, issueSaleID, "voided", "", "test void")
	if !errors.Is(err, data.ErrVoucherRedeemedCannotVoid) {
		t.Fatalf("voiding the issuing sale of a spent voucher: err = %v, want ErrVoucherRedeemedCannotVoid", err)
	}

	// Nothing changed: the sale is still completed, the voucher untouched.
	var saleStatus string
	if err := sqlDB.QueryRow(`SELECT status FROM sales WHERE id = ?`, issueSaleID).Scan(&saleStatus); err != nil {
		t.Fatalf("read sale: %v", err)
	}
	if saleStatus != "completed" {
		t.Fatalf("sale status after refused void = %q, want 'completed' (the refusal must roll everything back)", saleStatus)
	}
	var vStatus string
	var balance int64
	if err := sqlDB.QueryRow(`SELECT status, balance FROM vouchers WHERE id = 'GS-SPENT1'`).Scan(&vStatus, &balance); err != nil {
		t.Fatalf("read voucher: %v", err)
	}
	if vStatus != "active" || balance != 1000 {
		t.Fatalf("voucher after refused void: status=%q balance=%d, want 'active'/1000", vStatus, balance)
	}
}

// F3: a voucher amount above the sanity ceiling is rejected with a clear
// error — including the reviewer's exact overflow probe (two amounts near
// 2^62 that used to wrap `total` negative and sail past payment coverage).
func TestCompleteSale_VoucherIssueRejectsExcessiveAmount(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)

	if _, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []VoucherIssueInput{{Amount: MaxVoucherIssueAmount + 1}},
		Payments:      []PaymentInput{{MethodID: "cash", Amount: MaxVoucherIssueAmount + 1}},
	}); err == nil || !strings.Contains(err.Error(), "exceeds the maximum") {
		t.Fatalf("amount above ceiling: err = %v, want a clear exceeds-the-maximum error", err)
	}

	// The reviewer's probe: two near-2^62 vouchers plus a token cash payment
	// used to overflow total negative, so netPaid < total passed trivially.
	huge := money.FromMinor(int64(1) << 62)
	if _, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []VoucherIssueInput{{Amount: huge}, {Amount: huge}},
		Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1)}},
	}); err == nil {
		t.Fatalf("overflow probe committed a sale, want rejection")
	}
	var count int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&count); err != nil {
		t.Fatalf("count sales: %v", err)
	}
	if count != 0 {
		t.Fatalf("rejected oversized voucher sales still persisted %d sale(s)", count)
	}
}

// F4: tendering a voucher for MORE than the sale needs is refused (change
// and tips are forbidden for voucher redemptions, so the excess would be
// silently confiscated from the voucher's balance) — and the balance stays
// untouched.
func TestCompleteSale_VoucherOvertenderRejected(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)

	if _, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []VoucherIssueInput{{VoucherID: "GS-OT1", Amount: money.FromMinor(5000)}},
		Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(5000)}},
	}); err != nil {
		t.Fatalf("issue sale: %v", err)
	}

	// Small basket (10.00 inclusive), much larger voucher tender (50.00).
	_, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		Lines:    []SaleLineInput{articleLine()},
		Payments: []PaymentInput{{MethodID: "voucher", VoucherID: "GS-OT1", Amount: money.FromMinor(5000)}},
	})
	if !errors.Is(err, ErrVoucherOvertender) {
		t.Fatalf("over-tendered voucher: err = %v, want ErrVoucherOvertender", err)
	}

	var balance int64
	if err := sqlDB.QueryRow(`SELECT balance FROM vouchers WHERE id = 'GS-OT1'`).Scan(&balance); err != nil {
		t.Fatalf("read voucher: %v", err)
	}
	if balance != 5000 {
		t.Fatalf("balance after refused over-tender = %d, want 5000 (nothing may be confiscated)", balance)
	}
	var saleCount int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM sales WHERE tender_type = 'voucher'`).Scan(&saleCount); err != nil {
		t.Fatalf("count sales: %v", err)
	}
	if saleCount != 0 {
		t.Fatalf("refused over-tender still persisted %d sale(s)", saleCount)
	}
}

// F5: a sale cannot redeem a voucher it is issuing itself — the issue rows
// are written before the payment loop, so without the up-front guard this
// fabricated an issue+redemption pair from nothing.
func TestCompleteSale_RejectsRedeemingVoucherIssuedInSameSale(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)

	_, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		Lines:         []SaleLineInput{articleLine()},
		VoucherIssues: []VoucherIssueInput{{VoucherID: "GS-SELF1", Amount: money.FromMinor(1000)}},
		Payments: []PaymentInput{
			{MethodID: "voucher", VoucherID: "GS-SELF1", Amount: money.FromMinor(1000)},
			{MethodID: "cash", Amount: money.FromMinor(1000)},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "same sale") {
		t.Fatalf("same-sale issue+redeem: err = %v, want a clear same-sale rejection", err)
	}
	var count int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM vouchers WHERE id = 'GS-SELF1'`).Scan(&count); err != nil {
		t.Fatalf("count vouchers: %v", err)
	}
	if count != 0 {
		t.Fatalf("rejected same-sale voucher still persisted %d vouchers row(s)", count)
	}
}

// F6: a redemption's voucher id gets the SAME validation the issue path has
// always had — length bound and no control characters.
func TestCompleteSale_RedemptionVoucherIDValidation(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)

	long := strings.Repeat("x", 65)
	if _, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		Lines:    []SaleLineInput{articleLine()},
		Payments: []PaymentInput{{MethodID: "voucher", VoucherID: long, Amount: money.FromMinor(1000)}},
	}); err == nil || !strings.Contains(err.Error(), "64 characters") {
		t.Fatalf("65-char redemption id: err = %v, want the 64-character bound error", err)
	}
	if _, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		Lines:    []SaleLineInput{articleLine()},
		Payments: []PaymentInput{{MethodID: "voucher", VoucherID: "GS\x00BAD", Amount: money.FromMinor(1000)}},
	}); err == nil || !strings.Contains(err.Error(), "control characters") {
		t.Fatalf("control-char redemption id: err = %v, want the control-characters error", err)
	}
}
