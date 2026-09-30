package pages

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// Enable-time half of ADR-0129 §2's `fiscal.*` exclusivity (ut-docs#3281):
// enabling a plugin whose persisted provides rows carry a fiscal.*
// capability another INSTALLED plugin also provides is refused with a 409
// naming the incumbent; a DB error fails closed with a 500. The
// persist/rollback half lives in internal/plugins
// (fiscal_provides_exclusivity_test.go). Install normally refuses the
// second provider, so this guards rows that predate or bypass that check.

func seedProvidesPluginRows(t *testing.T, dp *common.Deps, pluginID string, active bool, capabilities ...string) {
	t.Helper()
	ctx := context.Background()
	activeInt := 0
	if active {
		activeInt = 1
	}
	if _, err := dp.Db.ExecContext(ctx, `
INSERT OR IGNORE INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, min_pos_version, api_version, published_at)
VALUES (?, '1.0.0', ?, 'desc', 'none', '', 'url', 'sha', 'auth', 'site', '[]', '0.0.0', '1', datetime('now'))`,
		pluginID, "Plugin "+pluginID); err != nil {
		t.Fatalf("seed plugin_catalog: %v", err)
	}
	if _, err := dp.Db.ExecContext(ctx, `
INSERT INTO plugins (id, name, version, install_state, entrypoint, runtime, is_active, trust_level)
VALUES (?, ?, '1.0.0', 'installed', '', 'none', ?, 'trusted')`,
		pluginID, "Plugin "+pluginID, activeInt); err != nil {
		t.Fatalf("seed plugins: %v", err)
	}
	for _, c := range capabilities {
		if _, err := dp.Db.ExecContext(ctx, `INSERT INTO plugin_provides (plugin_id, capability) VALUES (?, ?)`, pluginID, c); err != nil {
			t.Fatalf("seed plugin_provides: %v", err)
		}
	}
}

func TestFiscalProvidesExclusivity_EnableRefusesSecondProvider(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)
	// The incumbent is DISABLED: installed providers count, not only active.
	seedProvidesPluginRows(t, dp, "com.test.register-a", false, "fiscal.register")
	seedProvidesPluginRows(t, dp, "com.test.register-b", false, "fiscal.register")

	rec := enablePlugin(t, mux, "com.test.register-b")
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 enabling a second fiscal.register provider, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "com.test.register-a") || !strings.Contains(body, "Plugin com.test.register-a") || !strings.Contains(body, "fiscal.register") {
		t.Fatalf("refusal must name the incumbent (id and name) and the capability, got: %s", body)
	}
	if pluginIsActive(t, dp, "com.test.register-b") {
		t.Fatal("the refused plugin must stay inactive")
	}
}

// A sole provider re-enables fine, a different fiscal capability doesn't
// conflict, and non-fiscal capabilities are never exclusive at enable.
func TestFiscalProvidesExclusivity_EnableAllowsNonConflicting(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)
	seedProvidesPluginRows(t, dp, "com.test.register", false, "fiscal.register")
	seedProvidesPluginRows(t, dp, "com.test.device", false, "fiscal.device")
	seedProvidesPluginRows(t, dp, "com.test.ai-a", true, "ai")
	seedProvidesPluginRows(t, dp, "com.test.ai-b", false, "ai")

	for _, id := range []string{"com.test.register", "com.test.device", "com.test.ai-b"} {
		if rec := enablePlugin(t, mux, id); rec.Code != http.StatusOK {
			t.Fatalf("enabling %s must succeed, got %d: %s", id, rec.Code, rec.Body.String())
		}
	}
}

func TestFiscalProvidesExclusivity_EnableFailsClosedOnDBError(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)
	seedProvidesPluginRows(t, dp, "com.test.register", false, "fiscal.register")
	if _, err := dp.Db.Exec(`DROP TABLE plugin_provides`); err != nil {
		t.Fatal(err)
	}
	rec := enablePlugin(t, mux, "com.test.register")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("a DB error during the fiscal exclusivity check must refuse the enable (fail closed), got %d: %s", rec.Code, rec.Body.String())
	}
	if pluginIsActive(t, dp, "com.test.register") {
		t.Fatal("the plugin must stay inactive when the check could not run")
	}
}
