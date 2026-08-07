package manual

import (
	"strings"
	"testing"
	"testing/fstest"
)

// A miniature help tree standing in for web/help/. Two locales, so the
// fallback and the "same topic ids across locales" rules are both exercised.
var testFS = fstest.MapFS{
	"help/en/sell.md": &fstest.MapFile{Data: []byte(`---
id: sell
title: Selling
section: Everyday selling
order: 10
summary: Ring up a sale and take payment.
routes: [/, /ui/basket]
keywords: [basket, checkout, tender, till]
---

# Selling

Tap a button to add it to the basket.

- Tap **Pay** to open the tender panel.
`)},
	"help/en/catalog.md": &fstest.MapFile{Data: []byte(`---
id: catalog
title: Catalog
section: Setting up
order: 20
summary: Your products, prices and barcodes.
routes: [/catalog]
keywords: [items, prices, barcode]
---

# Catalog

Your products live here.
`)},
	"help/fa/sell.md": &fstest.MapFile{Data: []byte(`---
id: sell
title: فروش
section: فروش روزمره
order: 10
summary: ثبت یک فروش.
routes: [/, /ui/basket]
keywords: [سبد]
---

# فروش

روی دکمه بزنید.
`)},
	// Deliberately no fa/catalog.md — fa must fall back to English.
	"help/en/not-a-topic.txt": &fstest.MapFile{Data: []byte("ignored")},
}

func load(t *testing.T) *Library {
	t.Helper()
	lib, err := Load(testFS, "help")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return lib
}

func TestLoadParsesFrontMatter(t *testing.T) {
	lib := load(t)
	tp, ok := lib.Topic("en", "sell")
	if !ok {
		t.Fatal("en/sell not loaded")
	}
	if tp.Title != "Selling" {
		t.Errorf("title = %q, want Selling", tp.Title)
	}
	if tp.Section != "Everyday selling" {
		t.Errorf("section = %q", tp.Section)
	}
	if tp.Order != 10 {
		t.Errorf("order = %d, want 10", tp.Order)
	}
	if len(tp.Routes) != 2 || tp.Routes[0] != "/" || tp.Routes[1] != "/ui/basket" {
		t.Errorf("routes = %v", tp.Routes)
	}
	if len(tp.Keywords) != 4 {
		t.Errorf("keywords = %v", tp.Keywords)
	}
	if tp.Summary == "" {
		t.Error("summary empty")
	}
}

func TestLoadRendersMarkdownBody(t *testing.T) {
	lib := load(t)
	tp, _ := lib.Topic("en", "sell")
	html := string(tp.HTML)
	if !strings.Contains(html, "<strong>Pay</strong>") {
		t.Errorf("bold not rendered: %s", html)
	}
	if !strings.Contains(html, "<li>") {
		t.Errorf("list not rendered: %s", html)
	}
	// Front matter must not leak into the rendered body.
	if strings.Contains(html, "routes:") || strings.Contains(html, "---") {
		t.Errorf("front matter leaked into body: %s", html)
	}
}

func TestLoadIgnoresNonMarkdown(t *testing.T) {
	lib := load(t)
	if _, ok := lib.Topic("en", "not-a-topic"); ok {
		t.Error("a .txt file became a topic")
	}
}

func TestTopicFallsBackToEnglish(t *testing.T) {
	lib := load(t)
	tp, ok := lib.Topic("fa", "catalog")
	if !ok {
		t.Fatal("fa/catalog should fall back to en, got nothing")
	}
	if tp.Locale != "en" {
		t.Errorf("fallback locale = %q, want en", tp.Locale)
	}
	if !tp.Translated {
		// Translated must be false for a fallback so the page can say so.
	} else {
		t.Error("fallback topic reported as translated")
	}

	native, ok := lib.Topic("fa", "sell")
	if !ok || native.Locale != "fa" {
		t.Fatalf("fa/sell should be native fa, got %+v", native)
	}
	if !native.Translated {
		t.Error("native topic reported as untranslated")
	}
}

