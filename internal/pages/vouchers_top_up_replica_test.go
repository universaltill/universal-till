package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// Voucher top-up from an additional till (ut-docs#4416). The replica asks
// the main till for the voucher (GET /api/sync/vouchers/{id}, whose answer
// carries accepts_top_up), judges the main till's row, seeds its local
// mirror and adds the basket line; its own sale force-credits the mirror
// and the main till force-credits the journaled sale on replay.

func TestSyncVouchers_GetCarriesAcceptsTopUp(t *testing.T) {
	mux, dp, dbase := newSyncVouchersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	seedSyncVoucher(t, data.NewPOSRepo(dbase.DB), "GS-1", 1000)

	rec := getSyncVoucher(mux, "GS-1", "bearer-t2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	var raw struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if v, ok := raw.Data["accepts_top_up"].(bool); !ok || !v {
		t.Fatalf("answer %s: want accepts_top_up true", rec.Body.String())
	}
	if raw.Data["id"] != "GS-1" || raw.Data["balance"] != float64(1000) {
		t.Fatalf("the row fields must stay flat beside accepts_top_up: %s", rec.Body.String())
	}

	// /redeem and /adjust answers are unchanged on the wire.
	rec = postSyncVoucher(mux, "GS-1", "adjust", `{"adjustment_id":"adj-1","expected_balance_minor":1000,"new_balance_minor":900,"reason":"x","actor":"u"}`, "bearer-t2")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "accepts_top_up") {
		t.Fatalf("/adjust answer = %d %s, must not carry accepts_top_up", rec.Code, rec.Body.String())
	}
}

// newReplicaTopUpDeps is the top-up POS harness pointed at a recording main
// till; the replica holds no row for the vouchers seeded on the main till.
func newReplicaTopUpDeps(t *testing.T) (*http.ServeMux, *common.Deps, *common.Deps, *data.POSRepo) {
	t.Helper()
	mux, dp := newTopUpTestDeps(t)
	primary, primaryDp, primaryRepo := newVoucherPrimaryServer(t, "b-replica")
	setReplicaSettings(t, dp.Settings, primary.URL, "b-replica")
	return mux, dp, primaryDp, primaryRepo
}

func localVoucherRows(t *testing.T, dp *common.Deps, id string) int {
	t.Helper()
	return countRows(t, dp, `SELECT COUNT(*) FROM vouchers WHERE id = '`+id+`'`)
}

func TestVoucherTopUp_ReplicaAddsLineAndSeedsMirror(t *testing.T) {
	mux, dp, _, primaryRepo := newReplicaTopUpDeps(t)
	seedPrimaryVoucher(t, primaryRepo, "GS-R1", "Remote Rita", 2000)
	if n := localVoucherRows(t, dp, "GS-R1"); n != 0 {
		t.Fatalf("precondition: replica already has %d local rows", n)
	}

	rec := vTopUp(t, mux, "GS-R1", "25", &vManager, true)
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/" {
		t.Fatalf("replica top-up = %d HX-Redirect %q (%s)", rec.Code, rec.Header().Get("HX-Redirect"), rec.Body.String())
	}
	lines := dp.Engine.Lines()
	if len(lines) != 1 || lines[0].GiftVoucher == nil || !lines[0].GiftVoucher.TopUp || lines[0].GiftVoucher.Code != "GS-R1" || lines[0].GiftVoucher.HolderLabel != "Remote Rita" || lines[0].PriceCents.Minor() != 2500 {
		t.Fatalf("basket = %+v", lines)
	}
	local, err := data.NewPOSRepo(dp.Db).GetVoucherBalance(context.Background(), nil, "GS-R1")
	if err != nil {
		t.Fatalf("local mirror row missing: %v", err)
	}
	if local.BalanceMinor != 2000 || local.Status != "active" || local.HolderLabel != "Remote Rita" {
		t.Fatalf("local mirror = %+v, want the main till's 2000 active", local)
	}
	// The detail page (it needs the local row just seeded) shows the form on
	// a replica now, with no main-till-only note.
	page := vGet(t, mux, "/vouchers/GS-R1?lang=en", &vManager, false).Body.String()
	if !strings.Contains(page, `hx-post="/vouchers/GS-R1/top-up"`) || strings.Contains(page, httpx.T("en", "vouchers.top_up.replica_note")) {
		t.Fatal("a replica must show the Top up form, not the replica note")
	}
	// Nothing credited anywhere until the sale is paid.
	if v := vBalance(t, primaryRepo, "GS-R1"); v.BalanceMinor != 2000 {
		t.Fatalf("main till balance moved to %d before payment", v.BalanceMinor)
	}
}

