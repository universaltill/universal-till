package pages

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// newUsersTestDeps mirrors newPermissionSettingsTestDeps
// (permission_settings_page_test.go) — same fixture, since the
// promote-to-super_admin path (ut-docs#761) is gated on the same
// permission_management action.
func newUsersTestDeps(t *testing.T) (*http.ServeMux, *common.Deps, *auth.Service) {
	t.Helper()
	chdirRoot(t)
	initPagesI18n(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	dp := &common.Deps{
		Cfg:     &config.Config{},
		Db:      db,
		AuthSvc: auth.NewService(db),
	}
	svc := auth.NewService(db)
	mux := http.NewServeMux()
	registerUsers(mux, dp, svc)
	return mux, dp, svc
}

// insertTestUser mirrors this package's own raw-INSERT fixture convention
// (audit_page_test.go, fiscal_gate_test.go, …) — most handler tests in
// this package seed rows directly rather than through AuthRepo, so a row
// is exactly what the actor/target it needs, no more.
func insertTestUser(t *testing.T, db *sql.DB, id, username, displayName, role string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO users(id,username,display_name,pin_hash,role,created_at) VALUES(?,?,?,'',?,datetime('now'))`,
		id, username, displayName, role); err != nil {
		t.Fatalf("insert test user %s: %v", username, err)
	}
}

// insertTestUserWithPIN is insertTestUser plus a non-empty pin_hash — the
// last-admin/last-super_admin deactivate guards only count users who can
// actually sign in (pin_hash set), so a guard test needs one.
func insertTestUserWithPIN(t *testing.T, db *sql.DB, id, username, displayName, role, pinHash string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO users(id,username,display_name,pin_hash,role,created_at) VALUES(?,?,?,?,?,datetime('now'))`,
		id, username, displayName, pinHash, role); err != nil {
		t.Fatalf("insert test user %s: %v", username, err)
	}
}

// userRole reads a user's role directly rather than through
// AuthRepo.ListUsers/GetUser — seedForPages' own seed rows ('user1',
// 'system') have a NULL display_name, which scanUser (a plain string
// field) can't scan, so any repo read that touches the whole table fails
// in this fixture regardless of this card's change.
func userRole(t *testing.T, db *sql.DB, username string) (string, bool) {
	t.Helper()
	var role string
	err := db.QueryRow(`SELECT role FROM users WHERE username = ?`, username).Scan(&role)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		t.Fatalf("query role for %s: %v", username, err)
	}
	return role, true
}

func TestUsersPage_PromoteSuperAdmin_RequiresPermissionManagement(t *testing.T) {
	mux, dp, _ := newUsersTestDeps(t)
	insertTestUser(t, dp.Db, "target-1", "amir", "Amir", "admin")

	for _, role := range []string{"cashier", "manager", "admin"} {
		t.Run(role+"_denied", func(t *testing.T) {
			rec := postForm(mux, "/api/users/target-1/promote-super-admin", nil, &auth.User{ID: "actor-1", Role: role})
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s = %d, want 403: %s", role, rec.Code, rec.Body.String())
			}
		})
	}

	if role, ok := userRole(t, dp.Db, "amir"); !ok || role != "admin" {
		t.Fatalf("target role changed by a denied request: role=%q ok=%v", role, ok)
	}
}

func TestUsersPage_PromoteSuperAdmin_Success(t *testing.T) {
	mux, dp, _ := newUsersTestDeps(t)
	posRepo := data.NewPOSRepo(dp.Db)
	insertTestUser(t, dp.Db, "target-1", "amir", "Amir", "admin")

	rec := postForm(mux, "/api/users/target-1/promote-super-admin", nil, &auth.User{ID: "sa-1", Role: "super_admin"})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("promote = %d, want 303: %s", rec.Code, rec.Body.String())
	}

	if role, ok := userRole(t, dp.Db, "amir"); !ok || role != "super_admin" {
		t.Fatalf("role after promote: role=%q ok=%v, want super_admin", role, ok)
	}

	entries, err := posRepo.ListAudit(t.Context(), data.AuditFilters{EntityType: "user", ActorID: "sa-1"})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "user_role_changed" && e.EntityID == "target-1" {
			found = true
			if !strings.Contains(e.DataJSON, "super_admin") {
				t.Fatalf("audit payload missing super_admin: %s", e.DataJSON)
			}
		}
	}
	if !found {
		t.Fatalf("expected a user_role_changed audit entry by sa-1, got %+v", entries)
	}
}

func TestUsersPage_PromoteSuperAdmin_AlreadySuperAdminIsNoop(t *testing.T) {
	mux, dp, _ := newUsersTestDeps(t)
	insertTestUser(t, dp.Db, "target-1", "sam", "Sam", "super_admin")

	rec := postForm(mux, "/api/users/target-1/promote-super-admin", nil, &auth.User{ID: "sa-1", Role: "super_admin"})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("promote = %d, want 303: %s", rec.Code, rec.Body.String())
	}
}

