package pages

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ut-docs#5456: web/public/app.js bounds every user-initiated htmx request
// with a 30 s client timeout (ut-docs#5404) except the paths listed in
// UT_UNBOUNDED_PATHS (+ the /pairing-restart suffix). A long-running Go
// route missing from that list shows "may not have been done" while the
// server is still working. The Go tables below are the authoritative
// declaration; TestUnboundedRoutesMatchAppJS reads the JS list at test time
// (never duplicated here) and keeps the two paired.

// unboundedRoutes: handlers that legitimately run past the client timeout.
// pattern is the exact mux pattern string. Each needs a matching prefix in
// UT_UNBOUNDED_PATHS (web/public/app.js) and vice versa.
var unboundedRoutes = []struct{ pattern, why string }{
	{"POST /api/backup/now", "writes a full database backup copy"},
	{"POST /api/backup/save-copy/{name}", "copies a backup to the chosen location"},
	{"POST /api/backup/restore", "replaces the live database from a backup"},
	{"POST /api/backup/upload", "uploads and validates a backup file"},
	{"POST /api/backup/restart-now", "the till restarts itself"},
	{"POST /api/import", "catalogue import of an arbitrary size"},
	{"POST /api/import/sample-data", "seeds a whole sample catalogue"},
	{"POST /api/import/from-file", "catalogue import of an arbitrary size"},
	{"POST /api/import/from-system", "catalogue import from another POS, arbitrary size"},
	{"POST /api/catalog/export-save", "writes the whole catalogue to disk"},
	{"POST /api/update/apply", "downloads, verifies and applies a self-update, then restarts"},
	{"POST /api/setup/update-apply", "same as /api/update/apply, from the setup wizard"},
	{"/api/pos/tender", "payment gate waits on a card terminal; plugins.paymentGateTimeout is 30 s (wasm_runtime.go)"},
	{"POST /api/refund", "payment gate waits on a card terminal / fiscal device"},
	{"POST /api/self-order/checkout", "payment gate waits on a card terminal / fiscal device"},
	{"POST /api/sync/join", "backs up and replaces this till's data behind a 60 s LAN client"},
	{"POST /api/setup/join", "same as /api/sync/join, from the setup wizard"},
	{"POST /api/sync/pairing-restart", "the till restarts itself"},
	{"POST /api/setup/pairing-restart", "the till restarts itself"},
}

// timedOnPurpose: routes the keyword scan (check d) flags that were reviewed
// and are fine to time out client-side. Reason each.
var timedOnPurpose = map[string]string{
	"POST /api/data/import":                             "multipart upload: already never timed by the multipart rule",
	"POST /api/vouchers/import":                         "multipart upload: already never timed by the multipart rule",
	"POST /api/plugins/import-from-file":                "sent with fetch(), not htmx",
	"POST /api/data/reset-archives/{id}/restore":        "goes through utPostWithElevation; tracked on ut-docs#5445/#5454",
	"POST /api/settings/menu/restore":                   "plain form post, fast",
	"POST /api/sync/catalog/apply":                      "till-to-till sync, not a browser request",
	"POST /api/sync/settings/apply":                     "till-to-till sync, not a browser request",
	"POST /api/sync/users/apply":                        "till-to-till sync, not a browser request",
	"POST /ui/join-notice/dismiss":                      "dismisses a banner, fast",
	"POST /api/settings/catalog-import-barcode-default": "stores a setting, fast",
	"/v1/install/bundles/import":                        "local marketplace stub for the plugin store, called with fetch()",
	// Review of ut-docs#5456 (F1): export|upload|restart|install|update keywords.
	"POST /api/data/export":                      "sent with fetch(), not htmx",
	"POST /api/reports/archive/export":           "goes through utPostWithElevation; tracked on ut-docs#5445/#5454",
	"POST /api/plugins/install-from-marketplace": "no browser caller (my./smoke tooling); not sent by htmx",
	"POST /api/plugins/store/download":           "sent with fetch() (plugins_store_detail.html), not htmx",
	"POST /api/plugins/store/install":            "sent with fetch() (plugins_store_detail.html), not htmx",
	"POST /api/plugins/{id}/update":              "sent with fetch() via act() (plugins.html), not htmx",
	"POST /api/update/android-install":           "sent with fetch() (android-update.js), not htmx",
	"POST /api/update/check":                     "server bounds the release check at 10 s (updates.CheckNow)",
	"POST /api/setup/update-check":               "hx-trigger=load, not a user event: never timed",
	"POST /api/tills/{id}/update-now":            "only queues the request on the fleet link, fast",
	"POST /api/settings/update-schedule":         "stores a setting, fast",
	"POST /api/catalog/tax-codes/update":         "saves one row, fast",
	"POST /api/catalog/option-set-value/update":  "saves one row, fast",
	"/api/catalog/item/update":                   "saves one item via htmx.ajax (not a user event), fast",
	"/v1/install/bundles/export":                 "local marketplace stub for the plugin store, called with fetch()",
	"/v1/install/intents":                        "local marketplace stub for the plugin store, called with fetch()",
	"/v1/install/status":                         "local marketplace stub for the plugin store, called with fetch()",
}

