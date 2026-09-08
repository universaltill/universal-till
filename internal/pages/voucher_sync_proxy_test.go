package pages

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// Cross-till voucher redemption, end to end through the REAL /api/pos/tender
// handler (ut-docs#1668, made atomic by ADR-0084 / ut-docs#1716) — the
// replica side, proven against a REAL registerSyncVouchers mux acting as the
// primary (its own separate DB, wrapped in an httptest.Server) rather than a
// hand-rolled JSON stub: this exercises the actual repository code on the
// "primary" side, not just that the client sends the right bytes.
//
// History these tests encode: #1668's first draft debited the primary in a
// write-through with NO idempotency key, so the replica's own completed sale
// journaled the SAME debit up again (applyJournal forces it through via
// AllowVoucherOverdraft) — round-2 review, 2026-09-07 — and #1668 shipped
// read-only instead. ADR-0084 reinstates the write-through as a RESERVATION
// keyed on (voucher_id, sale_id): the primary IS debited at tender time now
// (so the old "primary untouched" assertions are inverted by design), and
// the journal replay recognizes that key and does not debit again
// (TestPOSTender_CrossTillVoucherRedemption_ThenJournalReplay_DebitsExactlyOnce
// is the direct regression test; TestCrossTillVoucherRace_… is ut-docs#1716
// acceptance criterion #2 literally).

// primaryCallLog records every request the replica made to the primary, so a
// test can assert not just the primary's final state but the exact
// reserve/release sequence that produced it.
type primaryCallLog struct {
	mu    sync.Mutex
	calls []string // "METHOD /path body"
}

func (l *primaryCallLog) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(body)))
}

