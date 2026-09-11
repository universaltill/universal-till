package catalog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/testsupport"
)

// getHX is get (handlers_coverage_test.go) with extra request headers, for
// exercising the htmx-vs-full-page branch each of these routes now has
// (ut-docs#1950).
func getHX(t *testing.T, mux *http.ServeMux, path string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// ut-docs#1950: /catalog is the /items rail's default ("Library") section.
// An htmx request (from that panel) must get just the "content" block, plus
// an out-of-band refresh of the rail with /catalog marked is-current — not
// the full standalone page's chrome.
func TestCatalogPage_HXRequestReturnsContentFragmentWithOOBRail(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Cola", BasePrice: 100, IsActive: true})
	testsupport.SeedTaxCode(t, db, "tax_std", "Standard", 2000)

	rec := getHX(t, mux, "/catalog", "HX-Request", "true")
	if rec.Code != http.StatusOK {
		t.Fatalf("htmx GET /catalog: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Cola") {
		t.Errorf("fragment missing catalog content: %s", body)
	}
	if strings.Contains(body, "<html") || strings.Contains(body, `class="nav"`) {
		t.Errorf("htmx request re-rendered the whole page shell: %s", body)
	}
	if !strings.Contains(body, `id="items-rail"`) || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Fatalf("fragment missing the OOB items-rail swap: %s", body)
	}
	railStart := strings.Index(body, `id="items-rail"`)
	rail := body[railStart:]
	if got := strings.Count(rail, "is-current"); got != 1 {
		t.Errorf("OOB rail has %d is-current rows, want exactly 1: %s", got, rail)
	}
	catalogIdx := strings.Index(rail, `href="/catalog"`)
	if catalogIdx < 0 {
		t.Fatalf("OOB rail missing the /catalog row: %s", rail)
	}
	tagStart := strings.LastIndex(rail[:catalogIdx], "<a ")
	tagEnd := strings.Index(rail[tagStart:], ">") + tagStart
	if !strings.Contains(rail[tagStart:tagEnd], "is-current") {
		t.Errorf("the /catalog row itself is not marked is-current: %s", rail[tagStart:tagEnd])
	}
}

// A plain browser GET (no HX-Request) must still render the exact same full
// standalone page as before this card — /catalog stays directly linkable.
func TestCatalogPage_NonHXRequestStillRendersFullPage(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Cola", BasePrice: 100, IsActive: true})
	testsupport.SeedTaxCode(t, db, "tax_std", "Standard", 2000)

	rec := get(t, mux, "/catalog")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<html") || !strings.Contains(body, `class="nav"`) {
		t.Errorf("expected the full standalone page shell, got: %s", body)
	}
	if !strings.Contains(body, "Cola") {
		t.Errorf("expected catalog content, got: %s", body)
	}
}

