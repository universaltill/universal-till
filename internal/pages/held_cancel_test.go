package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#3582: an explicit "Cancel order" for a held, table or
// pay-at-counter order -- gated on void_comp_waste (checkOrElevate, the same
// gate voiding a priced basket line uses), taken off the shop's authority
// with heldSaleClaimForResume, dropped rather than restored, its table claim
// released, audited as held_sale/cancel and announced as fiscal.order.cancel.

// newCancelTestDeps is newPOSMuxRealSession (real migrations, real AuthSvc,
// UT_AUTH on) with the hold routes mounted on the same mux and i18n loaded,
// so the popup / page fragments render real copy.
func newCancelTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := newPOSMuxRealSession(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
	registerHoldAPI(mux, dp)
	return mux, dp
}

// seedCancelHeldOrder parks an order straight into held_sales (one priced
// line, optionally on a table that also carries this till's live claim, the
// way a table pick + Hold leaves it -- ut-docs#1704).
func seedCancelHeldOrder(t *testing.T, dp *common.Deps, id, label, tableID string) {
	t.Helper()
	snap := pos.BasketSnapshot{
		Lines:     []pos.SnapshotLine{{SKU: "PLAIN", Name: "Plain Item", Qty: 2, PriceCents: 200, ItemID: "itm-plain2", TaxRateBP: 2000}},
		TableID:   tableID,
		DisplayNo: "A-7",
	}
	payload, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if tableID != "" {
		if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, created_at, updated_at) VALUES (?, 'Table 4', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, tableID); err != nil {
			t.Fatalf("seed table: %v", err)
		}
		if ok, err := data.NewPOSRepo(dp.Db).ClaimTable(context.Background(), tableID); err != nil || !ok {
			t.Fatalf("seed table claim: ok=%v err=%v", ok, err)
		}
	}
	if err := data.NewHeldSalesRepo(dp.Db).Upsert(context.Background(), data.HeldSale{
		ID: id, Label: label, TotalMinor: 480, LineCount: 1, Payload: string(payload), TableID: tableID,
		CreatedAt: "2026-10-03 10:00:00",
	}); err != nil {
		t.Fatalf("seed held sale: %v", err)
	}
}

func heldSaleExists(t *testing.T, dp *common.Deps, id string) bool {
	t.Helper()
	_, found, err := data.NewHeldSalesRepo(dp.Db).Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get held sale: %v", err)
	}
	return found
}

type cancelAuditRow struct {
	entityID string
	actor    sql.NullString
	blocked  sql.NullString
	payload  map[string]any
}

