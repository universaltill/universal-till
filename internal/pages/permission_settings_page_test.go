package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func newPermissionSettingsTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	initPagesI18n(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db) // role_permissions + migration 047's permission_management row must exist

	dp := &common.Deps{
		Cfg:     &config.Config{},
		Db:      db,
		AuthSvc: auth.NewService(db),
	}
	mux := http.NewServeMux()
	registerPermissionSettings(mux, dp)
	return mux, dp
}

func TestPermissionSettingsPage_GET_RequiresSuperAdmin(t *testing.T) {
	mux, _ := newPermissionSettingsTestDeps(t)

	for _, role := range []string{"cashier", "manager", "admin"} {
		t.Run(role+"_denied", func(t *testing.T) {
			req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/users/permissions", nil), auth.User{ID: "u1", Role: role})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s = %d, want 403: %s", role, rec.Code, rec.Body.String())
			}
		})
	}

	t.Run("super_admin_allowed", func(t *testing.T) {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/users/permissions", nil), auth.User{ID: "sa-1", Role: "super_admin"})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("super_admin = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "Refunds") || !strings.Contains(body, "Permissions") {
			t.Fatalf("expected the full action catalog rendered (translated labels), got: %s", body)
		}
	})
}

func TestPermissionSettingsPage_POST_RequiresSuperAdmin(t *testing.T) {
	mux, _ := newPermissionSettingsTestDeps(t)

	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/users/permissions",
		formBody("role=manager&action=refund&granted=0")), auth.User{ID: "u1", Role: "admin"})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin POST = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

func TestPermissionSettingsPage_POST_TogglesGrantAndJournals(t *testing.T) {
	mux, dp := newPermissionSettingsTestDeps(t)
	authRepo := data.NewAuthRepo(dp.Db)
	posRepo := data.NewPOSRepo(dp.Db)
	ctx := t.Context()

	// Seed data grants manager the "refund" action (039's seed). Revoke it.
	if granted, err := authRepo.HasPermission(ctx, "manager", "refund"); err != nil || !granted {
		t.Fatalf("precondition: manager should start granted refund, got %v err=%v", granted, err)
	}

	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/users/permissions",
		formBody("role=manager&action=refund&granted=0")), auth.User{ID: "sa-1", Role: "super_admin"})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	granted, err := authRepo.HasPermission(ctx, "manager", "refund")
	if err != nil {
		t.Fatalf("HasPermission: %v", err)
	}
	if granted {
		t.Fatal("manager should no longer be granted refund")
	}

	entries, err := posRepo.ListAudit(ctx, data.AuditFilters{EntityType: "role_permission"})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "role_permission_revoked" || entries[0].ActorID != "sa-1" {
		t.Fatalf("expected exactly one role_permission_revoked entry by sa-1, got %+v", entries)
	}

	// Re-grant it back.
	req2 := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/users/permissions",
		formBody("role=manager&action=refund&granted=1")), auth.User{ID: "sa-1", Role: "super_admin"})
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("re-grant = %d, want 200: %s", rec2.Code, rec2.Body.String())
	}
	if granted, err := authRepo.HasPermission(ctx, "manager", "refund"); err != nil || !granted {
		t.Fatalf("manager should be granted refund again, got %v err=%v", granted, err)
	}
}

// The self-lockout guard (ut-docs#556's own acceptance criteria): revoking
// (super_admin, permission_management) would lock every super_admin out of
// the one page that can grant it back. Must be rejected, and must not touch
// the DB row.
func TestPermissionSettingsPage_POST_SelfLockoutGuardBlocksRevoke(t *testing.T) {
	mux, dp := newPermissionSettingsTestDeps(t)
	authRepo := data.NewAuthRepo(dp.Db)
	ctx := t.Context()

	if granted, err := authRepo.HasPermission(ctx, "super_admin", "permission_management"); err != nil || !granted {
		t.Fatalf("precondition: super_admin should start granted permission_management, got %v err=%v", granted, err)
	}

	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/users/permissions",
		formBody("role=super_admin&action=permission_management&granted=0")), auth.User{ID: "sa-1", Role: "super_admin"})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("self-lockout revoke = %d, want 409: %s", rec.Code, rec.Body.String())
	}

	if granted, err := authRepo.HasPermission(ctx, "super_admin", "permission_management"); err != nil || !granted {
		t.Fatalf("super_admin must still be granted permission_management after the rejected revoke, got %v err=%v", granted, err)
	}
}

// A DIFFERENT role losing permission_management (there isn't one seeded
// today, but a future custom-role card could add one) is not the guarded
// cell and must be allowed through normally — the guard is scoped to
// exactly (super_admin, permission_management), not the action in general.
func TestPermissionSettingsPage_POST_LockoutGuardScopedToSuperAdminOnly(t *testing.T) {
	mux, dp := newPermissionSettingsTestDeps(t)
	authRepo := data.NewAuthRepo(dp.Db)
	ctx := t.Context()

	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/users/permissions",
		formBody("role=admin&action=permission_management&granted=1")), auth.User{ID: "sa-1", Role: "super_admin"})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("grant to admin = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if granted, err := authRepo.HasPermission(ctx, "admin", "permission_management"); err != nil || !granted {
		t.Fatalf("admin should now be granted permission_management, got %v err=%v", granted, err)
	}

	// And revoking that same cell right back off must also be allowed —
	// it's admin, not super_admin, so the lockout guard doesn't apply.
	req2 := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/users/permissions",
		formBody("role=admin&action=permission_management&granted=0")), auth.User{ID: "sa-1", Role: "super_admin"})
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("revoke from admin = %d, want 200: %s", rec2.Code, rec2.Body.String())
	}
}

func formBody(s string) *strings.Reader {
	return strings.NewReader(s)
}