func TestVoucherTopUp_ReplicaRefusals(t *testing.T) {
	mux, dp, primaryDp, primaryRepo := newReplicaTopUpDeps(t)
	ctx := context.Background()
	seedPrimaryVoucher(t, primaryRepo, "GS-VOID", "", 500)
	if _, err := primaryDp.Db.Exec(`UPDATE vouchers SET status = 'void', balance = 0 WHERE id = 'GS-VOID'`); err != nil {
		t.Fatal(err)
	}
	seedPrimaryVoucher(t, primaryRepo, "GS-FULL", "", pos.MaxVoucherIssueAmount.Minor()-100)
	if err := primaryRepo.CreateVoucher(ctx, nil, data.Voucher{ID: "GS-SP", OriginalAmountMinor: 500, BalanceMinor: 500, Currency: "GBP", Purpose: data.VoucherPurposeSingle, CreatedAt: "2026-10-01T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	// This till's own mirror of GS-VOID is a stale ACTIVE one: the verdict
	// must come from the main till's row, not this one.
	if err := data.NewPOSRepo(dp.Db).EnsureVoucherLocalRow(ctx, nil, data.Voucher{ID: "GS-VOID", OriginalAmountMinor: 500, BalanceMinor: 500, Currency: "GBP", CreatedAt: "2026-10-01T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	// …and GS-FULL's local mirror is low: the ceiling is judged on the main
	// till's balance.
	if err := data.NewPOSRepo(dp.Db).EnsureVoucherLocalRow(ctx, nil, data.Voucher{ID: "GS-FULL", OriginalAmountMinor: 100, BalanceMinor: 100, Currency: "GBP", CreatedAt: "2026-10-01T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, id string
		code     int
		key      string
	}{
		{"void on the main till", "GS-VOID", http.StatusConflict, "vouchers.top_up.not_void"},
		{"unknown on the main till", "GS-NOPE", http.StatusNotFound, "vouchers.error.not_found"},
		{"single purpose on the main till", "GS-SP", http.StatusConflict, "vouchers.top_up.not_single"},
		{"over the ceiling by the main till's balance", "GS-FULL", http.StatusBadRequest, "vouchers.top_up.error.amount"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := vTopUp(t, mux, tc.id, "5", &vManager, true)
			if rec.Code != tc.code {
				t.Fatalf("code = %d, want %d (%s)", rec.Code, tc.code, rec.Body.String())
			}
			want := httpx.T("en", tc.key)
			if i := strings.Index(want, "%"); i >= 0 {
				want = want[:i]
			}
			if !strings.Contains(rec.Body.String(), html.EscapeString(want)) {
				t.Fatalf("body = %s, want %s", rec.Body.String(), tc.key)
			}
		})
	}
	if n := len(dp.Engine.Lines()); n != 0 {
		t.Fatalf("refused replica top-ups added %d line(s)", n)
	}
	if n := localVoucherRows(t, dp, "GS-NOPE") + localVoucherRows(t, dp, "GS-SP"); n != 0 {
		t.Fatalf("a refused top-up seeded %d local mirror row(s)", n)
	}
}

func TestVoucherTopUp_ReplicaMainTillUnreachableRefuses(t *testing.T) {
	mux, dp := newTopUpTestDeps(t)
	topUpSeedVoucher(t, dp, "GS-LOCAL", 1000) // known locally, but the main till can't be asked
	setReplicaSettings(t, dp.Settings, unreachableURL(), "b-replica")

	rec := vTopUp(t, mux, "GS-LOCAL", "5", &vManager, true)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), html.EscapeString(httpx.T("en", "vouchers.top_up.replica_note"))) {
		t.Fatalf("unreachable main till = %d %s, want 409 + the replica note", rec.Code, rec.Body.String())
	}
	if n := len(dp.Engine.Lines()); n != 0 {
		t.Fatalf("an unreachable main till still added %d line(s)", n)
	}
}

func TestVoucherTopUp_ReplicaMainTillWithoutAcceptsTopUpRefuses(t *testing.T) {
	mux, dp := newTopUpTestDeps(t)
	// An older main till: a valid, active voucher row, no accepts_top_up.
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":"GS-OLD","holder_label":"","original_amount":1000,"balance":1000,"currency":"GBP","voucher_type":"multi_purpose","status":"active","issued_sale_id":"","created_at":"2026-10-01T10:00:00Z"},"error":null}`))
	}))
	t.Cleanup(old.Close)
	setReplicaSettings(t, dp.Settings, old.URL, "b-replica")

	rec := vTopUp(t, mux, "GS-OLD", "5", &vManager, true)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), html.EscapeString(httpx.T("en", "vouchers.top_up.replica_note"))) {
		t.Fatalf("older main till = %d %s, want 409 + the replica note", rec.Code, rec.Body.String())
	}
	if n := len(dp.Engine.Lines()) + localVoucherRows(t, dp, "GS-OLD"); n != 0 {
		t.Fatalf("an older main till still left %d line/mirror row(s)", n)
	}
}