func TestTopicUnknownID(t *testing.T) {
	lib := load(t)
	if _, ok := lib.Topic("en", "nope"); ok {
		t.Error("unknown id resolved")
	}
	// A path-traversal shaped id must not resolve either.
	if _, ok := lib.Topic("en", "../../etc/passwd"); ok {
		t.Error("traversal id resolved")
	}
}

func TestTreeGroupsAndOrders(t *testing.T) {
	lib := load(t)
	tree := lib.Tree("en")
	if len(tree) != 2 {
		t.Fatalf("sections = %d, want 2", len(tree))
	}
	// Ordered by the lowest topic order in each section: Everyday selling (10)
	// before Setting up (20).
	if tree[0].Title != "Everyday selling" {
		t.Errorf("first section = %q", tree[0].Title)
	}
	if len(tree[0].Topics) != 1 || tree[0].Topics[0].ID != "sell" {
		t.Errorf("section topics = %+v", tree[0].Topics)
	}
}

func TestTreeForLocaleIncludesUntranslatedTopics(t *testing.T) {
	// A shop reading in Persian must still see every topic listed — an
	// untranslated one falls back rather than vanishing from the tree.
	lib := load(t)
	var ids []string
	for _, s := range lib.Tree("fa") {
		for _, tp := range s.Topics {
			ids = append(ids, tp.ID)
		}
	}
	if len(ids) != 2 {
		t.Fatalf("fa tree ids = %v, want both topics", ids)
	}
}

func TestTopicForRoute(t *testing.T) {
	lib := load(t)
	id, ok := lib.TopicForRoute("/catalog")
	if !ok || id != "catalog" {
		t.Errorf("TopicForRoute(/catalog) = %q,%v", id, ok)
	}
	if id, _ := lib.TopicForRoute("/ui/basket"); id != "sell" {
		t.Errorf("TopicForRoute(/ui/basket) = %q, want sell", id)
	}
	if _, ok := lib.TopicForRoute("/nowhere"); ok {
		t.Error("unmapped route resolved")
	}
}

func TestSearchRanksTitleAboveBody(t *testing.T) {
	lib := load(t)
	res := lib.Search("en", "catalog")
	if len(res) == 0 {
		t.Fatal("no results for 'catalog'")
	}
	if res[0].Topic.ID != "catalog" {
		t.Errorf("top hit = %q, want catalog", res[0].Topic.ID)
	}
}

func TestSearchMatchesKeywordsAndBody(t *testing.T) {
	lib := load(t)
	if res := lib.Search("en", "tender"); len(res) == 0 || res[0].Topic.ID != "sell" {
		t.Errorf("keyword search failed: %+v", res)
	}
	if res := lib.Search("en", "basket"); len(res) == 0 {
		t.Error("body/keyword search found nothing for 'basket'")
	}
}

func TestSearchIsCaseAndAccentInsensitiveEnough(t *testing.T) {
	lib := load(t)
	if res := lib.Search("en", "SELLING"); len(res) == 0 {
		t.Error("uppercase query found nothing")
	}
}

func TestSearchEmptyQueryReturnsNothing(t *testing.T) {
	lib := load(t)
	if res := lib.Search("en", "   "); len(res) != 0 {
		t.Errorf("blank query returned %d results", len(res))
	}
}

func TestSearchSnippetHasNoHTML(t *testing.T) {
	lib := load(t)
	res := lib.Search("en", "button")
	if len(res) == 0 {
		t.Fatal("no results")
	}
	if strings.Contains(res[0].Snippet, "<") {
		t.Errorf("snippet contains markup: %q", res[0].Snippet)
	}
}

func TestIDsMatchAcrossLocalesReportsGaps(t *testing.T) {
	// The guard script's Go-side counterpart: which topics are missing a
	// translation. fa is missing catalog.
	lib := load(t)
	missing := lib.MissingTranslations("fa")
	if len(missing) != 1 || missing[0] != "catalog" {
		t.Errorf("MissingTranslations(fa) = %v, want [catalog]", missing)
	}
	if got := lib.MissingTranslations("en"); len(got) != 0 {
		t.Errorf("en should be complete, missing %v", got)
	}
}

