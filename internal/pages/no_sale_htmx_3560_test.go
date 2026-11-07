package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#3560: the sale screen's No sale button posts htmx (an empty form,
// HX-Request: true) to the same URL the JSON API serves. The answer is always
// HTTP 200 (htmx does not swap 4xx/5xx): either the shared elevation prompt
// or an inline notice fragment for #no-sale-status.

func postNoSaleHTMX(t *testing.T, mux *http.ServeMux, u auth.User, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pos/no-sale", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req = auth.WithUser(req, u)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func noSaleHTMXInit(t *testing.T) {
	t.Helper()
	chdirRoot(t)
	initPagesI18n(t)
}

func TestNoSaleHTMX_ManagerOpensDrawerNoPrompt(t *testing.T) {
	noSaleHTMXInit(t)
	mux, dp, dev := newNoSaleTestDeps(t, "device")
	rec := postNoSaleHTMX(t, mux, manager3134, url.Values{})
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %q", rec.Code, rec.Body.String())
	}
	// Not "ok": the elevation dialog would reload the page on it, wiping the notice.
	if got := rec.Header().Get("X-UT-Response"); got != "" {
		t.Fatalf("X-UT-Response = %q, want none on success", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Cash drawer opened.") {
		t.Fatalf("success notice missing: %s", body)
	}
	// The inner notice carries no role (the #no-sale-status container is the live region).
	if strings.Contains(body, `role=`) {
		t.Fatalf("notice must not carry a role attribute: %s", body)
	}
	if got, _ := os.ReadFile(dev); string(got) != string(print.DrawerKickBytes(5)) {
		t.Fatalf("printer got % x, want one pin-5 kick", got)
	}
	if rows := noSaleRows(t, dp.Db); len(rows) != 1 || rows[0].actor != "mgr1" || rows[0].approver != "" {
		t.Fatalf("want one event actor=mgr1 no approver, got %+v", rows)
	}
	if a := noSaleAudit(t, dp.Db); len(a) != 1 || a[0].actor != "mgr1" || a[0].blocked != "" {
		t.Fatalf("want one plain audit row, got %+v", a)
	}
}

func assertNoSalePrompt(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-UT-Response"); got != "elevation-prompt" {
		t.Fatalf("X-UT-Response = %q, want elevation-prompt (body %q)", got, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`id="elevation-modal"`, `hx-post="/api/pos/no-sale"`, `hx-target="#no-sale-status"`, "Open the cash drawer without a sale (No sale)."} {
		if !strings.Contains(body, want) {
			t.Fatalf("prompt missing %q: %s", want, body)
		}
	}
	return body
}

func TestNoSaleHTMX_CashierWithoutGrantGetsPromptNothingRecorded(t *testing.T) {
	noSaleHTMXInit(t)
	mux, dp, dev := newNoSaleTestDeps(t, "device")
	assertNoSalePrompt(t, postNoSaleHTMX(t, mux, cashier3134, url.Values{}))
	if got, _ := os.ReadFile(dev); len(got) != 0 {
		t.Fatalf("a prompt must not kick the drawer, got % x", got)
	}
	assertNothingRecorded(t, dp)
}

func TestNoSaleHTMX_CashierWithManagerPINElevates(t *testing.T) {
	noSaleHTMXInit(t)
	mux, dp, dev := newNoSaleTestDeps(t, "device")
	rec := postNoSaleHTMX(t, mux, cashier3134, url.Values{"override_pin": {perm3134MgrPIN}})
	if rec.Code != http.StatusOK || rec.Header().Get("X-UT-Response") != "" || !strings.Contains(rec.Body.String(), "Cash drawer opened.") {
		t.Fatalf("want ok notice, got %d %q %q", rec.Code, rec.Header().Get("X-UT-Response"), rec.Body.String())
	}
	if got, _ := os.ReadFile(dev); len(got) == 0 {
		t.Fatal("drawer was not kicked")
	}
	rows := noSaleRows(t, dp.Db)
	if len(rows) != 1 || rows[0].actor != "cashier1" || rows[0].approver != "mgr1" {
		t.Fatalf("want actor=cashier1 approver=mgr1, got %+v", rows)
	}
	if a := noSaleAudit(t, dp.Db); len(a) != 1 || a[0].actor != "mgr1" || a[0].blocked != "cashier1" {
		t.Fatalf("want elevated audit row, got %+v", a)
	}
}

func TestNoSaleHTMX_WrongPINAndLockout(t *testing.T) {
	noSaleHTMXInit(t)
	mux, dp, dev := newNoSaleTestDeps(t, "device")
	body := assertNoSalePrompt(t, postNoSaleHTMX(t, mux, cashier3134, url.Values{"override_pin": {"000000"}}))
	if !strings.Contains(body, httpx.T("en", "elevation.error_invalid_pin")) {
		t.Fatalf("wrong PIN: want invalid-PIN text in prompt: %s", body)
	}
	var last string
	for i := 0; i < 8; i++ {
		last = assertNoSalePrompt(t, postNoSaleHTMX(t, mux, cashier3134, url.Values{"override_pin": {"000000"}}))
	}
	if !strings.Contains(last, httpx.T("en", "elevation.error_locked_out")) {
		t.Fatalf("want lockout text after repeated bad PINs: %s", last)
	}
	if got, _ := os.ReadFile(dev); len(got) != 0 {
		t.Fatalf("no kick expected, got % x", got)
	}
	assertNothingRecorded(t, dp)
}