// TestUsersPage_PromoteSuperAdmin_KioskServiceIdentityForbidden covers the
// same class of guard as the existing 'system' exclusion: 'kiosk' (migration
// 018) is a PIN-less service identity self-order sales attribute to
// (self_order_shop.go), reachable by any anonymous LAN client via the
// auth-exempt /self-order surface — never a real operator. Promoting it to
// super_admin would be a real privilege-escalation path, not a theoretical
// one, so it gets the same treatment as 'system'.
func TestUsersPage_PromoteSuperAdmin_KioskServiceIdentityForbidden(t *testing.T) {
	mux, dp, _ := newUsersTestDeps(t)
	insertTestUser(t, dp.Db, "kiosk", "kiosk", "Self-order kiosk", "cashier")

	rec := postForm(mux, "/api/users/kiosk/promote-super-admin", nil, &auth.User{ID: "sa-1", Role: "super_admin"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("promoting kiosk = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if role, ok := userRole(t, dp.Db, "kiosk"); !ok || role != "cashier" {
		t.Fatalf("kiosk role changed: role=%q ok=%v", role, ok)
	}
}

func TestUsersPage_PromoteSuperAdmin_UnknownUser404s(t *testing.T) {
	mux, _, _ := newUsersTestDeps(t)

	rec := postForm(mux, "/api/users/does-not-exist/promote-super-admin", nil, &auth.User{ID: "sa-1", Role: "super_admin"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("promote unknown user = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestUsersPage_CreateUser_SuperAdminRole_RequiresPermissionManagement(t *testing.T) {
	mux, dp, _ := newUsersTestDeps(t)

	t.Run("admin_actor_denied", func(t *testing.T) {
		form := url.Values{"username": {"newsa1"}, "display_name": {"New SA"}, "role": {"super_admin"}}
		rec := postForm(mux, "/api/users", form, &auth.User{ID: "admin-1", Role: "admin"})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("admin creating super_admin = %d, want 403: %s", rec.Code, rec.Body.String())
		}
		if _, ok := userRole(t, dp.Db, "newsa1"); ok {
			t.Fatal("user must not have been created")
		}
	})

	// ut-docs#795: success is now an in-place htmx confirmation (200), not
	// a 303 redirect — the actor already canPerform("user_management") in
	// every subtest here, so checkOrElevate resolves allowed with no PIN
	// prompt at all (unchanged pass-through behavior).
	t.Run("super_admin_actor_allowed", func(t *testing.T) {
		form := url.Values{"username": {"newsa2"}, "display_name": {"New SA 2"}, "role": {"super_admin"}}
		rec := postForm(mux, "/api/users", form, &auth.User{ID: "sa-1", Role: "super_admin"})
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "elevation-dialog") {
			t.Fatalf("super_admin creating super_admin = %d, want 200 with no elevation prompt: %s", rec.Code, rec.Body.String())
		}
		if role, ok := userRole(t, dp.Db, "newsa2"); !ok || role != "super_admin" {
			t.Fatalf("role for newsa2: role=%q ok=%v, want super_admin", role, ok)
		}
	})

	// Existing behavior (cashier/manager/admin creation) must be untouched:
	// a plain manager can still only create cashiers.
	t.Run("manager_actor_still_limited_to_cashier", func(t *testing.T) {
		form := url.Values{"username": {"cash1"}, "display_name": {"Cash One"}, "role": {"cashier"}}
		rec := postForm(mux, "/api/users", form, &auth.User{ID: "mgr-1", Role: "manager"})
		if rec.Code != http.StatusOK {
			t.Fatalf("manager creating cashier = %d, want 200: %s", rec.Code, rec.Body.String())
		}
	})

	// ut-docs#761 review finding 3: super_admin is the TOP of the role
	// hierarchy (canManage already treats it that way — admin and
	// super_admin manage everyone but 'system'), so it must be able to do
	// at least what a plain admin can, including creating managers/admins.
	// Before this fix, the pre-existing "only admins create managers or
	// admins" check (actor.Role != "admin") rejected a super_admin actor
	// too, since super_admin literally isn't "admin" — the exact same line
	// this card's own super_admin branch sits next to.
	t.Run("super_admin_actor_can_create_manager_and_admin", func(t *testing.T) {
		for _, role := range []string{"manager", "admin"} {
			t.Run(role, func(t *testing.T) {
				form := url.Values{"username": {"newuser_" + role}, "display_name": {"New " + role}, "role": {role}}
				rec := postForm(mux, "/api/users", form, &auth.User{ID: "sa-1", Role: "super_admin"})
				if rec.Code != http.StatusOK {
					t.Fatalf("super_admin creating %s = %d, want 200: %s", role, rec.Code, rec.Body.String())
				}
				if got, ok := userRole(t, dp.Db, "newuser_"+role); !ok || got != role {
					t.Fatalf("role for newuser_%s: role=%q ok=%v, want %s", role, got, ok, role)
				}
			})
		}
	})
}

// TestUsersPage_Deactivate_LastSuperAdminGuard covers ut-docs#761 review
// finding 4: the pre-existing "cannot deactivate the last admin" guard
// (below) never had a super_admin equivalent, because nothing could reach
// super_admin before this card. Deactivating the only super_admin would
// strand the till with nobody able to reach the permission matrix, audit
// page or backoffice — the same class of lockout the admin guard already
// exists to prevent.
func TestUsersPage_Deactivate_LastSuperAdminGuard(t *testing.T) {
	mux, dp, _ := newUsersTestDeps(t)
	insertTestUserWithPIN(t, dp.Db, "sa-only", "sa-only", "Only SA", "super_admin", "h1")

	// ut-docs#795 review Blocker 2: the old "/users?err=key" redirect is
	// gone — the error now renders inline in this response's own body (a
	// translated "login-error" span), status ALWAYS 200 (see
	// usersRespondError's own doc comment) with X-UT-Response: refused as
	// the actual signal users.html's hx-on::after-request uses to skip the
	// reload — a non-2xx here would never be swapped by htmx at all.
	t.Run("last_super_admin_blocked", func(t *testing.T) {
		form := url.Values{"active": {"0"}}
		rec := postForm(mux, "/api/users/sa-only/active", form, &auth.User{ID: "sa-only", Role: "super_admin"})
		if rec.Code != http.StatusOK || rec.Header().Get("X-UT-Response") != "refused" || !strings.Contains(rec.Body.String(), httpx.T("en", "users.error.last_super_admin")) {
			t.Fatalf("deactivate last super_admin = %d (X-UT-Response=%q), want 200/refused with the last_super_admin message: %s", rec.Code, rec.Header().Get("X-UT-Response"), rec.Body.String())
		}
		if role, ok := userRole(t, dp.Db, "sa-only"); !ok || role != "super_admin" {
			t.Fatalf("sa-only must still be active/super_admin, role=%q ok=%v", role, ok)
		}
	})

	t.Run("second_super_admin_allows_deactivation", func(t *testing.T) {
		insertTestUserWithPIN(t, dp.Db, "sa-second", "sa-second", "Second SA", "super_admin", "h2")
		form := url.Values{"active": {"0"}}
		rec := postForm(mux, "/api/users/sa-only/active", form, &auth.User{ID: "sa-second", Role: "super_admin"})
		if rec.Code != http.StatusOK {
			t.Fatalf("deactivate with a second super_admin present = %d, want 200: %s", rec.Code, rec.Body.String())
		}
	})
}

// TestUsersPage_ChangeRole_DemoteSuperAdmin covers the ut-docs#766 gap:
// promote-super-admin (above) can only ever move a user *to* super_admin,
// so there was no in-app way back down. A second super_admin must exist
// first so the last-super_admin guard (below) doesn't itself block the move.
func TestUsersPage_ChangeRole_DemoteSuperAdmin(t *testing.T) {
	mux, dp, _ := newUsersTestDeps(t)
	posRepo := data.NewPOSRepo(dp.Db)
	insertTestUserWithPIN(t, dp.Db, "sa-1", "sa-1", "SA One", "super_admin", "h1")
	insertTestUserWithPIN(t, dp.Db, "sa-2", "sa-2", "SA Two", "super_admin", "h2")

	t.Run("plain_admin_denied", func(t *testing.T) {
		form := url.Values{"role": {"admin"}}
		rec := postForm(mux, "/api/users/sa-2/role", form, &auth.User{ID: "admin-1", Role: "admin"})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("admin demoting super_admin = %d, want 403: %s", rec.Code, rec.Body.String())
		}
		if role, ok := userRole(t, dp.Db, "sa-2"); !ok || role != "super_admin" {
			t.Fatalf("target role changed by a denied request: role=%q ok=%v", role, ok)
		}
	})

	t.Run("super_admin_actor_allowed", func(t *testing.T) {
		rec := postForm(mux, "/api/users/sa-2/role", url.Values{"role": {"admin"}}, &auth.User{ID: "sa-1", Role: "super_admin"})
		if rec.Code != http.StatusOK {
			t.Fatalf("demote = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if role, ok := userRole(t, dp.Db, "sa-2"); !ok || role != "admin" {
			t.Fatalf("role after demote: role=%q ok=%v, want admin", role, ok)
		}

		entries, err := posRepo.ListAudit(t.Context(), data.AuditFilters{EntityType: "user", ActorID: "sa-1"})
		if err != nil {
			t.Fatalf("ListAudit: %v", err)
		}
		found := false
		for _, e := range entries {
			if e.Action == "user_role_changed" && e.EntityID == "sa-2" {
				found = true
				if !strings.Contains(e.DataJSON, `"from":"super_admin"`) || !strings.Contains(e.DataJSON, `"to":"admin"`) {
					t.Fatalf("audit payload missing from/to: %s", e.DataJSON)
				}
			}
		}
		if !found {
			t.Fatalf("expected a user_role_changed audit entry by sa-1, got %+v", entries)
		}
	})
}

// TestUsersPage_ChangeRole_LastSuperAdminGuard mirrors
// TestUsersPage_Deactivate_LastSuperAdminGuard: changing the only
// super_admin's role away from super_admin would strand the till exactly
// the same way deactivating them would.
func TestUsersPage_ChangeRole_LastSuperAdminGuard(t *testing.T) {
	mux, dp, _ := newUsersTestDeps(t)
	insertTestUserWithPIN(t, dp.Db, "sa-only", "sa-only", "Only SA", "super_admin", "h1")

	rec := postForm(mux, "/api/users/sa-only/role", url.Values{"role": {"admin"}}, &auth.User{ID: "sa-only", Role: "super_admin"})
	if rec.Code != http.StatusOK || rec.Header().Get("X-UT-Response") != "refused" || !strings.Contains(rec.Body.String(), httpx.T("en", "users.error.last_super_admin")) {
		t.Fatalf("demote last super_admin = %d (X-UT-Response=%q), want 200/refused with the last_super_admin message: %s", rec.Code, rec.Header().Get("X-UT-Response"), rec.Body.String())
	}
	if role, ok := userRole(t, dp.Db, "sa-only"); !ok || role != "super_admin" {
		t.Fatalf("sa-only role must be unchanged, role=%q ok=%v", role, ok)
	}
}

// TestUsersPage_ChangeRole_LastAdminGuard: same guard, extended to admin —
// CountOtherActiveAdminsWithPIN already exists for the deactivate handler,
// so role-change reuses it rather than leaving this direction unguarded.
func TestUsersPage_ChangeRole_LastAdminGuard(t *testing.T) {
	mux, dp, _ := newUsersTestDeps(t)
	insertTestUserWithPIN(t, dp.Db, "sa-1", "sa-1", "SA One", "super_admin", "h1")
	insertTestUserWithPIN(t, dp.Db, "admin-only", "admin-only", "Only Admin", "admin", "h2")

	t.Run("last_admin_blocked", func(t *testing.T) {
		rec := postForm(mux, "/api/users/admin-only/role", url.Values{"role": {"manager"}}, &auth.User{ID: "sa-1", Role: "super_admin"})
		if rec.Code != http.StatusOK || rec.Header().Get("X-UT-Response") != "refused" || !strings.Contains(rec.Body.String(), httpx.T("en", "users.error.last_admin")) {
			t.Fatalf("demote last admin = %d (X-UT-Response=%q), want 200/refused with the last_admin message: %s", rec.Code, rec.Header().Get("X-UT-Response"), rec.Body.String())
		}
		if role, ok := userRole(t, dp.Db, "admin-only"); !ok || role != "admin" {
			t.Fatalf("admin-only role must be unchanged, role=%q ok=%v", role, ok)
		}
	})

	t.Run("second_admin_allows_change", func(t *testing.T) {
		insertTestUserWithPIN(t, dp.Db, "admin-second", "admin-second", "Second Admin", "admin", "h3")
		rec := postForm(mux, "/api/users/admin-only/role", url.Values{"role": {"manager"}}, &auth.User{ID: "sa-1", Role: "super_admin"})
		if rec.Code != http.StatusOK {
			t.Fatalf("demote with a second admin present = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if role, ok := userRole(t, dp.Db, "admin-only"); !ok || role != "manager" {
			t.Fatalf("role after demote: role=%q ok=%v, want manager", role, ok)
		}
	})
}

func TestUsersPage_ChangeRole_ManagerCannotPromoteCashier(t *testing.T) {
	mux, dp, _ := newUsersTestDeps(t)
	insertTestUser(t, dp.Db, "cash-1", "cash-1", "Cash One", "cashier")

	rec := postForm(mux, "/api/users/cash-1/role", url.Values{"role": {"manager"}}, &auth.User{ID: "mgr-1", Role: "manager"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("manager promoting cashier to manager = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if role, ok := userRole(t, dp.Db, "cash-1"); !ok || role != "cashier" {
		t.Fatalf("target role changed by a denied request: role=%q ok=%v", role, ok)
	}
}

func TestUsersPage_ChangeRole_ManagerCannotTouchAdmin(t *testing.T) {
	mux, dp, _ := newUsersTestDeps(t)
	insertTestUser(t, dp.Db, "admin-1", "admin-1", "Admin One", "admin")

	// canManage denies a manager acting on a non-cashier target before the
	// role-sensitivity gate is even reached.
	rec := postForm(mux, "/api/users/admin-1/role", url.Values{"role": {"cashier"}}, &auth.User{ID: "mgr-1", Role: "manager"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("manager changing admin's role = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

func TestUsersPage_ChangeRole_AdminPromotesCashierToManager(t *testing.T) {
	mux, dp, _ := newUsersTestDeps(t)
	insertTestUser(t, dp.Db, "cash-1", "cash-1", "Cash One", "cashier")

	rec := postForm(mux, "/api/users/cash-1/role", url.Values{"role": {"manager"}}, &auth.User{ID: "admin-1", Role: "admin"})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin promoting cashier to manager = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if role, ok := userRole(t, dp.Db, "cash-1"); !ok || role != "manager" {
		t.Fatalf("role after promote: role=%q ok=%v, want manager", role, ok)
	}
}

func TestUsersPage_ChangeRole_SameRoleIsNoop(t *testing.T) {
	mux, dp, _ := newUsersTestDeps(t)
	posRepo := data.NewPOSRepo(dp.Db)
	insertTestUser(t, dp.Db, "cash-1", "cash-1", "Cash One", "cashier")

	rec := postForm(mux, "/api/users/cash-1/role", url.Values{"role": {"cashier"}}, &auth.User{ID: "mgr-1", Role: "manager"})
	if rec.Code != http.StatusOK {
		t.Fatalf("same-role change = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	entries, err := posRepo.ListAudit(t.Context(), data.AuditFilters{EntityType: "user", ActorID: "mgr-1"})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	for _, e := range entries {
		if e.Action == "user_role_changed" && e.EntityID == "cash-1" {
			t.Fatalf("no-op same-role change must not journal an audit entry, got %+v", e)
		}
	}
}

func TestUsersPage_ChangeRole_InvalidRole(t *testing.T) {
	mux, dp, _ := newUsersTestDeps(t)
	insertTestUser(t, dp.Db, "cash-1", "cash-1", "Cash One", "cashier")

	rec := postForm(mux, "/api/users/cash-1/role", url.Values{"role": {"owner"}}, &auth.User{ID: "admin-1", Role: "admin"})
	if rec.Code != http.StatusOK || rec.Header().Get("X-UT-Response") != "refused" || !strings.Contains(rec.Body.String(), httpx.T("en", "users.error.role")) {
		t.Fatalf("invalid role = %d (X-UT-Response=%q), want 200/refused with the role error: %s", rec.Code, rec.Header().Get("X-UT-Response"), rec.Body.String())
	}
}

func TestUsersPage_ChangeRole_KioskServiceIdentityForbidden(t *testing.T) {
	mux, dp, _ := newUsersTestDeps(t)
	insertTestUser(t, dp.Db, "kiosk", "kiosk", "Self-order kiosk", "cashier")

	rec := postForm(mux, "/api/users/kiosk/role", url.Values{"role": {"manager"}}, &auth.User{ID: "sa-1", Role: "super_admin"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("changing kiosk role = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if role, ok := userRole(t, dp.Db, "kiosk"); !ok || role != "cashier" {
		t.Fatalf("kiosk role changed: role=%q ok=%v", role, ok)
	}
}

func TestUsersPage_ChangeRole_UnknownUser404s(t *testing.T) {
	mux, _, _ := newUsersTestDeps(t)

	rec := postForm(mux, "/api/users/does-not-exist/role", url.Values{"role": {"manager"}}, &auth.User{ID: "sa-1", Role: "super_admin"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("change role of unknown user = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// --- ut-docs#795: checkOrElevate wiring on the 4 mutating handlers ---

// newUsersElevationPrincipals seeds a real approver (with a working PIN,
// granted user_management by seedForPages — approverRole must be manager,
// admin or super_admin) and a blocked cashier session — the same
// actor/approver pair every elevated-path test below needs. Mirrors
// eod_api_test.go's newElevationTestPrincipals for the eod_report family.
func newUsersElevationPrincipals(t *testing.T, svc *auth.Service, approverRole, approverUsername, cashierUsername, pin string) (approverID, blockedID string) {
	t.Helper()
	authRepo := svc.Repo()
	ctx := t.Context()

	approverID, err := authRepo.CreateUser(ctx, approverUsername, "Approver", approverRole)
	if err != nil {
		t.Fatalf("create approver: %v", err)
	}
	hash, err := auth.HashPIN(pin)
	if err != nil {
		t.Fatalf("hash approver pin: %v", err)
	}
	if err := authRepo.SetUserPIN(ctx, approverID, hash); err != nil {
		t.Fatalf("set approver pin: %v", err)
	}
	blockedID, err = authRepo.CreateUser(ctx, cashierUsername, "Blocked Cashier", "cashier")
	if err != nil {
		t.Fatalf("create blocked cashier: %v", err)
	}
	return approverID, blockedID
}

// TestUsersPage_CreateUser_ElevationFlow covers POST /api/users end to end:
// (a) an actor who canPerform("user_management") passes straight through
// unchanged, (b) a blocked actor with no PIN gets the elevation prompt (not
// a flat 403), (c) a blocked actor with a valid approver PIN succeeds and
// writes a dual-attribution (InsertAuditElevated-shaped) audit row, and
// (d) an invalid approver PIN still denies. The approver is a manager —
// sufficient here since the target role is "cashier", so the "only admins
// create managers/admins" business rule never triggers.
func TestUsersPage_CreateUser_ElevationFlow(t *testing.T) {
	mux, dp, svc := newUsersTestDeps(t)
	posRepo := data.NewPOSRepo(dp.Db)
	mgrID, blockedID := newUsersElevationPrincipals(t, svc, "manager", "mgr-create", "blocked-create", "778899")

	t.Run("canPerform_actor_passes_straight_through", func(t *testing.T) {
		form := url.Values{"username": {"direct1"}, "display_name": {"Direct One"}, "role": {"cashier"}}
		rec := postForm(mux, "/api/users", form, &auth.User{ID: mgrID, Role: "manager"})
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "elevation-dialog") {
			t.Fatalf("manager create = %d, want 200 with no elevation prompt: %s", rec.Code, rec.Body.String())
		}
		if _, ok := userRole(t, dp.Db, "direct1"); !ok {
			t.Fatal("user must have been created")
		}
	})

	t.Run("blocked_no_pin_gets_prompt_not_403", func(t *testing.T) {
		form := url.Values{"username": {"blocked-attempt"}, "display_name": {"Blocked Attempt"}, "role": {"cashier"}}
		rec := postForm(mux, "/api/users", form, &auth.User{ID: blockedID, Role: "cashier"})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") || !strings.Contains(rec.Body.String(), `name="override_pin"`) {
			t.Fatalf("blocked create = %d, want 200 with the elevation prompt: %s", rec.Code, rec.Body.String())
		}
		// ut-docs#795 review (round 2 test-coverage gap): X-UT-Response is
		// the ONLY signal users.html's hx-on::after-request uses to skip
		// the destructive reload on a prompt response (Blocker 1) — without
		// this assertion, deleting that header from renderElevationPrompt
		// would reinstate Blocker 1 with every other test still green.
		if got := rec.Header().Get("X-UT-Response"); got != "elevation-prompt" {
			t.Fatalf("blocked create: X-UT-Response = %q, want \"elevation-prompt\" (or the reload fires before a PIN can be entered)", got)
		}
		// Hidden must replay username/display_name/role so the dialog's
		// retry resubmits the SAME new-user form, not a blank one.
		if !strings.Contains(rec.Body.String(), `name="username" value="blocked-attempt"`) ||
			!strings.Contains(rec.Body.String(), `name="display_name" value="Blocked Attempt"`) ||
			!strings.Contains(rec.Body.String(), `name="role" value="cashier"`) {
			t.Fatalf("expected username/display_name/role to round-trip via Hidden fields, got: %s", rec.Body.String())
		}
		if _, ok := userRole(t, dp.Db, "blocked-attempt"); ok {
			t.Fatal("user must not have been created without approval")
		}
	})

	t.Run("valid_approver_pin_elevates_and_dual_attributes", func(t *testing.T) {
		form := url.Values{"username": {"elevated1"}, "display_name": {"Elevated One"}, "role": {"cashier"}, "override_pin": {"778899"}}
		rec := postForm(mux, "/api/users", form, &auth.User{ID: blockedID, Role: "cashier"})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
			t.Fatalf("elevated create = %d, want 200 success: %s", rec.Code, rec.Body.String())
		}
		// X-UT-Response: ok is the reload signal itself (ut-docs#795 review
		// round 2 test-coverage gap) — without this, usersRespondOK
		// dropping the header would silently stop the row/form from ever
		// refreshing after a real, successful write.
		if got := rec.Header().Get("X-UT-Response"); got != "ok" {
			t.Fatalf("elevated create: X-UT-Response = %q, want \"ok\" (or the success response never reloads)", got)
		}
		if _, ok := userRole(t, dp.Db, "elevated1"); !ok {
			t.Fatal("user must have been created once elevated")
		}
		var userID string
		if err := dp.Db.QueryRow(`SELECT id FROM users WHERE username='elevated1'`).Scan(&userID); err != nil {
			t.Fatalf("lookup created user id: %v", err)
		}
		entries, err := posRepo.ListAudit(t.Context(), data.AuditFilters{EntityType: "user", ActorID: mgrID, Action: "user_create"})
		if err != nil {
			t.Fatalf("ListAudit: %v", err)
		}
		found := false
		for _, e := range entries {
			if e.EntityID == userID {
				found = true
				if e.BlockedActorID != blockedID {
					t.Fatalf("blocked_actor_id = %q, want the originally-blocked session user %q", e.BlockedActorID, blockedID)
				}
			}
		}
		if !found {
			t.Fatalf("expected a user_create audit entry attributed to the approver %q, got %+v", mgrID, entries)
		}
	})

	t.Run("invalid_pin_still_denies", func(t *testing.T) {
		form := url.Values{"username": {"never-created"}, "display_name": {"Never Created"}, "role": {"cashier"}, "override_pin": {"000000"}}
		rec := postForm(mux, "/api/users", form, &auth.User{ID: blockedID, Role: "cashier"})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") {
			t.Fatalf("invalid-pin create = %d, want 200 with the elevation prompt (denied): %s", rec.Code, rec.Body.String())
		}
		if _, ok := userRole(t, dp.Db, "never-created"); ok {
			t.Fatal("user must not have been created with an invalid approver pin")
		}
	})
}

// TestUsersPage_SetPIN_ElevationFlow covers POST /api/users/{id}/pin the
// same way — see TestUsersPage_CreateUser_ElevationFlow's doc comment for
// the (a)-(d) shape.
func TestUsersPage_SetPIN_ElevationFlow(t *testing.T) {
	mux, dp, svc := newUsersTestDeps(t)
	posRepo := data.NewPOSRepo(dp.Db)
	mgrID, blockedID := newUsersElevationPrincipals(t, svc, "manager", "mgr-pin", "blocked-pin", "334455")
	// Distinct targets per subtest (not just one shared "target-pin"): mgrID
	// plays BOTH the direct session actor (subtest 1) and the approver
	// (subtest 3) below — sharing one target would make the dual-attribution
	// ListAudit query in subtest 3 match subtest 1's own plain (non-elevated)
	// audit row too, since both would carry actor_id=mgrID on the same
	// entity_id.
	insertTestUser(t, dp.Db, "target-pin-1", "target-pin-1", "Target Pin 1", "cashier")
	insertTestUser(t, dp.Db, "target-pin-2", "target-pin-2", "Target Pin 2", "cashier")

	// pinMatches checks the stored hash directly rather than through
	// svc.Login — this fixture (seedForPages) carries no sessions table,
	// and a real login isn't what these assertions are about anyway.
	pinMatches := func(id, pin string) bool {
		t.Helper()
		var hash string
		if err := dp.Db.QueryRow(`SELECT pin_hash FROM users WHERE id = ?`, id).Scan(&hash); err != nil {
			t.Fatalf("query pin_hash for %s: %v", id, err)
		}
		return hash != "" && auth.VerifyPIN(pin, hash)
	}

	t.Run("canPerform_actor_passes_straight_through", func(t *testing.T) {
		rec := postForm(mux, "/api/users/target-pin-1/pin", url.Values{"pin": {"246810"}}, &auth.User{ID: mgrID, Role: "manager"})
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "elevation-dialog") {
			t.Fatalf("manager set pin = %d, want 200 with no elevation prompt: %s", rec.Code, rec.Body.String())
		}
		if !pinMatches("target-pin-1", "246810") {
			t.Fatal("target's stored PIN must match the new PIN")
		}
	})

	t.Run("blocked_no_pin_gets_prompt_not_403", func(t *testing.T) {
		rec := postForm(mux, "/api/users/target-pin-2/pin", url.Values{"pin": {"135791"}}, &auth.User{ID: blockedID, Role: "cashier"})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") || !strings.Contains(rec.Body.String(), `name="override_pin"`) {
			t.Fatalf("blocked set pin = %d, want 200 with the elevation prompt: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `name="pin" value="135791"`) {
			t.Fatalf("expected the pin to round-trip via a Hidden field, got: %s", rec.Body.String())
		}
	})

	t.Run("valid_approver_pin_elevates_and_dual_attributes", func(t *testing.T) {
		form := url.Values{"pin": {"998877"}, "override_pin": {"334455"}}
		rec := postForm(mux, "/api/users/target-pin-2/pin", form, &auth.User{ID: blockedID, Role: "cashier"})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
			t.Fatalf("elevated set pin = %d, want 200 success: %s", rec.Code, rec.Body.String())
		}
		if !pinMatches("target-pin-2", "998877") {
			t.Fatal("target's stored PIN must match the elevated PIN")
		}
		entries, err := posRepo.ListAudit(t.Context(), data.AuditFilters{EntityType: "user", ActorID: mgrID, Action: "user_pin_set"})
		if err != nil {
			t.Fatalf("ListAudit: %v", err)
		}
		found := false
		for _, e := range entries {
			if e.EntityID == "target-pin-2" {
				found = true
				if e.BlockedActorID != blockedID {
					t.Fatalf("blocked_actor_id = %q, want %q", e.BlockedActorID, blockedID)
				}
			}
		}
		if !found {
			t.Fatalf("expected a user_pin_set audit entry attributed to the approver %q, got %+v", mgrID, entries)
		}
	})

	t.Run("invalid_pin_still_denies", func(t *testing.T) {
		form := url.Values{"pin": {"112233"}, "override_pin": {"000000"}}
		rec := postForm(mux, "/api/users/target-pin-2/pin", form, &auth.User{ID: blockedID, Role: "cashier"})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") {
			t.Fatalf("invalid-pin set pin = %d, want 200 with the elevation prompt (denied): %s", rec.Code, rec.Body.String())
		}
		if pinMatches("target-pin-2", "112233") {
			t.Fatal("pin must not have been set with an invalid approver pin")
		}
	})
}

// TestUsersPage_SetActive_ElevationFlow covers POST /api/users/{id}/active
// the same way — see TestUsersPage_CreateUser_ElevationFlow's doc comment.
func TestUsersPage_SetActive_ElevationFlow(t *testing.T) {
	mux, dp, svc := newUsersTestDeps(t)
	posRepo := data.NewPOSRepo(dp.Db)
	mgrID, blockedID := newUsersElevationPrincipals(t, svc, "manager", "mgr-active", "blocked-active", "112244")
	insertTestUserWithPIN(t, dp.Db, "target-active-1", "target-active-1", "Target Active 1", "cashier", "h1")
	insertTestUserWithPIN(t, dp.Db, "target-active-2", "target-active-2", "Target Active 2", "cashier", "h2")
	insertTestUserWithPIN(t, dp.Db, "target-active-3", "target-active-3", "Target Active 3", "cashier", "h3")

	isActive := func(id string) bool {
		t.Helper()
		var v bool
		if err := dp.Db.QueryRow(`SELECT is_active FROM users WHERE id = ?`, id).Scan(&v); err != nil {
			t.Fatalf("query is_active for %s: %v", id, err)
		}
		return v
	}

	t.Run("canPerform_actor_passes_straight_through", func(t *testing.T) {
		rec := postForm(mux, "/api/users/target-active-1/active", url.Values{"active": {"0"}}, &auth.User{ID: mgrID, Role: "manager"})
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "elevation-dialog") {
			t.Fatalf("manager deactivate = %d, want 200 with no elevation prompt: %s", rec.Code, rec.Body.String())
		}
		if isActive("target-active-1") {
			t.Fatal("target must be deactivated")
		}
	})

	t.Run("blocked_no_pin_gets_prompt_not_403", func(t *testing.T) {
		rec := postForm(mux, "/api/users/target-active-2/active", url.Values{"active": {"0"}}, &auth.User{ID: blockedID, Role: "cashier"})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") {
			t.Fatalf("blocked deactivate = %d, want 200 with the elevation prompt: %s", rec.Code, rec.Body.String())
		}
		if !isActive("target-active-2") {
			t.Fatal("target must still be active while blocked")
		}
	})

	t.Run("valid_approver_pin_elevates_and_dual_attributes", func(t *testing.T) {
		form := url.Values{"active": {"0"}, "override_pin": {"112244"}}
		rec := postForm(mux, "/api/users/target-active-2/active", form, &auth.User{ID: blockedID, Role: "cashier"})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
			t.Fatalf("elevated deactivate = %d, want 200 success: %s", rec.Code, rec.Body.String())
		}
		if isActive("target-active-2") {
			t.Fatal("target must be deactivated once elevated")
		}
		entries, err := posRepo.ListAudit(t.Context(), data.AuditFilters{EntityType: "user", ActorID: mgrID, Action: "user_deactivate"})
		if err != nil {
			t.Fatalf("ListAudit: %v", err)
		}
		found := false
		for _, e := range entries {
			if e.EntityID == "target-active-2" {
				found = true
				if e.BlockedActorID != blockedID {
					t.Fatalf("blocked_actor_id = %q, want %q", e.BlockedActorID, blockedID)
				}
			}
		}
		if !found {
			t.Fatalf("expected a user_deactivate audit entry attributed to the approver %q, got %+v", mgrID, entries)
		}
	})

	t.Run("invalid_pin_still_denies", func(t *testing.T) {
		form := url.Values{"active": {"0"}, "override_pin": {"000000"}}
		rec := postForm(mux, "/api/users/target-active-3/active", form, &auth.User{ID: blockedID, Role: "cashier"})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") {
			t.Fatalf("invalid-pin deactivate = %d, want 200 with the elevation prompt (denied): %s", rec.Code, rec.Body.String())
		}
		if !isActive("target-active-3") {
			t.Fatal("target must still be active — a denied request must not deactivate")
		}
	})
}

// TestUsersPage_ChangeRole_ElevationFlow covers POST /api/users/{id}/role
// the same way — see TestUsersPage_CreateUser_ElevationFlow's doc comment.
// The approver here is an ADMIN, not a manager: the "only admins change
// managers or admins" business rule (resolveActingUser + the else-if branch
// in the handler) runs against the RESOLVED (post-elevation) actor, so a
// manager approver would elevate past checkOrElevate's own user_management
// gate but then still get denied by that business rule — this test proves
// the admin case actually completes end to end.
func TestUsersPage_ChangeRole_ElevationFlow(t *testing.T) {
	mux, dp, svc := newUsersTestDeps(t)
	posRepo := data.NewPOSRepo(dp.Db)
	adminID, blockedID := newUsersElevationPrincipals(t, svc, "admin", "admin-role-approver", "blocked-role", "556677")
	insertTestUser(t, dp.Db, "target-role-1", "target-role-1", "Target Role 1", "cashier")
	insertTestUser(t, dp.Db, "target-role-2", "target-role-2", "Target Role 2", "cashier")
	insertTestUser(t, dp.Db, "target-role-3", "target-role-3", "Target Role 3", "cashier")

	t.Run("canPerform_actor_passes_straight_through", func(t *testing.T) {
		rec := postForm(mux, "/api/users/target-role-1/role", url.Values{"role": {"manager"}}, &auth.User{ID: "direct-admin-1", Role: "admin"})
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "elevation-dialog") {
			t.Fatalf("admin change role = %d, want 200 with no elevation prompt: %s", rec.Code, rec.Body.String())
		}
		if role, ok := userRole(t, dp.Db, "target-role-1"); !ok || role != "manager" {
			t.Fatalf("role after direct change: role=%q ok=%v, want manager", role, ok)
		}
	})

	t.Run("blocked_no_pin_gets_prompt_not_403", func(t *testing.T) {
		rec := postForm(mux, "/api/users/target-role-2/role", url.Values{"role": {"manager"}}, &auth.User{ID: blockedID, Role: "cashier"})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") {
			t.Fatalf("blocked change role = %d, want 200 with the elevation prompt: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `name="role" value="manager"`) {
			t.Fatalf("expected the requested role to round-trip via a Hidden field, got: %s", rec.Body.String())
		}
		if role, ok := userRole(t, dp.Db, "target-role-2"); !ok || role != "cashier" {
			t.Fatalf("role must be unchanged while blocked, role=%q ok=%v", role, ok)
		}
	})

	t.Run("valid_approver_pin_elevates_and_dual_attributes", func(t *testing.T) {
		form := url.Values{"role": {"manager"}, "override_pin": {"556677"}}
		rec := postForm(mux, "/api/users/target-role-2/role", form, &auth.User{ID: blockedID, Role: "cashier"})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
			t.Fatalf("elevated change role = %d, want 200 success: %s", rec.Code, rec.Body.String())
		}
		if role, ok := userRole(t, dp.Db, "target-role-2"); !ok || role != "manager" {
			t.Fatalf("role after elevated change: role=%q ok=%v, want manager", role, ok)
		}
		entries, err := posRepo.ListAudit(t.Context(), data.AuditFilters{EntityType: "user", ActorID: adminID, Action: "user_role_changed"})
		if err != nil {
			t.Fatalf("ListAudit: %v", err)
		}
		found := false
		for _, e := range entries {
			if e.EntityID == "target-role-2" {
				found = true
				if e.BlockedActorID != blockedID {
					t.Fatalf("blocked_actor_id = %q, want %q", e.BlockedActorID, blockedID)
				}
			}
		}
		if !found {
			t.Fatalf("expected a user_role_changed audit entry attributed to the approver %q, got %+v", adminID, entries)
		}
	})

	t.Run("invalid_pin_still_denies", func(t *testing.T) {
		form := url.Values{"role": {"manager"}, "override_pin": {"000000"}}
		rec := postForm(mux, "/api/users/target-role-3/role", form, &auth.User{ID: blockedID, Role: "cashier"})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") {
			t.Fatalf("invalid-pin change role = %d, want 200 with the elevation prompt (denied): %s", rec.Code, rec.Body.String())
		}
		if role, ok := userRole(t, dp.Db, "target-role-3"); !ok || role != "cashier" {
			t.Fatalf("role must be unchanged with an invalid approver pin, role=%q ok=%v", role, ok)
		}
	})
}

// --- ut-docs#795 review S4: resolveActingUser proof for create/pin/active ---
//
// Before these 3 tests, resolveActingUser's whole design decision (canManage
// / the role-sensitivity checks run against the RESOLVED approver, not the
// still-blocked session actor) was proven by exactly ONE test
// (TestUsersPage_ChangeRole_ElevationFlow's admin-approver case) — the
// create/pin/active elevation tests above all use cashier targets, where
// session-vs-approver never matters (a manager approver is already enough
// to manage a cashier target, so those tests would still pass even with
// resolveActingUser deleted entirely and canManage/the role checks reading
// the blocked SESSION cashier instead). Each test below picks a target/role
// where that substitution would flip the outcome: an ADMIN approver acting
// on a MANAGER-or-ADMIN target, via a BLOCKED CASHIER session — if
// resolveActingUser were bypassed in favor of the session actor, the
// cashier's own role would wrongly deny every one of these.

// TestUsersPage_CreateUser_ElevationFlow_ActorResolutionMatters: creating a
// "manager" hits the "only admins create managers or admins" rule
// (actingUser.Role != "admin" && != "super_admin"), which a blocked
// cashier's OWN role would always fail — only the resolved admin approver
// passes it.
func TestUsersPage_CreateUser_ElevationFlow_ActorResolutionMatters(t *testing.T) {
	mux, dp, svc := newUsersTestDeps(t)
	posRepo := data.NewPOSRepo(dp.Db)
	adminID, blockedID := newUsersElevationPrincipals(t, svc, "admin", "admin-create-approver", "blocked-create-mgr", "445566")

	form := url.Values{"username": {"newmgr1"}, "display_name": {"New Mgr"}, "role": {"manager"}, "override_pin": {"445566"}}
	rec := postForm(mux, "/api/users", form, &auth.User{ID: blockedID, Role: "cashier"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("admin-approved manager create = %d, want 200 success (would 403 if canManage/the role check ran against the blocked cashier session instead of the resolved admin approver): %s", rec.Code, rec.Body.String())
	}
	if role, ok := userRole(t, dp.Db, "newmgr1"); !ok || role != "manager" {
		t.Fatalf("role for newmgr1: role=%q ok=%v, want manager", role, ok)
	}
	entries, err := posRepo.ListAudit(t.Context(), data.AuditFilters{EntityType: "user", ActorID: adminID, Action: "user_create"})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.BlockedActorID == blockedID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a user_create audit entry attributed to the approver %q with blocked_actor_id %q, got %+v", adminID, blockedID, entries)
	}
}

// TestUsersPage_SetPIN_ElevationFlow_ActorResolutionMatters: canManage only
// lets an admin/super_admin actor touch a non-cashier target — a blocked
// cashier session's own role would always fail canManage against an admin
// target; only the resolved admin approver passes it.
func TestUsersPage_SetPIN_ElevationFlow_ActorResolutionMatters(t *testing.T) {
	mux, dp, svc := newUsersTestDeps(t)
	posRepo := data.NewPOSRepo(dp.Db)
	adminID, blockedID := newUsersElevationPrincipals(t, svc, "admin", "admin-pin-approver", "blocked-pin-mgr", "556699")
	insertTestUser(t, dp.Db, "target-pin-admin", "target-pin-admin", "Target Pin Admin", "admin")

	form := url.Values{"pin": {"224466"}, "override_pin": {"556699"}}
	rec := postForm(mux, "/api/users/target-pin-admin/pin", form, &auth.User{ID: blockedID, Role: "cashier"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("admin-approved pin set on an admin target = %d, want 200 success (would 403 if canManage ran against the blocked cashier session instead of the resolved admin approver): %s", rec.Code, rec.Body.String())
	}
	var hash string
	if err := dp.Db.QueryRow(`SELECT pin_hash FROM users WHERE id = 'target-pin-admin'`).Scan(&hash); err != nil {
		t.Fatalf("query pin_hash: %v", err)
	}
	if hash == "" || !auth.VerifyPIN("224466", hash) {
		t.Fatal("target's stored PIN must match the elevated PIN")
	}
	entries, err := posRepo.ListAudit(t.Context(), data.AuditFilters{EntityType: "user", ActorID: adminID, Action: "user_pin_set"})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.EntityID == "target-pin-admin" && e.BlockedActorID == blockedID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a user_pin_set audit entry attributed to the approver %q with blocked_actor_id %q, got %+v", adminID, blockedID, entries)
	}
}

// TestUsersPage_SetActive_ElevationFlow_ActorResolutionMatters: same
// canManage proof as the PIN test above, via activate (not deactivate) so
// the last-active-admin guard never enters into it — this test is purely
// about actor resolution, not that separate guard.
func TestUsersPage_SetActive_ElevationFlow_ActorResolutionMatters(t *testing.T) {
	mux, dp, svc := newUsersTestDeps(t)
	posRepo := data.NewPOSRepo(dp.Db)
	adminID, blockedID := newUsersElevationPrincipals(t, svc, "admin", "admin-active-approver", "blocked-active-mgr", "667700")
	insertTestUserWithPIN(t, dp.Db, "target-active-admin", "target-active-admin", "Target Active Admin", "admin", "h1")

	form := url.Values{"active": {"1"}, "override_pin": {"667700"}}
	rec := postForm(mux, "/api/users/target-active-admin/active", form, &auth.User{ID: blockedID, Role: "cashier"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("admin-approved activate on an admin target = %d, want 200 success (would 403 if canManage ran against the blocked cashier session instead of the resolved admin approver): %s", rec.Code, rec.Body.String())
	}
	entries, err := posRepo.ListAudit(t.Context(), data.AuditFilters{EntityType: "user", ActorID: adminID, Action: "user_activate"})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.EntityID == "target-active-admin" && e.BlockedActorID == blockedID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a user_activate audit entry attributed to the approver %q with blocked_actor_id %q, got %+v", adminID, blockedID, entries)
	}
}

// TestUsersPage_BusinessRuleRefusal_IsHtmxSwappable is ut-docs#795 review
// Blocker 2's own explicitly-requested regression guard: the Go tests above
// pass purely on rec.Body (httptest never renders a page or runs htmx), so a
// return to a 4xx/5xx status on any of these paths would silently reintroduce
// the "every business-rule refusal renders as a useless generic banner"
// regression without failing any BODY-content assertion at all. This test
// asserts on the response SHAPE instead: status must be one htmx actually
// swaps (2xx — htmx's own isError treats anything else as
// htmx:responseError, and this repo's app.js turns that into a generic
// banner, never the real message), plus the X-UT-Response: refused header
// users.html's hx-on::after-request depends on to skip the reload, across
// all 4 mutating handlers' own business-rule refusal paths (not merely the
// last-admin/last-super_admin/invalid-role cases already covered above).
func TestUsersPage_BusinessRuleRefusal_IsHtmxSwappable(t *testing.T) {
	mux, dp, _ := newUsersTestDeps(t)
	insertTestUser(t, dp.Db, "swap-target-1", "swap-target-1", "Swap Target 1", "cashier")
	admin := &auth.User{ID: "swap-admin-1", Role: "admin"}

	assertSwappableRefusal := func(t *testing.T, name string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code < 200 || rec.Code >= 300 {
			t.Fatalf("%s = %d, want a 2xx status — htmx never swaps a non-2xx response by default, so this would render app.js's generic error banner instead of the real reason: %s", name, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("X-UT-Response"); got != "refused" {
			t.Fatalf("%s: X-UT-Response = %q, want \"refused\" (users.html's hx-on::after-request keys off this to skip the reload): %s", name, got, rec.Body.String())
		}
	}

	t.Run("create_invalid_role", func(t *testing.T) {
		form := url.Values{"username": {"x1"}, "display_name": {"X1"}, "role": {"owner"}}
		rec := postForm(mux, "/api/users", form, admin)
		assertSwappableRefusal(t, "create invalid role", rec)
	})
	t.Run("create_missing_required_field", func(t *testing.T) {
		form := url.Values{"username": {""}, "display_name": {"X2"}, "role": {"cashier"}}
		rec := postForm(mux, "/api/users", form, admin)
		assertSwappableRefusal(t, "create missing field", rec)
	})
	t.Run("pin_bad_format", func(t *testing.T) {
		rec := postForm(mux, "/api/users/swap-target-1/pin", url.Values{"pin": {"12"}}, admin)
		assertSwappableRefusal(t, "pin bad format", rec)
	})
	t.Run("role_invalid", func(t *testing.T) {
		rec := postForm(mux, "/api/users/swap-target-1/role", url.Values{"role": {"owner"}}, admin)
		assertSwappableRefusal(t, "role invalid", rec)
	})
}
