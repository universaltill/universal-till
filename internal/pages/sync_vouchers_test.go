package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cross-till voucher lookup, primary side (ut-docs#1668): GET
// /api/sync/vouchers/{id} is the bearer-authed, READ-ONLY endpoint a
// replica's fetchVoucherFromPrimary (voucher_sync_proxy.go) proxies to — for
// both a plain balance lookup AND the pre-debit validation check
// voucherRedeemWriteThrough runs before a local redemption. See
// sync_vouchers.go's own file-level comment for why there is deliberately no
// mutating redeem/debit endpoint here (round-2 review, 2026-09-07: the first
// draft's write-through debit double-applied every online cross-till
// redemption once the replica's own completed sale journaled the SAME debit
// up again). Same shape as sync_tables_test.go: syncTill auth, JSON
// envelope, snake_case.

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
