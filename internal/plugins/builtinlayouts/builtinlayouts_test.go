package builtinlayouts

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	layoutsalon "github.com/universaltill/universal-till/plugins/layout-salon"
)

// openTestDB isolates paths.Plugins()/paths.Data() to a per-test temp dir via
// paths.Init (matching internal/secrets/keystore_test.go's own
// withTestDataDir precedent) — NOT t.Setenv("UT_DATA_DIR", ...), which is
// inert here: paths.DataDir() reads an atomic.Value paths.Init sets, never
// the environment directly. Without this, a test exercising installBuiltin's
// disk writes silently falls back to a CWD-relative "./data" and writes real
// files into this package's own source directory instead of a throwaway dir.
func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	dataDir := t.TempDir()
	paths.Init(dataDir)
	t.Cleanup(func() { paths.Init("") })
	d, err := db.Open(filepath.Join(dataDir, "unitill-pos.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// salonID resolves the embedded salon layout's id through the shop-type
// lookup, as core does (ut-docs#3178) — never a hard-coded plugin id.
func salonID(t *testing.T) string {
	t.Helper()
	id, err := PluginIDForShopType("service")
	if err != nil || id == "" {
		t.Fatalf("PluginIDForShopType(service) = %q, %v", id, err)
	}
	return id
}

func embeddedSalon(t *testing.T) *plugins.Manifest {
	t.Helper()
	m, err := plugins.ParseManifest(bytes.NewReader(layoutsalon.ManifestJSON))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSync_ServiceShopType_InstallsAndActivatesSalonLayout(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if _, err := Sync(ctx, d.DB, "service"); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	pm, err := plugins.Init(ctx, &config.Config{}, d.DB)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pm.Installed[salonID(t)]; !ok {
		t.Fatalf("want %s installed after Sync(\"service\"), got %+v", salonID(t), pm.Installed)
	}

	var hidesTables, hidesKitchen bool
	for _, a := range pm.LayoutAmendments {
		if a.PluginID != salonID(t) {
			continue
		}
		switch a.Key {
		case "/tables":
			hidesTables = a.Hide
		case "/kitchen-stations":
			hidesKitchen = a.Hide
		}
	}
	if !hidesTables || !hidesKitchen {
		t.Fatalf("salon layout must hide /tables and /kitchen-stations once active, got amendments %+v", pm.LayoutAmendments)
	}

	// syncLocales reads locale files back from disk (paths.Plugins()), not
	// from the manifest bytes — Sync must have written them there, or the
	// plugin's relabelled /items tile silently falls back to the core
	// label with no error anywhere (ADR-0088 Decision G).
	localeDir := paths.Plugins(salonID(t), embeddedSalon(t).Version, "locales")
	entries, err := os.ReadDir(localeDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("Sync must write plugins/layout-salon's locale files to %s: %v", localeDir, err)
	}
}

func TestSync_NonServiceShopTypes_NeverInstallSalonLayout(t *testing.T) {
	for _, st := range []string{"cafe", "retail", "hospitality", "market_stall", "other", ""} {
		t.Run(st, func(t *testing.T) {
			d := openTestDB(t)
			ctx := context.Background()
			if _, err := Sync(ctx, d.DB, st); err != nil {
				t.Fatalf("Sync(%q): %v", st, err)
			}
			active, err := data.NewPluginRepo(d.DB).PluginActive(ctx, salonID(t))
			if err != nil {
				t.Fatal(err)
			}
			if active {
				t.Fatalf("shop_type %q must never activate the salon layout", st)
			}
		})
	}
}

func TestSync_SwitchingAwayFromService_RemovesSalonLayoutCleanly(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if _, err := Sync(ctx, d.DB, "service"); err != nil {
		t.Fatalf("Sync(service): %v", err)
	}
	if _, err := Sync(ctx, d.DB, "cafe"); err != nil {
		t.Fatalf("Sync(cafe): %v", err)
	}

	pm, err := plugins.Init(ctx, &config.Config{}, d.DB)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pm.Installed[salonID(t)]; ok {
		t.Fatalf("switching to shop_type=cafe must remove the salon layout, still installed: %+v", pm.Installed)
	}
	for _, a := range pm.LayoutAmendments {
		if a.PluginID == salonID(t) {
			t.Fatalf("no orphaned amendment may survive removal, got %+v", a)
		}
	}
	if _, err := os.Stat(paths.Plugins(salonID(t))); !os.IsNotExist(err) {
		t.Fatalf("switching away must remove the plugin's on-disk files, stat err = %v", err)
	}
}

func TestSync_IsIdempotent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if _, err := Sync(ctx, d.DB, "service"); err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	if _, err := Sync(ctx, d.DB, "service"); err != nil {
		t.Fatalf("second Sync (same shop_type again): %v", err)
	}

	pm, err := plugins.Init(ctx, &config.Config{}, d.DB)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, a := range pm.LayoutAmendments {
		if a.PluginID == salonID(t) && a.Key == "/tables" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("re-syncing the same shop_type must not duplicate the plugin's amendments, got %d /tables amendments", n)
	}

	// Re-running Sync("cafe") after it's already inactive must also be a
	// clean no-op, not an error from trying to uninstall something absent.
	if _, err := Sync(ctx, d.DB, "cafe"); err != nil {
		t.Fatalf("Sync(cafe) after already-cafe: %v", err)
	}
	if _, err := Sync(ctx, d.DB, "cafe"); err != nil {
		t.Fatalf("second Sync(cafe): %v", err)
	}
}

// A plugin an operator manually DISABLED (is_active=0) from Settings →
// Plugins is still INSTALLED — it must be removed when shop_type moves away
// from it, or it can resurrect on a later re-enable with the shop on a
// different business type by then. Sync must key off installed state, not
// active state.
func TestSync_DisabledSalonLayout_StillRemovedOnSwitchAway(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if _, err := Sync(ctx, d.DB, "service"); err != nil {
		t.Fatalf("Sync(service): %v", err)
	}
	if err := data.NewPluginRepo(d.DB).SetPluginActive(ctx, nil, salonID(t), false); err != nil {
		t.Fatalf("simulate manual disable: %v", err)
	}

	if _, err := Sync(ctx, d.DB, "cafe"); err != nil {
		t.Fatalf("Sync(cafe) with salon layout disabled-but-installed: %v", err)
	}

	if _, found, err := data.NewPluginRepo(d.DB).GetInstalledPluginVersion(ctx, salonID(t)); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("switching away must remove a DISABLED-but-installed salon layout too, not just an active one")
	}
	if _, err := os.Stat(paths.Plugins(salonID(t))); !os.IsNotExist(err) {
		t.Fatalf("switching away must also remove the disabled plugin's on-disk files, stat err = %v", err)
	}
}

// A stale version installed by a previous till binary (before the embedded
// plugin.json's own version bumped) must be replaced with a clean copy of
// the current version — not left in place forever because Sync only ever
// checked "is SOME version of this plugin id present."
func TestSync_StaleInstalledVersion_ReplacedWithCurrentOnResync(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// Simulate a PRIOR till binary, whose embedded plugin.json still said
	// "0.0.1-stale", having already installed the salon layout through this
	// exact mechanism (PersistManifest — the same path Sync itself uses).
	// This is the realistic prior state a self-update lands on top of, not a
	// hand-rolled DB row: real installs always go through PersistManifest,
	// which itself creates the plugin_catalog row plugins(id,version)'s
	// foreign key requires, so building the scenario any other way (e.g. a
	// raw UPDATE of plugins.version) trips that FK instead of testing Sync.
	staleManifest, err := plugins.ParseManifest(bytes.NewReader(layoutsalon.ManifestJSON))
	if err != nil {
		t.Fatal(err)
	}
	staleManifest.Version = "0.0.1-stale"
	if err := plugins.PersistManifest(ctx, d.DB, staleManifest, plugins.InstallOptions{TrustLevel: "system"}); err != nil {
		t.Fatalf("install fabricated stale version: %v", err)
	}

	if _, err := Sync(ctx, d.DB, "service"); err != nil {
		t.Fatalf("Sync(service) over a stale version: %v", err)
	}

	version, found, err := data.NewPluginRepo(d.DB).GetInstalledPluginVersion(ctx, salonID(t))
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("re-syncing must leave the salon layout installed, found none")
	}
	if version == "0.0.1-stale" {
		t.Fatal("a version mismatch must trigger a clean reinstall, not silently keep the stale row")
	}

	pm, err := plugins.Init(ctx, &config.Config{}, d.DB)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, a := range pm.LayoutAmendments {
		if a.PluginID == salonID(t) && a.Key == "/tables" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("reinstalling over a stale version must not leave duplicate amendments, got %d", n)
	}
}