func TestLoadRejectsTopicWithoutID(t *testing.T) {
	bad := fstest.MapFS{"help/en/x.md": &fstest.MapFile{Data: []byte("---\ntitle: No id\n---\nbody\n")}}
	if _, err := Load(bad, "help"); err == nil {
		t.Error("a topic with no id should be a load error, not a silent skip")
	}
}

func TestLoadRejectsDuplicateRoute(t *testing.T) {
	// Two topics claiming the same route makes the ? link ambiguous.
	dup := fstest.MapFS{
		"help/en/a.md": &fstest.MapFile{Data: []byte("---\nid: a\ntitle: A\nroutes: [/x]\n---\nb\n")},
		"help/en/b.md": &fstest.MapFile{Data: []byte("---\nid: b\ntitle: B\nroutes: [/x]\n---\nb\n")},
	}
	if _, err := Load(dup, "help"); err == nil {
		t.Error("duplicate route should be a load error")
	}
}

// The till's default locale is a BCP-47 tag ("en-US"), while the manual's
// directories are bare language codes ("en"). Without matching on the base
// subtag, a default install falls back for every topic and stamps "not
// translated yet" across the whole manual — which is exactly what it did the
// first time this was driven in a browser.
func TestTopicMatchesRegionalLocaleTag(t *testing.T) {
	lib := load(t)
	tp, ok := lib.Topic("en-US", "sell")
	if !ok {
		t.Fatal("en-US did not resolve")
	}
	if !tp.Translated {
		t.Error("en-US topic wrongly reported as untranslated")
	}
	fa, ok := lib.Topic("fa-IR", "sell")
	if !ok || fa.Locale != "fa" || !fa.Translated {
		t.Errorf("fa-IR did not resolve to the fa topic: %+v", fa)
	}
}

// ---------------------------------------------------------------------------
// Screenshots (ut-docs#327): a topic whose locale has a generated screenshot
// under help/img/<locale>/<id>.png leads with it; one without renders exactly
// as before. A separate FS so the shared testFS's assertions stay untouched.
// ---------------------------------------------------------------------------

// tinyPNG stands in for a generated screenshot — the injection only stats the
// file, it never decodes it.
var tinyPNG = &fstest.MapFile{Data: []byte("\x89PNG\r\n\x1a\nnot-a-real-png")}

var shotFS = fstest.MapFS{
	"help/en/sell.md": &fstest.MapFile{Data: []byte(`---
id: sell
title: Selling & "tender"
routes: [/]
---

Tap a button.
`)},
	"help/en/catalog.md": &fstest.MapFile{Data: []byte(`---
id: catalog
title: Catalog
routes: [/catalog]
---

Your products.
`)},
	"help/fa/sell.md": &fstest.MapFile{Data: []byte(`---
id: sell
title: فروش
---

روی دکمه بزنید.
`)},
	// en has screenshots for both topics; fa has none yet, and fa/catalog.md
	// doesn't exist at all (falls back to en).
	"help/img/en/sell.png":    tinyPNG,
	"help/img/en/catalog.png": tinyPNG,
}

func TestTopicHTMLLeadsWithScreenshotWhenPresent(t *testing.T) {
	lib, err := Load(shotFS, "help")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tp, ok := lib.Topic("en", "sell")
	if !ok {
		t.Fatal("en/sell not loaded")
	}
	html := string(tp.HTML)
	if !strings.HasPrefix(html, "<figure") {
		t.Errorf("screenshot should lead the topic, got: %s", html)
	}
	if !strings.Contains(html, `<img src="/help/img/en/sell.png"`) {
		t.Errorf("screenshot img missing: %s", html)
	}
	// The alt text is the localized title, escaped — it carries a quote here.
	if !strings.Contains(html, `alt="Selling &amp; &#34;tender&#34;"`) {
		t.Errorf("alt text not the escaped title: %s", html)
	}
}

