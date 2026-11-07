package pages

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#2558: POST /api/pos/no-sale opens the cash drawer without a sale.
// It is gated exactly like a cash skim (cash_adjustment, manager PIN via
// checkOrElevate), kicks the drawer FIRST, and records the open (event row
// + audit row) only once the printer accepted the kick.

// newNoSaleTestDeps is the shifts API harness (real migrated DB, real
// auth.Service) with the no-sale route registered, cashier1/mgr1 seeded,
// this till's register pinned to reg-ns and — unless printerMode is "" —
// a thermal "device" printer pointed at a plain temp file, so the bytes
// that would reach the printer can be read back.
func newNoSaleTestDeps(t *testing.T, printerMode string) (*http.ServeMux, *common.Deps, string) {
	t.Helper()
	t.Setenv("UT_AUTH", "")
	mux, dp := newShiftsAPITestDeps(t)
	registerNoSaleAPI(mux, dp)
	seed3134Users(t, dp.Db)
	if _, err := dp.Db.Exec(`INSERT INTO registers (id, name) VALUES ('reg-ns', 'No-sale till')`); err != nil {
		t.Fatalf("seed register: %v", err)
	}
	setSettings(t, dp, map[string]string{pos.SettingsKeyTillRegisterID: "reg-ns"})
	dev := filepath.Join(t.TempDir(), "lp0")
	if err := os.WriteFile(dev, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	switch printerMode {
	case "device":
		setSettings(t, dp, map[string]string{"printer.mode": "device", "printer.device": dev, "printer.drawer_pin": "5"})
	case "broken":
		// A thermal printer whose device path does not exist: the kick fails.
		setSettings(t, dp, map[string]string{"printer.mode": "device", "printer.device": filepath.Join(t.TempDir(), "gone", "lp0")})
	case "system":
		setSettings(t, dp, map[string]string{"printer.mode": "system"})
	}
	return mux, dp, dev
}

func postNoSale(t *testing.T, mux *http.ServeMux, u auth.User, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pos/no-sale", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = auth.WithUser(req, u)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

type noSaleRow struct {
	id, register, till, actor, approver, reason string
}

func noSaleRows(t *testing.T, db *sql.DB) []noSaleRow {
	t.Helper()
	rows, err := db.Query(`SELECT id, COALESCE(register_id,''), COALESCE(till_id,''), COALESCE(actor_id,''), COALESCE(approver_id,''), COALESCE(reason,'')
FROM no_sale_events ORDER BY created_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []noSaleRow
	for rows.Next() {
		var r noSaleRow
		if err := rows.Scan(&r.id, &r.register, &r.till, &r.actor, &r.approver, &r.reason); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func noSaleAudit(t *testing.T, db *sql.DB) []auditRow3134 {
	t.Helper()
	return audit3134(t, db, `action='no_sale'`)
}

func decodeNoSaleEnvelope(t *testing.T, rec *httptest.ResponseRecorder) (id string, errCode string) {
	t.Helper()
	var env struct {
		Data *struct {
			ID string `json:"id"`
		} `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not the JSON envelope: %v (%q)", err, rec.Body.String())
	}
	if env.Data != nil {
		id = env.Data.ID
	}
	if env.Error != nil {
		errCode = env.Error.Code
	}
	return id, errCode
}

func assertNothingRecorded(t *testing.T, dp *common.Deps) {
	t.Helper()
	if rows := noSaleRows(t, dp.Db); len(rows) != 0 {
		t.Fatalf("no no_sale_events row may be written, got %+v", rows)
	}
	if rows := noSaleAudit(t, dp.Db); len(rows) != 0 {
		t.Fatalf("no no_sale audit row may be written, got %+v", rows)
	}
}

func TestNoSale_ManagerOpensDrawerAndRecordsEventAndAudit(t *testing.T) {
	mux, dp, dev := newNoSaleTestDeps(t, "device")

	rec := postNoSale(t, mux, manager3134, `{"reason":"  change for the float  "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %q", rec.Code, rec.Body.String())
	}
	id, code := decodeNoSaleEnvelope(t, rec)
	if id == "" || code != "" {
		t.Fatalf("want data.id and error null, got id %q error %q (%s)", id, code, rec.Body.String())
	}
	got, err := os.ReadFile(dev)
	if err != nil {
		t.Fatal(err)
	}
	if want := print.DrawerKickBytes(5); !bytes.Equal(got, want) {
		t.Fatalf("printer received % x, want only the pin-5 kick % x", got, want)
	}
	rows := noSaleRows(t, dp.Db)
	want := noSaleRow{id: id, register: "reg-ns", actor: "mgr1", reason: "change for the float"}
	if len(rows) != 1 || rows[0] != want {
		t.Fatalf("no_sale_events = %+v, want [%+v]", rows, want)
	}
	if a := noSaleAudit(t, dp.Db); len(a) != 1 || a[0].actor != "mgr1" || a[0].blocked != "" {
		t.Fatalf("want one no_sale audit row actor=mgr1 (not elevated), got %+v", a)
	}
}

func TestNoSale_CashierWithoutGrantNeedsPIN403RecordsNothing(t *testing.T) {
	mux, dp, dev := newNoSaleTestDeps(t, "device")

	rec := postNoSale(t, mux, cashier3134, `{}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d %q", rec.Code, rec.Body.String())
	}
	if _, code := decodeNoSaleEnvelope(t, rec); code != "manager_pin_required" {
		t.Fatalf("error code = %q, want manager_pin_required", code)
	}
	if got, _ := os.ReadFile(dev); len(got) != 0 {
		t.Fatalf("a refused no-sale must not kick the drawer, printer got % x", got)
	}
	assertNothingRecorded(t, dp)

	// A wrong PIN is refused the same way.
	rec = postNoSale(t, mux, cashier3134, `{"manager_pin":"000000"}`)
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusTooManyRequests {
		t.Fatalf("wrong PIN: want 403/429, got %d %q", rec.Code, rec.Body.String())
	}
	assertNothingRecorded(t, dp)
}

func TestNoSale_CashierWithManagerPINElevatesAndRecordsBoth(t *testing.T) {
	mux, dp, _ := newNoSaleTestDeps(t, "device")

	rec := postNoSale(t, mux, cashier3134, `{"manager_pin":"`+perm3134MgrPIN+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 with a manager PIN, got %d %q", rec.Code, rec.Body.String())
	}
	rows := noSaleRows(t, dp.Db)
	if len(rows) != 1 || rows[0].actor != "cashier1" || rows[0].approver != "mgr1" || rows[0].register != "reg-ns" {
		t.Fatalf("want one event actor=cashier1 approver=mgr1 register=reg-ns, got %+v", rows)
	}
	if a := noSaleAudit(t, dp.Db); len(a) != 1 || a[0].actor != "mgr1" || a[0].blocked != "cashier1" {
		t.Fatalf("want one elevated audit row actor=mgr1 blocked=cashier1, got %+v", a)
	}
}

func TestNoSale_CashierGrantedCashAdjustmentNeedsNoPIN(t *testing.T) {
	mux, dp, _ := newNoSaleTestDeps(t, "device")
	set3134Grant(t, dp.Db, "cashier", "cash_adjustment", 1)

	rec := postNoSale(t, mux, cashier3134, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %q", rec.Code, rec.Body.String())
	}
	if rows := noSaleRows(t, dp.Db); len(rows) != 1 || rows[0].actor != "cashier1" || rows[0].approver != "" {
		t.Fatalf("want one event actor=cashier1 with no approver, got %+v", rows)
	}
}

func TestNoSale_KickFailureRecordsNothing(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		wantCode int
		wantErr  string
	}{
		{"", http.StatusConflict, "no_drawer_printer"},
		{"system", http.StatusConflict, "drawer_unsupported"},
		{"broken", http.StatusServiceUnavailable, "drawer_kick_failed"},
	} {
		t.Run("mode="+tc.mode, func(t *testing.T) {
			mux, dp, _ := newNoSaleTestDeps(t, tc.mode)
			rec := postNoSale(t, mux, manager3134, `{}`)
			if rec.Code != tc.wantCode {
				t.Fatalf("want %d, got %d %q", tc.wantCode, rec.Code, rec.Body.String())
			}
			if _, code := decodeNoSaleEnvelope(t, rec); code != tc.wantErr {
				t.Fatalf("error code = %q, want %q", code, tc.wantErr)
			}
			assertNothingRecorded(t, dp)
		})
	}
}

func TestNoSale_BadInput400RecordsNothing(t *testing.T) {
	for _, tc := range []struct {
		name, body, wantErr string
	}{
		{"reason too long", `{"reason":"` + strings.Repeat("x", noSaleReasonMaxRunes+1) + `"}`, "reason_too_long"},
		{"malformed json", `{"reason":`, "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, dp, dev := newNoSaleTestDeps(t, "device")
			rec := postNoSale(t, mux, manager3134, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d %q", rec.Code, rec.Body.String())
			}
			if _, code := decodeNoSaleEnvelope(t, rec); code != tc.wantErr {
				t.Fatalf("error code = %q, want %q", code, tc.wantErr)
			}
			if got, _ := os.ReadFile(dev); len(got) != 0 {
				t.Fatalf("invalid input must not kick the drawer, printer got % x", got)
			}
			assertNothingRecorded(t, dp)
		})
	}
	// Exactly the cap is fine (runes, not bytes: multi-byte text counts once per character).
	mux, dp, _ := newNoSaleTestDeps(t, "device")
	if rec := postNoSale(t, mux, manager3134, `{"reason":"`+strings.Repeat("é", noSaleReasonMaxRunes)+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("a %d-character reason must be accepted, got %d %q", noSaleReasonMaxRunes, rec.Code, rec.Body.String())
	}
	if rows := noSaleRows(t, dp.Db); len(rows) != 1 {
		t.Fatalf("want one event, got %+v", rows)
	}
}

