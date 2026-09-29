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
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

func newPermissionSettingsTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	initPagesI18n(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db) // role_permissions + migration 047's permission_management row must exist
	// audit_log.actor_id/blocked_actor_id both have real FKs to users(id).
	// Several tests inject a session directly via auth.WithUser using these
	// synthetic ids without a real login, so seed matching rows here.
	if _, err := db.Exec(`INSERT INTO users(id, username, display_name, role) VALUES
		('sa-1', 'sa-1', 'Super Admin One', 'super_admin'),
		('blocked-admin', 'blocked-admin', 'Blocked Admin', 'admin')`); err != nil {
		t.Fatalf("seed test actors: %v", err)
	}

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

// ut-docs#942: migration 057 added the tax_code_management action to the
// catalog with no matching web/locales/*.json key, so httpx.T's raw-key
// fallback rendered the literal action name on the matrix instead of a
// translated label. Assert the real label shows and the raw key never leaks
// into the response — a regression here means a locale file has drifted out
// of sync with the DB-seeded action catalog again.
func TestPermissionSettingsPage_GET_TaxCodeManagementHasTranslatedLabel(t *testing.T) {
	mux, _ := newPermissionSettingsTestDeps(t)

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/users/permissions", nil), auth.User{ID: "sa-1", Role: "super_admin"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Tax codes") {
		t.Fatalf("expected the translated \"Tax codes\" label for the tax_code_management action, got: %s", body)
	}
	if strings.Contains(body, ">tax_code_management<") {
		t.Fatalf("raw action key leaked into the response instead of a translated label: %s", body)
	}
}

// ut-docs#2312: catalog_management's own analogue of the regression test
// above — same failure class (a new seeded action with no matching
// web/locales/*.json key), same fix shape.
func TestPermissionSettingsPage_GET_CatalogManagementHasTranslatedLabel(t *testing.T) {
	mux, _ := newPermissionSettingsTestDeps(t)

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/users/permissions", nil), auth.User{ID: "sa-1", Role: "super_admin"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Catalog &amp; quick buttons") && !strings.Contains(body, "Catalog & quick buttons") {
		t.Fatalf("expected the translated \"Catalog & quick buttons\" label for the catalog_management action, got: %s", body)
	}
	if strings.Contains(body, ">catalog_management<") {
		t.Fatalf("raw action key leaked into the response instead of a translated label: %s", body)
	}
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

// ut-docs#557: a denied POST now renders the in-place elevation prompt
// (200, htmx-swappable) instead of a flat 403 — canPerform() still denies
// an admin session exactly as before (permission_management is
// super_admin-only), but the response shape changed on purpose.
func TestPermissionSettingsPage_POST_RequiresSuperAdmin(t *testing.T) {
	mux, dp := newPermissionSettingsTestDeps(t)

	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/users/permissions",
		formBody("role=manager&action=refund&granted=0")), auth.User{ID: "u1", Role: "admin"})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin POST = %d, want 200 (elevation prompt): %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "elevation-dialog") || !strings.Contains(body, `name="override_pin"`) {
		t.Fatalf("expected the elevation prompt dialog, got: %s", body)
	}
	// Nothing must actually change without a valid approver PIN.
	authRepo := data.NewAuthRepo(dp.Db)
	if granted, err := authRepo.HasPermission(t.Context(), "manager", "refund"); err != nil || !granted {
		t.Fatalf("manager should still be granted refund (no write without elevation), got %v err=%v", granted, err)
	}
}

