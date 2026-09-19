package data

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// Single-purpose voucher repo tests (ADR-0105, ut-docs#1037) — real
// migrated schema via b8OpenDB (internal/db.Open runs migration 036).

// CreateVoucher persists the resolved type and basis-point rate stamped at
// issue; a multi-purpose voucher stores NULL (no rate is fixed until
// redemption). GetVoucherBalance reads both back, and EnsureVoucherLocalRow
// (a replica's local mirror of a primary-issued voucher) carries them too —
// the replica's own redemption validation needs the rate.
func TestVoucherRepo_SinglePurposePersistsTypeAndRate(t *testing.T) {
	d := b8OpenDB(t, "voucher-sp-create.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	now := time.Now().UTC().Format(time.RFC3339)
	if err := repo.CreateVoucher(ctx, nil, Voucher{
		ID: "SP-1", OriginalAmountMinor: 1900, BalanceMinor: 1900, Currency: "EUR",
		VoucherType: VoucherTypeSinglePurpose, TaxRateBP: 700, IssuedSaleID: "sale-sp", CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateVoucher single-purpose: %v", err)
	}
	vSeedVoucher(t, ctx, repo, "MP-1", 1500)

	sp, err := repo.GetVoucherBalance(ctx, nil, "SP-1")
	if err != nil {
		t.Fatalf("GetVoucherBalance SP-1: %v", err)
	}
	if sp.VoucherType != VoucherTypeSinglePurpose || sp.TaxRateBP != 700 {
		t.Fatalf("SP-1 = type %q rate %d, want single_purpose/700", sp.VoucherType, sp.TaxRateBP)
	}
	mp, err := repo.GetVoucherBalance(ctx, nil, "MP-1")
	if err != nil {
		t.Fatalf("GetVoucherBalance MP-1: %v", err)
	}
	if mp.VoucherType != VoucherTypeMultiPurpose || mp.TaxRateBP != 0 {
		t.Fatalf("MP-1 = type %q rate %d, want multi_purpose/0", mp.VoucherType, mp.TaxRateBP)
	}
	var mpRate sql.NullInt64
	if err := d.DB.QueryRow(`SELECT tax_rate_bp FROM vouchers WHERE id = 'MP-1'`).Scan(&mpRate); err != nil {
		t.Fatal(err)
	}
	if mpRate.Valid {
		t.Fatalf("multi-purpose voucher stored tax_rate_bp=%d, want NULL (no rate fixed until redemption)", mpRate.Int64)
	}

	// A single-purpose voucher must carry its rate: refusing to persist one
	// without it is the fail-closed choice (the stamped rate is what
	// redemption validation compares against).
	if err := repo.CreateVoucher(ctx, nil, Voucher{
		ID: "SP-norate", OriginalAmountMinor: 100, BalanceMinor: 100, Currency: "EUR",
		VoucherType: VoucherTypeSinglePurpose, TaxRateBP: -1, CreatedAt: now,
	}); err == nil {
		t.Fatalf("a single-purpose voucher with a negative rate must be refused")
	}
	if err := repo.CreateVoucher(ctx, nil, Voucher{
		ID: "SP-badtype", OriginalAmountMinor: 100, BalanceMinor: 100, Currency: "EUR",
		VoucherType: "three_purpose", CreatedAt: now,
	}); err == nil {
		t.Fatalf("an unknown voucher type must be refused")
	}

	// The replica-side mirror carries the same two fields.
	if err := repo.EnsureVoucherLocalRow(ctx, nil, Voucher{
		ID: "SP-mirror", OriginalAmountMinor: 500, BalanceMinor: 500, Currency: "EUR",
		VoucherType: VoucherTypeSinglePurpose, TaxRateBP: 1900, CreatedAt: now,
	}); err != nil {
		t.Fatalf("EnsureVoucherLocalRow: %v", err)
	}
	mirror, err := repo.GetVoucherBalance(ctx, nil, "SP-mirror")
	if err != nil {
		t.Fatalf("GetVoucherBalance SP-mirror: %v", err)
	}
	if mirror.VoucherType != VoucherTypeSinglePurpose || mirror.TaxRateBP != 1900 {
		t.Fatalf("mirror = type %q rate %d, want single_purpose/1900", mirror.VoucherType, mirror.TaxRateBP)
	}
}

// The primary-side reservation (POST /api/sync/vouchers/{id}/redeem,
// ADR-0084) is the one redemption path that runs BEFORE any sale shape
// exists to validate against, so it enforces the part of ADR-0105 Decision
// 4 it can see: a single-purpose voucher is redeemed for its full face
// value or not at all. Without this an older replica could reserve a
// partial single-purpose redemption on the primary, debit it, and only
// have the primary's journal replay refuse the sale afterwards.
func TestVoucherRepo_ReserveSinglePurposeRequiresFullFaceValue(t *testing.T) {
	d := b8OpenDB(t, "voucher-sp-reserve.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	now := time.Now().UTC().Format(time.RFC3339)
	if err := repo.CreateVoucher(ctx, nil, Voucher{
		ID: "SP-R", OriginalAmountMinor: 1000, BalanceMinor: 1000, Currency: "EUR",
		VoucherType: VoucherTypeSinglePurpose, TaxRateBP: 1900, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	reserve := func(saleID string, amount int64) error {
		t.Helper()
		tx, err := d.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := repo.ReserveVoucherRedemption(ctx, tx, "SP-R", saleID, amount, now); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err := reserve("sale-part", 400); !errors.Is(err, ErrVoucherSinglePurposeMismatch) {
		t.Fatalf("partial reservation err = %v, want ErrVoucherSinglePurposeMismatch", err)
	}
	if v, _ := repo.GetVoucherBalance(ctx, nil, "SP-R"); v.BalanceMinor != 1000 || v.Status != "active" {
		t.Fatalf("a refused reservation touched the voucher: balance %d status %s", v.BalanceMinor, v.Status)
	}
	if err := reserve("sale-full", 1000); err != nil {
		t.Fatalf("full-face reservation must succeed: %v", err)
	}
	if v, _ := repo.GetVoucherBalance(ctx, nil, "SP-R"); v.BalanceMinor != 0 || v.Status != "redeemed" {
		t.Fatalf("after full reservation: balance %d status %s, want 0/redeemed", v.BalanceMinor, v.Status)
	}
	// A multi-purpose voucher keeps partial reservations exactly as before.
	vSeedVoucher(t, ctx, repo, "MP-R", 1000)
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := repo.ReserveVoucherRedemption(ctx, tx, "MP-R", "sale-mp", 400, now); err != nil {
		t.Fatalf("multi-purpose partial reservation must be unchanged: %v", err)
	}
}

// The sentinel every caller classifies a refused single-purpose redemption
// by (ADR-0105 Decision 4) — same fail-closed family as ErrVoucherNotFound.
func TestVoucherRepo_SinglePurposeMismatchSentinelIsDistinct(t *testing.T) {
	for _, other := range []error{ErrVoucherNotFound, ErrVoucherNotActive, ErrVoucherInsufficientBalance, ErrVoucherRedeemedCannotVoid} {
		if errors.Is(ErrVoucherSinglePurposeMismatch, other) || errors.Is(other, ErrVoucherSinglePurposeMismatch) {
			t.Fatalf("ErrVoucherSinglePurposeMismatch must be distinct from %v", other)
		}
	}
}

// VouchersIssuedRedeemedForRange (and its instant-window sibling) must keep
// a single-purpose issue OUT of the Issued liability bucket — it is ordinary
// taxed revenue already sitting in a VAT band, and counting it under
// GUTSCHEINE too would overstate the day — and report single-purpose flows
// in their own informational buckets. Multi-purpose flows are unchanged.
func TestVoucherRepo_IssuedRedeemedForRange_SinglePurposeBuckets(t *testing.T) {
	d := b8OpenDB(t, "voucher-sp-range.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	at := b8At(today)

	vSeedVoucher(t, ctx, repo, "MP-A", 3000)
	if err := repo.CreateVoucher(ctx, nil, Voucher{
		ID: "SP-A", OriginalAmountMinor: 1900, BalanceMinor: 1900, Currency: "EUR",
		VoucherType: VoucherTypeSinglePurpose, TaxRateBP: 1900, IssuedSaleID: "sale-sp-issue", CreatedAt: at,
	}); err != nil {
		t.Fatalf("CreateVoucher SP-A: %v", err)
	}
	rec := func(id, voucherID, saleID, txType string, amount int64) {
		t.Helper()
		if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{
			ID: id, VoucherID: voucherID, SaleID: saleID, Type: txType, AmountMinor: amount, CreatedAt: at,
		}); err != nil {
			t.Fatalf("RecordVoucherTransaction %s: %v", id, err)
		}
	}
	rec("tx-mp-i", "MP-A", "sale-mp-issue", "issue", 3000)
	rec("tx-mp-r", "MP-A", "sale-mp-redeem", "redemption", 500)
	rec("tx-sp-i", "SP-A", "sale-sp-issue", "issue", 1900)
	rec("tx-sp-r", "SP-A", "sale-sp-redeem", "redemption", 1900)

	check := func(label string, sum VoucherRangeSummary) {
		t.Helper()
		if sum.IssuedCount != 1 || sum.IssuedMinor != 3000 {
			t.Fatalf("%s: issued = %d/%d, want 1/3000 (multi-purpose only — the single-purpose issue is taxed revenue, not a liability)", label, sum.IssuedCount, sum.IssuedMinor)
		}
		if sum.RedeemedCount != 1 || sum.RedeemedMinor != 500 {
			t.Fatalf("%s: redeemed = %d/%d, want 1/500 (multi-purpose only)", label, sum.RedeemedCount, sum.RedeemedMinor)
		}
		if sum.SinglePurposeIssuedCount != 1 || sum.SinglePurposeIssuedMinor != 1900 {
			t.Fatalf("%s: single-purpose issued = %d/%d, want 1/1900", label, sum.SinglePurposeIssuedCount, sum.SinglePurposeIssuedMinor)
		}
		if sum.SinglePurposeRedeemedCount != 1 || sum.SinglePurposeRedeemedMinor != 1900 {
			t.Fatalf("%s: single-purpose redeemed = %d/%d, want 1/1900", label, sum.SinglePurposeRedeemedCount, sum.SinglePurposeRedeemedMinor)
		}
		if sum.ImportedCount != 0 {
			t.Fatalf("%s: imported = %d, want 0", label, sum.ImportedCount)
		}
	}
	day := b8ExpectedDay(t, d, today, 0, 0)
	sum, err := repo.VouchersIssuedRedeemedForRange(ctx, day, day)
	if err != nil {
		t.Fatalf("VouchersIssuedRedeemedForRange: %v", err)
	}
	check("range", sum)
	sumInstant, err := repo.VouchersIssuedRedeemedForInstantWindow(ctx, today.Add(-time.Hour), today.Add(time.Hour))
	if err != nil {
		t.Fatalf("VouchersIssuedRedeemedForInstantWindow: %v", err)
	}
	check("instant", sumInstant)

	// And the day-close report carries them through (informational, never
	// folded into the GUTSCHEINE liability figures).
	rep, err := repo.EndOfDay(ctx, day)
	if err != nil {
		t.Fatalf("EndOfDay: %v", err)
	}
	if rep.VouchersIssued != 3000 || rep.VouchersRedeemed != 500 {
		t.Fatalf("EOD liability buckets = issued %d redeemed %d, want 3000/500", rep.VouchersIssued, rep.VouchersRedeemed)
	}
	if rep.VouchersSinglePurposeIssuedCount != 1 || rep.VouchersSinglePurposeIssued != 1900 ||
		rep.VouchersSinglePurposeRedeemedCount != 1 || rep.VouchersSinglePurposeRedeemed != 1900 {
		t.Fatalf("EOD single-purpose buckets = %d/%d issued, %d/%d redeemed, want 1/1900 each",
			rep.VouchersSinglePurposeIssuedCount, rep.VouchersSinglePurposeIssued, rep.VouchersSinglePurposeRedeemedCount, rep.VouchersSinglePurposeRedeemed)
	}
}

// SalesForTaxBands (and its instant/window siblings) must hand the banding
// layer each sale's single-purpose voucher flows: a single-purpose issue is
// never a sale_lines row, so without this the day-close would re-derive the
// bands from lines alone and under-declare VAT for exactly the sales that
// issued one (ADR-0105 Decision 3); a single-purpose REDEMPTION marks the
// sale whose lines must be left OUT of the bands (Decision 4). Multi-purpose
// flows must not appear in either field.
func TestPOSRepo_SalesForTaxBands_SinglePurposeVoucherFlows(t *testing.T) {
	d := b8OpenDB(t, "sp-tax-band-sales.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	at := b8At(today)

	b8Item(t, d, "itm", 1000, nil, 1)
	// s-issue: an ordinary 19% line plus a single-purpose voucher issue at
	// 7% and a multi-purpose issue (which must NOT surface).
	b8Sale(t, d, "s-issue", at, "completed", "sale", 190, 1190+700+1500)
	b8Line(t, d, "s-issue", 1, "itm", "", "Widget", 1, 1900, 190, 1000, 1190)
	// s-redeem: the sale a single-purpose voucher paid for in full.
	b8Sale(t, d, "s-redeem", at, "completed", "sale", 0, 700)
	b8Line(t, d, "s-redeem", 1, "itm", "", "Cake", 1, 700, 46, 654, 700)
	// s-plain: no voucher activity at all.
	b8Sale(t, d, "s-plain", at, "completed", "sale", 190, 1190)
	b8Line(t, d, "s-plain", 1, "itm", "", "Widget", 1, 1900, 190, 1000, 1190)
	// s-void: voided — nothing about it may surface.
	b8Sale(t, d, "s-void", at, "voided", "sale", 0, 700)

	mk := func(id, vtype string, rate any, amount int64) {
		t.Helper()
		mustExec(t, d, `INSERT INTO vouchers (id, original_amount, balance, currency, voucher_type, tax_rate_bp, created_at) VALUES (?, ?, ?, 'EUR', ?, ?, ?)`,
			id, amount, amount, vtype, rate, at)
	}
	mk("SP-7", VoucherTypeSinglePurpose, 700, 700)
	mk("MP-15", VoucherTypeMultiPurpose, nil, 1500)
	mk("SP-old", VoucherTypeSinglePurpose, 700, 700)
	tx := func(id, voucherID, saleID, txType string, amount int64) {
		t.Helper()
		mustExec(t, d, `INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, voucherID, saleID, txType, amount, at)
	}
	tx("t1", "SP-7", "s-issue", "issue", 700)
	tx("t2", "MP-15", "s-issue", "issue", 1500)
	tx("t3", "SP-old", "s-redeem", "redemption", 700)
	tx("t4", "SP-7", "s-void", "redemption", 700)

	check := func(label string, sales []EODTaxBandSale) {
		t.Helper()
		byID := map[string]EODTaxBandSale{}
		for _, s := range sales {
			byID[s.ID] = s
		}
		if len(byID) != 3 {
			t.Fatalf("%s: expected 3 completed sales, got %+v", label, sales)
		}
		iss := byID["s-issue"]
		if len(iss.SinglePurposeVoucherIssues) != 1 || iss.SinglePurposeVoucherIssues[0] != (EODTaxBandVoucherIssue{Amount: 700, RateBP: 700}) {
			t.Fatalf("%s: s-issue single-purpose issues = %+v, want exactly [{700 700}] (the multi-purpose issue must not appear)", label, iss.SinglePurposeVoucherIssues)
		}
		if iss.SinglePurposeRedeemed != 0 {
			t.Fatalf("%s: s-issue single-purpose redeemed = %d, want 0", label, iss.SinglePurposeRedeemed)
		}
		red := byID["s-redeem"]
		if red.SinglePurposeRedeemed != 700 || len(red.SinglePurposeVoucherIssues) != 0 {
			t.Fatalf("%s: s-redeem = redeemed %d issues %+v, want 700 / none", label, red.SinglePurposeRedeemed, red.SinglePurposeVoucherIssues)
		}
		plain := byID["s-plain"]
		if plain.SinglePurposeRedeemed != 0 || len(plain.SinglePurposeVoucherIssues) != 0 {
			t.Fatalf("%s: s-plain must carry no voucher flows, got %+v", label, plain)
		}
	}
	day := b8ExpectedDay(t, d, today, 0, 0)
	sales, err := repo.SalesForTaxBands(ctx, day, day)
	if err != nil {
		t.Fatalf("SalesForTaxBands: %v", err)
	}
	check("range", sales)
	instant, err := repo.SalesForTaxBandsInstant(ctx, today.Add(-time.Hour), today.Add(time.Hour))
	if err != nil {
		t.Fatalf("SalesForTaxBandsInstant: %v", err)
	}
	check("instant", instant)
	window, err := repo.SalesForTaxWindow(ctx, today.Add(-time.Hour), today.Add(time.Hour))
	if err != nil {
		t.Fatalf("SalesForTaxWindow: %v", err)
	}
	check("window", window)
}

// GetSaleDetail carries a voucher's stamped type and rate on the LAN-sync
// journal (additive, omitempty), so a primary replaying a replica's sale
// books the same classification the replica stamped — never re-resolving
// it from its own settings at replay time.
func TestPOSRepo_GetSaleDetail_VoucherIssuesCarryTypeAndRate(t *testing.T) {
	d := b8OpenDB(t, "sp-sale-detail.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	now := time.Now()
	at := b8At(now)
	b8Sale(t, d, "s1", at, "completed", "sale", 0, 2200)
	mustExec(t, d, `INSERT INTO vouchers (id, original_amount, balance, currency, voucher_type, tax_rate_bp, created_at) VALUES ('SP-D', 700, 700, 'EUR', 'single_purpose', 700, ?)`, at)
	mustExec(t, d, `INSERT INTO vouchers (id, original_amount, balance, currency, voucher_type, created_at) VALUES ('MP-D', 1500, 1500, 'EUR', 'multi_purpose', ?)`, at)
	mustExec(t, d, `INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount, created_at) VALUES ('t1', 'SP-D', 's1', 'issue', 700, ?)`, at)
	mustExec(t, d, `INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount, created_at) VALUES ('t2', 'MP-D', 's1', 'issue', 1500, ?)`, at)

	det, ok, err := repo.GetSaleDetail(ctx, "R-s1") // keyed by receipt_no (b8Sale writes "R-"+id)
	if err != nil || !ok {
		t.Fatalf("GetSaleDetail: ok=%v err=%v", ok, err)
	}
	if len(det.VoucherIssues) != 2 {
		t.Fatalf("voucher issues = %+v, want 2", det.VoucherIssues)
	}
	byID := map[string]SaleDetailVoucherIssue{}
	for _, vi := range det.VoucherIssues {
		byID[vi.VoucherID] = vi
	}
	if sp := byID["SP-D"]; sp.VoucherType != VoucherTypeSinglePurpose || sp.TaxRateBP != 700 || sp.Amount != 700 {
		t.Fatalf("SP-D on the wire = %+v, want single_purpose/700/700", sp)
	}
	if mp := byID["MP-D"]; mp.VoucherType != VoucherTypeMultiPurpose || mp.TaxRateBP != 0 {
		t.Fatalf("MP-D on the wire = %+v, want multi_purpose/0", mp)
	}
}
