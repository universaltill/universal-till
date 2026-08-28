package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// TestMissingTaxRateSwitcher covers missingTaxRateSwitcher's decision
// cases (ADR-0068): unlike missingFiscalSigner, this is NOT gated on
// fiscal.system_of_record — VAT-rate correctness applies regardless of
// fiscal-signing status, so only country and tax.rate.ask subscriber
// state matter here.
func TestMissingTaxRateSwitcher(t *testing.T) {
	cases := []struct {
		name       string
		country    string
		seedTax    bool // seed an active com.universaltill.tax-uk plugin holding tax.rate.ask
		seedBroken bool // mark that plugin install_state='broken' after seeding
		want       bool
	}{
		{
			name:    "non-DE country ignored regardless of plugin state",
			country: "GB",
			seedTax: false,
			want:    false,
		},
		{
			name:    "DE, no active tax.rate.ask plugin at all",
			country: "DE",
			seedTax: false,
			want:    true,
		},
		{
			name:    "DE, an active plugin holds tax.rate.ask",
			country: "DE",
			seedTax: true,
			want:    false,
		},
		{
			name:       "DE, active plugin holds tax.rate.ask but is install_state=broken",
			country:    "DE",
			seedTax:    true,
			seedBroken: true,
			want:       true,
		},
		{
			name:    "non-DE with an active tax.rate.ask plugin is still ignored",
			country: "GB",
			seedTax: true,
			want:    false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := openPagesTestDB(t)
			defer db.Close()
			seedForPages(t, db)

			if c.seedTax {
				seedTaxPlugin(t, db)
			}
			if c.seedBroken {
				markPluginBroken(t, db, "com.universaltill.tax-uk")
			}

			dp := &common.Deps{Db: db}
			got, err := missingTaxRateSwitcher(context.Background(), dp, c.country)
			if err != nil {
				t.Fatalf("missingTaxRateSwitcher: %v", err)
			}
			if got != c.want {
				t.Fatalf("missingTaxRateSwitcher() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestSettingsShowsTaxRateSwitcherMissingBanner is the render-level twin of
// TestSettingsShowsFiscalSignerMissingBanner (settings_page_test.go), added
// in review: TestMissingTaxRateSwitcher above only exercises the predicate,
// so nothing was proving the predicate is actually WIRED — a typo in
// settings_page.go's template-data key, or in settings.html's `{{ if
// .missingTaxRateSwitcher }}`, would silently render no banner at all with
// every other test still green. It also pins the two behavioural promises
// the manual makes about this banner: never dismissable (no endpoint, no
// control in its markup), and it clears itself the moment a working
// tax.rate.ask plugin is active.
func TestSettingsShowsTaxRateSwitcherMissingBanner(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	ctx := context.Background()

	// newFullAuthDeps' schema is deliberately minimal (no plugin registry);
	// add just enough of the real plugins/plugin_hooks shape — same fixture
	// the sibling fiscal-signer render test uses — for PluginRepo's joins.
	for _, stmt := range []string{
		`CREATE TABLE plugins (id TEXT PRIMARY KEY, name TEXT, version TEXT, author TEXT, is_active INTEGER DEFAULT 1, trust_level TEXT DEFAULT 'untrusted', install_state TEXT DEFAULT 'installed', runtime TEXT DEFAULT 'go', entrypoint TEXT DEFAULT '', updated_at TEXT NOT NULL DEFAULT (datetime('now')));`,
		`CREATE TABLE plugin_hooks (id TEXT PRIMARY KEY, plugin_id TEXT NOT NULL, event TEXT NOT NULL, action TEXT NOT NULL, priority INTEGER NOT NULL DEFAULT 100, is_active INTEGER NOT NULL DEFAULT 1, config_json TEXT, UNIQUE(plugin_id, event, action));`,
	} {
		if _, err := d.Db.Exec(stmt); err != nil {
			t.Fatalf("seed plugins schema: %v", err)
		}
	}

	getSettings := func() string {
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		req = auth.WithUser(req, mgrUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /settings = %d", rec.Code)
		}
		return rec.Body.String()
	}

	// Non-mandated country (the GB default): absent.
	if strings.Contains(getSettings(), `data-testid="tax-rate-switcher-missing"`) {
		t.Fatal("banner shown for a non-mandated country")
	}

	// DE with no tax.rate.ask answerer: shown, and never dismissable. Country
	// must go through the real upsert endpoint, not a direct Settings.Set —
	// missingTaxRateSwitcher reads it from d.CurrentState().Country, which
	// only /api/settings/upsert keeps in sync with the DB.
	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.country"}, "value": {"DE"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("country upsert = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	body := getSettings()
	if !strings.Contains(body, `data-testid="tax-rate-switcher-missing"`) {
		t.Fatal("banner not shown for a DE shop with no tax.rate.ask plugin")
	}
	if strings.Contains(body, "dismiss-tax-rate-switcher") {
		t.Fatal("banner must not offer any dismiss endpoint/attribute")
	}

	// An active plugin holding tax.rate.ask: the banner clears itself.
	if _, err := d.Db.ExecContext(ctx, `INSERT INTO plugins (id, name, version, install_state, entrypoint, runtime, is_active, trust_level) VALUES ('tax-de','Tax DE','1.0.0','installed','./plugin.wasm','wasm',1,'trusted')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.ExecContext(ctx, `INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES ('hook-tax-de','tax-de','tax.rate.ask','tax.rate',1)`); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(getSettings(), `data-testid="tax-rate-switcher-missing"`) {
		t.Fatal("banner still shown once a tax.rate.ask plugin is active")
	}

	// ut-docs#368 shape: that same plugin's wasm module goes broken while it
	// stays is_active — the banner must come back, not read as "installed".
	markPluginBroken(t, d.Db, "tax-de")
	if !strings.Contains(getSettings(), `data-testid="tax-rate-switcher-missing"`) {
		t.Fatal("banner not shown again once the active tax.rate.ask plugin is install_state=broken")
	}
}
