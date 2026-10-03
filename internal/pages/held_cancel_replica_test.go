package pages

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
)

// cancelAsManager posts the cancel as a real manager (void_comp_waste held
// directly), failing if the gate answered with the elevation prompt instead
// -- without a user the request never reaches the cancel at all, which would
// make every "nothing happened" assertion below pass vacuously.
func cancelAsManager(t *testing.T, ct heldSaleCrossTill, mgrID, id string) {
	t.Helper()
	rec := postForm(ct.mux, "/api/pos/held/cancel", url.Values{"id": {id}, "view": {"parked-orders"}},
		&auth.User{ID: mgrID, Role: "manager"})
	if rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("a manager's cancel must run, not prompt: %d %v", rec.Code, rec.Header())
	}
}

// ut-docs#3582 (Tester): Cancel order must CLAIM the order off the shop's
// authority (heldSaleClaimForResume), not just read it and drop the local
// copy -- on a replica, dropping only the local row leaves the order live on
// the primary (it comes back on the next sync, and any other till can still
// tender it) while the cancel audit row and fiscal.order.cancel have already
// gone out. Mirrors TestResumeOnReplica_ClaimsOnPrimaryBeforeRestoring.
func TestCancelOnReplica_ClaimsOrderOffThePrimary(t *testing.T) {
	ct := newHeldSaleCrossTill(t)
	local := ct.parkOnReplica(t, "Table 4")

	mgrID, _ := newElevationTestPrincipals(t, ct.dp, "mgr-replica-1", "cashier-replica-1", "556677")
	cancelAsManager(t, ct, mgrID, local.ID)
	if _, ok, _ := ct.primaryHeld.Get(context.Background(), local.ID); ok {
		t.Fatal("a cancelled order must be gone from the primary, not only from this till's local copy")
	}
	if res := claimOnPrimaryAs(t, ct.primaryURL, "a-456", local.ID); res.Claimed || !res.Known {
		t.Fatalf("after a cancel another till must not be able to take the order, got %+v", res)
	}
	if _, ok := heldSaleRowOnLocal(t, ct.dp, local.ID); ok {
		t.Fatal("the cancelled order's local row must be dropped")
	}
	if n := len(cancelAuditRows(t, ct.dp)); n != 1 {
		t.Fatalf("expected one held_sale/cancel audit row, got %d", n)
	}
}

// An order another till already took (resumed/tendered/cancelled there)
// since this till drew its list is refused as not found, with no audit row
// -- this till never cancelled anything.
func TestCancelOnReplica_OrderTakenElsewhereIsNotFound(t *testing.T) {
	ct := newHeldSaleCrossTill(t)
	local := ct.parkOnReplica(t, "Table 4")
	if res := claimOnPrimaryAs(t, ct.primaryURL, "a-456", local.ID); !res.Claimed {
		t.Fatalf("till A's claim must win, got %+v", res)
	}

	mgrID, _ := newElevationTestPrincipals(t, ct.dp, "mgr-replica-2", "cashier-replica-2", "667788")
	cancelAsManager(t, ct, mgrID, local.ID)
	if n := len(cancelAuditRows(t, ct.dp)); n != 0 {
		t.Fatalf("no cancel happened here, so no audit row: got %d", n)
	}
}

// ut-docs#3621: with the main till unreachable, a primary_synced mirror is
// an order the primary still holds -- cancelling the local copy would let it
// come back on reconnect and be cancelled (audited, fiscal.order.cancel
// dispatched) a second time. The cancel is refused before any side effect:
// local row kept, primary row kept, no audit row, and the cashier is told why.
func TestCancelOnReplica_MainTillUnreachable_RefusesSyncedMirror(t *testing.T) {
	ct := newHeldSaleCrossTill(t)
	local := ct.parkOnReplica(t, "Table 4")
	if row, ok := heldSaleRowOnLocal(t, ct.dp, local.ID); !ok || !row.PrimarySynced {
		t.Fatalf("precondition: the parked order must be a primary_synced mirror, got %+v ok=%v", row, ok)
	}
	setReplicaSettings(t, ct.dp.Settings, deadPrimaryURL(), "b-123")

	mgrID, _ := newElevationTestPrincipals(t, ct.dp, "mgr-replica-3", "cashier-replica-3", "778899")
	rec := postForm(ct.mux, "/api/pos/held/cancel", url.Values{"id": {local.ID}, "view": {"parked-orders"}},
		&auth.User{ID: mgrID, Role: "manager"})
	if rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("a manager's cancel must run, not prompt: %d %v", rec.Code, rec.Header())
	}
	if !strings.Contains(rec.Body.String(), html.EscapeString(httpx.T("en", "open_orders.cancel.error.main_till_unreachable"))) {
		t.Fatalf("the refusal must say the main till can't be reached, got body %q", rec.Body.String())
	}
	if _, ok := heldSaleRowOnLocal(t, ct.dp, local.ID); !ok {
		t.Fatal("a refused cancel must keep this till's copy of the order")
	}
	if _, ok, _ := ct.primaryHeld.Get(context.Background(), local.ID); !ok {
		t.Fatal("the primary's order must be untouched")
	}
	if n := len(cancelAuditRows(t, ct.dp)); n != 0 {
		t.Fatalf("nothing was cancelled, so no audit row: got %d", n)
	}

	// /open-orders gets the same refusal as its ?err= banner key.
	rec = postForm(ct.mux, "/api/pos/held/cancel", url.Values{"id": {local.ID}, "view": {"page"}},
		&auth.User{ID: mgrID, Role: "manager"})
	if got := rec.Header().Get("HX-Redirect"); got != "/open-orders?tab=hold&err=open_orders.cancel.error.main_till_unreachable" {
		t.Fatalf("open-orders refusal redirect: got %q", got)
	}
	if _, ok := heldSaleRowOnLocal(t, ct.dp, local.ID); !ok {
		t.Fatal("the page's refused cancel must keep the order too")
	}
}