// ut-docs#3178 review: a version bump reinstalls the builtin fresh, and an
// operator who disabled it in Settings → Plugins must not find it switched
// back on after the till self-updates.
func TestSync_StaleVersionReinstall_KeepsOperatorDisable(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	repo := data.NewPluginRepo(d.DB)

	stale := embeddedSalon(t)
	stale.Version = "0.0.1-stale"
	if err := plugins.PersistManifest(ctx, d.DB, stale, plugins.InstallOptions{TrustLevel: "system"}); err != nil {
		t.Fatalf("install stale version: %v", err)
	}
	if err := repo.SetPluginActive(ctx, nil, salonID(t), false); err != nil {
		t.Fatalf("simulate manual disable: %v", err)
	}

	if _, err := Sync(ctx, d.DB, "service"); err != nil {
		t.Fatalf("Sync(service) over a disabled stale version: %v", err)
	}
	version, found, err := repo.GetInstalledPluginVersion(ctx, salonID(t))
	if err != nil || !found || version != embeddedSalon(t).Version {
		t.Fatalf("stale copy must be replaced with the embedded version, got %q found=%v err=%v", version, found, err)
	}
	active, err := repo.PluginActive(ctx, salonID(t))
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatal("a reinstall over an operator-disabled builtin must keep it disabled")
	}
}

