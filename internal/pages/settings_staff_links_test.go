package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#3208: Settings → Staff & security links to the Users and
// Permissions pages, each link following its own page's gate
// (user_management / permission_management).
func settingsStaffLinksBody(t *testing.T, u auth.User) (int, string) {
	t.Helper()
	t.Setenv("UT_AUTH", "")
	mux, _, _ := newFullAuthDeps(t)
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), u)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestSettingsPage_StaffLinksManagerSeesUsersOnly(t *testing.T) {
	code, body := settingsStaffLinksBody(t, mgrUser)
	if code != http.StatusOK {
		t.Fatalf("manager GET /settings = %d", code)
	}
	if !strings.Contains(body, `id="settings-staff-links"`) || !strings.Contains(body, `href="/users"`) {
		t.Errorf("manager (user_management) must see the staff-links card with the Users link")
	}
	if strings.Contains(body, `href="/users/permissions"`) {
		t.Errorf("manager lacks permission_management: Permissions link must not render")
	}
	if !strings.Contains(body, `data-key="settings-staff-links"`) {
		t.Errorf("manager's sidebar index must list settings-staff-links")
	}
}

func TestSettingsPage_StaffLinksSuperAdminSeesBoth(t *testing.T) {
	code, body := settingsStaffLinksBody(t, auth.User{ID: "sa-1", Role: "super_admin"})
	if code != http.StatusOK {
		t.Fatalf("super_admin GET /settings = %d", code)
	}
	for _, want := range []string{`id="settings-staff-links"`, `href="/users"`, `href="/users/permissions"`} {
		if !strings.Contains(body, want) {
			t.Errorf("super_admin render missing %s", want)
		}
	}
}

// A cashier holds no "settings" grant, so this is only a 403 smoke test;
// the gate itself is covered by the two tests below (review, ut-docs#3208).
func TestSettingsPage_StaffLinksNeverReachCashier(t *testing.T) {
	code, body := settingsStaffLinksBody(t, cashUser)
	if code != http.StatusForbidden {
		t.Fatalf("cashier GET /settings = %d, want 403", code)
	}
	if strings.Contains(body, `id="settings-staff-links"`) {
		t.Errorf("cashier 403 page renders the staff-links card")
	}
}

// staffLinksBodyWithGrant renders /settings for u after setting one
// role-permission grant, so a role can open Settings without the two pages.
func staffLinksBodyWithGrant(t *testing.T, u auth.User, role, action string, granted bool) string {
	t.Helper()
	t.Setenv("UT_AUTH", "")
	mux, _, d := newFullAuthDeps(t)
	if err := data.NewAuthRepo(d.Db).SetRolePermission(t.Context(), nil, role, action, granted); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), u))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s GET /settings = %d, want 200", u.Role, rec.Code)
	}
	return rec.Body.String()
}

func TestSettingsPage_StaffLinksHiddenForSettingsOnlyRole(t *testing.T) {
	body := staffLinksBodyWithGrant(t, cashUser, "cashier", "settings", true)
	for _, bad := range []string{`id="settings-staff-links"`, `data-key="settings-staff-links"`, `href="/users"`, `href="/users/permissions"`} {
		if strings.Contains(body, bad) {
			t.Errorf("settings-only role leaks %q", bad)
		}
	}
}

func TestSettingsPage_StaffLinksHiddenWhenManagerLosesUserManagement(t *testing.T) {
	body := staffLinksBodyWithGrant(t, mgrUser, "manager", "user_management", false)
	for _, bad := range []string{`id="settings-staff-links"`, `data-key="settings-staff-links"`} {
		if strings.Contains(body, bad) {
			t.Errorf("manager without user_management leaks %q", bad)
		}
	}
}
