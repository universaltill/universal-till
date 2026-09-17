package pages

// ut-docs#2224 / ADR-0098: the persistent app shell. Static guards over
// the shipped source, same pattern as transitions_test.go — the browser
// half (a boosted navigation keeps the document, syncs body/html
// attributes, falls back to a full load on a signature mismatch) lives in
// e2e/tests/persistent-shell-2224.spec.ts.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

func TestPersistentShell_BaseHTMLWrapsThePageRegion(t *testing.T) {
	src := readBaseHTML(t)
	open := strings.Index(src, `<div id="ut-page"`)
	if open < 0 {
		t.Fatal(`base.html has no <div id="ut-page"> region`)
	}
	tag := src[open : strings.Index(src[open:], ">")+open]
	for _, want := range []string{`hx-boost="true"`, `hx-history-elt`, `hx-history="false"`} {
		if !strings.Contains(tag, want) {
			t.Errorf("#ut-page tag is missing %s:\n%s", want, tag)
		}
	}
	// hx-target/hx-select/hx-swap on the region would be INHERITED by every
	// chip/poller inside it (their first `load` then swaps the region away
	// with an empty selection — seen live). The server sets them per
	// boosted response instead (httpx.BoostedNavigation).
	for _, forbidden := range []string{"hx-target", "hx-select", "hx-swap", "hx-push-url"} {
		if strings.Contains(tag, forbidden) {
			t.Errorf("#ut-page must not carry %s (inherited by every descendant): %s", forbidden, tag)
		}
	}
	end := strings.Index(src, "<!-- /ut-page -->")
	if end < 0 {
		t.Fatal("base.html has no `<!-- /ut-page -->` end marker")
	}
	region := src[open:end]
	// The status bar renders per-request state (update available, plugin
	// updates, enrolment) — it must be re-rendered per navigation, so it is
	// inside the region; its `view-transition-name` keeps it visually fixed.
	for _, inside := range []string{`{{ template "nav" . }}`, `id="pairing-notice-mount"`, `{{ template "pos_alert" . }}`, `<main class="container">`, `<footer class="statusbar"`} {
		if !strings.Contains(region, inside) {
			t.Errorf("%s must be INSIDE #ut-page (swapped per navigation)", inside)
		}
	}
	after := src[end:]
	for _, outside := range []string{`{{ template "bugreport_panel" . }}`, `id="elevation-modal"`} {
		if strings.Contains(region, outside) || !strings.Contains(after, outside) {
			t.Errorf("%s must be OUTSIDE #ut-page (persists across navigation)", outside)
		}
	}
}

// The rail chips and the pairing mount poll on `load`; without
// hx-preserve every navigation would re-fire all six requests — the very
// request count this card exists to cut.
func TestPersistentShell_RailChipsArePreserved(t *testing.T) {
	chdirRoot(t)
	nav, err := os.ReadFile("web/ui/partials/nav.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"bugreport-chip", "sync-chip", "fiscal-chip", "diagnostics-chip", "session-chip"} {
		if !strings.Contains(string(nav), `<span id="`+id+`" hx-preserve`) {
			t.Errorf("nav.html #%s lacks hx-preserve", id)
		}
	}
	if !strings.Contains(readBaseHTML(t), `id="pairing-notice-mount" hx-get="/ui/pairing-notice" hx-trigger="load, every 30s" hx-preserve`) {
		t.Error("base.html #pairing-notice-mount lacks hx-preserve")
	}
}

// An inline page script re-runs on every boosted arrival as a fresh classic
// script in the same global scope: a top-level `const`/`let`/`class` throws
// "Identifier has already been declared" on the second visit. Wrap in an
// IIFE (the rest of the codebase's convention) or use `var`.
func TestPersistentShell_NoTopLevelLexicalDeclarationsInInlineScripts(t *testing.T) {
	chdirRoot(t)
	var files []string
	for _, pat := range []string{"web/ui/layouts/*.html", "web/ui/pages/*.html", "web/ui/partials/*.html"} {
		m, _ := filepath.Glob(pat)
		files = append(files, m...)
	}
	script := regexp.MustCompile(`(?s)<script(?:\s[^>]*)?>(.*?)</script>`)
	decl := regexp.MustCompile(`^(const|let|class)\s`)
	var bad []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, m := range script.FindAllStringSubmatchIndex(src, -1) {
			body := src[m[2]:m[3]]
			if strings.Contains(src[m[0]:m[2]], "src=") {
				continue
			}
			depth := 0
			for i, line := range strings.Split(body, "\n") {
				st := strings.TrimSpace(line)
				if depth == 0 && decl.MatchString(st) {
					bad = append(bad, f+":"+itoa(1+strings.Count(src[:m[2]], "\n")+i)+" "+st)
				}
				depth += strings.Count(line, "{") + strings.Count(line, "(") - strings.Count(line, "}") - strings.Count(line, ")")
			}
		}
	}
	if len(bad) > 0 {
		t.Errorf("top-level lexical declaration(s) in an inline script (re-declared on every boosted arrival — wrap in an IIFE, ADR-0098):\n  %s", strings.Join(bad, "\n  "))
	}
}