// A correct super_admin approver PIN elevates: the grant proceeds as the
// approver, and the audit trail records both the approver (actor) and the
// originally-blocked admin session (blocked_actor_id).
func TestPermissionSettingsPage_POST_ElevatesOnValidApproverPIN(t *testing.T) {
	mux, dp := newPermissionSettingsTestDeps(t)
	authRepo := data.NewAuthRepo(dp.Db)
	posRepo := data.NewPOSRepo(dp.Db)
	ctx := t.Context()

	saID, err := authRepo.CreateUser(ctx, "sa2", "Super Admin Two", "super_admin")
	if err != nil {
		t.Fatalf("create super_admin: %v", err)
	}
	hash, err := auth.HashPIN("192837")
	if err != nil {
		t.Fatalf("hash pin: %v", err)
	}
	if err := authRepo.SetUserPIN(ctx, saID, hash); err != nil {
		t.Fatalf("set pin: %v", err)
	}

	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/users/permissions",
		formBody("role=manager&action=refund&granted=0&override_pin=192837")), auth.User{ID: "blocked-admin", Role: "admin"})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("elevated POST = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	if granted, err := authRepo.HasPermission(ctx, "manager", "refund"); err != nil || granted {
		t.Fatalf("manager should no longer be granted refund, got %v err=%v", granted, err)
	}

	entries, err := posRepo.ListAudit(ctx, data.AuditFilters{EntityType: "role_permission"})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one audit entry, got %+v", entries)
	}
	if entries[0].ActorID != saID {
		t.Fatalf("ActorID = %q, want the approver %q", entries[0].ActorID, saID)
	}
	if entries[0].BlockedActorID != "blocked-admin" {
		t.Fatalf("BlockedActorID = %q, want the originally-blocked session user", entries[0].BlockedActorID)
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

// ut-docs#557 review Fix 4: input validation must run BEFORE the elevation
// check, so a denied session with a genuinely bad request (unknown role)
// gets a plain 400 immediately, not the elevation dialog first — burning a
// manager's PIN on a request that would 400 anyway either way is a needless
// cost. An admin session is denied lockoutAction (super_admin-only), so
// pre-fix ordering would have rendered the elevation prompt here instead.
func TestPermissionSettingsPage_POST_ValidatesBeforeElevating(t *testing.T) {
	mux, _ := newPermissionSettingsTestDeps(t)

	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/users/permissions",
		formBody("role=not-a-real-role&action=refund&granted=1")), auth.User{ID: "u1", Role: "admin"})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown role (validated before elevation), got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("expected NO elevation prompt for a request that fails input validation, got: %s", rec.Body.String())
	}
}

// ut-docs#557 review Fix 3: the elevation prompt must show a human-readable,
// visible description of the SPECIFIC role/permission change being
// approved — not just a generic "manager approval required" — since this
// page's elevated action is a PERMANENT permission grant/revoke, worst of
// the three checkOrElevate call sites to approve blind.
func TestPermissionSettingsPage_POST_ElevationPromptShowsSpecificSummary(t *testing.T) {
	mux, _ := newPermissionSettingsTestDeps(t)

	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/users/permissions",
		formBody("role=manager&action=refund&granted=0")), auth.User{ID: "u1", Role: "admin"})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "elevation-summary") {
		t.Fatalf("expected a visible elevation-summary line, got: %s", body)
	}
	// role=manager, action=refund, granted=0 (revoke): the summary must
	// name BOTH the role and the action, not a generic phrase.
	if !strings.Contains(body, "manager") || !strings.Contains(body, "Refund") {
		t.Fatalf("expected the summary to name the specific role (manager) and action (Refund), got: %s", body)
	}
}

// ADR-0115 §1 (ut-docs#2755): the role permission matrix is shop-wide
// configuration the main till owns; on an additional till a change would
// be reverted by the next admin-bundle pull, so it is refused up front
// with a 409 whose text/html body app.js force-swaps into #perm-msg.
func TestPermissionSettingsPage_POST_RefusedOnReplica(t *testing.T) {
	mux, dp := newPermissionSettingsTestDeps(t)
	dp.Settings = settings.NewStore(dp.Db)
	setReplicaSettings(t, dp.Settings, "http://192.0.2.1:8080", "b-123")

	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/users/permissions",
		formBody("role=manager&action=refund&granted=0")), auth.User{ID: "sa-1", Role: "super_admin"})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("replica POST = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html so htmx swaps the message in", ct)
	}
	if want := httpx.T("en", "users.error.replica_use_primary"); !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("body %q missing %q", rec.Body.String(), want)
	}
	if granted, err := data.NewAuthRepo(dp.Db).HasPermission(t.Context(), "manager", "refund"); err != nil || !granted {
		t.Fatalf("a replica must not change the matrix, granted=%v err=%v", granted, err)
	}
}

