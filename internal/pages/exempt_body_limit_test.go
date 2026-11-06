package pages

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
)

// Every body-size override in auth must name a real POST route, so a renamed
// or removed route cannot leave a stale (and silently looser) cap behind
// (ut-docs#5338).
func TestExemptBodyLimitOverridesAreRoutes(t *testing.T) {
	registered := map[string]bool{}
	for _, p := range registeredRoutePatterns(t) {
		registered[p] = true
	}
	paths := auth.ExemptBodyOverridePaths
	if len(paths) == 0 {
		t.Fatal("no overrides: the accessor has gone blind")
	}
	for _, p := range paths {
		if !registered["POST "+p] {
			t.Errorf("override %q has no registered POST route", p)
		}
	}
}

// A handler's own cap on an exempt route may be tighter than the middleware
// cap, never looser: otherwise the middleware would refuse a body the
// handler is documented to accept.
func TestHandlerCapsFitExemptBodyLimit(t *testing.T) {
	for _, c := range []struct {
		path string
		cap  int64
	}{
		{"/api/sync/catalog/apply", maxSyncCatalogBody},
		{"/api/sync/closes", salesMirrorClosesMaxBody},
		{"/api/sync/settings/apply", maxSyncSettingsBody},
		{"/api/sync/users/apply", maxSyncUserBody},
		{"/csp-report", cspReportMaxBody},
	} {
		if limit := auth.ExemptBodyLimit(c.path); c.cap > limit {
			t.Errorf("%s: handler cap %d exceeds middleware limit %d", c.path, c.cap, limit)
		}
	}
}

// AC9 wedge guard: a replica that is refused a /api/sync/sales batch never
// advances its cursor, so a legitimate batch (sender: 50 per tick, receiver
// accepts 100) must never reach the cap. Worst realistic sale: 200 lines
// with long names and five modifiers, six payments, a card-present payment's
// reconciliation fields, charges and voucher issues. Needs 2x headroom.
func TestMaxSyncSalesBatchFitsExemptLimit(t *testing.T) {
	long := func(n int) string { return strings.Repeat("x", n) }
	id := "01234567-89ab-cdef-0123-456789abcdef"
	var s data.SaleDetail
	s.ID, s.ReceiptNo, s.DisplayNo, s.Status, s.SaleType = id, long(24), long(12), "completed", "sale"
	s.TenderType, s.OrderType, s.TableID, s.TableLabel = "card", "dine_in", id, long(40)
	s.SyncStatus, s.Currency, s.CreatedAt, s.CashierID, s.RegisterID = "pending", "GBP", "2026-11-04T10:00:00Z", id, id // fixed-date:ok body-size fixture, never clock-compared
	for i := 0; i < 200; i++ {
		s.Lines = append(s.Lines, data.SaleDetailLine{
			Name: long(200), SKU: long(64), ItemID: id, VariantID: id,
			TaxRateBP: 2000, Qty: 1234.5678, UnitPrice: 123456789, LineDiscount: 123456789,
			TaxAmount: 123456789, LineTotal: 123456789,
			Modifiers: []string{long(80), long(80), long(80), long(80), long(80)},
			OrderType: "takeaway",
		})
	}
	for i := 0; i < 6; i++ {
		s.Payments = append(s.Payments, data.SaleDetailPayment{
			Method: "card", Amount: 123456789, ChangeGiven: 1, TipAmount: 1, TipRecipient: "employee",
			Reference: long(64), MaskedPAN: "************1234", AuthCode: long(12), TerminalID: long(16),
			TraceID: long(36), VoucherID: id, PaidAt: "2026-11-04T10:00:00Z", // fixed-date:ok body-size fixture, never clock-compared
		})
	}
	for i := 0; i < 10; i++ {
		s.Charges = append(s.Charges, data.SaleCharge{})
		s.VoucherIssues = append(s.VoucherIssues, data.SaleDetailVoucherIssue{VoucherID: id, HolderLabel: long(80), Amount: 1, Purpose: "single_purpose"})
	}
	batch := make([]journalSale, 100)
	for i := range batch {
		batch[i] = journalSale{Sale: s, OriginalSaleID: id}
	}
	b, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	limit := auth.ExemptBodyLimit("/api/sync/sales")
	t.Logf("worst-case 100-sale batch = %d bytes (limit %d)", len(b), limit)
	if int64(len(b))*2 > limit {
		t.Fatalf("worst-case batch %d bytes needs 2x headroom under limit %d", len(b), limit)
	}
}