// A replica's sale with a top-up completes even when its own mirror is
// stale or void: the main till vetted the top-up, the sale books it.
func TestVoucherTopUp_ReplicaSaleCompletesWithStaleOrVoidMirror(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		mirror       int64
		wantStatus   string
	}{
		{"stale balance", "active", 100, "active"},
		{"void mirror", "void", 0, "void"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, dp, _, primaryRepo := newReplicaTopUpDeps(t)
			ctx := context.Background()
			repo := data.NewPOSRepo(dp.Db)
			seedPrimaryVoucher(t, primaryRepo, "GS-S1", "", 2000)
			if err := repo.EnsureVoucherLocalRow(ctx, nil, data.Voucher{ID: "GS-S1", OriginalAmountMinor: 100, BalanceMinor: tc.mirror, Currency: "GBP", Status: tc.status, CreatedAt: "2026-10-01T10:00:00Z"}); err != nil {
				t.Fatal(err)
			}
			if rec := vTopUp(t, mux, "GS-S1", "25", &vManager, true); rec.Code != http.StatusOK {
				t.Fatalf("top-up: %d %s", rec.Code, rec.Body.String())
			}
			if rec := cashTender(mux); rec.Code != http.StatusOK {
				t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
			}
			local := vBalance(t, repo, "GS-S1")
			if local.BalanceMinor != tc.mirror+2500 || local.Status != tc.wantStatus {
				t.Fatalf("local mirror after the sale = %+v, want balance %d, status %s", local, tc.mirror+2500, tc.wantStatus)
			}
			// The main till is credited only by the journal replay.
			if v := vBalance(t, primaryRepo, "GS-S1"); v.BalanceMinor != 2000 {
				t.Fatalf("main till balance = %d before replay", v.BalanceMinor)
			}
		})
	}
}

