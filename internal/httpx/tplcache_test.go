package httpx

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/pos"
	uiassets "github.com/universaltill/universal-till/web"
)

// TestCachedTemplate_ParsesOncePerKey is the acceptance criterion's own
// wording made concrete: "asserting ParseFS is called a bounded number of
// times not proportional to request count." tplCache is unexported but this
// test lives in the same package, so it can assert directly on cache
// occupancy rather than instrumenting production code with a counter.
func TestCachedTemplate_ParsesOncePerKey(t *testing.T) {
	key := "test:parse-once:" + t.Name()
	files := []string{"ui/partials/help_nav.html"}
	funcs := FuncsFor("en")

	first, err := cachedTemplate(key, "base.html", funcs, files...)
	if err != nil {
		t.Fatalf("cachedTemplate (1st): %v", err)
	}

	for i := 0; i < 50; i++ {
		again, err := cachedTemplate(key, "base.html", funcs, files...)
		if err != nil {
			t.Fatalf("cachedTemplate (repeat %d): %v", i, err)
		}
		if again != first {
			t.Fatalf("call %d returned a different *Template for the same key — reparsed instead of reusing the cache", i)
		}
	}
}

// TestCachedTemplate_DistinctKeysDontCollide guards the flip side: two
// different file sets must never share a cache entry.
func TestCachedTemplate_DistinctKeysDontCollide(t *testing.T) {
	funcs := FuncsFor("en")
	a, err := cachedTemplate("test:distinct:a:"+t.Name(), "base.html", funcs, "ui/partials/help_nav.html")
	if err != nil {
		t.Fatalf("cachedTemplate a: %v", err)
	}
	b, err := cachedTemplate("test:distinct:b:"+t.Name(), "base.html", funcs, "ui/partials/journal.html")
	if err != nil {
		t.Fatalf("cachedTemplate b: %v", err)
	}
	if a == b {
		t.Fatalf("different keys/file sets must not resolve to the same cached template")
	}
}

// TestClonedTemplate_IndependentAcrossCalls proves ClonedTemplate always
// hands back its own independent *Template (never the shared cached base,
// and never a previous call's clone) — the property that makes it safe for
// two calls to bind different funcs without one clobbering the other. The
// locale-correctness consequence of this (money/T actually rendering the
// right locale, not a stale closure) is asserted end-to-end through a real
// production view in internal/ui.TestNewJournalView_LocaleNotStaleAcrossCachedCalls.
func TestClonedTemplate_IndependentAcrossCalls(t *testing.T) {
	key := "test:cloned-independent:" + t.Name()
	files := []string{"ui/partials/help_nav.html"}

	base, err := cachedTemplate(key, "base.html", FuncsFor("en"), files...)
	if err != nil {
		t.Fatalf("cachedTemplate: %v", err)
	}
	a, err := ClonedTemplate(key, "base.html", FuncsFor("en"), files...)
	if err != nil {
		t.Fatalf("ClonedTemplate a: %v", err)
	}
	b, err := ClonedTemplate(key, "base.html", FuncsFor("fa"), files...)
	if err != nil {
		t.Fatalf("ClonedTemplate b: %v", err)
	}
	if a == base || b == base {
		t.Fatalf("ClonedTemplate must never return the shared cached base directly")
	}
	if a == b {
		t.Fatalf("ClonedTemplate must return a fresh clone per call, not reuse a previous call's clone")
	}
}

