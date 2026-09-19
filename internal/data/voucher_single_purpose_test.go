package data

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Single-purpose voucher repo tests (ut-docs#1037) — real migrated schema
// via b8OpenDB (migration 036 adds vouchers.purpose / issue_vat_rate_bp).

func spNow() string { return time.Now().UTC().Format(time.RFC3339) }

func spSeedVoucher(t *testing.T, ctx context.Context, repo *POSRepo, id string, amount int64, rateBP int) {
	t.Helper()
	if err := repo.CreateVoucher(ctx, nil, Voucher{
		ID: id, HolderLabel: "Sample Holder", OriginalAmountMinor: amount,
		BalanceMinor: amount, Currency: "EUR", IssuedSaleID: "sale-" + id,
		Purpose: VoucherPurposeSingle, IssueVATRateBP: &rateBP,
		CreatedAt: spNow(),
	}); err != nil {
		t.Fatalf("CreateVoucher %s: %v", id, err)
	}
}

func TestVoucherRepo_SinglePurpose_CreatePersistsPurposeAndRate(t *testing.T) {
	d := b8OpenDB(t, "voucher-sp-create.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	spSeedVoucher(t, ctx, repo, "GS-SP-A", 2500, 1900)
	v, err := repo.GetVoucherBalance(ctx, nil, "GS-SP-A")
	if err != nil {
		t.Fatalf("GetVoucherBalance: %v", err)
	}
	if v.Purpose != VoucherPurposeSingle || v.IssueVATRateBP == nil || *v.IssueVATRateBP != 1900 || v.VoucherType != "multi_purpose" {
		t.Fatalf("voucher = purpose %q rate %v voucher_type %q (voucher_type is vestigial, always 'multi_purpose')", v.Purpose, v.IssueVATRateBP, v.VoucherType)
	}

	// The pre-#1037 shape is the default: no purpose -> multi_purpose, no rate.
	vSeedVoucher(t, ctx, repo, "GS-MP-A", 1500)
	m, err := repo.GetVoucherBalance(ctx, nil, "GS-MP-A")
	if err != nil {
		t.Fatalf("GetVoucherBalance: %v", err)
	}
	if m.Purpose != VoucherPurposeMulti || m.IssueVATRateBP != nil {
		t.Fatalf("default voucher = purpose %q rate %v, want multi_purpose/nil", m.Purpose, m.IssueVATRateBP)
	}

	// A 0% rate is a real rate, distinguishable from "no rate".
	zero := 0
	if err := repo.CreateVoucher(ctx, nil, Voucher{
		ID: "GS-SP-0", OriginalAmountMinor: 100, BalanceMinor: 100, Currency: "EUR",
		Purpose: VoucherPurposeSingle, IssueVATRateBP: &zero, CreatedAt: spNow(),
	}); err != nil {
		t.Fatalf("CreateVoucher 0%%: %v", err)
	}
	z, err := repo.GetVoucherBalance(ctx, nil, "GS-SP-0")
	if err != nil || z.IssueVATRateBP == nil || *z.IssueVATRateBP != 0 {
		t.Fatalf("0%% voucher rate = %v (err %v), want a non-nil 0", z.IssueVATRateBP, err)
	}

	// The schema refuses anything outside the two purposes.
	if err := repo.CreateVoucher(ctx, nil, Voucher{
		ID: "GS-BAD", OriginalAmountMinor: 100, BalanceMinor: 100, Currency: "EUR",
		Purpose: "gift", CreatedAt: spNow(),
	}); err == nil {
		t.Fatalf("CreateVoucher with purpose 'gift' succeeded, want a CHECK failure")
	}
}

// Redemption drains the voucher in one step (balance 0, status
// 'redeemed'), records one 'redemption' transaction for the FULL original
// amount, and — being a voucher already taxed at issue — writes nothing
// that carries tax: the sale_id is optional and soft, no sales row is
// involved at all.
func TestVoucherRepo_SinglePurpose_RedeemDrainsAndRecords(t *testing.T) {
	d := b8OpenDB(t, "voucher-sp-redeem.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	spSeedVoucher(t, ctx, repo, "GS-SP-B", 2500, 1900)

	var salesBefore int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&salesBefore); err != nil {
		t.Fatalf("count sales: %v", err)
	}
	v, err := repo.RedeemSinglePurposeVoucher(ctx, nil, "GS-SP-B", "", spNow())
	if err != nil {
		t.Fatalf("RedeemSinglePurposeVoucher: %v", err)
	}
	if v.ID != "GS-SP-B" || v.BalanceMinor != 0 || v.Status != "redeemed" || v.OriginalAmountMinor != 2500 || v.Purpose != VoucherPurposeSingle {
		t.Fatalf("returned voucher = %+v, want balance 0 / redeemed", v)
	}
	again, err := repo.GetVoucherBalance(ctx, nil, "GS-SP-B")
	if err != nil || again.BalanceMinor != 0 || again.Status != "redeemed" {
		t.Fatalf("persisted voucher = %+v (err %v), want balance 0 / redeemed", again, err)
	}

	var count int
	var txType string
	var amount int64
	var saleID *string
	if err := d.DB.QueryRow(`SELECT COUNT(*), MAX(type), MAX(amount), MAX(sale_id) FROM voucher_transactions WHERE voucher_id = 'GS-SP-B' AND type = 'redemption'`).
		Scan(&count, &txType, &amount, &saleID); err != nil {
		t.Fatalf("read redemption tx: %v", err)
	}
	if count != 1 || txType != "redemption" || amount != 2500 || saleID != nil {
		t.Fatalf("redemption tx = count %d type %q amount %d sale_id %v, want 1/'redemption'/2500/NULL", count, txType, amount, saleID)
	}
	var salesAfter int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&salesAfter); err != nil {
		t.Fatalf("count sales: %v", err)
	}
	if salesAfter != salesBefore {
		t.Fatalf("redemption wrote a sales row (%d -> %d): a single-purpose redemption carries no tax event", salesBefore, salesAfter)
	}

	// Optional soft sale id is stored verbatim when given.
	spSeedVoucher(t, ctx, repo, "GS-SP-C", 500, 700)
	if _, err := repo.RedeemSinglePurposeVoucher(ctx, nil, "GS-SP-C", "sale-xyz", spNow()); err != nil {
		t.Fatalf("redeem with sale id: %v", err)
	}
	var storedSale string
	if err := d.DB.QueryRow(`SELECT sale_id FROM voucher_transactions WHERE voucher_id = 'GS-SP-C' AND type = 'redemption'`).Scan(&storedSale); err != nil || storedSale != "sale-xyz" {
		t.Fatalf("soft sale_id = %q (err %v), want sale-xyz", storedSale, err)
	}
}

func TestVoucherRepo_SinglePurpose_RedeemRejectsWrongPurposeAndState(t *testing.T) {
	d := b8OpenDB(t, "voucher-sp-reject.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	// A multi-purpose voucher never goes through this path.
	vSeedVoucher(t, ctx, repo, "GS-MP-R", 1500)
	if _, err := repo.RedeemSinglePurposeVoucher(ctx, nil, "GS-MP-R", "", spNow()); !errors.Is(err, ErrVoucherNotSinglePurpose) {
		t.Fatalf("multi-purpose through single-purpose redeem: err = %v, want ErrVoucherNotSinglePurpose", err)
	}
	m, err := repo.GetVoucherBalance(ctx, nil, "GS-MP-R")
	if err != nil || m.BalanceMinor != 1500 || m.Status != "active" {
		t.Fatalf("multi-purpose voucher touched by refused redeem: %+v (err %v)", m, err)
	}

	// Unknown id.
	if _, err := repo.RedeemSinglePurposeVoucher(ctx, nil, "GS-NOPE", "", spNow()); !errors.Is(err, ErrVoucherNotFound) {
		t.Fatalf("unknown: err = %v, want ErrVoucherNotFound", err)
	}

	// Second redemption of the same voucher.
	spSeedVoucher(t, ctx, repo, "GS-SP-T", 2500, 1900)
	if _, err := repo.RedeemSinglePurposeVoucher(ctx, nil, "GS-SP-T", "", spNow()); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if _, err := repo.RedeemSinglePurposeVoucher(ctx, nil, "GS-SP-T", "", spNow()); !errors.Is(err, ErrVoucherNotActive) {
		t.Fatalf("second redeem: err = %v, want ErrVoucherNotActive", err)
	}
	var txCount int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM voucher_transactions WHERE voucher_id = 'GS-SP-T' AND type = 'redemption'`).Scan(&txCount); err != nil || txCount != 1 {
		t.Fatalf("redemption rows after refused second redeem = %d (err %v), want 1", txCount, err)
	}
}

// The double-tax guard at the repo level: the tender path's debit (every
// caller — local tender, cross-till reservation, journal replay) refuses a
// single-purpose voucher with its own sentinel, force or not.
func TestVoucherRepo_TenderDebitRejectsSinglePurpose(t *testing.T) {
	d := b8OpenDB(t, "voucher-sp-debit.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	spSeedVoucher(t, ctx, repo, "GS-SP-D", 2500, 1900)

	for _, force := range []bool{false, true} {
		if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-SP-D", 1000, force); !errors.Is(err, ErrVoucherNotMultiPurpose) {
			t.Fatalf("debit force=%v: err = %v, want ErrVoucherNotMultiPurpose", force, err)
		}
	}
	v, err := repo.GetVoucherBalance(ctx, nil, "GS-SP-D")
	if err != nil || v.BalanceMinor != 2500 || v.Status != "active" {
		t.Fatalf("voucher after refused debit = %+v (err %v)", v, err)
	}

	// ReserveVoucherRedemption (the primary-side cross-till reservation)
	// rides on the same debit and refuses identically.
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	if _, err := repo.ReserveVoucherRedemption(ctx, tx, "GS-SP-D", "sale-r", 1000, spNow()); !errors.Is(err, ErrVoucherNotMultiPurpose) {
		t.Fatalf("reserve: err = %v, want ErrVoucherNotMultiPurpose", err)
	}
}

// The three tax-band sale readers (calendar-day, instant window, rolling
// window) attach a sale's single-purpose voucher issues — rate and face
// value — so the pages layer can band them; a multi-purpose issue in the
// same sale is NOT attached (it lives in VoucherIssueTotal, in no band).
func TestVoucherRepo_TaxBandReadersCarrySinglePurposeIssues(t *testing.T) {
	d := b8OpenDB(t, "voucher-sp-bands.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	now := time.Now().UTC()
	createdAt := now.Format("2006-01-02T15:04:05Z")
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, voucher_issue_total, created_at, local_date)
VALUES ('sale-sp', 'R-SP', 'completed', 'sale', 'EUR', 2500, 0, 399, 4000, 1500, ?, date(?, 'localtime'))`, createdAt, createdAt)
	spSeedVoucher(t, ctx, repo, "GS-SP-E", 2500, 1900)
	vSeedVoucher(t, ctx, repo, "GS-MP-E", 1500)
	for _, id := range []string{"GS-SP-E", "GS-MP-E"} {
		amount := int64(2500)
		if id == "GS-MP-E" {
			amount = 1500
		}
		if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{
			ID: "tx-" + id, VoucherID: id, SaleID: "sale-sp", Type: "issue", AmountMinor: amount, CreatedAt: createdAt,
		}); err != nil {
			t.Fatalf("issue tx %s: %v", id, err)
		}
	}
	var day string
	if err := d.DB.QueryRow(`SELECT date(?, 'localtime')`, createdAt).Scan(&day); err != nil {
		t.Fatalf("day: %v", err)
	}

	check := func(name string, sales []EODTaxBandSale, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(sales) != 1 || sales[0].ID != "sale-sp" {
			t.Fatalf("%s: sales = %+v, want just sale-sp", name, sales)
		}
		got := sales[0].SinglePurposeVoucherIssues
		if len(got) != 1 || got[0].RateBP != 1900 || got[0].Amount != 2500 {
			t.Fatalf("%s: single-purpose issues = %+v, want one {1900, 2500}", name, got)
		}
		if sales[0].VoucherIssueTotal != 1500 {
			t.Fatalf("%s: VoucherIssueTotal = %d, want the multi-purpose 1500", name, sales[0].VoucherIssueTotal)
		}
	}
	s, err := repo.SalesForTaxBands(ctx, day, day)
	check("SalesForTaxBands", s, err)
	s, err = repo.SalesForTaxBandsInstant(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	check("SalesForTaxBandsInstant", s, err)
	s, err = repo.SalesForTaxWindow(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	check("SalesForTaxWindow", s, err)
}

// The Z-report's GUTSCHEINE section is the LIABILITY reconciliation
// (eod_tax_bands.go: sum(band.Gross) == Net − vouchers issued). A
// single-purpose issue is revenue already inside a VAT band, and its
// hand-over moves no money — so neither its 'issue' nor its 'redemption'
// row counts in Issued/Redeemed, on either the calendar-day or the
// instant-window aggregation. The multi-purpose voucher in the same sale
// still counts exactly as before.
func TestVoucherRepo_RangeSummariesExcludeSinglePurpose(t *testing.T) {
	d := b8OpenDB(t, "voucher-sp-range.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	now := time.Now().UTC()
	createdAt := now.Format("2006-01-02T15:04:05Z")
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, voucher_issue_total, created_at, local_date)
VALUES ('sale-rg', 'R-RG', 'completed', 'sale', 'EUR', 2500, 0, 399, 4000, 1500, ?, date(?, 'localtime'))`, createdAt, createdAt)
	spSeedVoucher(t, ctx, repo, "GS-SP-RG", 2500, 1900)
	vSeedVoucher(t, ctx, repo, "GS-MP-RG", 1500)
	for id, amount := range map[string]int64{"GS-SP-RG": 2500, "GS-MP-RG": 1500} {
		if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{
			ID: "tx-" + id, VoucherID: id, SaleID: "sale-rg", Type: "issue", AmountMinor: amount, CreatedAt: createdAt,
		}); err != nil {
			t.Fatalf("issue tx %s: %v", id, err)
		}
	}
	if _, err := repo.RedeemSinglePurposeVoucher(ctx, nil, "GS-SP-RG", "", createdAt); err != nil {
		t.Fatalf("redeem single-purpose: %v", err)
	}
	var day string
	if err := d.DB.QueryRow(`SELECT date(?, 'localtime')`, createdAt).Scan(&day); err != nil {
		t.Fatalf("day: %v", err)
	}
	check := func(name string, s VoucherRangeSummary, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if s.IssuedCount != 1 || s.IssuedMinor != 1500 || s.RedeemedCount != 0 || s.RedeemedMinor != 0 {
			t.Fatalf("%s = %+v, want issued 1/1500 (multi-purpose only) and redeemed 0/0", name, s)
		}
	}
	s, err := repo.VouchersIssuedRedeemedForRange(ctx, day, day)
	check("VouchersIssuedRedeemedForRange", s, err)
	s, err = repo.VouchersIssuedRedeemedForInstantWindow(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	check("VouchersIssuedRedeemedForInstantWindow", s, err)
}

// The LAN-sync journal's sale detail carries purpose + rate for a
// single-purpose issue (additive, omitted for multi-purpose), so a replica-
// issued single-purpose voucher replays on the primary as the same kind.
func TestVoucherRepo_SaleDetailCarriesVoucherPurpose(t *testing.T) {
	d := b8OpenDB(t, "voucher-sp-detail.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	createdAt := spNow()
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at)
VALUES ('sale-det', 'R-DET', 'completed', 'sale', 'EUR', 2500, 0, 399, 4000, ?)`, createdAt)
	spSeedVoucher(t, ctx, repo, "GS-SP-F", 2500, 1900)
	vSeedVoucher(t, ctx, repo, "GS-MP-F", 1500)
	for id, amount := range map[string]int64{"GS-SP-F": 2500, "GS-MP-F": 1500} {
		if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{
			ID: "tx-" + id, VoucherID: id, SaleID: "sale-det", Type: "issue", AmountMinor: amount, CreatedAt: createdAt,
		}); err != nil {
			t.Fatalf("issue tx %s: %v", id, err)
		}
	}
	det, found, err := repo.GetSaleDetail(ctx, "R-DET")
	if err != nil || !found {
		t.Fatalf("GetSaleDetail: found=%v err=%v", found, err)
	}
	byID := map[string]SaleDetailVoucherIssue{}
	for _, vi := range det.VoucherIssues {
		byID[vi.VoucherID] = vi
	}
	sp, ok := byID["GS-SP-F"]
	if !ok || sp.Purpose != VoucherPurposeSingle || sp.VATRateBP == nil || *sp.VATRateBP != 1900 {
		t.Fatalf("single-purpose detail = %+v (found %v), want purpose single_purpose / rate 1900", sp, ok)
	}
	mp, ok := byID["GS-MP-F"]
	if !ok || mp.Purpose != "" || mp.VATRateBP != nil {
		t.Fatalf("multi-purpose detail = %+v (found %v), want the pre-#1037 shape (no purpose, no rate)", mp, ok)
	}
}
