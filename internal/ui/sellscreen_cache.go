package ui

import (
	"bufio"
	"bytes"
	"container/list"
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
)

// Sell-screen tile cache (ut-docs#2501). GET /ui/buttons (ButtonsHTTP.List)
// and GET /ui/buttons/category (CategoryItems) used to re-read the whole
// active catalog — items, hidden flags, barcodes, thumbnails, modifier/
// variant/current-price lookups, categories, per-category counts — and
// re-execute the template on EVERY render, although the output only changes
// when the catalog does. The rendered bytes are now kept in memory and served
// as-is until something that can change them changes.
//
// Invalidation contract — an entry is served only while ALL of these hold:
//
//  1. SellScreenVersion is unchanged: sync_admin_version.generation
//     (migration 023's triggers on every admin table — items, categories,
//     shortcut_buttons, settings, modifiers, variants, barcodes, translation
//     overrides, …) AND sell_screen_version.generation (migration 042's
//     triggers on price_history and item_images, the two sell-screen inputs
//     023 doesn't cover, plus migration 047's on every other catalog table
//     the tiles render from — items, categories, shortcut_buttons, barcodes,
//     variants, modifier groups and links, translation overrides; never
//     sales or settings, ut-docs#2765). The sell half alone is also the open
//     sale screen's live-refresh signal (SellVersionHeader, GET
//     /ui/buttons/version). Read in one query per request, before rendering
//     (the ensureCached ordering in internal/data/sync_admin_repo.go: a write
//     racing the render leaves the entry keyed on the OLDER version, never a
//     pre-write render on the post-write version).
//  2. now is before the next price_history starts_at/ends_at boundary that
//     was in the future when the render began (read before rendering, with
//     the version) — a scheduled price goes live with no write at all.
//  3. The entry is younger than sellScreenCacheMaxAge — the safety net for
//     inputs that live outside the database: an uploaded category image or
//     photo file arriving on disk (httpx.AssetExists / imgv's mtime).
//
// Everything else the HTML varies on is in the key (sellScreenKeyParts):
// route and its parameter, request locale, the process-global currency,
// the translator's version (a language-pack install or translation edit),
// the session's catalog_management grant (lock badges), the browsing mode
// and the All-tab toggle. The Designer's edit mode, search and the All-tab
// "load more" pages are never cached.
//
// Memory: a fixed byte budget and entry cap, LRU-evicted; an entry bigger
// than the whole budget is never stored; and on Linux, when MemAvailable
// (/proc/meminfo, read at most every sellScreenMemCheckEvery) is below
// sellScreenMemFloorBytes, nothing is stored and the cache is emptied.
const (
	sellScreenCacheBudgetBytes = 4 << 20
	sellScreenCacheMaxEntries  = 64
	sellScreenCacheMaxAge      = 5 * time.Minute
	sellScreenMemFloorBytes    = 64 << 20
	sellScreenMemCheckEvery    = 5 * time.Second
	// sellScreenEntryOverhead approximates the per-entry bookkeeping (list
	// element, map slot, struct, header map) on top of key + body bytes.
	sellScreenEntryOverhead = 256
)

// SellVersionHeader carries, on a GET /ui/buttons response, the
// sell_screen_version generation that grid was rendered (or cached) at
// (ut-docs#2765). web/public/sell-screen-watch.js records it and compares it
// with GET /ui/buttons/version to learn that the catalog changed outside this
// document (another till or tab, a cloud push, an admin sync pull).
const SellVersionHeader = "X-UT-Sell-Version"

// SellScreenVersion is the pair of change counters every cache entry is
// valid for (internal/data's SellScreenRepo.SellScreenVersion).
type SellScreenVersion struct {
	Admin int64
	Sell  int64
}

// CachedResponse is one rendered fragment. Body is shared with the cache and
// must be treated as read-only (http.ResponseWriter.Write never modifies its
// argument); Header is the caller's own copy.
type CachedResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

type sellScreenCacheOptions struct {
	now           func() time.Time
	memAvailable  func() (uint64, bool)
	budgetBytes   int
	maxEntries    int
	maxAge        time.Duration
	memFloorBytes uint64
	memCheckEvery time.Duration
}