// ut-docs#3621: an order parked while the main till was already unreachable
// (primary_synced=0) exists only here -- this till is its authority for the
// outage, so cancelling it stays local and goes through as before.
func TestCancelOnReplica_MainTillUnreachable_CancelsLocalOnlyOrder(t *testing.T) {
	ct := newHeldSaleCrossTill(t)
	setReplicaSettings(t, ct.dp.Settings, deadPrimaryURL(), "b-123")
	local := ct.parkOnReplica(t, "Table 5")
	if row, ok := heldSaleRowOnLocal(t, ct.dp, local.ID); !ok || row.PrimarySynced {
		t.Fatalf("precondition: an outage-parked order must be local-only, got %+v ok=%v", row, ok)
	}

	mgrID, _ := newElevationTestPrincipals(t, ct.dp, "mgr-replica-4", "cashier-replica-4", "889900")
	cancelAsManager(t, ct, mgrID, local.ID)
	if _, ok := heldSaleRowOnLocal(t, ct.dp, local.ID); ok {
		t.Fatal("a local-only order must be cancelled during the outage")
	}
	if n := len(cancelAuditRows(t, ct.dp)); n != 1 {
		t.Fatalf("expected one held_sale/cancel audit row, got %d", n)
	}
}

// ut-docs#3621 (review finding 1): resuming a confirmed mirror while the
// main till is unreachable claims nothing, so the primary still holds the
// order; re-parking it must keep it a confirmed mirror, or the cancel would
// read it as outage-parked and act on it while the primary's copy lives on.
func TestCancelOnReplica_MainTillUnreachable_RefusesResumedAndReparkedMirror(t *testing.T) {
	ct := newHeldSaleCrossTill(t)
	local := ct.parkOnReplica(t, "Table 6")
	setReplicaSettings(t, ct.dp.Settings, deadPrimaryURL(), "b-123")

	if rec := holdTestPost(ct.mux, "/api/pos/resume", "id="+url.QueryEscape(local.ID)); rec.Code != http.StatusOK {
		t.Fatalf("offline resume: %d %s", rec.Code, rec.Body.String())
	}
	if rec := holdTestPost(ct.mux, "/api/pos/hold", ""); rec.Code != http.StatusOK {
		t.Fatalf("offline re-park: %d %s", rec.Code, rec.Body.String())
	}
	if row, ok := heldSaleRowOnLocal(t, ct.dp, local.ID); !ok || !row.PrimarySynced {
		t.Fatalf("an unclaimed mirror re-parked offline must stay a confirmed mirror, got %+v ok=%v", row, ok)
	}

	mgrID, _ := newElevationTestPrincipals(t, ct.dp, "mgr-replica-5", "cashier-replica-5", "990011")
	rec := postForm(ct.mux, "/api/pos/held/cancel", url.Values{"id": {local.ID}, "view": {"page"}},
		&auth.User{ID: mgrID, Role: "manager"})
	if got := rec.Header().Get("HX-Redirect"); got != "/open-orders?tab=hold&err=open_orders.cancel.error.main_till_unreachable" {
		t.Fatalf("cancel of a re-parked mirror must be refused, got redirect %q", got)
	}
	if _, ok := heldSaleRowOnLocal(t, ct.dp, local.ID); !ok {
		t.Fatal("a refused cancel must keep this till's copy of the order")
	}
	if _, ok, _ := ct.primaryHeld.Get(context.Background(), local.ID); !ok {
		t.Fatal("the primary's order must be untouched")
	}
	if n := len(cancelAuditRows(t, ct.dp)); n != 0 {
		t.Fatalf("nothing was cancelled, so no audit row: got %d", n)
	}
}
