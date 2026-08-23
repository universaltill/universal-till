package pages

import (
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// A cashier (or no session at all) must not see the audit trail; a manager
// gets a real render of the seeded entries, filter form, and actor names
// resolved via the users join.
func TestAuditPage_ManagerOnlyAndRendersRealData(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	if _, err := db.Exec(`INSERT INTO users(id, username, display_name, pin_hash, role, created_at) VALUES ('mgr-1','manager1','Manager One','x','manager','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed manager user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO audit_log(id, actor_id, entity_type, entity_id, action, data_json, created_at) VALUES ('a1','mgr-1','plugin','com.x.faq','plugin_install','{"version":"1.0.0"}','2026-01-02T10:00:00Z')`); err != nil {
		t.Fatalf("seed audit entry: %v", err)
	}

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db), AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerAuditPage(mux, dp)

	get := func(u *auth.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/audit", nil)
		if u != nil {
			req = auth.WithUser(req, *u)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := get(nil); rec.Code != http.StatusForbidden {
		t.Fatalf("no session = %d, want 403", rec.Code)
	}
	if rec := get(&auth.User{ID: "cashier-1", Role: "cashier"}); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier = %d, want 403", rec.Code)
	}

	rec := get(&auth.User{ID: "mgr-1", Role: "manager"})
	if rec.Code != http.StatusOK {
		t.Fatalf("manager = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"plugin_install", "com.x.faq", "Manager One"} {
		if !strings.Contains(body, want) {
			t.Fatalf("audit page missing %q in rendered output", want)
		}
	}
}

// A repo failure on GET /audit must never leak the raw Go/SQL error to the
// operator (ut-docs#893, the wider sweep #316 deferred) — it goes through
// common.LogAndLocalizedError like catalog/handlers.go's sites already do.
func TestAuditPage_RepoErrorNeverLeaksRawErrorToBody(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	seedForPages(t, db)

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db), AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerAuditPage(mux, dp)

	if _, err := db.Exec(`INSERT INTO users(id, username, display_name, pin_hash, role, created_at) VALUES ('mgr-1','manager1','Manager One','x','manager','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed manager user: %v", err)
	}
	// Force ListAudit specifically to fail with a raw driver error, without
	// breaking canPerform's own DB-backed permission check (a different
	// table) — proves the repo-error path, not the auth gate, is what's
	// under test here.
	if _, err := db.Exec(`DROP TABLE audit_log`); err != nil {
		t.Fatalf("drop audit_log: %v", err)
	}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/audit", nil), auth.User{ID: "mgr-1", Role: "manager"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "no such table") || strings.Contains(body, "audit_log") {
		t.Fatalf("response leaked the raw driver error: %q", body)
	}
	if want := httpx.T("en", "audit.error.server"); strings.TrimSpace(body) != want {
		t.Fatalf("expected the translated message %q, got %q", want, body)
	}
}

// super_admin is #554/#555's noted broadening vs. the old isManagerOrAuthOff
// gate (which only recognized manager/admin) — accepted and inert since
// nothing in the codebase creates a super_admin-role user today. Pin it
// explicitly on the audit trail, the most sensitive of #709's 5 gated pages.
func TestAuditPage_SuperAdminGranted(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db), AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerAuditPage(mux, dp)

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/audit", nil), auth.User{ID: "sa-1", Role: "super_admin"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("super_admin = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// The entity_type/actor_id/action/date filters actually narrow the results
// (not just accepted and ignored).
func TestAuditPage_FiltersNarrowResults(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	if _, err := db.Exec(`INSERT INTO audit_log(id, actor_id, entity_type, entity_id, action, data_json, created_at) VALUES
		('a1', NULL, 'plugin', 'p1', 'plugin_install', NULL, '2026-01-01T10:00:00Z'),
		('a2', NULL, 'sale', 's1', 'void', NULL, '2026-01-01T11:00:00Z')`); err != nil {
		t.Fatalf("seed audit entries: %v", err)
	}

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db), AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerAuditPage(mux, dp)

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/audit?entity_type=plugin", nil), auth.User{ID: "mgr-1", Role: "manager"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered request = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "plugin_install") {
		t.Fatal("expected the plugin entry in filtered results")
	}
	if strings.Contains(body, "void") {
		t.Fatal("expected the sale entry to be filtered out by entity_type=plugin")
	}
}

func TestAuditExport_ManagerOnly(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db), AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerAuditPage(mux, dp)

	get := func(u *auth.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/audit/export", nil)
		if u != nil {
			req = auth.WithUser(req, *u)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := get(nil); rec.Code != http.StatusForbidden {
		t.Fatalf("no session = %d, want 403", rec.Code)
	}
	if rec := get(&auth.User{ID: "cashier-1", Role: "cashier"}); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier = %d, want 403", rec.Code)
	}
}

func TestAuditExport_CSVHeadersFiltersAndAuditEntry(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	if _, err := db.Exec(`INSERT INTO users(id, username, display_name, pin_hash, role, created_at) VALUES ('mgr-1','manager1','Manager One','x','manager','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed manager user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO audit_log(id, actor_id, entity_type, entity_id, action, data_json, created_at) VALUES
		('a1', 'mgr-1', 'plugin', 'com.x.faq', 'plugin_install', '{"version":"1.0.0"}', '2026-01-02T10:00:00Z'),
		('a2', NULL, 'sale', 's1', 'void', NULL, '2026-01-02T11:00:00Z')`); err != nil {
		t.Fatalf("seed audit entries: %v", err)
	}

	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db), AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerAuditPage(mux, dp)

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/api/audit/export?entity_type=plugin", nil),
		auth.User{ID: "mgr-1", Role: "manager"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Fatalf("expected a CSV content type, got %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, `attachment; filename="audit-log-`) {
		t.Fatalf("expected an audit-log attachment filename, got %q", got)
	}

	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("expected valid CSV, got parse error: %v\nbody:\n%s", err, rec.Body.String())
	}
	if len(rows) != 2 { // header + the one plugin entry (sale entry filtered out)
		t.Fatalf("expected header + 1 filtered row, got %d rows: %v", len(rows), rows)
	}
	if got := rows[0]; got[0] != "When" || got[1] != "Actor" || got[4] != "Action" {
		t.Fatalf("unexpected CSV header row: %v", got)
	}
	if got := rows[1]; got[1] != "Manager One" || got[4] != "plugin_install" {
		t.Fatalf("expected the manager's plugin_install row, got %v", got)
	}

	repo := data.NewPOSRepo(db)
	entries, err := repo.ListAudit(req.Context(), data.AuditFilters{Action: "audit_exported"})
	if err != nil {
		t.Fatalf("ListAudit for audit_exported: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected the export itself to write one audit_exported entry, got %d", len(entries))
	}
}

// ut-docs#195: Actor (a manager's own free-typed display_name), Entity ID,
// and Action (plugin code can supply an arbitrary string via
// PluginRepo.InsertAuditRaw) must come out defused (leading apostrophe)
// rather than reaching Excel/LibreOffice as a live formula.
func TestAuditExport_FormulaShapedFieldsAreCSVSafe(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	const malicious = `=cmd|'/c calc'!A1`
	if _, err := db.Exec(`INSERT INTO users(id, username, display_name, pin_hash, role, created_at) VALUES ('mgr-2','manager2',?,'x','manager','2026-01-01T00:00:00Z')`, malicious); err != nil {
		t.Fatalf("seed manager user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO audit_log(id, actor_id, entity_type, entity_id, action, data_json, created_at) VALUES
		('a3', 'mgr-2', 'plugin', ?, ?, NULL, '2026-01-03T10:00:00Z')`, malicious, malicious); err != nil {
		t.Fatalf("seed audit entry: %v", err)
	}

	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db), AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerAuditPage(mux, dp)

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/api/audit/export?entity_type=plugin&actor_id=mgr-2", nil),
		auth.User{ID: "mgr-2", Role: "manager"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("expected valid CSV, got parse error: %v\nbody:\n%s", err, rec.Body.String())
	}
	if len(rows) != 2 {
		t.Fatalf("expected header + 1 data row, got %d: %v", len(rows), rows)
	}
	const actorCol, entityIDCol, actionCol = 1, 3, 4
	got := rows[1]
	want := "'" + malicious
	if got[actorCol] != want {
		t.Fatalf("Actor not defused: got %q, want %q", got[actorCol], want)
	}
	if got[entityIDCol] != want {
		t.Fatalf("Entity ID not defused: got %q, want %q", got[entityIDCol], want)
	}
	if got[actionCol] != want {
		t.Fatalf("Action not defused: got %q, want %q", got[actionCol], want)
	}
}

func TestAuditExportFilename_TruncationIsVisibleNotJustAHeader(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	if got, want := auditExportFilename(now, false, 42), "audit-log-2026-08-01"; got != want {
		t.Fatalf("untruncated filename = %q, want %q", got, want)
	}
	got := auditExportFilename(now, true, 9999)
	if !strings.HasPrefix(got, "audit-log-2026-08-01") {
		t.Fatalf("truncated filename lost its date prefix: %q", got)
	}
	if !strings.Contains(got, "TRUNCATED") || !strings.Contains(got, "9999") {
		t.Fatalf("expected the truncated filename to visibly say so and carry the row count, got %q", got)
	}
}

// A genuinely NULL actor_id (a plugin-originated entry, as opposed to the
// seeded 'system' user some deployments have) must fall back to a literal
// "system" in the CSV, not an empty cell.
func TestAuditExport_NullActorFallsBackToSystemLiteral(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	if _, err := db.Exec(`INSERT INTO audit_log(id, actor_id, entity_type, entity_id, action, data_json, created_at) VALUES
		('a1', NULL, 'plugin', 'p1', 'plugin_install', NULL, '2026-01-01T10:00:00Z')`); err != nil {
		t.Fatalf("seed audit entry: %v", err)
	}

	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db), AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerAuditPage(mux, dp)

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/api/audit/export", nil), auth.User{ID: "mgr-1", Role: "manager"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("expected valid CSV, got parse error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected header + 1 row, got %d: %v", len(rows), rows)
	}
	if got := rows[1][1]; got != "system" {
		t.Fatalf("expected a NULL-actor entry to export Actor=%q, got %q", "system", got)
	}
}
