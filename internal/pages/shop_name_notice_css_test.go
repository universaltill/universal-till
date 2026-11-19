package pages

import (
	"regexp"
	"strings"
	"testing"
)

// ut-docs#4391 (card comment): at 360x740 in German the "Name your shop"
// banner wrapped one or two letters per line. The shared .pairing-notice
// row gives its text flex:1 / min-width:0 / overflow-wrap:anywhere, so a
// long CTA link beside it squeezes the text to ~0 and it breaks per letter.
// The banner must wrap its link and dismiss button onto a new line and give
// the text a real minimum basis. A browser-free guard on the rule.
func TestShopNameNoticeCSSWrapsInsteadOfSqueezing(t *testing.T) {
	css := readAppCSS(t)
	rule := func(sel string) string {
		m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(sel) + `\s*\{([^{}]*)\}`).FindStringSubmatch(css)
		if m == nil {
			t.Fatalf("app.css has no %q rule (#4391)", sel)
		}
		return m[1]
	}
	if !strings.Contains(rule(".shop-name-notice"), "flex-wrap: wrap") {
		t.Errorf(".shop-name-notice must set flex-wrap: wrap so a long CTA drops to its own line")
	}
	text := rule(".shop-name-notice .pairing-notice-text")
	if !regexp.MustCompile(`flex:\s*1 1 (1[0-9]|[2-9][0-9])rem`).MatchString(text) {
		t.Errorf(".shop-name-notice text needs a flex-basis of at least 10rem (got %q), else it shrinks to a letter per line", strings.TrimSpace(text))
	}
}