// alsoExempted: non-GET routes a UT_UNBOUNDED_PATHS prefix exempts as a side
// effect although they are not long-running (check c2). Keep it tiny; a new
// entry means the JS prefix is broader than it should be.
var alsoExempted = map[string]string{
	"POST /api/refund/preview": "read-only total recompute on qty input; harmless untimed (BA, ut-docs#5456)",
}

var (
	unboundedListRe  = regexp.MustCompile(`(?s)var UT_UNBOUNDED_PATHS\s*=\s*\[(.*?)\];`)
	singleQuotedRe   = regexp.MustCompile(`'([^']*)'`)
	registeredRe     = regexp.MustCompile(`Handle(?:Func)?\("([^"]+)"`)
	longRunningKeyRe = regexp.MustCompile(`(?i)/(backup|restore|import|export|upload|join|apply|update|install|restart|tender|refund|checkout)`)
)

const pairingRestartMarker = `/\/pairing-restart$/.test(path)`

func routePath(pattern string) string {
	if i := strings.Index(pattern, " "); i >= 0 {
		return pattern[i+1:]
	}
	return pattern
}

func routeMethod(pattern string) string {
	if i := strings.Index(pattern, " "); i >= 0 {
		return pattern[:i]
	}
	return ""
}

// mutating: anything but GET/HEAD; a method-less pattern serves every method.
func mutating(pattern string) bool {
	m := routeMethod(pattern)
	return m != "GET" && m != "HEAD"
}

