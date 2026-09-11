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
