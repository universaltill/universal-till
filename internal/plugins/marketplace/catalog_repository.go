package marketplace

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// CatalogSnapshot represents a cached marketplace catalog
type CatalogSnapshot struct {
	Plugins         []PluginSummary `json:"plugins"`
	SnapshotVersion int64           `json:"snapshot_version"`
	FetchedAt       time.Time       `json:"fetched_at"`
	Locale          string          `json:"locale"`
	DeviceArch      string          `json:"device_arch"`
}

// CatalogRepository manages on-disk catalog snapshots with stale markers
type CatalogRepository struct {
	client       *Client
	snapshotPath string
	mu           sync.RWMutex
	cached       *CatalogSnapshot
	staleAfter   time.Duration
	// refreshing guards against duplicate concurrent background refreshes
	// (ut-docs#2143) — GetOrFetch sets it before spawning a refresh
	// goroutine and clears it when that goroutine finishes, so a second
	// caller arriving while one is already in flight just serves the stale
	// cache too instead of starting its own network round-trip.
	refreshing bool
	// coldFetch coalesces concurrent GetOrFetch calls made with identical
	// (locale, deviceArch) parameters while there is NO cache at all yet
	// (ut-docs#2143 review, Finding 3): several such callers used to each
	// fire their own synchronous network round-trip, serialized only by
	// accident (Fetch used to hold cr.mu for the whole call) — removing
	// that accidental serialization to fix the stale-cache blocking bug
	// turned a cold till's first few concurrent /plugins-family page loads
	// into that many separate 30s-timeout sockets against a dead route.
	// Keyed by the exact parameters (not a single shared key) so a caller
	// never receives a DIFFERENT arch/locale's filtered result — see
	// server.go's own comment on exactly that class of bug.
	coldFetch singleflight.Group
}

// catalogFetchMaxPages bounds how many pages Fetch will follow before
// giving up — mirrors setupBasePluginMaxPages/setupLanguageCatalogMaxPages
// (internal/pages/setup_base_plugins.go / setup_language_catalog.go),
// the same cap value and rationale (ut-docs#2149, same shape as the
// ut-docs#2133/#1108 fix those two already carry): bounds a malformed or
// hostile server from looping forever, well beyond any real catalog's page
// count.
const catalogFetchMaxPages = 25

// catalogFetchTimeout bounds the ENTIRE multi-page fetch, not just one
// page's own request timeout (config.MarketplaceConfig.RequestTimeoutSec,
// applied per-request inside Client). Fetch's two setup-wizard siblings
// (resolveAndInstallBasePlugin, languagePackLocalesForListing) rely on
// their CALLER wrapping the whole attempt in a short context
// (setupBasePluginAttemptTimeout, internal/pages/setup_base_plugins.go) —
// but Fetch has no such caller-side wrapper (its own callers, e.g.
// internal/pages/plugins_page.go, pass a bare request context with no
// deadline), and Fetch itself is what owns the pagination loop, so it has
// to bound its own worst case here. Without this, a slow-but-alive server
// that keeps emitting a non-empty NextPageToken could hold cr.mu (and
// therefore every other Get()/Filter() reader) for up to
// catalogFetchMaxPages * RequestTimeoutSec — ~12.5 minutes at the config
// default, against ~30s before this fix (independent review finding,
// ut-docs#2149). 30s is comfortably enough for a real catalog (25 pages
// in 30s is generous for any network this product runs on) while keeping
// the worst case bounded to roughly one page's own timeout instead of 25.
const catalogFetchTimeout = 30 * time.Second

// NewCatalogRepository creates a catalog repository
func NewCatalogRepository(client *Client, cacheDir string) (*CatalogRepository, error) {
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache dir: %w", err)
	}

	return &CatalogRepository{
		client:       client,
		snapshotPath: filepath.Join(cacheDir, "catalog-snapshot.json"),
		staleAfter:   15 * time.Minute,
	}, nil
}

