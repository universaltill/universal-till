// Command checkhelptopics is the Go half of guard-help-topics.sh
// (ut-docs#361). It loads the embedded user manual exactly the way
// internal/manual.Builtin() does at runtime, and fails loudly on anything
// Builtin() would instead swallow into a log line and silently degrade
// from — a duplicate/conflicting route across topics, or a topic whose
// front matter doesn't parse — plus checks that every shipped locale has
// translated every topic english has.
//
// This deliberately calls the real internal/manual package rather than
// re-parsing front matter in bash/python the way guard-i18n.sh parses
// JSON: the front-matter grammar and the route-conflict rule (a route is
// checked against one map shared across ALL locales, not per-locale — see
// manual.Load's doc comment) are subtle enough that a shell reimplementation
// risks silently drifting from the real algorithm, which is exactly the bug
// class this guard exists to close (CLAUDE.md's claim about this very
// script had already drifted from reality once).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/universaltill/universal-till/internal/manual"
	uiassets "github.com/universaltill/universal-till/web"
)

func main() {
	lib, err := manual.Load(uiassets.HelpFS, "help")
	if err != nil {
		// Load's own error already names the conflicting topics + route
		// (a duplicate `routes:` entry) or the malformed file (bad/missing
		// front matter, duplicate id, id/filename mismatch, ...).
		fmt.Fprintf(os.Stderr, "guard-help-topics: %v\n", err)
		os.Exit(1)
	}

	locales, err := shippedLocales()
	if err != nil {
		fmt.Fprintf(os.Stderr, "guard-help-topics: %v\n", err)
		os.Exit(1)
	}

	fail := false
	for _, locale := range locales {
		// MissingTranslations reports every topic id as missing for a
		// locale it has never even seen a file for (a nil map lookup),
		// which is exactly what we want here: a locale whose whole
		// web/help/<locale>/ directory got deleted must fail this check,
		// not just one that's merely incomplete. The locale set itself
		// therefore has to come from the product's real shipped-locale
		// registry (web/locales/*.json — the same one guard-i18n.sh
		// enforces web/locales parity against), NOT from lib.Locales(),
		// which only lists locales that already have at least one topic
		// file loaded and so is blind to a locale with zero.
		missing := lib.MissingTranslations(locale)
		if len(missing) > 0 {
			fail = true
			fmt.Fprintf(os.Stderr, "guard-help-topics: locale %q is missing manual topics: %v\n", locale, missing)
		}
	}

	if fail {
		os.Exit(1)
	}
	fmt.Println("✓ help-topics guard: no route conflicts, every topic parses, all shipped locales complete")
}

// shippedLocales lists the product's shipped locales from web/locales/*.json
// (the base locale, en, excluded) — the same registry guard-i18n.sh checks
// every locale file's key set against. Run via `go run` from the repo root
// (guard-help-topics.sh cds there first), so a relative path is correct.
func shippedLocales() ([]string, error) {
	matches, err := filepath.Glob(filepath.Join("web", "locales", "*.json"))
	if err != nil {
		return nil, fmt.Errorf("listing web/locales: %w", err)
	}
	var locales []string
	for _, m := range matches {
		locale := strings.TrimSuffix(filepath.Base(m), ".json")
		if locale == manual.FallbackLocale {
			continue
		}
		locales = append(locales, locale)
	}
	sort.Strings(locales)
	return locales, nil
}