func TestTopicWithoutScreenshotRendersAsBefore(t *testing.T) {
	lib, err := Load(shotFS, "help")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// fa/sell is translated but has no fa screenshot yet — no image, no
	// placeholder, no broken link.
	tp, ok := lib.Topic("fa", "sell")
	if !ok || tp.Locale != "fa" {
		t.Fatalf("fa/sell should be native fa, got %+v", tp)
	}
	if strings.Contains(string(tp.HTML), "/help/img/") {
		t.Errorf("untranslated-screenshot topic grew an img: %s", tp.HTML)
	}
}

func TestFallbackTopicCarriesEnglishScreenshot(t *testing.T) {
	lib, err := Load(shotFS, "help")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// fa/catalog falls back to the English topic — it must carry the English
	// screenshot (tp.Locale is en), not a broken fa image link.
	tp, ok := lib.Topic("fa", "catalog")
	if !ok || tp.Locale != "en" {
		t.Fatalf("fa/catalog should fall back to en, got %+v", tp)
	}
	if !strings.Contains(string(tp.HTML), `<img src="/help/img/en/catalog.png"`) {
		t.Errorf("fallback topic lost the English screenshot: %s", tp.HTML)
	}
}

// NOTE (found in review): a dedicated "img is not a locale" test was removed
// here. help/img/ only ever contains .png files, and Load's per-entry loop
// already skips anything not ending in ".md" — so even with the explicit
// `if locale == "img" { continue }` guard in Load deleted, "img" can never
// produce a topic or appear in Locales() with the current fixture (verified:
// removing the guard leaves every existing test green). The guard is
// documentation/a saved ReadDir call, not behavior a test can meaningfully
// pin without fabricating a help/img/*.md file that doesn't reflect reality.

// ---------------------------------------------------------------------------
// Parameterized routes (ut-docs#326): a topic may declare /invoice/{display_no}
// and the "?" on a real /invoice/12345 request must still resolve to it. Exact
// declared routes keep priority, and the guard's pattern-vs-pattern coverage
// check reuses the same matcher, so both are pinned here.
// ---------------------------------------------------------------------------

var patternFS = fstest.MapFS{
	"help/en/invoices.md": &fstest.MapFile{Data: []byte(`---
id: invoices
title: Invoices
routes: [/invoices, /invoice/{display_no}]
---

Invoices.
`)},
	"help/en/reports.md": &fstest.MapFile{Data: []byte(`---
id: reports
title: Reports
routes: [/journal, /journal/{receipt}]
---

Reports.
`)},
	// Claims the literal path /invoice/new — an exact route must beat the
	// invoices topic's {display_no} pattern for that same path.
	"help/en/newinvoice.md": &fstest.MapFile{Data: []byte(`---
id: newinvoice
title: New invoice
routes: [/invoice/new]
---

New invoice.
`)},
	// A LITERAL route with no sibling pattern of the same shape — isolates
	// the directional bug review found: /plugins/store must not be treated
	// as covering a registered /plugins/{id}, unlike /invoice/{display_no}
	// above, which already IS a pattern and so would (correctly) cover a
	// same-shaped registered pattern regardless of directionality.
	"help/en/plugins.md": &fstest.MapFile{Data: []byte(`---
id: plugins
title: Plugins
routes: [/plugins/store]
---

Plugins.
`)},
}

func loadPatterns(t *testing.T) *Library {
	t.Helper()
	lib, err := Load(patternFS, "help")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return lib
}

func TestTopicForRouteMatchesParameterizedPattern(t *testing.T) {
	lib := loadPatterns(t)
	if id, ok := lib.TopicForRoute("/invoice/12345"); !ok || id != "invoices" {
		t.Errorf("TopicForRoute(/invoice/12345) = %q,%v; want invoices,true", id, ok)
	}
	if id, ok := lib.TopicForRoute("/journal/R-0001"); !ok || id != "reports" {
		t.Errorf("TopicForRoute(/journal/R-0001) = %q,%v; want reports,true", id, ok)
	}
}

