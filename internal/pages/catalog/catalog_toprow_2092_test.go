package catalog

// ut-docs#2092: after removing the two rail-duplicate buttons (Modifiers,
// Option sets — see option_sets_test.go's
// TestCatalogPage_TopRowHasNoRailDuplicateButtons), the row's remaining
// five controls go icon-only. These pin the server-rendered shape: each
// control keeps a non-empty accessible name (aria-label + title, reusing
// its pre-existing i18n key rather than a new one — no new locale strings
// this card needs to add) and renders its assigned icon, per
// ut-docs/reference/list-and-dialog-pattern.md's icon-only vocabulary. The
// Playwright spec covers real layout (one line at 1024x600, RTL mirroring).

import (
	"regexp"
	"strings"
	"testing"
)

func TestCatalogTopRow_IconOnlyControlsHaveAccessibleNamesAndIcons(t *testing.T) {
	body := catalogPageBody(t)

	cases := []struct {
		name string
		// A regexp locating the control's opening tag uniquely.
		openTag *regexp.Regexp
		icon    string
	}{
		{"item-form-add-btn", regexp.MustCompile(`<button[^>]*id="item-form-add-btn"[^>]*>`), "plus"},
		{"import link", regexp.MustCompile(`<a[^>]*href="/import"[^>]*>`), "upload"},
		{"tax codes link", regexp.MustCompile(`<a[^>]*href="/catalog/tax-codes"[^>]*>`), "landmark"},
		{"catalog-export-btn", regexp.MustCompile(`<button[^>]*id="catalog-export-btn"[^>]*>`), "download"},
		{"catalog-barcode-backfill-btn", regexp.MustCompile(`<button[^>]*id="catalog-barcode-backfill-btn"[^>]*>`), "scan-barcode"},
	}

	for _, c := range cases {
		openTag := c.openTag.FindString(body)
		if openTag == "" {
			t.Fatalf("%s: no matching control found in /catalog", c.name)
		}
		if !nonEmptyAttr("aria-label").MatchString(openTag) {
			t.Errorf("%s has no non-empty aria-label: %s", c.name, openTag)
		}
		if !nonEmptyAttr("title").MatchString(openTag) {
			t.Errorf("%s has no non-empty title: %s", c.name, openTag)
		}
		if !strings.Contains(openTag, "btn-icon") {
			t.Errorf("%s should carry the .btn-icon class, got: %s", c.name, openTag)
		}
		// The icon itself renders just after the opening tag, inside the
		// control — find the whole element (button/anchor) body.
		closeTagName := "button"
		if strings.HasPrefix(openTag, "<a") {
			closeTagName = "a"
		}
		elRe := regexp.MustCompile(`(?s)` + regexp.QuoteMeta(openTag) + `(.*?)</` + closeTagName + `>`)
		m := elRe.FindStringSubmatch(body)
		if len(m) < 2 {
			t.Fatalf("%s: could not isolate control body", c.name)
		}
		if !strings.Contains(m[1], `data-icon="`+c.icon+`"`) {
			t.Errorf("%s should render the %q icon, got: %q", c.name, c.icon, m[1])
		}
	}
}