// End to end: the replica's top-up sale, journaled and applied on the main
// till, raises the balance exactly once; a replay of the same batch is a
// no-op.
func TestVoucherTopUp_ReplicaJournalCreditsMainTillExactlyOnce(t *testing.T) {
	mux, dp, primaryDp, primaryRepo := newReplicaTopUpDeps(t)
	ctx := context.Background()
	seedPrimaryVoucher(t, primaryRepo, "GS-E1", "", 2000)
	if rec := vTopUp(t, mux, "GS-E1", "25", &vManager, true); rec.Code != http.StatusOK {
		t.Fatalf("top-up: %d %s", rec.Code, rec.Body.String())
	}
	if rec := cashTender(mux); rec.Code != http.StatusOK {
		t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
	}
	var receiptNo string
	if err := dp.Db.QueryRow(`SELECT receipt_no FROM sales WHERE id IN (SELECT sale_id FROM voucher_transactions WHERE type = 'top_up')`).Scan(&receiptNo); err != nil {
		t.Fatalf("top-up sale: %v", err)
	}
	j, found, err := buildJournal(ctx, data.NewPOSRepo(dp.Db), receiptNo)
	if err != nil || !found {
		t.Fatalf("buildJournal: %v %v", found, err)
	}

	for i := 0; i < 2; i++ {
		applied, reason, err := applyJournal(ctx, primaryDp, "till-2", j)
		if err != nil || reason != "" {
			t.Fatalf("apply %d: applied=%v reason=%q err=%v", i, applied, reason, err)
		}
		if applied != (i == 0) {
			t.Fatalf("apply %d: applied=%v", i, applied)
		}
		if v := vBalance(t, primaryRepo, "GS-E1"); v.BalanceMinor != 4500 {
			t.Fatalf("apply %d: main till balance = %d, want 4500 (credited exactly once)", i, v.BalanceMinor)
		}
	}
	if n := countRows(t, primaryDp, `SELECT COUNT(*) FROM voucher_transactions WHERE voucher_id = 'GS-E1' AND type = 'top_up'`); n != 1 {
		t.Fatalf("main till top_up ledger rows = %d, want 1", n)
	}
}

// topUpJournalFor builds the journal of a replica's top-up sale for
// voucherID (seeded on the replica only), via a direct forced CompleteSale.
func topUpJournalFor(t *testing.T, voucherID string, amount int64) journalSale {
	t.Helper()
	_, replicaDp := newSyncSalesTestDeps(t)
	topUpSeedVoucher(t, replicaDp, voucherID, 1000)
	if _, err := pos.CompleteSale(context.Background(), replicaDp.Db, pos.SaleInput{
		SaleType: "sale", SaleID: "sale-" + voucherID, ReceiptNo: "T2-" + voucherID, Currency: "GBP", TaxInclusive: true, CashierID: "user1",
		ForceVoucherTopUps: true,
		VoucherIssues:      []pos.VoucherIssueInput{{VoucherID: voucherID, Amount: money.FromMinor(amount), TopUp: true}},
		Payments:           []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(amount)}},
	}); err != nil {
		t.Fatalf("replica sale: %v", err)
	}
	j, found, err := buildJournal(context.Background(), data.NewPOSRepo(replicaDp.Db), "T2-"+voucherID)
	if err != nil || !found {
		t.Fatalf("buildJournal: %v %v", found, err)
	}
	return j
}

// The voucher was voided on the main till after the top-up was rung: the
// replay applies (credit booked, voucher stays void) and does not 422 the
// batch; a Problem names the voucher.
func TestApplyJournal_TopUpOfVoidedVoucherIsAppliedAndWarned(t *testing.T) {
	ctx := context.Background()
	_, primaryDp := newSyncSalesTestDeps(t)
	primaryRepo := data.NewPOSRepo(primaryDp.Db)
	topUpSeedVoucher(t, primaryDp, "GS-VD", 1000)
	if _, err := primaryDp.Db.Exec(`UPDATE vouchers SET status = 'void', balance = 0 WHERE id = 'GS-VD'`); err != nil {
		t.Fatal(err)
	}
	j := topUpJournalFor(t, "GS-VD", 700)

	applied, reason, err := applyJournal(ctx, primaryDp, "till-2", j)
	if err != nil || !applied || reason != "" {
		t.Fatalf("applyJournal: applied=%v reason=%q err=%v, want applied", applied, reason, err)
	}
	if v := vBalance(t, primaryRepo, "GS-VD"); v.BalanceMinor != 700 || v.Status != "void" {
		t.Fatalf("voucher after replay = %+v, want credited 700 and still void", v)
	}
	found := false
	for _, p := range logging.Recent() {
		if strings.Contains(fmt.Sprint(p), "GS-VD") && strings.Contains(fmt.Sprint(p), "refund the customer or reissue") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no Problem names the void voucher GS-VD: %+v", logging.Recent())
	}
}

