package catalog

// ut-docs#6125: the full-screen catalog dialogs sit BESIDE the nav rail and
// ABOVE the status bar (coding-standards §10, ut-docs#1999), and on a phone
// the item editor carries its own Lock + exit-to-OS inside the title line.
// These pin the server-rendered shape; dialog-lock-status-reachable-6125
// .spec.ts drives the geometry and the real logout in a browser.

import (
	"regexp"
	"strings"
	"testing"
)

func dialogOpenTag(t *testing.T, body, id string) string {
	t.Helper()
	re := regexp.MustCompile(`<dialog[^>]*id="` + id + `"[^>]*>`)
	m := re.FindString(body)
	if m == "" {
		t.Fatalf("no <dialog id=%q> on /catalog", id)
	}
	return m
}

func TestCatalogDialogs_SitBesideChrome(t *testing.T) {
	body := catalogPageBody(t)
	for _, id := range []string{"item-form-modal", "import-modal", "tax-codes-modal"} {
		if !strings.Contains(dialogOpenTag(t, body, id), "dialog-beside-chrome") {
			t.Errorf("#%s lacks the dialog-beside-chrome class (ut-docs#6125)", id)
		}
	}
	// UX-3: the two swapped-in page dialogs scroll with a pinned head.
	for _, id := range []string{"import-modal", "tax-codes-modal"} {
		if !strings.Contains(dialogOpenTag(t, body, id), "dialog-page-scroll") {
			t.Errorf("#%s lacks dialog-page-scroll: its content could not be scrolled", id)
		}
	}
	// ...but the item editor has its own head/body split.
	if strings.Contains(dialogOpenTag(t, body, "item-form-modal"), "dialog-page-scroll") {
		t.Errorf("#item-form-modal must not take dialog-page-scroll (own head/body split)")
	}
}

func TestItemForm_PhoneLockAndExitInTitleLine(t *testing.T) {
	dialog := itemFormDialog(t, catalogPageBody(t))
	titleStart := strings.Index(dialog, `class="catalog-form-head-title"`)
	h3 := strings.Index(dialog, `<h3 id="item-form-title"`)
	if titleStart < 0 || h3 < titleStart {
		t.Fatalf("cannot locate the item form title line")
	}
	line := dialog[titleStart:h3]
	if !strings.Contains(line, `dialog-status-inline`) {
		t.Fatalf("the Lock/exit controls are not the first children of .catalog-form-head-title (before the h3): %q", line)
	}
	lock := strings.Index(line, `action="/api/auth/logout"`)
	exit := strings.Index(line, `href="/settings#settings-display"`)
	if lock < 0 || exit < 0 || lock > exit {
		t.Errorf("want the Lock form (%d) before the exit-to-OS link (%d) in DOM order", lock, exit)
	}
	// Lock must never sit inside a main-till-lock fieldset (ut-docs#3605).
	if strings.Contains(line, "data-main-till-lock") {
		t.Errorf("Lock/exit must be outside every fieldset[data-main-till-lock]")
	}
	// No in-dialog connection chip (UX-1): the real status bar is uncovered.
	if strings.Contains(dialog, "data-record-dialog-conn") {
		t.Errorf("item form must not render its own connection chip (ut-docs#6125 UX-1)")
	}
	// UX-2: stray focus lands on a neutral home, never on Lock/exit.
	if !strings.Contains(dialog, `data-ut-focus-home`) {
		t.Errorf("item form has no data-ut-focus-home: stray focus would land on Lock")
	}
}
