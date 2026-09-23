package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// Enable-time enforcement of ADR-0106 Decision C (ut-docs#1905): enabling a
// `layout` plugin whose persisted entry carries role:"preset" is refused
// with a 409 naming the incumbent while a DIFFERENT preset is active — the
// same shape as the fiscal-sign group's check in setPluginActiveHandler
// (fiscal_sign_hook_test.go / fiscal_sign_reconcile_test.go §4). This
// handler covers re-enabling an installed-but-disabled preset; the
// persist-time half lives in internal/plugins.

// seedLayoutPluginRows seeds a `layout` plugin straight into the tables the
// enable path reads (no manifest, no files): plugin_catalog (composite FK
// target), plugins, and ONE layout entry whose config_json optionally
// carries the ADR-0106 role marker. The role is a literal here so this file
// compiles — and demonstrably FAILS — against the pre-ADR-0106 code.
func seedLayoutPluginRows(t *testing.T, dp *common.Deps, pluginID string, preset, active bool) {
	t.Helper()
	ctx := context.Background()
	activeInt := 0
	if active {
		activeInt = 1
	}
	config := `{"slot":"menu","amendments":[{"key":"/tables","hide":true}]}`
	if preset {
		config = `{"slot":"menu","role":"preset","amendments":[{"key":"/tables","hide":true}]}`
	}
	if _, err := dp.Db.ExecContext(ctx, `
INSERT OR IGNORE INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, min_pos_version, api_version, published_at)
VALUES (?, '1.0.0', ?, 'desc', 'none', '', 'url', 'sha', 'auth', 'site', '[]', '0.0.0', '1', datetime('now'))`,
		pluginID, "Layout "+pluginID); err != nil {
		t.Fatalf("seed plugin_catalog: %v", err)
	}
	if _, err := dp.Db.ExecContext(ctx, `
INSERT INTO plugins (id, name, version, install_state, entrypoint, runtime, is_active, trust_level)
VALUES (?, ?, '1.0.0', 'installed', '', 'none', ?, 'trusted')`,
		pluginID, "Layout "+pluginID, activeInt); err != nil {
		t.Fatalf("seed plugins: %v", err)
	}
	if _, err := dp.Db.ExecContext(ctx, `
INSERT INTO plugin_entries (id, plugin_id, type, key, label, config_json, is_active)
VALUES (?, ?, 'layout', 'menu', ?, ?, 1)`, "entry-"+pluginID, pluginID, "Layout "+pluginID, config); err != nil {
		t.Fatalf("seed plugin_entries: %v", err)
	}
}

func enablePlugin(t *testing.T, mux *http.ServeMux, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/"+id+"/enable", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func pluginIsActive(t *testing.T, dp *common.Deps, id string) bool {
	t.Helper()
	var active int
	if err := dp.Db.QueryRow(`SELECT is_active FROM plugins WHERE id = ?`, id).Scan(&active); err != nil {
		t.Fatal(err)
	}
	return active == 1
}

// Enabling a second preset while one is active is refused with a 409
// naming the owning plugin (id and name); the refused plugin stays inactive.
func TestLayoutPresetExclusivity_SecondActivePresetRefused(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)
	seedLayoutPluginRows(t, dp, "com.test.preset-a", true, true)
	seedLayoutPluginRows(t, dp, "com.test.preset-b", true, false)

	rec := enablePlugin(t, mux, "com.test.preset-b")
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 enabling a second layout preset, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "com.test.preset-a") || !strings.Contains(body, "Layout com.test.preset-a") {
		t.Fatalf("refusal must name the incumbent preset (id and name), got: %s", body)
	}
	if pluginIsActive(t, dp, "com.test.preset-b") {
		t.Fatal("the refused preset must stay inactive")
	}
}

// A vertical (non-preset) layout plugin and a preset coexist at enable time
// in either direction — only preset-vs-preset is exclusive.
func TestLayoutPresetExclusivity_VerticalLayoutAndPresetCoexist(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)
	seedLayoutPluginRows(t, dp, "com.test.preset", true, true)
	seedLayoutPluginRows(t, dp, "com.test.salon", false, false)

	if rec := enablePlugin(t, mux, "com.test.salon"); rec.Code != http.StatusOK {
		t.Fatalf("a vertical layout must enable alongside an active preset, got %d: %s", rec.Code, rec.Body.String())
	}
	// Now flip it round: the vertical is active, the preset is not.
	if _, err := dp.Db.Exec(`UPDATE plugins SET is_active = 0 WHERE id = 'com.test.preset'`); err != nil {
		t.Fatal(err)
	}
	if rec := enablePlugin(t, mux, "com.test.preset"); rec.Code != http.StatusOK {
		t.Fatalf("a preset must enable alongside an active vertical layout, got %d: %s", rec.Code, rec.Body.String())
	}
	if !pluginIsActive(t, dp, "com.test.preset") || !pluginIsActive(t, dp, "com.test.salon") {
		t.Fatal("both the preset and the vertical layout must end up active")
	}
}

// Self never conflicts: re-enabling the active preset succeeds, and so does
// enabling the sole installed preset when nothing else holds the role.
func TestLayoutPresetExclusivity_SelfAndUnownedSucceed(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)
	seedLayoutPluginRows(t, dp, "com.test.preset-a", true, true)
	seedLayoutPluginRows(t, dp, "com.test.preset-b", true, false)

	if rec := enablePlugin(t, mux, "com.test.preset-a"); rec.Code != http.StatusOK {
		t.Fatalf("re-enabling the active preset must not conflict with itself, got %d: %s", rec.Code, rec.Body.String())
	}
	// The merchant's switch: disable A, then B enables fine.
	if _, err := dp.Db.Exec(`UPDATE plugins SET is_active = 0 WHERE id = 'com.test.preset-a'`); err != nil {
		t.Fatal(err)
	}
	if rec := enablePlugin(t, mux, "com.test.preset-b"); rec.Code != http.StatusOK {
		t.Fatalf("a preset must enable once the previous one is disabled, got %d: %s", rec.Code, rec.Body.String())
	}
	// ...and A is now the one refused.
	if rec := enablePlugin(t, mux, "com.test.preset-a"); rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 re-enabling A while B holds the role, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Fail CLOSED on a DB error (ADR-0106 C): the check reads plugin_entries,
// so dropping it makes the exclusivity check itself error — the enable
// must be refused with a 500 and the plugin must stay inactive, never
// silently skip the check and activate a possibly-second preset.
func TestLayoutPresetExclusivity_EnableFailsClosedOnDBError(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)
	seedLayoutPluginRows(t, dp, "com.test.preset-a", true, true)
	seedLayoutPluginRows(t, dp, "com.test.preset-b", true, false)
	if _, err := dp.Db.Exec(`DROP TABLE plugin_entries`); err != nil {
		t.Fatal(err)
	}
	rec := enablePlugin(t, mux, "com.test.preset-b")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("a DB error during the preset exclusivity check must refuse the enable (fail closed), got %d: %s", rec.Code, rec.Body.String())
	}
	if pluginIsActive(t, dp, "com.test.preset-b") {
		t.Fatal("the plugin must stay inactive when the check could not run")
	}
}
