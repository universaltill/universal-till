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
