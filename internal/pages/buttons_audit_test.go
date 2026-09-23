package pages

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
)

// ut-docs#2358: a manager-PIN-approved add/remove now writes an
// InsertAuditElevated row, same dual-attribution shape (actor = the
// approver, blocked_actor = the originally-denied session) as the
// existing reorder/move audit calls (ut-docs#2312) already had. Modelled
// on eod_api_test.go's TestPostEODRun_ElevatesOnValidApproverPIN, the
// established pattern for this exact assertion elsewhere in the package.

func TestButtonsAdd_ElevatedPINWritesAuditRow(t *testing.T) {
	mux, d := newButtonsMuxRealSession(t)
	seedOneButton(t, d)
	mgrID, blockedID := newElevationTestPrincipals(t, d, "mgr-btn-add", "blocked-cashier-btn-add", "778899")

	form := url.Values{
		"label":        {"New Tile"},
		"code":         {"NEWTILE"},
		"itemId":       {"itm-btn"},
		"override_pin": {"778899"},
	}
	rec := postForm(mux, "/api/buttons/add", form, &auth.User{ID: blockedID, Role: "cashier"})
	if isElevationPrompt(rec) {
		t.Fatalf("expected the PIN to clear the gate, got an elevation prompt: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("add with approver PIN: code=%d, want 204: %s", rec.Code, rec.Body.String())
	}

	var count int
	if err := d.Db.QueryRow(`SELECT count(*) FROM shortcut_buttons WHERE barcode='NEWTILE'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("button must be created, count=%d err=%v", count, err)
	}

	var actorID, blockedActorID, action string
	if err := d.Db.QueryRow(`SELECT actor_id, blocked_actor_id, action FROM audit_log WHERE action='buttons_add'`).
		Scan(&actorID, &blockedActorID, &action); err != nil {
		t.Fatalf("expected a buttons_add audit row: %v", err)
	}
	if actorID != mgrID {
		t.Fatalf("actor_id = %q, want the approver %q", actorID, mgrID)
	}
	if blockedActorID != blockedID {
		t.Fatalf("blocked_actor_id = %q, want the originally-blocked session user %q", blockedActorID, blockedID)
	}
}

func TestButtonsRemove_ElevatedPINWritesAuditRow(t *testing.T) {
	mux, d := newButtonsMuxRealSession(t)
	seedOneButton(t, d)
	mgrID, blockedID := newElevationTestPrincipals(t, d, "mgr-btn-remove", "blocked-cashier-btn-remove", "112233")

	form := url.Values{
		"code":         {"BTN"},
		"override_pin": {"112233"},
	}
	rec := postForm(mux, "/api/buttons/remove", form, &auth.User{ID: blockedID, Role: "cashier"})
	if isElevationPrompt(rec) {
		t.Fatalf("expected the PIN to clear the gate, got an elevation prompt: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("remove with approver PIN: code=%d, want 204: %s", rec.Code, rec.Body.String())
	}

	var count int
	if err := d.Db.QueryRow(`SELECT count(*) FROM shortcut_buttons WHERE barcode='BTN'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("button must be removed, count=%d err=%v", count, err)
	}

	var actorID, blockedActorID string
	if err := d.Db.QueryRow(`SELECT actor_id, blocked_actor_id FROM audit_log WHERE action='buttons_remove'`).
		Scan(&actorID, &blockedActorID); err != nil {
		t.Fatalf("expected a buttons_remove audit row: %v", err)
	}
	if actorID != mgrID {
		t.Fatalf("actor_id = %q, want the approver %q", actorID, mgrID)
	}
	if blockedActorID != blockedID {
		t.Fatalf("blocked_actor_id = %q, want the originally-blocked session user %q", blockedActorID, blockedID)
	}
}

// A manager session that already passes checkOrElevate's canPerform check
// (elev.Outcome == allowed, never touching the PIN path at all) must NOT
// write an audit row -- only the elevated (PIN-override) case is a
// PIN-approved exception worth journaling; a manager's own direct action
// is already covered by whatever ordinary access logging exists elsewhere,
// and double-auditing it would misrepresent every routine manager add/
// remove as a PIN-approved override. This guards the `elev.Outcome ==
// elevated` condition in registerButtonsAPI specifically -- a future
// refactor loosening it (e.g. to `!= needsElevation`) would start
// auditing every manager action with nothing else to catch it.
func TestButtonsAdd_NonElevatedManager_NoAuditRow(t *testing.T) {
	mux, d := newButtonsMuxRealSession(t)
	seedOneButton(t, d)
	mgr := auth.User{ID: "mgr-direct-add", Role: "manager"}

	form := url.Values{"label": {"Direct Add"}, "code": {"DIRECT1"}, "itemId": {"itm-btn"}}
	rec := postForm(mux, "/api/buttons/add", form, &mgr)
	if isElevationPrompt(rec) {
		t.Fatalf("manager add: got elevation prompt, want past the gate: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("manager add: code=%d, want 204: %s", rec.Code, rec.Body.String())
	}

	var count int
	if err := d.Db.QueryRow(`SELECT count(*) FROM shortcut_buttons WHERE barcode='DIRECT1'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("button must be created, count=%d err=%v", count, err)
	}
	if err := d.Db.QueryRow(`SELECT count(*) FROM audit_log WHERE action='buttons_add'`).Scan(&count); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected NO buttons_add audit row for a manager's own direct (non-elevated) action, got %d", count)
	}
}

// A FAILED store operation (bad add: missing itemId, so ButtonStore.Add's
// own validation rejects it) must never write an audit row, even when the
// PIN itself was valid -- the audit is meant to record what actually
// happened, not merely that a PIN was presented. Proves the `ok` gate
// added in registerButtonsAPI (ut-docs#2358) is actually load-bearing:
// without it, this test fails (an audit row would exist for the rejected
// add).
func TestButtonsAdd_ElevatedPINButStoreRejects_NoAuditRow(t *testing.T) {
	mux, d := newButtonsMuxRealSession(t)
	seedOneButton(t, d)
	_, blockedID := newElevationTestPrincipals(t, d, "mgr-btn-add-fail", "blocked-cashier-btn-add-fail", "445500")

	form := url.Values{
		"label":        {"No Item"},
		"code":         {"NOITEM"},
		"override_pin": {"445500"},
		// itemId deliberately omitted -- ButtonStore.Add requires it.
	}
	rec := postForm(mux, "/api/buttons/add", form, &auth.User{ID: blockedID, Role: "cashier"})
	if isElevationPrompt(rec) {
		t.Fatalf("expected the PIN to clear the gate, got an elevation prompt: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("add with no itemId: code=%d, want 400: %s", rec.Code, rec.Body.String())
	}

	var count int
	if err := d.Db.QueryRow(`SELECT count(*) FROM audit_log WHERE action='buttons_add'`).Scan(&count); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected NO buttons_add audit row for a rejected add, got %d", count)
	}
}
