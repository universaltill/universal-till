package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#999: fiscal.sign.ask (ADR-0044 Decision 1) is dispatched on the
// refund completion path exactly the way completeTender dispatches it for a
// sale — a refund is aufzeichnungspflichtig under KassenSichV the same as a
// sale, so a refund that's allowed through the ADR-0048 gate must still get
// a real signing attempt, not just the gate check. Same fixtures/helpers as
// fiscal_sign_hook_test.go's sale-path coverage (same package), driving
// POST /api/refund instead of POST /api/pos/tender.

// (i) A till whose installed signer answers "approved" completes the refund
// with no unsigned_fiscal_signing marker AND was actually dispatched exactly
// once — the marker-absence alone is a tautology (it's equally true if the
// dispatch call were never made at all), so this asserts the invocation
// count too, same pattern as TestFiscalSignAsk_NeverReDispatchesAfterTenderCompletes.
func TestRefundFiscalSignAsk_ApprovedHasNoMarker(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	t.Cleanup(func() { plugins.SharedBus(dp.Db).ResetSubscribers() })
	var invocations atomic.Int32
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-ok", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		invocations.Add(1)
		return json.RawMessage(`{"status":"approved"}`), nil
	})
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := invocations.Load(); n != 1 {
		t.Fatalf("fiscal.sign.ask must be dispatched exactly once for the refund, got %d invocations", n)
	}
	if n := countAuditRows(t, dp, "unsigned_fiscal_signing"); n != 0 {
		t.Fatalf("approved refund must carry no unsigned_fiscal_signing marker, got %d", n)
	}
}

// (ii) The signer declares its backend unreachable: the refund completes
// anyway (proceed-and-declare, ADR-0044/ADR-0056) and IS journaled unsigned
// — mirroring TestFiscalSignAsk_UnreachableDeclaredProceedsAndDeclares.
func TestRefundFiscalSignAsk_UnreachableDeclaredProceedsAndDeclares(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	t.Cleanup(func() { plugins.SharedBus(dp.Db).ResetSubscribers() })
	installFiscalSignWasmPlugin(t, dp, "com.test.fiscal-sign-down", "fiscalsign_unreachable_guest")
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("refund must complete despite the signing failure, got %d: %s", rec.Code, rec.Body.String())
	}

	var refundSaleID string
	if err := dp.Db.QueryRow(`SELECT id FROM sales WHERE sale_type = 'return'`).Scan(&refundSaleID); err != nil {
		t.Fatalf("expected a return sale row: %v", err)
	}
	var markerSaleID, markerPayload string
	if err := dp.Db.QueryRow(`SELECT entity_id, data_json FROM audit_log WHERE entity_type='sale' AND action='unsigned_fiscal_signing'`).
		Scan(&markerSaleID, &markerPayload); err != nil {
		t.Fatalf("expected a sale/unsigned_fiscal_signing audit row for the refund: %v", err)
	}
	if markerSaleID != refundSaleID {
		t.Fatalf("marker not attached to the refund's own sale row: %q != %q", markerSaleID, refundSaleID)
	}
	if !strings.Contains(markerPayload, "unreachable") {
		t.Fatalf("marker payload should carry the failure reason, got %s", markerPayload)
	}
	assertNoFiscalSignRetryQueue(t, dp)
}
