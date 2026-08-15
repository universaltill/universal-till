package pages

import (
	"database/sql"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
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

	t.Run("super_admin_actor_allowed", func(t *testing.T) {
		form := url.Values{"username": {"newsa2"}, "display_name": {"New SA 2"}, "role": {"super_admin"}}
		rec := postForm(mux, "/api/users", form, &auth.User{ID: "sa-1", Role: "super_admin"})
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("super_admin creating super_admin = %d, want 303: %s", rec.Code, rec.Body.String())
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
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("manager creating cashier = %d, want 303: %s", rec.Code, rec.Body.String())
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
				if rec.Code != http.StatusSeeOther {
					t.Fatalf("super_admin creating %s = %d, want 303: %s", role, rec.Code, rec.Body.String())
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

	t.Run("last_super_admin_blocked", func(t *testing.T) {
		form := url.Values{"active": {"0"}}
		rec := postForm(mux, "/api/users/sa-only/active", form, &auth.User{ID: "sa-only", Role: "super_admin"})
		if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "err=users.error.last_super_admin") {
			t.Fatalf("deactivate last super_admin = %d loc=%q, want 303 with last_super_admin error", rec.Code, rec.Header().Get("Location"))
		}
		if role, ok := userRole(t, dp.Db, "sa-only"); !ok || role != "super_admin" {
			t.Fatalf("sa-only must still be active/super_admin, role=%q ok=%v", role, ok)
		}
	})

	t.Run("second_super_admin_allows_deactivation", func(t *testing.T) {
		insertTestUserWithPIN(t, dp.Db, "sa-second", "sa-second", "Second SA", "super_admin", "h2")
		form := url.Values{"active": {"0"}}
		rec := postForm(mux, "/api/users/sa-only/active", form, &auth.User{ID: "sa-second", Role: "super_admin"})
		if rec.Code != http.StatusSeeOther || strings.Contains(rec.Header().Get("Location"), "err=") {
			t.Fatalf("deactivate with a second super_admin present = %d loc=%q, want a plain 303 to /users", rec.Code, rec.Header().Get("Location"))
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
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("demote = %d, want 303: %s", rec.Code, rec.Body.String())
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
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "err=users.error.last_super_admin") {
		t.Fatalf("demote last super_admin = %d loc=%q, want 303 with last_super_admin error", rec.Code, rec.Header().Get("Location"))
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
		if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "err=users.error.last_admin") {
			t.Fatalf("demote last admin = %d loc=%q, want 303 with last_admin error", rec.Code, rec.Header().Get("Location"))
		}
		if role, ok := userRole(t, dp.Db, "admin-only"); !ok || role != "admin" {
			t.Fatalf("admin-only role must be unchanged, role=%q ok=%v", role, ok)
		}
	})

	t.Run("second_admin_allows_change", func(t *testing.T) {
		insertTestUserWithPIN(t, dp.Db, "admin-second", "admin-second", "Second Admin", "admin", "h3")
		rec := postForm(mux, "/api/users/admin-only/role", url.Values{"role": {"manager"}}, &auth.User{ID: "sa-1", Role: "super_admin"})
		if rec.Code != http.StatusSeeOther || strings.Contains(rec.Header().Get("Location"), "err=") {
			t.Fatalf("demote with a second admin present = %d loc=%q, want a plain 303", rec.Code, rec.Header().Get("Location"))
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
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("admin promoting cashier to manager = %d, want 303: %s", rec.Code, rec.Body.String())
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
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("same-role change = %d, want 303: %s", rec.Code, rec.Body.String())
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
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "err=users.error.role") {
		t.Fatalf("invalid role = %d loc=%q, want 303 with role error", rec.Code, rec.Header().Get("Location"))
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
