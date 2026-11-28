package pages

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/updates"
)

// ut-docs#3706, ADR-0149 §8 + the 2026-11-28 amendment item 5, ADR-0121 §2:
// the wizard's shop-type step may SUGGEST one optional plugin per shop type.
// The suggestion is driven only by the neutral, signed `shop_types` field the
// catalogue carries (ut-docs#7246, `GET /v1/catalog/plugins?shop_type=`): core
// never names a plugin (guard-core-neutral) — the name and description shown
// are the listing's own.
//
// Rules, all deliberate:
//   - never auto-installs: a plugin installs only when the operator ticks the
//     suggestion's box (an unticked box posts nothing);
//   - skipping leaves no trace: no setting, no pending-list entry, no queued
//     retry. A failed install attempt after a tick is logged and dropped; the
//     operator finds the plugin in the marketplace (offline-first: setup never
//     blocks on a download);
//   - the posted listing id is never trusted: it must equal the suggestion the
//     catalogue gives for the shop type that is actually saved;
//   - a free, non-fiscal listing only (a `tax` listing is prompted by its own
//     tile, ADR-0025 D4/ADR-0129 §4; a paid one needs the purchase flow);
//   - an unreachable catalogue simply shows no suggestion (no offline tile).

const (
	// setupShopTypeCatalogTTL/FetchTimeout/RetryInterval/StaleMax mirror the tax
	// catalogue cache (setup_tax_catalog.go): same reasoning, one shared fetch
	// budget for every shop type so an offline till costs one short timeout,
	// not one per type.
	setupShopTypeCatalogTTL           = 5 * time.Minute
	setupShopTypeCatalogFetchTimeout  = 3 * time.Second
	setupShopTypeCatalogRetryInterval = 30 * time.Second
	setupShopTypeCatalogStaleMax      = 15 * time.Minute

	// setupShopTypeInstallTimeout bounds the one install attempt at the end of
	// POST /api/setup (same value as the base-plugin attempt).
	setupShopTypeInstallTimeout = setupBasePluginAttemptTimeout
)

var setupShopTypeCache struct {
	mu          sync.Mutex
	lastAttempt time.Time
	lastSuccess time.Time
	fetched     bool
	byType      map[string]*marketplace.PluginSummary
}

// resetSetupShopTypeSuggest clears the cache — tests only (package-global).
func resetSetupShopTypeSuggest() {
	c := &setupShopTypeCache
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastAttempt, c.lastSuccess = time.Time{}, time.Time{}
	c.fetched = false
	c.byType = nil
}

// shopTypeSuggestionFor is the pure choice: the highest-semver free, non-tax
// listing that NAMES shopType in its `shop_types`. The client-side check is
// not redundant: a server that ignores the filter must not leak a listing
// that does not name the type. A listing naming no shop type is never chosen.
func shopTypeSuggestionFor(entries []marketplace.PluginSummary, shopType string) *marketplace.PluginSummary {
	if !plugins.IsKnownShopType(shopType) {
		return nil
	}
	var best *marketplace.PluginSummary
	for i := range entries {
		e := &entries[i]
		if e.PaidListing || e.CanonicalType == "tax" || !slices.Contains(e.ShopTypes, shopType) {
			continue
		}
		if best == nil || updates.Newer(e.Version, best.Version) {
			best = e
		}
	}
	return best
}

func listingIDOf(p *marketplace.PluginSummary) string {
	if p.ListingID != "" {
		return p.ListingID
	}
	return p.ID
}

// setupShopTypeSuggestions returns the suggestion per shop type (only types
// that have one), from the cached catalogue, fetching (bounded) when the cache
// is cold or expired. Unreachable and nothing usable cached: empty. Listings
// already installed and active are left out.
func setupShopTypeSuggestions(ctx context.Context, d *common.Deps) map[string]*marketplace.PluginSummary {
	byType := setupShopTypeCatalog(ctx, d)
	out := make(map[string]*marketplace.PluginSummary, len(byType))
	for t, p := range byType {
		if status, had, err := plugins.NewInstallStatusStore(d.Db).Get(ctx, listingIDOf(p)); err == nil && had && status.State == plugins.InstallStateActive && status.PluginID != "" {
			continue
		}
		out[t] = p
	}
	return out
}