// ut-docs#433's htmx-history-restore rule (mirrored from /help/{topic}, see
// httpx.IsFragmentSwap): a restored bfcache'd URL re-requests with BOTH
// headers set and must get the full page back, not a bare fragment.
func TestCatalogPage_HXHistoryRestoreReturnsFullPage(t *testing.T) {
	mux, _ := newCatalogMux(t)
	rec := getHX(t, mux, "/catalog", "HX-Request", "true", "HX-History-Restore-Request", "true")
	if rec.Code != http.StatusOK {
		t.Fatalf("history-restore GET /catalog: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<html") || !strings.Contains(body, `class="nav"`) {
		t.Errorf("history-restore response did not render the full page shell: %s", body)
	}
}

// ut-docs#2090 review finding: nothing previously asserted the NEGATIVE
// branch of catalog.html's own Modifiers/Option-sets top-action buttons —
// every existing test only checked the htmx-fragment (.InItemsShell=true)
// case. htmx 1.9.x preventDefault()s a click on an <a href> carrying
// hx-get BEFORE resolving hx-target; on failure it fires htmx:targetError
// and does nothing else. So if the .InItemsShell guard were ever dropped
// or inverted, a bare (non-shell) /catalog visitor would get two silently
// dead buttons — no navigation, no swap, just a console error — and every
// test that only exercises the htmx-fragment branch would stay green.
// This pins the bare-page case: plain hrefs, no hx- attributes at all.
func TestCatalogPage_NonHXRequest_TopActionButtonsAreNotHXEnabled(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Cola", BasePrice: 100, IsActive: true})
	testsupport.SeedTaxCode(t, db, "tax_std", "Standard", 2000)

	rec := get(t, mux, "/catalog")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, id := range []string{"catalog-modifiers-btn", "catalog-option-sets-btn"} {
		tagStart := strings.Index(body, `id="`+id+`"`)
		if tagStart < 0 {
			t.Fatalf("button #%s not found in bare /catalog response: %s", id, body)
		}
		tagEnd := strings.Index(body[tagStart:], ">") + tagStart
		tag := body[tagStart:tagEnd]
		if strings.Contains(tag, "hx-get") || strings.Contains(tag, "hx-target") {
			t.Errorf("#%s must stay a plain link on a bare (non-shell) /catalog page, got: %s", id, tag)
		}
	}
}

// The htmx-fragment (.InItemsShell=true) mirror of the test above: inside
// the /items shell, both buttons DO carry the in-panel-swap attributes.
func TestCatalogPage_HXRequest_TopActionButtonsAreHXEnabled(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Cola", BasePrice: 100, IsActive: true})
	testsupport.SeedTaxCode(t, db, "tax_std", "Standard", 2000)

	rec := getHX(t, mux, "/catalog", "HX-Request", "true")
	if rec.Code != http.StatusOK {
		t.Fatalf("htmx GET /catalog: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, tc := range []struct{ id, href string }{
		{"catalog-modifiers-btn", "/modifiers"},
		{"catalog-option-sets-btn", "/catalog/option-sets"},
	} {
		tagStart := strings.Index(body, `id="`+tc.id+`"`)
		if tagStart < 0 {
			t.Fatalf("button #%s not found in htmx-fragment /catalog response: %s", tc.id, body)
		}
		tagEnd := strings.Index(body[tagStart:], ">") + tagStart
		tag := body[tagStart:tagEnd]
		if !strings.Contains(tag, `hx-get="`+tc.href+`"`) || !strings.Contains(tag, `hx-target="#items-panel"`) {
			t.Errorf("#%s must swap #items-panel in place inside the /items shell, got: %s", tc.id, tag)
		}
	}
}

// ut-docs#1950: same htmx-fragment-plus-OOB-rail treatment for /modifiers,
// the rail's second live section.
func TestModifiersPage_HXRequestReturnsContentFragmentWithOOBRail(t *testing.T) {
	mux, _ := newCatalogMux(t)
	rec := getHX(t, mux, "/modifiers", "HX-Request", "true")
	if rec.Code != http.StatusOK {
		t.Fatalf("htmx GET /modifiers: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "<html") || strings.Contains(body, `class="nav"`) {
		t.Errorf("htmx request re-rendered the whole page shell: %s", body)
	}
	railStart := strings.Index(body, `id="items-rail"`)
	if railStart < 0 || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Fatalf("fragment missing the OOB items-rail swap: %s", body)
	}
	rail := body[railStart:]
	modIdx := strings.Index(rail, `href="/modifiers"`)
	if modIdx < 0 {
		t.Fatalf("OOB rail missing the /modifiers row: %s", rail)
	}
	tagStart := strings.LastIndex(rail[:modIdx], "<a ")
	tagEnd := strings.Index(rail[tagStart:], ">") + tagStart
	if !strings.Contains(rail[tagStart:tagEnd], "is-current") {
		t.Errorf("the /modifiers row itself is not marked is-current: %s", rail[tagStart:tagEnd])
	}
}

func TestModifiersPage_NonHXRequestStillRendersFullPage(t *testing.T) {
	mux, _ := newCatalogMux(t)
	rec := get(t, mux, "/modifiers")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /modifiers: %d %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "<html") || !strings.Contains(body, `class="nav"`) {
		t.Errorf("expected the full standalone page shell, got: %s", body)
	}
}

// ut-docs#1950: same treatment again for /catalog/option-sets, the rail's
// fifth section.
func TestOptionSetsPage_HXRequestReturnsContentFragmentWithOOBRail(t *testing.T) {
	mux, _ := newCatalogMux(t)
	rec := getHX(t, mux, "/catalog/option-sets", "HX-Request", "true")
	if rec.Code != http.StatusOK {
		t.Fatalf("htmx GET /catalog/option-sets: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "<html") || strings.Contains(body, `class="nav"`) {
		t.Errorf("htmx request re-rendered the whole page shell: %s", body)
	}
	railStart := strings.Index(body, `id="items-rail"`)
	if railStart < 0 || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Fatalf("fragment missing the OOB items-rail swap: %s", body)
	}
	rail := body[railStart:]
	idx := strings.Index(rail, `href="/catalog/option-sets"`)
	if idx < 0 {
		t.Fatalf("OOB rail missing the /catalog/option-sets row: %s", rail)
	}
	tagStart := strings.LastIndex(rail[:idx], "<a ")
	tagEnd := strings.Index(rail[tagStart:], ">") + tagStart
	if !strings.Contains(rail[tagStart:tagEnd], "is-current") {
		t.Errorf("the /catalog/option-sets row itself is not marked is-current: %s", rail[tagStart:tagEnd])
	}
}

func TestOptionSetsPage_NonHXRequestStillRendersFullPage(t *testing.T) {
	mux, _ := newCatalogMux(t)
	rec := get(t, mux, "/catalog/option-sets")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog/option-sets: %d %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "<html") || !strings.Contains(body, `class="nav"`) {
		t.Errorf("expected the full standalone page shell, got: %s", body)
	}
}

