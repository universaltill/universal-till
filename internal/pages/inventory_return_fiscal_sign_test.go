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

// ut-docs#999: fiscal.sign.ask (ADR-0044 Decision 1) is dispatched on the
// CreateReturn completion path (POST /api/inventory/return) the same way
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

// (i) approved return: no unsigned_fiscal_signing marker AND actually
// dispatched exactly once — marker-absence alone would pass identically if
// dispatch were never called at all, so this asserts the invocation count
// too, same pattern as TestFiscalSignAsk_NeverReDispatchesAfterTenderCompletes.
func TestReturnFiscalSignAsk_ApprovedHasNoMarker(t *testing.T) {
	mux, dp := newInventoryReturnTestDeps(t)
	var invocations atomic.Int32
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-ok", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		invocations.Add(1)
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
