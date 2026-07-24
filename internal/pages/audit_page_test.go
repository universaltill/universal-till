package pages

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
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
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db)}
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
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db)}
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
