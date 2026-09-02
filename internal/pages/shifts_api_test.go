package pages

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
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
	// { "data": …, "error": null } envelope (ut-docs#378), success nested
	// under "data" rather than the old bare top-level shape.
	data, hasData, errVal, hasError := envelopeOf(t, rec.Body.Bytes())
	if !hasData || !hasError {
		t.Fatalf("expected a {data,error} envelope, got %s", rec.Body.String())
	}
	if string(errVal) != "null" {
		t.Fatalf("expected error:null on success, got %s", errVal)
	}
	if !strings.Contains(string(data), `"success":true`) {
		t.Fatalf("expected success, got %s", data)
	}

	// A second shift on the SAME register while one is already open must
	// fail — pos.OpenShift enforces one open shift per register.
	rec = postShiftJSON(t, mux, "/api/shifts/open", `{"register_id":"reg1","cashier_id":"user1","opening_cash":1000}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected an error opening a second shift on the same register, got %d: %s", rec.Code, rec.Body.String())
	}
	if data, hasData, errVal, hasError := envelopeOf(t, rec.Body.Bytes()); !hasData || string(data) != "null" || !hasError || string(errVal) == "null" {
		t.Fatalf(`expected { "data": null, "error": "…" } for a failed open, got %s`, rec.Body.String())
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
	} else if data, hasData, errVal, hasError := envelopeOf(t, rec.Body.Bytes()); !hasData || string(data) != "null" || !hasError || string(errVal) == "null" {
		t.Fatalf(`expected { "data": null, "error": "…" } for invalid JSON, got %s`, rec.Body.String())
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
	// { "data": …, "error": null } envelope (ut-docs#378).
	data, hasData, errVal, hasError := envelopeOf(t, rec.Body.Bytes())
	if !hasData || !hasError {
		t.Fatalf("expected a {data,error} envelope, got %s", rec.Body.String())
	}
	if string(errVal) != "null" {
		t.Fatalf("expected error:null on success, got %s", errVal)
	}
	if !strings.Contains(string(data), `"expected_cash":5000`) {
		t.Fatalf("expected expected_cash=5000, got %s", data)
	}
	if !strings.Contains(string(data), `"variance":-100`) {
		t.Fatalf("expected variance=-100, got %s", data)
	}

	// Closing an already-closed shift must fail (shift not found or already
	// closed, per GetShiftOpeningCash's ok=false).
	rec = postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"`+shiftID+`","closing_cash":4900}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 closing an already-closed shift, got %d: %s", rec.Code, rec.Body.String())
	}
	if data, hasData, errVal, hasError := envelopeOf(t, rec.Body.Bytes()); !hasData || string(data) != "null" || !hasError || string(errVal) == "null" {
		t.Fatalf(`expected { "data": null, "error": "…" } closing an already-closed shift, got %s`, rec.Body.String())
	}
}

// TestCloseShift_HTMLSummaryIsCurrencyAwareAndTranslated: respondCloseSuccess's
// HTML-fragment path (the close form's actual on-screen confirmation, not
// the JSON envelope) used to hardcode a GBP `£%.2f` conversion and English
// prose outside T() entirely (ut-docs#1289/#1401 — same root cause as
// #1274's CarryForwardDisplay). On a 0-decimal-currency shop (IRT/toman)
// that both showed the wrong symbol and silently divided a whole-unit
// amount by 100. Covers both the plain-close and skim variants, and that a
// non-English locale actually renders translated prose.
func TestCloseShift_HTMLSummaryIsCurrencyAwareAndTranslated(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
	mux, dp := newShiftsAPITestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	httpx.InitCurrency("IRT")
	t.Cleanup(func() { httpx.InitCurrency("GBP") }) // ut-docs#970 convention: process-global, reset for later tests in this package.

	openAndGetID := func() string {
		rec := postShiftJSON(t, mux, "/api/shifts/open", `{"register_id":"reg1","cashier_id":"user1","opening_cash":5000}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("open shift: %d: %s", rec.Code, rec.Body.String())
		}
		var id string
		if err := dp.Db.QueryRowContext(ctx, `SELECT id FROM shifts WHERE closed_at IS NULL`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	// Plain close, no skim: 5000 IRT opening, 4900 IRT counted — expected
	// stays 5000 (no sales), variance -100. A 0-decimal currency's minor
	// units ARE its major units, so a correct render shows "5,000"/"4,900",
	// never "50.00"/"49.00" (the old `/100` corruption).
	shiftID := openAndGetID()
	rec := postShiftForm(t, mux, "/api/shifts/close", "shift_id="+shiftID+"&closing_cash=4900")
	if rec.Code != http.StatusOK {
		t.Fatalf("close: %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "تومان") {
		t.Fatalf("expected the IRT symbol in the close summary, got:\n%s", body)
	}
	if strings.Contains(body, "£") {
		t.Fatalf("expected NO leftover GBP symbol once currency is 0-decimal, got:\n%s", body)
	}
	if strings.Contains(body, "50.00") || strings.Contains(body, "49.00") {
		t.Fatalf("expected NO /100-corrupted 2-decimal amount for a 0-decimal currency, got:\n%s", body)
	}
	if !strings.Contains(body, "5,000") || !strings.Contains(body, "4,900") {
		t.Fatalf("expected the whole-unit IRT amounts (5,000 / 4,900), got:\n%s", body)
	}
	if !strings.Contains(body, "Shift closed. Expected:") {
		t.Fatalf("expected the translated shifts.close_success template, got:\n%s", body)
	}

	// Skim variant: the New float / Skim figures must be equally
	// currency-aware, not just the three plain-close figures.
	shiftID = openAndGetID()
	rec = postShiftForm(t, mux, "/api/shifts/close", "shift_id="+shiftID+"&closing_cash=5000&skim=3000&skim_reason=to+safe")
	if rec.Code != http.StatusOK {
		t.Fatalf("close with skim: %d: %s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if strings.Contains(body, "£") || strings.Contains(body, "30.00") {
		t.Fatalf("expected the skim/new-float figures to be currency-aware too, got:\n%s", body)
	}
	if !strings.Contains(body, "Skim:") || !strings.Contains(body, "New float:") {
		t.Fatalf("expected the translated shifts.close_success_with_skim template, got:\n%s", body)
	}

	// A non-English locale actually renders translated prose, not the
	// English template with formatted numbers spliced in.
	shiftID = openAndGetID()
	req := httptest.NewRequest(http.MethodPost, "/api/shifts/close", strings.NewReader("shift_id="+shiftID+"&closing_cash=4900"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "ut_lang", Value: "fa"})
	recFa := httptest.NewRecorder()
	mux.ServeHTTP(recFa, req)
	if recFa.Code != http.StatusOK {
		t.Fatalf("close (fa locale): %d: %s", recFa.Code, recFa.Body.String())
	}
	if !strings.Contains(recFa.Body.String(), "شیفت بسته شد") {
		t.Fatalf("expected the fa translation of shifts.close_success, got:\n%s", recFa.Body.String())
	}
}

// TestOpenShift_CarryForwardDefaultsOpeningCash: when opening_cash is
// omitted, the new shift's float defaults to what the last close on that
// register left in the drawer (its new_float after any skim) — the operator
// no longer re-types it. An explicitly provided value, including 0, is
// always respected (ut-docs#1006).
func TestOpenShift_CarryForwardDefaultsOpeningCash(t *testing.T) {
	// This test is about carry-forward, not the skim manager-PIN gate
	// (covered separately by TestCloseShift_SkimRequiresManagerPIN and
	// friends) — same UT_AUTH=off split TestCloseShift_SkimAndCountProtocol
	// uses.
	t.Setenv("UT_AUTH", "off")
	mux, dp := newShiftsAPITestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}

	// First shift: open with £100.00, take nothing, count £511.10 in (via a
	// manual count) — close skimming £411.10, leaving a £100.00 new float.
	rec := postShiftJSON(t, mux, "/api/shifts/open", `{"register_id":"reg1","cashier_id":"user1","opening_cash":10000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("open shift: %d: %s", rec.Code, rec.Body.String())
	}
	var shiftID string
	if err := dp.Db.QueryRowContext(ctx, `SELECT id FROM shifts LIMIT 1`).Scan(&shiftID); err != nil {
		t.Fatal(err)
	}
	rec = postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"`+shiftID+`","closing_cash":51110,"skim":41110}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("close shift with skim: %d: %s", rec.Code, rec.Body.String())
	}

	// Open with opening_cash omitted (JSON): carried forward from the close.
	rec = postShiftJSON(t, mux, "/api/shifts/open", `{"register_id":"reg1","cashier_id":"user1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("open with omitted opening_cash: %d: %s", rec.Code, rec.Body.String())
	}
	var openingCash int64
	if err := dp.Db.QueryRowContext(ctx, `SELECT opening_cash FROM shifts WHERE closed_at IS NULL`).Scan(&openingCash); err != nil {
		t.Fatal(err)
	}
	if openingCash != 10000 {
		t.Fatalf("expected carried-forward opening cash 10000, got %d", openingCash)
	}
	if err := dp.Db.QueryRowContext(ctx, `SELECT id FROM shifts WHERE closed_at IS NULL`).Scan(&shiftID); err != nil {
		t.Fatal(err)
	}
	rec = postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"`+shiftID+`","closing_cash":10000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("close second shift: %d: %s", rec.Code, rec.Body.String())
	}

	// Explicit 0 must NOT be replaced by the carried value.
	rec = postShiftJSON(t, mux, "/api/shifts/open", `{"register_id":"reg1","cashier_id":"user1","opening_cash":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("open with explicit 0: %d: %s", rec.Code, rec.Body.String())
	}
	if err := dp.Db.QueryRowContext(ctx, `SELECT opening_cash FROM shifts WHERE closed_at IS NULL`).Scan(&openingCash); err != nil {
		t.Fatal(err)
	}
	if openingCash != 0 {
		t.Fatalf("explicit opening_cash=0 must be respected, got %d", openingCash)
	}
	if err := dp.Db.QueryRowContext(ctx, `SELECT id FROM shifts WHERE closed_at IS NULL`).Scan(&shiftID); err != nil {
		t.Fatal(err)
	}
	if rec := postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"`+shiftID+`","closing_cash":0}`); rec.Code != http.StatusOK {
		t.Fatalf("close third shift: %d: %s", rec.Code, rec.Body.String())
	}

	// Form path with the field absent entirely: also carried forward
	// (from the third close, which left 0 in the drawer... use reg1's
	// latest close = 0). Seed a fresh register to make the carried value
	// distinguishable from a hardcoded 0.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg2','Back Till',1)`); err != nil {
		t.Fatal(err)
	}
	rec = postShiftForm(t, mux, "/api/shifts/open", "register_id=reg2&cashier_id=user1&opening_cash=2500")
	if rec.Code != http.StatusOK {
		t.Fatalf("open reg2: %d: %s", rec.Code, rec.Body.String())
	}
	if err := dp.Db.QueryRowContext(ctx, `SELECT id FROM shifts WHERE closed_at IS NULL AND register_id='reg2'`).Scan(&shiftID); err != nil {
		t.Fatal(err)
	}
	if rec := postShiftForm(t, mux, "/api/shifts/close", "shift_id="+shiftID+"&closing_cash=2500"); rec.Code != http.StatusOK {
		t.Fatalf("close reg2: %d: %s", rec.Code, rec.Body.String())
	}
	rec = postShiftForm(t, mux, "/api/shifts/open", "register_id=reg2&cashier_id=user1")
	if rec.Code != http.StatusOK {
		t.Fatalf("open reg2 with omitted opening_cash: %d: %s", rec.Code, rec.Body.String())
	}
	if err := dp.Db.QueryRowContext(ctx, `SELECT opening_cash FROM shifts WHERE closed_at IS NULL AND register_id='reg2'`).Scan(&openingCash); err != nil {
		t.Fatal(err)
	}
	if openingCash != 2500 {
		t.Fatalf("form path: expected carried-forward 2500, got %d", openingCash)
	}
}