type sellScreenEntry struct {
	key       string
	version   SellScreenVersion
	resp      CachedResponse
	expiresAt time.Time
	size      int
}

// SellScreenCache is safe for concurrent use. The zero value is not usable;
// build one with NewSellScreenCache. A nil *SellScreenCache on ButtonsHTTP
// means "no caching" (every existing ButtonsHTTP literal).
type SellScreenCache struct {
	opts sellScreenCacheOptions

	mu           sync.Mutex
	lru          *list.List // front = most recently used; values are *sellScreenEntry
	items        map[string]*list.Element
	used         int
	memCheckedAt time.Time
	memLow       bool
}

// NewSellScreenCache builds the production cache: the real clock, Linux
// /proc/meminfo, and the package's budget/age/floor constants.
func NewSellScreenCache() *SellScreenCache {
	return newSellScreenCache(sellScreenCacheOptions{
		now:           time.Now,
		memAvailable:  readMemAvailable,
		budgetBytes:   sellScreenCacheBudgetBytes,
		maxEntries:    sellScreenCacheMaxEntries,
		maxAge:        sellScreenCacheMaxAge,
		memFloorBytes: sellScreenMemFloorBytes,
		memCheckEvery: sellScreenMemCheckEvery,
	})
}

func newSellScreenCache(opts sellScreenCacheOptions) *SellScreenCache {
	return &SellScreenCache{opts: opts, lru: list.New(), items: map[string]*list.Element{}}
}

// Get returns the entry for key if it was stored for exactly version v and
// has not expired. A stale or expired entry is dropped on the way.
func (c *SellScreenCache) Get(key string, v SellScreenVersion) (CachedResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return CachedResponse{}, false
	}
	e := el.Value.(*sellScreenEntry)
	if e.version != v || !c.opts.now().Before(e.expiresAt) {
		c.removeLocked(el)
		return CachedResponse{}, false
	}
	c.lru.MoveToFront(el)
	return CachedResponse{Status: e.resp.Status, Header: e.resp.Header.Clone(), Body: e.resp.Body}, true
}

// Put stores resp under key for version v, expiring at the earlier of
// now+maxAge and priceBoundary (zero = no boundary). It reports whether the
// entry was stored: false for an oversize body, a boundary already passed,
// or low system memory (which also empties the cache).
func (c *SellScreenCache) Put(key string, v SellScreenVersion, resp CachedResponse, priceBoundary time.Time) bool {
	size := sellScreenEntrySize(key, resp)
	c.mu.Lock()
	defer c.mu.Unlock()
	if size > c.opts.budgetBytes || c.opts.maxEntries <= 0 {
		return false
	}
	now := c.opts.now()
	expiresAt := now.Add(c.opts.maxAge)
	if !priceBoundary.IsZero() && priceBoundary.Before(expiresAt) {
		expiresAt = priceBoundary
	}
	if !now.Before(expiresAt) {
		return false
	}
	if c.memoryLowLocked(now) {
		c.purgeLocked()
		return false
	}
	if el, ok := c.items[key]; ok {
		c.removeLocked(el)
	}
	for c.lru.Len() > 0 && (c.used+size > c.opts.budgetBytes || c.lru.Len() >= c.opts.maxEntries) {
		c.removeLocked(c.lru.Back())
	}
	e := &sellScreenEntry{key: key, version: v, resp: CachedResponse{Status: resp.Status, Header: resp.Header.Clone(), Body: resp.Body}, expiresAt: expiresAt, size: size}
	c.items[key] = c.lru.PushFront(e)
	c.used += size
	return true
}

func (c *SellScreenCache) removeLocked(el *list.Element) {
	e := el.Value.(*sellScreenEntry)
	c.lru.Remove(el)
	delete(c.items, e.key)
	c.used -= e.size
}

func (c *SellScreenCache) purgeLocked() {
	c.lru.Init()
	c.items = map[string]*list.Element{}
	c.used = 0
}

