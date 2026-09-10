package catalog

// ut-docs#2000: at 360px the item form's pinned head was measured at 42% of
// the viewport. Part of the fix is Close/Save going icon-only at phone width
// (app.css), matching the existing icon-only Delete (ut-docs#1956). +New
// stays text-only — its own label already bakes in a "+" glyph in every
// shipped locale, so pairing it with a separate icon doubled the symbol
// (independent review finding F2) — but still gets an aria-label/title for
// consistency. These pin the server-rendered shape: an accessible name
// (aria-label/title) must survive on Close/Save regardless of whether the
// visible .btn-label span is shown or hidden, and their icon must be
// present. The Playwright spec catalog-item-form-2000.spec.ts covers the
// header-height and tab-strip-scrolling behaviour a browser adds.

import (
	"regexp"
	"strings"
	"testing"
)

// A non-empty aria-label/title value, not merely the attribute's presence
// (independent review finding F8 — aria-label="" would have passed the
// original, looser check while being an accessibility no-op).
var nonEmptyAttr = func(attr string) *regexp.Regexp {
	return regexp.MustCompile(attr + `="[^"]*\S[^"]*"`)
}

func TestItemForm_HeadActionsHaveNonEmptyAccessibleNames(t *testing.T) {
	dialog := itemFormDialog(t, catalogPageBody(t))
	for _, id := range []string{"item-form-reset", "item-form-close-btn", "item-form-submit"} {
		openTag := regexp.MustCompile(`<button[^>]*id="` + id + `"[^>]*>`).FindString(dialog)
		if openTag == "" {
			t.Fatalf("no #%s button found in the item form dialog", id)
		}
		if !nonEmptyAttr("aria-label").MatchString(openTag) {
			t.Errorf("#%s has no non-empty aria-label: %s", id, openTag)
		}
		if !nonEmptyAttr("title").MatchString(openTag) {
			t.Errorf("#%s has no non-empty title: %s", id, openTag)
		}
	}
}

func TestItemForm_CloseAndSaveAreIconLabelled(t *testing.T) {
	dialog := itemFormDialog(t, catalogPageBody(t))
	for _, c := range []struct{ id, icon string }{
		{"item-form-close-btn", "x"},
		{"item-form-submit", "check"},
	} {
		re := regexp.MustCompile(`(?s)<button[^>]*id="` + c.id + `"[^>]*>(.*?)</button>`)
		m := re.FindStringSubmatch(dialog)
		if len(m) < 2 {
			t.Fatalf("no #%s button found in the item form dialog", c.id)
		}
		if !strings.Contains(m[1], `data-icon="`+c.icon+`"`) {
			t.Errorf("#%s should render the %q icon, got: %q", c.id, c.icon, m[1])
		}
		if !strings.Contains(m[1], `class="btn-label"`) {
			t.Errorf("#%s should keep a visible .btn-label span for desktop/kiosk width, got: %q", c.id, m[1])
		}
	}
}

func TestItemForm_ResetIsTextOnlyNotDoubleIconed(t *testing.T) {
	// Independent review finding F2: pairing a "plus" icon with the
	// catalog.plus_new label (which every locale already renders as
	// "+ New"/"+ Yeni"/… — a baked-in "+") doubled the plus sign. +New
	// must render no icon span and no separate .btn-label span — the
	// button's own text is its label at every width.
	dialog := itemFormDialog(t, catalogPageBody(t))
	re := regexp.MustCompile(`(?s)<button[^>]*id="item-form-reset"[^>]*>(.*?)</button>`)
	m := re.FindStringSubmatch(dialog)
	if len(m) < 2 {
		t.Fatalf("no #item-form-reset button found in the item form dialog")
	}
	if strings.Contains(m[1], `class="btn-ico"`) {
		t.Errorf("#item-form-reset should not render an icon (its label already carries \"+\"), got: %q", m[1])
	}
	if strings.Contains(m[1], `class="btn-label"`) {
		t.Errorf("#item-form-reset should not wrap its text in .btn-label (it never goes icon-only), got: %q", m[1])
	}
}
