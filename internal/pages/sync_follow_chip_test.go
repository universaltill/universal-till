package pages

import (
	"context"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#2984: when an additional till follows the main till's plugin
// update, the "Updates are installed from the main till (N)" chip must be
// recomputed at once — not wait for the scheduler's next 15-min tick.

// stubPluginSyncInstall swaps convergePluginSet's install seam for fn and
// records the listings it was asked to install.
func stubPluginSyncInstall(t *testing.T, err error) *[]string {
	t.Helper()
	orig := pluginSyncInstall
	t.Cleanup(func() { pluginSyncInstall = orig })
	var calls []string
	pluginSyncInstall = func(_ context.Context, _ *common.Deps, listingID, version string) (string, error) {
		calls = append(calls, listingID+"@"+version)
		return "", err
	}
	return &calls
}

// stubRecompute records calls to the chip-count recompute (the plugin
// update scheduler's tick) instead of reaching the marketplace.
func stubRecompute(t *testing.T) *int {
	t.Helper()
	orig := pluginUpdateTickFn
	t.Cleanup(func() { pluginUpdateTickFn = orig })
	n := 0
	pluginUpdateTickFn = func(context.Context, *common.Deps) { n++ }
	return &n
}

func seedInstalledPluginAt(t *testing.T, dp *common.Deps, pluginID, version string) {
	t.Helper()
	if _, err := dp.Db.ExecContext(t.Context(), `
INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
VALUES (?, ?, 'Tax DE', 'wasm', './plugin', 'https://marketplace.invalid/tax-de', 'deadbeef', '2.5.0', '1.0', '2026-01-01T00:00:00Z')`,
		pluginID, version); err != nil {
		t.Fatalf("seed plugin_catalog: %v", err)
	}
	if _, err := dp.Db.ExecContext(t.Context(), `
INSERT INTO plugins (id, name, version, entrypoint, runtime)
VALUES (?, ?, ?, './plugin', 'wasm')`, pluginID, "Tax DE", version); err != nil {
		t.Fatalf("seed plugins: %v", err)
	}
}

var followRow = data.PluginSyncRow{ListingID: "listing-tax-de", PluginID: "com.test.tax-de", PluginName: "Tax DE", Version: "0.9.0"}

// The chip counts "the marketplace has a newer version than installed", so
// the follow recomputes it rather than guessing a decrement (review: a
// broken plugin re-fetched at the same version, or a main till pinned
// behind latest, was never an applied pending update).
func TestConvergePluginSet_FollowedUpdateRecomputesTheChip(t *testing.T) {
	dp := newMigratedSyncDeps(t, "follow-chip.db")
	seedInstalledPluginAt(t, dp, followRow.PluginID, "0.8.0")
	calls := stubPluginSyncInstall(t, nil)
	recomputes := stubRecompute(t)

	convergePluginSet(t.Context(), dp, []data.PluginSyncRow{followRow})

	if len(*calls) != 1 || (*calls)[0] != "listing-tax-de@0.9.0" {
		t.Fatalf("expected one install of listing-tax-de@0.9.0, got %v", *calls)
	}
	if *recomputes != 1 {
		t.Fatalf("chip count recomputed %d times after following the main till's update, want 1 (ut-docs#2984)", *recomputes)
	}
}

func TestConvergePluginSet_FreshInstallRecomputesTheChip(t *testing.T) {
	dp := newMigratedSyncDeps(t, "follow-chip-fresh.db")
	stubPluginSyncInstall(t, nil)
	recomputes := stubRecompute(t)

	convergePluginSet(t.Context(), dp, []data.PluginSyncRow{followRow})

	if *recomputes != 1 {
		t.Fatalf("chip count recomputed %d times after a fresh install, want 1", *recomputes)
	}
}

func TestConvergePluginSet_FailedFollowDoesNotRecompute(t *testing.T) {
	dp := newMigratedSyncDeps(t, "follow-chip-fail.db")
	seedInstalledPluginAt(t, dp, followRow.PluginID, "0.8.0")
	stubPluginSyncInstall(t, errors.New("marketplace down"))
	recomputes := stubRecompute(t)

	if convergePluginSet(t.Context(), dp, []data.PluginSyncRow{followRow}) {
		t.Fatal("expected convergePluginSet to report not converged when the install fails")
	}
	if *recomputes != 0 {
		t.Fatalf("chip count recomputed %d times although nothing changed, want 0", *recomputes)
	}
}

func TestConvergePluginSet_AlreadyFollowingDoesNotRecompute(t *testing.T) {
	dp := newMigratedSyncDeps(t, "follow-chip-same.db")
	seedInstalledPluginAt(t, dp, followRow.PluginID, followRow.Version)
	calls := stubPluginSyncInstall(t, nil)
	recomputes := stubRecompute(t)

	convergePluginSet(t.Context(), dp, []data.PluginSyncRow{followRow})

	if len(*calls) != 0 || *recomputes != 0 {
		t.Fatalf("steady state: installs %v, recomputes %d; want none (this runs every 30 s)", *calls, *recomputes)
	}
}
