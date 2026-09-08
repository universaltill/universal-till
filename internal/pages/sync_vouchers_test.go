package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cross-till voucher endpoints, primary side. GET /api/sync/vouchers/{id}
// (ut-docs#1668) is the bearer-authed, read-only lookup a replica's
// fetchVoucherFromPrimary (voucher_sync_proxy.go) proxies to for a plain
// balance query. POST .../{id}/redeem and .../{id}/release (ADR-0084,
// ut-docs#1716) are the idempotent RESERVATION pair voucherRedeemWriteThrough
// now calls at tender time — the first draft's mutating endpoint was
// reverted (round-2 review, 2026-09-07) because nothing let the journal
// replay recognize a debit the write-through already applied; the
// (voucher_id, sale_id) idempotency key is what makes it safe now (see
// sync_vouchers.go's file-level comment). Same shape as sync_tables_test.go:
// syncTill auth, JSON envelope, snake_case.

func newSyncVouchersTestDeps(t *testing.T) (*http.ServeMux, *common.Deps, *db.DB) {
	t.Helper()
	dbase, err := db.Open(filepath.Join(t.TempDir(), "sync_vouchers.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbase.Close() })

	dp := &common.Deps{Db: dbase.DB}
	mux := http.NewServeMux()
	registerSyncVouchers(mux, dp)
	return mux, dp, dbase
}

func getSyncVoucher(mux *http.ServeMux, id, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/sync/vouchers/"+id, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

type syncVoucherGetResp struct {
	Data  *syncVoucherRow `json:"data"`
	Error any             `json:"error"`
}

func TestSyncVouchers_RequiresBearer(t *testing.T) {
	mux, dp, _ := newSyncVouchersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")

	if rec := getSyncVoucher(mux, "GS-1", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no bearer: status = %d, want 401", rec.Code)
	}
	if rec := getSyncVoucher(mux, "GS-1", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad bearer: status = %d, want 401", rec.Code)
	}
}

func TestSyncVouchers_GetUnknownIs404(t *testing.T) {
	mux, dp, _ := newSyncVouchersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")

	rec := getSyncVoucher(mux, "GS-NOPE", "bearer-t2")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
	}
}

func TestSyncVouchers_GetKnownVoucherReturnsBalance(t *testing.T) {
	mux, dp, dbase := newSyncVouchersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	repo := data.NewPOSRepo(dbase.DB)
	if err := repo.CreateVoucher(context.Background(), nil, data.Voucher{
		ID: "GS-1", HolderLabel: "Alice", OriginalAmountMinor: 2000, BalanceMinor: 1500,
		Currency: "EUR", CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed voucher: %v", err)
	}

	rec := getSyncVoucher(mux, "GS-1", "bearer-t2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var resp syncVoucherGetResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data == nil || resp.Data.ID != "GS-1" || resp.Data.Balance != 1500 || resp.Data.HolderLabel != "Alice" {
		t.Fatalf("voucher row = %+v", resp.Data)
	}
}

// A lookup must never mutate anything on the primary — it's a plain read,
// reused for both the balance-query API and the redemption pre-check.
func TestSyncVouchers_GetIsPurelyReadOnly(t *testing.T) {
	mux, dp, dbase := newSyncVouchersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	repo := data.NewPOSRepo(dbase.DB)
	if err := repo.CreateVoucher(context.Background(), nil, data.Voucher{
		ID: "GS-1", OriginalAmountMinor: 1000, BalanceMinor: 1000, Currency: "EUR",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed voucher: %v", err)
	}

	for i := 0; i < 3; i++ {
		if rec := getSyncVoucher(mux, "GS-1", "bearer-t2"); rec.Code != http.StatusOK {
			t.Fatalf("get #%d: status = %d", i, rec.Code)
		}
	}
	v, err := repo.GetVoucherBalance(context.Background(), nil, "GS-1")
	if err != nil || v.BalanceMinor != 1000 {
		t.Fatalf("balance after repeated lookups = %d (err %v), want unchanged 1000", v.BalanceMinor, err)
	}
	var txCount int
	if err := dbase.DB.QueryRow(`SELECT COUNT(*) FROM voucher_transactions WHERE voucher_id = 'GS-1'`).Scan(&txCount); err != nil {
		t.Fatal(err)
	}
	if txCount != 0 {
		t.Fatalf("a read-only lookup must never write a ledger row, got %d", txCount)
	}
}

// ---------------------------------------------------------------------------
// POST /api/sync/vouchers/{id}/redeem and /release (ADR-0084, ut-docs#1716).
// ---------------------------------------------------------------------------

func postSyncVoucher(mux *http.ServeMux, id, action, body, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/sync/vouchers/"+id+"/"+action, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func seedSyncVoucher(t *testing.T, repo *data.POSRepo, id string, balance int64) {
	t.Helper()
	if err := repo.CreateVoucher(context.Background(), nil, data.Voucher{
		ID: id, HolderLabel: "Alice", OriginalAmountMinor: balance, BalanceMinor: balance,
		Currency: "EUR", CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed voucher %s: %v", id, err)
	}
}

func TestSyncVouchers_RedeemAndReleaseRequireBearer(t *testing.T) {
	mux, dp, _ := newSyncVouchersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	for _, action := range []string{"redeem", "release"} {
		if rec := postSyncVoucher(mux, "GS-1", action, `{"sale_id":"s1","amount_minor":100}`, ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s with no bearer: status = %d, want 401", action, rec.Code)
		}
		if rec := postSyncVoucher(mux, "GS-1", action, `{"sale_id":"s1","amount_minor":100}`, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s with bad bearer: status = %d, want 401", action, rec.Code)
		}
	}
}

func TestSyncVouchers_RedeemValidatesBody(t *testing.T) {
	mux, dp, dbase := newSyncVouchersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	seedSyncVoucher(t, data.NewPOSRepo(dbase.DB), "GS-1", 1000)

	for name, body := range map[string]string{
		"not json":        `{`,
		"missing sale_id": `{"amount_minor":100}`,
		"empty sale_id":   `{"sale_id":"  ","amount_minor":100}`,
		"zero amount":     `{"sale_id":"s1","amount_minor":0}`,
		"negative amount": `{"sale_id":"s1","amount_minor":-5}`,
	} {
		if rec := postSyncVoucher(mux, "GS-1", "redeem", body, "bearer-t2"); rec.Code != http.StatusBadRequest {
			t.Fatalf("redeem %s: status = %d, want 400 (body %q)", name, rec.Code, rec.Body.String())
		}
	}
	if rec := postSyncVoucher(mux, "GS-1", "release", `{"sale_id":""}`, "bearer-t2"); rec.Code != http.StatusBadRequest {
		t.Fatalf("release with empty sale_id: status = %d, want 400", rec.Code)
	}
	// Nothing moved.
	v, err := data.NewPOSRepo(dbase.DB).GetVoucherBalance(context.Background(), nil, "GS-1")
	if err != nil || v.BalanceMinor != 1000 {
		t.Fatalf("balance after rejected bodies = %d (err %v), want 1000", v.BalanceMinor, err)
	}
}

// Happy path: the primary debits atomically, records the (voucher, sale)
// redemption row, and answers with the PRE-debit snapshot (brief addendum,
// correction 5) — the replica seeds its own local mirror from that number
// and then runs its own forced local debit against it.
func TestSyncVouchers_RedeemDebitsAndReturnsPreDebitSnapshot(t *testing.T) {
	mux, dp, dbase := newSyncVouchersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	repo := data.NewPOSRepo(dbase.DB)
	seedSyncVoucher(t, repo, "GS-1", 1500)

	rec := postSyncVoucher(mux, "GS-1", "redeem", `{"sale_id":"sale-A","amount_minor":300}`, "bearer-t2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var resp syncVoucherGetResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data == nil || resp.Data.ID != "GS-1" || resp.Data.Balance != 1500 || resp.Data.Status != "active" || resp.Data.HolderLabel != "Alice" {
		t.Fatalf("redeem response = %+v, want the PRE-debit snapshot (balance 1500, active)", resp.Data)
	}
	v, err := repo.GetVoucherBalance(context.Background(), nil, "GS-1")
	if err != nil || v.BalanceMinor != 1200 {
		t.Fatalf("primary balance after redeem = %d (err %v), want 1200", v.BalanceMinor, err)
	}
	if ok, _ := repo.VoucherRedemptionRecorded(context.Background(), nil, "GS-1", "sale-A"); !ok {
		t.Fatal("redeem must record the (GS-1, sale-A) redemption row — the idempotency key the journal replay will check")
	}
}

func TestSyncVouchers_RedeemRefusalsAre409WithStableReason(t *testing.T) {
	mux, dp, dbase := newSyncVouchersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	repo := data.NewPOSRepo(dbase.DB)
	seedSyncVoucher(t, repo, "GS-SHORT", 100)
	seedSyncVoucher(t, repo, "GS-VOID", 500)
	if _, err := dbase.DB.Exec(`UPDATE vouchers SET status = 'void', balance = 0 WHERE id = 'GS-VOID'`); err != nil {
		t.Fatal(err)
	}

	rec := postSyncVoucher(mux, "GS-SHORT", "redeem", `{"sale_id":"sale-A","amount_minor":300}`, "bearer-t2")
	if rec.Code != http.StatusConflict {
		t.Fatalf("insufficient balance: status = %d, want 409 (body %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"voucher_insufficient_balance"`) {
		t.Fatalf("insufficient balance body = %q, want the stable voucher_insufficient_balance reason", rec.Body.String())
	}
	rec = postSyncVoucher(mux, "GS-VOID", "redeem", `{"sale_id":"sale-B","amount_minor":1}`, "bearer-t2")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"error":"voucher_not_active"`) {
		t.Fatalf("void voucher: status = %d body = %q, want 409 voucher_not_active", rec.Code, rec.Body.String())
	}
	rec = postSyncVoucher(mux, "GS-NOPE", "redeem", `{"sale_id":"sale-C","amount_minor":1}`, "bearer-t2")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown voucher: status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
	}
	// A refusal commits nothing.
	if v, _ := repo.GetVoucherBalance(context.Background(), nil, "GS-SHORT"); v.BalanceMinor != 100 {
		t.Fatalf("GS-SHORT balance after refusal = %d, want 100", v.BalanceMinor)
	}
	var n int
	if err := dbase.DB.QueryRow(`SELECT COUNT(*) FROM voucher_transactions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("refused redeems wrote %d ledger rows, want 0", n)
	}
}

// The same (voucher, sale_id) redeemed twice — a replica retrying after a
// lost response — debits exactly once and answers identically both times.
func TestSyncVouchers_RedeemIsIdempotentPerSale(t *testing.T) {
	mux, dp, dbase := newSyncVouchersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	repo := data.NewPOSRepo(dbase.DB)
	seedSyncVoucher(t, repo, "GS-1", 1000)

	var bodies []string
	for i := 0; i < 2; i++ {
		rec := postSyncVoucher(mux, "GS-1", "redeem", `{"sale_id":"sale-A","amount_minor":400}`, "bearer-t2")
		if rec.Code != http.StatusOK {
			t.Fatalf("redeem #%d: status = %d (body %q)", i, rec.Code, rec.Body.String())
		}
		bodies = append(bodies, rec.Body.String())
	}
	if bodies[0] != bodies[1] {
		t.Fatalf("retry answered differently:\n first: %s\nsecond: %s", bodies[0], bodies[1])
	}
	v, _ := repo.GetVoucherBalance(context.Background(), nil, "GS-1")
	if v.BalanceMinor != 600 {
		t.Fatalf("balance after retried redeem = %d, want 600 (debited exactly once)", v.BalanceMinor)
	}
	var n int
	if err := dbase.DB.QueryRow(`SELECT COUNT(*) FROM voucher_transactions WHERE voucher_id = 'GS-1' AND type = 'redemption'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("redemption rows = %d, want 1", n)
	}
	// A DIFFERENT sale against the same voucher is a new redemption.
	if rec := postSyncVoucher(mux, "GS-1", "redeem", `{"sale_id":"sale-B","amount_minor":100}`, "bearer-t2"); rec.Code != http.StatusOK {
		t.Fatalf("second sale: status = %d", rec.Code)
	}
	if v, _ = repo.GetVoucherBalance(context.Background(), nil, "GS-1"); v.BalanceMinor != 500 {
		t.Fatalf("balance after a second sale's redeem = %d, want 500", v.BalanceMinor)
	}
}

func TestSyncVouchers_ReleaseCreditsBackAndNoOpsWhenNothingReserved(t *testing.T) {
	mux, dp, dbase := newSyncVouchersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	repo := data.NewPOSRepo(dbase.DB)
	seedSyncVoucher(t, repo, "GS-1", 1000)

	if rec := postSyncVoucher(mux, "GS-1", "redeem", `{"sale_id":"sale-A","amount_minor":400}`, "bearer-t2"); rec.Code != http.StatusOK {
		t.Fatalf("redeem: status = %d", rec.Code)
	}
	rec := postSyncVoucher(mux, "GS-1", "release", `{"sale_id":"sale-A"}`, "bearer-t2")
	if rec.Code != http.StatusOK {
		t.Fatalf("release: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	v, _ := repo.GetVoucherBalance(context.Background(), nil, "GS-1")
	if v.BalanceMinor != 1000 || v.Status != "active" {
		t.Fatalf("after release: %+v, want 1000/active", v)
	}
	if ok, _ := repo.VoucherRedemptionRecorded(context.Background(), nil, "GS-1", "sale-A"); ok {
		t.Fatal("release must drop the (GS-1, sale-A) redemption row")
	}

	// Nothing reserved (never was, or already released): 200, no change —
	// the replica fires release for a refused reservation too.
	for _, body := range []string{`{"sale_id":"sale-A"}`, `{"sale_id":"sale-never"}`} {
		if rec := postSyncVoucher(mux, "GS-1", "release", body, "bearer-t2"); rec.Code != http.StatusOK {
			t.Fatalf("no-op release %s: status = %d, want 200 (body %q)", body, rec.Code, rec.Body.String())
		}
	}
	if rec := postSyncVoucher(mux, "GS-UNKNOWN", "release", `{"sale_id":"sale-A"}`, "bearer-t2"); rec.Code != http.StatusOK {
		t.Fatalf("release of an unknown voucher: status = %d, want 200 (idempotent no-op)", rec.Code)
	}
	if v, _ = repo.GetVoucherBalance(context.Background(), nil, "GS-1"); v.BalanceMinor != 1000 {
		t.Fatalf("balance after no-op releases = %d, want 1000 (never credited twice)", v.BalanceMinor)
	}
}
