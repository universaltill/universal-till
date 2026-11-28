package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#7202: a hidden tile whose own VisibleIf gate is false for the
// viewer (Locations/Registers without stock_location_management) renders no
// tile either way, so a Restore row for it does nothing visible. The page
// skips such rows.
func TestMenuLayoutSettings_SkipsHiddenRowsTheViewerCannotSee_7202(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	pm, err := plugins.Init(t.Context(), d.Cfg, d.Db)
	if err != nil {
		t.Fatal(err)
	}
	d.Pm = pm
	registerMenuLayoutSettings(mux, d)
	m := &plugins.Manifest{
		ID: "com.example.hider", Name: "Hider layout", Version: "1.0.0", Runtime: "none", CanonicalType: "layout",
		Entries: []plugins.ManifestEntry{{Type: "layout", Key: "menu", Label: "Hider", Config: map[string]any{
			"slot": "menu", "amendments": []any{
				map[string]any{"key": "/tables", "hide": true},
				map[string]any{"key": "/locations", "hide": true},
			},
		}}},
	}
	if err := plugins.PersistManifest(t.Context(), d.Db, m, plugins.InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := d.ReloadPlugins(t.Context()); err != nil {
		t.Fatal(err)
	}
	get := func() string {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings/menu", nil), mgrUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /settings/menu = %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	if body := get(); !strings.Contains(body, `name="key" value="/locations"`) {
		t.Fatalf("precondition: manager with the permission sees the Locations row: %s", body)
	}
	if err := data.NewAuthRepo(d.Db).SetRolePermission(t.Context(), nil, "manager", "stock_location_management", false); err != nil {
		t.Fatal(err)
	}
	body := get()
	if strings.Contains(body, `name="key" value="/locations"`) {
		t.Errorf("Locations row must be skipped when its gate is off: %s", body)
	}
	if !strings.Contains(body, `name="key" value="/tables"`) {
		t.Errorf("a row whose tile is visible stays: %s", body)
	}
}
