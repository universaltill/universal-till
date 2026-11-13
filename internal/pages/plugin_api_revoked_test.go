package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
)

func revokeInPages(t *testing.T, dp *common.Deps, id, version string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"revocations":[{"pluginId":"` + id + `","manifestId":"` + id + `","version":"` + version + `","action":"disable","reason":"bad"}],"latestVersion":"1"}`))
	}))
	t.Cleanup(srv.Close)
	if _, err := plugins.NewRevocationChecker(dp.Db, srv.URL, nil).SyncRevocations(context.Background()); err != nil {
		t.Fatalf("SyncRevocations: %v", err)
	}
}

func pluginRowState(t *testing.T, dp *common.Deps, id string) (int, string, string) {
	t.Helper()
	var active int
	var state, version string
	if err := dp.Db.QueryRow(`SELECT is_active, install_state, version FROM plugins WHERE id = ?`, id).Scan(&active, &state, &version); err != nil {
		t.Fatal(err)
	}
	return active, state, version
}

func errKey(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return env.Error
}

func TestEnablePlugin_RevokedRelease409(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)
	seedLayoutPluginRows(t, dp, "com.test.rev1", false, true)
	revokeInPages(t, dp, "com.test.rev1", "1.0.0")
	if a, s, _ := pluginRowState(t, dp, "com.test.rev1"); a != 0 || s != "revoked" {
		t.Fatalf("precondition: active=%d state=%s", a, s)
	}

	rec := enablePlugin(t, mux, "com.test.rev1")
	if rec.Code != http.StatusConflict || errKey(t, rec) != "plugins.manage.error.revoked" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if a, s, _ := pluginRowState(t, dp, "com.test.rev1"); a != 0 || s != "revoked" {
		t.Fatalf("row changed: active=%d state=%s", a, s)
	}
}

func TestEnablePlugin_RevokedLookupDBError500(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)
	seedLayoutPluginRows(t, dp, "com.test.rev2", false, false)
	if _, err := dp.Db.Exec(`DROP TABLE plugin_revoked_releases`); err != nil {
		t.Fatal(err)
	}
	rec := enablePlugin(t, mux, "com.test.rev2")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if pluginIsActive(t, dp, "com.test.rev2") {
		t.Fatal("must fail closed")
	}
}

func TestEnablePlugin_OtherVersionAfterUpdateSucceeds(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)
	seedLayoutPluginRows(t, dp, "com.test.rev3", false, false)
	if _, err := data.NewPluginRepo(dp.Db).ReplaceRevokedReleases(context.Background(),
		[]data.RevokedRelease{{ManifestID: "com.test.rev3", Version: "0.9.0"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if rec := enablePlugin(t, mux, "com.test.rev3"); rec.Code != http.StatusOK {
		t.Fatalf("installed v1.0.0 is not revoked, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestRollbackPlugin_Revoked409(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)
	seedLayoutPluginRows(t, dp, "com.test.rev4", false, true)
	if _, err := data.NewPluginRepo(dp.Db).ReplaceRevokedReleases(context.Background(),
		[]data.RevokedRelease{{ManifestID: "com.test.rev4", Version: "0.5.0"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The target dir must exist for the manager to reach the revoked check.
	writeRollbackTarget(t, "com.test.rev4", "0.5.0")

	req := httptest.NewRequest(http.MethodPost, "/api/plugins/com.test.rev4/rollback", strings.NewReader(`{"version":"0.5.0"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || errKey(t, rec) != "plugins.manage.error.revoked" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func writeRollbackTarget(t *testing.T, id, version string) {
	t.Helper()
	dir := filepath.Join(paths.Plugins(), id, "versions", version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := `{"id":"` + id + `","name":"RB","version":"` + version + `","entrypoint":"./run","runtime":"none","canonical_type":"page","device_arch":"any","entries":[{"type":"page","key":"rb","label":"RB","route":"/rb","menu_group":"main"}]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(m), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Tester (ut-docs#5658, AC3 end to end): v1.0.0 revoked through the feed,
// the operator updates to v1.1.0 via PersistManifest (the real install
// backstop), turns it off, and turning it back on succeeds.
func TestEnablePlugin_UpdatedVersionAfterRevokeEnables(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)
	ctx := context.Background()
	const id = "com.test.rev5"
	seedLayoutPluginRows(t, dp, id, false, true)
	revokeInPages(t, dp, id, "1.0.0")
	if rec := enablePlugin(t, mux, id); rec.Code != http.StatusConflict {
		t.Fatalf("precondition: revoked v1.0.0 must be refused, got %d %s", rec.Code, rec.Body.String())
	}
	m := &plugins.Manifest{ID: id, Name: "Rev5", Version: "1.1.0", Entrypoint: "./run"}
	if err := plugins.PersistManifest(ctx, dp.Db, m, plugins.InstallOptions{}); err != nil {
		t.Fatalf("persisting another version must succeed: %v", err)
	}
	if err := data.NewPluginRepo(dp.Db).SetPluginActive(ctx, nil, id, false); err != nil {
		t.Fatal(err)
	}
	if rec := enablePlugin(t, mux, id); rec.Code != http.StatusOK {
		t.Fatalf("enabling v1.1.0 must succeed, got %d %s", rec.Code, rec.Body.String())
	}
	if a, _, v := pluginRowState(t, dp, id); a != 1 || v != "1.1.0" {
		t.Fatalf("want active v1.1.0, got active=%d version=%s", a, v)
	}
}
