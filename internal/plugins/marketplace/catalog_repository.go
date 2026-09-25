package marketplace

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
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

// CatalogRepository manages on-disk catalog snapshots with stale markers.
//
// The catalog is filtered server-side by (locale, device arch), so every
// cached snapshot belongs to exactly one such key (ut-docs#2674): one memory
// slot and one file per key. A single shared slot let each caller's refresh
// replace another caller's differently-filtered result — /plugins (UI
// locale) and /plugins/store (shop default locale) overwrote each other, and
// the update checker saw whichever wrote last.
type CatalogRepository struct {
	client     *Client
	cacheDir   string
	mu         sync.RWMutex
	cached     map[string]*CatalogSnapshot
	staleAfter time.Duration
	// refreshing guards against duplicate concurrent background refreshes
	// (ut-docs#2143) — GetOrFetch sets it (per key) before spawning a
	// refresh goroutine and clears it when that goroutine finishes, so a
	// second caller for the same key arriving while one is already in
	// flight just serves the stale cache too instead of starting its own
	// network round-trip.
	refreshing map[string]bool
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

// legacySnapshotFile is the single cache file every (locale, arch) shared
// before ut-docs#2674. It is still read — never written — as a fallback for
// the one key it records, so a till upgraded while offline keeps serving
// its catalog until the first successful per-key fetch.
const legacySnapshotFile = "catalog-snapshot.json"

// Key components end up in a file name, and the locale can come from a
// request (?lang=), so both are validated rather than escaped: a BCP 47-ish
// locale and an "os/arch" pair, each possibly empty ("no filter").
var (
	catalogLocaleRE = regexp.MustCompile(`^[A-Za-z0-9_-]{0,35}$`)
	catalogArchRE   = regexp.MustCompile(`^([A-Za-z0-9_]{1,32}(/[A-Za-z0-9_]{1,32})?)?$`)
)

// DeviceArch is the "os/arch" pair this till runs on — the arch every
// till-scoped catalog read and fetch uses.
func DeviceArch() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

// defaultCatalogLocale is the locale a till asks the catalog for when the
// shop has none configured.
const defaultCatalogLocale = "en-US"

// TillCatalogKey is the (locale, arch) snapshot this till's own reads use —
// the plugin store, /plugins, the update checker and the scheduler that
// keeps it fresh all share it, so they can't disagree (ut-docs#2674).
func TillCatalogKey(defaultLocale string) (locale, deviceArch string) {
	if defaultLocale == "" {
		defaultLocale = defaultCatalogLocale
	}
	return defaultLocale, DeviceArch()
}

// catalogKey returns the cache key for (locale, deviceArch), or an error
// when either component can't safely name a file.
func catalogKey(locale, deviceArch string) (string, error) {
	if !catalogLocaleRE.MatchString(locale) {
		return "", fmt.Errorf("invalid catalog locale %q", locale)
	}
	if !catalogArchRE.MatchString(deviceArch) {
		return "", fmt.Errorf("invalid catalog device arch %q", deviceArch)
	}
	return locale + "\x00" + deviceArch, nil
}

// snapshotFileName is the per-key cache file. '@' marks an empty component
// and '+' replaces the arch's '/'; neither is allowed inside a validated
// component, so distinct keys never share a file.
func snapshotFileName(locale, deviceArch string) string {
	part := func(v string) string {
		if v == "" {
			return "@"
		}
		return strings.ReplaceAll(v, "/", "+")
	}
	return "catalog-snapshot." + part(locale) + "." + part(deviceArch) + ".json"
}

// NewCatalogRepository creates a catalog repository
func NewCatalogRepository(client *Client, cacheDir string) (*CatalogRepository, error) {
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache dir: %w", err)
	}

	return &CatalogRepository{
		client:     client,
		cacheDir:   cacheDir,
		cached:     map[string]*CatalogSnapshot{},
		refreshing: map[string]bool{},
		staleAfter: 15 * time.Minute,
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
	key, err := catalogKey(locale, deviceArch)
	if err != nil {
		return nil, err
	}
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

	// Monotonic-write guard (ut-docs#2155): never replace a chronologically
	// NEWER cached snapshot for the same key with an older one. Two independent callers can
	// both be mid-flight here at once — server.go's scheduler (syncCatalog)
	// calls Fetch directly and so bypasses GetOrFetch's refreshing/coldFetch
	// coalescing, while a page handler's GetOrFetch drives its own
	// refreshInBackground/coldFetch path — and each stamps its own FetchedAt
	// (time.Now(), above) BEFORE racing for cr.mu, not after. So goroutine
	// scheduling can let the fetch with the numerically OLDER FetchedAt
	// acquire the lock SECOND, and the unconditional write that used to
	// live here then clobbered a newer, already-committed snapshot with
	// staler data: the cache silently went "stale again" sooner, and the
	// on-disk snapshot regressed with it. Rare, but real, and undetectable
	// from the outside. Comparing FetchedAt under the lock closes it — the
	// later stamp wins the cache regardless of lock-acquisition order.
	//
	// Both writes (disk AND memory) are skipped together: guarding only one
	// would leave the two disagreeing, and a restart would then reload the
	// older on-disk copy over the newer in-memory one.
	//
	// The RETURN VALUE is deliberately still this call's own snapshot,
	// never the cached one: each caller's answer is what its own request
	// produced, whether or not it won the cache slot.
	//
	// INVARIANT this comparison depends on: cr.cached[key] is only ever assigned
	// from an in-process time.Now() (the line at the bottom of this function
	// is the only production write), so BOTH times carry a monotonic reading
	// and After() compares monotonically — immune to the wall clock being
	// stepped, which matters on a till whose RTC boots wrong and is then
	// corrected by NTP. Do NOT start warming cr.cached from loadSnapshot()
	// (e.g. to save Get() re-reading the file on every call while the cache
	// is cold) without revisiting this: a JSON round-trip DROPS the monotonic
	// reading, so a snapshot written with a bad future RTC date would then
	// compare as permanently newer and freeze the cache until real time
	// caught up.
	if prev := cr.cached[key]; prev != nil && !snapshot.FetchedAt.After(prev.FetchedAt) {
		return snapshot, nil
	}

	// Save to disk
	if err := cr.saveSnapshot(snapshot); err != nil {
		return nil, fmt.Errorf("failed to save snapshot: %w", err)
	}

	cr.cached[key] = snapshot
	return snapshot, nil
}

// Get returns the cached catalog for (locale, deviceArch), marking it as
// stale if expired. It never returns another key's snapshot.
func (cr *CatalogRepository) Get(locale, deviceArch string) (*CatalogSnapshot, bool, error) {
	key, err := catalogKey(locale, deviceArch)
	if err != nil {
		return nil, false, err
	}

	cr.mu.RLock()
	defer cr.mu.RUnlock()

	// Try memory cache first
	if cached := cr.cached[key]; cached != nil {
		isStale := time.Since(cached.FetchedAt) > cr.staleAfter
		return cached, isStale, nil
	}

	// Load from disk
	snapshot, err := cr.loadSnapshot(locale, deviceArch)
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
	key, err := catalogKey(locale, deviceArch)
	if err != nil {
		return nil, false, err
	}

	// Try to get cached first
	snapshot, isStale, err := cr.Get(locale, deviceArch)
	if err == nil {
		if !isStale {
			return snapshot, false, nil
		}
		cr.refreshInBackground(key, locale, deviceArch)
		return snapshot, true, nil
	}

	// No cache available at all — nothing to serve while a background
	// refresh runs, so this path still fetches synchronously. Coalesce
	// identical concurrent callers via coldFetch (Finding 3 above) rather
	// than letting each fire its own request.
	v, err, _ := cr.coldFetch.Do(key, func() (any, error) {
		return cr.Fetch(ctx, locale, deviceArch)
	})
	if err != nil {
		return nil, false, err
	}

	return v.(*CatalogSnapshot), false, nil
}

// refreshInBackground kicks off an async catalog refetch unless one is
// already in flight for the same key (ut-docs#2143) — a second, third, … caller arriving
// while the cache is stale just serves that same stale snapshot rather than
// each starting its own network round-trip. Deliberately uses
// context.Background() rather than the triggering request's context: the
// request context is cancelled the moment its own HTTP handler returns,
// which would otherwise abort the refresh before it ever reaches the
// marketplace (the client's own RequestTimeoutSec still bounds the call).
func (cr *CatalogRepository) refreshInBackground(key, locale, deviceArch string) {
	cr.mu.Lock()
	if cr.refreshing[key] {
		cr.mu.Unlock()
		return
	}
	cr.refreshing[key] = true
	cr.mu.Unlock()

	go func() {
		defer func() {
			cr.mu.Lock()
			delete(cr.refreshing, key)
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

// Filter returns plugins matching the given criteria.
//
// No production caller (ut-docs#1566). Every production consumer of the
// snapshot ranges snapshot.Plugins itself and filters inline —
// internal/pages/plugins_store_page.go (the store browse, deliberately
// unfiltered), internal/plugins/catalog_match.go (IndexCatalog, keyed by
// listing and by developer+name), and the three setup-wizard lookups
// (internal/pages/setup_base_plugins.go, setup_tax_catalog.go,
// setup_language_catalog.go), which each need CanonicalType AND a locale
// match over AvailableLocales — a criterion this (type, developer,
// trustTier) signature can't express, which is why none of them adopted
// it. Kept as the assertion helper for TestCatalogRepository_Filter and
// TestCatalogRepository_FetchPagesThroughFullCatalog (the latter uses it
// to prove a listing beyond page 1 landed in the snapshot); a candidate
// for deletion together with those two tests if it never grows a
// production caller.
func (cr *CatalogRepository) Filter(locale, deviceArch, pluginType, developer, trustTier string) ([]PluginSummary, error) {
	snapshot, _, err := cr.Get(locale, deviceArch)
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

// SeedSnapshot writes snapshot to disk under its own (Locale, DeviceArch)
// key without touching the in-memory cache, exactly as if an earlier run had
// fetched it. For tests and fixtures that need a catalog with no network.
func (cr *CatalogRepository) SeedSnapshot(snapshot *CatalogSnapshot) error {
	if _, err := catalogKey(snapshot.Locale, snapshot.DeviceArch); err != nil {
		return err
	}
	return cr.saveSnapshot(snapshot)
}

// saveSnapshot writes the snapshot to its per-key file on disk
func (cr *CatalogRepository) saveSnapshot(snapshot *CatalogSnapshot) error {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cr.cacheDir, 0755); err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(cr.cacheDir, snapshotFileName(snapshot.Locale, snapshot.DeviceArch)), data, 0644)
}

// loadSnapshot reads the (locale, deviceArch) snapshot from disk, falling
// back to the pre-ut-docs#2674 single file only when that file was written
// for this same key. (nil, nil) means nothing is cached for the key.
func (cr *CatalogRepository) loadSnapshot(locale, deviceArch string) (*CatalogSnapshot, error) {
	// Both files are checked against the key they recorded: the legacy file
	// serves only its one key, and on a case-insensitive filesystem
	// "en-US" and "en-us" share a per-key file.
	for _, name := range []string{snapshotFileName(locale, deviceArch), legacySnapshotFile} {
		snapshot, err := readSnapshotFile(filepath.Join(cr.cacheDir, name))
		if err != nil {
			return nil, err
		}
		if snapshot != nil && snapshot.Locale == locale && snapshot.DeviceArch == deviceArch {
			return snapshot, nil
		}
	}
	return nil, nil
}

// readSnapshotFile decodes one snapshot file; (nil, nil) when it is absent.
func readSnapshotFile(path string) (*CatalogSnapshot, error) {
	data, err := os.ReadFile(path)
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
