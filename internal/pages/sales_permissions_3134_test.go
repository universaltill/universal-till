package pages

// ut-docs#3134: the Refund and Cash adjustment boxes on /users/permissions
// are enforced. A role holding the action does it with no manager PIN; a
// role without it gets the existing manager-PIN prompt (checkOrElevate),
// and an approved attempt records both the approver (actor) and the
// blocked session user (blocked_actor_id).

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
)

const (
	perm3134MgrPIN = "482913"
	// The handler's own refund audit row (entity = the new return
	// receipt), not pos.CompleteSale's per-sale row.
	refundAuditWhere3134 = `action='refund' AND entity_id IN (SELECT receipt_no FROM sales WHERE sale_type='return')`
	// The rendered PIN input, not base.html's querySelector('[name="manager_pin"]').
	pinInput3134 = `type="password" name="manager_pin"`
)

// seed3134Users inserts cashier1 (no PIN needed) and mgr1 (PIN
// perm3134MgrPIN) with fixed ids. INSERT OR IGNORE: some deps helpers
// already seed cashier1.
func seed3134Users(t *testing.T, db *sql.DB) {
	t.Helper()
	hash, err := auth.HashPIN(perm3134MgrPIN)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO users(id,username,display_name,pin_hash,role) VALUES('cashier1','cashier1','Cashier One','','cashier')`); err != nil {
		t.Fatalf("seed cashier1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,username,display_name,pin_hash,role) VALUES('mgr1','mgr1','Manager One',?,'manager')`, hash); err != nil {
		t.Fatalf("seed mgr1: %v", err)
	}
}