// Over the ceiling on the main till by the time the journal arrives: applied
// and flagged, never refused.
func TestApplyJournal_TopUpOverCeilingIsAppliedAndWarned(t *testing.T) {
	ctx := context.Background()
	_, primaryDp := newSyncSalesTestDeps(t)
	topUpSeedVoucher(t, primaryDp, "GS-CE", 1000)
	if _, err := primaryDp.Db.Exec(`UPDATE vouchers SET balance = ? WHERE id = 'GS-CE'`, pos.MaxVoucherIssueAmount.Minor()-100); err != nil {
		t.Fatal(err)
	}
	j := topUpJournalFor(t, "GS-CE", 700)
	if applied, reason, err := applyJournal(ctx, primaryDp, "till-2", j); err != nil || !applied || reason != "" {
		t.Fatalf("applyJournal: applied=%v reason=%q err=%v", applied, reason, err)
	}
	if v := vBalance(t, data.NewPOSRepo(primaryDp.Db), "GS-CE"); v.BalanceMinor != pos.MaxVoucherIssueAmount.Minor()+600 {
		t.Fatalf("balance = %d", v.BalanceMinor)
	}
	found := false
	for _, p := range logging.Recent() {
		if strings.Contains(fmt.Sprint(p), "GS-CE") && strings.Contains(fmt.Sprint(p), "above the maximum") {
			found = true
		}
	}
	if !found {
		t.Fatal("no Problem names the over-limit voucher GS-CE")
	}
}

func TestApplyJournal_TopUpReplayQuarantines(t *testing.T) {
	ctx := context.Background()
	t.Run("unknown voucher", func(t *testing.T) {
		_, primaryDp := newSyncSalesTestDeps(t) // never saw GS-UK
		j := topUpJournalFor(t, "GS-UK", 700)
		applied, reason, err := applyJournal(ctx, primaryDp, "till-2", j)
		if err != nil || applied || reason != "unknown voucher on top-up replay" {
			t.Fatalf("applyJournal: applied=%v reason=%q err=%v, want quarantine \"unknown voucher on top-up replay\"", applied, reason, err)
		}
		entries, err := data.NewPOSRepo(primaryDp.Db).ListJournalQuarantine(ctx, 10)
		if err != nil || len(entries) != 1 || entries[0].Reason != "unknown voucher on top-up replay" {
			t.Fatalf("quarantine = %+v (%v)", entries, err)
		}
	})
	t.Run("single purpose", func(t *testing.T) {
		_, primaryDp := newSyncSalesTestDeps(t)
		if err := data.NewPOSRepo(primaryDp.Db).CreateVoucher(ctx, nil, data.Voucher{ID: "GS-SPQ", OriginalAmountMinor: 1000, BalanceMinor: 1000, Currency: "GBP", Purpose: data.VoucherPurposeSingle, CreatedAt: "2026-10-01T10:00:00Z"}); err != nil {
			t.Fatal(err)
		}
		j := topUpJournalFor(t, "GS-SPQ", 700)
		applied, reason, err := applyJournal(ctx, primaryDp, "till-2", j)
		if err != nil || applied || reason != "single-purpose voucher on top-up replay" {
			t.Fatalf("applyJournal: applied=%v reason=%q err=%v, want quarantine \"single-purpose voucher on top-up replay\"", applied, reason, err)
		}
		if v := vBalance(t, data.NewPOSRepo(primaryDp.Db), "GS-SPQ"); v.BalanceMinor != 1000 {
			t.Fatalf("a quarantined top-up moved the balance to %d", v.BalanceMinor)
		}
	})
	t.Run("plain redemption keeps its own reason", func(t *testing.T) {
		if got := permanentJournalFailureReason(fmt.Errorf("debit: %w", data.ErrVoucherNotFound)); got != "unknown voucher on redemption replay" {
			t.Fatalf("redemption reason = %q", got)
		}
	})
}