// AC4: cash_adjustment alone no longer opens the drawer.
func TestNoSale_CashAdjustmentAloneNoLongerOpens(t *testing.T) {
	noSaleHTMXInit(t)
	mux, dp, dev := newNoSaleTestDeps(t, "device")
	set3134Grant(t, dp.Db, "cashier", "cash_adjustment", 1)
	set3134Grant(t, dp.Db, "cashier", "no_sale", 0)

	rec := postNoSale(t, mux, cashier3134, `{}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("JSON: want 403, got %d %q", rec.Code, rec.Body.String())
	}
	if _, code := decodeNoSaleEnvelope(t, rec); code != "manager_pin_required" {
		t.Fatalf("code = %q", code)
	}
	assertNoSalePrompt(t, postNoSaleHTMX(t, mux, cashier3134, url.Values{}))
	if got, _ := os.ReadFile(dev); len(got) != 0 {
		t.Fatalf("no kick expected, got % x", got)
	}
	assertNothingRecorded(t, dp)
}

func TestNoSaleHTMX_FailureNoticesAre200AndRecordNothing(t *testing.T) {
	noSaleHTMXInit(t)
	for _, tc := range []struct {
		mode, want string
	}{
		{"", "No receipt printer is set up to open the cash drawer."},
		{"system", "This printer can&#39;t open a cash drawer."},
		{"broken", "The cash drawer could not be opened."},
	} {
		t.Run("mode="+tc.mode, func(t *testing.T) {
			mux, dp, _ := newNoSaleTestDeps(t, tc.mode)
			rec := postNoSaleHTMX(t, mux, manager3134, url.Values{})
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200 (htmx swaps only 2xx), got %d", rec.Code)
			}
			if got := rec.Header().Get("X-UT-Response"); got != "refused" {
				t.Fatalf("X-UT-Response = %q, want refused", got)
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("want %q in %s", tc.want, rec.Body.String())
			}
			assertNothingRecorded(t, dp)
		})
	}
}

func TestNoSaleHTMX_SettingsHintVsAskManager(t *testing.T) {
	noSaleHTMXInit(t)
	// Tester (ut-docs#3560, UX U3): the hint follows both no_drawer_printer
	// ("") and drawer_unsupported ("system").
	for _, mode := range []string{"", "system"} {
		t.Run("mode="+mode, func(t *testing.T) { noSaleSettingsHintCase(t, mode) })
	}
}

func noSaleSettingsHintCase(t *testing.T, mode string) {
	mux, dp, _ := newNoSaleTestDeps(t, mode)
	// manager holds settings -> link
	body := postNoSaleHTMX(t, mux, manager3134, url.Values{}).Body.String()
	if !strings.Contains(body, `href="/settings#settings-printer"`) || strings.Contains(body, "Ask a manager to check the printer settings.") {
		t.Fatalf("manager: want settings link only: %s", body)
	}
	// cashier holding no_sale but not settings -> plain text
	set3134Grant(t, dp.Db, "cashier", "no_sale", 1)
	body = postNoSaleHTMX(t, mux, cashier3134, url.Values{}).Body.String()
	if strings.Contains(body, `href="/settings#settings-printer"`) || !strings.Contains(body, "Ask a manager to check the printer settings.") {
		t.Fatalf("cashier: want ask-manager text only: %s", body)
	}
}

func TestNoSaleHTMX_JSONContentTypeStaysJSON(t *testing.T) {
	noSaleHTMXInit(t)
	mux, _, _ := newNoSaleTestDeps(t, "")
	req := httptest.NewRequest(http.MethodPost, "/api/pos/no-sale", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, auth.WithUser(req, manager3134))
	if rec.Code != http.StatusConflict {
		t.Fatalf("JSON body must keep the JSON contract (409), got %d %q", rec.Code, rec.Body.String())
	}
}

// AC9 (tester, ut-docs#3560): No sale is offline-first — the path is a
// settings read, the local printer transport and a local SQLite write. With
// every default-transport HTTP request failing (network down) the drawer
// still opens and the open is recorded, and nothing tried the network.
type noSaleNoNetwork struct{ calls int }

func (n *noSaleNoNetwork) RoundTrip(r *http.Request) (*http.Response, error) {
	n.calls++
	return nil, fmt.Errorf("network down (test): %s", r.URL)
}

func TestNoSaleHTMX_WorksWithNetworkDown(t *testing.T) {
	noSaleHTMXInit(t)
	mux, dp, dev := newNoSaleTestDeps(t, "device")
	nn := &noSaleNoNetwork{}
	orig := http.DefaultTransport
	http.DefaultTransport = nn
	t.Cleanup(func() { http.DefaultTransport = orig })

	rec := postNoSaleHTMX(t, mux, manager3134, url.Values{})
	if !strings.Contains(rec.Body.String(), "Cash drawer opened.") {
		t.Fatalf("want success offline, got %d %q", rec.Code, rec.Body.String())
	}
	if got, _ := os.ReadFile(dev); len(got) == 0 {
		t.Fatal("drawer not kicked offline")
	}
	if rows := noSaleRows(t, dp.Db); len(rows) != 1 {
		t.Fatalf("want one recorded open offline, got %+v", rows)
	}
	if nn.calls != 0 {
		t.Fatalf("no-sale made %d outbound HTTP call(s)", nn.calls)
	}
}