func set3134Grant(t *testing.T, db *sql.DB, role, action string, granted int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO role_permissions(role, action, granted) VALUES(?, ?, ?)
ON CONFLICT(role, action) DO UPDATE SET granted = excluded.granted`, role, action, granted); err != nil {
		t.Fatalf("set grant %s/%s=%d: %v", role, action, granted, err)
	}
}

var (
	cashier3134 = auth.User{ID: "cashier1", Role: "cashier"}
	manager3134 = auth.User{ID: "mgr1", Role: "manager"}
)

type auditRow3134 struct {
	actor, blocked string
}

func audit3134(t *testing.T, db *sql.DB, where string, args ...any) []auditRow3134 {
	t.Helper()
	rows, err := db.Query(`SELECT COALESCE(actor_id,''), COALESCE(blocked_actor_id,'') FROM audit_log WHERE `+where, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow3134
	for rows.Next() {
		var a auditRow3134
		if err := rows.Scan(&a.actor, &a.blocked); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

// ---- refund ---------------------------------------------------------------

func postRefund3134(t *testing.T, mux *http.ServeMux, receipt string, u auth.User, pin string) *httptest.ResponseRecorder {
	t.Helper()
	form := "receipt=" + receipt + "&qty_0=2"
	if pin != "" {
		form += "&manager_pin=" + pin
	}
	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, u)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func countReturns3134(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sales WHERE sale_type='return'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRefund3134_CashierWithoutGrantNoPINRefused(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, dp, _ := newRefundTestDeps(t)
	seed3134Users(t, dp.Db)
	_, receipt := seedCompletedSaleForRefund(t, dp)

	rec := postRefund3134(t, mux, receipt, cashier3134, "")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "manager PIN required") {
		t.Fatalf("want 403 manager PIN required, got %d %q", rec.Code, rec.Body.String())
	}
	if n := countReturns3134(t, dp.Db); n != 0 {
		t.Fatalf("a refused refund wrote %d return sale(s)", n)
	}
	if rows := audit3134(t, dp.Db, refundAuditWhere3134); len(rows) != 0 {
		t.Fatalf("a refused refund wrote audit rows: %v", rows)
	}
}

func TestRefund3134_CashierWithManagerPINElevatesAndRecordsBoth(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, dp, _ := newRefundTestDeps(t)
	seed3134Users(t, dp.Db)
	_, receipt := seedCompletedSaleForRefund(t, dp)

	rec := postRefund3134(t, mux, receipt, cashier3134, perm3134MgrPIN)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 with a manager PIN, got %d %q", rec.Code, rec.Body.String())
	}
	rows := audit3134(t, dp.Db, refundAuditWhere3134)
	if len(rows) != 1 || rows[0].actor != "mgr1" || rows[0].blocked != "cashier1" {
		t.Fatalf("want one refund audit row actor=mgr1 blocked=cashier1, got %v", rows)
	}
}

func TestRefund3134_ManagerSessionNeedsNoPIN(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, dp, _ := newRefundTestDeps(t)
	seed3134Users(t, dp.Db)
	_, receipt := seedCompletedSaleForRefund(t, dp)

	rec := postRefund3134(t, mux, receipt, manager3134, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("a manager (refund granted) must refund with no PIN, got %d %q", rec.Code, rec.Body.String())
	}
	rows := audit3134(t, dp.Db, refundAuditWhere3134)
	if len(rows) != 1 || rows[0].actor != "mgr1" || rows[0].blocked != "" {
		t.Fatalf("want one refund audit row actor=mgr1 with no blocked actor, got %v", rows)
	}
}

func TestRefund3134_CashierGrantedRefundNeedsNoPIN(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, dp, _ := newRefundTestDeps(t)
	seed3134Users(t, dp.Db)
	set3134Grant(t, dp.Db, "cashier", "refund", 1)
	_, receipt := seedCompletedSaleForRefund(t, dp)

	rec := postRefund3134(t, mux, receipt, cashier3134, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("a cashier granted refund must refund with no PIN, got %d %q", rec.Code, rec.Body.String())
	}
	rows := audit3134(t, dp.Db, refundAuditWhere3134)
	if len(rows) != 1 || rows[0].actor != "cashier1" || rows[0].blocked != "" {
		t.Fatalf("want one refund audit row actor=cashier1, got %v", rows)
	}
}

func TestRefund3134_ManagerWithRefundRevokedNeedsPIN(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, dp, _ := newRefundTestDeps(t)
	seed3134Users(t, dp.Db)
	set3134Grant(t, dp.Db, "manager", "refund", 0)
	_, receipt := seedCompletedSaleForRefund(t, dp)

	rec := postRefund3134(t, mux, receipt, manager3134, "")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "manager PIN required") {
		t.Fatalf("a manager without refund must be asked for a PIN, got %d %q", rec.Code, rec.Body.String())
	}
	if n := countReturns3134(t, dp.Db); n != 0 {
		t.Fatalf("a refused refund wrote %d return sale(s)", n)
	}
}

func getRefundPage3134(t *testing.T, mux *http.ServeMux, receipt string, u auth.User) string {
	t.Helper()
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/refund/"+receipt, nil), u)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /refund/%s: %d", receipt, rec.Code)
	}
	return rec.Body.String()
}

func TestRefundPage3134_PINFieldOnlyWhenSessionCannotRefund(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, dp, _ := newRefundTestDeps(t)
	seed3134Users(t, dp.Db)
	_, receipt := seedCompletedSaleForRefund(t, dp)

	if body := getRefundPage3134(t, mux, receipt, cashier3134); !strings.Contains(body, pinInput3134) {
		t.Fatal("a cashier (no refund grant) must see the manager-PIN field")
	}
	if body := getRefundPage3134(t, mux, receipt, manager3134); strings.Contains(body, pinInput3134) {
		t.Fatal("a manager (refund granted) must not be shown a manager-PIN field")
	}
}

// ---- cash adjustment / skim at close --------------------------------------

func openShift3134(t *testing.T, db *sql.DB) string {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO shifts(id,register_id,cashier_id,opened_at,opening_cash) VALUES('shift3134','reg1','cashier1','2026-01-01T09:00:00Z',10000)`); err != nil {
		t.Fatal(err)
	}
	return "shift3134"
}

func postShiftAs3134(t *testing.T, mux *http.ServeMux, path, form string, u auth.User) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, u)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

type shiftGateCase3134 struct {
	name string
	path string
	form func(shiftID, pin string) string
}

var shiftGateCases3134 = []shiftGateCase3134{
	{
		name: "negative adjustment",
		path: "/api/shifts/adjustment",
		form: func(id, pin string) string {
			f := "shift_id=" + id + "&type=payout&amount=-500&reason=x"
			if pin != "" {
				f += "&manager_pin=" + pin
			}
			return f
		},
	},
	{
		name: "skim at close",
		path: "/api/shifts/close",
		form: func(id, pin string) string {
			f := "shift_id=" + id + "&closing_cash=51110&skim=41110"
			if pin != "" {
				f += "&manager_pin=" + pin
			}
			return f
		},
	},
}

