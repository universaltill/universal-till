package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// Install-time enforcement of ADR-0106 Decision C (ut-docs#1905): a
// `layout` entry whose config carries role:"preset" marks the plugin as a
// complete named arrangement, mutually exclusive with any OTHER active
// preset. Same shape and same reasoning as fiscal_sign_exclusivity_test.go:
// PersistManifest activates a plugin unconditionally, so the enable-time
// check alone would let a fresh install (or an update that newly declares
// the role) mint a second active preset without ever passing /enable.
// Checked inside PersistManifest's transaction; a refusal rolls the whole
// install back.
//
// A vertical-flavoured layout plugin (layoutManifest — the shape
// plugins/layout-salon ships) never sets role and must be completely
// unaffected (ADR-0106 Consequences).

// layoutPresetManifest is layoutManifest plus the role marker. The role is
// written as a literal here (not a constant) so this file compiles — and
// demonstrably FAILS — against the pre-ADR-0106 code too.
func layoutPresetManifest(id string, amendments ...map[string]any) *Manifest {
	m := layoutManifest(id, amendments...)
	m.Entries[0].Config["role"] = "preset"
	return m
}

func TestPersistManifest_RefusesSecondActiveLayoutPreset(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	if err := PersistManifest(ctx, d.DB, layoutPresetManifest("com.first.preset", map[string]any{"key": "/tables", "hide": true}), InstallOptions{}); err != nil {
		t.Fatalf("first preset must install cleanly: %v", err)
	}
	// A DIFFERENT key, so ADR-0088 Decision F (same-key restructuring) is
	// not what refuses this — only ADR-0106's exclusivity can.
	err := PersistManifest(ctx, d.DB, layoutPresetManifest("com.second.preset", map[string]any{"key": "/kitchen-stations", "hide": true}), InstallOptions{})
	if err == nil {
		t.Fatal("PersistManifest installed a second active layout preset — presets are mutually exclusive (ADR-0106 Decision C)")
	}
	if !strings.Contains(err.Error(), "com.first.preset") || !strings.Contains(err.Error(), "Layout com.first.preset") {
		t.Fatalf("refusal must name the incumbent preset (id and name), got: %v", err)
	}
	if !strings.Contains(err.Error(), "preset") {
		t.Fatalf("refusal must say what is exclusive, got: %v", err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.second.preset'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("refused plugin left %d plugins row(s), want 0 (transaction must roll back)", n)
	}
}

// An UPDATE of an installed vertical layout plugin whose new version starts
// declaring role:"preset" while another preset is active is refused the
// same way — the exact path the enable-time check never sees.
func TestPersistManifest_RefusesUpdateNewlyDeclaringLayoutPreset(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	if err := PersistManifest(ctx, d.DB, layoutManifest("com.vertical.layout", map[string]any{"key": "/tables", "hide": true}), InstallOptions{}); err != nil {
		t.Fatalf("install vertical layout: %v", err)
	}
	if err := PersistManifest(ctx, d.DB, layoutPresetManifest("com.owner.preset", map[string]any{"key": "/kitchen-stations", "hide": true}), InstallOptions{}); err != nil {
		t.Fatalf("install preset alongside a vertical layout: %v", err)
	}
	upgraded := layoutPresetManifest("com.vertical.layout", map[string]any{"key": "/tables", "hide": true})
	upgraded.Version = "1.0.1"
	err := PersistManifest(ctx, d.DB, upgraded, InstallOptions{})
	if err == nil {
		t.Fatal("an update newly declaring role:\"preset\" must be refused while another preset is active")
	}
	if !strings.Contains(err.Error(), "com.owner.preset") {
		t.Fatalf("refusal must name the incumbent, got: %v", err)
	}
	var version string
	if err := d.DB.QueryRow(`SELECT version FROM plugins WHERE id = 'com.vertical.layout'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "1.0.0" {
		t.Fatalf("refused update must roll back, plugin now at %q", version)
	}
}

// A preset updating / re-installing ITSELF never conflicts with its own
// prior registration.
func TestPersistManifest_LayoutPresetSelfUpdateNotAConflict(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	if err := PersistManifest(ctx, d.DB, layoutPresetManifest("com.self.preset", map[string]any{"key": "/tables", "hide": true}), InstallOptions{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	upgraded := layoutPresetManifest("com.self.preset", map[string]any{"key": "/tables", "hide": true}, map[string]any{"key": "/kitchen-stations", "hide": true})
	upgraded.Version = "1.0.1"
	if err := PersistManifest(ctx, d.DB, upgraded, InstallOptions{}); err != nil {
		t.Fatalf("a preset updating itself must not conflict with its own registration: %v", err)
	}
	var version string
	if err := d.DB.QueryRow(`SELECT version FROM plugins WHERE id = 'com.self.preset'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "1.0.1" {
		t.Fatalf("self-update must land, plugin at %q", version)
	}
}

// A vertical (non-preset) layout plugin and a preset coexist in either
// install order — ADR-0088's slots stay shared; only preset-vs-preset is
// exclusive.
func TestPersistManifest_VerticalLayoutCoexistsWithActivePreset(t *testing.T) {
	ctx := context.Background()
	t.Run("preset first", func(t *testing.T) {
		d := openRealDB(t)
		if err := PersistManifest(ctx, d.DB, layoutPresetManifest("com.example.preset", map[string]any{"key": "/tables", "hide": true}), InstallOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := PersistManifest(ctx, d.DB, layoutManifest("com.example.salon", map[string]any{"key": "/kitchen-stations", "hide": true}), InstallOptions{}); err != nil {
			t.Fatalf("a vertical layout must install alongside an active preset: %v", err)
		}
	})
	t.Run("vertical first", func(t *testing.T) {
		d := openRealDB(t)
		if err := PersistManifest(ctx, d.DB, layoutManifest("com.example.salon", map[string]any{"key": "/kitchen-stations", "hide": true}), InstallOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := PersistManifest(ctx, d.DB, layoutPresetManifest("com.example.preset", map[string]any{"key": "/tables", "hide": true}), InstallOptions{}); err != nil {
			t.Fatalf("a preset must install alongside an active vertical layout: %v", err)
		}
	})
}

// Exclusivity is between ACTIVE presets, like the fiscal-sign group: an
// installed-but-disabled preset holds nothing, so a merchant switching
// presets disables the old one and installs/enables the new one (ADR-0106
// Consequences — no new UI needed).
func TestPersistManifest_DisabledLayoutPresetDoesNotHoldExclusivity(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	if err := PersistManifest(ctx, d.DB, layoutPresetManifest("com.old.preset", map[string]any{"key": "/tables", "hide": true}), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := data.NewPluginRepo(d.DB).SetPluginActive(ctx, nil, "com.old.preset", false); err != nil {
		t.Fatal(err)
	}
	if err := PersistManifest(ctx, d.DB, layoutPresetManifest("com.new.preset", map[string]any{"key": "/kitchen-stations", "hide": true}), InstallOptions{}); err != nil {
		t.Fatalf("a second preset must install while the first is disabled: %v", err)
	}
}

// The role survives into plugin_entries.config_json verbatim — that is
// what the enable-time check (internal/pages) reads, when only the DB row
// exists and the manifest is long gone.
func TestPersistManifest_LayoutPresetRoleIsPersistedInConfigJSON(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	if err := PersistManifest(ctx, d.DB, layoutPresetManifest("com.example.preset", map[string]any{"key": "/tables", "hide": true}), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := PersistManifest(ctx, d.DB, layoutManifest("com.example.salon", map[string]any{"key": "/kitchen-stations", "hide": true}), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	var presetCfg, salonCfg string
	if err := d.DB.QueryRow(`SELECT config_json FROM plugin_entries WHERE plugin_id = 'com.example.preset' AND type = 'layout'`).Scan(&presetCfg); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.QueryRow(`SELECT config_json FROM plugin_entries WHERE plugin_id = 'com.example.salon' AND type = 'layout'`).Scan(&salonCfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(presetCfg, `"role":"preset"`) {
		t.Fatalf("preset role must be persisted in config_json, got %s", presetCfg)
	}
	if strings.Contains(salonCfg, `"role"`) {
		t.Fatalf("a vertical layout must persist no role, got %s", salonCfg)
	}
}

// A misspelled role value is refused at install (ADR-0106 B), not silently
// persisted as an ordinary shared amendment. Otherwise a preset author's
// typo would install a second arrangement next to the active preset with
// no error anywhere, which is exactly what Decision C exists to prevent.
func TestPersistManifest_RefusesUnknownLayoutRoleValue(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	if err := PersistManifest(ctx, d.DB, layoutPresetManifest("com.first.preset", map[string]any{"key": "/tables", "hide": true}), InstallOptions{}); err != nil {
		t.Fatalf("first preset must install cleanly: %v", err)
	}
	typo := layoutManifest("com.typo.preset", map[string]any{"key": "/kitchen-stations", "hide": true})
	typo.Entries[0].Config["role"] = "perset"
	err := PersistManifest(ctx, d.DB, typo, InstallOptions{})
	if err == nil {
		t.Fatal(`PersistManifest accepted role:"perset" — an unknown role value must be refused, not treated as an ordinary amendment`)
	}
	if !strings.Contains(err.Error(), "role") || !strings.Contains(err.Error(), "perset") {
		t.Fatalf("refusal must name the field and the bad value, got: %v", err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.typo.preset'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("refused plugin left %d plugins row(s), want 0", n)
	}
}

// A DB error while checking ownership fails CLOSED (ADR-0106 C, same
// posture as the fiscal-sign check): the install is refused with the error
// surfaced, never silently allowed. plugin_entries is what the ownership
// query reads, and the check runs before anything is written, so dropping
// the table makes the check itself — not a later write — the failure.
func TestPersistManifest_LayoutPresetCheckFailsClosedOnDBError(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	if err := PersistManifest(ctx, d.DB, layoutPresetManifest("com.first.preset", map[string]any{"key": "/tables", "hide": true}), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`DROP TABLE plugin_entries`); err != nil {
		t.Fatal(err)
	}
	err := PersistManifest(ctx, d.DB, layoutPresetManifest("com.second.preset", map[string]any{"key": "/kitchen-stations", "hide": true}), InstallOptions{})
	if err == nil {
		t.Fatal("a DB error during the preset exclusivity check must refuse the install")
	}
	if !strings.Contains(err.Error(), "preset exclusivity") {
		t.Fatalf("the refusal must come from the exclusivity check itself (fail closed there, not at a later write), got: %v", err)
	}
}

// Rollback writes plugin_entries without going through PersistManifest, so
// it needs the same protection (the site validateLayoutEntries already
// guards): a plugin whose CURRENT version is an ordinary layout but whose
// on-disk rollback target declared role:"preset" must not be rolled back
// into a second active preset while another plugin's preset is active.
func TestRollback_RefusesRestoringASecondActiveLayoutPreset(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	base := t.TempDir()

	if err := PersistManifest(ctx, d.DB, layoutPresetManifest("com.owner.preset", map[string]any{"key": "/kitchen-stations", "hide": true}), InstallOptions{}); err != nil {
		t.Fatalf("install the active preset: %v", err)
	}
	v2 := layoutManifest("com.rb.layout", map[string]any{"key": "/tables", "hide": true})
	v2.Version = "2.0.0"
	if err := PersistManifest(ctx, d.DB, v2, InstallOptions{}); err != nil {
		t.Fatalf("install v2 (an ordinary layout): %v", err)
	}
	mustExecSQL(t, d, `INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
VALUES ('com.rb.layout', '1.0.0', 'RB Layout', 'none', '', 'https://example.invalid', 'deadbeef', '0.0.1', '1', '2026-09-23T00:00:00Z')`)

	dir := filepath.Join(base, "com.rb.layout", "versions", "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Same hide-only amendment as v2 (hides never conflict under ADR-0088
	// F), so only the preset exclusivity can be what refuses this.
	manifest := `{"id":"com.rb.layout","name":"RB Layout","version":"1.0.0","runtime":"none","canonical_type":"layout",
"entries":[{"type":"layout","key":"menu","label":"RB Layout","config":{"slot":"menu","role":"preset","amendments":[{"key":"/tables","hide":true}]}}]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	rm := NewRollbackManager(d.DB, base)
	err := rm.Rollback(ctx, "com.rb.layout", "1.0.0", "tester")
	if err == nil {
		t.Fatal("rollback restored a role:\"preset\" entry while another plugin's preset is active")
	}
	if !strings.Contains(err.Error(), "com.owner.preset") {
		t.Fatalf("rollback refusal must name the incumbent preset, got: %v", err)
	}
	var version string
	if err := d.DB.QueryRow(`SELECT version FROM plugins WHERE id = 'com.rb.layout'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "2.0.0" {
		t.Fatalf("failed rollback must leave the current version in place, found %q", version)
	}
}
