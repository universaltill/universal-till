package pos

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// Single-purpose voucher tests (ADR-0105, ut-docs#1037). Same discipline as
// voucher_sale_test.go: the REAL migrated schema (migration 036) and the
// real pos.CompleteSale, never hand-inserted sale rows.

func spSetSetting(t *testing.T, sqlDB *sql.DB, key, value string) {
	t.Helper()
	if err := data.NewSettingsRepo(sqlDB).Set(context.Background(), key, value); err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
}

func spVoucherRow(t *testing.T, sqlDB *sql.DB, id string) (vtype string, rate sql.NullInt64, balance int64, status string) {
	t.Helper()
	if err := sqlDB.QueryRow(`SELECT voucher_type, tax_rate_bp, balance, status FROM vouchers WHERE id = ?`, id).
		Scan(&vtype, &rate, &balance, &status); err != nil {
		t.Fatalf("read voucher %s: %v", id, err)
	}
	return
}

// computeSaleTotals folds a single-purpose voucher's face value into
// subtotal/taxTotal at its stamped rate — a taxable supply at issue — and
// never into the multi-purpose liability accumulator; total is unaffected
// either way (the customer always paid the face value). Inclusive: the
// face is gross with the tax embedded; exclusive: subtotal carries the net
// and taxTotal the tax, so total still lands on the face value. A
// multi-purpose issue in the same call is byte-for-byte unchanged.
func TestComputeSaleTotals_SinglePurposeVoucherTaxedAtIssue(t *testing.T) {
	sp := VoucherIssueInput{VoucherID: "SP", Amount: money.FromMinor(1190), VoucherType: data.VoucherTypeSinglePurpose, TaxRateBP: 1900}
	mp := VoucherIssueInput{VoucherID: "MP", Amount: money.FromMinor(2000), VoucherType: data.VoucherTypeMultiPurpose}

	t.Run("inclusive", func(t *testing.T) {
		subtotal, tax, _, mpTotal, total, err := computeSaleTotals(SaleInput{
			TaxInclusive: true, VoucherIssues: []VoucherIssueInput{sp, mp},
		})
		if err != nil {
			t.Fatal(err)
		}
		if subtotal != 1190 || tax != 190 || mpTotal != 2000 || total != 3190 {
			t.Fatalf("inclusive: subtotal=%d tax=%d voucherIssueTotal=%d total=%d, want 1190/190/2000/3190", subtotal, tax, mpTotal, total)
		}
	})
	t.Run("exclusive", func(t *testing.T) {
		subtotal, tax, _, mpTotal, total, err := computeSaleTotals(SaleInput{
			TaxInclusive: false, VoucherIssues: []VoucherIssueInput{sp, mp},
		})
		if err != nil {
			t.Fatal(err)
		}
		if subtotal != 1000 || tax != 190 || mpTotal != 2000 || total != 3190 {
			t.Fatalf("exclusive: subtotal=%d tax=%d voucherIssueTotal=%d total=%d, want 1000/190/2000/3190 (face value is what the customer pays in both modes)", subtotal, tax, mpTotal, total)
		}
	})
	t.Run("alongside an article line, same band", func(t *testing.T) {
		subtotal, tax, _, _, total, err := computeSaleTotals(SaleInput{
			TaxInclusive: true, Lines: []SaleLineInput{articleLine()}, VoucherIssues: []VoucherIssueInput{sp},
		})
		if err != nil {
			t.Fatal(err)
		}
		// 1000 @19% incl -> tax 160; voucher 1190 @19% incl -> tax 190.
		if subtotal != 2190 || tax != 350 || total != 2190 {
			t.Fatalf("subtotal=%d tax=%d total=%d, want 2190/350/2190", subtotal, tax, total)
		}
	})
	t.Run("unresolved type is refused", func(t *testing.T) {
		if _, _, _, _, _, err := computeSaleTotals(SaleInput{VoucherIssues: []VoucherIssueInput{{VoucherID: "X", Amount: money.FromMinor(100), VoucherType: "three_purpose"}}}); err == nil {
			t.Fatal("an unknown voucher type must be refused, not silently treated as either kind")
		}
	})
}

