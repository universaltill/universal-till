package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/universaltill/universal-till/internal/manual"
	uiassets "github.com/universaltill/universal-till/web"
)

// The scanner reads real Go source, so the unit fixture is a real (temp) Go
// file — GET and method-less registrations are pages, other methods are not,
// and mux.Handle counts the same as mux.HandleFunc.
func TestPageRoutesFindsRegistrations(t *testing.T) {
	dir := t.TempDir()
	src := `package fake

func register(mux *http.ServeMux) {
	mux.HandleFunc("/plain", h)
	mux.HandleFunc("GET /getpage", h)
	mux.HandleFunc("GET /detail/{id}", h)
	mux.HandleFunc("POST /api-shaped-post", h)
	mux.Handle("/handled", handler)
	notAMux.HandleFunc("GET /also-counts", h)
}
`
	if err := os.WriteFile(filepath.Join(dir, "fake.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	// A _test.go file must NOT contribute routes.
	testSrc := `package fake

func testOnly(mux *http.ServeMux) { mux.HandleFunc("/test-only", h) }
`
	if err := os.WriteFile(filepath.Join(dir, "fake_test.go"), []byte(testSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := pageRoutes(dir)
	if err != nil {
		t.Fatalf("pageRoutes: %v", err)
	}
	want := []string{"/also-counts", "/detail/{id}", "/getpage", "/handled", "/plain"}
	if !slices.Equal(got, want) {
		t.Errorf("pageRoutes = %v, want %v", got, want)
	}
}

// Subdirectories are packages too — /catalog is registered in
// internal/pages/catalog, and missing it is exactly the silent gap this
// guard exists to close.
func TestPageRoutesRecursesIntoSubdirectories(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package sub

func register(mux *http.ServeMux) { mux.HandleFunc("/nested", h) }
`
	if err := os.WriteFile(filepath.Join(sub, "sub.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := pageRoutes(dir)
	if err != nil {
		t.Fatalf("pageRoutes: %v", err)
	}
	if !slices.Contains(got, "/nested") {
		t.Errorf("pageRoutes = %v, missing /nested from the subdirectory", got)
	}
}

func TestSkipRouteAppliesTheDenylistByPrefix(t *testing.T) {
	for route, skip := range map[string]bool{
		"/api/pos/scan":         true,
		"/ui/basket":            true,
		"/ext/display":          true,
		"/v1/install/intents":   true,
		"/plugin-icons/a/1/x":   true,
		"/themes/dark.css":      true,
		"/help":                 true,
		"/help/{topic}":         true,
		"/public/":              true,
		"/healthz":              true,
		"/plugin/faq":           true,
		"/self-order":           true,
		"/self-order/shop":      true,
		"/backoffice":           false,
		"/menu":                 false,
		"/invoice/{display_no}": false,
		"/helpless-page":        false, // "/help" skips itself and "/help/…", not unrelated pages sharing the string prefix
	} {
		if got := skipRoute(route); got != skip {
			t.Errorf("skipRoute(%q) = %v, want %v", route, got, skip)
		}
	}
}

// The comparison itself, pure: a route with no claiming topic must surface.
func TestUncoveredRoutes(t *testing.T) {
	covered := func(r string) bool { return r == "/covered" || r == "/detail/{id}" }
	got := uncoveredRoutes([]string{"/covered", "/detail/{id}", "/orphan", "/api/skipped"}, covered)
	if !slices.Equal(got, []string{"/orphan"}) {
		t.Errorf("uncoveredRoutes = %v, want [/orphan]", got)
	}
}

// de ships no web/locales/de.json (its UI strings come from the external
// ut-plugin-language-de pack — see manualOnlyLocales' own doc comment), so
// without manualOnlyLocales it would be invisible to a glob of web/locales
// and this guard would silently never check it. This is the regression this
// test exists to catch: en can gain a topic with no German counterpart and
// nothing here would fail.
func TestShippedLocalesIncludesManualOnlyLocales(t *testing.T) {
	t.Chdir(filepath.Join("..", "..", ".."))

	got, err := shippedLocales()
	if err != nil {
		t.Fatalf("shippedLocales: %v", err)
	}
	if !slices.Contains(got, "de") {
		t.Errorf("shippedLocales() = %v, want it to contain %q (manual-only locale, no web/locales/de.json exists)", got, "de")
	}
	// ar/fa/tr are the actual core-shipped web/locales/*.json today — this
	// is an anti-false-pass check that the glob half of shippedLocales
	// still runs at all, not just the manualOnlyLocales union.
	for _, want := range []string{"ar", "fa", "tr"} {
		if !slices.Contains(got, want) {
			t.Errorf("shippedLocales() = %v, missing core-shipped locale %q", got, want)
		}
	}
}

// Integration: the REAL internal/pages tree against the REAL embedded manual
// must be fully covered — this is the check CI runs, exercised as a test so
// a coverage regression fails `go test ./...` too, not only the guard step.
func TestRealPagesTreeIsFullyCovered(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	pagesDir := filepath.Join(root, "internal", "pages")
	routes, err := pageRoutes(pagesDir)
	if err != nil {
		t.Fatalf("pageRoutes(%s): %v", pagesDir, err)
	}
	// Anti-false-pass: an empty or half-blind scan must fail here, not
	// "cover" everything vacuously.
	for _, sentinel := range []string{"/", "/menu", "/backoffice", "/invoice/{display_no}", "/catalog"} {
		if !slices.Contains(routes, sentinel) {
			t.Fatalf("scan of the real tree did not find %q — scanner is blind, refusing to trust its coverage verdict (found %v)", sentinel, routes)
		}
	}
	lib, err := manual.Load(uiassets.HelpFS, "help")
	if err != nil {
		t.Fatalf("loading embedded manual: %v", err)
	}
	if un := uncoveredRoutes(routes, lib.RouteCovered); len(un) > 0 {
		t.Errorf("user-facing page routes with no claiming manual topic: %v", un)
	}
}

// ADR-0160 section 2: a PR adding an English-only topic passes; the missing
// translation is only a warning. A topic English lacks is not invented.
func TestEnglishOnlyTopicIsWarningNotFailure(t *testing.T) {
	fsys := fstest.MapFS{
		"help/en/x.md": {Data: []byte("---\nid: x\ntitle: X\nsection: T\norder: 1\n---\n\n# X\n")},
	}
	lib, err := manual.Load(fsys, "help")
	if err != nil {
		t.Fatalf("manual.Load: %v", err)
	}
	got := translationWarnings(lib, []string{"de", "fa"})
	if len(got) != 2 || !strings.Contains(got[0], "non-blocking") {
		t.Errorf("want one non-blocking warning per locale, got %v", got)
	}
	if w := translationWarnings(lib, nil); len(w) != 0 {
		t.Errorf("no locales -> no warnings, got %v", w)
	}
}

// ut-docs#2615: an inline screenshot reference with no committed PNG is a
// guard failure (manual.Load only drops it, so Builtin() keeps working).
func TestMissingInlineScreenshotIsReported(t *testing.T) {
	topic := "---\nid: x\ntitle: X\nsection: T\norder: 1\n---\n\n# X\n\n- a\n  ![Alt](img/x-a.png)\n"
	fsys := fstest.MapFS{"help/en/x.md": {Data: []byte(topic)}}
	lib, err := manual.Load(fsys, "help")
	if err != nil {
		t.Fatalf("manual.Load: %v", err)
	}
	got := missingImageLines(lib)
	if len(got) != 1 || !strings.Contains(got[0], "en/x: img/x-a.png") {
		t.Fatalf("want one line naming en/x: img/x-a.png, got %v", got)
	}
	fsys["help/img/en/x-a.png"] = &fstest.MapFile{Data: []byte("png")}
	lib, _ = manual.Load(fsys, "help")
	if got := missingImageLines(lib); len(got) != 0 {
		t.Errorf("committed image should satisfy the check, got %v", got)
	}
}
