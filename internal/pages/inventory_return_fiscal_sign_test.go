package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#999/#1405: fiscal.sign.ask (ADR-0044 Decision 1) is dispatched on
// the CreateReturn completion path (POST /api/inventory/return) the same way
// completeTender dispatches it for a sale — same reasoning as
// refund_fiscal_sign_test.go's coverage of the /api/refund path, applied to
// this second, independent return entry point.

func newInventoryReturnTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	setStore := settings.NewStore(db)
	state := common.LoadState(t.Context(), setStore, cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: setStore,
	}
	t.Cleanup(func() { plugins.SharedBus(db).ResetSubscribers() })
	t.Cleanup(dp.WaitForAsyncWork)
	mux := http.NewServeMux()
	registerInventoryAPI(mux, dp)
	return mux, dp
}

func postInventoryReturn(t *testing.T, mux *http.ServeMux, receiptNo, lineID string, qty float64) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"receipt_no":%q,"lines":[{"line_id":%q,"quantity":%v}]}`, receiptNo, lineID, qty)
	req := httptest.NewRequest(http.MethodPost, "/api/inventory/return", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// (i) approved return: no unsigned_fiscal_signing marker, actually
// dispatched exactly once — marker-absence alone would pass identically if
// dispatch were never called at all, so this asserts the invocation count
// too, same pattern as TestFiscalSignAsk_NeverReDispatchesAfterTenderCompletes
// — AND the payload the signer received carries sale_type "return" (the
// gap universal-till PR #594's review blocked on before ut-docs#1203 closed
// it; see refund_fiscal_sign_test.go's matching assertion).
func TestReturnFiscalSignAsk_ApprovedHasNoMarker(t *testing.T) {
	mux, dp := newInventoryReturnTestDeps(t)
	var invocations atomic.Int32
	var gotSaleType atomic.Value
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-ok", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		invocations.Add(1)
		var payload struct {
			SaleType string `json:"sale_type"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err == nil {
			gotSaleType.Store(payload.SaleType)
		}
		return json.RawMessage(`{"status":"approved"}`), nil
	})
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	rec := postInventoryReturn(t, mux, receiptNo, "line-refund-1", 2)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := invocations.Load(); n != 1 {
		t.Fatalf("fiscal.sign.ask must be dispatched exactly once for the return, got %d invocations", n)
	}
	if n := countAuditRows(t, dp, "unsigned_fiscal_signing"); n != 0 {
		t.Fatalf("approved return must carry no unsigned_fiscal_signing marker, got %d", n)
	}
	if got, _ := gotSaleType.Load().(string); got != "return" {
		t.Fatalf("signer must see sale_type %q for a return (ut-docs#1203/#1405), got %q — a signer cannot tell this apart from a sale of the same amount otherwise", "return", got)
	}
}

// (iii) Regression for the rounding mismatch an independent review of this
// change found (docs/code-reviews/2026-09-03-fiscal-sign-refund-return-dispatch-1405.md):
// CreateReturn used to compute its own total with a truncating int64
// conversion + integer tax division, which disagrees with pos.CompleteSale's
// half-up-rounded pos.ComputeTaxBasisPoints by one minor unit whenever a
// line's exclusive tax has a fractional part >= 0.5 (an entirely ordinary
// price, e.g. 99p @ 20% VAT: 99*0.2=19.8, truncated=19, half-up=20). Before
// the fix, fiscal.sign.ask fired BEFORE that mismatch surfaced, so a signer
// was asked to sign a return that CompleteSale then rejected with "payments
// do not cover total" — an orphan, irreversible TSE record for a return
// that never persisted. This asserts the return actually succeeds AND is
// signed exactly once AND exactly one return sale row exists (not zero, not
// signed-but-orphaned).
func TestReturnFiscalSignAsk_RoundingMatchesCompleteSale(t *testing.T) {
	mux, dp := newInventoryReturnTestDeps(t)
	var invocations atomic.Int32
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-round", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		invocations.Add(1)
		return json.RawMessage(`{"status":"approved"}`), nil
	})
	_, receiptNo := seedCompletedSaleForRoundingReturn(t, dp)

	rec := postInventoryReturn(t, mux, receiptNo, "line-round-1", 1)
	if rec.Code != http.StatusOK {
		t.Fatalf("return must succeed on an ordinary 99p @ 20%% VAT line, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := invocations.Load(); n != 1 {
		t.Fatalf("fiscal.sign.ask must be dispatched exactly once, got %d invocations", n)
	}
	var returnRows int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales WHERE sale_type = 'return'`).Scan(&returnRows); err != nil {
		t.Fatal(err)
	}
	if returnRows != 1 {
		t.Fatalf("expected exactly 1 persisted return sale row (a signed-but-unpersisted return is an orphan TSE record), got %d", returnRows)
	}
}

// seedCompletedSaleForRoundingReturn seeds a completed sale with one 99p
// (unit_price 99 minor units), qty-1, 20%-VAT (2000bp) line — the smallest
// fixture that reproduces the truncating-vs-half-up rounding disagreement
// TestReturnFiscalSignAsk_RoundingMatchesCompleteSale guards against.
func seedCompletedSaleForRoundingReturn(t *testing.T, dp *common.Deps) (saleID, receiptNo string) {
	t.Helper()
	ctx := context.Background()
	saleID, receiptNo = "sale-return-round-1", "R-ROUND-1"
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES(?, ?, 'completed', 'sale', 'GBP', 99, 0, 20, 119, datetime('now'), datetime('now'))`, saleID, receiptNo); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('line-round-1', ?, 1, 'itm1', 'Apple', 'ABC', 1, 99, 2000, 20, 99, 119)`, saleID); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES('pay-return-round-1', ?, 'cash', 119, 'GBP', 0, datetime('now'))`, saleID); err != nil {
		t.Fatal(err)
	}
	return saleID, receiptNo
}

// (ii) signer unreachable: return still completes, IS journaled unsigned,
// nothing queued for retry (ADR-0056).
func TestReturnFiscalSignAsk_UnreachableDeclaredProceedsAndDeclares(t *testing.T) {
	mux, dp := newInventoryReturnTestDeps(t)
	installFiscalSignWasmPlugin(t, dp, "com.test.fiscal-sign-down", "fiscalsign_unreachable_guest")
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	rec := postInventoryReturn(t, mux, receiptNo, "line-refund-1", 2)
	if rec.Code != http.StatusOK {
		t.Fatalf("return must complete despite the signing failure, got %d: %s", rec.Code, rec.Body.String())
	}

	var returnSaleID string
	if err := dp.Db.QueryRowContext(context.Background(), `SELECT id FROM sales WHERE sale_type = 'return'`).Scan(&returnSaleID); err != nil {
		t.Fatalf("expected a return sale row: %v", err)
	}
	var markerSaleID, markerPayload string
	if err := dp.Db.QueryRow(`SELECT entity_id, data_json FROM audit_log WHERE entity_type='sale' AND action='unsigned_fiscal_signing'`).
		Scan(&markerSaleID, &markerPayload); err != nil {
		t.Fatalf("expected a sale/unsigned_fiscal_signing audit row for the return: %v", err)
	}
	if markerSaleID != returnSaleID {
		t.Fatalf("marker not attached to the return's own sale row: %q != %q", markerSaleID, returnSaleID)
	}
	if !strings.Contains(markerPayload, "unreachable") {
		t.Fatalf("marker payload should carry the failure reason, got %s", markerPayload)
	}
	assertNoFiscalSignRetryQueue(t, dp)
}