// ut-docs#4416 review M1: a voucher sold on the main till, never seen on this
// additional till, must still be findable and offer the Top up form here —
// the list search and the detail page fall back to the main till. A void on
// the main till hides the form even when this till's mirror is stale-active.
func TestVoucherPages_ReplicaFallsBackToMainTill(t *testing.T) {
	mux, dp, primaryDp, primaryRepo := newReplicaTopUpDeps(t)
	seedPrimaryVoucher(t, primaryRepo, "GS-FAR", "Far Away", 1500)

	list := vGet(t, mux, "/ui/vouchers/list?q=GS-FAR&lang=en", &vManager, true).Body.String()
	if !strings.Contains(list, `href="/vouchers/GS-FAR"`) {
		t.Fatalf("a search for a main-till-only voucher must list it from the main till:\n%s", list)
	}
	if none := vGet(t, mux, "/ui/vouchers/list?q=GS-NOWHERE&lang=en", &vManager, true).Body.String(); strings.Contains(none, `href="/vouchers/GS-NOWHERE"`) {
		t.Fatal("an unknown number must not be listed")
	}
	rec := vGet(t, mux, "/vouchers/GS-FAR?lang=en", &vManager, false)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `hx-post="/vouchers/GS-FAR/top-up"`) {
		t.Fatalf("detail of a main-till-only voucher = %d, want 200 with the Top up form", rec.Code)
	}
	if n := localVoucherRows(t, dp, "GS-FAR"); n != 0 {
		t.Fatalf("viewing must not write a local row (got %d)", n)
	}

	// Stale-active local mirror, voided on the main till: no form.
	if err := data.NewPOSRepo(dp.Db).EnsureVoucherLocalRow(context.Background(), nil, data.Voucher{ID: "GS-FAR", OriginalAmountMinor: 1500, BalanceMinor: 1500, Currency: "GBP", Purpose: data.VoucherPurposeMulti, Status: "active", CreatedAt: "2026-10-01T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := primaryDp.Db.Exec(`UPDATE vouchers SET status = 'void', balance = 0 WHERE id = 'GS-FAR'`); err != nil {
		t.Fatal(err)
	}
	if page := vGet(t, mux, "/vouchers/GS-FAR?lang=en", &vManager, false).Body.String(); strings.Contains(page, `hx-post="/vouchers/GS-FAR/top-up"`) {
		t.Fatal("a voucher voided on the main till must not offer the Top up form")
	}
}

// Voiding on an additional till (ut-docs#5133). The main till holds a
// voucher issue / top-up's value through the sales journal and never learns
// of a local void, and the replica cannot tell whether the sale reached it,
// so the void of any voucher-issuing/topping-up sale is refused (409) with
// nothing written.

func voidSaleReq(t *testing.T, mux *http.ServeMux, saleID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pos/sale/status?lang=en",
		strings.NewReader(fmt.Sprintf(`{"saleId":%q,"status":"voided"}`, saleID)))
	req.Header.Set("Content-Type", "application/json")
	req = auth.WithUser(req, vManager)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func saleStatus(t *testing.T, dp *common.Deps, saleID string) string {
	t.Helper()
	var st string
	if err := dp.Db.QueryRow(`SELECT status FROM sales WHERE id = ?`, saleID).Scan(&st); err != nil {
		t.Fatalf("sale %s: %v", saleID, err)
	}
	return st
}

