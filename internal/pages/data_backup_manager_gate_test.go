package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Positive counterpart to TestDataAPI_AllEndpointsRequireManager and
// TestBackupAPI_AllEndpointsRequireManager (ut-docs#707 — canPerform()/
// Auth.Can() against the new data_management action, not just the
// no-session short-circuit those tests exercise). A real cashier session
// must still be denied by the actual permission check; a real manager
// session must get PAST the auth gate. Several of these endpoints then fail
// for unrelated reasons under this minimal fixture (missing form fields,
// unknown batch/backup name) -- this only asserts the response isn't 403,
// same scope as ut-docs#706's TestPluginManagementEndpoints_RealSessionGatesByRole.
func TestDataManagementEndpoints_RealSessionGatesByRole(t *testing.T) {
	chdirRoot(t)
	dbConn := openPagesTestDB(t)
	defer dbConn.Close()
	seedForPages(t, dbConn) // role_permissions must exist for AuthSvc.Can() to answer

	dp := &common.Deps{Cfg: &config.Config{}, Db: dbConn, AuthSvc: auth.NewService(dbConn)}
	mux := http.NewServeMux()
	registerDataAPI(mux, dp)

	cases := []struct {
		name, method, path string
	}{
		{"reset-transactions", http.MethodPost, "/api/data/reset-transactions"},
		{"reset-archives list", http.MethodGet, "/api/data/reset-archives"},
		{"reset-archives restore", http.MethodPost, "/api/data/reset-archives/some-batch/restore"},
		{"reset-archives purge", http.MethodPost, "/api/data/reset-archives/some-batch/purge"},
		{"customers search", http.MethodGet, "/api/data/customers"},
		{"customers erase", http.MethodPost, "/api/data/customers/erase"},
		{"obsolete-items", http.MethodGet, "/api/data/obsolete-items"},
		{"cleanup-catalog", http.MethodPost, "/api/data/cleanup-catalog"},
		{"export", http.MethodPost, "/api/data/export"},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/cashier_denied", func(t *testing.T) {
			req := auth.WithUser(httptest.NewRequest(tc.method, tc.path, nil), auth.User{ID: "u1", Role: "cashier"})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s cashier = %d, want 403", tc.method, tc.path, rec.Code)
			}
		})
		for _, role := range []string{"manager", "admin", "super_admin"} {
			t.Run(tc.name+"/"+role+"_past_gate", func(t *testing.T) {
				req := auth.WithUser(httptest.NewRequest(tc.method, tc.path, nil), auth.User{ID: "u1", Role: role})
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				if rec.Code == http.StatusForbidden {
					t.Fatalf("%s %s %s = 403, want past the auth gate (fixture has no request body/plugin, so a non-403 failure downstream is fine)", tc.method, tc.path, role)
				}
			})
		}
	}
}

// Same shape as TestDataManagementEndpoints_RealSessionGatesByRole for
// backup_api.go's endpoints, which share the same data_management action
// ("Manager/admin only throughout", backup_api.go's own doc comment). Uses
// a real on-disk DB via internal/db.Open (like newBackupTestDeps in
// backup_api_test.go) since db.Snapshot/BackupDir need a real file path --
// db.Open already runs the production migrations, so role_permissions is
// seeded without a separate seedForPages call (that would collide with
// tables db.Open's migrate() already created).
func TestBackupEndpoints_RealSessionGatesByRole(t *testing.T) {
	chdirRoot(t)
	i18n, err := config.NewI18n("web/locales", "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	dbPath := t.TempDir() + "/unitill-pos.db"
	conn, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	dp := &common.Deps{Cfg: &config.Config{DBPath: dbPath}, Db: conn.DB, AuthSvc: auth.NewService(conn.DB)}
	mux := http.NewServeMux()
	registerBackupAPI(mux, dp)

	cases := []struct{ name, method, path string }{
		{"backup now", http.MethodPost, "/api/backup/now"},
		{"backup download", http.MethodGet, "/api/backup/download/some.db"},
		{"backup save-copy", http.MethodPost, "/api/backup/save-copy/some.db"},
		{"backup restore", http.MethodPost, "/api/backup/restore"},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/cashier_denied", func(t *testing.T) {
			req := auth.WithUser(httptest.NewRequest(tc.method, tc.path, nil), auth.User{ID: "u1", Role: "cashier"})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s cashier = %d, want 403", tc.method, tc.path, rec.Code)
			}
		})
		for _, role := range []string{"manager", "admin", "super_admin"} {
			t.Run(tc.name+"/"+role+"_past_gate", func(t *testing.T) {
				req := auth.WithUser(httptest.NewRequest(tc.method, tc.path, nil), auth.User{ID: "u1", Role: role})
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				if rec.Code == http.StatusForbidden {
					t.Fatalf("%s %s %s = 403, want past the auth gate (fixture has no real backup file, so a non-403 failure downstream is fine)", tc.method, tc.path, role)
				}
			})
		}
	}
}