func TestPersistentShell_HeadCarriesSignatureAndHistoryConfig(t *testing.T) {
	src := readBaseHTML(t)
	head := src[:strings.Index(src, "\n  <body ")]
	if !strings.Contains(head, `<meta name="ut-shell" content="{{ shellsig .theme }}">`) {
		t.Error(`<head> lacks <meta name="ut-shell" content="{{ shellsig .theme }}">`)
	}
	cfg := regexp.MustCompile(`<meta name="htmx-config" content='([^']*)'>`).FindStringSubmatch(head)
	if cfg == nil {
		t.Fatal("no htmx-config meta")
	}
	for _, want := range []string{`"historyCacheSize":0`, `"refreshOnHistoryMiss":true`, `"defaultSettleDelay":0`} {
		if !strings.Contains(cfg[1], want) {
			t.Errorf("htmx-config %s lacks %s", cfg[1], want)
		}
	}
	if !strings.Contains(head, "UT.ready = function") && !strings.Contains(head, "UT.ready=function") {
		t.Error("<head> must define UT.ready (the boost-safe DOMContentLoaded replacement)")
	}
}

// httpx.HeadAssets (what the shell signature hashes) must be exactly the
// `?v={{ assetv … }}` assets base.html's <head> loads — a new deferred
// script that is not in the signature would run stale after a self-update.
func TestPersistentShell_SignatureCoversEveryHeadAsset(t *testing.T) {
	src := readBaseHTML(t)
	head := src[:strings.Index(src, "\n  <body ")]
	re := regexp.MustCompile(`assetv "(public/[^"]+)"`)
	got := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(head, -1) {
		got[m[1]] = true
	}
	want := map[string]bool{}
	for _, a := range httpx.HeadAssets {
		want[a] = true
	}
	var missing, extra []string
	for a := range got {
		if !want[a] {
			missing = append(missing, a)
		}
	}
	for a := range want {
		if !got[a] {
			extra = append(extra, a)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("base.html <head> loads assets not in httpx.HeadAssets: %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("httpx.HeadAssets lists assets base.html <head> does not load: %v", extra)
	}
}

// DOMContentLoaded fires once per document, never per boosted page. A bare
// listener silently does nothing on a boosted arrival; the readyState-
// guarded form (or UT.ready) runs on both paths.
func TestPersistentShell_NoBareDOMContentLoadedListeners(t *testing.T) {
	chdirRoot(t)
	var files []string
	for _, pat := range []string{"web/public/*.js", "web/ui/layouts/*.html", "web/ui/pages/*.html", "web/ui/partials/*.html"} {
		m, err := filepath.Glob(pat)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	var bad []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			if !strings.Contains(l, "addEventListener('DOMContentLoaded'") && !strings.Contains(l, `addEventListener("DOMContentLoaded"`) {
				continue
			}
			ctx := strings.Join(lines[max(0, i-3):i+1], "\n")
			if strings.Contains(ctx, "readyState") {
				continue
			}
			bad = append(bad, f+":"+itoa(i+1))
		}
	}
	if len(bad) > 0 {
		t.Errorf("bare DOMContentLoaded listener(s) — use UT.ready(fn) or guard with document.readyState (ADR-0098):\n  %s", strings.Join(bad, "\n  "))
	}
}

// Links that are not pages must opt out of boosting: a boosted export
// would try to swap CSV into the shell (ADR-0098 rule 4; the signature
// check is the safety net, not the mechanism).
func TestPersistentShell_NonPageLinksOptOutOfBoost(t *testing.T) {
	chdirRoot(t)
	var files []string
	for _, pat := range []string{"web/ui/layouts/*.html", "web/ui/pages/*.html", "web/ui/partials/*.html"} {
		m, _ := filepath.Glob(pat)
		files = append(files, m...)
	}
	anchor := regexp.MustCompile(`(?s)<a\b[^>]*>`)
	var bad []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, tag := range anchor.FindAllString(src, -1) {
			nonPage := strings.Contains(tag, "/export") ||
				strings.Contains(tag, " download") ||
				strings.Contains(tag, `target="_blank"`) ||
				strings.Contains(tag, `href="http`) ||
				strings.Contains(tag, `:href="'http`)
			if nonPage && !strings.Contains(tag, `hx-boost="false"`) {
				line := 1 + strings.Count(src[:strings.Index(src, tag)], "\n")
				bad = append(bad, f+":"+itoa(line)+" "+strings.Join(strings.Fields(tag), " "))
			}
		}
	}
	if len(bad) > 0 {
		t.Errorf("non-page link(s) inside the boosted shell without hx-boost=\"false\":\n  %s", strings.Join(bad, "\n  "))
	}
}
