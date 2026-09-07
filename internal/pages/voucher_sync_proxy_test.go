package pages

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cross-till voucher redemption, end to end through the REAL /api/pos/tender
// handler (ut-docs#1668) — the replica side of the fix, proven against a
// REAL registerSyncVouchers mux acting as the primary (its own separate DB,
// wrapped in an httptest.Server) rather than a hand-rolled JSON stub: this
// exercises the actual repository code on the "primary" side, not just that
// the client sends the right bytes.
//
// round-2 review (2026-09-07) found the first draft's design double-debited
// every online cross-till redemption: it committed a real debit on the
// primary synchronously (a write-through), and the replica's own completed
// sale journaled the SAME debit up moments later via the pre-existing,
// unconditional per-sale sync (applyJournal has no idea a write-through
// already applied it, and forces the debit through via
// AllowVoucherOverdraft regardless) — see sync_vouchers.go's file-level
// comment for the fix. TestPOSTender_CrossTillVoucherRedemption_ThenJournalReplay_DebitsExactlyOnce
// below is the direct regression test for exactly that bug.

// newVoucherPrimaryServer spins up an authentic primary for these tests: a
// real registerSyncVouchers mux over its own DB, so lookups here go through
// the SAME GetVoucherBalance path sync_vouchers_test.go pins directly. Also
// returns the primary's own common.Deps so a test can drive applyJournal
// against it directly, simulating the sale's journal actually arriving.
func newVoucherPrimaryServer(t *testing.T, bearer string) (*httptest.Server, *common.Deps, *data.POSRepo) {
	t.Helper()
	mux, dp, dbase := newSyncVouchersTestDeps(t)
	// seedForPages gives this "primary" DB the same baseline catalog fixture
	// (itm1/ABC, tax_std, cash/card payment methods, "user1") the replica's
	// own newVoucherTenderDeps DB has — needed so buildJournal/applyJournal
	// can actually replay a sale line onto this DB without hitting the
	// unknown-item-id FK quarantine (ut-docs#1127), for the tests that
	// simulate the sale's journal reaching this primary.
	seedForPages(t, dbase.DB)
	seedSyncOrdersTill(t, dp, "Replica", bearer)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, dp, data.NewPOSRepo(dbase.DB)
}

// A voucher issued at another till — this replica has NEVER seen it locally
// — must still be redeemable here while the primary is reachable: before
// ut-docs#1668, this failed closed with ErrVoucherNotFound (no local row to
// debit at all). The debit happens exactly once, LOCALLY — the primary is
// only ever READ here, never written (see this file's own top comment for
// why a second, primary-side debit would double-apply once the sale
// journals up — proven separately below).
func TestPOSTender_CrossTillVoucherRedemption_UnknownLocallyButKnownOnPrimary(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	primary, _, primaryRepo := newVoucherPrimaryServer(t, "b-replica")
	if err := primaryRepo.CreateVoucher(context.Background(), nil, data.Voucher{
		ID: "GS-REMOTE", HolderLabel: "Remote Alice", OriginalAmountMinor: 2000, BalanceMinor: 2000,
		Currency: "GBP", CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed voucher on primary: %v", err)
	}
	setReplicaSettings(t, dp.Settings, primary.URL, "b-replica")

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan item: %v", err)
	}
	rec := postTenderJSON(t, mux, `{"payments":[{"method":"voucher","amount":300,"voucher_id":"GS-REMOTE"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("cross-till redemption tender failed: code %d body %s", rec.Code, rec.Body.String())
	}

	// A real local sale was completed.
	var saleCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&saleCount); err != nil {
		t.Fatal(err)
	}
	if saleCount != 1 {
		t.Fatalf("sale count = %d, want 1", saleCount)
	}

	// This till's own local vouchers row was mirrored and debited.
	var localBalance int64
	if err := dp.Db.QueryRow(`SELECT balance FROM vouchers WHERE id = 'GS-REMOTE'`).Scan(&localBalance); err != nil {
		t.Fatalf("local mirror row: %v", err)
	}
	if localBalance != 1700 {
		t.Fatalf("local mirrored balance = %d, want 1700 (2000 - 300)", localBalance)
	}
	var localTxCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM voucher_transactions WHERE voucher_id = 'GS-REMOTE' AND type = 'redemption'`).Scan(&localTxCount); err != nil {
		t.Fatal(err)
	}
	if localTxCount != 1 {
		t.Fatalf("local redemption ledger rows = %d, want 1", localTxCount)
	}

	// The PRIMARY's balance must be UNTOUCHED at this point — this is a
	// validation-only lookup, not a write-through. If this ever reads 1700
	// here (before any journal has run), the primary-side debit regression
	// this card's review found is back.
	pv, err := primaryRepo.GetVoucherBalance(context.Background(), nil, "GS-REMOTE")
	if err != nil {
		t.Fatalf("primary balance: %v", err)
	}
	if pv.BalanceMinor != 2000 {
		t.Fatalf("primary balance = %d, want UNCHANGED 2000 — the primary must only ever be read here, never debited synchronously", pv.BalanceMinor)
	}
}

