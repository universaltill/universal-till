package plugins

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// ADR-0129 §2/§3, slice 2b (ut-docs#3281): the till persists a plugin's
// declared `provides` and `markets` at install, replaces them on update and
// rollback, drops them on uninstall, and answers "who provides X" through
// data.PluginRepo.PluginsProviding. `fiscal.*` capabilities are exclusive
// across INSTALLED plugins (active or disabled): a second provider is
// refused at PersistManifest and Rollback (here) and at /enable
// (internal/pages), failing closed on a DB error — the ADR-0106 preset
// pattern (layout_preset_exclusivity_test.go).

func providesManifest(id string, provides []string, markets []string) *Manifest {
	return &Manifest{
		ID:       id,
		Name:     "Plugin " + id,
		Version:  "1.0.0",
		Runtime:  "none",
		Provides: provides,
		Markets:  markets,
	}
}

func TestPersistManifest_PersistsProvidesAndMarkets(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	repo := data.NewPluginRepo(d.DB)
	m := providesManifest("com.test.register", []string{CapabilityFiscalRegister}, []string{"DE", "AT"})
	if err := PersistManifest(ctx, d.DB, m, InstallOptions{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	got, err := repo.PluginsProviding(ctx, CapabilityFiscalRegister, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"com.test.register"}) {
		t.Fatalf("PluginsProviding(fiscal.register) = %v, want [com.test.register]", got)
	}
	markets, err := pluginMarkets(t, d, "com.test.register")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(markets, []string{"AT", "DE"}) {
		t.Fatalf("persisted markets = %v, want [AT DE] (sorted)", markets)
	}
}

// An update replaces both lists: a value the new version no longer declares
// is gone, a new one is present, and an update that drops the fields
// entirely clears every row (empty markets = every market, ADR-0129 §3).
func TestPersistManifest_UpdateReplacesProvidesAndMarkets(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	repo := data.NewPluginRepo(d.DB)
	if err := PersistManifest(ctx, d.DB, providesManifest("com.test.ai", []string{CapabilityAI}, []string{"DE"}), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	v2 := providesManifest("com.test.ai", []string{CapabilityAI, "layout.shop_type:cafe"}, []string{"TR"})
	v2.Version = "1.1.0"
	if err := PersistManifest(ctx, d.DB, v2, InstallOptions{}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got, _ := repo.PluginsProviding(ctx, "layout.shop_type:cafe", false); !reflect.DeepEqual(got, []string{"com.test.ai"}) {
		t.Fatalf("new capability not persisted on update: %v", got)
	}
	if got, _ := pluginMarkets(t, d, "com.test.ai"); !reflect.DeepEqual(got, []string{"TR"}) {
		t.Fatalf("markets after update = %v, want [TR] (DE must be replaced)", got)
	}

	v3 := providesManifest("com.test.ai", nil, nil)
	v3.Version = "1.2.0"
	if err := PersistManifest(ctx, d.DB, v3, InstallOptions{}); err != nil {
		t.Fatalf("update dropping the fields: %v", err)
	}
	if got, _ := repo.PluginsProviding(ctx, CapabilityAI, false); len(got) != 0 {
		t.Fatalf("an update that no longer declares provides must clear its rows, still providing: %v", got)
	}
	if got, _ := pluginMarkets(t, d, "com.test.ai"); len(got) != 0 {
		t.Fatalf("an update that no longer declares markets must clear its rows, got %v", got)
	}
}

func TestUninstallPlugin_RemovesProvidesAndMarkets(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	if err := PersistManifest(ctx, d.DB, providesManifest("com.test.device", []string{CapabilityFiscalDevice}, []string{"TR"}), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := UninstallPlugin(ctx, d.DB, "com.test.device"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	for _, table := range []string{"plugin_provides", "plugin_markets"} {
		var n int
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE plugin_id = 'com.test.device'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("uninstall left %d %s row(s), want 0", n, table)
		}
	}
}

// activeOnly=false includes a disabled provider (fiscal.register reads the
// installed provider so a disabled plugin's register rows still sync —
// ADR-0129 §2); activeOnly=true leaves it out. Results are sorted by ID.
func TestPluginsProviding_ActiveVersusInstalled(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	repo := data.NewPluginRepo(d.DB)
	for _, id := range []string{"com.b.ai", "com.a.ai"} {
		if err := PersistManifest(ctx, d.DB, providesManifest(id, []string{CapabilityAI}, nil), InstallOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.SetPluginActive(ctx, nil, "com.b.ai", false); err != nil {
		t.Fatal(err)
	}
	installed, err := repo.PluginsProviding(ctx, CapabilityAI, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(installed, []string{"com.a.ai", "com.b.ai"}) {
		t.Fatalf("installed providers = %v, want [com.a.ai com.b.ai]", installed)
	}
	active, err := repo.PluginsProviding(ctx, CapabilityAI, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(active, []string{"com.a.ai"}) {
		t.Fatalf("active providers = %v, want [com.a.ai]", active)
	}
}

// A second INSTALLED provider of the same fiscal.* capability is refused,
// naming the incumbent, and the refused install rolls back entirely.
func TestPersistManifest_RefusesSecondFiscalProvider(t *testing.T) {
	for _, capability := range []string{CapabilityFiscalRegister, CapabilityFiscalDevice} {
		t.Run(capability, func(t *testing.T) {
			d := openRealDB(t)
			ctx := context.Background()
			if err := PersistManifest(ctx, d.DB, providesManifest("com.first.fiscal", []string{capability}, nil), InstallOptions{}); err != nil {
				t.Fatalf("first provider must install: %v", err)
			}
			err := PersistManifest(ctx, d.DB, providesManifest("com.second.fiscal", []string{capability}, nil), InstallOptions{})
			if err == nil {
				t.Fatalf("a second installed provider of %s must be refused (ADR-0129 §2)", capability)
			}
			if !strings.Contains(err.Error(), "com.first.fiscal") || !strings.Contains(err.Error(), "Plugin com.first.fiscal") || !strings.Contains(err.Error(), capability) {
				t.Fatalf("refusal must name the incumbent (id and name) and the capability, got: %v", err)
			}
			var n int
			if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.second.fiscal'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("refused install left %d plugins row(s), want 0", n)
			}
		})
	}
}

// Exclusivity counts INSTALLED providers, not only active ones: a disabled
// incumbent still owns the §146a register namespace (ADR-0129 §2).
func TestPersistManifest_DisabledFiscalProviderStillHoldsExclusivity(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	repo := data.NewPluginRepo(d.DB)
	if err := PersistManifest(ctx, d.DB, providesManifest("com.first.fiscal", []string{CapabilityFiscalRegister}, nil), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetPluginActive(ctx, nil, "com.first.fiscal", false); err != nil {
		t.Fatal(err)
	}
	if err := PersistManifest(ctx, d.DB, providesManifest("com.second.fiscal", []string{CapabilityFiscalRegister}, nil), InstallOptions{}); err == nil {
		t.Fatal("a disabled fiscal.register provider must still block a second install")
	}
}

// Different fiscal.* capabilities don't conflict; non-fiscal capabilities
// are never exclusive here (ai's tie-break is the reader's, slice 6); and a
// provider updating itself is not a conflict with its own rows.
func TestPersistManifest_FiscalExclusivityScope(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	if err := PersistManifest(ctx, d.DB, providesManifest("com.reg", []string{CapabilityFiscalRegister}, nil), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := PersistManifest(ctx, d.DB, providesManifest("com.dev", []string{CapabilityFiscalDevice}, nil), InstallOptions{}); err != nil {
		t.Fatalf("fiscal.device must coexist with a fiscal.register provider: %v", err)
	}
	for _, id := range []string{"com.ai.one", "com.ai.two"} {
		if err := PersistManifest(ctx, d.DB, providesManifest(id, []string{CapabilityAI}, nil), InstallOptions{}); err != nil {
			t.Fatalf("ai is not exclusive at install: %v", err)
		}
	}
	upd := providesManifest("com.reg", []string{CapabilityFiscalRegister}, []string{"DE"})
	upd.Version = "1.0.1"
	if err := PersistManifest(ctx, d.DB, upd, InstallOptions{}); err != nil {
		t.Fatalf("a fiscal provider updating itself must not conflict with its own rows: %v", err)
	}
}

// Fail CLOSED (ADR-0129 §2, ADR-0106 pattern): with plugin_provides gone
// the ownership query errors, and the install is refused by the check
// itself rather than let through.
func TestPersistManifest_FiscalExclusivityFailsClosedOnDBError(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	mustExecSQL(t, d, `DROP TABLE plugin_provides`)
	err := PersistManifest(ctx, d.DB, providesManifest("com.fiscal", []string{CapabilityFiscalRegister}, nil), InstallOptions{})
	if err == nil {
		t.Fatal("a DB error during the fiscal exclusivity check must refuse the install")
	}
	if !strings.Contains(err.Error(), "fiscal capability exclusivity") {
		t.Fatalf("the refusal must come from the exclusivity check itself, got: %v", err)
	}
}

// pluginMarkets reads pluginID's persisted markets, sorted (no production
// reader exists until ADR-0129 slice 6, ut-docs#3180).
func pluginMarkets(t *testing.T, d *db.DB, pluginID string) ([]string, error) {
	t.Helper()
	rows, err := d.DB.Query(`SELECT market FROM plugin_markets WHERE plugin_id = ? ORDER BY market`, pluginID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// seedRollbackTarget writes a version manifest into the RollbackManager's
// versions/ tree (the catalog row is seeded by the caller).
func seedRollbackTarget(t *testing.T, base, id, version, manifest string) {
	t.Helper()
	dir := filepath.Join(base, id, "versions", version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Rollback writes plugin rows without PersistManifest, so it must both
// rewrite provides/markets from the target manifest and refuse a target
// that would become a second fiscal.* provider.
func TestRollback_RewritesProvidesAndMarkets(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	base := t.TempDir()
	repo := data.NewPluginRepo(d.DB)
	v2 := providesManifest("com.rb.ai", []string{CapabilityAI}, []string{"TR"})
	v2.Version = "2.0.0"
	if err := PersistManifest(ctx, d.DB, v2, InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	mustExecSQL(t, d, `INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
VALUES ('com.rb.ai', '1.0.0', 'RB', 'none', '', 'https://example.invalid', 'deadbeef', '0.0.1', '1', '2026-09-30T00:00:00Z')`)
	seedRollbackTarget(t, base, "com.rb.ai", "1.0.0",
		`{"id":"com.rb.ai","name":"RB","version":"1.0.0","runtime":"none","markets":["DE"]}`)

	if err := NewRollbackManager(d.DB, base).Rollback(ctx, "com.rb.ai", "1.0.0", "tester"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got, _ := repo.PluginsProviding(ctx, CapabilityAI, false); len(got) != 0 {
		t.Fatalf("rollback to a version without provides must clear them, got %v", got)
	}
	if got, _ := pluginMarkets(t, d, "com.rb.ai"); !reflect.DeepEqual(got, []string{"DE"}) {
		t.Fatalf("rollback must restore the target's markets, got %v", got)
	}
}

func TestRollback_RefusesRestoringASecondFiscalProvider(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	base := t.TempDir()
	repo := data.NewPluginRepo(d.DB)
	if err := PersistManifest(ctx, d.DB, providesManifest("com.owner.register", []string{CapabilityFiscalRegister}, nil), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	v2 := providesManifest("com.rb.plugin", nil, nil)
	v2.Version = "2.0.0"
	if err := PersistManifest(ctx, d.DB, v2, InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	mustExecSQL(t, d, `INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
VALUES ('com.rb.plugin', '1.0.0', 'RB', 'none', '', 'https://example.invalid', 'deadbeef', '0.0.1', '1', '2026-09-30T00:00:00Z')`)
	seedRollbackTarget(t, base, "com.rb.plugin", "1.0.0",
		`{"id":"com.rb.plugin","name":"RB","version":"1.0.0","runtime":"none","provides":["fiscal.register"]}`)

	err := NewRollbackManager(d.DB, base).Rollback(ctx, "com.rb.plugin", "1.0.0", "tester")
	if err == nil {
		t.Fatal("rollback restored a second fiscal.register provider")
	}
	if !strings.Contains(err.Error(), "com.owner.register") {
		t.Fatalf("rollback refusal must name the incumbent, got: %v", err)
	}
	if got, _ := repo.PluginsProviding(ctx, CapabilityFiscalRegister, false); !reflect.DeepEqual(got, []string{"com.owner.register"}) {
		t.Fatalf("a refused rollback must leave provides untouched, got %v", got)
	}
}