// ut-docs#3132: the matrix renders one rowgroup heading per group in
// permissionGroups order, a plain-language description under each action,
// and the menu entries each action unlocks (derived from uislot).
func TestPermissionSettingsPage_GET_GroupedDescribedWithUnlocks(t *testing.T) {
	mux, _ := newPermissionSettingsTestDeps(t)

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/users/permissions", nil), auth.User{ID: "sa-1", Role: "super_admin"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Group headings, in table order, each a rowgroup header spanning the grid.
	headRe := regexp.MustCompile(`<th scope="rowgroup" colspan="(\d+)"[^>]*>\s*<span[^>]*>([^<]+)</span>`)
	var got []string
	for _, m := range headRe.FindAllStringSubmatch(body, -1) {
		if m[1] != "5" { // action column + 4 seeded roles
			t.Errorf("group heading %q spans %s columns, want 5", m[2], m[1])
		}
		got = append(got, strings.TrimSpace(m[2]))
	}
	want := []string{"Sales", "Catalog", "Stock", "Reports", "Staff", "Settings", "Plugins", "System"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("group headings = %v, want %v", got, want)
	}
	// An action row sits under its own group: refund after Sales, before Catalog.
	sales, catalog := strings.Index(body, ">Sales<"), strings.Index(body, ">Catalog<")
	refund := strings.Index(body, ">Refunds<")
	if !(sales < refund && refund < catalog) {
		t.Fatalf("Refunds row not inside the Sales group (sales=%d refunds=%d catalog=%d)", sales, refund, catalog)
	}

	// A description under an action label.
	if !strings.Contains(body, httpx.T("en", "permissions.action_desc.reports")) {
		t.Fatalf("expected the reports description, got: %s", body)
	}
	// The Unlocks list: reports → the Reports tile.
	if !strings.Contains(body, "Unlocks: Reports<") {
		t.Fatalf(`expected "Unlocks: Reports" under the reports action, got: %s`, body)
	}
	// Composite fiscal tiles only show where the shop has them (not DE here).
	if strings.Contains(body, httpx.T("en", "fiscalregister.title")) {
		t.Fatalf("the Germany-only fiscal register tile must not be listed for a non-DE shop: %s", body)
	}
}

// ut-docs#3132: an action the DB has but permissionGroups doesn't place
// still renders (in a trailing "Other" group) — the page never hides a
// grantable permission; TestPermissionGroups_EveryActionInExactlyOneGroup
// is what fails CI for the gap.
func TestPermissionSettingsPage_GET_UngroupedActionRendersUnderOther(t *testing.T) {
	mux, dp := newPermissionSettingsTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO permission_actions(action) VALUES ('zz_unplaced_action')`); err != nil {
		t.Fatalf("seed action: %v", err)
	}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/users/permissions", nil), auth.User{ID: "sa-1", Role: "super_admin"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	other := strings.Index(body, ">Other<")
	system := strings.Index(body, ">System<")
	row := strings.Index(body, `&#34;action&#34;:&#34;zz_unplaced_action&#34;`)
	if other < 0 || row < 0 || !(system < other && other < row) {
		t.Fatalf("expected a trailing Other group holding zz_unplaced_action (system=%d other=%d row=%d): %s", system, other, row, body)
	}
}

// ut-docs#3134: an action in permissionHiddenActions is not rendered — not
// in its old group, and not in the trailing "Other" fallback either —
// while its stored grants stay untouched.
func TestPermissionSettingsPage_GET_HiddenActionsNotRendered(t *testing.T) {
	mux, dp := newPermissionSettingsTestDeps(t)

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/users/permissions", nil), auth.User{ID: "sa-1", Role: "super_admin"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, a := range []string{"void", "price_override"} {
		if strings.Contains(body, `&#34;action&#34;:&#34;`+a+`&#34;`) {
			t.Errorf("hidden action %q still renders a checkbox", a)
		}
	}
	if strings.Contains(body, ">Other<") {
		t.Error("hidden actions must not fall into the Other group")
	}
	for _, a := range []string{"refund", "cash_adjustment"} {
		if !strings.Contains(body, `&#34;action&#34;:&#34;`+a+`&#34;`) {
			t.Errorf("enforced action %q must still render", a)
		}
	}
	var granted int
	if err := dp.Db.QueryRow(`SELECT granted FROM role_permissions WHERE role='manager' AND action='void'`).Scan(&granted); err != nil || granted != 1 {
		t.Fatalf("stored grant for manager/void must be untouched: granted=%d err=%v", granted, err)
	}
}
