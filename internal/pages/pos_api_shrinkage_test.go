package pages

import (
	"database/sql"
	"net/http"
	"net/url"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// newPOSMuxRealSession is newPOSTestDeps' real-auth twin (mirrors
// newButtonsMuxRealSession, buttons_api_catalog_management_gate_test.go):
// wires a real AuthSvc against the real seedForPages/migration-seeded
// role_permissions (035_shrinkage_events.sql's void_comp_waste grants
// included), and turns UT_AUTH on so canPerform/checkOrElevate actually
// consult the session in the request context instead of the UT_AUTH=off
// escape hatch every other /api/pos/remove test uses.
func newPOSMuxRealSession(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	t.Setenv("UT_AUTH", "on")
	mux, dp := newPOSTestDeps(t)
	dp.AuthSvc = auth.NewService(dp.Db)
	return mux, dp
}

// TestRemoveHandler_ZeroValueLineSkipsGate (ut-docs#1465): a $0
// promotional line's extended value is always zero -- the reason/
// elevation gate must not apply at all, and removal must succeed with no
// reason field and no session/permission of any kind (deliberately NOT
// using UT_AUTH=off here: a real, unauthenticated request with zero
// session context must still remove a free line, proving the gate itself
// -- not just canPerform's escape hatch -- is what's being skipped).
func TestRemoveHandler_ZeroValueLineSkipsGate(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("FREE"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	key := dp.Engine.Basket().Lines[0].LineKey

	rec := posPostForm(mux, "/api/pos/remove", "key="+key)
	if rec.Code != http.StatusOK {
		t.Fatalf("zero-value remove with no reason: code=%d, want 200: %s", rec.Code, rec.Body.String())
	}
	if b := dp.Engine.Basket(); len(b.Lines) != 0 {
		t.Fatalf("expected the free line removed, got %d lines", len(b.Lines))
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM shrinkage_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("a zero-value removal must not write a shrinkage event, got %d", count)
	}
}

// TestRemoveHandler_NonZeroLineMissingReason_400 (ut-docs#1465): a
// non-zero extended-value line with no `reason` field is a 400,
// independent of permission -- checked under UT_AUTH=off (canPerform's
// documented escape hatch) so this proves the reason requirement itself,
// not a permission denial dressed up as one.
func TestRemoveHandler_NonZeroLineMissingReason_400(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	key := dp.Engine.Basket().Lines[0].LineKey

	rec := posPostForm(mux, "/api/pos/remove", "key="+key)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-zero remove with no reason: code=%d, want 400: %s", rec.Code, rec.Body.String())
	}
	if b := dp.Engine.Basket(); len(b.Lines) != 1 {
		t.Fatalf("line must not be removed on a missing-reason 400, got %d lines", len(b.Lines))
	}
}

// TestRemoveHandler_InvalidReason_400 (ut-docs#1465): a `reason` value
// outside void|comp|waste is also a 400, same non-permission-dependent
// check as the missing-reason case above.
func TestRemoveHandler_InvalidReason_400(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	key := dp.Engine.Basket().Lines[0].LineKey

	rec := posPostForm(mux, "/api/pos/remove", "key="+key+"&reason=bogus")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid reason: code=%d, want 400: %s", rec.Code, rec.Body.String())
	}
	if b := dp.Engine.Basket(); len(b.Lines) != 1 {
		t.Fatalf("line must not be removed on an invalid-reason 400, got %d lines", len(b.Lines))
	}
}