// ut-docs#2091: /catalog, /modifiers and /catalog/option-sets all return a
// different body depending on the HX-Request header (a bare fragment vs.
// the full standalone page) — with no Vary header, a browser/WebView cache
// keyed on the URL alone can serve one to a request that wanted the other
// (the reported symptom: a cached fragment rendered unstyled on Android
// Back). Both branches of all three routes must carry the header.
func TestCatalogRailRoutes_VaryHXRequestOnBothBranches(t *testing.T) {
	for _, path := range []string{"/catalog", "/modifiers", "/catalog/option-sets"} {
		t.Run(path, func(t *testing.T) {
			mux, _ := newCatalogMux(t)

			fragRec := getHX(t, mux, path, "HX-Request", "true")
			if got := fragRec.Header().Get("Vary"); got != "HX-Request" {
				t.Errorf("fragment branch: Vary header = %q, want %q", got, "HX-Request")
			}

			fullRec := get(t, mux, path)
			if got := fullRec.Header().Get("Vary"); got != "HX-Request" {
				t.Errorf("full-page branch: Vary header = %q, want %q", got, "HX-Request")
			}
		})
	}
}

// cacheEntry is what a Vary-respecting HTTP cache keeps per URL: the
// response plus the request-header values it was served under, read from
// the response's own Vary header at store time.
type cacheEntry struct {
	body       string
	varyValues map[string]string
}

// varyAwareCache is a minimal stand-in for a browser/WebView HTTP cache,
// just enough to prove the actual caching mechanism ut-docs#2091 is about.
// A real cache keyed on the URL ALONE is exactly the pre-fix bug: it would
// unconditionally return whatever body it first stored under that URL,
// fragment or full page, regardless of what a later request actually
// wanted. Honoring Vary means the cache key also has to include the
// request's value for every header the response's own Vary lists — so a
// fragment cached under "HX-Request: true" is never handed to a request
// that has no such header.
type varyAwareCache struct {
	mux     *http.ServeMux
	entries map[string]cacheEntry
}

func (c *varyAwareCache) get(t *testing.T, path string, reqHeaders map[string]string) string {
	t.Helper()
	if entry, ok := c.entries[path]; ok {
		hit := true
		for h, v := range entry.varyValues {
			if reqHeaders[h] != v {
				hit = false
				break
			}
		}
		if hit {
			return entry.body
		}
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for h, v := range reqHeaders {
		req.Header.Set(h, v)
	}
	rec := httptest.NewRecorder()
	c.mux.ServeHTTP(rec, req)

	entry := cacheEntry{body: rec.Body.String(), varyValues: map[string]string{}}
	if vary := rec.Header().Get("Vary"); vary != "" {
		for _, h := range strings.Split(vary, ",") {
			h = strings.TrimSpace(h)
			entry.varyValues[h] = reqHeaders[h]
		}
	}
	c.entries[path] = entry
	return entry.body
}

// ut-docs#2091's actual reported path: the /items rail fetches /catalog as
// an htmx fragment and pushes /catalog into browser history; a later plain
// navigation to that same URL (e.g. the Android hardware Back button) must
// NOT be served the cached fragment. This drives that exact sequence
// through a Vary-respecting cache in front of the real handler: without
// Vary: HX-Request on the response, the cache's key would be the URL alone
// and the second request would wrongly hit the first's cached fragment
// (entry.varyValues would be empty, so every request "matches"); with it
// present, the two requests key differently and the second is a genuine
// cache miss that reaches the real handler for the full page.
func TestCatalogFragmentCache_VaryPreventsFragmentServedToPlainNavigation(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Cola", BasePrice: 100, IsActive: true})
	testsupport.SeedTaxCode(t, db, "tax_std", "Standard", 2000)

	cache := &varyAwareCache{mux: mux, entries: map[string]cacheEntry{}}

	fragBody := cache.get(t, "/catalog", map[string]string{"HX-Request": "true"})
	if strings.Contains(fragBody, "<html") {
		t.Fatalf("expected a bare fragment from the first (htmx) request, got a full page: %s", fragBody)
	}

	fullBody := cache.get(t, "/catalog", map[string]string{})
	if !strings.Contains(fullBody, "<html") || !strings.Contains(fullBody, `class="nav"`) {
		t.Errorf("plain navigation after a cached fragment got the cached fragment back instead of the full page (ut-docs#2091's reported bug): %s", fullBody)
	}
}
