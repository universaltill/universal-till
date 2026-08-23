package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
)

// Positive counterpart to TestImport_ManagerGate and
// TestImportDispatch_RequiresManager (ut-docs#713 — canPerform()/
// Auth.Can() against the new import_export action, not just the no-session
// short-circuit those tests exercise). A real cashier session must still be
// denied by the actual permission check; a real manager session must get
// PAST the auth gate. Mirrors ut-docs#707's
// TestDataManagementEndpoints_RealSessionGatesByRole in scope and intent.
func TestImportExportEndpoints_RealSessionGatesByRole(t *testing.T) {
	dp := newImportTestDeps(t)
	dp.AuthSvc = auth.NewService(dp.Db) // seedForPages-equivalent: appdb.Open already ran migration 045
	mux := http.NewServeMux()
	registerImport(mux, dp)
	registerImportDispatch(mux, dp)

	cashier := auth.User{ID: "c1", Role: "cashier"}
	for _, role := range []string{"manager", "admin", "super_admin"} {
		mgr := auth.User{ID: "u-" + role, Role: role}

		t.Run("GET /import "+role, func(t *testing.T) {
			req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/import", nil), mgr)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s = %d, want 200 past the gate: %s", role, rec.Code, rec.Body.String())
			}
		})
		t.Run("GET /api/catalog/export "+role, func(t *testing.T) {
			req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/api/catalog/export", nil), mgr)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code == http.StatusForbidden {
				t.Fatalf("%s = 403, want past the auth gate", role)
			}
		})
		t.Run("POST /api/catalog/export-save "+role, func(t *testing.T) {
			req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/catalog/export-save", nil), mgr)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			// This endpoint never sets a non-200 status (see its own source) —
			// the forbidden-vs-allowed distinction lives in the response body
			// (ut-docs#238: a pos-notice error, not the old ad-hoc error span).
			if strings.Contains(rec.Body.String(), `pos-notice error`) {
				t.Fatalf("%s got the forbidden error notice, want past the auth gate: %s", role, rec.Body.String())
			}
		})
		t.Run("POST /api/import "+role, func(t *testing.T) {
			body, ct := multipartCSV(t, importCSV, map[string]string{"commit": "0"})
			req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/import", body), mgr)
			req.Header.Set("Content-Type", ct)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code == http.StatusForbidden {
				t.Fatalf("%s = 403, want past the auth gate: %s", role, rec.Body.String())
			}
		})
		t.Run("POST /api/data/import "+role, func(t *testing.T) {
			req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/data/import", nil), mgr)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code == http.StatusForbidden {
				t.Fatalf("%s = 403, want past the auth gate: %s", role, rec.Body.String())
			}
		})
	}

	// cashier stays denied on every site.
	cashierCases := []struct{ name, method, path string }{
		{"GET /import", http.MethodGet, "/import"},
		{"GET /api/catalog/export", http.MethodGet, "/api/catalog/export"},
		{"POST /api/import", http.MethodPost, "/api/import"},
	}
	for _, tc := range cashierCases {
		t.Run(tc.name+"/cashier_denied", func(t *testing.T) {
			req := auth.WithUser(httptest.NewRequest(tc.method, tc.path, nil), cashier)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if tc.path == "/import" {
				if rec.Code != http.StatusSeeOther {
					t.Fatalf("%s cashier = %d, want 303 redirect to /catalog", tc.path, rec.Code)
				}
				return
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s cashier = %d, want 403", tc.path, rec.Code)
			}
		})
	}
	t.Run("POST /api/catalog/export-save/cashier_denied", func(t *testing.T) {
		req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/catalog/export-save", nil), cashier)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if !strings.Contains(rec.Body.String(), `pos-notice error`) {
			t.Fatalf("cashier export-save: expected the forbidden error notice, got: %s", rec.Body.String())
		}
	})
	t.Run("POST /api/data/import/cashier_denied", func(t *testing.T) {
		req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/data/import", nil), cashier)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("cashier /api/data/import = %d, want 403: %s", rec.Code, rec.Body.String())
		}
	})
}

// Positive counterpart to TestAskAPI_RequiresManagerWhenConfigured — a real
// cashier session is denied by canPerform(d, r, "reports"), a real manager
// session gets past the gate (ut-docs#713).
func TestAskAPI_RealSessionGatesByRole(t *testing.T) {
	mux, dp, _ := newAskAPITestDeps(t)
	dp.AuthSvc = auth.NewService(dp.Db)
	dp.AI = fakeAskServer(t, "irrelevant", http.StatusOK)

	ask := func(u auth.User) *httptest.ResponseRecorder {
		req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/reports/ask", strings.NewReader("question=how did we do today?")), u)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := ask(auth.User{ID: "c1", Role: "cashier"}); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	for _, role := range []string{"manager", "admin", "super_admin"} {
		if rec := ask(auth.User{ID: "u-" + role, Role: role}); rec.Code == http.StatusForbidden {
			t.Fatalf("%s = 403, want past the auth gate: %s", role, rec.Body.String())
		}
	}
}

// Positive counterpart to TestMenuPage_ManagerOnlyTilesGatedByRole, which
// only exercises the no-session/UT_AUTH=off pair — a real cashier session
// must not see the manager-only tiles either, and manager/admin/super_admin
// sessions must (ut-docs#713, canPerform(d, r, "settings")). super_admin in
// particular is the case that would silently regress back to
// isManagerOrAuthOff (which only recognizes manager/admin, per #555) without
// this covering it explicitly.
func TestMenuPage_ManagerOnlyTilesRealSessionGatedByRole(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, nil)
	dp.AuthSvc = auth.NewService(dp.Db)

	get := func(u auth.User) *httptest.ResponseRecorder {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/menu", nil), u)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	cashierBody := get(auth.User{ID: "c1", Role: "cashier"}).Body.String()
	if strings.Contains(cashierBody, `href="/users"`) {
		t.Fatalf("cashier should not see the /users tile, got: %s", cashierBody)
	}

	for _, role := range []string{"manager", "admin", "super_admin"} {
		body := get(auth.User{ID: "u-" + role, Role: role}).Body.String()
		if !strings.Contains(body, `href="/users"`) || !strings.Contains(body, `href="/report-issue"`) {
			t.Fatalf("%s should see the manager-only tiles, got: %s", role, body)
		}
	}
}
