// Command seed_docs_plugin_catalog writes a fixed, offline marketplace
// catalogue snapshot into a throwaway till's data dir, so the manual's
// Plugin Store screenshots (ut-docs#335) show a populated store without a
// network or a cloud. Docs-shots only: run-till-plugins.sh is the one caller,
// and no e2e spec boots that till.
//
// The till serves a cached snapshot even when it is stale and keeps it when
// the background refresh fails, so a file written before boot is what
// /plugins/store and /plugins/store/{id} render. All listings are fictional.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}

// listings are the six fictional catalogue entries, in catalogue order. No
// icons (letter fallback, so no network), no markets, no min host version.
func listings() []marketplace.PluginSummary {
	mk := func(id, name, typ, tier, vendor, desc string, paid bool, perms ...string) marketplace.PluginSummary {
		return marketplace.PluginSummary{
			ID: id, ListingID: id, Name: name, Description: desc, Vendor: vendor,
			PaidListing: paid, Version: "1.0.0", TrustTier: tier,
			Permissions: perms, CanonicalType: typ,
		}
	}
	return []marketplace.PluginSummary{
		mk("sample-loyalty-stamps", "Sample Loyalty Stamps", "pricing", "official", "Universal Till",
			"Rewards regular customers with a free item after a set number of purchases.", false, "db:own", "ui:page"),
		mk("sample-card-terminal", "Sample Card Terminal", "payment", "verified", "Example Payments Ltd",
			"Takes card payments through a terminal on your shop network.", false, "http:lan", "secret:write"),
		mk("sample-stock-sync", "Sample Stock Sync", "integration", "unverified", "Example Developer",
			"Keeps stock counts in step with an outside inventory service.", false,
			"schedule", "db:own", "net:validation:validation.example.org", "sales:read"),
		mk("sample-weekly-report", "Sample Weekly Report", "report", "verified", "Example Analytics",
			"A weekly summary of sales by day and by category.", true, "sales:read"),
		mk("sample-night-theme", "Sample Night Theme", "theme", "official", "Universal Till",
			"A dark look for the sale screen.", false),
		mk("sample-language-pack", "Sample Language Pack", "language", "official", "Universal Till",
			"Adds another language to the till's screens.", false),
	}
}

// buildSnapshot is the catalogue file's content. FetchedAt is fixed and long
// past, so every read counts it as stale and the (failing) refresh keeps it.
func buildSnapshot(locale, arch string) marketplace.CatalogSnapshot {
	return marketplace.CatalogSnapshot{
		Plugins:         listings(),
		SnapshotVersion: 1,
		FetchedAt:       time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC),
		Locale:          locale,
		DeviceArch:      arch,
	}
}

// snapshotFile repeats marketplace.snapshotFileName (unexported): '@' for an
// empty component, '+' for the arch's '/'. main_test.go loads the result back
// through the real CatalogRepository, so the two cannot drift unnoticed.
func snapshotFile(locale, arch string) string {
	part := func(v string) string {
		if v == "" {
			return "@"
		}
		return strings.ReplaceAll(v, "/", "+")
	}
	return "catalog-snapshot." + part(locale) + "." + part(arch) + ".json"
}

func writeSnapshot(cacheDir string, s marketplace.CatalogSnapshot) error {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(cacheDir, snapshotFile(s.Locale, s.DeviceArch)), b, 0o644)
}

func main() {
	dataDir := os.Getenv("UT_DATA_DIR")
	if dataDir == "" {
		fatalf("UT_DATA_DIR must be set")
	}
	// Equals paths.Plugins("cache") (internal/app/app.go).
	cacheDir := filepath.Join(dataDir, "plugins", "cache")
	locale, arch := marketplace.TillCatalogKey(os.Getenv("UT_MARKETPLACE_LOCALE"))
	if err := writeSnapshot(cacheDir, buildSnapshot(locale, arch)); err != nil {
		fatalf("write catalogue snapshot: %v", err)
	}
	// Give the shop a real name, so the "Name your shop" banner (about 40px on
	// every page) does not push the listing page's Download button out of the
	// 1024x600 frame. Same store.name setting the Settings page writes.
	conn, err := db.Open(filepath.Join(dataDir, "unitill-pos.db"))
	if err != nil {
		fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := data.NewSettingsRepo(conn.DB).Set(context.Background(), "store.name", "Sample Shop"); err != nil {
		fatalf("set store name: %v", err)
	}
}