// THE regression test for the round-2 review's blocker: after a cross-till
// redemption completes locally, the sale's journal reaching the primary
// (the ONE real path a redemption ever debits the primary through) must
// debit it EXACTLY ONCE — not a second time on top of whatever this
// validation-only lookup already did (it does nothing, per the test above,
// but this proves the END-TO-END number is right, not just "primary
// untouched so far").
func TestPOSTender_CrossTillVoucherRedemption_ThenJournalReplay_DebitsExactlyOnce(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	primary, primaryDp, primaryRepo := newVoucherPrimaryServer(t, "b-replica")
	if err := primaryRepo.CreateVoucher(context.Background(), nil, data.Voucher{
		ID: "GS-REMOTE", HolderLabel: "Remote Alice", OriginalAmountMinor: 2000, BalanceMinor: 2000,
		Currency: "GBP", CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed voucher on primary: %v", err)
	}
	setReplicaSettings(t, dp.Settings, primary.URL, "b-replica")

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan item: %v", err)
	}
	rec := postTenderJSON(t, mux, `{"payments":[{"method":"voucher","amount":300,"voucher_id":"GS-REMOTE"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("cross-till redemption tender failed: code %d body %s", rec.Code, rec.Body.String())
	}

	var receiptNo string
	if err := dp.Db.QueryRow(`SELECT receipt_no FROM sales LIMIT 1`).Scan(&receiptNo); err != nil {
		t.Fatalf("read the completed sale's receipt: %v", err)
	}
	localRepo := data.NewPOSRepo(dp.Db)
	journal, found, err := buildJournal(context.Background(), localRepo, receiptNo)
	if err != nil || !found {
		t.Fatalf("buildJournal %s: found=%v err=%v", receiptNo, found, err)
	}

	// Simulate the replica's own sales journal reaching the primary, exactly
	// as syncPushTick does moments after any completed sale.
	applied, quarantineReason, err := applyJournal(context.Background(), primaryDp, "replica-till", journal)
	if err != nil {
		t.Fatalf("applyJournal: %v", err)
	}
	if quarantineReason != "" {
		t.Fatalf("journal was quarantined: %s", quarantineReason)
	}
	if !applied {
		t.Fatal("expected the journal to apply")
	}

	pv, err := primaryRepo.GetVoucherBalance(context.Background(), nil, "GS-REMOTE")
	if err != nil {
		t.Fatalf("primary balance after journal replay: %v", err)
	}
	if pv.BalanceMinor != 1700 {
		t.Fatalf("primary balance after journal replay = %d, want 1700 (debited EXACTLY ONCE) — 1400 would mean the redemption was double-applied (the round-2 review's blocker)", pv.BalanceMinor)
	}
	var redemptionCount int
	if err := primaryDp.Db.QueryRow(`SELECT COUNT(*) FROM voucher_transactions WHERE voucher_id = 'GS-REMOTE' AND type = 'redemption'`).Scan(&redemptionCount); err != nil {
		t.Fatal(err)
	}
	if redemptionCount != 1 {
		t.Fatalf("primary redemption ledger rows = %d, want exactly 1", redemptionCount)
	}
}

// The primary's authoritative refusal (insufficient balance here) must abort
// the tender outright — no sale row, no local voucher row created. Unlike
// the first draft, there is nothing to ORPHAN here either: this validation
// check never mutates the primary, so a refusal simply means the tender
// never proceeds anywhere, on either till.
func TestPOSTender_CrossTillVoucherRedemption_PrimaryRefusalAbortsWithNoSaleRow(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	primary, _, primaryRepo := newVoucherPrimaryServer(t, "b-replica")
	if err := primaryRepo.CreateVoucher(context.Background(), nil, data.Voucher{
		ID: "GS-REMOTE2", OriginalAmountMinor: 100, BalanceMinor: 100, Currency: "GBP",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed voucher on primary: %v", err)
	}
	setReplicaSettings(t, dp.Settings, primary.URL, "b-replica")

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan item: %v", err)
	}
	// 3.00 due, the primary's voucher holds only 1.00 — refused centrally.
	rec := postTenderJSON(t, mux, `{"payments":[{"method":"voucher","amount":300,"voucher_id":"GS-REMOTE2"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("refusal should render the basket with a toast (200), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "does not cover") {
		t.Fatalf("missing the localized voucher-insufficient toast: %s", rec.Body.String())
	}

	var saleCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&saleCount); err != nil {
		t.Fatal(err)
	}
	if saleCount != 0 {
		t.Fatalf("refused tender persisted %d sale(s), want 0", saleCount)
	}
	var localVoucherCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM vouchers WHERE id = 'GS-REMOTE2'`).Scan(&localVoucherCount); err != nil {
		t.Fatal(err)
	}
	if localVoucherCount != 0 {
		t.Fatalf("a refused redemption must not mirror a local row, got %d", localVoucherCount)
	}

	// The primary's own balance is untouched — it was only ever read.
	pv, err := primaryRepo.GetVoucherBalance(context.Background(), nil, "GS-REMOTE2")
	if err != nil || pv.BalanceMinor != 100 {
		t.Fatalf("primary balance after refusal = %d (err %v), want unchanged 100", pv.BalanceMinor, err)
	}
}

// Round-2 review blocker 2's exact shape: a sale with TWO tracked voucher
// payments, the SECOND of which the primary refuses. Because nothing is
// ever debited on the primary during this pre-check, and pos.CompleteSale
// is one all-or-nothing local transaction, there is no debit anywhere to
// orphan when the sale as a whole is refused — including on the FIRST
// voucher, which the primary would have happily approved.
func TestPOSTender_CrossTillVoucherRedemption_SecondPaymentRefusalOrphansNothing(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	primary, _, primaryRepo := newVoucherPrimaryServer(t, "b-replica")
	if err := primaryRepo.CreateVoucher(context.Background(), nil, data.Voucher{
		ID: "GS-OK", OriginalAmountMinor: 2000, BalanceMinor: 2000, Currency: "GBP",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed GS-OK on primary: %v", err)
	}
	if err := primaryRepo.CreateVoucher(context.Background(), nil, data.Voucher{
		ID: "GS-SHORT", OriginalAmountMinor: 50, BalanceMinor: 50, Currency: "GBP",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed GS-SHORT on primary: %v", err)
	}
	setReplicaSettings(t, dp.Settings, primary.URL, "b-replica")

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan item: %v", err)
	}
	// 3.00 due, split 1.00 GS-OK (comfortably covered) + 2.00 GS-SHORT
	// (only 0.50 available) — the second payment's primary check refuses.
	rec := postTenderJSON(t, mux, `{"payments":[{"method":"voucher","amount":100,"voucher_id":"GS-OK"},{"method":"voucher","amount":200,"voucher_id":"GS-SHORT"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("refusal should render the basket with a toast (200), got %d: %s", rec.Code, rec.Body.String())
	}

	var saleCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&saleCount); err != nil {
		t.Fatal(err)
	}
	if saleCount != 0 {
		t.Fatalf("a sale where one voucher payment is refused must persist 0 sales, got %d", saleCount)
	}
	// GS-OK's own validation succeeded before GS-SHORT's refusal aborted the
	// sale, so it may have been harmlessly mirrored locally (EnsureVoucherLocalRow
	// runs outside the sale's own transaction) — that's fine, since nothing
	// about it was ever debited. What actually matters (the review's
	// blocker) is that no debit/ledger row exists anywhere for a sale that
	// never completed.
	var localTxCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM voucher_transactions`).Scan(&localTxCount); err != nil {
		t.Fatal(err)
	}
	if localTxCount != 0 {
		t.Fatalf("no local voucher_transactions row should exist — nothing was ever debited, got %d", localTxCount)
	}
	var okLocalBalance sql.NullInt64
	if err := dp.Db.QueryRow(`SELECT balance FROM vouchers WHERE id = 'GS-OK'`).Scan(&okLocalBalance); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if okLocalBalance.Valid && okLocalBalance.Int64 != 2000 {
		t.Fatalf("GS-OK local balance = %d, want either no local row at all or untouched 2000 — never debited despite the sale as a whole never completing", okLocalBalance.Int64)
	}

	// Neither voucher on the primary moved — GS-OK, which would have been
	// perfectly valid on its own, must not have been debited and left
	// dangling just because GS-SHORT failed afterward.
	okV, err := primaryRepo.GetVoucherBalance(context.Background(), nil, "GS-OK")
	if err != nil || okV.BalanceMinor != 2000 {
		t.Fatalf("GS-OK balance after aborted sale = %d (err %v), want untouched 2000 — this is the orphaned-debit regression", okV.BalanceMinor, err)
	}
	shortV, err := primaryRepo.GetVoucherBalance(context.Background(), nil, "GS-SHORT")
	if err != nil || shortV.BalanceMinor != 50 {
		t.Fatalf("GS-SHORT balance after aborted sale = %d (err %v), want untouched 50", shortV.BalanceMinor, err)
	}
}

// Primary unreachable: this till falls back to today's exact behaviour — a
// voucher it has never locally seen still fails closed, offline-first
// unchanged, no different from before this card shipped.
func TestPOSTender_CrossTillVoucherRedemption_PrimaryUnreachableFallsBackToLocalNotFound(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	setReplicaSettings(t, dp.Settings, deadURL, "b-replica")

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan item: %v", err)
	}
	rec := postTenderJSON(t, mux, `{"payments":[{"method":"voucher","amount":300,"voucher_id":"GS-NEVER-SEEN"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("fallback rejection should render the basket with a toast (200), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Voucher not found or no longer active") {
		t.Fatalf("missing the localized voucher-not-found toast: %s", rec.Body.String())
	}
	var saleCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&saleCount); err != nil {
		t.Fatal(err)
	}
	if saleCount != 0 {
		t.Fatalf("refused tender persisted %d sale(s), want 0", saleCount)
	}
}

// A voucher this till already knows about LOCALLY (it issued it itself, or
// has redeemed against it before) with a STALE balance relative to the
// primary must never be silently clobbered by EnsureVoucherLocalRow — but
// the forced local debit against that stale number can still go negative.
// This must surface as a Problem (warnIfVoucherOverdrawnReason), not vanish
// silently — the round-2 review's should-fix 4.
func TestPOSTender_CrossTillVoucherRedemption_StaleLocalBalanceOverdrawSurfacesProblem(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	primary, _, primaryRepo := newVoucherPrimaryServer(t, "b-replica")
	// The primary reports a HEALTHY 2000 balance...
	if err := primaryRepo.CreateVoucher(context.Background(), nil, data.Voucher{
		ID: "GS-STALE", OriginalAmountMinor: 2000, BalanceMinor: 2000, Currency: "GBP",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed voucher on primary: %v", err)
	}
	// ...but THIS till already has its own, much lower local view (as if it
	// redeemed against this same voucher earlier while offline).
	localRepo := data.NewPOSRepo(dp.Db)
	if err := localRepo.CreateVoucher(context.Background(), nil, data.Voucher{
		ID: "GS-STALE", OriginalAmountMinor: 2000, BalanceMinor: 100, Currency: "GBP",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed stale local voucher: %v", err)
	}
	setReplicaSettings(t, dp.Settings, primary.URL, "b-replica")

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan item: %v", err)
	}
	// 3.00 due: the primary happily validates it (2000 >= 300), but the
	// LOCAL debit runs against the stale 100 and goes to -200.
	rec := postTenderJSON(t, mux, `{"payments":[{"method":"voucher","amount":300,"voucher_id":"GS-STALE"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("tender failed: code %d body %s", rec.Code, rec.Body.String())
	}
	var localBalance int64
	if err := dp.Db.QueryRow(`SELECT balance FROM vouchers WHERE id = 'GS-STALE'`).Scan(&localBalance); err != nil {
		t.Fatal(err)
	}
	if localBalance != -200 {
		t.Fatalf("local balance = %d, want -200 (the forced debit against the STALE local row, never clobbered by the mirror)", localBalance)
	}
}

// GET /api/vouchers/{id} (voucher_api.go): a lookup for a voucher this till
// has never locally seen falls through to the primary while reachable,
// answering the "can't be LOOKED UP" half of ut-docs#1668's title, not just
// redemption.
func TestVoucherAPI_CrossTillLookupFallsThroughToPrimary(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	primary, _, primaryRepo := newVoucherPrimaryServer(t, "b-replica")
	if err := primaryRepo.CreateVoucher(context.Background(), nil, data.Voucher{
		ID: "GS-LOOKUP", HolderLabel: "Bob", OriginalAmountMinor: 500, BalanceMinor: 500,
		Currency: "GBP", CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed voucher on primary: %v", err)
	}
	setReplicaSettings(t, dp.Settings, primary.URL, "b-replica")

	req := httptest.NewRequest(http.MethodGet, "/api/vouchers/GS-LOOKUP", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/vouchers/GS-LOOKUP = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"id":"GS-LOOKUP"`, `"balance":500`, `"holder_label":"Bob"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("lookup payload missing %s: %s", want, rec.Body.String())
		}
	}

	// This is a READ-ONLY lookup — it must not have created a local mirror
	// row (only the redemption pre-check mirrors, and only when it's about
	// to debit).
	var localCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM vouchers WHERE id = 'GS-LOOKUP'`).Scan(&localCount); err != nil {
		t.Fatal(err)
	}
	if localCount != 0 {
		t.Fatalf("a read-only lookup must not mirror a local row, got %d", localCount)
	}
}
