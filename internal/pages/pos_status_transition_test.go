package pages

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
)

// ut-docs#3368: POST /api/pos/sale/status can no longer reopen a completed
// sale or mark it refunded (409), and voiding through it needs the refund permission
// or a manager's PIN.

func seedCompletedSaleForStatus(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at)
VALUES (?, ?, 'completed', 'sale', 'GBP', 120, 120, '2026-10-02T10:00:00Z')`, id, "R-"+id); err != nil {
		t.Fatalf("seed completed sale: %v", err)
	}
}

// sessionUser returns a session for a seeded operator — audit_log.actor_id
// is a foreign key to users(id), so the session id must be real.
func sessionUser(t *testing.T, db *sql.DB, username, role string) *auth.User {
	t.Helper()
	var id string
	if err := db.QueryRow(`SELECT id FROM users WHERE username = ?`, username).Scan(&id); err != nil {
		t.Fatalf("look up %s: %v", username, err)
	}
	return &auth.User{ID: id, Role: role}
}

func postSaleStatus(t *testing.T, mux *http.ServeMux, u *auth.User, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pos/sale/status", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if u != nil {
		req = auth.WithUser(req, *u)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func readSaleStatusAndAudit(t *testing.T, db *sql.DB, id string) (status string, audits int) {
	t.Helper()
	if err := db.QueryRow(`SELECT status FROM sales WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type = 'sale' AND entity_id = ?`, id).Scan(&audits); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	return
}

func TestSaleStatus_CompletedSaleCannotBeReopened(t *testing.T) {
	dp := newElevationTestDeps(t)
	seedCompletedSaleForStatus(t, dp.Db, "done1")
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	mgr := sessionUser(t, dp.Db, "mgr1", "manager")

	for _, to := range []string{"open", "parked", "completed", "refunded"} {
		rec := postSaleStatus(t, mux, mgr, `{"saleId":"done1","status":"`+to+`"}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("completed -> %s = %d, want 409: %s", to, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "transition") {
			t.Fatalf("raw error text leaked: %s", rec.Body.String())
		}
	}
	if status, audits := readSaleStatusAndAudit(t, dp.Db, "done1"); status != "completed" || audits != 0 {
		t.Fatalf("after refused reopen: status=%q audits=%d, want completed/0", status, audits)
	}
}

func TestSaleStatus_VoidNeedsRefundPermission(t *testing.T) {
	dp := newElevationTestDeps(t)
	seedCompletedSaleForStatus(t, dp.Db, "done2")
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	cashier := sessionUser(t, dp.Db, "cash1", "cashier")

	// No PIN: refused, nothing changed.
	rec := postSaleStatus(t, mux, cashier, `{"saleId":"done2","status":"voided"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier void without PIN = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	// A cashier's own PIN is no manager approval.
	rec = postSaleStatus(t, mux, cashier, `{"saleId":"done2","status":"voided","override_pin":"246800"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier void with a cashier PIN = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if status, audits := readSaleStatusAndAudit(t, dp.Db, "done2"); status != "completed" || audits != 0 {
		t.Fatalf("after refused void: status=%q audits=%d, want completed/0", status, audits)
	}

	// A manager's PIN approves it; the audit row names both.
	rec = postSaleStatus(t, mux, cashier, `{"saleId":"done2","status":"voided","override_pin":"135790"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("cashier void with manager PIN = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	var actor, blocked string
	if err := dp.Db.QueryRow(`SELECT COALESCE(u.username,''), COALESCE(a.blocked_actor_id,'') FROM audit_log a LEFT JOIN users u ON u.id = a.actor_id
WHERE a.entity_type = 'sale' AND a.entity_id = 'done2' AND a.action = 'voided'`).Scan(&actor, &blocked); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if actor != "mgr1" || blocked != cashier.ID {
		t.Fatalf("audit approver/blocked = %q/%q, want mgr1/%s", actor, blocked, cashier.ID)
	}
}

func TestSaleStatus_ManagerVoidsCompletedSale(t *testing.T) {
	dp := newElevationTestDeps(t)
	seedCompletedSaleForStatus(t, dp.Db, "done3")
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)

	rec := postSaleStatus(t, mux, sessionUser(t, dp.Db, "mgr1", "manager"), `{"saleId":"done3","status":"voided"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("manager void = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if status, audits := readSaleStatusAndAudit(t, dp.Db, "done3"); status != "voided" || audits != 1 {
		t.Fatalf("status=%q audits=%d, want voided/1", status, audits)
	}
}
