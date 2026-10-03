package pages

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
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
