package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

// LAN sync (ADR-0011, D3): a replica journals its local sales to the
// primary, which replays them through the SAME engine (pos.CompleteSale)
// with the original ids/receipts so applying a journal batch twice is a
// no-op (idempotent by sale id) — the core guarantee that makes offline
// operation safe to reconcile later without double-counting stock or
// double-charging a customer.

func newSyncSalesTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", TaxRate: 20},
		Marketplace: config.MarketplaceConfig{
			EndpointURL: "http://localhost:8081",
		},
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
	}
	mux := http.NewServeMux()
	registerSyncSales(mux, dp)
	return mux, dp
}

func TestBuildJournal_RoundTripsASaleWithReturnLinkage(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)

	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES('sale-j1', 'R-J1', 'completed', 'sale', 'GBP', 100, 0, 20, 120, '2026-01-01T10:00:00Z', '2026-01-01T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('line-j1', 'sale-j1', 1, 'itm1', 'Apple', 'ABC', 1, 100, 2000, 20, 100, 120)`); err != nil {
		t.Fatal(err)
	}

	j, found, err := buildJournal(ctx, repo, "R-J1")
	if err != nil || !found {
		t.Fatalf("expected the sale journaled, got found=%v err=%v", found, err)
	}
	if j.Sale.ID != "sale-j1" || len(j.Sale.Lines) != 1 {
		t.Fatalf("unexpected journal payload: %+v", j)
	}
	if j.OriginalSaleID != "" {
		t.Fatalf("expected no original_sale_id for a plain sale, got %q", j.OriginalSaleID)
	}

	// A return sale must carry its original_sale_id.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES('return-j1', 'R-J2', 'completed', 'return', 'GBP', 50, 0, 10, 60, '2026-01-01T11:00:00Z', '2026-01-01T11:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_links(id, sale_id, original_sale_id, reason) VALUES('link-j1', 'return-j1', 'sale-j1', 'test')`); err != nil {
		t.Fatal(err)
	}
	j2, found, err := buildJournal(ctx, repo, "R-J2")
	if err != nil || !found {
		t.Fatal(err)
	}
	if j2.OriginalSaleID != "sale-j1" {
		t.Fatalf("expected original_sale_id=sale-j1 for the return, got %q", j2.OriginalSaleID)
	}

	// An unknown receipt: found=false, not an error.
	if _, found, err := buildJournal(ctx, repo, "NO-SUCH"); err != nil || found {
		t.Fatalf("expected found=false for an unknown receipt, got found=%v err=%v", found, err)
	}
}

func seedJournalSale(id, receipt, saleType, originalSaleID string, itemID string, qty float64, unitPrice int64) journalSale {
	return journalSale{
		OriginalSaleID: originalSaleID,
		Sale: data.SaleDetail{
			ID: id, ReceiptNo: receipt, Status: "completed", SaleType: saleType,
			Currency: "GBP", Subtotal: int64(float64(unitPrice) * qty), Total: int64(float64(unitPrice) * qty),
			CreatedAt: "2026-01-01T10:00:00Z", CashierID: "user1",
			Lines: []data.SaleDetailLine{
				{Name: "Apple", SKU: "ABC", ItemID: itemID, UnitPrice: unitPrice, Qty: qty, LineTotal: int64(float64(unitPrice) * qty)},
			},
			Payments: []data.SaleDetailPayment{
				{Method: "cash", Amount: int64(float64(unitPrice) * qty)},
			},
		},
	}
}

func TestApplyJournal_AppliesOnceThenIdempotent(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)

	j := seedJournalSale("remote-sale-1", "T2-R001", "sale", "", "itm1", 1, 100)
	applied, err := applyJournal(ctx, dp, "till-1", j)
	if err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("expected the journal to apply on first replay")
	}
	if exists, err := repo.SaleExists(ctx, "remote-sale-1"); err != nil || !exists {
		t.Fatalf("expected the sale to now exist locally, got exists=%v err=%v", exists, err)
	}

	// The sale is stamped with the originating till and its true creation
	// time (not "now") -- provenance for a journaled-in sale.
	var tillID string
	if err := dp.Db.QueryRowContext(ctx, `SELECT till_id FROM sales WHERE id = 'remote-sale-1'`).Scan(&tillID); err != nil {
		t.Fatal(err)
	}
	if tillID != "till-1" {
		t.Fatalf("expected till_id stamped as till-1, got %q", tillID)
	}

	// Replaying the SAME journal entry again must be a no-op, not a
	// duplicate sale or an error -- this is the whole idempotency
	// guarantee sync relies on to reconcile safely after a retry/replay.
	applied, err = applyJournal(ctx, dp, "till-1", j)
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Fatal("expected the second application of the same sale id to be skipped (already exists)")
	}
	var count int
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sales WHERE id = 'remote-sale-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one sale row after replaying twice, got %d", count)
	}
}

