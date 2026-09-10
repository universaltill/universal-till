package builtinlayouts

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

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
// the environment directly. Without this, a test exercising installSalon's
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

func TestSync_ServiceShopType_InstallsAndActivatesSalonLayout(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if err := Sync(ctx, d.DB, "service"); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	pm, err := plugins.Init(ctx, &config.Config{}, d.DB)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pm.Installed[SalonPluginID]; !ok {
		t.Fatalf("want %s installed after Sync(\"service\"), got %+v", SalonPluginID, pm.Installed)
	}

	var hidesTables, hidesKitchen bool
	for _, a := range pm.LayoutAmendments {
		if a.PluginID != SalonPluginID {
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
	localeDir := paths.Plugins(SalonPluginID, "0.1.0", "locales")
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
			if err := Sync(ctx, d.DB, st); err != nil {
				t.Fatalf("Sync(%q): %v", st, err)
			}
			active, err := data.NewPluginRepo(d.DB).PluginActive(ctx, SalonPluginID)
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

	if err := Sync(ctx, d.DB, "service"); err != nil {
		t.Fatalf("Sync(service): %v", err)
	}
	if err := Sync(ctx, d.DB, "cafe"); err != nil {
		t.Fatalf("Sync(cafe): %v", err)
	}

	pm, err := plugins.Init(ctx, &config.Config{}, d.DB)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pm.Installed[SalonPluginID]; ok {
		t.Fatalf("switching to shop_type=cafe must remove the salon layout, still installed: %+v", pm.Installed)
	}
	for _, a := range pm.LayoutAmendments {
		if a.PluginID == SalonPluginID {
			t.Fatalf("no orphaned amendment may survive removal, got %+v", a)
		}
	}
	if _, err := os.Stat(paths.Plugins(SalonPluginID)); !os.IsNotExist(err) {
		t.Fatalf("switching away must remove the plugin's on-disk files, stat err = %v", err)
	}
}

func TestSync_IsIdempotent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if err := Sync(ctx, d.DB, "service"); err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	if err := Sync(ctx, d.DB, "service"); err != nil {
		t.Fatalf("second Sync (same shop_type again): %v", err)
	}

	pm, err := plugins.Init(ctx, &config.Config{}, d.DB)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, a := range pm.LayoutAmendments {
		if a.PluginID == SalonPluginID && a.Key == "/tables" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("re-syncing the same shop_type must not duplicate the plugin's amendments, got %d /tables amendments", n)
	}

	// Re-running Sync("cafe") after it's already inactive must also be a
	// clean no-op, not an error from trying to uninstall something absent.
	if err := Sync(ctx, d.DB, "cafe"); err != nil {
		t.Fatalf("Sync(cafe) after already-cafe: %v", err)
	}
	if err := Sync(ctx, d.DB, "cafe"); err != nil {
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

	if err := Sync(ctx, d.DB, "service"); err != nil {
		t.Fatalf("Sync(service): %v", err)
	}
	if err := data.NewPluginRepo(d.DB).SetPluginActive(ctx, nil, SalonPluginID, false); err != nil {
		t.Fatalf("simulate manual disable: %v", err)
	}

	if err := Sync(ctx, d.DB, "cafe"); err != nil {
		t.Fatalf("Sync(cafe) with salon layout disabled-but-installed: %v", err)
	}

	if _, found, err := data.NewPluginRepo(d.DB).GetInstalledPluginVersion(ctx, SalonPluginID); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("switching away must remove a DISABLED-but-installed salon layout too, not just an active one")
	}
	if _, err := os.Stat(paths.Plugins(SalonPluginID)); !os.IsNotExist(err) {
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

	if err := Sync(ctx, d.DB, "service"); err != nil {
		t.Fatalf("Sync(service) over a stale version: %v", err)
	}

	version, found, err := data.NewPluginRepo(d.DB).GetInstalledPluginVersion(ctx, SalonPluginID)
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
		if a.PluginID == SalonPluginID && a.Key == "/tables" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("reinstalling over a stale version must not leave duplicate amendments, got %d", n)
	}
}