// TestCloseShift_SkimAndCountProtocol wires the new close-time skim /
// count-protocol params through the handler into pos.CloseShift.
func TestCloseShift_SkimAndCountProtocol(t *testing.T) {
	// Auth is exercised separately below (TestCloseShift_
	// SkimRequiresManagerPIN and friends); this test is about the
	// accounting effect of a skim, same split TestRecordCashAdjustment
	// uses for the standalone adjustment endpoint.
	t.Setenv("UT_AUTH", "off")
	mux, dp := newShiftsAPITestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	openAndGetID := func() string {
		rec := postShiftJSON(t, mux, "/api/shifts/open", `{"register_id":"reg1","cashier_id":"user1","opening_cash":10000}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("open shift: %d: %s", rec.Code, rec.Body.String())
		}
		var id string
		if err := dp.Db.QueryRowContext(ctx, `SELECT id FROM shifts WHERE closed_at IS NULL`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	shiftID := openAndGetID()
	// Form path, mirroring shifts.html's minor-unit hidden fields.
	rec := postShiftForm(t, mux, "/api/shifts/close",
		"shift_id="+shiftID+"&closing_cash=51110&skim=41110&skim_reason=to+safe&count_protocol="+`%7B%225000%22%3A10%2C%22100%22%3A11%2C%2210%22%3A1%7D`)
	if rec.Code != http.StatusOK {
		t.Fatalf("close with skim: %d: %s", rec.Code, rec.Body.String())
	}
	var newFloat int64
	var countProtocol string
	if err := dp.Db.QueryRowContext(ctx, `SELECT new_float, count_protocol FROM shifts WHERE id=?`, shiftID).Scan(&newFloat, &countProtocol); err != nil {
		t.Fatal(err)
	}
	if newFloat != 10000 {
		t.Fatalf("expected new_float 10000, got %d", newFloat)
	}
	if countProtocol != `{"5000":10,"100":11,"10":1}` {
		t.Fatalf("count_protocol not persisted, got %q", countProtocol)
	}
	var skimType string
	var skimAmount int64
	if err := dp.Db.QueryRowContext(ctx, `
SELECT json_extract(data_json,'$.type'), CAST(json_extract(data_json,'$.amount') AS INTEGER)
FROM audit_log WHERE entity_type='shift' AND entity_id=? AND action='cash_adjustment'`, shiftID).Scan(&skimType, &skimAmount); err != nil {
		t.Fatal(err)
	}
	if skimType != "skim" || skimAmount != -41110 {
		t.Fatalf("expected skim audit row (-41110), got type=%q amount=%d", skimType, skimAmount)
	}

	// A negative skim is rejected before anything is written.
	shiftID = openAndGetID()
	if rec := postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"`+shiftID+`","closing_cash":10000,"skim":-1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for negative skim, got %d: %s", rec.Code, rec.Body.String())
	}
	// A skim larger than the counted drawer is rejected and the shift
	// stays open.
	if rec := postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"`+shiftID+`","closing_cash":10000,"skim":10001}`); rec.Code == http.StatusOK {
		t.Fatalf("expected an error for skim > counted cash, got 200: %s", rec.Body.String())
	}
	var stillOpen int
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM shifts WHERE id=? AND closed_at IS NULL`, shiftID).Scan(&stillOpen); err != nil {
		t.Fatal(err)
	}
	if stillOpen != 1 {
		t.Fatal("rejected skim must leave the shift open")
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

// ut-docs#1006 review finding 1 (blocker, confirmed live by the reviewer): a
// skim moves real cash out of the drawer — exactly the class of action
// RecordCashAdjustment's sign-based manager-PIN gate exists for
// (ut-docs#266) — but the close-flow skim path bypassed it entirely,
// writing the negative cash_adjustment audit row with no PIN check at all.
// A plain close (no skim) must stay ungated, same as a positive adjustment
// does today.
func TestCloseShift_SkimRequiresManagerPIN(t *testing.T) {
	// UT_AUTH unset (auth enabled) — the default this pipeline's own
	// TestRecordCashAdjustment_RequiresManagerPINWhenAmountRemovesCash uses.
	t.Setenv("UT_AUTH", "")
	mux, dp := newShiftsAPITestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	openReq := httptest.NewRequest(http.MethodPost, "/api/shifts/open", strings.NewReader(`{"register_id":"reg1","cashier_id":"user1","opening_cash":10000}`))
	openReq.Header.Set("Content-Type", "application/json")
	openRec := httptest.NewRecorder()
	mux.ServeHTTP(openRec, openReq)
	if openRec.Code != http.StatusOK {
		t.Fatalf("open shift: %d: %s", openRec.Code, openRec.Body.String())
	}
	var shiftID string
	if err := dp.Db.QueryRowContext(ctx, `SELECT id FROM shifts LIMIT 1`).Scan(&shiftID); err != nil {
		t.Fatal(err)
	}

	// A skim with no manager_pin must be rejected, and the shift must stay
	// open (no partial close, no cash_adjustment row written).
	req := httptest.NewRequest(http.MethodPost, "/api/shifts/close",
		strings.NewReader(`shift_id=`+shiftID+`&closing_cash=51110&skim=41110`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, auth.User{ID: "user1"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a skim with no manager PIN, got %d: %s", rec.Code, rec.Body.String())
	}
	var stillOpen int
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM shifts WHERE id=? AND closed_at IS NULL`, shiftID).Scan(&stillOpen); err != nil {
		t.Fatal(err)
	}
	if stillOpen != 1 {
		t.Fatal("a rejected skim must leave the shift open, not partially close it")
	}
	var adjustments int
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE entity_type='shift' AND entity_id=? AND action='cash_adjustment'`, shiftID).Scan(&adjustments); err != nil {
		t.Fatal(err)
	}
	if adjustments != 0 {
		t.Fatal("a rejected skim must not write a cash_adjustment audit row")
	}

	// A plain close (no skim) needs no PIN at all — the gate only fires
	// when cash actually leaves the drawer.
	req2 := httptest.NewRequest(http.MethodPost, "/api/shifts/close",
		strings.NewReader(`shift_id=`+shiftID+`&closing_cash=10000`))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2 = auth.WithUser(req2, auth.User{ID: "user1"})
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 for a plain close with no skim and no PIN, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

// Correct manager PIN authorizes the skim, and — same as PfandRueckgabe/
// RecordCashAdjustment — the approving MANAGER, not the shift's cashier,
// becomes the skim audit row's actor.
func TestCloseShift_SkimWithManagerPINRecordsManagerAsActor(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, dp := newShiftsAPITestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPIN("482913")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx,
		`INSERT INTO users(id,username,display_name,pin_hash,role,created_at) VALUES('mgr1','mgr1','Manager One',?,'manager',datetime('now'))`, hash); err != nil {
		t.Fatal(err)
	}
	openReq := httptest.NewRequest(http.MethodPost, "/api/shifts/open", strings.NewReader(`{"register_id":"reg1","cashier_id":"cashier1","opening_cash":10000}`))
	openReq.Header.Set("Content-Type", "application/json")
	openRec := httptest.NewRecorder()
	mux.ServeHTTP(openRec, openReq)
	if openRec.Code != http.StatusOK {
		t.Fatalf("open shift: %d: %s", openRec.Code, openRec.Body.String())
	}
	var shiftID string
	if err := dp.Db.QueryRowContext(ctx, `SELECT id FROM shifts LIMIT 1`).Scan(&shiftID); err != nil {
		t.Fatal(err)
	}

	makeReq := func(form string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/shifts/close", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = auth.WithUser(req, auth.User{ID: "cashier1", Role: "cashier"})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// Wrong PIN: forbidden, shift stays open.
	if rec := makeReq("shift_id=" + shiftID + "&closing_cash=51110&skim=41110&manager_pin=000000"); rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 with a wrong PIN, got %d: %s", rec.Code, rec.Body.String())
	}

	// Correct PIN: succeeds, and the manager (not cashier1) is the skim
	// audit row's actor.
	rec2 := makeReq("shift_id=" + shiftID + "&closing_cash=51110&skim=41110&manager_pin=482913")
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 with a correct manager PIN, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var actorID string
	if err := dp.Db.QueryRowContext(ctx, `SELECT actor_id FROM audit_log WHERE entity_type='shift' AND entity_id=? AND action='cash_adjustment'`, shiftID).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if actorID != "mgr1" {
		t.Fatalf("expected the manager as the skim audit row's actor, got %q (was: the shift's cashier — ut-docs#1006 review finding 1)", actorID)
	}
}

// ut-docs#1006 review finding 6: skim>closing_cash and a malformed
// count_protocol are user-input errors and must 400, not fall through to
// the handler's generic 500 mapping.
func TestCloseShift_SkimAndCountProtocolValidationAre400(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newShiftsAPITestDeps(t)
	ctx := context.Background()

	// A rejected close leaves the shift open (see
	// TestCloseShift_SkimRequiresManagerPIN), so each case below opens on
	// its OWN fresh register rather than reusing one that may still be open.
	regN := 0
	openAndGetID := func() string {
		regN++
		regID := fmt.Sprintf("reg-400-%d", regN)
		if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES(?,?,1)`, regID, "Till "+regID); err != nil {
			t.Fatal(err)
		}
		rec := postShiftJSON(t, mux, "/api/shifts/open", `{"register_id":"`+regID+`","cashier_id":"user1","opening_cash":10000}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("open shift: %d: %s", rec.Code, rec.Body.String())
		}
		var id string
		if err := dp.Db.QueryRowContext(ctx, `SELECT id FROM shifts WHERE register_id=? AND closed_at IS NULL`, regID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	shiftID := openAndGetID()
	rec := postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"`+shiftID+`","closing_cash":10000,"skim":10001}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for skim > closing_cash, got %d: %s", rec.Code, rec.Body.String())
	}

	shiftID = openAndGetID()
	rec2 := postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"`+shiftID+`","closing_cash":10000,"count_protocol":"[1,2,3]"}`)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-object count_protocol, got %d: %s", rec2.Code, rec2.Body.String())
	}

	shiftID = openAndGetID()
	rec3 := postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"`+shiftID+`","closing_cash":10000,"count_protocol":"\"hello\""}`)
	if rec3.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a bare-string count_protocol, got %d: %s", rec3.Code, rec3.Body.String())
	}
}

func TestRecordCashAdjustment(t *testing.T) {
	// Auth is exercised separately below (TestRecordCashAdjustment_
	// RequiresManagerPINWhenAmountRemovesCash and friends); this test is
	// about the accounting effect of a payout, same pattern as
	// TestPfandRueckgabe_RecordsPayoutAndReducesExpectedCash.
	t.Setenv("UT_AUTH", "off")
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
	// { "data": …, "error": null } envelope (ut-docs#378).
	adjData, hasData, errVal, hasError := envelopeOf(t, rec2.Body.Bytes())
	if !hasData || !hasError {
		t.Fatalf("expected a {data,error} envelope, got %s", rec2.Body.String())
	}
	if string(errVal) != "null" {
		t.Fatalf("expected error:null on success, got %s", errVal)
	}
	if !strings.Contains(string(adjData), `"success":true`) {
		t.Fatalf("expected data.success=true, got %s", adjData)
	}

	// A subsequent close must reflect the adjustment in expected cash:
	// 5000 (opening) - 500 (payout) = 4500.
	rec3 := postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"`+shiftID+`","closing_cash":4500}`)
	if rec3.Code != http.StatusOK {
		t.Fatalf("close shift: %d: %s", rec3.Code, rec3.Body.String())
	}
	closeData, hasData, errVal, hasError := envelopeOf(t, rec3.Body.Bytes())
	if !hasData || !hasError {
		t.Fatalf("expected a {data,error} envelope, got %s", rec3.Body.String())
	}
	if string(errVal) != "null" {
		t.Fatalf("expected error:null on success, got %s", errVal)
	}
	if !strings.Contains(string(closeData), `"expected_cash":4500`) {
		t.Fatalf("expected expected_cash=4500 reflecting the payout, got %s", closeData)
	}
	// ShiftCloseResponse.Variance is `json:"variance,omitempty"` — a true
	// zero variance is dropped from the JSON entirely, not printed as 0.
	if strings.Contains(string(closeData), `"variance"`) {
		t.Fatalf("expected the zero-variance field omitted (omitempty), got %s", closeData)
	}
}

