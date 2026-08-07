package httpx

import (
	"strings"
	"testing"
)

// helpLink backs {{ helpLink "topic-id" }} — the explicit contextual "?" for a
// SECTION inside a page whose route is already claimed by another topic (the
// settings cards: backups, claim, payments, printing, updates all live inside
// /settings, which display.md owns). It must render the same .help-hint
// markup nav.html's automatic "?" uses, so the e2e walk finds both the same
// way, with the same translated a11y label.
func TestHelpLinkRendersHintForKnownTopic(t *testing.T) {
	InitI18n(nil, "en") // no translator: T falls back to the key itself
	got := string(helpLinkHTML("backups", "en"))
	for _, want := range []string{
		`class="help-hint"`,
		`href="/help/backups"`,
		`data-testid="help-hint"`,
		`aria-label="help.open"`,
		`title="help.open"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("helpLinkHTML(backups) = %q, missing %q", got, want)
		}
	}
}

// A typo'd topic id must degrade to the manual's index — a "?" that 404s is
// worse than one that lands on the contents page (same rule as HelpHref).
func TestHelpLinkFallsBackToIndexForUnknownTopic(t *testing.T) {
	InitI18n(nil, "en")
	got := string(helpLinkHTML("no-such-topic", "en"))
	if !strings.Contains(got, `href="/help"`) {
		t.Errorf("helpLinkHTML(no-such-topic) = %q, want a /help index link", got)
	}
	if strings.Contains(got, "/help/no-such-topic") {
		t.Errorf("helpLinkHTML(no-such-topic) rendered a dead link: %q", got)
	}
}

// The func is part of every template render path's FuncMap, locale-bound.
func TestFuncsForIncludesHelpLink(t *testing.T) {
	if _, ok := FuncsFor("en")["helpLink"]; !ok {
		t.Fatal("FuncsFor is missing helpLink")
	}
	if _, ok := baseFuncs["helpLink"]; !ok {
		t.Fatal("baseFuncs is missing the helpLink fallback (fragment renderers parse templates with baseFuncs only)")
	}
}
