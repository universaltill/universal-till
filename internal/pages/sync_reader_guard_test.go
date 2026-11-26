package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
)

// ut-docs#6538 R1a (design-batch2 §3): a card reader's bearer reaches exactly
// four /api/sync routes. Every other /api/sync/* route must refuse it with
// 401. The route list is read from the package's own registrations (every
// "METHOD /api/sync/..." literal in a non-test file), each is checked to be
// served by the mux the test builds, and a route added later is neither
// allowed nor in the documented non-bearer set -> the test fails until it is
// classified on purpose.

var readerAllowed = map[string]bool{
	"GET /api/sync/link":             true, // upgrade: the reader link
	"POST /api/sync/bearer/rotate":   true,
	"POST /api/sync/enroll/withdraw": true,
	"POST /api/sync/primary-proof":   true, // unauthenticated challenge: no bearer involved
}

// readerNoBearerRoutes are /api/sync routes that do not authenticate by
// bearer at all: the first-contact handshake (token or request secret), the
// manager-session routes and the joining-till helpers. A reader bearer gives
// no access to them either; they are asserted to refuse it as a credential
// (never 2xx with it) and listed explicitly so a new route cannot slip in.
var readerNoBearerRoutes = map[string]bool{
	"POST /api/sync/pair-request":               true,
	"GET /api/sync/pair-requests":               true, // manager session
	"POST /api/sync/pair-requests/{id}/approve": true, // manager session + PIN
	"POST /api/sync/pair-requests/{id}/deny":    true,
	"GET /api/sync/pair-requests/{id}":          true, // request secret
	"POST /api/sync/enroll":                     true, // one-time token
	"POST /api/sync/enroll-token":               true, // manager session
	"POST /api/sync/tills/{id}/revoke":          true,
	"POST /api/sync/promote":                    true,
	"POST /api/sync/join":                       true,
	"POST /api/sync/pair-start":                 true,
	"GET /api/sync/pair-status":                 true,
	"POST /api/sync/pairing-restart":            true,
	"GET /api/sync/discover-primaries":          true,
	"GET /api/sync/sales-mirror/status":         true, // manager session
	"POST /api/sync/sales-mirror/enable":        true,
	"POST /api/sync/sales-mirror/disable":       true,
}

var syncRouteLiteral = regexp.MustCompile(`"((?:GET|POST|PUT|PATCH|DELETE) /api/sync/[^"\s]*)"`)

func syncRoutesInSource(t *testing.T) []string {
	t.Helper()
	// TestMain chdirs to the repo root; the package's own files are here.
	_, self, _, _ := runtime.Caller(0)
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(self), "*.go"))
	seen := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range syncRouteLiteral.FindAllStringSubmatch(string(b), -1) {
			// A pattern inside a comment or a client-side URL is not a
			// registration: only HandleFunc/Handle lines count.
			line := lineContaining(string(b), m[0])
			if !strings.Contains(line, "HandleFunc(") && !strings.Contains(line, "Handle(") {
				continue
			}
			seen[m[1]] = true
		}
	}
	var out []string
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func lineContaining(s, sub string) string {
	i := strings.Index(s, sub)
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(s[:i], "\n") + 1
	end := strings.Index(s[i:], "\n")
	if end < 0 {
		return s[start:]
	}
	return s[start : i+end]
}

func samplePath(pattern string) (method, path string) {
	method, path, _ = strings.Cut(pattern, " ")
	path = regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(path, "sample-id")
	return
}

func TestReaderBearerRefusedOnEverySyncRoute(t *testing.T) {
	g := newReaderRig(t, true)
	// Every other registration on the main's real mux, so each literal in the
	// source resolves to a live handler.
	registerSyncSales(g.mux, g.d)
	registerSyncSalesMirror(g.mux, g.d)
	registerSalesMirrorStatus(g.mux, g.d)
	registerSyncPeerCloses(g.mux, g.d)
	registerSyncNoSales(g.mux, g.d)
	registerSyncClearedBaskets(g.mux, g.d)
	registerSyncOrders(g.mux, g.d)
	registerSyncTables(g.mux, g.d)
	registerSyncTablesClaim(g.mux, g.d)
	registerSyncVouchers(g.mux, g.d)
	registerSyncHeldSales(g.mux, g.d)
	registerSyncUsers(g.mux, g.d)
	registerSyncSettings(g.mux, g.d, nil)
	registerSyncCatalog(g.mux, g.d)
	registerSyncAdmin(g.mux, g.d)
	registerSyncCloudDevice(g.mux, g.d)
	registerSyncAssets(g.mux, g.d)
	registerTillRole(g.mux, g.d)
	registerDiscoveryAPI(g.mux, g.d)
	registerPairingJoinAPI(g.mux, g.d)
	registerTillsTabs(g.mux, g.d) // POST /api/sync/tills/revoke-stale (ut-docs#7057)

	_, bearer := g.enrolReader(t, "Guard phone")
	// Real session middleware in front, auth ON, exactly as served.
	t.Setenv("UT_AUTH", "")
	handler := auth.Middleware(g.mux, g.d.AuthSvc)

	routes := syncRoutesInSource(t)
	if len(routes) < 30 {
		t.Fatalf("found only %d /api/sync routes in source; the scan is broken", len(routes))
	}
	for _, pattern := range routes {
		method, path := samplePath(pattern)
		probe := httptest.NewRequest(method, path, nil)
		if _, served := g.mux.Handler(probe); served == "" || !strings.HasSuffix(served, strings.SplitN(pattern, " ", 2)[1]) {
			t.Errorf("%s is in the source but not served by the guard's mux (%q): register it in this test", pattern, served)
			continue
		}
		req := httptest.NewRequest(method, path, strings.NewReader(""))
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.RemoteAddr = "203.0.113." + "9:9"
		req = g.asTLS(req)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		switch {
		case readerAllowed[pattern]:
			if rec.Code == http.StatusUnauthorized && pattern != "POST /api/sync/primary-proof" {
				t.Errorf("allowed %s refused the reader (401)", pattern)
			}
		case readerNoBearerRoutes[pattern]:
			if rec.Code >= 200 && rec.Code < 300 {
				t.Errorf("%s answered %d to a reader bearer: it must never grant access", pattern, rec.Code)
			}
		default:
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s = %d for a reader bearer, want 401 (classify a deliberate new route in readerAllowed / readerNoBearerRoutes)", pattern, rec.Code)
			}
		}
		g.resetFailures()
	}
}

func (g *readerRigT) resetFailures() { resetBearerFailures(nil) }
