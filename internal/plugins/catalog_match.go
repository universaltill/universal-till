package plugins

import "github.com/universaltill/universal-till/internal/plugins/marketplace"

// CatalogIndex resolves an installed plugin to its marketplace catalog
// entry by two tiers, mirroring UpdateChecker's own established fallback
// (ut-docs#1953): byListing is authoritative wherever the till recorded an
// install-status listing mapping (a marketplace install); byAuthorName is
// the fallback for anything that never got one.
//
// Extracted (ut-docs#2131) so this two-tier resolution lives in exactly one
// place — before this, UpdateChecker.CheckForUpdates had it and
// /plugins' management page (plugins_page.go) didn't, so a file-imported
// plugin (which writes no plugin_install_status row at all,
// internal/data/sync_plugins_repo.go) silently reported no update ever
// available on the management page while UpdateChecker could see one fine.
type CatalogIndex struct {
	byListing    map[string]marketplace.PluginSummary
	byAuthorName map[string]marketplace.PluginSummary
}

// IndexCatalog builds both lookups once per snapshot so a caller resolving
// many installed plugins against it does O(1) work per plugin instead of
// re-scanning the whole catalog for each one.
func IndexCatalog(snapshot *marketplace.CatalogSnapshot) CatalogIndex {
	idx := CatalogIndex{
		byListing:    make(map[string]marketplace.PluginSummary),
		byAuthorName: make(map[string]marketplace.PluginSummary),
	}
	if snapshot == nil {
		return idx
	}
	keepHighest := func(m map[string]marketplace.PluginSummary, key string, p marketplace.PluginSummary) {
		if key == "" {
			return
		}
		if existing, ok := m[key]; ok && compareVersions(p.Version, existing.Version) <= 0 {
			return
		}
		m[key] = p
	}
	for _, p := range snapshot.Plugins {
		listingID := p.ListingID
		if listingID == "" {
			listingID = p.ID
		}
		keepHighest(idx.byListing, listingID, p)
		// Author+name is a name-collision risk on its own (ut-docs#2131
		// review, and the original ut-docs#1953 heuristic this mirrors was
		// already flagged as unreliable) — never index a listing whose
		// vendor is unknown, or "Foo Plugin" by an unrelated vendor with no
		// developer_id on file would match any installed "Foo Plugin" with
		// no author recorded either (plugins.author is nullable). Requiring
		// a real developer_id on the catalog side is a cheap, meaningful
		// narrowing even though it can't fully eliminate the risk (two
		// vendors could still coincidentally share a name+id string).
		if p.DeveloperID != "" {
			keepHighest(idx.byAuthorName, p.DeveloperID+"/"+p.Name, p)
		}
	}
	return idx
}

// Resolve looks up the catalog entry for an installed plugin: the listing
// it was installed from (if the till recorded one via plugin_install_status),
// falling back to an author+name match for a plugin with no install-status
// record — most commonly one installed via "Import from file". The fallback
// requires a non-empty author on the installed side too (ut-docs#2131
// review) — plugins.author is a nullable column, and matching on name alone
// would treat two different vendors' same-named plugin as the same one.
// Returns ok=false when neither tier finds anything; a caller must treat
// that as "unknown against this catalog snapshot", never as "up to date"
// (ut-docs#2131 AC2 — latest:"" must never be presented as current).
func (idx CatalogIndex) Resolve(listingID, author, name string) (marketplace.PluginSummary, bool) {
	if listingID != "" {
		if p, ok := idx.byListing[listingID]; ok {
			return p, true
		}
	}
	if author == "" {
		return marketplace.PluginSummary{}, false
	}
	p, ok := idx.byAuthorName[author+"/"+name]
	return p, ok
}

// VersionNewer reports whether candidate is a real newer version than
// current, via the same semver-ish compareVersions UpdateChecker already
// uses — not a naive string inequality (ut-docs#2131 review: `latest !=
// row.Version` on the management page would call "1.0.10" newer than
// "1.0.9" and vice versa on any run where the catalog momentarily reports
// an older/equal build, e.g. right after a rollback).
func VersionNewer(candidate, current string) bool {
	return candidate != "" && compareVersions(candidate, current) > 0
}