// memoryLowLocked reads MemAvailable at most once per memCheckEvery and
// reports whether it was below the floor. Unreadable (non-Linux, a
// restricted /proc) = not low: the byte budget alone then bounds the cache.
func (c *SellScreenCache) memoryLowLocked(now time.Time) bool {
	if c.opts.memAvailable == nil {
		return false
	}
	if !c.memCheckedAt.IsZero() && now.Sub(c.memCheckedAt) < c.opts.memCheckEvery {
		return c.memLow
	}
	avail, ok := c.opts.memAvailable()
	c.memCheckedAt = now
	c.memLow = ok && avail < c.opts.memFloorBytes
	return c.memLow
}

func sellScreenEntrySize(key string, resp CachedResponse) int {
	n := len(key) + len(resp.Body) + sellScreenEntryOverhead
	for k, vs := range resp.Header {
		n += len(k)
		for _, v := range vs {
			n += len(v)
		}
	}
	return n
}

func readMemAvailable() (uint64, bool) { return readMemAvailableFrom("/proc/meminfo") }

// readMemAvailableFrom parses the "MemAvailable: N kB" line of a
// /proc/meminfo-format file, in bytes.
func readMemAvailableFrom(path string) (uint64, bool) {
	f, err := os.Open(path) //nolint:gosec // fixed /proc path (tests pass a temp file)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		rest, ok := strings.CutPrefix(sc.Text(), "MemAvailable:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0, false
		}
		kb, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}

// sellScreenKeyParts is everything besides SellScreenVersion that a cached
// fragment's bytes vary on — see the invalidation contract above. Anything
// new the fragment starts varying on must be added here, or two different
// renders would share one entry.
type sellScreenKeyParts struct {
	Route        string // "list" | "category"
	Param        string // category id for "category"
	Locale       string
	Currency     string
	Translations string
	Granted      bool
	BrowsingMode string
}

// String encodes the parts unambiguously: every field is Go-quoted, so a
// separator inside a value (a category id is request input) can't forge
// another key.
func (p sellScreenKeyParts) String() string {
	var b strings.Builder
	for _, s := range []string{p.Route, p.Param, p.Locale, p.Currency, p.Translations, strconv.FormatBool(p.Granted), p.BrowsingMode} {
		b.WriteString(strconv.Quote(s))
		b.WriteByte('|')
	}
	return b.String()
}

func (h *ButtonsHTTP) sellScreenKey(route, param string) string {
	return sellScreenKeyParts{
		Route:        route,
		Param:        param,
		Locale:       h.Locale,
		Currency:     httpx.ActiveCurrency().Code,
		Translations: httpx.TranslationsVersion(),
		Granted:      h.Granted,
		BrowsingMode: h.BrowsingMode,
	}.String()
}

// bufferedResponse captures a render so it can be both cached and written.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newBufferedResponse() *bufferedResponse {
	return &bufferedResponse{header: http.Header{}}
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}

// response finalizes the capture the way net/http would have: status 200
// when nothing was written explicitly, and a sniffed Content-Type when the
// render set none (net/http sniffs the same first 512 bytes).
func (b *bufferedResponse) response() CachedResponse {
	status := b.status
	if status == 0 {
		status = http.StatusOK
	}
	body := b.body.Bytes()
	if b.header.Get("Content-Type") == "" && len(body) > 0 {
		b.header.Set("Content-Type", http.DetectContentType(body))
	}
	return CachedResponse{Status: status, Header: b.header, Body: body}
}

func writeCachedResponse(w http.ResponseWriter, resp CachedResponse) {
	dst := w.Header()
	for k, vs := range resp.Header {
		dst[k] = vs
	}
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
}

