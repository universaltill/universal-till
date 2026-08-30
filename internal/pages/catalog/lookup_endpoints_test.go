package catalog

import (
	"bytes"
	"database/sql"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	productlookup "github.com/universaltill/universal-till/internal/lookup"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
)

// addAuditTable gives the shared minimal catalog schema the audit_log table
// the lookup endpoint writes to (its InsertAudit error is deliberately
// swallowed by the handler, so tests that want to assert the audit trail —
// real behavior on a real till, every lookup is audited — must create it).
func addAuditTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE audit_log (id TEXT PRIMARY KEY, actor_id TEXT, entity_type TEXT NOT NULL, entity_id TEXT NOT NULL, action TEXT NOT NULL, data_json TEXT, created_at TEXT NOT NULL, blocked_actor_id TEXT)`); err != nil {
		t.Fatal(err)
	}
}

// stubLookupClient swaps the package's lookup-client seam for one whose
// product-database sources are hermetic httptest servers and whose image
// fetches are answered by transport (a RoundTripper stub), so the real
// FetchImage host allowlist still runs but no request leaves the process.
// Restores the real constructor on cleanup.
func stubLookupClient(t *testing.T, handler http.HandlerFunc, images http.RoundTripper) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	orig := newLookupClient
	t.Cleanup(func() { newLookupClient = orig })
	hc := &http.Client{}
	if images != nil {
		hc.Transport = images
	} else {
		// No test should reach the wire when it didn't stub images.
		hc.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			// httptest sources are contacted via their own URLs below; only
			// image fetches (allowlisted https hosts) land here.
			if strings.HasPrefix(r.URL.String(), srv.URL) {
				return http.DefaultTransport.RoundTrip(r)
			}
			t.Errorf("unexpected outbound request in test: %s", r.URL)
			return nil, http.ErrUseLastResponse
		})
	}
	newLookupClient = func() *productlookup.Client {
		return productlookup.NewClient(hc, []productlookup.Source{{Name: "test-db", BaseURL: srv.URL}})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// pngResponse builds a canned 200 response carrying a real decodable PNG.
func pngResponse(t *testing.T) *http.Response {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(1, 0, color.RGBA{G: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(buf.Bytes())),
		Header:     http.Header{},
	}
}

// imageTransport answers image fetches for allowlisted hosts from memory and
// records what was requested; anything routed at the given source server
// still goes through for barcode lookups.
func imageTransport(t *testing.T, srvURL string, body func() *http.Response) roundTripFunc {
	t.Helper()
	return func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.String(), srvURL) {
			return http.DefaultTransport.RoundTrip(r)
		}
		if r.URL.Scheme == "https" && strings.HasSuffix(r.URL.Hostname(), ".openfoodfacts.org") {
			return body(), nil
		}
		t.Errorf("unexpected outbound request in test: %s", r.URL)
		return nil, http.ErrUseLastResponse
	}
}

func TestCatalogLookup_HitReturnsProductAndAudits(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	addAuditTable(t, db)
	stubLookupClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":1,"product":{"product_name":"Coca-Cola","brands":"Coca-Cola","quantity":"330 ml"}}`))
	}, nil)

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/lookup?barcode=5449000000996", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Coca-Cola") || !strings.Contains(body, `"error":null`) {
		t.Fatalf("want product JSON envelope, got %s", body)
	}
	var action string
	var dataJSON string
	if err := db.QueryRow(`SELECT action, data_json FROM audit_log WHERE entity_id = '5449000000996'`).Scan(&action, &dataJSON); err != nil {
		t.Fatalf("lookup not audited: %v", err)
	}
	if action != "barcode_lookup" || !strings.Contains(dataJSON, `"found":true`) {
		t.Fatalf("audit row wrong: action=%q data=%s", action, dataJSON)
	}
}

func TestCatalogLookup_NotFoundIs404AndAuditedAsMiss(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	addAuditTable(t, db)
	stubLookupClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}, nil)

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/lookup?barcode=40084107", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not found in product databases") {
		t.Fatalf("want not-found envelope, got %s", rec.Body.String())
	}
	var dataJSON string
	if err := db.QueryRow(`SELECT data_json FROM audit_log WHERE entity_id = '40084107'`).Scan(&dataJSON); err != nil {
		t.Fatalf("miss not audited: %v", err)
	}
	if !strings.Contains(dataJSON, `"found":false`) {
		t.Fatalf("audit row should record the miss: %s", dataJSON)
	}
}

