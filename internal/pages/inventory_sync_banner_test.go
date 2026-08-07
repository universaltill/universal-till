package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// TestInventoryReplicaBannerNeverLinksAcrossDevices guards ut-docs#390: a
// replica's "Edit on the primary" banner used to be a plain <a
// target="_blank"> pointing the KIOSK'S OWN browser at another till's
// origin. On the kiosk shell (fullscreen, no chrome, no tabs — the
// platform crossdevicelinkactionable is false for) that's a permanent
// dead end: field-reported as the operator getting stranded on till 1
// with no way back to till 2. This asserts the real, current-runtime
// behaviour of the rendered page, not just the template source: on this
// (non-Windows/macOS) test runtime, a replica's inventory page must
// contain NO clickable link to the primary's origin, only the inert,
// informational text.
func TestInventoryReplicaBannerNeverLinksAcrossDevices(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	store := settings.NewStore(db)
	if err := store.Set(t.Context(), "sync.primary_url", "http://primary.till.local:8080"); err != nil {
		t.Fatalf("seed sync.primary_url: %v", err)
	}

	d := &common.Deps{Db: db, Settings: store, Menu: []common.MenuItem{{Href: "/", Label: "Home"}}}
	mux := http.NewServeMux()
	registerInventoryPage(mux, d)

	req := httptest.NewRequest(http.MethodGet, "/inventory", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /inventory = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if !strings.Contains(body, "sync-banner") {
		t.Fatal("expected the replica sync-banner to render at all — did the {{ if .SyncPrimary }} block regress?")
	}
	// crossdevicelinkactionable is false on this (Linux) test runtime — the
	// same platform this bug was field-reported on. The banner must not
	// contain an <a href="...primary.till.local..."> under that condition,
	// and must fall back to inert text instead.
	if strings.Contains(body, `href="http://primary.till.local:8080/inventory"`) {
		t.Fatal("ut-docs#390: replica inventory banner still links the kiosk's own browser to the primary till's origin — this strands the operator on a kiosk with no way back")
	}
	if !strings.Contains(body, "Edit this on the primary till") {
		t.Fatal("ut-docs#390: replica banner should fall back to inert text (sync.banner_open_primary_unavailable) when crossdevicelinkactionable is false")
	}
}

// TestInventoryNonReplicaHasNoSyncBanner is the negative control: a
// standalone/primary till (no sync.primary_url set) must not render the
// banner at all.
func TestInventoryNonReplicaHasNoSyncBanner(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	d := &common.Deps{Db: db, Settings: settings.NewStore(db), Menu: []common.MenuItem{{Href: "/", Label: "Home"}}}
	mux := http.NewServeMux()
	registerInventoryPage(mux, d)

	req := httptest.NewRequest(http.MethodGet, "/inventory", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /inventory = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sync-banner") {
		t.Fatal("a standalone/primary till must not render the replica sync banner")
	}
}
