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