func cancelAuditRows(t *testing.T, dp *common.Deps) []cancelAuditRow {
	t.Helper()
	rows, err := dp.Db.Query(`SELECT entity_id, actor_id, blocked_actor_id, data_json FROM audit_log WHERE entity_type = 'held_sale' AND action = 'cancel'`)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	defer rows.Close()
	var out []cancelAuditRow
	for rows.Next() {
		var r cancelAuditRow
		var raw string
		if err := rows.Scan(&r.entityID, &r.actor, &r.blocked, &raw); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if err := json.Unmarshal([]byte(raw), &r.payload); err != nil {
			t.Fatalf("decode audit payload %q: %v", raw, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// A manager (who holds void_comp_waste directly) cancels a table order from
// the sale screen's popup: no PIN asked, the row is gone from held_sales and
// from the re-rendered popup, the table is free again, and the audit row
// names the manager -- with the same payload shape as #3423's discard row.
func TestHeldCancel_ManagerCancelsTableOrderFromPopup(t *testing.T) {
	mux, dp := newCancelTestDeps(t)
	mgrID, _ := newElevationTestPrincipals(t, dp, "mgr-cancel-1", "cashier-cancel-unused", "112233")
	seedCancelHeldOrder(t, dp, "hold-c1", "Table 4 order", "tbl-c1")
	if !holdTestTableClaimed(t, dp, "tbl-c1") {
		t.Fatal("precondition: the parked order's table claim must exist")
	}

	rec := postForm(mux, "/api/pos/held/cancel", url.Values{"id": {"hold-c1"}, "view": {"parked-orders"}, "tab": {"hold"}},
		&auth.User{ID: mgrID, Role: "manager"})
	if isElevationPrompt(rec) {
		t.Fatalf("a manager holds void_comp_waste: no PIN prompt expected, got %s", rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel: code %d: %s", rec.Code, rec.Body.String())
	}
	if heldSaleExists(t, dp, "hold-c1") {
		t.Fatal("the cancelled order must be gone from held_sales")
	}
	if holdTestTableClaimed(t, dp, "tbl-c1") {
		t.Fatal("the cancelled order's table claim must be released")
	}
	if got := rec.Header().Get("HX-Retarget"); got != "#parked-orders-body" {
		t.Fatalf("popup answer must re-render the popup body, HX-Retarget=%q", got)
	}
	if !strings.Contains(rec.Header().Get("HX-Trigger"), "held-changed") {
		t.Fatalf("cancel must fire held-changed for the Open orders badge, got %q", rec.Header().Get("HX-Trigger"))
	}
	body := html.UnescapeString(rec.Body.String())
	if strings.Contains(body, `data-held-id="hold-c1"`) {
		t.Fatalf("the cancelled order must not be offered any more: %s", body)
	}
	if !strings.Contains(body, httpx.T("en", "open_orders.cancel.done")) {
		t.Fatalf("expected the cancelled toast in the popup body: %s", body)
	}
	if !strings.Contains(body, httpx.T("en", "open_orders.empty")) {
		t.Fatalf("cancelling the last order must leave the empty state: %s", body)
	}

	got := cancelAuditRows(t, dp)
	if len(got) != 1 {
		t.Fatalf("expected one held_sale/cancel audit row, got %d", len(got))
	}
	row := got[0]
	if row.entityID != "hold-c1" || row.actor.String != mgrID || row.blocked.Valid {
		t.Fatalf("audit row = entity %q actor %v blocked %v, want hold-c1 / %s / NULL", row.entityID, row.actor, row.blocked, mgrID)
	}
	for k, want := range map[string]any{
		"label":           "Table 4 order",
		"total_minor":     float64(480),
		"line_count":      float64(1),
		"by_hand_count":   float64(0),
		"table_id":        "tbl-c1",
		"display_no":      "A-7",
		"first_parked_at": "2026-10-03T10:00:00Z",
	} {
		if row.payload[k] != want {
			t.Errorf("payload[%s] = %v, want %v", k, row.payload[k], want)
		}
	}
}

// A cashier without void_comp_waste and no PIN gets the existing elevation
// prompt (not a 403, not a second modal) -- and nothing is cancelled.
func TestHeldCancel_CashierNoPINGetsElevationPrompt(t *testing.T) {
	mux, dp := newCancelTestDeps(t)
	_, cashierID := newElevationTestPrincipals(t, dp, "mgr-cancel-2", "cashier-cancel-2", "223344")
	seedCancelHeldOrder(t, dp, "hold-c2", "Sarah", "")

	rec := postForm(mux, "/api/pos/held/cancel", url.Values{"id": {"hold-c2"}, "view": {"parked-orders"}, "tab": {"hold"}},
		&auth.User{ID: cashierID, Role: "cashier"})
	if !isElevationPrompt(rec) {
		t.Fatalf("cashier with no PIN: want the elevation prompt, got %d: %s", rec.Code, rec.Body.String())
	}
	body := html.UnescapeString(rec.Body.String())
	if !strings.Contains(body, `hx-post="/api/pos/held/cancel"`) || !strings.Contains(body, `hx-target="#parked-orders-hint"`) {
		t.Fatalf("the prompt must retry this same cancel into the popup's hint: %s", body)
	}
	if !strings.Contains(body, `name="id" value="hold-c2"`) || !strings.Contains(body, `name="view" value="parked-orders"`) {
		t.Fatalf("the prompt must carry the order id and view through the retry: %s", body)
	}
	if !strings.Contains(body, "Sarah") {
		t.Fatalf("the approver must see which order they are approving: %s", body)
	}
	if !heldSaleExists(t, dp, "hold-c2") {
		t.Fatal("nothing may be cancelled pending elevation")
	}
	if n := len(cancelAuditRows(t, dp)); n != 0 {
		t.Fatalf("no audit row until the cancel happens, got %d", n)
	}
}

// A manager's PIN clears the gate for a cashier: the order is cancelled and
// the audit row carries the dual attribution (approver as actor, the
// cashier as blocked_actor_id), same as an elevated line void.
func TestHeldCancel_CashierWithManagerPINRecordsApprover(t *testing.T) {
	mux, dp := newCancelTestDeps(t)
	mgrID, cashierID := newElevationTestPrincipals(t, dp, "mgr-cancel-3", "cashier-cancel-3", "334455")
	seedCancelHeldOrder(t, dp, "hold-c3", "Sarah", "")

	rec := postForm(mux, "/api/pos/held/cancel",
		url.Values{"id": {"hold-c3"}, "view": {"parked-orders"}, "tab": {"hold"}, "override_pin": {"334455"}},
		&auth.User{ID: cashierID, Role: "cashier"})
	if isElevationPrompt(rec) {
		t.Fatalf("a manager PIN must clear the gate, got another prompt: %s", rec.Body.String())
	}
	if heldSaleExists(t, dp, "hold-c3") {
		t.Fatal("the order must be cancelled once approved")
	}
	got := cancelAuditRows(t, dp)
	if len(got) != 1 {
		t.Fatalf("expected one held_sale/cancel row, got %d", len(got))
	}
	if got[0].actor.String != mgrID || got[0].blocked.String != cashierID {
		t.Fatalf("elevated audit = actor %v blocked %v, want approver %s / blocked cashier %s", got[0].actor, got[0].blocked, mgrID, cashierID)
	}
}

// From the /open-orders page (a full page, no #basket and no popup body)
// a cancel answers with an htmx redirect back to the same tab with a
// one-shot success banner; the page then renders that banner.
func TestHeldCancel_FromOpenOrdersPageRedirectsWithNotice(t *testing.T) {
	mux, dp := newCancelTestDeps(t)
	mgrID, _ := newElevationTestPrincipals(t, dp, "mgr-cancel-4", "cashier-cancel-4", "445566")
	seedCancelHeldOrder(t, dp, "hold-c4", "Sarah", "")

	rec := postForm(mux, "/api/pos/held/cancel", url.Values{"id": {"hold-c4"}, "view": {"page"}, "tab": {"counter"}},
		&auth.User{ID: mgrID, Role: "manager"})
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Redirect"); got != "/open-orders?tab=counter&msg=open_orders.cancel.done" {
		t.Fatalf("HX-Redirect = %q", got)
	}
	if heldSaleExists(t, dp, "hold-c4") {
		t.Fatal("the order must be cancelled")
	}

	pageMux := http.NewServeMux()
	registerOpenOrders(pageMux, dp)
	page := getWithUser(pageMux, "/open-orders?tab=counter&msg=open_orders.cancel.done", &auth.User{ID: mgrID, Role: "manager"})
	if !strings.Contains(html.UnescapeString(page.Body.String()), httpx.T("en", "open_orders.cancel.done")) {
		t.Fatalf("the page must show the one-shot cancelled banner: %s", page.Body.String())
	}
}

// An order that is already gone (resumed or cancelled on another till since
// the list was drawn) is refused with the existing not-found copy, and
// nothing is audited.
func TestHeldCancel_UnknownOrderIsNotFound(t *testing.T) {
	mux, dp := newCancelTestDeps(t)
	mgrID, _ := newElevationTestPrincipals(t, dp, "mgr-cancel-5", "cashier-cancel-5", "556677")

	rec := postForm(mux, "/api/pos/held/cancel", url.Values{"id": {"hold-none"}, "view": {"page"}, "tab": {"hold"}},
		&auth.User{ID: mgrID, Role: "manager"})
	if got := rec.Header().Get("HX-Redirect"); got != "/open-orders?tab=hold&err=hold.error.not_found" {
		t.Fatalf("HX-Redirect = %q", got)
	}
	if n := len(cancelAuditRows(t, dp)); n != 0 {
		t.Fatalf("nothing cancelled, nothing audited; got %d rows", n)
	}
}

// Both surfaces offer the action on every held row, with a confirmation and
// an accessible name that says which order (UX note, ut-docs#3582).
func TestHeldCancel_ControlOnBothSurfaces(t *testing.T) {
	_, dp := newCancelTestDeps(t)
	seedCancelHeldOrder(t, dp, "hold-c6", "Sarah", "")
	pageMux := http.NewServeMux()
	registerOpenOrders(pageMux, dp)

	for name, path := range map[string]string{"page": "/open-orders", "popup": "/ui/parked-orders"} {
		t.Run(name, func(t *testing.T) {
			rec := getWithUser(pageMux, path, nil)
			body := html.UnescapeString(rec.Body.String())
			for _, want := range []string{
				`hx-post="/api/pos/held/cancel"`,
				`"id":"hold-c6"`,
				`hx-confirm="` + strings.Replace(httpx.T("en", "open_orders.cancel.confirm_named"), "%s", "Sarah", 1) + `"`,
				`aria-label="` + strings.Replace(httpx.T("en", "open_orders.cancel.action_named"), "%s", "Sarah", 1) + `"`,
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("expected %q on the %s, got: %s", want, name, body)
				}
			}
		})
	}
}