// TestRemoveHandler_CashierNoPINGetsElevationPrompt (ut-docs#1465): a
// cashier session (no void_comp_waste permission, seedForPages' real
// migration-seeded role_permissions) and no override_pin gets the
// checkOrElevate elevation prompt, not a flat 403 -- and the line stays in
// the basket.
func TestRemoveHandler_CashierNoPINGetsElevationPrompt(t *testing.T) {
	mux, dp := newPOSMuxRealSession(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	key := dp.Engine.Basket().Lines[0].LineKey

	cashier := auth.User{ID: "cashier-1", Role: "cashier"}
	rec := postForm(mux, "/api/pos/remove", url.Values{"key": {key}, "reason": {"void"}}, &cashier)
	if !isElevationPrompt(rec) {
		t.Fatalf("cashier with no PIN: want elevation prompt, got %d: %s", rec.Code, rec.Body.String())
	}
	if b := dp.Engine.Basket(); len(b.Lines) != 1 {
		t.Fatalf("line must not be removed pending elevation, got %d lines", len(b.Lines))
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM shrinkage_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("no shrinkage event until elevation succeeds, got %d", count)
	}
}

// TestRemoveHandler_CashierValidPINElevates (ut-docs#1465): a cashier's
// own PIN attempt fails (no permission), but a manager's override_pin
// clears the gate -- line removed, shrinkage event AND audit row written
// with approver_id/actor_id set to the dual-attribution shape
// InsertAuditElevated already establishes elsewhere (buttons_audit_test.go).
func TestRemoveHandler_CashierValidPINElevates(t *testing.T) {
	mux, dp := newPOSMuxRealSession(t)
	mgrID, blockedID := newElevationTestPrincipals(t, dp, "mgr-void-1", "cashier-void-1", "445566")
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	key := dp.Engine.Basket().Lines[0].LineKey

	form := url.Values{"key": {key}, "reason": {"waste"}, "override_pin": {"445566"}}
	rec := postForm(mux, "/api/pos/remove", form, &auth.User{ID: blockedID, Role: "cashier"})
	if isElevationPrompt(rec) {
		t.Fatalf("expected the PIN to clear the gate, got an elevation prompt: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("remove with approver PIN: code=%d, want 200: %s", rec.Code, rec.Body.String())
	}
	if b := dp.Engine.Basket(); len(b.Lines) != 0 {
		t.Fatalf("expected the line removed, got %d lines", len(b.Lines))
	}

	// shrinkage_events' own actor/approver convention (this card's brief):
	// actor_id is the SESSION user who initiated the removal (the
	// originally-blocked cashier here), approver_id the manager who
	// actually cleared the gate — the opposite of audit_log's elevated
	// convention just below (InsertAuditElevated's actorID is the
	// approver), which is deliberate: the two tables answer different
	// questions ("who initiated this loss" vs. "who performed this
	// approved action").
	var reasonCategory, actorID string
	var approverID string
	if err := dp.Db.QueryRow(`SELECT reason_category, actor_id, approver_id FROM shrinkage_events`).
		Scan(&reasonCategory, &actorID, &approverID); err != nil {
		t.Fatalf("expected a shrinkage_events row: %v", err)
	}
	if reasonCategory != "waste" {
		t.Fatalf("reason_category = %q, want waste", reasonCategory)
	}
	if actorID != blockedID {
		t.Fatalf("actor_id = %q, want the originally-blocked session user %q", actorID, blockedID)
	}
	if approverID != mgrID {
		t.Fatalf("approver_id = %q, want the approver %q", approverID, mgrID)
	}

	var auditActor, auditBlocked, auditAction string
	if err := dp.Db.QueryRow(`SELECT actor_id, blocked_actor_id, action FROM audit_log WHERE entity_type='pos_line'`).
		Scan(&auditActor, &auditBlocked, &auditAction); err != nil {
		t.Fatalf("expected a pos_line audit row: %v", err)
	}
	if auditActor != mgrID || auditBlocked != blockedID || auditAction != "waste" {
		t.Fatalf("audit row = actor:%q blocked:%q action:%q, want %q/%q/waste", auditActor, auditBlocked, auditAction, mgrID, blockedID)
	}
}

// TestRemoveHandler_ManagerDirectNoApprover (ut-docs#1465): a manager
// session already holds void_comp_waste directly (035_shrinkage_events.sql
// grants it to manager/admin/super_admin) -- removal succeeds with no PIN
// at all, and approver_id is left empty (this was never a PIN-approved
// override).
func TestRemoveHandler_ManagerDirectNoApprover(t *testing.T) {
	mux, dp := newPOSMuxRealSession(t)
	// audit_log.actor_id carries a real FOREIGN KEY onto users(id)
	// (001_init.sql) — this card's handler writes an audit row even on
	// the direct (non-elevated) path (see the doc comment on
	// TestRemoveHandler_CashierValidPINElevates's audit assertion above),
	// so the session user needs a real users row, unlike a synthetic
	// auth.User{ID: "mgr-direct-1", ...} with nothing backing it in the DB.
	mgrID, _ := newElevationTestPrincipals(t, dp, "mgr-direct-1", "unused-cashier-1", "998877")
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	key := dp.Engine.Basket().Lines[0].LineKey

	mgr := auth.User{ID: mgrID, Role: "manager"}
	rec := postForm(mux, "/api/pos/remove", url.Values{"key": {key}, "reason": {"comp"}}, &mgr)
	if isElevationPrompt(rec) {
		t.Fatalf("manager direct: got elevation prompt, want past the gate: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("manager direct: code=%d, want 200: %s", rec.Code, rec.Body.String())
	}
	if b := dp.Engine.Basket(); len(b.Lines) != 0 {
		t.Fatalf("expected the line removed, got %d lines", len(b.Lines))
	}

	var actorID string
	var approverID string
	if err := dp.Db.QueryRow(`SELECT actor_id, COALESCE(approver_id, '') FROM shrinkage_events`).Scan(&actorID, &approverID); err != nil {
		t.Fatalf("expected a shrinkage_events row: %v", err)
	}
	if actorID != mgr.ID {
		t.Fatalf("actor_id = %q, want the manager %q", actorID, mgr.ID)
	}
	if approverID != "" {
		t.Fatalf("approver_id must be empty for a direct-permission removal, got %q", approverID)
	}

	// Every removal that goes through the reason gate writes an audit_log
	// row (this card's own convention — unlike buttons_api.go's reorder/
	// add/remove, which only audits the PIN-elevated case, a shrinkage
	// removal is itself the durable loss record worth journaling
	// regardless of how the permission was satisfied), but a direct
	// (non-elevated) manager action is NOT dual-attribution: blocked_actor_id
	// must stay NULL/empty, only InsertAuditElevated's elevated path sets it.
	var auditActor, auditAction string
	var auditBlocked sql.NullString
	if err := dp.Db.QueryRow(`SELECT actor_id, blocked_actor_id, action FROM audit_log WHERE entity_type='pos_line'`).
		Scan(&auditActor, &auditBlocked, &auditAction); err != nil {
		t.Fatalf("expected a pos_line audit row: %v", err)
	}
	if auditActor != mgr.ID || auditAction != "comp" {
		t.Fatalf("audit row = actor:%q action:%q, want %q/comp", auditActor, auditAction, mgr.ID)
	}
	if auditBlocked.Valid {
		t.Fatalf("blocked_actor_id must be NULL for a direct (non-elevated) action, got %q", auditBlocked.String)
	}
}