// ut-docs#2006: Sync's changed return lets a caller skip a redundant
// ReloadPlugins on a genuine no-op — both "never needed the layout" and
// "already installed at the current version" must report changed=false.
func TestSync_ChangedIsFalseOnGenuineNoOp(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	changed, err := Sync(ctx, d.DB, "cafe")
	if err != nil {
		t.Fatalf("Sync(cafe) from a fresh DB: %v", err)
	}
	if changed {
		t.Fatal("shop_type never needing the salon layout must report changed=false")
	}

	changed, err = Sync(ctx, d.DB, "service")
	if err != nil {
		t.Fatalf("Sync(service): %v", err)
	}
	if !changed {
		t.Fatal("a fresh install must report changed=true")
	}

	changed, err = Sync(ctx, d.DB, "service")
	if err != nil {
		t.Fatalf("second Sync(service): %v", err)
	}
	if changed {
		t.Fatal("re-syncing the same shop_type at the same installed version must report changed=false")
	}
}

// ut-docs#2006: install/remove/reinstall must all report changed=true.
func TestSync_ChangedIsTrueOnRealChanges(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if changed, err := Sync(ctx, d.DB, "service"); err != nil {
		t.Fatalf("Sync(service): %v", err)
	} else if !changed {
		t.Fatal("fresh install must report changed=true")
	}

	if changed, err := Sync(ctx, d.DB, "cafe"); err != nil {
		t.Fatalf("Sync(cafe): %v", err)
	} else if !changed {
		t.Fatal("removal must report changed=true")
	}

	// Reinstall over a stale version (same scenario as
	// TestSync_StaleInstalledVersion_ReplacedWithCurrentOnResync).
	staleManifest, err := plugins.ParseManifest(bytes.NewReader(layoutsalon.ManifestJSON))
	if err != nil {
		t.Fatal(err)
	}
	staleManifest.Version = "0.0.1-stale"
	if err := plugins.PersistManifest(ctx, d.DB, staleManifest, plugins.InstallOptions{TrustLevel: "system"}); err != nil {
		t.Fatalf("install fabricated stale version: %v", err)
	}
	if changed, err := Sync(ctx, d.DB, "service"); err != nil {
		t.Fatalf("Sync(service) over a stale version: %v", err)
	} else if !changed {
		t.Fatal("a version-bump reinstall must report changed=true")
	}
}