func setupShopTypeCatalog(ctx context.Context, d *common.Deps) map[string]*marketplace.PluginSummary {
	c := &setupShopTypeCache
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if c.fetched && now.Sub(c.lastSuccess) < setupShopTypeCatalogTTL {
		return c.byType
	}
	serveCached := func() map[string]*marketplace.PluginSummary {
		if !c.fetched || now.Sub(c.lastSuccess) >= setupShopTypeCatalogStaleMax {
			return nil
		}
		return c.byType
	}
	if now.Sub(c.lastAttempt) < setupShopTypeCatalogRetryInterval {
		return serveCached()
	}
	c.lastAttempt = now
	if d.Cfg == nil || strings.TrimSpace(d.Cfg.Marketplace.EndpointURL) == "" {
		return serveCached()
	}

	fctx, cancel := context.WithTimeout(ctx, setupShopTypeCatalogFetchTimeout)
	defer cancel()
	// enroll.Effective, NOT EnsureRegistered: browsing never mints the shop's
	// cloud identity (ADR-0015), same as the language and tax catalogues.
	effCfg := enroll.Effective(d.Cfg)
	client := marketplace.NewClient(&effCfg.Marketplace)
	byType := make(map[string]*marketplace.PluginSummary)
	for _, t := range plugins.ShopTypes() {
		var all []marketplace.PluginSummary
		pageToken := ""
		for page := 0; page < setupBasePluginMaxPages; page++ {
			resp, err := client.ListPlugins(fctx, &marketplace.ListPluginsRequest{ShopType: t, PageToken: pageToken})
			if err != nil {
				logging.L().Warnf("setup wizard: shop-type suggestion fetch failed (serving cache or nothing): %v", err)
				return serveCached()
			}
			all = append(all, resp.Plugins...)
			if resp.NextPageToken == "" {
				break
			}
			pageToken = resp.NextPageToken
		}
		if p := shopTypeSuggestionFor(all, t); p != nil {
			byType[t] = p
		}
	}
	c.byType, c.fetched, c.lastSuccess = byType, true, now
	return c.byType
}

// shopTypeSuggestionView is one suggestion block of the shop-type step.
type shopTypeSuggestionView struct {
	ShopType    string
	ListingID   string
	Name        string
	Description string
}

// shopTypeSuggestionViews is the step's blocks in the taxonomy's display order.
func shopTypeSuggestionViews(ctx context.Context, d *common.Deps) []shopTypeSuggestionView {
	sugg := setupShopTypeSuggestions(ctx, d)
	var views []shopTypeSuggestionView
	for _, t := range plugins.ShopTypes() {
		if p := sugg[t]; p != nil {
			views = append(views, shopTypeSuggestionView{ShopType: t, ListingID: listingIDOf(p), Name: p.Name, Description: p.Description})
		}
	}
	return views
}

// installShopTypeSuggestionForSetup is POST /api/setup's hook: when the
// operator ticked the suggestion, install exactly the catalogue's suggestion
// for the saved shop type. Best effort and silent on failure (logged): setup
// never fails or waits on it, and nothing is queued (skipping/failing leaves
// no trace). A no-op unless listingID is non-empty.
func installShopTypeSuggestionForSetup(ctx context.Context, d *common.Deps, shopType, listingID string) {
	listingID = strings.TrimSpace(listingID)
	if listingID == "" || !plugins.IsKnownShopType(shopType) {
		return
	}
	p := setupShopTypeSuggestions(ctx, d)[shopType]
	if p == nil || listingIDOf(p) != listingID {
		logging.L().Warnf("setup wizard: ignoring a shop-type suggestion for %q that the catalogue does not give", shopType)
		return
	}
	ictx, cancel := context.WithTimeout(ctx, setupShopTypeInstallTimeout)
	defer cancel()
	if _, err := cloudInstallPluginVersion(ictx, d, listingID, p.Version); err != nil {
		logging.L().Warnf("setup wizard: optional %s suggestion not installed (not queued): %v", shopType, err)
	}
}