func TestSyncSalesAPI_RejectsUnauthorized(t *testing.T) {
	mux, _ := newSyncSalesTestDeps(t)
	req := httptest.NewRequest(http.MethodPost, "/api/sync/sales", bytes.NewReader([]byte(`[]`)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no bearer token, got %d", rec.Code)
	}
}

func TestSyncSalesAPI_RejectsOversizedBatch(t *testing.T) {
	mux, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	tillsRepo := data.NewTillsRepo(dp.Db)
	_, err := tillsRepo.InsertTill(ctx, "Replica 1", hashBearer("token-abc"))
	if err != nil {
		t.Fatal(err)
	}

	batch := make([]journalSale, 101)
	raw, _ := json.Marshal(batch)
	req := httptest.NewRequest(http.MethodPost, "/api/sync/sales", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer token-abc")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a batch over 100 entries, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSyncSalesAPI_AppliesBatchAndReportsAppliedSkipped(t *testing.T) {
	mux, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	tillsRepo := data.NewTillsRepo(dp.Db)
	_, err := tillsRepo.InsertTill(ctx, "Replica 1", hashBearer("token-abc"))
	if err != nil {
		t.Fatal(err)
	}
	j1 := seedJournalSale("remote-sale-a", "T2-R010", "sale", "", "itm1", 1, 100)
	batch := []journalSale{j1}
	raw, _ := json.Marshal(batch)

	req := httptest.NewRequest(http.MethodPost, "/api/sync/sales", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer token-abc")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			Applied int `json:"applied"`
			Skipped int `json:"skipped"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Applied != 1 || resp.Data.Skipped != 0 {
		t.Fatalf("expected applied=1 skipped=0, got %+v", resp.Data)
	}

	// Re-submitting the SAME batch (a retried push after a dropped
	// response, for instance) must report it as skipped, not applied
	// again or erroring.
	req = httptest.NewRequest(http.MethodPost, "/api/sync/sales", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer token-abc")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on retry, got %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Applied != 0 || resp.Data.Skipped != 1 {
		t.Fatalf("expected a retried batch to be fully skipped, got %+v", resp.Data)
	}
}

// --- syncPushTick ---

// StartSyncPush must register its goroutine on the caller's wg (ut-docs#153)
// so app.Run's shutdown drain can prove it actually exited before
// database.Close() runs, the same join shape as cloudsync.Start. A cancelled
// ctx must let the goroutine finish and call wg.Done() promptly, not leave a
// leaked goroutine that only stops on the next 30s tick.
func TestStartSyncPush_JoinsWaitGroupAndExitsOnCtxCancel(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	StartSyncPush(ctx, dp, &wg)
	cancel()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StartSyncPush's goroutine did not call wg.Done() within 2s of ctx cancel — not joined to the shutdown drain")
	}
}

func TestSyncPushTick_NoPrimaryConfigured_NoOp(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	client := &http.Client{Timeout: 1 * time.Second}
	syncPushTick(ctx, dp, client)
	if v, _, _ := dp.Settings.Get(ctx, "sync.push_cursor"); v != "" {
		t.Fatalf("expected sync.push_cursor untouched with no primary configured, got %q", v)
	}
}

func TestSyncPushTick_NoLocalSales_NoOp(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, "sync.bearer", "token-abc"); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 1 * time.Second}
	syncPushTick(ctx, dp, client)
	if v, _, _ := dp.Settings.Get(ctx, "sync.last_push_at"); v != "" {
		t.Fatalf("expected no push recorded with no local sales queued, got last_push_at=%q", v)
	}
}

func seedLocalSale(t *testing.T, db *common.Deps, id, receipt, createdAt string) {
	t.Helper()
	if _, err := db.Db.ExecContext(context.Background(), `
INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES(?, ?, 'completed', 'sale', 'GBP', 100, 0, 20, 120, ?, ?)`, id, receipt, createdAt, createdAt); err != nil {
		t.Fatalf("seed local sale: %v", err)
	}
	if _, err := db.Db.ExecContext(context.Background(), `
INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES(?, ?, 1, 'itm1', 'Apple', 'ABC', 1, 100, 2000, 20, 100, 120)`, id+"-line", id); err != nil {
		t.Fatalf("seed local sale line: %v", err)
	}
	if _, err := db.Db.ExecContext(context.Background(), `
INSERT INTO payments(id, sale_id, method_id, amount, currency, paid_at) VALUES(?, ?, 'cash', 120, 'GBP', ?)`,
		id+"-pay", id, createdAt); err != nil {
		t.Fatalf("seed local sale payment: %v", err)
	}
}

func TestSyncPushTick_PushesLocalSalesAndAdvancesCursor(t *testing.T) {
	primaryMux, primaryDp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	if _, err := data.NewTillsRepo(primaryDp.Db).InsertTill(ctx, "Replica 1", hashBearer("token-abc")); err != nil {
		t.Fatalf("enrol till: %v", err)
	}
	server := httptest.NewServer(primaryMux)
	t.Cleanup(server.Close)

	_, replicaDp := newSyncSalesTestDeps(t)
	seedLocalSale(t, replicaDp, "push-sale-1", "R-PUSH-1", "2026-01-01T10:00:00Z")
	if err := replicaDp.Settings.Set(ctx, "sync.primary_url", server.URL); err != nil {
		t.Fatal(err)
	}
	if err := replicaDp.Settings.Set(ctx, "sync.bearer", "token-abc"); err != nil {
		t.Fatal(err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	syncPushTick(ctx, replicaDp, client)

	var count int
	if err := primaryDp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sales WHERE id = 'push-sale-1'`).Scan(&count); err != nil {
		t.Fatalf("query primary sales: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected the local sale pushed to the primary, got count=%d", count)
	}
	cursor, _, _ := replicaDp.Settings.Get(ctx, "sync.push_cursor")
	if cursor != "2026-01-01T10:00:00Z" {
		t.Fatalf("expected sync.push_cursor advanced to the pushed sale's created_at, got %q", cursor)
	}
	if v, _, _ := replicaDp.Settings.Get(ctx, "sync.last_push_at"); v == "" {
		t.Fatalf("expected sync.last_push_at set")
	}
	if v, _, _ := replicaDp.Settings.Get(ctx, "sync.last_contact_at"); v == "" {
		t.Fatalf("expected sync.last_contact_at set")
	}
}

// TestSyncPushTick_RejectedResponse_CursorNotAdvanced guards the offline-first
// retry guarantee: if the primary rejects a push (auth failure, transient
// error, etc.), the cursor must NOT move -- otherwise the next tick would
// silently skip re-sending sales that were never actually accepted.
func TestSyncPushTick_RejectedResponse_CursorNotAdvanced(t *testing.T) {
	primaryMux, primaryDp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	if _, err := data.NewTillsRepo(primaryDp.Db).InsertTill(ctx, "Replica 1", hashBearer("token-abc")); err != nil {
		t.Fatalf("enrol till: %v", err)
	}
	server := httptest.NewServer(primaryMux)
	t.Cleanup(server.Close)

	_, replicaDp := newSyncSalesTestDeps(t)
	seedLocalSale(t, replicaDp, "push-sale-2", "R-PUSH-2", "2026-01-01T10:00:00Z")
	if err := replicaDp.Settings.Set(ctx, "sync.primary_url", server.URL); err != nil {
		t.Fatal(err)
	}
	// Wrong bearer -- the primary will reject with 401.
	if err := replicaDp.Settings.Set(ctx, "sync.bearer", "wrong-token"); err != nil {
		t.Fatal(err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	syncPushTick(ctx, replicaDp, client)

	var count int
	if err := primaryDp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sales WHERE id = 'push-sale-2'`).Scan(&count); err != nil {
		t.Fatalf("query primary sales: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected the rejected sale NOT applied on the primary, got count=%d", count)
	}
	if v, _, _ := replicaDp.Settings.Get(ctx, "sync.push_cursor"); v != "" {
		t.Fatalf("expected sync.push_cursor to stay at the start so the next tick retries, got %q", v)
	}
}