func TestCashAdjustment3134_CashierWithoutGrantNoPINRefused(t *testing.T) {
	for _, tc := range shiftGateCases3134 {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("UT_AUTH", "")
			mux, dp := newShiftsAPITestDeps(t)
			seed3134Users(t, dp.Db)
			id := openShift3134(t, dp.Db)

			rec := postShiftAs3134(t, mux, tc.path, tc.form(id, ""), cashier3134)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "manager PIN required") {
				t.Fatalf("want 403 manager PIN required, got %d %q", rec.Code, rec.Body.String())
			}
			if rows := audit3134(t, dp.Db, `action='cash_adjustment'`); len(rows) != 0 {
				t.Fatalf("a refused request wrote cash_adjustment rows: %v", rows)
			}
			var open int
			if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM shifts WHERE id=? AND closed_at IS NULL`, id).Scan(&open); err != nil {
				t.Fatal(err)
			}
			if open != 1 {
				t.Fatal("a refused request must leave the shift open")
			}
		})
	}
}

func TestCashAdjustment3134_CashierWithManagerPINElevatesAndRecordsBoth(t *testing.T) {
	for _, tc := range shiftGateCases3134 {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("UT_AUTH", "")
			mux, dp := newShiftsAPITestDeps(t)
			seed3134Users(t, dp.Db)
			id := openShift3134(t, dp.Db)

			rec := postShiftAs3134(t, mux, tc.path, tc.form(id, perm3134MgrPIN), cashier3134)
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200 with a manager PIN, got %d %q", rec.Code, rec.Body.String())
			}
			rows := audit3134(t, dp.Db, `action='cash_adjustment'`)
			if len(rows) != 1 || rows[0].actor != "mgr1" || rows[0].blocked != "cashier1" {
				t.Fatalf("want one cash_adjustment row actor=mgr1 blocked=cashier1, got %v", rows)
			}
		})
	}
}

func TestCashAdjustment3134_GrantedRoleNeedsNoPIN(t *testing.T) {
	for _, tc := range shiftGateCases3134 {
		for _, who := range []struct {
			name  string
			user  auth.User
			grant bool
		}{{"manager", manager3134, false}, {"cashier granted", cashier3134, true}} {
			t.Run(tc.name+"/"+who.name, func(t *testing.T) {
				t.Setenv("UT_AUTH", "")
				mux, dp := newShiftsAPITestDeps(t)
				seed3134Users(t, dp.Db)
				if who.grant {
					set3134Grant(t, dp.Db, "cashier", "cash_adjustment", 1)
				}
				id := openShift3134(t, dp.Db)

				rec := postShiftAs3134(t, mux, tc.path, tc.form(id, ""), who.user)
				if rec.Code != http.StatusOK {
					t.Fatalf("a role holding cash_adjustment must need no PIN, got %d %q", rec.Code, rec.Body.String())
				}
				rows := audit3134(t, dp.Db, `action='cash_adjustment'`)
				if len(rows) != 1 || rows[0].actor != who.user.ID || rows[0].blocked != "" {
					t.Fatalf("want one cash_adjustment row actor=%s, no blocked actor, got %v", who.user.ID, rows)
				}
			})
		}
	}
}

func TestCashAdjustment3134_ManagerWithGrantRevokedNeedsPIN(t *testing.T) {
	for _, tc := range shiftGateCases3134 {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("UT_AUTH", "")
			mux, dp := newShiftsAPITestDeps(t)
			seed3134Users(t, dp.Db)
			set3134Grant(t, dp.Db, "manager", "cash_adjustment", 0)
			id := openShift3134(t, dp.Db)

			rec := postShiftAs3134(t, mux, tc.path, tc.form(id, ""), manager3134)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "manager PIN required") {
				t.Fatalf("a manager without cash_adjustment must be asked for a PIN, got %d %q", rec.Code, rec.Body.String())
			}
			if rows := audit3134(t, dp.Db, `action='cash_adjustment'`); len(rows) != 0 {
				t.Fatalf("a refused request wrote cash_adjustment rows: %v", rows)
			}
		})
	}
}

func TestShiftsPage3134_PINFieldOnlyWhenSessionCannotAdjustCash(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, dp := newShiftsPageTestDeps(t)
	seed3134Users(t, dp.Db)
	openShift3134(t, dp.Db)

	get := func(u auth.User) string {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/shifts", nil), u)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /shifts: %d", rec.Code)
		}
		return rec.Body.String()
	}
	if body := get(cashier3134); strings.Count(body, pinInput3134) != 2 || !strings.Contains(body, "Skimming cash to the safe requires manager approval") {
		t.Fatalf("a cashier (no cash_adjustment) must see both manager-PIN fields and the skim hint; got %d fields", strings.Count(body, pinInput3134))
	}
	body := get(manager3134)
	if strings.Contains(body, pinInput3134) {
		t.Fatal("a manager (cash_adjustment granted) must not be shown a manager-PIN field")
	}
	if strings.Contains(body, "Skimming cash to the safe requires manager approval") {
		t.Fatal("a manager (cash_adjustment granted) must not be shown the skim manager-approval hint")
	}
}