// serveSellScreen answers from h.Cache when it can, and otherwise runs
// render — which reports whether its output is clean enough to cache (no
// load error, no degraded inner lookup, no template error) — capturing the result to store and write. With no
// cache, or no trustworthy version (a missing counter row, a failed read),
// render writes straight to w exactly as before this cache existed.
//
// versionHeader (ut-docs#2765) stamps the response with SellVersionHeader —
// the sell_screen_version generation this entry is keyed on — on both the
// cache hit and the fresh render, so the sale screen's watcher knows which
// catalog state the grid on screen shows. Only /ui/buttons asks for it: the
// watcher compares against the grid's own render, never a popup's.
//
// ut-docs#2989: the same generation also rides on the request context into
// render (sellVersionFrom), so the grid's root can carry it as
// data-sell-version — GET /'s inline first-paint grid has no response header
// of its own for the watcher to read. Embedding it in the cached body is
// correct: an entry is only ever served for exactly the SellScreenVersion it
// was stored under (Get compares both counters), so a hit's body always
// carries the current Sell generation — the same value the header gets.
//
// It reports whether what was written is a clean render: a cache hit (only
// clean renders are ever stored) or a render that returned true.
func (h *ButtonsHTTP) serveSellScreen(w http.ResponseWriter, r *http.Request, key string, versionHeader bool, render func(http.ResponseWriter, *http.Request) bool) bool {
	if h.Cache == nil {
		return render(w, r)
	}
	ctx := r.Context()
	v, ok, err := h.Store.SellScreenVersion(ctx)
	if err != nil {
		logging.L().Warnf("ui: sell screen cache: read version failed, rendering uncached: %v", err)
	}
	if err != nil || !ok {
		return render(w, r)
	}
	if versionHeader {
		// Set on w, never on the captured render: the stored entry's headers
		// are copied over w on a hit, and this value is the key's own.
		w.Header().Set(SellVersionHeader, strconv.FormatInt(v.Sell, 10))
		r = r.WithContext(context.WithValue(ctx, sellVersionCtxKey{}, v.Sell))
	}
	if resp, hit := h.Cache.Get(key, v); hit {
		writeCachedResponse(w, resp)
		return true
	}
	// The next price boundary is read BEFORE rendering, like the version
	// (review finding 2): read after, a boundary passing between the render's
	// current-price read and this read would no longer be in the future, and
	// the pre-boundary render would be stored for the full max age. Read
	// first, a boundary that passes mid-render is the entry's expiry, already
	// passed at Put — which then refuses it.
	boundary, boundaryErr := h.Store.NextPriceBoundary(ctx)
	if boundaryErr != nil {
		logging.L().Warnf("ui: sell screen cache: read next price boundary failed, not caching: %v", boundaryErr)
	}
	buf := newBufferedResponse()
	clean := render(buf, r)
	resp := buf.response()
	if clean && boundaryErr == nil && resp.Status == http.StatusOK {
		h.Cache.Put(key, v, resp, boundary)
	}
	writeCachedResponse(w, resp)
	return clean
}

// sellVersionCtxKey carries serveSellScreen's Sell generation to the render
// (ut-docs#2989).
type sellVersionCtxKey struct{}

// sellVersionFrom is the Sell generation serveSellScreen read for this
// request, as the string the X-UT-Sell-Version header carries — "" when the
// render is uncached (no cache, no trustworthy version), which is exactly
// when that header is absent too.
func sellVersionFrom(ctx context.Context) string {
	if v, ok := ctx.Value(sellVersionCtxKey{}).(int64); ok {
		return strconv.FormatInt(v, 10)
	}
	return ""
}

// ListFragment renders the sale screen's grid (List, never EditMode) into
// memory for GET /'s first paint (ut-docs#2989): the same bytes, and the same
// #2501 cache entry, GET /ui/buttons serves — ok only for a clean 200 render.
// A caller falls back to the lazy hx-get placeholder when !ok, so a failed
// render never ships a blank or half-rendered grid.
func (h *ButtonsHTTP) ListFragment(r *http.Request) ([]byte, bool) {
	if h.EditMode {
		return nil, false
	}
	buf := newBufferedResponse()
	clean := h.serveSellScreen(buf, r, h.sellScreenKey("list", ""), true, h.renderList)
	resp := buf.response()
	if !clean || resp.Status != http.StatusOK || len(resp.Body) == 0 {
		return nil, false
	}
	return resp.Body, true
}