func TestUnboundedRoutesMatchAppJS(t *testing.T) {
	root := "." // TestMain chdirs to the repo root
	const jsFile = "web/public/app.js"

	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(jsFile)))
	if err != nil {
		t.Fatalf("read %s: %v", jsFile, err)
	}
	js := string(src)
	m := unboundedListRe.FindStringSubmatch(js)
	if m == nil {
		t.Fatalf("`var UT_UNBOUNDED_PATHS = [...];` not found in %s: keep this test pointed at the list (internal/pages/unbounded_routes_test.go)", jsFile)
	}
	var prefixes []string
	for _, q := range singleQuotedRe.FindAllStringSubmatch(m[1], -1) {
		prefixes = append(prefixes, q[1])
	}
	if len(prefixes) == 0 {
		t.Fatalf("UT_UNBOUNDED_PATHS in %s has no single-quoted entries: keep this test pointed at the list", jsFile)
	}
	if !strings.Contains(js, pairingRestartMarker) {
		t.Fatalf("%s no longer contains %s in utIsUnboundedPath: keep this test pointed at the pairing-restart rule (internal/pages/unbounded_routes_test.go)", jsFile, pairingRestartMarker)
	}

	covered := func(path string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(path, p) {
				return true
			}
		}
		return strings.HasSuffix(path, "/pairing-restart")
	}

	// Registered mux patterns, from non-test Go files under internal/.
	registered := map[string]bool{}
	internal := filepath.Join(root, "internal")
	err = filepath.WalkDir(internal, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".claude" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		for _, r := range registeredRe.FindAllStringSubmatch(string(b), -1) {
			registered[r[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan internal/: %v", err)
	}

	// e. the scanner itself works.
	for _, must := range []string{"/api/pos/tender", "POST /api/backup/now"} {
		if !registered[must] {
			t.Fatalf("route scanner did not find %q under internal/: the Handle/HandleFunc regexp in unbounded_routes_test.go is broken", must)
		}
	}

	declared := map[string]bool{}
	for _, r := range unboundedRoutes {
		declared[r.pattern] = true
		// a. still registered.
		if !registered[r.pattern] {
			t.Errorf("stale Go entry %q in unboundedRoutes: no such route is registered under internal/ (removed or renamed?); update or delete the entry in internal/pages/unbounded_routes_test.go, and the matching prefix in UT_UNBOUNDED_PATHS (%s)", r.pattern, jsFile)
		}
		// b. client side knows it.
		if !covered(routePath(r.pattern)) {
			t.Errorf("%s is declared long-running (%s) but %s does not exempt it: add a prefix for %s to UT_UNBOUNDED_PATHS in %s, or mark the trigger data-ut-no-timeout and drop it from unboundedRoutes", r.pattern, r.why, jsFile, routePath(r.pattern), jsFile)
		}
	}

	// c. every JS prefix covers a declared route.
	for _, p := range prefixes {
		hit := false
		for _, r := range unboundedRoutes {
			if strings.HasPrefix(routePath(r.pattern), p) {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("UT_UNBOUNDED_PATHS entry %q in %s matches no declared long-running route: declare the route in unboundedRoutes (with why) in internal/pages/unbounded_routes_test.go, or remove the entry", p, jsFile)
		}
	}

	// c2. no JS prefix is broader than the declared routes (an entry like
	// '/api/' would un-time every action under it).
	var regSorted []string
	for p := range registered {
		regSorted = append(regSorted, p)
	}
	sort.Strings(regSorted)
	for _, p := range prefixes {
		var extra []string
		for _, pat := range regSorted {
			if !mutating(pat) || !strings.HasPrefix(routePath(pat), p) || declared[pat] {
				continue
			}
			if _, ok := alsoExempted[pat]; ok {
				continue
			}
			extra = append(extra, pat)
		}
		if len(extra) > 0 {
			shown := extra
			if len(shown) > 5 {
				shown = shown[:5]
			}
			t.Errorf("UT_UNBOUNDED_PATHS entry %q in %s also exempts %d route(s) not declared long-running (%s): narrow the prefix, declare each route in unboundedRoutes, or (if harmless) list it in alsoExempted with a reason (internal/pages/unbounded_routes_test.go)", p, jsFile, len(extra), strings.Join(shown, ", "))
		}
	}

	// d. keyword net for new slow-looking routes.
	for _, pat := range regSorted {
		if !mutating(pat) {
			continue
		}
		path := routePath(pat)
		if !longRunningKeyRe.MatchString(path) || declared[pat] || covered(path) {
			continue
		}
		if _, ok := timedOnPurpose[pat]; ok {
			continue
		}
		t.Errorf("new long-running-looking route %q: if it can outlast 30 s, add it to unboundedRoutes and its prefix to UT_UNBOUNDED_PATHS in %s; if timing it is fine, add it to timedOnPurpose with a reason (both in internal/pages/unbounded_routes_test.go)", pat, jsFile)
	}
	for pat := range timedOnPurpose {
		if !registered[pat] {
			t.Errorf("stale timedOnPurpose entry %q: no such route is registered; delete it from internal/pages/unbounded_routes_test.go", pat)
		}
	}
	for pat := range alsoExempted {
		if !registered[pat] {
			t.Errorf("stale alsoExempted entry %q: no such route is registered; delete it from internal/pages/unbounded_routes_test.go", pat)
		}
	}
}
