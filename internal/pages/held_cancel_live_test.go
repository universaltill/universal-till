package pages

import (
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#3582 (review): the order currently OPEN in this till's basket must
// never be cancelled from the list. Normally its held_sales row is already
// gone (resumeHeldSale claims it), but a stale row can survive a failed
// delete, and the popup / page then still offers it -- cancelling that row
// would drop the only durable copy while the live basket stays tenderable,
// so the handler refuses it untouched: no claim, no table release, no audit
// row, no fiscal.order.cancel, and the cashier is told to finish or re-hold
// the sale first. Both surfaces answer with the same refusal.
func TestHeldCancel_OrderOpenInBasketIsRefused(t *testing.T) {
	mux, dp := newCancelTestDeps(t)
	mgrID, _ := newElevationTestPrincipals(t, dp, "mgr-cancel-7", "cashier-cancel-7", "778899")
	seedCancelHeldOrder(t, dp, "hold-c7", "Table 4 order", "tbl-c7")
	// What a stale row looks like: the same order is live in the basket.
	dp.Engine.RestoreHeld(
		pos.BasketSnapshot{Lines: []pos.SnapshotLine{{SKU: "PLAIN", Name: "Plain Item", Qty: 2, PriceCents: 200, ItemID: "itm-plain2", TaxRateBP: 2000}}, TableID: "tbl-c7"},
		pos.HeldOrigin{ID: "hold-c7", Label: "Table 4 order", CreatedAt: "2026-10-03 10:00:00"},
	)

	t.Run("page", func(t *testing.T) {
		rec := postForm(mux, "/api/pos/held/cancel", url.Values{"id": {"hold-c7"}, "view": {"page"}, "tab": {"hold"}},
			&auth.User{ID: mgrID, Role: "manager"})
		if rec.Code != http.StatusOK {
			t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("HX-Redirect"); got != "/open-orders?tab=hold&err=open_orders.cancel.error.live" {
			t.Fatalf("HX-Redirect = %q, want the live-order refusal", got)
		}
	})
	t.Run("popup", func(t *testing.T) {
		rec := postForm(mux, "/api/pos/held/cancel", url.Values{"id": {"hold-c7"}, "view": {"parked-orders"}, "tab": {"hold"}},
			&auth.User{ID: mgrID, Role: "manager"})
		if rec.Code != http.StatusOK {
			t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
		}
		body := html.UnescapeString(rec.Body.String())
		if !strings.Contains(body, httpx.T("en", "open_orders.cancel.error.live")) {
			t.Fatalf("the popup must carry the live-order refusal: %s", body)
		}
		if strings.Contains(rec.Header().Get("HX-Trigger"), "held-changed") {
			t.Fatalf("nothing changed, so no held-changed trigger expected, got %q", rec.Header().Get("HX-Trigger"))
		}
	})

	if !heldSaleExists(t, dp, "hold-c7") {
		t.Fatal("the refused order's row must be left untouched")
	}
	if !holdTestTableClaimed(t, dp, "tbl-c7") {
		t.Fatal("the refused order's table claim must be kept")
	}
	if dp.Engine.HeldOrigin().ID != "hold-c7" || !dp.Engine.HasItems() {
		t.Fatal("the live basket must be left exactly as it was")
	}
	if n := len(cancelAuditRows(t, dp)); n != 0 {
		t.Fatalf("a refused cancel must not be audited as a cancel, got %d rows", n)
	}
}