func TestVoidSale_ReplicaRefusesTopUpSale(t *testing.T) {
	mux, dp, primaryDp, primaryRepo := newReplicaTopUpDeps(t)
	seedPrimaryVoucher(t, primaryRepo, "GS-V1", "", 2000)
	if rec := vTopUp(t, mux, "GS-V1", "25", &vManager, true); rec.Code != http.StatusOK {
		t.Fatalf("top-up: %d %s", rec.Code, rec.Body.String())
	}
	if rec := cashTender(mux); rec.Code != http.StatusOK {
		t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
	}
	var saleID string
	if err := dp.Db.QueryRow(`SELECT sale_id FROM voucher_transactions WHERE type = 'top_up'`).Scan(&saleID); err != nil {
		t.Fatalf("top-up sale: %v", err)
	}
	mirrorBefore := vBalance(t, data.NewPOSRepo(dp.Db), "GS-V1").BalanceMinor

	rec := voidSaleReq(t, mux, saleID)
	if rec.Code != http.StatusConflict {
		t.Fatalf("void = %d (%s), want 409", rec.Code, rec.Body.String())
	}
	if !strings.Contains(html.UnescapeString(rec.Body.String()), httpx.T("en", "pos.error.void_voucher_additional_till")) {
		t.Fatalf("body %q lacks the pos.error.void_voucher_additional_till text", rec.Body.String())
	}
	if st := saleStatus(t, dp, saleID); st != "completed" {
		t.Fatalf("refused void left the sale %q", st)
	}
	if got := vBalance(t, data.NewPOSRepo(dp.Db), "GS-V1").BalanceMinor; got != mirrorBefore {
		t.Fatalf("mirror balance %d -> %d", mirrorBefore, got)
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM voucher_transactions WHERE id LIKE 'topup-void:%'`); n != 0 {
		t.Fatalf("%d topup-void row(s) written", n)
	}
	if got := vBalance(t, primaryRepo, "GS-V1").BalanceMinor; got != 2000 {
		t.Fatalf("main till balance moved to %d", got)
	}
	_ = primaryDp
}

func TestVoidSale_ReplicaRefusesIssueSale(t *testing.T) {
	mux, dp, _, _ := newReplicaTopUpDeps(t)
	topUpSeedVoucher(t, dp, "GS-V2", 1500) // issues the voucher in a replica sale
	var saleID string
	if err := dp.Db.QueryRow(`SELECT sale_id FROM voucher_transactions WHERE type = 'issue'`).Scan(&saleID); err != nil {
		t.Fatalf("issue sale: %v", err)
	}
	rec := voidSaleReq(t, mux, saleID)
	if rec.Code != http.StatusConflict || !strings.Contains(html.UnescapeString(rec.Body.String()), httpx.T("en", "pos.error.void_voucher_additional_till")) {
		t.Fatalf("void = %d (%s), want 409 with the new copy", rec.Code, rec.Body.String())
	}
	if st := saleStatus(t, dp, saleID); st != "completed" {
		t.Fatalf("refused void left the sale %q", st)
	}
	if v := vBalance(t, data.NewPOSRepo(dp.Db), "GS-V2"); v.Status != "active" || v.BalanceMinor != 1500 {
		t.Fatalf("voucher after refused void = %+v, want active/1500", v)
	}
}

// Control: a sale with no voucher on it voids on a replica as before.
func TestVoidSale_ReplicaVoidsPlainSale(t *testing.T) {
	mux, dp, _, _ := newReplicaTopUpDeps(t)
	saleID, err := pos.CompleteSale(context.Background(), dp.Db, pos.SaleInput{
		SaleType: "sale", Currency: "GBP", TaxInclusive: true, AllowNegativeInventory: true,
		Lines:    []pos.SaleLineInput{{ItemID: "itm1", Name: "Plain Item", Qty: 1, UnitPrice: money.FromMinor(500), TaxRateBasisPoints: 2000, LocationID: "loc_main"}},
		Payments: []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(500)}},
	})
	if err != nil {
		t.Fatalf("plain sale: %v", err)
	}
	if rec := voidSaleReq(t, mux, saleID); rec.Code != http.StatusNoContent {
		t.Fatalf("plain void = %d (%s), want 204", rec.Code, rec.Body.String())
	}
	if st := saleStatus(t, dp, saleID); st != "voided" {
		t.Fatalf("status %q, want voided", st)
	}
}
