package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#2765: the open sale screen's live-refresh signal, through the real
// mux. GET /ui/buttons/version is what web/public/sell-screen-watch.js polls;
// /ui/buttons' X-UT-Sell-Version header is what it compares against.

func getSellVersion(t *testing.T, mux *http.ServeMux) int64 {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/buttons/version", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/buttons/version = %d (%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, rec.Body.String())
	}
	if string(raw["error"]) != "null" {
		t.Fatalf("error = %s, want null (%s)", raw["error"], rec.Body.String())
	}
	var d struct {
		Version *int64 `json:"version"`
	}
	if err := json.Unmarshal(raw["data"], &d); err != nil || d.Version == nil {
		t.Fatalf("data = %s, want {\"version\": <int>}", raw["data"])
	}
	return *d.Version
}

func TestButtonsVersion_ShapeAndMovesOnCatalogDeactivate(t *testing.T) {
	mux, dp := newButtonsAndCatalogMux(t)
	v0 := getSellVersion(t, mux)
	want, ok, err := data.NewSellScreenRepo(dp.Db).SellGeneration(t.Context())
	if err != nil || !ok || v0 != want {
		t.Fatalf("/ui/buttons/version = %d, sell_screen_version = %d (ok %v, err %v)", v0, want, ok, err)
	}
	if again := getSellVersion(t, mux); again != v0 {
		t.Fatalf("version moved with no write: %d -> %d", v0, again)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/item/deactivate", strings.NewReader(url.Values{"id": {"itm1"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("deactivate = %d (%s)", rec.Code, rec.Body.String())
	}
	if v1 := getSellVersion(t, mux); v1 <= v0 {
		t.Fatalf("version after a catalog deactivate = %d, want > %d", v1, v0)
	}
}

func TestButtonsUIFragment_SellVersionHeaderMatchesVersionRoute(t *testing.T) {
	mux, _ := newButtonsAndCatalogMux(t)
	list := func() (string, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/buttons", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /ui/buttons = %d", rec.Code)
		}
		return rec.Header().Get("X-UT-Sell-Version"), rec.Body.String()
	}
	// First render (fresh), second (fresh again: the first render's one-time
	// settings seed moved the admin half of the cache key), third (cached).
	for i := 1; i <= 3; i++ {
		h, body := list()
		if !strings.Contains(body, "Apple") {
			t.Fatalf("render %d has no Apple tile", i)
		}
		if h == "" {
			t.Fatalf("render %d: no X-UT-Sell-Version header", i)
		}
		if want := strconv.FormatInt(getSellVersion(t, mux), 10); h != want {
			t.Fatalf("render %d: X-UT-Sell-Version = %q, /ui/buttons/version = %s", i, h, want)
		}
	}
}

// A completed sale through the real /api/pos/tender path must not move the
// counter — or every sale would make every open sale screen refetch.
func TestButtonsVersion_CompletedSaleDoesNotMoveIt(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	repo := data.NewSellScreenRepo(dp.Db)
	before, ok, err := repo.SellGeneration(t.Context())
	if err != nil || !ok {
		t.Fatalf("SellGeneration: ok %v err %v", ok, err)
	}
	if _, err := dp.Engine.Scan("PLAIN"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"cash","amount":240}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tender = %d (%s)", rec.Code, rec.Body.String())
	}
	var sales int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&sales); err != nil || sales == 0 {
		t.Fatalf("no sale recorded (count %d, err %v) — the test did not complete a sale", sales, err)
	}
	after, _, _ := repo.SellGeneration(t.Context())
	if after != before {
		t.Fatalf("a completed sale moved sell_screen_version %d -> %d; it must stay catalog-only", before, after)
	}
	// Control: the same counter does move on a catalog write here, so the
	// assertion above can fail.
	if _, err := dp.Db.Exec(`UPDATE items SET name = 'Plain Item 2' WHERE id = 'itm-plain2'`); err != nil {
		t.Fatal(err)
	}
	if moved, _, _ := repo.SellGeneration(t.Context()); moved == after {
		t.Fatal("control: an item rename did not move sell_screen_version")
	}
}
