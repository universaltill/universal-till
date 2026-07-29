package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

func newShiftsAPITestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", TaxRate: 20},
		Marketplace: config.MarketplaceConfig{
			EndpointURL: "http://localhost:8081",
		},
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
		AuthSvc:  auth.NewService(db),
	}
	mux := http.NewServeMux()
	registerShiftsAPI(mux, dp)
	return mux, dp
}

func postShiftJSON(t *testing.T, mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func postShiftForm(t *testing.T, mux *http.ServeMux, path, form string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestOpenShift_JSONAndValidation(t *testing.T) {
	mux, dp := newShiftsAPITestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}

	rec := postShiftJSON(t, mux, "/api/shifts/open", `{"register_id":"reg1","cashier_id":"user1","opening_cash":5000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success, got %s", rec.Body.String())
	}

	// A second shift on the SAME register while one is already open must
	// fail — pos.OpenShift enforces one open shift per register.
	rec = postShiftJSON(t, mux, "/api/shifts/open", `{"register_id":"reg1","cashier_id":"user1","opening_cash":1000}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected an error opening a second shift on the same register, got %d: %s", rec.Code, rec.Body.String())
	}

	// Validation.
	if rec := postShiftForm(t, mux, "/api/shifts/open", "cashier_id=user1&opening_cash=100"); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing register_id, got %d", rec.Code)
	}
	if rec := postShiftForm(t, mux, "/api/shifts/open", "register_id=reg1&opening_cash=-1"); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for negative opening_cash, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postShiftJSON(t, mux, "/api/shifts/open", `{not valid`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", rec.Code)
	}
}

func TestOpenShift_DefaultsCashierToSessionUser(t *testing.T) {
	mux, dp := newShiftsAPITestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/shifts/open", strings.NewReader(`{"register_id":"reg1","opening_cash":5000}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req = auth.WithUser(req, auth.User{ID: "user1"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with cashier_id defaulted from session, got %d: %s", rec.Code, rec.Body.String())
	}

	var cashierID string
	if err := dp.Db.QueryRowContext(ctx, `SELECT cashier_id FROM shifts LIMIT 1`).Scan(&cashierID); err != nil {
		t.Fatal(err)
	}
	if cashierID != "user1" {
		t.Fatalf("expected the session user as cashier_id, got %q", cashierID)
	}
}

func TestCloseShift_ComputesExpectedCashAndVariance(t *testing.T) {
	mux, dp := newShiftsAPITestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}

	rec := postShiftJSON(t, mux, "/api/shifts/open", `{"register_id":"reg1","cashier_id":"user1","opening_cash":5000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("open shift: %d: %s", rec.Code, rec.Body.String())
	}
	var shiftID string
	if err := dp.Db.QueryRowContext(ctx, `SELECT id FROM shifts LIMIT 1`).Scan(&shiftID); err != nil {
		t.Fatal(err)
	}

	// Close with a closing count that's £10 short of the (no-sales) expected
	// 5000: expected stays 5000 (opening only, no cash sales), variance =
	// 4900 - 5000 = -100.
	rec = postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"`+shiftID+`","closing_cash":4900}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"expected_cash":5000`) {
		t.Fatalf("expected expected_cash=5000, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"variance":-100`) {
		t.Fatalf("expected variance=-100, got %s", rec.Body.String())
	}

	// Closing an already-closed shift must fail (shift not found or already
	// closed, per GetShiftOpeningCash's ok=false).
	rec = postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"`+shiftID+`","closing_cash":4900}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 closing an already-closed shift, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCloseShift_ValidationErrors(t *testing.T) {
	mux, _ := newShiftsAPITestDeps(t)
	if rec := postShiftForm(t, mux, "/api/shifts/close", "closing_cash=100"); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing shift_id, got %d", rec.Code)
	}
	if rec := postShiftForm(t, mux, "/api/shifts/close", "shift_id=x&closing_cash=-1"); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for negative closing_cash, got %d", rec.Code)
	}
	if rec := postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"does-not-exist","closing_cash":100}`); rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown shift, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRecordCashAdjustment(t *testing.T) {
	mux, dp := newShiftsAPITestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	rec := postShiftJSON(t, mux, "/api/shifts/open", `{"register_id":"reg1","cashier_id":"user1","opening_cash":5000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("open shift: %d: %s", rec.Code, rec.Body.String())
	}
	var shiftID string
	if err := dp.Db.QueryRowContext(ctx, `SELECT id FROM shifts LIMIT 1`).Scan(&shiftID); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/shifts/adjustment",
		strings.NewReader(`{"shift_id":"`+shiftID+`","type":"payout","amount":-500,"reason":"float top-up to safe"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req = auth.WithUser(req, auth.User{ID: "user1"})
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// A subsequent close must reflect the adjustment in expected cash:
	// 5000 (opening) - 500 (payout) = 4500.
	rec3 := postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"`+shiftID+`","closing_cash":4500}`)
	if rec3.Code != http.StatusOK {
		t.Fatalf("close shift: %d: %s", rec3.Code, rec3.Body.String())
	}
	if !strings.Contains(rec3.Body.String(), `"expected_cash":4500`) {
		t.Fatalf("expected expected_cash=4500 reflecting the payout, got %s", rec3.Body.String())
	}
	// ShiftCloseResponse.Variance is `json:"variance,omitempty"` — a true
	// zero variance is dropped from the JSON entirely, not printed as 0.
	if strings.Contains(rec3.Body.String(), `"variance"`) {
		t.Fatalf("expected the zero-variance field omitted (omitempty), got %s", rec3.Body.String())
	}
}

func TestRecordCashAdjustment_ValidationErrors(t *testing.T) {
	mux, _ := newShiftsAPITestDeps(t)
	makeReq := func(form string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/shifts/adjustment", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = auth.WithUser(req, auth.User{ID: "user1"})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := makeReq("type=payout&amount=-100&reason=x"); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing shift_id, got %d", rec.Code)
	}
	if rec := makeReq("shift_id=s1&type=bogus&amount=-100&reason=x"); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid type, got %d", rec.Code)
	}
	if rec := makeReq("shift_id=s1&type=payout&amount=0&reason=x"); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a zero amount, got %d", rec.Code)
	}
	if rec := makeReq("shift_id=s1&type=payout&amount=-100"); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a missing reason, got %d", rec.Code)
	}

	// Empty actor id: unauthorized.
	req := httptest.NewRequest(http.MethodPost, "/api/shifts/adjustment", strings.NewReader("shift_id=s1&type=payout&amount=-100&reason=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, auth.User{ID: ""})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for an empty actor id, got %d: %s", rec.Code, rec.Body.String())
	}
}
