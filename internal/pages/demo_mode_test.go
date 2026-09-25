package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
)

// --- Route classification (ADR-0113 §1.4, ut-docs#2687) ---------------------

// nonLiteralRoutePatterns resolves the registrations whose pattern is not a
// string literal, keyed by the pattern expression's source text. A new
// non-literal registration fails the scan until it is added here — the scan
// must never silently skip a route.
var nonLiteralRoutePatterns = map[string]func() []string{
	`"GET " + scope.listPath`: func() []string {
		var out []string
		for _, s := range syncAssetScopes {
			out = append(out, "GET "+s.listPath)
		}
		return out
	},
	`"GET " + scope.filePath`: func() []string {
		var out []string
		for _, s := range syncAssetScopes {
			out = append(out, "GET "+s.filePath)
		}
		return out
	},
	`"POST " + discovery.ProofPath`: func() []string { return []string{"POST " + discovery.ProofPath} },
}

// registeredRoutePatterns walks every non-test .go file under internal/pages
// (recursively — catalog.Register lives in a subpackage) and collects the
// first argument of every <recv>.HandleFunc(...) / <recv>.Handle(...) call,
// the same go/ast scan scripts/ci/checkhelptopics uses, but keeping every
// method (the demo allow-list is per method) and resolving non-literal
// patterns through nonLiteralRoutePatterns instead of skipping them.
func registeredRoutePatterns(t *testing.T) []string {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	dir := filepath.Dir(self) // internal/pages, independent of any test's chdir
	seen := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
				return true
			}
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				p, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Errorf("%s: unquote %s: %v", fset.Position(lit.Pos()), lit.Value, err)
					return true
				}
				seen[p] = true
				return true
			}
			var src bytes.Buffer
			_ = printer.Fprint(&src, fset, call.Args[0])
			resolve, ok := nonLiteralRoutePatterns[src.String()]
			if !ok {
				t.Errorf("%s: route pattern %s is not a string literal and has no entry in nonLiteralRoutePatterns — add one so the demo classification sees it",
					fset.Position(call.Pos()), src.String())
				return true
			}
			for _, p := range resolve() {
				seen[p] = true
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan internal/pages: %v", err)
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Every registered route is classified exactly once, and every list entry
// is a registered route: a new route is denied in the demo (fail closed at
// runtime) and fails this test until someone decides.
func TestDemoRouteClassification(t *testing.T) {
	routes := registeredRoutePatterns(t)
	if len(routes) < 300 {
		t.Fatalf("scan found only %d routes — it has gone blind", len(routes))
	}
	registered := map[string]bool{}
	for _, p := range routes {
		registered[p] = true
		allowed, denied := demoAllowedRoutes[p], demoDeniedRoutes[p]
		switch {
		case !allowed && !denied:
			t.Errorf("route %q is on neither demoAllowedRoutes nor demoDeniedRoutes (demo_mode.go) — classify it (when in doubt, deny)", p)
		case allowed && denied:
			t.Errorf("route %q is on both demoAllowedRoutes and demoDeniedRoutes", p)
		}
	}
	for _, list := range []struct {
		name string
		m    map[string]bool
	}{{"demoAllowedRoutes", demoAllowedRoutes}, {"demoDeniedRoutes", demoDeniedRoutes}} {
		for p := range list.m {
			if !registered[p] {
				t.Errorf("%s entry %q matches no registered route (stale — the route was renamed or removed)", list.name, p)
			}
		}
	}
	t.Logf("demo routes: %d registered, %d allowed, %d denied", len(routes), len(demoAllowedRoutes), len(demoDeniedRoutes))
}

// ADR-0113 §1.5's always-denied surfaces, spot-checked by exact pattern so a
// careless move to the allow-list fails here with the reason, not just in
// review.
func TestDemoAlwaysDeniedSurfaces(t *testing.T) {
	for _, p := range []string{
		"POST /api/plugins/import-from-file",
		"POST /api/plugins/install-from-marketplace",
		"POST /api/plugins/{id}/update",
		"POST /api/plugins/{id}/rollback",
		"POST /api/plugins/{id}/enable",
		"/api/plugins/marketplace",
		"/plugins/store",
		"/v1/install/intents",
		"POST /api/import",
		"POST /api/data/import",
		"POST /api/backup/restore",
		"POST /api/backup/now",
		"GET /api/backup/download/{name}",
		"POST /api/backup/save-copy/{name}",
		"POST /api/backup/restart-now",
		"POST /api/settings/exit-to-os",
		"POST /api/settings/display-mode",
		"POST /api/sync/enroll",
		"POST /api/enrol/now",
		"GET /api/sync/snapshot",
		"POST /api/setup/join",
		"GET /self-order",
		"POST /api/self-order/checkout",
		"GET /o/{token}",
		"POST /api/pos/identify",
		"POST /api/reports/ask",
		"POST /api/update/check",
		"POST /api/update/apply",
		"POST /api/settings/printer",
		"POST /api/print/test",
		"POST /api/fiscal-device/confirm",
		"POST /api/bluetooth-devices/pair",
		"POST /api/settings/upsert",
		"/ext/",
		"POST /api/issue-reports",
		"POST /api/settings/diagnostics/activate",
		"GET /api/catalog/lookup",
		"POST /api/catalog/item/image",
		"POST /api/receipt-designer/logo",
		// Shop type re-syncs the built-in layout plugins
		// (builtinlayouts.Sync → plugins.PersistManifest) — a plugin install.
		"POST /api/settings/shop-type",
	} {
		if !demoDeniedRoutes[p] || demoAllowedRoutes[p] {
			t.Errorf("%q must be always-denied in the demo (ADR-0113 §1.5)", p)
		}
	}
}

// --- Middleware behaviour --------------------------------------------------

const testDemoToken = "0123456789abcdef0123456789abcdef"

// demoTestHandler builds the real middleware over a mux carrying a few real
// patterns (so the lists' own entries apply): an allowed JSON route, an
// allowed form route that reads its form back, and a denied route. ran
// reports whether the inner handler executed.
func demoTestHandler(t *testing.T) (http.Handler, *bool, *string) {
	t.Helper()
	httpx.InitI18n(nil, "en") // no translator: T answers the key itself
	ran := new(bool)
	gotName := new(string)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/pos/scan", func(w http.ResponseWriter, r *http.Request) {
		*ran = true
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /api/categories", func(w http.ResponseWriter, r *http.Request) {
		*ran = true
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			if err := r.ParseForm(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		*gotName = r.FormValue("name")
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /api/backup/restore", func(w http.ResponseWriter, r *http.Request) {
		*ran = true
	})
	mux.HandleFunc("GET /menu-not-classified", func(w http.ResponseWriter, r *http.Request) {
		*ran = true
	})
	// Production registers the catch-all index "/" (index_page.go), so every
	// otherwise-unmatched path resolves to it — the fixture must too.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		*ran = true
	})
	return newDemoMiddleware(mux, mux, testDemoToken), ran, gotName
}