// ut-docs#2006 gap 2: a failed reinstall (removeBuiltin succeeds, installBuiltin
// then fails) must still return an error — changed's value on an error path
// is never load-bearing for the caller (the caller reloads unconditionally
// on any error, per the call-site contract), but Sync itself must not lose
// the error or crash.
func TestSync_ReinstallFailure_StillReturnsError(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	staleManifest, err := plugins.ParseManifest(bytes.NewReader(layoutsalon.ManifestJSON))
	if err != nil {
		t.Fatal(err)
	}
	staleManifest.Version = "0.0.1-stale"
	if err := plugins.PersistManifest(ctx, d.DB, staleManifest, plugins.InstallOptions{TrustLevel: "system"}); err != nil {
		t.Fatalf("install fabricated stale version: %v", err)
	}

	// Block installBuiltin's os.MkdirAll(destDir, ...) deterministically: put a
	// regular file at the plugins ROOT itself (not under paths.Plugins(id) —
	// removeBuiltin's own os.RemoveAll(paths.Plugins(id)) runs first on this
	// path and would silently delete a blocking file placed any deeper,
	// since PersistManifest above never wrote any real directory for the
	// stale version). This is a portable failure-injection (unlike
	// chmod-based approaches, which behave differently when tests run as
	// root, as they do in this sandbox).
	pluginsRoot := paths.Plugins()
	if err := os.MkdirAll(filepath.Dir(pluginsRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pluginsRoot, []byte("blocking file"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = Sync(ctx, d.DB, "service")
	if err == nil {
		t.Fatal("Sync must return an error when installBuiltin's MkdirAll is blocked, got nil")
	}

	// The remove half of the reinstall must still have gone through — this
	// is the exact residual-state gap ut-docs#2006 describes: the DB says
	// uninstalled even though Sync errored.
	if _, found, err := data.NewPluginRepo(d.DB).GetInstalledPluginVersion(ctx, salonID(t)); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("removeBuiltin's half of the reinstall must have succeeded despite installBuiltin failing")
	}
}

// TestSync_SalonInstall_WaitsForPluginTreeLock pins ut-docs#3278: installBuiltin
// must hold the per-plugin tree lock across the live locale-dir write and
// PersistManifest, like every other install path (ut-docs#3273). Otherwise a
// Rollback of layout-salon racing a shop-type reconcile (Sync) can commit over
// the fresh install. While another holder has the lock, Sync must neither
// write the live locale dir nor install the plugin, and it must finish once
// the lock is released.
func TestSync_SalonInstall_WaitsForPluginTreeLock(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	m, err := plugins.ParseManifest(bytes.NewReader(layoutsalon.ManifestJSON))
	if err != nil {
		t.Fatal(err)
	}
	localesDir := paths.Plugins(salonID(t), m.Version, "locales")

	unlock := plugins.LockPluginTree(paths.Plugins(), salonID(t))
	var once sync.Once
	release := func() { once.Do(unlock) }
	t.Cleanup(release)

	done := make(chan error, 1)
	go func() {
		_, err := Sync(ctx, d.DB, "service")
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("Sync returned (%v) while the plugin tree lock was held", err)
	case <-time.After(200 * time.Millisecond):
	}
	if _, err := os.Stat(localesDir); err == nil {
		t.Errorf("locale dir %s written while the plugin tree lock was held", localesDir)
	}
	repo := data.NewPluginRepo(d.DB)
	if _, ok, err := repo.GetInstalledPluginVersion(ctx, salonID(t)); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Error("salon installed while the plugin tree lock was held")
	}

	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Sync after lock release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Sync did not finish after the plugin tree lock was released")
	}
	if v, ok, err := repo.GetInstalledPluginVersion(ctx, salonID(t)); err != nil || !ok || v != m.Version {
		t.Fatalf("want salon %s installed, got %q ok=%v err=%v", m.Version, v, ok, err)
	}
}