// CompleteSale resolves each issue's type from the shop's
// vouchers.default_type setting AT ISSUE TIME and stamps it on the row
// (ADR-0105 Decision 1): absent/multi_purpose -> today's behaviour exactly;
// single_purpose -> taxed now at vouchers.single_purpose_tax_rate_bp (or
// the shop's standard store.tax_rate when that key is unset), never
// counted as a liability.
func TestCompleteSale_SinglePurposeIssueResolvedFromSettings(t *testing.T) {
	ctx := context.Background()

	sell := func(t *testing.T, sqlDB *sql.DB, id string) string {
		t.Helper()
		saleID, err := CompleteSale(ctx, sqlDB, SaleInput{
			SaleType: "sale", Currency: "EUR", TaxInclusive: true,
			VoucherIssues: []VoucherIssueInput{{VoucherID: id, Amount: money.FromMinor(1190)}},
			Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1190)}},
		})
		if err != nil {
			t.Fatalf("CompleteSale: %v", err)
		}
		return saleID
	}
	saleHeader := func(t *testing.T, sqlDB *sql.DB, saleID string) (subtotal, tax, total, voucherIssueTotal int64) {
		t.Helper()
		if err := sqlDB.QueryRow(`SELECT subtotal, tax_total, total, voucher_issue_total FROM sales WHERE id = ?`, saleID).Scan(&subtotal, &tax, &total, &voucherIssueTotal); err != nil {
			t.Fatal(err)
		}
		return
	}

	t.Run("default is multi-purpose, unchanged", func(t *testing.T) {
		sqlDB := setupVoucherDB(t)
		saleID := sell(t, sqlDB, "GS-MP")
		vtype, rate, _, _ := spVoucherRow(t, sqlDB, "GS-MP")
		if vtype != data.VoucherTypeMultiPurpose || rate.Valid {
			t.Fatalf("voucher = %s rate=%+v, want multi_purpose with NULL rate", vtype, rate)
		}
		if s, tx, tot, vit := saleHeader(t, sqlDB, saleID); s != 0 || tx != 0 || tot != 1190 || vit != 1190 {
			t.Fatalf("header = subtotal %d tax %d total %d voucher_issue_total %d, want 0/0/1190/1190 (the ut-docs#1008 liability shape)", s, tx, tot, vit)
		}
	})

	t.Run("single-purpose at the configured rate", func(t *testing.T) {
		sqlDB := setupVoucherDB(t)
		spSetSetting(t, sqlDB, SettingKeyVoucherDefaultType, data.VoucherTypeSinglePurpose)
		spSetSetting(t, sqlDB, SettingKeyVoucherSinglePurposeTaxRateBP, "700")
		spSetSetting(t, sqlDB, SettingKeyStoreTaxRate, "19") // must NOT win over the explicit key
		saleID := sell(t, sqlDB, "GS-SP")
		vtype, rate, balance, status := spVoucherRow(t, sqlDB, "GS-SP")
		if vtype != data.VoucherTypeSinglePurpose || !rate.Valid || rate.Int64 != 700 || balance != 1190 || status != "active" {
			t.Fatalf("voucher = %s rate=%+v balance=%d status=%s, want single_purpose/700/1190/active", vtype, rate, balance, status)
		}
		// 1190 @7% inclusive: tax = 1190 - 1190*10000/10700 = 78.
		if s, tx, tot, vit := saleHeader(t, sqlDB, saleID); s != 1190 || tx != 78 || tot != 1190 || vit != 0 {
			t.Fatalf("header = subtotal %d tax %d total %d voucher_issue_total %d, want 1190/78/1190/0 (taxed revenue, not a liability)", s, tx, tot, vit)
		}
		// Still one 'issue' ledger row, exactly as for a multi-purpose voucher.
		var n int
		if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM voucher_transactions WHERE voucher_id = 'GS-SP' AND sale_id = ? AND type = 'issue'`, saleID).Scan(&n); err != nil || n != 1 {
			t.Fatalf("issue rows = %d err=%v, want 1", n, err)
		}
	})

	t.Run("rate falls back to the shop's standard rate", func(t *testing.T) {
		sqlDB := setupVoucherDB(t)
		spSetSetting(t, sqlDB, SettingKeyVoucherDefaultType, data.VoucherTypeSinglePurpose)
		spSetSetting(t, sqlDB, SettingKeyStoreTaxRate, "19")
		saleID := sell(t, sqlDB, "GS-SP19")
		if vtype, rate, _, _ := spVoucherRow(t, sqlDB, "GS-SP19"); vtype != data.VoucherTypeSinglePurpose || !rate.Valid || rate.Int64 != 1900 {
			t.Fatalf("voucher = %s rate=%+v, want single_purpose/1900 (store.tax_rate 19%% * 100)", vtype, rate)
		}
		if s, tx, _, _ := saleHeader(t, sqlDB, saleID); s != 1190 || tx != 190 {
			t.Fatalf("header = subtotal %d tax %d, want 1190/190", s, tx)
		}
	})

	t.Run("single-purpose with no rate anywhere is refused", func(t *testing.T) {
		sqlDB := setupVoucherDB(t)
		spSetSetting(t, sqlDB, SettingKeyVoucherDefaultType, data.VoucherTypeSinglePurpose)
		_, err := CompleteSale(ctx, sqlDB, SaleInput{
			SaleType: "sale", Currency: "EUR", TaxInclusive: true,
			VoucherIssues: []VoucherIssueInput{{VoucherID: "GS-NORATE", Amount: money.FromMinor(1190)}},
			Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1190)}},
		})
		if err == nil {
			t.Fatal("a single-purpose issue with no configured rate must fail closed, never silently tax at 0%")
		}
		var n int
		if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM vouchers WHERE id = 'GS-NORATE'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("refused issue still wrote a voucher row (n=%d err=%v)", n, err)
		}
	})

	t.Run("changing the setting later never reclassifies an issued voucher", func(t *testing.T) {
		sqlDB := setupVoucherDB(t)
		sell(t, sqlDB, "GS-BEFORE")
		spSetSetting(t, sqlDB, SettingKeyVoucherDefaultType, data.VoucherTypeSinglePurpose)
		spSetSetting(t, sqlDB, SettingKeyVoucherSinglePurposeTaxRateBP, "1900")
		sell(t, sqlDB, "GS-AFTER")
		if vtype, _, _, _ := spVoucherRow(t, sqlDB, "GS-BEFORE"); vtype != data.VoucherTypeMultiPurpose {
			t.Fatalf("GS-BEFORE became %s — the type is stamped at issue, immutable from then on", vtype)
		}
		if vtype, _, _, _ := spVoucherRow(t, sqlDB, "GS-AFTER"); vtype != data.VoucherTypeSinglePurpose {
			t.Fatalf("GS-AFTER = %s, want single_purpose", vtype)
		}
	})
}

// An issue that arrives with its type already stamped (a LAN-sync journal
// replay carrying the issuing till's classification) is persisted as given
// — the primary must not re-resolve it from its own settings.
func TestCompleteSale_SinglePurposeIssueExplicitTypeWins(t *testing.T) {
	sqlDB := setupVoucherDB(t)
	// The primary's own setting says multi-purpose…
	spSetSetting(t, sqlDB, SettingKeyVoucherDefaultType, data.VoucherTypeMultiPurpose)
	if _, err := CompleteSale(context.Background(), sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []VoucherIssueInput{{VoucherID: "GS-REPLAY", Amount: money.FromMinor(1190), VoucherType: data.VoucherTypeSinglePurpose, TaxRateBP: 700}},
		Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1190)}},
	}); err != nil {
		t.Fatalf("CompleteSale: %v", err)
	}
	// …but the replayed issue keeps what the replica stamped.
	if vtype, rate, _, _ := spVoucherRow(t, sqlDB, "GS-REPLAY"); vtype != data.VoucherTypeSinglePurpose || !rate.Valid || rate.Int64 != 700 {
		t.Fatalf("voucher = %s rate=%+v, want single_purpose/700 as stamped by the issuing till", vtype, rate)
	}
}

// spIssue sells one single-purpose voucher of `amount` at `rateBP` and
// returns nothing — the redemption tests below then tender it.
func spIssue(t *testing.T, sqlDB *sql.DB, id string, amount int64, rateBP int) {
	t.Helper()
	if _, err := CompleteSale(context.Background(), sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []VoucherIssueInput{{VoucherID: id, Amount: money.FromMinor(amount), VoucherType: data.VoucherTypeSinglePurpose, TaxRateBP: rateBP}},
		Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(amount)}},
	}); err != nil {
		t.Fatalf("issue %s: %v", id, err)
	}
}

// Redeeming a single-purpose voucher as the sole, exact payment for a sale
// whose lines total its face value at its stamped rate adds ZERO VAT
// (ADR-0105 Decision 4): the sale persists with subtotal/tax_total 0 (the
// mirror image of a multi-purpose ISSUE's header — excluded from revenue
// and tax, present in total), its sale_lines rows still land normally for
// stock/receipt purposes, the voucher drains to 'redeemed' and the
// redemption ledger row is written.
func TestCompleteSale_SinglePurposeRedemption_ExactMatchAddsNoTax(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)
	spIssue(t, sqlDB, "SP-1000", 1000, 1900)

	saleID, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		Lines:    []SaleLineInput{articleLine()}, // 1000 @ 19% inclusive
		Payments: []PaymentInput{{MethodID: "voucher", VoucherID: "SP-1000", Amount: money.FromMinor(1000)}},
	})
	if err != nil {
		t.Fatalf("exact-match redemption must succeed: %v", err)
	}
	subtotal, taxTotal, total := saleRow(t, sqlDB, saleID)
	if subtotal != 0 || taxTotal != 0 || total != 1000 {
		t.Fatalf("redeeming sale header = subtotal %d tax %d total %d, want 0/0/1000 (no VAT at redemption — it was collected at issue)", subtotal, taxTotal, total)
	}
	var lines int
	var lineTax int64
	if err := sqlDB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(tax_amount), 0) FROM sale_lines WHERE sale_id = ?`, saleID).Scan(&lines, &lineTax); err != nil {
		t.Fatal(err)
	}
	if lines != 1 || lineTax != 160 {
		t.Fatalf("sale_lines = %d rows / tax %d, want 1 / 160 — lines persist normally (stock, receipt), only the header is excluded", lines, lineTax)
	}
	var stock float64
	if err := sqlDB.QueryRow(`SELECT quantity FROM inventory WHERE id = 'inv1'`).Scan(&stock); err != nil || stock != 49 {
		t.Fatalf("stock after redemption = %v err=%v, want 49 (the goods really left the shop)", stock, err)
	}
	if _, _, balance, status := spVoucherRow(t, sqlDB, "SP-1000"); balance != 0 || status != "redeemed" {
		t.Fatalf("voucher after redemption = balance %d status %s, want 0/redeemed", balance, status)
	}
	var redemptions int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM voucher_transactions WHERE voucher_id = 'SP-1000' AND sale_id = ? AND type = 'redemption' AND amount = 1000`, saleID).Scan(&redemptions); err != nil || redemptions != 1 {
		t.Fatalf("redemption rows = %d err=%v, want 1", redemptions, err)
	}
	// A whole-sale discount that brings the lines to exactly the face value
	// still matches: the customer's gross for these goods IS the face.
	spIssue(t, sqlDB, "SP-900", 900, 1900)
	if _, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true, SaleDiscount: money.FromMinor(100),
		Lines:    []SaleLineInput{articleLine()},
		Payments: []PaymentInput{{MethodID: "voucher", VoucherID: "SP-900", Amount: money.FromMinor(900)}},
	}); err != nil {
		t.Fatalf("discounted exact match must succeed: %v", err)
	}
}

// Every shape v1 cannot prove adds zero VAT is refused with
// data.ErrVoucherSinglePurposeMismatch, atomically: no sale row, no
// payment, no stock movement, voucher balance untouched.
func TestCompleteSale_SinglePurposeRedemption_MismatchRejected(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)
	spIssue(t, sqlDB, "SP-1000", 1000, 1900)
	var salesBefore int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&salesBefore); err != nil {
		t.Fatal(err)
	}

	cheap := articleLine()
	cheap.UnitPrice = money.FromMinor(500)
	reduced := articleLine()
	reduced.TaxRateBasisPoints = 700

	cases := []struct {
		name string
		in   SaleInput
	}{
		{"mixed tender: voucher plus cash", SaleInput{
			Lines:    []SaleLineInput{articleLine(), articleLine()},
			Payments: []PaymentInput{{MethodID: "voucher", VoucherID: "SP-1000", Amount: money.FromMinor(1000)}, {MethodID: "cash", Amount: money.FromMinor(1000)}},
		}},
		{"partial redemption: sale smaller than the face value", SaleInput{
			Lines:    []SaleLineInput{cheap},
			Payments: []PaymentInput{{MethodID: "voucher", VoucherID: "SP-1000", Amount: money.FromMinor(500)}},
		}},
		{"line at a different rate than the voucher's", SaleInput{
			Lines:    []SaleLineInput{reduced},
			Payments: []PaymentInput{{MethodID: "voucher", VoucherID: "SP-1000", Amount: money.FromMinor(1000)}},
		}},
		{"a second line at another rate alongside a matching one", SaleInput{
			Lines:    []SaleLineInput{cheap, func() SaleLineInput { l := reduced; l.UnitPrice = money.FromMinor(500); return l }()},
			Payments: []PaymentInput{{MethodID: "voucher", VoucherID: "SP-1000", Amount: money.FromMinor(1000)}},
		}},
		{"service charge in the sale", SaleInput{
			ServiceCharge: money.FromMinor(100),
			Lines:         []SaleLineInput{func() SaleLineInput { l := articleLine(); l.UnitPrice = money.FromMinor(900); return l }()},
			Payments:      []PaymentInput{{MethodID: "voucher", VoucherID: "SP-1000", Amount: money.FromMinor(1000)}},
		}},
		{"issuing another voucher in the same sale", SaleInput{
			Lines:         []SaleLineInput{cheap},
			VoucherIssues: []VoucherIssueInput{{VoucherID: "GS-NEW", Amount: money.FromMinor(500)}},
			Payments:      []PaymentInput{{MethodID: "voucher", VoucherID: "SP-1000", Amount: money.FromMinor(1000)}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			in.SaleType, in.Currency, in.TaxInclusive = "sale", "EUR", true
			_, err := CompleteSale(ctx, sqlDB, in)
			if !errors.Is(err, data.ErrVoucherSinglePurposeMismatch) {
				t.Fatalf("err = %v, want ErrVoucherSinglePurposeMismatch", err)
			}
			var salesNow int
			if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&salesNow); err != nil {
				t.Fatal(err)
			}
			if salesNow != salesBefore {
				t.Fatalf("a refused redemption persisted a sale (%d -> %d)", salesBefore, salesNow)
			}
			if _, _, balance, status := spVoucherRow(t, sqlDB, "SP-1000"); balance != 1000 || status != "active" {
				t.Fatalf("voucher touched by a refused redemption: balance %d status %s", balance, status)
			}
			var stock float64
			if err := sqlDB.QueryRow(`SELECT quantity FROM inventory WHERE id = 'inv1'`).Scan(&stock); err != nil || stock != 50 {
				t.Fatalf("stock moved on a refused redemption: %v (err %v)", stock, err)
			}
		})
	}

	// And a multi-purpose voucher in the same partial/mixed shapes keeps
	// working exactly as before — the restriction is single-purpose only.
	if _, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []VoucherIssueInput{{VoucherID: "MP-1000", Amount: money.FromMinor(1000)}},
		Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1000)}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		Lines:    []SaleLineInput{articleLine(), reduced},
		Payments: []PaymentInput{{MethodID: "voucher", VoucherID: "MP-1000", Amount: money.FromMinor(600)}, {MethodID: "cash", Amount: money.FromMinor(1400)}},
	}); err != nil {
		t.Fatalf("multi-purpose mixed tender must be unchanged: %v", err)
	}
}
