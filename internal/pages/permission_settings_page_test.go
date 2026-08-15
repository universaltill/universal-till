package pages

import (
	"net/http"
	"net/http/httptest"
	"regexp"
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

// The rendered grid must actually reflect grant state — a version of this
// page that dropped Granted/Locked handling entirely, or rendered every
// cell the same way, would still pass a test that only checks for the
// presence of translated labels. Assert the real per-cell markup instead.
// Matches by regexp rather than a literal multi-line string so an unrelated
// template reformat (whitespace/attribute order) doesn't break this test —
// what must hold is "this specific <input> tag has these attributes",
// not "the template is byte-identical to what it was when this was written".
func TestPermissionSettingsPage_GET_RendersCheckedLockedAndHxVals(t *testing.T) {
	mux, _ := newPermissionSettingsTestDeps(t)

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/users/permissions", nil), auth.User{ID: "sa-1", Role: "super_admin"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// html/template HTML-escapes the double quotes inside the single-quoted
	// hx-vals attribute (&#34; not ") — same as every other jsonVals call
	// site in this codebase; the browser decodes it back to " when htmx
	// reads the attribute, so this is the real on-the-wire shape, not an
	// artifact to work around.
	roleActionJSON := func(action, role string) string {
		return `&#34;action&#34;:&#34;` + action + `&#34;,&#34;role&#34;:&#34;` + role + `&#34;`
	}
	inputTagFor := func(t *testing.T, action, role string) string {
		t.Helper()
		re := regexp.MustCompile(`(?s)<input[^>]*?hx-vals='\{` + regexp.QuoteMeta(roleActionJSON(action, role)) + `\}'[^>]*>`)
		m := re.FindString(body)
		if m == "" {
			t.Fatalf("no <input> found with hx-vals for action=%s role=%s in:\n%s", action, role, body)
		}
		return m
	}

	// A granted, non-locked cell (manager/refund, seeded true): checked,
	// carries the interactive hx-post wiring.
	granted := inputTagFor(t, "refund", "manager")
	if !strings.Contains(granted, "checked") || !strings.Contains(granted, `hx-post="/api/users/permissions"`) {
		t.Fatalf("expected a checked, interactive manager/refund cell, got: %s", granted)
	}

	// An ungranted, non-locked cell (cashier/refund, seeded false): no
	// checked attribute, still interactive.
	ungranted := inputTagFor(t, "refund", "cashier")
	if strings.Contains(ungranted, "checked") {
		t.Fatalf("expected cashier/refund to render unchecked, got: %s", ungranted)
	}

	// The self-lockout cell (super_admin/permission_management): checked
	// AND disabled — and must NOT carry the interactive hx-post/hx-vals
	// wiring at all, so it can never be toggled off from the UI.
	if strings.Contains(body, roleActionJSON("permission_management", "super_admin")) {
		t.Fatal("the locked super_admin/permission_management cell must not carry interactive hx-vals wiring")
	}
	lockedRe := regexp.MustCompile(`<input type="checkbox" checked disabled title="[^"]*">`)
	if !lockedRe.MatchString(body) {
		t.Fatalf("expected the locked cell to render checked+disabled, got:\n%s", body)
	}
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
	// 200, not 409: htmx never swaps a non-2xx response by default (and
	// this codebase's only override is scoped to 400s under /api/pos/), so
	// the rejection has to come back as a normal swappable fragment for the
	// message to actually reach the operator — see the handler's own comment.
	if rec.Code != http.StatusOK {
		t.Fatalf("self-lockout revoke = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "login-error") {
		t.Fatalf("expected the lockout error fragment, got: %s", rec.Body.String())
	}

	if granted, err := authRepo.HasPermission(ctx, "super_admin", "permission_management"); err != nil || !granted {
		t.Fatalf("super_admin must still be granted permission_management after the rejected revoke, got %v err=%v", granted, err)
	}
}

// Bad input (unknown role/action) must fail clean with 400, before ever
// reaching a write — not surface a raw SQLite FK-constraint error, and not
// silently no-op.
func TestPermissionSettingsPage_POST_RejectsUnknownRoleOrAction(t *testing.T) {
	mux, _ := newPermissionSettingsTestDeps(t)

	cases := []string{
		"role=not-a-real-role&action=refund&granted=1",
		"role=manager&action=not-a-real-action&granted=1",
		"role=&action=refund&granted=1",
		"role=manager&action=&granted=1",
	}
	for _, body := range cases {
		req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/users/permissions", formBody(body)), auth.User{ID: "sa-1", Role: "super_admin"})
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%q = %d, want 400: %s", body, rec.Code, rec.Body.String())
		}
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
