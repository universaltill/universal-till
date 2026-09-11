package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ut-docs#1950: /inventory is one of the /items rail's five section
// destinations. An htmx request (from that panel) must get just the
// "content" block, plus an out-of-band refresh of the rail with
// /inventory marked is-current — not the full standalone page's chrome.
func TestInventoryPage_HXRequestReturnsContentFragmentWithOOBRail(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	registerInventoryPage(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/inventory", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("htmx GET /inventory: %d %s", rec.Code, rec.Body.String())
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
	idx := strings.Index(rail, `href="/inventory"`)
	if idx < 0 {
		t.Fatalf("OOB rail missing the /inventory row: %s", rail)
	}
	tagStart := strings.LastIndex(rail[:idx], "<a ")
	tagEnd := strings.Index(rail[tagStart:], ">") + tagStart
	if !strings.Contains(rail[tagStart:tagEnd], "is-current") {
		t.Errorf("the /inventory row itself is not marked is-current: %s", rail[tagStart:tagEnd])
	}
}

// A plain browser GET (no HX-Request) must still render the exact same full
// standalone page as before this card.
func TestInventoryPage_NonHXRequestStillRendersFullPage(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	registerInventoryPage(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/inventory", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /inventory: %d %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "<html") || !strings.Contains(body, `class="nav"`) {
		t.Errorf("expected the full standalone page shell, got: %s", body)
	}
}

// ut-docs#433's htmx-history-restore rule (mirrored from /help/{topic}, see
// httpx.IsFragmentSwap): a restored bfcache'd URL re-requests with BOTH
// headers set and must get the full page back, not a bare fragment.
func TestInventoryPage_HXHistoryRestoreReturnsFullPage(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	registerInventoryPage(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/inventory", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-History-Restore-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("history-restore GET /inventory: %d %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "<html") || !strings.Contains(body, `class="nav"`) {
		t.Errorf("history-restore response did not render the full page shell: %s", body)
	}
}

// ut-docs#2091: /inventory returns a different body depending on the
// HX-Request header (a bare fragment vs. the full standalone page) — with
// no Vary header, a browser/WebView cache keyed on the URL alone can serve
// one to a request that wanted the other. Both branches must carry it.
func TestInventoryPage_VaryHXRequestOnBothBranches(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	registerInventoryPage(mux, dp)

	fragReq := httptest.NewRequest(http.MethodGet, "/inventory", nil)
	fragReq.Header.Set("HX-Request", "true")
	fragRec := httptest.NewRecorder()
	mux.ServeHTTP(fragRec, fragReq)
	if got := fragRec.Header().Get("Vary"); got != "HX-Request" {
		t.Errorf("fragment branch: Vary header = %q, want %q", got, "HX-Request")
	}

	fullRec := httptest.NewRecorder()
	mux.ServeHTTP(fullRec, httptest.NewRequest(http.MethodGet, "/inventory", nil))
	if got := fullRec.Header().Get("Vary"); got != "HX-Request" {
		t.Errorf("full-page branch: Vary header = %q, want %q", got, "HX-Request")
	}
}