func withToken(r *http.Request) *http.Request {
	r.Header.Set(demoTokenHeader, testDemoToken)
	return r
}

func assertDemoRefused(t *testing.T, w *httptest.ResponseRecorder, ran bool, wantStatus int) {
	t.Helper()
	if ran {
		t.Fatal("inner handler ran; the demo middleware must refuse before it")
	}
	if w.Code != wantStatus {
		t.Fatalf("status = %d, want %d (body %q)", w.Code, wantStatus, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "demo.not_available") {
		t.Fatalf("body %q does not carry the localised demo message", w.Body.String())
	}
}

func TestDemoMiddleware_Token(t *testing.T) {
	for _, tc := range []struct {
		name  string
		token *string
	}{
		{"missing", nil},
		{"empty", ptr("")},
		{"wrong same length", ptr(strings.Repeat("x", len(testDemoToken)))},
		{"prefix", ptr(testDemoToken[:10])},
		{"longer", ptr(testDemoToken + "x")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, ran, _ := demoTestHandler(t)
			r := httptest.NewRequest(http.MethodPost, "/api/pos/scan", nil)
			if tc.token != nil {
				r.Header.Set(demoTokenHeader, *tc.token)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			assertDemoRefused(t, w, *ran, http.StatusForbidden)
		})
	}
}

// A till started with an empty token never matches anything — not even an
// empty header. (The start gate refuses an empty token already; this is the
// middleware not relying on that.)
func TestDemoMiddleware_EmptyConfiguredTokenNeverMatches(t *testing.T) {
	httpx.InitI18n(nil, "en")
	ran := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/pos/scan", func(w http.ResponseWriter, r *http.Request) { ran = true })
	h := newDemoMiddleware(mux, mux, "")
	for _, hdr := range []*string{nil, ptr("")} {
		r := httptest.NewRequest(http.MethodPost, "/api/pos/scan", nil)
		if hdr != nil {
			r.Header.Set(demoTokenHeader, *hdr)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		assertDemoRefused(t, w, ran, http.StatusForbidden)
	}
}

func ptr(s string) *string { return &s }

func TestDemoMiddleware_AllowedRouteRuns(t *testing.T) {
	h, ran, _ := demoTestHandler(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, withToken(httptest.NewRequest(http.MethodPost, "/api/pos/scan", strings.NewReader("code=1"))))
	if !*ran || w.Code != http.StatusOK {
		t.Fatalf("allowed route with token: ran=%v status=%d, want the handler to run", *ran, w.Code)
	}
}

func TestDemoMiddleware_DeniedRoute(t *testing.T) {
	for _, tc := range []struct {
		name   string
		hx     bool
		wantCT string
	}{
		{name: "api json", wantCT: "application/json"},
		{name: "htmx fragment", hx: true, wantCT: "text/html"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, ran, _ := demoTestHandler(t)
			r := withToken(httptest.NewRequest(http.MethodPost, "/api/backup/restore", nil))
			if tc.hx {
				r.Header.Set("HX-Request", "true")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			assertDemoRefused(t, w, *ran, http.StatusForbidden)
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, tc.wantCT) {
				t.Fatalf("Content-Type = %q, want %s", ct, tc.wantCT)
			}
			if !tc.hx {
				var body struct {
					Data  any `json:"data"`
					Error struct {
						Code    string `json:"code"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatalf("JSON body: %v (%q)", err, w.Body.String())
				}
				if body.Data != nil || body.Error.Code != "demo_not_available" || body.Error.Message != "demo.not_available" {
					t.Fatalf("envelope = %+v, want data null + error{demo_not_available, message}", body)
				}
			}
		})
	}
}

// A route registered but on neither list (a new route nobody classified)
// and a path that matches no route at all are both refused: fail closed.
func TestDemoMiddleware_UnclassifiedAndUnmatchedAreRefused(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/menu-not-classified"},
		{http.MethodGet, "/no/such/route"},
		{http.MethodGet, "/api/backup/restore"}, // wrong method: falls to "/" — refused, not leaked
	} {
		h, ran, _ := demoTestHandler(t)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withToken(httptest.NewRequest(tc.method, tc.path, nil)))
		assertDemoRefused(t, w, *ran, http.StatusForbidden)
	}
}

// The catch-all "/" is allowed only for the root path itself. With "/"
// registered (as in production), an unknown path, a plugin-owned page the
// index handler would dispatch (findPageEntry, e.g. /faq) and a wrong-method
// hit on a denied pattern all resolve to "/" — and must all be refused.
func TestDemoMiddleware_CatchAllIndexServesOnlyRoot(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/no/such/route"},
		{http.MethodGet, "/faq"},
		{http.MethodPost, "/faq"},
		{http.MethodGet, "/api/backup/restore"},
		{http.MethodPut, "/api/backup/restore"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			h, ran, _ := demoTestHandler(t)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, withToken(httptest.NewRequest(tc.method, tc.path, nil)))
			assertDemoRefused(t, w, *ran, http.StatusForbidden)
		})
	}
	h, ran, _ := demoTestHandler(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, withToken(httptest.NewRequest(http.MethodGet, "/", nil)))
	if !*ran || w.Code != http.StatusOK {
		t.Fatalf("GET / with token: ran=%v status=%d, want the sale screen served", *ran, w.Code)
	}
}

func multipartBody(t *testing.T, withFile bool) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("name", "Drinks"); err != nil {
		t.Fatal(err)
	}
	if withFile {
		fw, err := mw.CreateFormFile("image", "photo.png")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write([]byte("\x89PNG not really"))
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

func TestDemoMiddleware_MultipartWithFileIsRefused(t *testing.T) {
	h, ran, _ := demoTestHandler(t)
	body, ct := multipartBody(t, true)
	r := withToken(httptest.NewRequest(http.MethodPost, "/api/categories", body))
	r.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assertDemoRefused(t, w, *ran, http.StatusForbidden)
}

// A multipart form with no file part on an allowed route passes, and the
// handler still reads the whole form (the middleware restored the body it
// inspected).
func TestDemoMiddleware_MultipartWithoutFilePassesAndFormIsIntact(t *testing.T) {
	h, ran, name := demoTestHandler(t)
	body, ct := multipartBody(t, false)
	r := withToken(httptest.NewRequest(http.MethodPost, "/api/categories", body))
	r.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !*ran || w.Code != http.StatusOK || *name != "Drinks" {
		t.Fatalf("ran=%v status=%d name=%q body=%q; want the handler to run and read name=Drinks", *ran, w.Code, *name, w.Body.String())
	}
}

// A body over 64 KiB is rejected before the handler, whether its length is
// declared or streamed (chunked, ContentLength -1).
func TestDemoMiddleware_BodyCap(t *testing.T) {
	for _, declared := range []bool{true, false} {
		h, ran, _ := demoTestHandler(t)
		big := strings.Repeat("a", demoMaxBodyBytes+1)
		var rd io.Reader = strings.NewReader(big)
		if !declared {
			rd = io.MultiReader(strings.NewReader(big)) // hides the length from NewRequest
		}
		r := withToken(httptest.NewRequest(http.MethodPost, "/api/pos/scan", rd))
		if !declared {
			r.ContentLength = -1
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		assertDemoRefused(t, w, *ran, http.StatusRequestEntityTooLarge)
	}
	// Exactly at the cap still passes.
	h, ran, _ := demoTestHandler(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, withToken(httptest.NewRequest(http.MethodPost, "/api/pos/scan", strings.NewReader(strings.Repeat("a", demoMaxBodyBytes)))))
	if !*ran {
		t.Fatalf("a body of exactly %d bytes was refused (status %d)", demoMaxBodyBytes, w.Code)
	}
}

// --- Installation: only when cfg.Demo -------------------------------------

// pages.Init installs the demo middleware only in demo mode: with Demo on, a
// request with no token is refused by the real handler chain; with Demo off
// the same request reaches the till (healthz answers).
func TestInit_DemoMiddlewareInstalledOnlyInDemoMode(t *testing.T) {
	for _, demo := range []bool{false, true} {
		t.Run(fmt.Sprintf("demo=%v", demo), func(t *testing.T) {
			chdirRoot(t)
			paths.Init(t.TempDir())
			d := &db.DB{DB: openPagesTestDB(t)}
			defer d.Close()
			cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}, Demo: demo, DemoToken: testDemoToken}
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			pm, err := plugins.Init(ctx, cfg, d.DB)
			if err != nil {
				t.Fatalf("plugins.Init: %v", err)
			}
			var wg sync.WaitGroup
			h, _ := Init(ctx, ctx, cfg, pm, d.DB, nil, &wg)

			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			if demo && w.Code != http.StatusForbidden {
				t.Fatalf("demo on, no token: /healthz status = %d, want 403", w.Code)
			}
			if !demo && w.Code == http.StatusForbidden {
				t.Fatalf("demo off: /healthz answered 403 — the demo middleware must not be installed")
			}
			if demo {
				w = httptest.NewRecorder()
				h.ServeHTTP(w, withToken(httptest.NewRequest(http.MethodGet, "/healthz", nil)))
				if w.Code == http.StatusForbidden {
					t.Fatalf("demo on, with token: /healthz status = 403, want it served")
				}
			}
		})
	}
}