func TestNoSale_GetIsNotAllowed(t *testing.T) {
	mux, dp, _ := newNoSaleTestDeps(t, "device")
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/api/pos/no-sale", nil), manager3134)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: want 405, got %d", rec.Code)
	}
	assertNothingRecorded(t, dp)
}

// An empty body is an empty request (review nit, ut-docs#2558): the UI may
// POST with no payload when no PIN or reason is needed.
func TestNoSale_EmptyBodyIsAnEmptyRequest(t *testing.T) {
	mux, dp, _ := newNoSaleTestDeps(t, "device")

	rec := postNoSale(t, mux, manager3134, ``)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 for an empty body, got %d %q", rec.Code, rec.Body.String())
	}
	if rows := noSaleRows(t, dp.Db); len(rows) != 1 || rows[0].reason != "" {
		t.Fatalf("want one no_sale_events row with no reason, got %+v", rows)
	}
}

// ut-docs#3560: the sale-screen No Sale button posts the till's shared
// utPostWithElevation shape (application/x-www-form-urlencoded). A denial is
// answered with the elevation-prompt HTML fragment; every other outcome stays
// the JSON envelope. A JSON caller keeps the JSON 403.

func postNoSaleForm(t *testing.T, mux *http.ServeMux, u auth.User, vals url.Values, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pos/no-sale", strings.NewReader(vals.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	req = auth.WithUser(req, u)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func requireNoSalePrompt(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 prompt, got %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	if got := rec.Header().Get("X-UT-Response"); got != "elevation-prompt" {
		t.Fatalf("X-UT-Response = %q, want elevation-prompt", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="elevation-modal"`) || !strings.Contains(body, `hx-post="/api/pos/no-sale"`) {
		t.Fatalf("prompt must carry #elevation-modal with hx-post=/api/pos/no-sale, got %q", body)
	}
	return body
}

func TestNoSale_FormCashierNoPINRendersElevationPrompt(t *testing.T) {
	mux, dp, dev := newNoSaleTestDeps(t, "device")
	rec := postNoSaleForm(t, mux, cashier3134, url.Values{}, nil)
	body := requireNoSalePrompt(t, rec)
	if !strings.Contains(body, "Open the cash drawer without a sale") {
		t.Fatalf("prompt must summarise the action, got %q", body)
	}
	if got, _ := os.ReadFile(dev); len(got) != 0 {
		t.Fatalf("a prompt must not kick the drawer, printer got % x", got)
	}
	assertNothingRecorded(t, dp)
}

func TestNoSale_FormWrongPINRePromptsWithError(t *testing.T) {
	mux, dp, _ := newNoSaleTestDeps(t, "device")
	rec := postNoSaleForm(t, mux, cashier3134, url.Values{"override_pin": {"000000"}}, nil)
	body := requireNoSalePrompt(t, rec)
	if !strings.Contains(body, "Invalid PIN") {
		t.Fatalf("want the invalid-PIN text, got %q", body)
	}
	assertNothingRecorded(t, dp)

	// Past the device-wide threshold the prompt shows the lockout text, and
	// even the correct PIN is refused.
	for i := 0; i < 6; i++ {
		rec = postNoSaleForm(t, mux, cashier3134, url.Values{"override_pin": {"000000"}}, nil)
	}
	rec = postNoSaleForm(t, mux, cashier3134, url.Values{"override_pin": {perm3134MgrPIN}}, nil)
	body = requireNoSalePrompt(t, rec)
	if !strings.Contains(body, "Too many attempts") {
		t.Fatalf("want the locked-out text, got %q", body)
	}
	assertNothingRecorded(t, dp)
}

func TestNoSale_FormManagerPINElevatesReturnsJSON(t *testing.T) {
	mux, dp, _ := newNoSaleTestDeps(t, "device")
	rec := postNoSaleForm(t, mux, cashier3134, url.Values{"override_pin": {perm3134MgrPIN}}, nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("want 200 JSON, got %d %q (%q)", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	if id, code := decodeNoSaleEnvelope(t, rec); id == "" || code != "" {
		t.Fatalf("want data.id, got id %q error %q", id, code)
	}
	rows := noSaleRows(t, dp.Db)
	if len(rows) != 1 || rows[0].actor != "cashier1" || rows[0].approver != "mgr1" {
		t.Fatalf("want one event actor=cashier1 approver=mgr1, got %+v", rows)
	}
	if a := noSaleAudit(t, dp.Db); len(a) != 1 || a[0].actor != "mgr1" || a[0].blocked != "cashier1" {
		t.Fatalf("want one elevated audit row actor=mgr1 blocked=cashier1, got %+v", a)
	}
}

func TestNoSale_FormPermittedReturnsJSONNoPrompt(t *testing.T) {
	mux, dp, _ := newNoSaleTestDeps(t, "device")
	rec := postNoSaleForm(t, mux, manager3134, url.Values{}, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("X-UT-Response") != "" {
		t.Fatalf("want 200 JSON with no prompt header, got %d %q", rec.Code, rec.Header().Get("X-UT-Response"))
	}
	if id, _ := decodeNoSaleEnvelope(t, rec); id == "" {
		t.Fatalf("want data.id, got %q", rec.Body.String())
	}
	if rows := noSaleRows(t, dp.Db); len(rows) != 1 || rows[0].approver != "" {
		t.Fatalf("want one un-elevated event, got %+v", rows)
	}
}

func TestNoSale_FormErrorsStayJSON(t *testing.T) {
	mux, dp, _ := newNoSaleTestDeps(t, "")
	rec := postNoSaleForm(t, mux, manager3134, url.Values{}, nil)
	if rec.Code != http.StatusConflict || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("want 409 JSON, got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if _, code := decodeNoSaleEnvelope(t, rec); code != "no_drawer_printer" {
		t.Fatalf("error code = %q, want no_drawer_printer", code)
	}
	assertNothingRecorded(t, dp)
}

func TestNoSale_FormCrossOriginRefusedBeforePIN(t *testing.T) {
	mux, dp, dev := newNoSaleTestDeps(t, "device")
	// More wrong-PIN cross-site posts than the lockout threshold: none may
	// reach AuthorizeManager.
	for i := 0; i < 8; i++ {
		rec := postNoSaleForm(t, mux, cashier3134, url.Values{"override_pin": {"000000"}}, map[string]string{"Sec-Fetch-Site": "cross-site"})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: want 403, got %d %q", i, rec.Code, rec.Body.String())
		}
		if _, code := decodeNoSaleEnvelope(t, rec); code != "cross_origin" {
			t.Fatalf("attempt %d: error code = %q, want cross_origin", i, code)
		}
	}
	rec := postNoSaleForm(t, mux, cashier3134, url.Values{"override_pin": {perm3134MgrPIN}}, map[string]string{"Origin": "https://evil.example"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("valid PIN cross-origin: want 403, got %d", rec.Code)
	}
	if got, _ := os.ReadFile(dev); len(got) != 0 {
		t.Fatalf("cross-origin must not kick the drawer, printer got % x", got)
	}
	assertNothingRecorded(t, dp)
	// The lockout counter did not move: a same-origin valid PIN still works.
	if rec := postNoSaleForm(t, mux, cashier3134, url.Values{"override_pin": {perm3134MgrPIN}}, nil); rec.Code != http.StatusOK || rec.Header().Get("X-UT-Response") != "" {
		t.Fatalf("same-origin valid PIN after cross-origin probing: want 200 JSON, got %d %q", rec.Code, rec.Body.String())
	}
}

func TestNoSale_JSONContractUnchanged(t *testing.T) {
	mux, _, _ := newNoSaleTestDeps(t, "device")
	rec := postNoSale(t, mux, cashier3134, ``)
	if rec.Code != http.StatusForbidden || rec.Header().Get("X-UT-Response") != "" {
		t.Fatalf("JSON cashier without PIN: want 403 JSON, got %d %q", rec.Code, rec.Header().Get("X-UT-Response"))
	}
	if _, code := decodeNoSaleEnvelope(t, rec); code != "manager_pin_required" {
		t.Fatalf("error code = %q, want manager_pin_required", code)
	}
}

func TestNoSale_MultipartIsInvalidRequest(t *testing.T) {
	mux, dp, _ := newNoSaleTestDeps(t, "device")
	req := httptest.NewRequest(http.MethodPost, "/api/pos/no-sale", strings.NewReader("--b\r\nContent-Disposition: form-data; name=\"override_pin\"\r\n\r\n1\r\n--b--\r\n"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=b")
	req = auth.WithUser(req, manager3134)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d %q", rec.Code, rec.Body.String())
	}
	assertNothingRecorded(t, dp)
}