// Fetch retrieves the latest catalog from the marketplace, paging through
// the full result set rather than just page 1 (ut-docs#2149) — same shape
// as the ut-docs#2133/#1108 fix already applied to this package's two
// setup-wizard callers (resolveAndInstallBasePlugin,
// languagePackLocalesForListing): ut-cloud's real defaultPageSize is 20, so
// a legitimate listing can sort past page 1 as the catalog grows, and this
// snapshot backs the general catalog browse UI, category listings and
// tax-code listings, not just one bounded lookup. Bounded by
// catalogFetchMaxPages (page count) and catalogFetchTimeout (wall clock)
// so a malformed/hostile server can't loop forever or stall every reader.
//
// The network calls deliberately happen with NO lock held (ut-docs#2143) —
// this used to hold cr.mu for the whole call (every page of it), so any
// concurrent Get()/GetOrFetch() caller blocked on the same round-trip(s) as
// the caller doing the actual refetch, for as long as the marketplace
// client's own RequestTimeoutSec on a dead/blackholed route. The lock is
// only needed for the brief update to cr.cached and the on-disk snapshot
// afterward, once every page has been collected.
//
// Interaction between the two fixes: unchanged for ut-docs#2143's literal
// case — a genuinely dead route fails on page 1 and Fetch returns
// immediately, same as before the pagination fix. The window pagination
// widens is different: a server that stays alive and slow while still
// emitting a non-empty NextPageToken. catalogFetchTimeout caps that widened
// window back down to roughly one page's own timeout rather than up to
// catalogFetchMaxPages of them (independent review finding, ut-docs#2149).
func (cr *CatalogRepository) Fetch(ctx context.Context, locale, deviceArch string) (*CatalogSnapshot, error) {
	if cr.client == nil {
		// A repo with no configured marketplace client (e.g. a test fixture
		// that seeds a snapshot directly on disk, ut-docs#2131) can't fetch —
		// treat it as an ordinary fetch failure rather than a nil-pointer
		// panic, so GetOrFetch's stale-cache fallback handles it the same
		// way it handles any other unreachable marketplace.
		return nil, fmt.Errorf("no marketplace client configured")
	}

	ctx, cancel := context.WithTimeout(ctx, catalogFetchTimeout)
	defer cancel()

	var all []PluginSummary
	var snapshotVersion int64
	pageToken := ""
	for page := 0; page < catalogFetchMaxPages; page++ {
		resp, err := cr.client.ListPlugins(ctx, &ListPluginsRequest{
			Locale:     locale,
			DeviceArch: deviceArch,
			PageToken:  pageToken,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to fetch catalog: %w", err)
		}
		all = append(all, resp.Plugins...)
		if page == 0 {
			// The snapshot version describes the catalog as a whole, not a
			// single page — it doesn't vary across pages of the same query,
			// so the first page's value is as good as any.
			snapshotVersion = resp.SnapshotVersion
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}

	snapshot := &CatalogSnapshot{
		Plugins:         all,
		SnapshotVersion: snapshotVersion,
		FetchedAt:       time.Now(),
		Locale:          locale,
		DeviceArch:      deviceArch,
	}

	cr.mu.Lock()
	defer cr.mu.Unlock()

	// Save to disk
	if err := cr.saveSnapshot(snapshot); err != nil {
		return nil, fmt.Errorf("failed to save snapshot: %w", err)
	}

	cr.cached = snapshot
	return snapshot, nil
}

// Get returns the cached catalog, marking it as stale if expired
func (cr *CatalogRepository) Get() (*CatalogSnapshot, bool, error) {
	cr.mu.RLock()
	defer cr.mu.RUnlock()

	// Try memory cache first
	if cr.cached != nil {
		isStale := time.Since(cr.cached.FetchedAt) > cr.staleAfter
		return cr.cached, isStale, nil
	}

	// Load from disk
	snapshot, err := cr.loadSnapshot()
	if err != nil {
		return nil, false, err
	}

	if snapshot == nil {
		return nil, false, fmt.Errorf("no catalog snapshot available")
	}

	isStale := time.Since(snapshot.FetchedAt) > cr.staleAfter
	return snapshot, isStale, nil
}

// GetOrFetch returns the cached catalog, refreshing it first when the cache
// is stale — offline-first is preserved throughout: a refetch failure (no
// network) falls back to serving the stale cache rather than erroring, the
// same fallback GetOrFetch has always used for "no cache at all".
//
// Before ut-docs#2131 this only fetched when there was NO cache — Get()
// computed isStale correctly (staleAfter is 15 minutes) but the caller threw
// it away, so a till that had ever written one snapshot to disk kept serving
// it forever, however old. That's a third, independent way a plugin's
// "latest" version goes stale/wrong on the management page, on top of the
// missing plugin_install_status mapping and the locale-filtered snapshot.
//
// ut-docs#2131's fix introduced a new problem (ut-docs#2143): refetching
// inline made every caller (the /plugins page, the plugin store, and
// update-listing resolution) block on a real network round-trip — up to the
// marketplace client's own RequestTimeoutSec (30s) — whenever the cache had
// merely gone stale, serializing concurrent page loads into sequential
// full-timeout waits on a dead route. So a STALE cache (one exists, just
// old) is now served immediately, with the refresh happening in a detached
// background goroutine for the next caller to benefit from; only the
// NO-cache-at-all case still fetches synchronously, since there is nothing
// to serve in the meantime.
func (cr *CatalogRepository) GetOrFetch(ctx context.Context, locale, deviceArch string) (*CatalogSnapshot, bool, error) {
	// Try to get cached first
	snapshot, isStale, err := cr.Get()
	if err == nil {
		if !isStale {
			return snapshot, false, nil
		}
		cr.refreshInBackground(locale, deviceArch)
		return snapshot, true, nil
	}

	// No cache available at all — nothing to serve while a background
	// refresh runs, so this path still fetches synchronously. Coalesce
	// identical concurrent callers via coldFetch (Finding 3 above) rather
	// than letting each fire its own request.
	key := locale + "\x00" + deviceArch
	v, err, _ := cr.coldFetch.Do(key, func() (any, error) {
		return cr.Fetch(ctx, locale, deviceArch)
	})
	if err != nil {
		return nil, false, err
	}

	return v.(*CatalogSnapshot), false, nil
}

// refreshInBackground kicks off an async catalog refetch unless one is
// already in flight (ut-docs#2143) — a second, third, … caller arriving
// while the cache is stale just serves that same stale snapshot rather than
// each starting its own network round-trip. Deliberately uses
// context.Background() rather than the triggering request's context: the
// request context is cancelled the moment its own HTTP handler returns,
// which would otherwise abort the refresh before it ever reaches the
// marketplace (the client's own RequestTimeoutSec still bounds the call).
func (cr *CatalogRepository) refreshInBackground(locale, deviceArch string) {
	cr.mu.Lock()
	if cr.refreshing {
		cr.mu.Unlock()
		return
	}
	cr.refreshing = true
	cr.mu.Unlock()

	go func() {
		defer func() {
			cr.mu.Lock()
			cr.refreshing = false
			cr.mu.Unlock()
			// A recover() here keeps the same offline-first promise the
			// scheduler's own background ticks make (see
			// pluginUpdateCheckTick's identical comment): this goroutine is
			// detached from any request, so an unrecovered panic here would
			// otherwise take down the whole till process, not just this one
			// refresh (ut-docs#2143 review, Finding 5).
			if r := recover(); r != nil {
				log.Printf("[WARN] recovered from panic in background catalog refresh (will retry on next stale read): %v", r)
			}
		}()
		if _, err := cr.Fetch(context.Background(), locale, deviceArch); err != nil {
			log.Printf("[WARN] background catalog refresh failed, keeping stale cache: %v", err)
		}
	}()
}

// Filter returns plugins matching the given criteria
func (cr *CatalogRepository) Filter(pluginType, developer, trustTier string) ([]PluginSummary, error) {
	snapshot, _, err := cr.Get()
	if err != nil {
		return nil, err
	}

	var filtered []PluginSummary
	for _, p := range snapshot.Plugins {
		match := true

		if pluginType != "" && p.CanonicalType != pluginType {
			match = false
		}
		if developer != "" && p.DeveloperID != developer {
			match = false
		}
		if trustTier != "" && p.TrustTier != trustTier {
			match = false
		}

		if match {
			filtered = append(filtered, p)
		}
	}

	return filtered, nil
}

// saveSnapshot writes the snapshot to disk
func (cr *CatalogRepository) saveSnapshot(snapshot *CatalogSnapshot) error {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(cr.snapshotPath, data, 0644)
}

// loadSnapshot reads the snapshot from disk
func (cr *CatalogRepository) loadSnapshot() (*CatalogSnapshot, error) {
	data, err := os.ReadFile(cr.snapshotPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var snapshot CatalogSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}

	return &snapshot, nil
}