// TestClonedTemplate_ConcurrentSafe runs many goroutines through
// ClonedTemplate + Execute concurrently, each binding a DISTINCT, uniquely
// identifiable "T" closure (rather than every goroutine sharing identical
// funcs) and asserting its own output carries only its own marker — exactly
// the "many overlapping requests during a busy checkout, different locales
// or a per-request taxCodeName closure" shape this fix targets.
//
// The distinct-funcs shape matters, not just concurrency: an earlier version
// of this test had every goroutine pass identical funcs, so it had nothing
// to detect a cross-talk bug WITH (ut-docs#1320 review, finding 3) — e.g. a
// future refactor that calls .Funcs() on the shared cached base before
// Clone() instead of after passes go test AND go test -race cleanly (the
// base's funcmap write is itself mutex-guarded inside text/template, so
// there's no data race to catch) while silently making every concurrent
// caller of a shared key race to clobber which locale's closures "win" —
// this test's per-goroutine markers turn that logical corruption into a
// deterministic, -race-covered failure.
func TestClonedTemplate_ConcurrentSafe(t *testing.T) {
	const goroutines = 64
	key := "test:concurrent:" + t.Name()
	files := []string{"ui/partials/journal.html"}

	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			marker := fmt.Sprintf("MARKER-%d", id)
			funcs := FuncsFor("en")
			funcs["T"] = func(string) string { return marker }

			tpl, err := ClonedTemplate(key, "journal.html", funcs, files...)
			if err != nil {
				errs <- fmt.Errorf("goroutine %d: ClonedTemplate: %w", id, err)
				return
			}
			var buf bytes.Buffer
			if err := tpl.ExecuteTemplate(&buf, "journal", map[string]any{}); err != nil {
				errs <- fmt.Errorf("goroutine %d: Execute: %w", id, err)
				return
			}
			out := buf.String()
			if !strings.Contains(out, marker) {
				errs <- fmt.Errorf("goroutine %d: own marker %q missing from its own output: %s", id, marker, out)
				return
			}
			for other := 0; other < goroutines; other++ {
				if other == id {
					continue
				}
				otherMarker := fmt.Sprintf("MARKER-%d", other)
				// MARKER-1 is a substring of MARKER-10..19/100+ — only
				// flag a genuine foreign marker, not a numeric prefix
				// collision with this goroutine's own.
				if strings.Contains(out, otherMarker) && !strings.Contains(marker, otherMarker) {
					errs <- fmt.Errorf("goroutine %d: CROSS-TALK, saw goroutine %d's marker %q in own output: %s", id, other, otherMarker, out)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// BenchmarkTemplateParse_Uncached vs BenchmarkTemplateParse_Cached isolate
// just the parse step (what every render call site did before this fix,
// vs. a cache hit now) — useful for seeing the parse cost in isolation, but
// NOT the full picture: see BenchmarkRenderBasket_* below for the
// execute-inclusive numbers, which are what actually changed for a real
// request. html/template's contextual auto-escaping analysis re-runs on
// every Clone() (Clone deep-copies the parse trees into a fresh, unescaped
// namespace) — this fix removes the LEXING/PARSING cost, not the escaping
// cost, and the execute-inclusive benchmarks below are what proves the net
// win honestly (ut-docs#1320 review, finding 2).
func BenchmarkTemplateParse_Uncached(b *testing.B) {
	funcs := FuncsFor("en")
	files := []string{
		"ui/layouts/base.html",
		"ui/partials/nav.html",
		"ui/partials/buttons.html",
		"ui/partials/buttons_admin.html",
		"ui/partials/basket.html",
		"ui/partials/bugreport_panel.html",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := template.New("base.html").Funcs(funcs).ParseFS(uiassets.FS, files...); err != nil {
			b.Fatalf("ParseFS: %v", err)
		}
	}
}

func BenchmarkTemplateParse_Cached(b *testing.B) {
	funcs := FuncsFor("en")
	files := []string{
		"ui/layouts/base.html",
		"ui/partials/nav.html",
		"ui/partials/buttons.html",
		"ui/partials/buttons_admin.html",
		"ui/partials/basket.html",
		"ui/partials/bugreport_panel.html",
	}
	key := "bench:cached-parse"
	// Prime the cache once, outside the timed loop, mirroring the first
	// real request in a running process.
	if _, err := ClonedTemplate(key, "base.html", funcs, files...); err != nil {
		b.Fatalf("prime ClonedTemplate: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ClonedTemplate(key, "base.html", funcs, files...); err != nil {
			b.Fatalf("ClonedTemplate: %v", err)
		}
	}
}

// BenchmarkRenderBasket_Uncached vs BenchmarkRenderBasket_Cached measure the
// full request shape on the actual checkout hot path — parse (or cache hit)
// + clone + escape + Execute — the honest, acceptance-criterion-satisfying
// "did this fix remove real cost" comparison (ut-docs#1320 review, finding
// 2: the parse-only benchmarks above understate the win by omitting the
// escape-analysis cost that Clone() re-pays on every call, but overstate it
// by omitting Execute entirely — this pair includes both).
func BenchmarkRenderBasket_Uncached(b *testing.B) {
	funcs := FuncsFor("en")
	basket := &pos.Basket{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t, err := template.New("base.html").Funcs(funcs).ParseFS(uiassets.FS,
			"ui/layouts/base.html",
			"ui/partials/basket.html",
			"ui/partials/nav.html",
		)
		if err != nil {
			b.Fatalf("ParseFS: %v", err)
		}
		var buf bytes.Buffer
		if err := t.ExecuteTemplate(&buf, "basket", basket); err != nil {
			b.Fatalf("Execute: %v", err)
		}
	}
}

func BenchmarkRenderBasket_Cached(b *testing.B) {
	funcs := FuncsFor("en")
	basket := &pos.Basket{}
	key := "bench:render-basket"
	files := []string{
		"ui/layouts/base.html",
		"ui/partials/basket.html",
		"ui/partials/nav.html",
	}
	if _, err := ClonedTemplate(key, "base.html", funcs, files...); err != nil {
		b.Fatalf("prime ClonedTemplate: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t, err := ClonedTemplate(key, "base.html", funcs, files...)
		if err != nil {
			b.Fatalf("ClonedTemplate: %v", err)
		}
		var buf bytes.Buffer
		if err := t.ExecuteTemplate(&buf, "basket", basket); err != nil {
			b.Fatalf("Execute: %v", err)
		}
	}
}