func TestTopicForRouteExactMatchBeatsPattern(t *testing.T) {
	lib := loadPatterns(t)
	// /invoice/new matches BOTH the exact route and the {display_no} pattern;
	// the exact declaration must win, deterministically.
	if id, ok := lib.TopicForRoute("/invoice/new"); !ok || id != "newinvoice" {
		t.Errorf("TopicForRoute(/invoice/new) = %q,%v; want newinvoice,true", id, ok)
	}
	// The plain exact route still fast-paths.
	if id, ok := lib.TopicForRoute("/invoices"); !ok || id != "invoices" {
		t.Errorf("TopicForRoute(/invoices) = %q,%v; want invoices,true", id, ok)
	}
}

func TestTopicForRoutePatternSegmentRules(t *testing.T) {
	lib := loadPatterns(t)
	for _, path := range []string{
		"/invoice",          // too few segments
		"/invoice/12/extra", // too many segments
		"/order/12345",      // differing literal segment
		"/invoice/",         // a {name} placeholder must not match an empty segment
		"/nowhere",          // totally unmatched (unchanged behavior)
	} {
		if id, ok := lib.TopicForRoute(path); ok {
			t.Errorf("TopicForRoute(%q) = %q,true; want no match", path, id)
		}
	}
}

// RouteMatches is shared with the CI coverage guard, which compares a
// registered mux pattern against declared routes — so a {param} on either
// side has to match, and the param names don't have to agree.
func TestRouteMatches(t *testing.T) {
	for _, tc := range []struct {
		pattern, path string
		want          bool
	}{
		{"/invoice/{display_no}", "/invoice/12345", true},
		{"/invoice/{display_no}", "/invoice/{id}", true}, // pattern vs pattern, names differ
		{"/invoice/{display_no}", "/invoice", false},
		{"/invoice/{display_no}", "/invoice/1/2", false},
		{"/invoice/{display_no}", "/order/1", false},
		{"/invoice/{display_no}", "/invoice/", false},
		{"/invoices", "/invoices", true},
		{"/", "/", true},
		{"/", "/x", false},
		// Directional (ut-docs#326 review finding): a declared LITERAL must
		// not be treated as covering a registered pattern just because the
		// registered side has a {param} segment — only a {param} on the
		// declared (pattern/first-argument) side is generic.
		{"/plugins/store", "/plugins/{id}", false},
	} {
		if got := RouteMatches(tc.pattern, tc.path); got != tc.want {
			t.Errorf("RouteMatches(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

// RouteCovered backs the guard's page-route coverage check: a registered mux
// pattern counts as covered when a topic claims it exactly or via a
// parameter-compatible pattern.
func TestRouteCovered(t *testing.T) {
	lib := loadPatterns(t)
	for _, tc := range []struct {
		pattern string
		want    bool
	}{
		{"/invoices", true},
		{"/invoice/{display_no}", true},
		{"/invoice/{id}", true}, // same shape, different param name — covered
		{"/journal/{receipt}", true},
		{"/backoffice-nope", false},
		{"/invoice/{id}/settings", false},
		// The exact bug review found: /plugins/store is a real declared
		// literal, but it must not "cover" a hypothetical registered
		// /plugins/{id} — that would let the guard go green while
		// TopicForRoute still 404s the "?" on every real request to it.
		{"/plugins/{id}", false},
	} {
		if got := lib.RouteCovered(tc.pattern); got != tc.want {
			t.Errorf("RouteCovered(%q) = %v, want %v", tc.pattern, got, tc.want)
		}
	}
}

// Snippets are shown to a shop owner, so they keep the prose's own casing —
// the lower-cased copy exists for matching only. (First driven run showed
// "catalog, variants & barcodes your products: names…" in flat lowercase.)
func TestSearchSnippetKeepsOriginalCase(t *testing.T) {
	lib := load(t)
	res := lib.Search("en", "tap")
	if len(res) == 0 {
		t.Fatal("no results")
	}
	if !strings.Contains(res[0].Snippet, "Tap") {
		t.Errorf("snippet lost its casing: %q", res[0].Snippet)
	}
}