// Transport failure (shop offline, databases down) is a 502, not a 404 —
// the form must be able to tell "unknown barcode" from "try again later".
func TestCatalogLookup_UnreachableSourcesAre502(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	addAuditTable(t, db)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // connection refused from here on
	orig := newLookupClient
	t.Cleanup(func() { newLookupClient = orig })
	newLookupClient = func() *productlookup.Client {
		return productlookup.NewClient(nil, []productlookup.Source{{Name: "dead", BaseURL: srv.URL}})
	}

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/lookup?barcode=40084107", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unreachable") {
		t.Fatalf("want unreachable envelope, got %s", rec.Body.String())
	}
}

// The auto-fill create flow: a create carrying the looked-up imageUrl saves
// the product photo as the item's thumb.png via the real allowlist code —
// hermetically, through a stubbed transport.
func TestCatalogCreate_SavesLookupImage(t *testing.T) {
	chdirToRepoRoot(t)
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })
	db := setupCatalogPageDB(t)
	defer db.Close()

	stub := func(w http.ResponseWriter, r *http.Request) {}
	stubbed := false
	stubLookupClient(t, stub, imageTransport(t, "http://never-used", func() *http.Response {
		stubbed = true
		return pngResponse(t)
	}))

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}})

	form := "name=Cola&price=150&isActive=1&imageUrl=" +
		"https%3A%2F%2Fimages.openfoodfacts.org%2Fimages%2Ffront.png"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/item", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !stubbed {
		t.Fatal("image transport never consulted — saveLookupImage did not run")
	}
	itemID := extractItemID(t, db)
	thumb := paths.Data("public", "assets", "items", itemID, "thumb.png")
	raw, err := os.ReadFile(thumb)
	if err != nil {
		t.Fatalf("lookup image not stored at %s: %v", thumb, err)
	}
	if _, _, err := image.Decode(bytes.NewReader(raw)); err != nil {
		t.Fatalf("stored thumb is not a decodable image: %v", err)
	}
	// Review finding F2 (ut-docs#1189): the disk file alone isn't enough —
	// item_images (what the POS grid/basket/self-order/suggestions
	// actually resolve a thumbnail from) must be updated too.
	var imgPath string
	if err := db.QueryRow(`SELECT path FROM item_images WHERE item_id = ? AND role = 'thumbnail'`, itemID).Scan(&imgPath); err != nil {
		t.Fatalf("expected an item_images thumbnail row for the lookup image: %v", err)
	}
	if imgPath != "/public/assets/items/"+itemID+"/thumb.png" {
		t.Fatalf("item_images path = %q", imgPath)
	}
}

// A disallowed image host (SSRF attempt or stale URL) must not fail the
// create — the item lands, the photo is skipped. Same for undecodable bytes.
func TestCatalogCreate_LookupImageFailuresAreNonFatal(t *testing.T) {
	cases := []struct {
		name     string
		imageURL string
		resp     func(t *testing.T) *http.Response
	}{
		{"disallowed host is refused by the allowlist", "https://evil.example.com/x.png", nil},
		{"non-image body is skipped", "https://images.openfoodfacts.org/x.png", func(t *testing.T) *http.Response {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("not a png")), Header: http.Header{}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chdirToRepoRoot(t)
			paths.Init(t.TempDir())
			t.Cleanup(func() { paths.Init("") })
			db := setupCatalogPageDB(t)
			defer db.Close()
			stubLookupClient(t, func(w http.ResponseWriter, r *http.Request) {}, imageTransport(t, "http://never-used", func() *http.Response {
				if tc.resp == nil {
					t.Error("allowlist should have refused before any request was made")
					return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}
				}
				return tc.resp(t)
			}))

			mux := http.NewServeMux()
			Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}})

			form := "name=Widget&price=99&isActive=1&imageUrl=" + strings.ReplaceAll(tc.imageURL, ":", "%3A")
			req := httptest.NewRequest(http.MethodPost, "/api/catalog/item", strings.NewReader(form))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("item create must survive a failed image fetch: got %d: %s", rec.Code, rec.Body.String())
			}
			itemID := extractItemID(t, db)
			if _, err := os.Stat(paths.Data("public", "assets", "items", itemID, "thumb.png")); err == nil {
				t.Fatal("no thumb should have been written for a failed fetch")
			}
		})
	}
}
