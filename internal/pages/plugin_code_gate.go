package pages

import (
	"context"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/bundled"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// catalogCanonicalType reads a listing's catalog type from the till's LOCAL
// catalog snapshot (no network). "" when the catalog is unavailable or does
// not list it: on the App Store channel that fails closed (code plugins are
// never fetched, ADR-0194), and elsewhere the type is unused.
func catalogCanonicalType(d *common.Deps, listingID string) string {
	if d == nil || d.CatalogRepo == nil || d.Cfg == nil {
		return ""
	}
	locale, arch := marketplace.TillCatalogKey(d.Cfg.DefaultLocale)
	snap, _, err := d.CatalogRepo.Get(locale, arch)
	if err != nil || snap == nil {
		return ""
	}
	for _, p := range snap.Plugins {
		if p.ListingID == listingID || p.ID == listingID {
			return p.CanonicalType
		}
	}
	return ""
}

// refuseCodeInstall resolves the listing's catalog type and applies the
// App Store gate (ADR-0194) before anything is staged, recorded or fetched.
// It returns the type (to pass on to the installer) and the typed refusal.
func refuseCodeInstall(d *common.Deps, listingID string) (string, error) {
	t := catalogCanonicalType(d, listingID)
	return t, plugins.RefuseCodeCanonicalType(t)
}

// isBundledPlugin reports whether an installed plugin is one of the
// first-party plugins that ship inside the app (ADR-0194): in the compiled-in
// lock AND persisted with system trust AND this build embeds the set, so a
// marketplace-installed copy of the same plugin on another channel, or an
// import that forged trust_level=system, is never mistaken for it.
func isBundledPlugin(id, trustLevel string) bool {
	return bundled.Available() && trustLevel == "system" && bundled.IsBundled(id)
}

// isBundledInstalled reads the installed row's trust level and applies
// isBundledPlugin. A read error reports false (the uninstall then proceeds as
// it always did).
func isBundledInstalled(ctx context.Context, d *common.Deps, id string) bool {
	if !bundled.IsBundled(id) || d == nil || d.Db == nil {
		return false
	}
	trust, found, err := data.NewPluginRepo(d.Db).PluginTrustLevel(ctx, id)
	return err == nil && found && isBundledPlugin(id, trust)
}
