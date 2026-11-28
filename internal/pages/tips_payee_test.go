package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
)

func postPayee(mux *http.ServeMux, label string, user *auth.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/reports/tips-payees", strings.NewReader(url.Values{"label": {label}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// ut-docs#7247: core (never a plugin) creates a payee-only user on the
// manager's confirmation; it is inert and audited.
func TestTipsPayee_CreatedByManagerOnly_Inert_7247(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, dp := newReportsPageTestDeps(t)

	if rec := postPayee(mux, "Sam", &auth.User{ID: "u1", Role: "cashier"}); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier: %d, want 403", rec.Code)
	}
	if rec := postPayee(mux, "  ", &auth.User{ID: "mgr1", Role: "manager"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty label: %d, want 400", rec.Code)
	}
	rec := postPayee(mux, "Sam Jones", &auth.User{ID: "mgr1", Role: "manager"})
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	var role string
	var active int
	var pin *string
	var id string
	if err := dp.Db.QueryRow(`SELECT id, role, is_active, pin_hash FROM users WHERE display_name='Sam Jones'`).Scan(&id, &role, &active, &pin); err != nil {
		t.Fatal(err)
	}
	if role != "payee" || active != 0 || pin != nil {
		t.Fatalf("payee must be inactive, PIN-less: role=%s active=%d pin=%v", role, active, pin)
	}
	if !strings.Contains(rec.Body.String(), id) {
		t.Fatalf("response must carry the new id: %s", rec.Body.String())
	}
	// Idempotent per label; one audit row for the creation.
	if rec2 := postPayee(mux, "sam  jones", &auth.User{ID: "mgr1", Role: "manager"}); rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), id) {
		t.Fatalf("repeat: %d %s", rec2.Code, rec2.Body.String())
	}
	var n int
	_ = dp.Db.QueryRow(`SELECT count(*) FROM users WHERE role='payee'`).Scan(&n)
	if n != 1 {
		t.Fatalf("payee rows = %d, want 1", n)
	}
	_ = dp.Db.QueryRow(`SELECT count(*) FROM audit_log WHERE action='tips_payee_created' AND entity_id=?`, id).Scan(&n)
	if n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}
	// The created payee is a valid payout target and a suggestion target.
	if workerAllocationCount(t, dp) != 0 {
		t.Fatal("creating a payee must write no allocation")
	}
}

// A payee never shows on the users admin page (no edit/PIN/activate path in
// the UI) and is never reported to the cloud as a user.
func TestTipsPayee_HiddenFromUsersPageAndCloudReport_7247(t *testing.T) {
	mux, dp, _ := newUsersTestDeps(t)
	insertTestUser(t, dp.Db, "adm1", "boss", "Boss Person", "admin")
	if _, err := dp.Db.Exec(`INSERT INTO users(id,username,display_name,role,is_active) VALUES('p1','payee:sam','Sam Payee','payee',0)`); err != nil {
		t.Fatal(err)
	}
	rec := getWithUser(mux, "/users", &auth.User{ID: "adm1", Role: "admin"})
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Sam Payee") || !strings.Contains(rec.Body.String(), "Boss Person") {
		t.Fatalf("payee must be hidden, admin shown: %s", rec.Body.String())
	}
	for _, u := range remoteUsersReport(t.Context(), dp) {
		if u.UserID == "p1" {
			t.Fatal("payee must not be reported to the cloud")
		}
	}
}