func TestRecordCashAdjustment_RequiresManagerPINWhenAmountRemovesCash(t *testing.T) {
	// UT_AUTH is unset in this test process, so auth.Disabled(...) is
	// false — a negative amount must require a manager PIN, same gate as
	// PfandRueckgabe/refund (ut-docs#266: the generic endpoint used to let
	// any cashier record an unapproved payout).
	t.Setenv("UT_AUTH", "")
	mux, dp := newShiftsAPITestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	openReq := httptest.NewRequest(http.MethodPost, "/api/shifts/open", strings.NewReader(`{"register_id":"reg1","cashier_id":"user1","opening_cash":5000}`))
	openReq.Header.Set("Content-Type", "application/json")
	openRec := httptest.NewRecorder()
	mux.ServeHTTP(openRec, openReq)
	if openRec.Code != http.StatusOK {
		t.Fatalf("open shift: %d: %s", openRec.Code, openRec.Body.String())
	}
	var shiftID string
	if err := dp.Db.QueryRowContext(ctx, `SELECT id FROM shifts LIMIT 1`).Scan(&shiftID); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/shifts/adjustment",
		strings.NewReader(`shift_id=`+shiftID+`&type=payout&amount=-500&reason=till+float+to+safe`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, auth.User{ID: "user1"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager PIN, got %d: %s", rec.Code, rec.Body.String())
	}

	// The "type" field is a client-supplied label, not an authoritative
	// signal — a cashier relabeling the same negative amount as
	// type=adjustment must be gated identically, or the fix is a no-op
	// bypassable by just picking the other option in the form.
	req2 := httptest.NewRequest(http.MethodPost, "/api/shifts/adjustment",
		strings.NewReader(`shift_id=`+shiftID+`&type=adjustment&amount=-500&reason=till+count+correction`))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2 = auth.WithUser(req2, auth.User{ID: "user1"})
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for type=adjustment with a negative amount and no manager PIN, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

func TestRecordCashAdjustment_PositiveAmountNeedsNoManagerPIN(t *testing.T) {
	// UT_AUTH unset (auth enabled), no manager_pin supplied — a positive
	// adjustment (cash going IN, e.g. a float top-up correction) is not
	// the risk this gate exists for, so it must succeed same as before.
	t.Setenv("UT_AUTH", "")
	mux, dp := newShiftsAPITestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	openReq := httptest.NewRequest(http.MethodPost, "/api/shifts/open", strings.NewReader(`{"register_id":"reg1","cashier_id":"user1","opening_cash":5000}`))
	openReq.Header.Set("Content-Type", "application/json")
	openRec := httptest.NewRecorder()
	mux.ServeHTTP(openRec, openReq)
	if openRec.Code != http.StatusOK {
		t.Fatalf("open shift: %d: %s", openRec.Code, openRec.Body.String())
	}
	var shiftID string
	if err := dp.Db.QueryRowContext(ctx, `SELECT id FROM shifts LIMIT 1`).Scan(&shiftID); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/shifts/adjustment",
		strings.NewReader(`shift_id=`+shiftID+`&type=adjustment&amount=200&reason=float+top-up`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, auth.User{ID: "user1"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a positive adjustment with no manager PIN, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRecordCashAdjustment_WrongManagerPINForbiddenCorrectPINRecordsManagerAsActor(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, dp := newShiftsAPITestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	openReq := httptest.NewRequest(http.MethodPost, "/api/shifts/open", strings.NewReader(`{"register_id":"reg1","cashier_id":"cashier1","opening_cash":5000}`))
	openReq.Header.Set("Content-Type", "application/json")
	openRec := httptest.NewRecorder()
	mux.ServeHTTP(openRec, openReq)
	if openRec.Code != http.StatusOK {
		t.Fatalf("open shift: %d: %s", openRec.Code, openRec.Body.String())
	}
	var shiftID string
	if err := dp.Db.QueryRowContext(ctx, `SELECT id FROM shifts LIMIT 1`).Scan(&shiftID); err != nil {
		t.Fatal(err)
	}

	hash, err := auth.HashPIN("482913")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx,
		`INSERT INTO users(id,username,display_name,pin_hash,role,created_at) VALUES('mgr1','mgr1','Manager One',?,'manager',datetime('now'))`, hash); err != nil {
		t.Fatal(err)
	}

	makeReq := func(form string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/shifts/adjustment", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = auth.WithUser(req, auth.User{ID: "cashier1", Role: "cashier"})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// Wrong PIN: forbidden.
	if rec := makeReq("shift_id=" + shiftID + "&type=payout&amount=-500&reason=x&manager_pin=000000"); rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 with a wrong PIN, got %d: %s", rec.Code, rec.Body.String())
	}

	// Correct PIN: succeeds, and the MANAGER (not the cashier who submitted
	// the request) becomes the audit actor — same as PfandRueckgabe/refund.
	rec2 := makeReq("shift_id=" + shiftID + "&type=payout&amount=-500&reason=x&manager_pin=482913")
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 with a correct manager PIN, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var actorID string
	if err := dp.Db.QueryRowContext(ctx, `SELECT actor_id FROM audit_log WHERE action='cash_adjustment'`).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if actorID != "mgr1" {
		t.Fatalf("expected the manager as audit actor, got %q", actorID)
	}
}

// A blank manager_pin is the natural first mistake — the field can't be
// HTML-`required` (a positive adjustment must be allowed to submit it
// blank) — and must be rejected WITHOUT reaching auth.Service.
// AuthorizeManager, which would otherwise burn a failed-attempt count
// shared device-wide with keypad login (5 failures = 30s lockout). Proven
// here by exhausting that budget with blank submissions and then still
// succeeding immediately with the correct PIN.
func TestRecordCashAdjustment_BlankManagerPINRejectedWithoutBurningLockoutBudget(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, dp := newShiftsAPITestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	openReq := httptest.NewRequest(http.MethodPost, "/api/shifts/open", strings.NewReader(`{"register_id":"reg1","cashier_id":"cashier1","opening_cash":5000}`))
	openReq.Header.Set("Content-Type", "application/json")
	openRec := httptest.NewRecorder()
	mux.ServeHTTP(openRec, openReq)
	if openRec.Code != http.StatusOK {
		t.Fatalf("open shift: %d: %s", openRec.Code, openRec.Body.String())
	}
	var shiftID string
	if err := dp.Db.QueryRowContext(ctx, `SELECT id FROM shifts LIMIT 1`).Scan(&shiftID); err != nil {
		t.Fatal(err)
	}

	hash, err := auth.HashPIN("482913")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx,
		`INSERT INTO users(id,username,display_name,pin_hash,role,created_at) VALUES('mgr1','mgr1','Manager One',?,'manager',datetime('now'))`, hash); err != nil {
		t.Fatal(err)
	}

	makeReq := func(form string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/shifts/adjustment", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = auth.WithUser(req, auth.User{ID: "cashier1", Role: "cashier"})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// 6 blank-PIN submissions — one more than the device-wide 5-failure
	// lockout budget. Every one must be a plain 403, never 429.
	for i := 0; i < 6; i++ {
		rec := makeReq("shift_id=" + shiftID + "&type=payout&amount=-100&reason=x")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("blank PIN attempt %d: expected 403, got %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}

	// The correct PIN must still work immediately — proof the blank
	// attempts never touched the lockout counter.
	rec := makeReq("shift_id=" + shiftID + "&type=payout&amount=-100&reason=x&manager_pin=482913")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with the correct PIN after blank attempts, got %d: %s", rec.Code, rec.Body.String())
	}
}

// type=payout is, by definition, cash leaving the till — a positive
// amount there would write an audit row that lies about its own
// direction (adds cash while labelled a "payout"), the same class of
// integrity gap this change closes for the type/sign mismatch.
func TestRecordCashAdjustment_PositivePayoutAmountRejected(t *testing.T) {
	mux, dp := newShiftsAPITestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	openReq := httptest.NewRequest(http.MethodPost, "/api/shifts/open", strings.NewReader(`{"register_id":"reg1","cashier_id":"user1","opening_cash":5000}`))
	openReq.Header.Set("Content-Type", "application/json")
	openRec := httptest.NewRecorder()
	mux.ServeHTTP(openRec, openReq)
	if openRec.Code != http.StatusOK {
		t.Fatalf("open shift: %d: %s", openRec.Code, openRec.Body.String())
	}
	var shiftID string
	if err := dp.Db.QueryRowContext(ctx, `SELECT id FROM shifts LIMIT 1`).Scan(&shiftID); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/shifts/adjustment",
		strings.NewReader(`shift_id=`+shiftID+`&type=payout&amount=200&reason=x`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, auth.User{ID: "user1"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a positive payout amount, got %d: %s", rec.Code, rec.Body.String())
	}
}

// The error helpers wrap the message in raw HTML for non-JSON requests
// (respondAdjustmentError/respondShiftError/respondCloseError) — since
// shifts.html's new htmx:responseError handler now renders that body via
// innerHTML (rather than htmx silently dropping it, per the other fix in
// this same change), an unescaped message is a live HTML-injection sink
// the moment any caller threads user-influenced text into one — e.g.
// pos.OpenShift's "register %s already has an open shift: %s" error,
// which does echo the client-supplied register_id. Tested directly
// against the helper: it's the shared choke point every error path in
// this file writes through, regardless of which one happens to echo
// attacker-controlled text today.
func TestRespondAdjustmentError_EscapesHTMLInMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/shifts/adjustment", nil)
	respondAdjustmentError(rec, req, http.StatusBadRequest, `<img src=x onerror=alert(1)>`)
	body := rec.Body.String()
	if strings.Contains(body, "<img") {
		t.Fatalf("expected the message to be HTML-escaped, got %s", body)
	}
	if !strings.Contains(body, "&lt;img") {
		t.Fatalf("expected an escaped &lt;img&gt; in the body, got %s", body)
	}
}

func TestPfandRueckgabe_RecordsPayoutAndReducesExpectedCash(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
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

	req := httptest.NewRequest(http.MethodPost, "/api/shifts/pfandrueckgabe", strings.NewReader(`{"amount":500}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req = auth.WithUser(req, auth.User{ID: "user1"})
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	var reason string
	if err := dp.Db.QueryRowContext(ctx, `SELECT json_extract(data_json,'$.reason') FROM audit_log WHERE action='cash_adjustment'`).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "Pfandrückgabe" {
		t.Fatalf("expected the audit reason to be the fixed Pfandrückgabe constant, got %q", reason)
	}

	// A subsequent close must reflect the payout in expected cash:
	// 5000 (opening) - 500 (payout) = 4500.
	rec3 := postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"`+shiftID+`","closing_cash":4500}`)
	if rec3.Code != http.StatusOK {
		t.Fatalf("close shift: %d: %s", rec3.Code, rec3.Body.String())
	}
	if !strings.Contains(rec3.Body.String(), `"expected_cash":4500`) {
		t.Fatalf("expected expected_cash=4500 reflecting the payout, got %s", rec3.Body.String())
	}
}

func TestPfandRueckgabe_RequiresManagerPINWhenAuthEnabled(t *testing.T) {
	// UT_AUTH is unset in this test process, so auth.Disabled(...) is
	// false — the handler requires the manager-PIN gate, same as refund.
	t.Setenv("UT_AUTH", "")
	mux, dp := newShiftsAPITestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	rec := postShiftJSON(t, mux, "/api/shifts/open", `{"register_id":"reg1","cashier_id":"user1","opening_cash":5000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("open shift: %d: %s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/api/shifts/pfandrueckgabe", strings.NewReader("amount=500"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, auth.User{ID: "user1"})
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager PIN, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

// Same adjacent fix, same reasoning as
// TestRecordCashAdjustment_BlankManagerPINRejectedWithoutBurningLockoutBudget:
// index.html's pfand.modal.manager_pin field isn't HTML-`required` either.
func TestPfandRueckgabe_BlankManagerPINRejectedWithoutBurningLockoutBudget(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, dp := newShiftsAPITestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	openReq := httptest.NewRequest(http.MethodPost, "/api/shifts/open", strings.NewReader(`{"register_id":"reg1","cashier_id":"cashier1","opening_cash":5000}`))
	openReq.Header.Set("Content-Type", "application/json")
	openRec := httptest.NewRecorder()
	mux.ServeHTTP(openRec, openReq)
	if openRec.Code != http.StatusOK {
		t.Fatalf("open shift: %d: %s", openRec.Code, openRec.Body.String())
	}

	hash, err := auth.HashPIN("482913")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx,
		`INSERT INTO users(id,username,display_name,pin_hash,role,created_at) VALUES('mgr1','mgr1','Manager One',?,'manager',datetime('now'))`, hash); err != nil {
		t.Fatal(err)
	}

	makeReq := func(form string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/shifts/pfandrueckgabe", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = auth.WithUser(req, auth.User{ID: "cashier1", Role: "cashier"})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	for i := 0; i < 6; i++ {
		rec := makeReq("amount=500")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("blank PIN attempt %d: expected 403, got %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}

	rec := makeReq("amount=500&manager_pin=482913")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with the correct PIN after blank attempts, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPfandRueckgabe_WrongManagerPINForbiddenCorrectPINRecordsManagerAsActor(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, dp := newShiftsAPITestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	rec := postShiftJSON(t, mux, "/api/shifts/open", `{"register_id":"reg1","cashier_id":"cashier1","opening_cash":5000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("open shift: %d: %s", rec.Code, rec.Body.String())
	}

	hash, err := auth.HashPIN("482913")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx,
		`INSERT INTO users(id,username,display_name,pin_hash,role,created_at) VALUES('mgr1','mgr1','Manager One',?,'manager',datetime('now'))`, hash); err != nil {
		t.Fatal(err)
	}

	makeReq := func(form string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/shifts/pfandrueckgabe", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = auth.WithUser(req, auth.User{ID: "cashier1", Role: "cashier"})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// Wrong PIN: forbidden.
	if rec := makeReq("amount=500&manager_pin=000000"); rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 with a wrong PIN, got %d: %s", rec.Code, rec.Body.String())
	}

	// Correct PIN: succeeds, and the MANAGER (not the cashier who submitted
	// the request) becomes the audit actor — same as refund/inventory-override.
	rec2 := makeReq("amount=500&manager_pin=482913")
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 with a correct manager PIN, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var actorID string
	if err := dp.Db.QueryRowContext(ctx, `SELECT actor_id FROM audit_log WHERE action='cash_adjustment'`).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if actorID != "mgr1" {
		t.Fatalf("expected the manager as audit actor, got %q", actorID)
	}
}

func TestPfandRueckgabe_NoOpenShiftRejected(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newShiftsAPITestDeps(t)
	req := httptest.NewRequest(http.MethodPost, "/api/shifts/pfandrueckgabe", strings.NewReader("amount=500"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, auth.User{ID: "user1"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 with no open shift, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestPfandRueckgabe_TwoRegistersPaysOutOnThisTillsOwnShift is the
// ut-docs#268 acceptance-criteria regression test: on a two-register shop
// with two CONCURRENT open shifts, a Pfandrückgabe payout must land on the
// shift of THIS till's own register (till.register_id), not "whichever
// shift was opened most recently anywhere". Register B's shift is opened
// LAST on purpose — it is exactly the one the old CurrentOpenShift
// heuristic would pick, so this test fails against the old code.
func TestPfandRueckgabe_TwoRegistersPaysOutOnThisTillsOwnShift(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newShiftsAPITestDeps(t)
	ctx := context.Background()
	for _, ins := range []string{
		`INSERT INTO registers(id,name,is_active) VALUES('regA','Front Till',1)`,
		`INSERT INTO registers(id,name,is_active) VALUES('regB','Back Till',1)`,
	} {
		if _, err := dp.Db.ExecContext(ctx, ins); err != nil {
			t.Fatal(err)
		}
	}

	// Open register A's shift FIRST, then register B's — the most recent
	// open shift anywhere is B's.
	if rec := postShiftJSON(t, mux, "/api/shifts/open", `{"register_id":"regA","cashier_id":"user1","opening_cash":5000}`); rec.Code != http.StatusOK {
		t.Fatalf("open shift on regA: %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postShiftJSON(t, mux, "/api/shifts/open", `{"register_id":"regB","cashier_id":"user2","opening_cash":6000}`); rec.Code != http.StatusOK {
		t.Fatalf("open shift on regB: %d: %s", rec.Code, rec.Body.String())
	}
	// The stored opened_at is second-granular RFC3339, so two back-to-back
	// opens can tie; force B's to sort strictly last, the way a real later
	// opening would.
	if _, err := dp.Db.ExecContext(ctx, `UPDATE shifts SET opened_at='2099-01-01T09:00:00Z' WHERE register_id='regB'`); err != nil {
		t.Fatal(err)
	}
	var shiftA, shiftB string
	if err := dp.Db.QueryRowContext(ctx, `SELECT id FROM shifts WHERE register_id='regA'`).Scan(&shiftA); err != nil {
		t.Fatal(err)
	}
	if err := dp.Db.QueryRowContext(ctx, `SELECT id FROM shifts WHERE register_id='regB'`).Scan(&shiftB); err != nil {
		t.Fatal(err)
	}

	// THIS till is register A — the persistent identity ut-docs#268 adds.
	if err := dp.Settings.Set(ctx, pos.SettingsKeyTillRegisterID, "regA"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/shifts/pfandrueckgabe", strings.NewReader(`{"amount":500}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req = auth.WithUser(req, auth.User{ID: "user1"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// The payout must be recorded against register A's shift specifically.
	var gotShiftID string
	if err := dp.Db.QueryRowContext(ctx,
		`SELECT json_extract(data_json,'$.shift_id') FROM audit_log WHERE action='cash_adjustment'`).Scan(&gotShiftID); err != nil {
		t.Fatal(err)
	}
	if gotShiftID != shiftA {
		t.Fatalf("payout landed on shift %q, expected THIS till's own register A shift %q (register B's concurrent shift is %q)", gotShiftID, shiftA, shiftB)
	}

	// And the accounting agrees: A's expected cash reflects the payout
	// (5000-500), B's is untouched (6000).
	recA := postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"`+shiftA+`","closing_cash":4500}`)
	if recA.Code != http.StatusOK || !strings.Contains(recA.Body.String(), `"expected_cash":4500`) {
		t.Fatalf("expected regA close with expected_cash=4500, got %d: %s", recA.Code, recA.Body.String())
	}
	recB := postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"`+shiftB+`","closing_cash":6000}`)
	if recB.Code != http.StatusOK || !strings.Contains(recB.Body.String(), `"expected_cash":6000`) {
		t.Fatalf("expected regB close with expected_cash=6000 (untouched by the payout), got %d: %s", recB.Code, recB.Body.String())
	}
}

// A multi-register shop where this till has never been told which register
// it is: a write must fail loudly (409 pointing at Settings), never guess —
// the "no register identity yet" upgrade-path decision from ut-docs#268.
func TestPfandRueckgabe_AmbiguousRegisterIdentityConflicts(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newShiftsAPITestDeps(t)
	ctx := context.Background()
	for _, ins := range []string{
		`INSERT INTO registers(id,name,is_active) VALUES('regA','Front Till',1)`,
		`INSERT INTO registers(id,name,is_active) VALUES('regB','Back Till',1)`,
	} {
		if _, err := dp.Db.ExecContext(ctx, ins); err != nil {
			t.Fatal(err)
		}
	}
	if rec := postShiftJSON(t, mux, "/api/shifts/open", `{"register_id":"regA","cashier_id":"user1","opening_cash":5000}`); rec.Code != http.StatusOK {
		t.Fatalf("open shift on regA: %d: %s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/api/shifts/pfandrueckgabe", strings.NewReader(`{"amount":500}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req = auth.WithUser(req, auth.User{ID: "user1"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for an unset register identity on a multi-register shop, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Settings") {
		t.Fatalf("expected the error to point staff at Settings, got %s", rec.Body.String())
	}
	// Nothing recorded.
	var n int
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE action='cash_adjustment'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected no cash adjustment recorded, got %d", n)
	}
}

func TestPfandRueckgabe_ValidationErrors(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newShiftsAPITestDeps(t)
	makeReq := func(form string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/shifts/pfandrueckgabe", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = auth.WithUser(req, auth.User{ID: "user1"})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := makeReq("amount=0"); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a zero amount, got %d", rec.Code)
	}
	if rec := makeReq("amount=-500"); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a negative amount, got %d", rec.Code)
	}

	// Empty actor id: unauthorized.
	req := httptest.NewRequest(http.MethodPost, "/api/shifts/pfandrueckgabe", strings.NewReader("amount=500"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, auth.User{ID: ""})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for an empty actor id, got %d: %s", rec.Code, rec.Body.String())
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

	// Same validation error, but with Accept: application/json -- covers
	// respondAdjustmentError's JSON branch specifically: { "data": null,
	// "error": "…" }, not the bare struct it used to write (ut-docs#378).
	jsonReq := httptest.NewRequest(http.MethodPost, "/api/shifts/adjustment", strings.NewReader("shift_id=s1&type=payout&amount=-100"))
	jsonReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	jsonReq.Header.Set("Accept", "application/json")
	jsonReq = auth.WithUser(jsonReq, auth.User{ID: "user1"})
	jsonRec := httptest.NewRecorder()
	mux.ServeHTTP(jsonRec, jsonReq)
	if jsonRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a missing reason, got %d: %s", jsonRec.Code, jsonRec.Body.String())
	}
	if data, hasData, errVal, hasError := envelopeOf(t, jsonRec.Body.Bytes()); !hasData || string(data) != "null" || !hasError || string(errVal) == "null" {
		t.Fatalf(`expected { "data": null, "error": "…" }, got %s`, jsonRec.Body.String())
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