// ut-docs#3178 / ADR-0129 §2: core finds the builtin layout for a shop type
// through the embedded manifest's `provides`, never by naming a plugin id.
func TestPluginIDForShopType_ResolvesEmbeddedSalonForService(t *testing.T) {
	m, err := plugins.ParseManifest(bytes.NewReader(layoutsalon.ManifestJSON))
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range plugins.ShopTypes() {
		got, err := PluginIDForShopType(st)
		if err != nil {
			t.Fatalf("PluginIDForShopType(%q): %v", st, err)
		}
		want := ""
		if st == "service" {
			want = m.ID
		}
		if got != want {
			t.Errorf("PluginIDForShopType(%q) = %q, want %q", st, got, want)
		}
	}
}

func TestEmbeddedSalonManifest_ProvidesServiceShopType(t *testing.T) {
	m, err := plugins.ParseManifest(bytes.NewReader(layoutsalon.ManifestJSON))
	if err != nil {
		t.Fatal(err)
	}
	want := plugins.CapabilityLayoutShopTypePrefix + "service"
	for _, p := range m.Provides {
		if p == want {
			return
		}
	}
	t.Fatalf("embedded salon manifest must provide %q, got %v", want, m.Provides)
}

// ADR-0129 §2: `layout.shop_type:*` is embedded-builtin only. A non-embedded
// (marketplace) plugin declaring it is never selected nor removed by Sync.
func TestSync_LeavesNonEmbeddedShopTypeProviderAlone(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	repo := data.NewPluginRepo(d.DB)

	const otherID = "com.example.layout-service-lookalike"
	other, err := plugins.ParseManifest(bytes.NewReader(layoutsalon.ManifestJSON))
	if err != nil {
		t.Fatal(err)
	}
	other.ID = otherID
	// Amend a destination the salon doesn't touch, so both can coexist.
	other.Entries = other.Entries[:1]
	other.Entries[0].Config = map[string]interface{}{
		"slot":       "menu",
		"amendments": []interface{}{map[string]interface{}{"key": "/reports", "order": 900}},
	}
	other.Provides = []string{plugins.CapabilityLayoutShopTypePrefix + "service"}
	if err := plugins.PersistManifest(ctx, d.DB, other, plugins.InstallOptions{TrustLevel: "verified"}); err != nil {
		t.Fatalf("install non-embedded provider: %v", err)
	}
	salonID, err := PluginIDForShopType("service")
	if err != nil || salonID == "" {
		t.Fatalf("PluginIDForShopType(service) = %q, %v", salonID, err)
	}

	for _, st := range []string{"service", "cafe", "service"} {
		if _, err := Sync(ctx, d.DB, st); err != nil {
			t.Fatalf("Sync(%q): %v", st, err)
		}
		if _, found, err := repo.GetInstalledPluginVersion(ctx, otherID); err != nil || !found {
			t.Fatalf("after Sync(%q) the non-embedded provider must stay installed, found=%v err=%v", st, found, err)
		}
		_, salonFound, err := repo.GetInstalledPluginVersion(ctx, salonID)
		if err != nil {
			t.Fatal(err)
		}
		if salonFound != (st == "service") {
			t.Fatalf("after Sync(%q) embedded salon installed = %v, want %v", st, salonFound, st == "service")
		}
	}
}
