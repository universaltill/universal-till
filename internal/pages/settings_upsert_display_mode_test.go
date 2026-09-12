package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/ui"
)

// ut-docs#2121: POST /api/settings/upsert (the generic key/value editor)
// didn't get the same live side effects as the dedicated
// POST /api/settings/display-mode handler when the posted key is
// "display.mode" — same class of bug ut-docs#2099's review found and fixed
// in newRederiveSettings (the cloud set_setting/replica-drift path), just
// via a different generic-editor door. This drives the fix through the
// generic upsert endpoint and asserts httpx's live "selforder" template
// flag (record_dialog.html's status/lock/exit-to-OS withholding,
// coding-standards.md §10) actually flips immediately, the same assertion
// shape categories_page_test.go's TestCategoriesPage_RecordDialogStatusLockExitAffordance
// uses for the dedicated handler's own equivalent effect.
func TestSettingsUpsertDisplayMode_UpdatesLiveKioskFlag(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	dp.BtnStore = ui.NewButtonStore(d.DB)
	dp.Menu = []common.MenuItem{{Href: "/", Label: "Home"}}

	mux := http.NewServeMux()
	registerSettings(mux, dp)
	registerCategories(mux, dp)

	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	// httpx.selfOrderMode is process-global (mirrors categories_page_test.go's
	// own TestCategoriesPage_RecordDialogStatusLockExitAffordance setup) — set
	// the known baseline explicitly rather than relying on some other test in
	// the package happening to have left it false, or a `-run` filter that
	// skips whichever test would have reset it makes this one spuriously fail.
	httpx.InitSelfOrderMode(false)
	t.Cleanup(func() { httpx.InitSelfOrderMode(false) })

	dialogHTML := func(t *testing.T) string {
		t.Helper()
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/categories", nil), manager)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /categories: %d %s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		start := strings.Index(body, `<dialog id="category-dialog"`)
		if start < 0 {
			t.Fatalf("page has no #category-dialog:\n%s", body)
		}
		end := strings.Index(body[start:], `</dialog>`)
		if end < 0 {
			t.Fatalf("dialog is not closed")
		}
		return body[start : start+end]
	}

	// Baseline: not in self-order mode, the affordance is present.
	if dlg := dialogHTML(t); !strings.Contains(dlg, `data-record-dialog-lock`) {
		t.Fatalf("register-mode dialog is missing the lock affordance before the upsert:\n%s", dlg)
	}

	upsert := func(t *testing.T, value string) {
		t.Helper()
		req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/settings/upsert",
			strings.NewReader(url.Values{"key": {"display.mode"}, "value": {value}}.Encode())), manager)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
			t.Fatalf("upsert display.mode=%s: %d %s", value, rec.Code, rec.Body.String())
		}
	}

	// The bug: without httpx.InitSelfOrderMode being called from this path,
	// the dialog would still show the lock/exit affordance here, staying
	// stale until the process restarted (exactly ut-docs#2099's B1 finding,
	// just reached through the generic editor instead of the cloud path).
	upsert(t, "self_order")
	if dlg := dialogHTML(t); strings.Contains(dlg, `data-record-dialog-lock`) {
		t.Fatalf("self_order-mode dialog must NOT render the lock affordance immediately after POST /api/settings/upsert (customer containment):\n%s", dlg)
	}

	// And it flips back live too, same unconditional shape as the dedicated
	// handler's own InitSelfOrderMode(rawMode == "self_order") call.
	upsert(t, "backoffice")
	if dlg := dialogHTML(t); !strings.Contains(dlg, `data-record-dialog-lock`) {
		t.Fatalf("leaving self_order via upsert must restore the lock affordance immediately:\n%s", dlg)
	}
}

// ut-docs#2121 / ut-docs#1259: the generic upsert door must not be a side
// door around the session-revoke-on-self_order-entry protection the
// dedicated POST /api/settings/display-mode handler already has — the acting
// session must not survive a switch into the customer-facing, auth-exempt
// self-order surface no matter which handler made the switch. Mirrors
// TestSelfOrderMode_RevokesActingSessionOnEntry, but drives the switch
// through /api/settings/upsert instead of the dedicated endpoint.
func TestSettingsUpsertDisplayMode_RevokesActingSessionOnSelfOrderEntry(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	// This test drives the till into self_order mode, which flips
	// httpx's process-global flag (see the sibling test's own comment) —
	// reset it so a later test in the package doesn't inherit self_order=true.
	t.Cleanup(func() { httpx.InitSelfOrderMode(false) })
	svc := auth.NewService(d.DB)
	mgrID, err := svc.Repo().CreateUser(t.Context(), "mgr-upsert", "Manager Upsert", "manager")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPIN("9997")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Repo().SetUserPIN(t.Context(), mgrID, hash); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerIndex(mux, dp)
	registerSettings(mux, dp)
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	registerAuth(mux, dp, svc)
	h := auth.Middleware(mux, svc)

	loginRec := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(url.Values{"pin": {"9997"}}.Encode()))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(loginRec, loginReq)
	var cookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == auth.CookieName {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value == "" {
		t.Fatal("setup: manager login did not set a session cookie")
	}

	setRec := httptest.NewRecorder()
	setReq := httptest.NewRequest(http.MethodPost, "/api/settings/upsert",
		strings.NewReader(url.Values{"key": {"display.mode"}, "value": {"self_order"}}.Encode()))
	setReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setReq.AddCookie(cookie)
	h.ServeHTTP(setRec, setReq)
	if setRec.Code != http.StatusOK && setRec.Code != http.StatusNoContent {
		t.Fatalf("manager upsert display.mode=self_order: %d %s", setRec.Code, setRec.Body.String())
	}

	var cleared *http.Cookie
	for _, c := range setRec.Result().Cookies() {
		if c.Name == auth.CookieName {
			cleared = c
		}
	}
	if cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("switching to self_order via upsert must clear the session cookie in the response, got %+v", cleared)
	}

	if _, ok := svc.Resolve(t.Context(), cookie.Value); ok {
		t.Fatal("session token must be revoked server-side once the till enters self_order mode via the generic upsert path")
	}

	if got := auditCount(t, d.DB, "self_order_session_revoked"); got != 1 {
		t.Fatalf("self_order_session_revoked audit rows = %d, want 1 (actor %s)", got, mgrID)
	}
}
