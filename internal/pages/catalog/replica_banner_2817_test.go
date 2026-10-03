package catalog

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#2817: the additional till's catalogue banner no longer says local
// changes are overwritten — they are saved on the main till. While the main
// till can't be reached (the status chip's cached link state, via
// Deps.MainTillUnreachable) it says changes can be made again once it is.
func TestCatalogReplicaBanner_SaysChangesGoToTheMainTill(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	store := settings.NewStore(db)
	if err := store.Set(t.Context(), "sync.primary_url", "http://primary.till.local:8080"); err != nil {
		t.Fatal(err)
	}
	unreachable := false
	dp := &common.Deps{
		Db: db, Settings: store, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{},
		MainTillUnreachable: func(context.Context) bool { return unreachable },
	}
	mux := http.NewServeMux()
	Register(mux, dp)
	render := func() string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /catalog = %d", rec.Code)
		}
		return rec.Body.String()
	}
	// The page is HTML: the messages arrive escaped.
	writeThrough := html.EscapeString(httpx.T("en", "sync.banner_replica_write_through"))
	away := html.EscapeString(httpx.T("en", "sync.banner_replica_main_till_unreachable"))

	body := render()
	if !strings.Contains(body, writeThrough) || strings.Contains(body, away) {
		t.Fatalf("reachable main till: banner must say changes are saved on the main till (%q)", writeThrough)
	}
	if strings.Contains(body, "overwritten") {
		t.Fatal("the banner still says catalogue changes made here are overwritten")
	}
	unreachable = true
	body = render()
	if !strings.Contains(body, away) || strings.Contains(body, writeThrough) {
		t.Fatalf("unreachable main till: banner must say changes can be made when it is reachable (%q)", away)
	}
}

// ut-docs#2817: the item editor carries the updated_at of the row it loaded
// (data-updated-at on the card, copied into the form's base_updated_at), so
// the main till's conflict check compares against what the operator saw —
// not against this till's copy, which the link's pull refreshes within a
// second of a change on the main till. The card a save re-renders carries
// the new stamp, so the next save from the same dialog has a fresh base.
func TestCatalogItemEditor_CarriesTheLoadedUpdatedAt(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	t.Cleanup(func() { db.Close() })
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})
	if _, err := db.Exec(`UPDATE items SET updated_at = '2026-01-01 08:00:00' WHERE id = 'itm1'`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `data-updated-at="2026-01-01 08:00:00"`) {
		t.Fatal("the catalogue card does not carry the item's updated_at")
	}
	if !strings.Contains(body, `name="base_updated_at"`) {
		t.Fatal("the item form has no base_updated_at field")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/item/update", strings.NewReader("id=itm1&name=Renamed&price=330"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var stamp string
	if err := db.QueryRow(`SELECT updated_at FROM items WHERE id = 'itm1'`).Scan(&stamp); err != nil {
		t.Fatal(err)
	}
	if stamp == "2026-01-01 08:00:00" || !strings.Contains(rec.Body.String(), `data-updated-at="`+stamp+`"`) {
		t.Fatalf("the re-rendered card must carry the new stamp %q: %q", stamp, rec.Body.String())
	}
}