func (l *primaryCallLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

// newRecordingVoucherPrimaryServer spins up an authentic primary: a real
// registerSyncVouchers mux over its own DB, so reserve/release here go
// through the SAME repository paths sync_vouchers_test.go pins directly.
// Also returns the primary's own common.Deps so a test can drive
// applyJournal against it directly, simulating the sale's journal actually
// arriving, and a call log of everything the replica sent.
func newRecordingVoucherPrimaryServer(t *testing.T, bearer string) (*httptest.Server, *common.Deps, *data.POSRepo, *primaryCallLog) {
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
	log := &primaryCallLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, dp, data.NewPOSRepo(dbase.DB), log
}

func newVoucherPrimaryServer(t *testing.T, bearer string) (*httptest.Server, *common.Deps, *data.POSRepo) {
	t.Helper()
	srv, dp, repo, _ := newRecordingVoucherPrimaryServer(t, bearer)
	return srv, dp, repo
}

func seedPrimaryVoucher(t *testing.T, repo *data.POSRepo, id, holder string, balance int64) {
	t.Helper()
	if err := repo.CreateVoucher(context.Background(), nil, data.Voucher{
		ID: id, HolderLabel: holder, OriginalAmountMinor: balance, BalanceMinor: balance,
		Currency: "GBP", CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed voucher %s on primary: %v", id, err)
	}
}

func primaryRedemptionRows(t *testing.T, dp *common.Deps, voucherID string) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM voucher_transactions WHERE voucher_id = ? AND type = 'redemption'`, voucherID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A voucher issued at another till — this replica has NEVER seen it locally
// — must still be redeemable here while the primary is reachable: before
// ut-docs#1668, this failed closed with ErrVoucherNotFound (no local row to
// debit at all). Under ADR-0084 the primary is RESERVED at tender time — it
// is debited synchronously, once, under this sale's id — and the replica's
// own local mirror is debited too.
func TestPOSTender_CrossTillVoucherRedemption_UnknownLocallyButKnownOnPrimary(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	primary, primaryDp, primaryRepo, calls := newRecordingVoucherPrimaryServer(t, "b-replica")
	seedPrimaryVoucher(t, primaryRepo, "GS-REMOTE", "Remote Alice", 2000)
	setReplicaSettings(t, dp.Settings, primary.URL, "b-replica")

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan item: %v", err)
	}
	rec := postTenderJSON(t, mux, `{"payments":[{"method":"voucher","amount":300,"voucher_id":"GS-REMOTE"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("cross-till redemption tender failed: code %d body %s", rec.Code, rec.Body.String())
	}

	// A real local sale was completed.
	var saleID string
	if err := dp.Db.QueryRow(`SELECT id FROM sales`).Scan(&saleID); err != nil {
		t.Fatalf("exactly one local sale expected: %v", err)
	}

	// This till's own local vouchers row was mirrored (from the PRE-debit
	// snapshot the primary answered with) and then debited locally.
	var localBalance int64
	if err := dp.Db.QueryRow(`SELECT balance FROM vouchers WHERE id = 'GS-REMOTE'`).Scan(&localBalance); err != nil {
		t.Fatalf("local mirror row: %v", err)
	}
	if localBalance != 1700 {
		t.Fatalf("local mirrored balance = %d, want 1700 (2000 - 300; 1400 would mean the mirror was seeded from the POST-debit snapshot — brief addendum correction 5)", localBalance)
	}
	var localTxCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM voucher_transactions WHERE voucher_id = 'GS-REMOTE' AND type = 'redemption' AND sale_id = ?`, saleID).Scan(&localTxCount); err != nil {
		t.Fatal(err)
	}
	if localTxCount != 1 {
		t.Fatalf("local redemption ledger rows = %d, want 1", localTxCount)
	}

	// The PRIMARY was reserved — debited exactly once, synchronously, and
	// its redemption row carries the SAME sale id the local sale has (the
	// idempotency key the journal replay will find). Under ut-docs#1668
	// this read 2000 "untouched"; ADR-0084 inverts that by design.
	pv, err := primaryRepo.GetVoucherBalance(context.Background(), nil, "GS-REMOTE")
	if err != nil {
		t.Fatalf("primary balance: %v", err)
	}
	if pv.BalanceMinor != 1700 {
		t.Fatalf("primary balance = %d, want 1700 (reserved once at tender time)", pv.BalanceMinor)
	}
	if ok, _ := primaryRepo.VoucherRedemptionRecorded(context.Background(), nil, "GS-REMOTE", saleID); !ok {
		t.Fatalf("primary has no redemption row for (GS-REMOTE, %s) — the reservation must be keyed on the replica's own sale id", saleID)
	}
	if n := primaryRedemptionRows(t, primaryDp, "GS-REMOTE"); n != 1 {
		t.Fatalf("primary redemption rows = %d, want exactly 1", n)
	}
	// Exactly one reserve, no release.
	var reserves, releases int
	for _, c := range calls.all() {
		switch {
		case strings.HasPrefix(c, "POST /api/sync/vouchers/GS-REMOTE/redeem "):
			reserves++
			if !strings.Contains(c, `"sale_id":"`+saleID+`"`) || !strings.Contains(c, `"amount_minor":300`) {
				t.Fatalf("reserve call carried the wrong body: %s", c)
			}
		case strings.HasPrefix(c, "POST /api/sync/vouchers/GS-REMOTE/release "):
			releases++
		}
	}
	if reserves != 1 || releases != 0 {
		t.Fatalf("primary saw %d reserve(s) and %d release(s), want 1/0\ncalls: %v", reserves, releases, calls.all())
	}
}

// THE regression test for the round-2 review's blocker, now closed for
// real: after a cross-till redemption is RESERVED on the primary at tender
// time (balance 1700), the sale's journal reaching the primary must find
// that reservation via the (voucher_id, sale_id) key and NOT debit again —
// the primary stays at 1700, not 1400. Asserted against the
// POST-reservation balance, which is exactly what fails without ADR-0084's
// idempotency skip in pos.CompleteSale.
func TestPOSTender_CrossTillVoucherRedemption_ThenJournalReplay_DebitsExactlyOnce(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	primary, primaryDp, primaryRepo := newVoucherPrimaryServer(t, "b-replica")
	seedPrimaryVoucher(t, primaryRepo, "GS-REMOTE", "Remote Alice", 2000)
	setReplicaSettings(t, dp.Settings, primary.URL, "b-replica")

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan item: %v", err)
	}
	rec := postTenderJSON(t, mux, `{"payments":[{"method":"voucher","amount":300,"voucher_id":"GS-REMOTE"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("cross-till redemption tender failed: code %d body %s", rec.Code, rec.Body.String())
	}
	// Precondition: the reservation already debited the primary once.
	if pv, _ := primaryRepo.GetVoucherBalance(context.Background(), nil, "GS-REMOTE"); pv.BalanceMinor != 1700 {
		t.Fatalf("primary balance after tender = %d, want 1700 (reserved)", pv.BalanceMinor)
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
		t.Fatalf("primary balance after journal replay = %d, want STILL 1700 — 1400 means the replay debited on top of the reservation (the round-2 review's blocker, back)", pv.BalanceMinor)
	}
	if n := primaryRedemptionRows(t, primaryDp, "GS-REMOTE"); n != 1 {
		t.Fatalf("primary redemption ledger rows = %d, want exactly 1 (the reservation's own; the replay must not add a second)", n)
	}
	// …while the replayed sale and its payment row landed normally.
	var payCount int
	if err := primaryDp.Db.QueryRow(`SELECT COUNT(*) FROM payments WHERE sale_id = ? AND voucher_id = 'GS-REMOTE'`, journal.Sale.ID).Scan(&payCount); err != nil {
		t.Fatal(err)
	}
	if payCount != 1 {
		t.Fatalf("replayed payment rows = %d, want 1", payCount)
	}
}

// The primary's authoritative refusal (insufficient balance here) must abort
// the tender outright — no sale row, no local voucher row created, nothing
// reserved on the primary (a refused /redeem commits nothing), and the
// replica's best-effort release of it is a harmless no-op.
func TestPOSTender_CrossTillVoucherRedemption_PrimaryRefusalAbortsWithNoSaleRow(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	primary, primaryDp, primaryRepo := newVoucherPrimaryServer(t, "b-replica")
	seedPrimaryVoucher(t, primaryRepo, "GS-REMOTE2", "", 100)
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

	// The primary's own balance is untouched and holds no reservation row.
	pv, err := primaryRepo.GetVoucherBalance(context.Background(), nil, "GS-REMOTE2")
	if err != nil || pv.BalanceMinor != 100 {
		t.Fatalf("primary balance after refusal = %d (err %v), want unchanged 100", pv.BalanceMinor, err)
	}
	if n := primaryRedemptionRows(t, primaryDp, "GS-REMOTE2"); n != 0 {
		t.Fatalf("a refused reserve must not leave a redemption row, got %d", n)
	}
}

// Round-2 review blocker 2's exact shape, closed by ADR-0084 Decision 3: a
// sale with TWO tracked voucher payments, the SECOND of which the primary
// refuses. The FIRST was genuinely RESERVED on the primary (a real debit),
// so the replica must RELEASE it — synchronously, in the same request,
// before returning — leaving the primary exactly as it was and no sale
// anywhere. The tender as a whole fails with the SECOND voucher's refusal.
func TestPOSTender_CrossTillVoucherRedemption_SecondPaymentRefusalReleasesFirst(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	primary, primaryDp, primaryRepo, calls := newRecordingVoucherPrimaryServer(t, "b-replica")
	seedPrimaryVoucher(t, primaryRepo, "GS-OK", "", 2000)
	seedPrimaryVoucher(t, primaryRepo, "GS-SHORT", "", 50)
	setReplicaSettings(t, dp.Settings, primary.URL, "b-replica")

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan item: %v", err)
	}
	// 3.00 due, split 1.00 GS-OK (comfortably covered) + 2.00 GS-SHORT
	// (only 0.50 available) — the second payment's reservation refuses.
	rec := postTenderJSON(t, mux, `{"payments":[{"method":"voucher","amount":100,"voucher_id":"GS-OK"},{"method":"voucher","amount":200,"voucher_id":"GS-SHORT"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("refusal should render the basket with a toast (200), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "does not cover") {
		t.Fatalf("the tender must fail with GS-SHORT's refusal: %s", rec.Body.String())
	}

	var saleCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&saleCount); err != nil {
		t.Fatal(err)
	}
	if saleCount != 0 {
		t.Fatalf("a sale where one voucher payment is refused must persist 0 sales, got %d", saleCount)
	}
	var localTxCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM voucher_transactions`).Scan(&localTxCount); err != nil {
		t.Fatal(err)
	}
	if localTxCount != 0 {
		t.Fatalf("no local voucher_transactions row should exist — the local sale never ran, got %d", localTxCount)
	}
	var okLocalBalance sql.NullInt64
	if err := dp.Db.QueryRow(`SELECT balance FROM vouchers WHERE id = 'GS-OK'`).Scan(&okLocalBalance); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if okLocalBalance.Valid && okLocalBalance.Int64 != 2000 {
		t.Fatalf("GS-OK local balance = %d, want either no local row or the untouched 2000 mirror", okLocalBalance.Int64)
	}

	// The primary saw GS-OK reserved and then RELEASED for the same sale id;
	// GS-SHORT's reserve was refused and its (no-op) release is harmless.
	var reserveSale, releaseSale string
	for _, c := range calls.all() {
		if strings.HasPrefix(c, "POST /api/sync/vouchers/GS-OK/redeem ") {
			reserveSale = c
		}
		if strings.HasPrefix(c, "POST /api/sync/vouchers/GS-OK/release ") {
			releaseSale = c
		}
	}
	if reserveSale == "" || releaseSale == "" {
		t.Fatalf("expected GS-OK to be reserved AND released on the primary\ncalls: %v", calls.all())
	}
	reserveSaleID := strings.TrimSuffix(strings.Split(reserveSale, `"sale_id":"`)[1], `","amount_minor":100}`)
	if !strings.Contains(releaseSale, `"sale_id":"`+reserveSaleID+`"`) {
		t.Fatalf("release was not for the reserved sale id %q: %s", reserveSaleID, releaseSale)
	}
	okV, err := primaryRepo.GetVoucherBalance(context.Background(), nil, "GS-OK")
	if err != nil || okV.BalanceMinor != 2000 || okV.Status != "active" {
		t.Fatalf("GS-OK on primary after aborted sale = %+v (err %v), want 2000/active — the reservation was not released (the orphaned-debit regression)", okV, err)
	}
	if n := primaryRedemptionRows(t, primaryDp, "GS-OK"); n != 0 {
		t.Fatalf("GS-OK redemption rows on primary after release = %d, want 0", n)
	}
	shortV, err := primaryRepo.GetVoucherBalance(context.Background(), nil, "GS-SHORT")
	if err != nil || shortV.BalanceMinor != 50 {
		t.Fatalf("GS-SHORT balance after aborted sale = %d (err %v), want untouched 50", shortV.BalanceMinor, err)
	}
}

// ADR-0084 Decision 3, the other trigger: the primary approves the
// reservation, then this till's OWN pos.CompleteSale fails for a reason
// unrelated to vouchers (here: the article is out of stock and negative
// inventory is not allowed). The reservation must be released on the
// primary — same till, same request — and the primary ends exactly where
// it started.
func TestPOSTender_CrossTillVoucherRedemption_LocalSaleFailureReleasesReservation(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	primary, primaryDp, primaryRepo, calls := newRecordingVoucherPrimaryServer(t, "b-replica")
	seedPrimaryVoucher(t, primaryRepo, "GS-REL", "", 2000)
	setReplicaSettings(t, dp.Settings, primary.URL, "b-replica")
	// Make the replica's OWN pos.CompleteSale fail for a reason unrelated to
	// vouchers, AFTER the reservation loop has run: rename the column
	// InsertPayment writes, so the sale's transaction dies on its payment
	// insert and rolls back — the same column-rename injection
	// TestPOSTender_VoucherIssueCountCapFailsFastAtAPIBoundary uses.
	if _, err := dp.Db.Exec(`ALTER TABLE payments RENAME COLUMN voucher_id TO voucher_id_disabled`); err != nil {
		t.Fatalf("rename payments.voucher_id: %v", err)
	}

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan item: %v", err)
	}
	rec := postTenderJSON(t, mux, `{"payments":[{"method":"voucher","amount":300,"voucher_id":"GS-REL"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("local failure should render the basket with a toast (200), got %d: %s", rec.Code, rec.Body.String())
	}
	var saleCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&saleCount); err != nil {
		t.Fatal(err)
	}
	if saleCount != 0 {
		t.Fatalf("failed tender persisted %d sale(s), want 0", saleCount)
	}

	var sawReserve, sawRelease bool
	var reserveBody string
	for _, c := range calls.all() {
		if strings.HasPrefix(c, "POST /api/sync/vouchers/GS-REL/redeem ") {
			sawReserve = true
			reserveBody = c
		}
		if strings.HasPrefix(c, "POST /api/sync/vouchers/GS-REL/release ") {
			sawRelease = true
			saleID := strings.TrimSuffix(strings.Split(reserveBody, `"sale_id":"`)[1], `","amount_minor":300}`)
			if !strings.Contains(c, `"sale_id":"`+saleID+`"`) {
				t.Fatalf("release for the wrong sale id (reserved %q): %s", saleID, c)
			}
		}
	}
	if !sawReserve || !sawRelease {
		t.Fatalf("expected the primary to see GS-REL reserved (the tender got that far) and then released (the local sale failed)\ncalls: %v", calls.all())
	}
	pv, err := primaryRepo.GetVoucherBalance(context.Background(), nil, "GS-REL")
	if err != nil || pv.BalanceMinor != 2000 {
		t.Fatalf("primary balance after the local failure = %d (err %v), want 2000 (released)", pv.BalanceMinor, err)
	}
	if n := primaryRedemptionRows(t, primaryDp, "GS-REL"); n != 0 {
		t.Fatalf("primary redemption rows after release = %d, want 0", n)
	}
}

// A release that cannot be delivered is the accepted residual-risk shape
// ADR-0084 Decision 4 names (a primary-side debit with no sale behind it),
// so it must surface as a Problem (Warnf → logging.Recent), never vanish.
func TestPOSTender_CrossTillVoucherRedemption_FailedReleaseSurfacesProblem(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	// A primary that reserves happily but cannot release.
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/redeem"):
			writeSyncOrdersJSON(w, http.StatusOK, syncVoucherRow{ID: "GS-BROKEN", Balance: 2000, OriginalAmount: 2000, Currency: "GBP", Status: "active"}, nil)
		default:
			writeSyncOrdersJSON(w, http.StatusInternalServerError, nil, "server error")
		}
	}))
	t.Cleanup(fake.Close)
	setReplicaSettings(t, dp.Settings, fake.URL, "b-replica")
	// Same local-failure injection as the test above: the sale dies on its
	// payment insert after the reservation, so a release is attempted.
	if _, err := dp.Db.Exec(`ALTER TABLE payments RENAME COLUMN voucher_id TO voucher_id_disabled`); err != nil {
		t.Fatalf("rename payments.voucher_id: %v", err)
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan item: %v", err)
	}
	logging.ResetRecent()
	if rec := postTenderJSON(t, mux, `{"payments":[{"method":"voucher","amount":300,"voucher_id":"GS-BROKEN"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
	}
	if n := recentMatches("voucher reservation release failed", "GS-BROKEN"); n != 1 {
		t.Fatalf("expected exactly one Problem naming the unreleased GS-BROKEN reservation, got %d\nrecent: %+v", n, logging.Recent())
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

// Brief addendum, correction 4: a voucher issued at THIS till and not yet
// journaled to the primary 404s on /redeem. That must fall back to the
// local-only path (the local row IS the correct answer), not abort the
// tender — otherwise a till could not redeem its own freshly-issued voucher
// the moment it comes online.
func TestPOSTender_CrossTillVoucherRedemption_OwnIssuedVoucherUnknownOnPrimaryRedeemsLocally(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	primary, primaryDp, _, calls := newRecordingVoucherPrimaryServer(t, "b-replica")
	setReplicaSettings(t, dp.Settings, primary.URL, "b-replica")

	// Issue GS-OWN here (voucher-only sale, paid cash) — the primary never
	// hears about it in this test.
	if rec := postTenderJSON(t, mux, `{"payments":[{"method":"cash","amount":1500}],"issue_vouchers":[{"amount":1500,"code":"GS-OWN"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("issue tender: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan item: %v", err)
	}
	rec := postTenderJSON(t, mux, `{"payments":[{"method":"voucher","amount":300,"voucher_id":"GS-OWN"}]}`)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Voucher not found") {
		t.Fatalf("redeeming an own-issued voucher the primary does not know must succeed locally: %d %s", rec.Code, rec.Body.String())
	}
	var saleCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&saleCount); err != nil {
		t.Fatal(err)
	}
	if saleCount != 2 {
		t.Fatalf("sales = %d, want 2 (issue + redemption)", saleCount)
	}
	var balance int64
	if err := dp.Db.QueryRow(`SELECT balance FROM vouchers WHERE id = 'GS-OWN'`).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 1200 {
		t.Fatalf("local balance = %d, want 1200", balance)
	}
	// The primary was asked (and answered 404) but holds nothing for it.
	var asked bool
	for _, c := range calls.all() {
		if strings.HasPrefix(c, "POST /api/sync/vouchers/GS-OWN/redeem ") {
			asked = true
		}
	}
	if !asked {
		t.Fatalf("the replica should have tried the primary first\ncalls: %v", calls.all())
	}
	if n := primaryRedemptionRows(t, primaryDp, "GS-OWN"); n != 0 {
		t.Fatalf("primary redemption rows for an unknown voucher = %d, want 0", n)
	}
}

// reserveVoucherOnPrimary's fallback shapes, directly: every non-definitive
// answer collapses into reserved=false, err=nil (proceed locally,
// offline-first unchanged); only a 409 with a KNOWN reason is a refusal.
func TestReserveVoucherOnPrimary_FallbackAndRefusalShapes(t *testing.T) {
	_, dp := newVoucherTenderDeps(t)
	ctx := context.Background()

	// Not a replica at all: no settings → no call, no error.
	if v, reserved, err := reserveVoucherOnPrimary(ctx, dp, voucherProxyClient, "GS-X", "sale-1", 100); reserved || err != nil || v.ID != "" {
		t.Fatalf("not a replica: got reserved=%v err=%v v=%+v, want false/nil/empty", reserved, err, v)
	}

	answer := func(status int, body string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	fallbacks := map[string]*httptest.Server{
		"404 (own-issued voucher, correction 4)": answer(http.StatusNotFound, `{"data":null,"error":"not found"}`),
		"500":                                    answer(http.StatusInternalServerError, `{"data":null,"error":"server error"}`),
		"401 (exempt-list regression)":           answer(http.StatusUnauthorized, `{"data":null,"error":"unauthorized"}`),
		"409 unrecognised reason":                answer(http.StatusConflict, `{"data":null,"error":"something_new"}`),
		"200 malformed body":                     answer(http.StatusOK, `{"data":`),
		"200 empty data":                         answer(http.StatusOK, `{"data":null,"error":null}`),
	}
	for name, srv := range fallbacks {
		setReplicaSettings(t, dp.Settings, srv.URL, "b-replica")
		if _, reserved, err := reserveVoucherOnPrimary(ctx, dp, voucherProxyClient, "GS-X", "sale-1", 100); reserved || err != nil {
			t.Fatalf("%s: got reserved=%v err=%v, want false/nil (fall back to local)", name, reserved, err)
		}
	}
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	setReplicaSettings(t, dp.Settings, deadURL, "b-replica")
	if _, reserved, err := reserveVoucherOnPrimary(ctx, dp, voucherProxyClient, "GS-X", "sale-1", 100); reserved || err != nil {
		t.Fatalf("unreachable: got reserved=%v err=%v, want false/nil", reserved, err)
	}

	refusals := map[string]error{
		syncVoucherErrNotActive:           data.ErrVoucherNotActive,
		syncVoucherErrInsufficientBalance: data.ErrVoucherInsufficientBalance,
	}
	for reason, want := range refusals {
		srv := answer(http.StatusConflict, `{"data":null,"error":"`+reason+`"}`)
		setReplicaSettings(t, dp.Settings, srv.URL, "b-replica")
		_, reserved, err := reserveVoucherOnPrimary(ctx, dp, voucherProxyClient, "GS-X", "sale-1", 100)
		if reserved || !errors.Is(err, want) {
			t.Fatalf("409 %s: got reserved=%v err=%v, want the %v sentinel", reason, reserved, err, want)
		}
	}

	// Success carries the primary's snapshot through verbatim.
	ok := answer(http.StatusOK, `{"data":{"id":"GS-X","holder_label":"H","original_amount":500,"balance":500,"currency":"GBP","voucher_type":"multi_purpose","status":"active","issued_sale_id":"","created_at":"2026-09-08T00:00:00Z"},"error":null}`)
	setReplicaSettings(t, dp.Settings, ok.URL, "b-replica")
	v, reserved, err := reserveVoucherOnPrimary(ctx, dp, voucherProxyClient, "GS-X", "sale-1", 100)
	if !reserved || err != nil || v.ID != "GS-X" || v.BalanceMinor != 500 || v.HolderLabel != "H" {
		t.Fatalf("200: got reserved=%v err=%v v=%+v", reserved, err, v)
	}
}

// voucherRedeemWriteThrough's own fallback still holds under the new
// signature: primary unreachable → preauthorized=false, err=nil, and no
// local mirror row is created (nothing was validated anywhere).
func TestVoucherRedeemWriteThrough_PrimaryUnreachableFallsBack(t *testing.T) {
	_, dp := newVoucherTenderDeps(t)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	setReplicaSettings(t, dp.Settings, deadURL, "b-replica")

	preauth, err := voucherRedeemWriteThrough(context.Background(), dp, data.NewPOSRepo(dp.Db), "GS-NOWHERE", 300, "sale-1")
	if preauth || err != nil {
		t.Fatalf("got preauthorized=%v err=%v, want false/nil", preauth, err)
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM vouchers WHERE id = 'GS-NOWHERE'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("fallback must not mirror a local row, got %d", n)
	}
}

// A voucher this till already knows about LOCALLY (it issued it itself, or
// has redeemed against it before) with a STALE balance relative to the
// primary must never be silently clobbered by EnsureVoucherLocalRow — but
// the forced local debit against that stale number can still go negative.
// This must surface as a Problem (warnIfVoucherOverdrawnReason), not vanish
// silently — the round-2 review's should-fix 4. Unchanged by ADR-0084: the
// primary's reservation is the shop-wide truth; the local mirror is not.
func TestPOSTender_CrossTillVoucherRedemption_StaleLocalBalanceOverdrawSurfacesProblem(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	primary, _, primaryRepo := newVoucherPrimaryServer(t, "b-replica")
	// The primary reports a HEALTHY 2000 balance...
	seedPrimaryVoucher(t, primaryRepo, "GS-STALE", "", 2000)
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
	// 3.00 due: the primary reserves it (2000 >= 300), but the LOCAL debit
	// runs against the stale 100 and goes to -200.
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
	if pv, _ := primaryRepo.GetVoucherBalance(context.Background(), nil, "GS-STALE"); pv.BalanceMinor != 1700 {
		t.Fatalf("primary balance = %d, want 1700 (reserved once)", pv.BalanceMinor)
	}
}

// ut-docs#1716 acceptance criterion #2, literally, end to end: one primary
// holding a voucher with balance 100; two replicas each try to redeem 60
// (sum 120 > 100) at the same moment, under two different sale ids, through
// the real replica-side reservation (voucherRedeemWriteThrough → the real
// /redeem handler over HTTP). Exactly one wins and the primary lands on 40.
// The winner then completes its sale locally and its journal replays onto
// the SAME primary; the primary must STILL read 40 — not -20 — because the
// replay recognizes the reservation via (voucher_id, sale_id) and skips its
// own debit. That last assertion is the one that fails without this card
// (proven by reverting pos.CompleteSale's alreadyApplied skip). The loser's
// release is a safe no-op.
func TestCrossTillVoucherRace_OnlyOneReservationWinsAndReplayDoesNotDoubleDebit(t *testing.T) {
	ctx := context.Background()
	primary, primaryDp, primaryRepo := newVoucherPrimaryServer(t, "b-replica")
	seedPrimaryVoucher(t, primaryRepo, "GS-RACE", "Race Holder", 100)

	type replica struct {
		dp     *common.Deps
		repo   *data.POSRepo
		saleID string
	}
	replicas := make([]replica, 2)
	for i, saleID := range []string{"race-sale-A", "race-sale-B"} {
		_, dp := newVoucherTenderDeps(t)
		setReplicaSettings(t, dp.Settings, primary.URL, "b-replica")
		// payments.method_id has a real FK to payment_methods; the live till
		// ensures the 'voucher' method at boot (index_page.go), and the HTTP
		// tender path does too — a direct pos.CompleteSale here needs it
		// seeded, exactly as internal/pos's setupVoucherDB does.
		if _, err := dp.Db.Exec(`INSERT OR IGNORE INTO payment_methods (id, name, type, is_active) VALUES ('voucher', 'Voucher', 'voucher', 1)`); err != nil {
			t.Fatalf("seed voucher payment method on replica %d: %v", i, err)
		}
		replicas[i] = replica{dp: dp, repo: data.NewPOSRepo(dp.Db), saleID: saleID}
	}

	// Both tills reserve 60 against the same voucher as simultaneously as a
	// barrier channel allows (a real race: the primary's BEGIN IMMEDIATE is
	// what serializes them — brief addendum, correction 9).
	type outcome struct {
		preauth bool
		err     error
	}
	outcomes := make([]outcome, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range replicas {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			p, err := voucherRedeemWriteThrough(ctx, replicas[i].dp, replicas[i].repo, "GS-RACE", 60, replicas[i].saleID)
			outcomes[i] = outcome{preauth: p, err: err}
		}(i)
	}
	close(start)
	wg.Wait()

	winner, loser := -1, -1
	for i, o := range outcomes {
		switch {
		case o.err == nil && o.preauth:
			if winner != -1 {
				t.Fatalf("BOTH reservations succeeded — the over-redeem this card exists to close: %+v", outcomes)
			}
			winner = i
		case errors.Is(o.err, data.ErrVoucherInsufficientBalance):
			loser = i
		default:
			t.Fatalf("replica %d: unexpected outcome %+v", i, o)
		}
	}
	if winner == -1 || loser == -1 {
		t.Fatalf("want exactly one winner and one refused loser, got %+v", outcomes)
	}
	pv, err := primaryRepo.GetVoucherBalance(ctx, nil, "GS-RACE")
	if err != nil || pv.BalanceMinor != 40 || pv.Status != "active" {
		t.Fatalf("primary after the race = %+v (err %v), want balance 40 / active", pv, err)
	}
	if n := primaryRedemptionRows(t, primaryDp, "GS-RACE"); n != 1 {
		t.Fatalf("primary redemption rows after the race = %d, want 1", n)
	}

	// The winner completes its sale LOCALLY under the reserved sale id —
	// exactly what completeTender does next (VoucherPreauthorized forces
	// the local debit against the mirrored pre-debit snapshot).
	w := replicas[winner]
	locID, err := w.repo.EnsureStockLocation(ctx)
	if err != nil {
		t.Fatalf("ensure location: %v", err)
	}
	if _, err := pos.CompleteSale(ctx, w.dp.Db, pos.SaleInput{
		SaleType: "sale", SaleID: w.saleID, ReceiptNo: "RACE-W-1",
		Currency: "GBP", TaxInclusive: true, CashierID: "user1",
		AllowNegativeInventory: true,
		Lines: []pos.SaleLineInput{{
			ItemID: "itm1", SKU: "ABC", Name: "Apple", Qty: 1,
			UnitPrice: money.FromMinor(60), TaxRateBasisPoints: 2000, LocationID: locID,
		}},
		Payments: []pos.PaymentInput{{MethodID: "voucher", VoucherID: "GS-RACE", Amount: money.FromMinor(60), VoucherPreauthorized: true}},
	}); err != nil {
		t.Fatalf("winner's local sale: %v", err)
	}
	var localBalance int64
	if err := w.dp.Db.QueryRow(`SELECT balance FROM vouchers WHERE id = 'GS-RACE'`).Scan(&localBalance); err != nil {
		t.Fatal(err)
	}
	if localBalance != 40 {
		t.Fatalf("winner's local mirror balance = %d, want 40 (seeded from the pre-debit 100, then debited 60 locally)", localBalance)
	}

	// …and its journal replays onto the primary, exactly as syncPushTick
	// would (applyJournal → pos.CompleteSale with AllowVoucherOverdraft).
	journal, found, err := buildJournal(ctx, w.repo, "RACE-W-1")
	if err != nil || !found {
		t.Fatalf("buildJournal: found=%v err=%v", found, err)
	}
	applied, quarantineReason, err := applyJournal(ctx, primaryDp, "replica-till", journal)
	if err != nil || !applied || quarantineReason != "" {
		t.Fatalf("applyJournal: applied=%v quarantine=%q err=%v", applied, quarantineReason, err)
	}
	pv, err = primaryRepo.GetVoucherBalance(ctx, nil, "GS-RACE")
	if err != nil {
		t.Fatal(err)
	}
	if pv.BalanceMinor != 40 {
		t.Fatalf("primary balance after the winner's journal replay = %d, want STILL 40 — -20 means the replay debited on top of the reservation (the double-debit this card closes)", pv.BalanceMinor)
	}
	if n := primaryRedemptionRows(t, primaryDp, "GS-RACE"); n != 1 {
		t.Fatalf("primary redemption rows after replay = %d, want exactly 1", n)
	}
	var replayedSales int
	if err := primaryDp.Db.QueryRow(`SELECT COUNT(*) FROM sales WHERE id = ?`, w.saleID).Scan(&replayedSales); err != nil {
		t.Fatal(err)
	}
	if replayedSales != 1 {
		t.Fatalf("the winner's sale did not land on the primary (rows = %d)", replayedSales)
	}

	// The loser was never granted anything: releasing it is a safe no-op.
	logging.ResetRecent()
	releaseVoucherOnPrimary(ctx, replicas[loser].dp, voucherProxyClient, "GS-RACE", replicas[loser].saleID)
	if n := recentMatches("voucher reservation release failed"); n != 0 {
		t.Fatalf("the loser's no-op release must not log a Problem, got %d", n)
	}
	if pv, _ = primaryRepo.GetVoucherBalance(ctx, nil, "GS-RACE"); pv.BalanceMinor != 40 {
		t.Fatalf("primary balance after the loser's release = %d, want 40 (nothing to release)", pv.BalanceMinor)
	}
}

// GET /api/vouchers/{id} (voucher_api.go): a lookup for a voucher this till
// has never locally seen falls through to the primary while reachable,
// answering the "can't be LOOKED UP" half of ut-docs#1668's title, not just
// redemption — and stays a pure READ (fetchVoucherFromPrimary, never the
// reservation).
func TestVoucherAPI_CrossTillLookupFallsThroughToPrimary(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	primary, primaryDp, primaryRepo, calls := newRecordingVoucherPrimaryServer(t, "b-replica")
	seedPrimaryVoucher(t, primaryRepo, "GS-LOOKUP", "Bob", 500)
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
	// row, reserved anything, or touched the primary's balance.
	var localCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM vouchers WHERE id = 'GS-LOOKUP'`).Scan(&localCount); err != nil {
		t.Fatal(err)
	}
	if localCount != 0 {
		t.Fatalf("a read-only lookup must not mirror a local row, got %d", localCount)
	}
	for _, c := range calls.all() {
		if strings.HasPrefix(c, "POST ") {
			t.Fatalf("a read-only lookup must never POST to the primary: %s", c)
		}
	}
	if pv, _ := primaryRepo.GetVoucherBalance(context.Background(), nil, "GS-LOOKUP"); pv.BalanceMinor != 500 {
		t.Fatalf("lookup changed the primary's balance to %d", pv.BalanceMinor)
	}
	if n := primaryRedemptionRows(t, primaryDp, "GS-LOOKUP"); n != 0 {
		t.Fatalf("lookup wrote %d redemption rows", n)
	}
}
