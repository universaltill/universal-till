package httpx

import (
	"html/template"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/web"
)

// ut-docs#2165 — category_filter_popover.html wraps category_filter.html
// (unchanged, ut-docs#2119) behind a collapsed trigger + <dialog> popover.
// Same req-guard convention as its sibling partials (ut-docs#2010's B2
// finding); this pins the wrapper's own markup, not the chip row itself
// (already covered by category_filter_partial_test.go).
func parseCategoryFilterPopover(t *testing.T) *template.Template {
	t.Helper()
	tpl := template.New("t").Funcs(FuncsFor("en"))
	for _, path := range []string{
		"ui/partials/category_filter.html",
		"ui/partials/category_filter_popover.html",
	} {
		src, err := web.FS.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if _, err := tpl.Parse(string(src)); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
	}
	return tpl
}

func TestCategoryFilterPopover_RendersTriggerAndDialog(t *testing.T) {
	tpl := parseCategoryFilterPopover(t)

	type categoryNode struct{ ID, Name, ParentID string }
	full := map[string]any{
		"categories": []categoryNode{{ID: "cat1", Name: "Drinks"}},
		"controlID":  "catalog-category-filter",
	}
	var out strings.Builder
	if err := tpl.ExecuteTemplate(&out, "category_filter_popover", full); err != nil {
		t.Fatalf("complete dict must render: %v", err)
	}
	body := out.String()

	// The trigger: icon-only, discloses the popover, starts closed, and
	// carries both the idle and active accessible labels for
	// category-filter.js's paintTriggerState() to switch between.
	if !strings.Contains(body, `id="catalog-category-filter-trigger"`) {
		t.Fatalf("expected the trigger button id; got:\n%s", body)
	}
	if !strings.Contains(body, `aria-haspopup="dialog"`) {
		t.Fatalf("expected aria-haspopup=dialog on the trigger; got:\n%s", body)
	}
	if !strings.Contains(body, `aria-controls="catalog-category-filter-dialog"`) {
		t.Fatalf("expected aria-controls pointing at the dialog id; got:\n%s", body)
	}
	if !strings.Contains(body, `aria-expanded="false"`) {
		t.Fatalf("expected the trigger to start collapsed (aria-expanded=false); got:\n%s", body)
	}
	if !strings.Contains(body, `data-label-idle=`) || !strings.Contains(body, `data-label-active=`) {
		t.Fatalf("expected both idle and active accessible-label data attributes; got:\n%s", body)
	}
	if !strings.Contains(body, `class="category-filter-badge"`) {
		t.Fatalf("expected the active-state badge element (hidden by default); got:\n%s", body)
	}
	// ut-docs#2178: a persistent aria-live region category-filter.js's
	// paintTriggerState writes the same active/idle label into, so a
	// screen reader gets a spoken confirmation when the filter changes
	// what's listed.
	if !strings.Contains(body, `id="catalog-category-filter-status"`) {
		t.Fatalf("expected the status live-region id; got:\n%s", body)
	}
	if !strings.Contains(body, `aria-live="polite"`) || !strings.Contains(body, `data-category-filter-status`) {
		t.Fatalf("expected an aria-live=\"polite\" region marked data-category-filter-status; got:\n%s", body)
	}

	// The dialog: closed by default (no `open` attribute), labelled, and
	// holding a close control.
	if !strings.Contains(body, `id="catalog-category-filter-dialog"`) {
		t.Fatalf("expected the dialog id; got:\n%s", body)
	}
	// The `open` attribute is what makes a <dialog> visible/interactive at
	// all — bindPopover()'s dialog.show() adds it at runtime, so the
	// server-rendered markup must never carry it up front.
	if strings.Contains(body, "data-category-filter-dialog open") || strings.Contains(body, "data-category-filter-dialog\nopen") {
		t.Fatalf("dialog must NOT start open; got:\n%s", body)
	}
	if !strings.Contains(body, `aria-labelledby="catalog-category-filter-dialog-title"`) {
		t.Fatalf("expected the dialog to be labelled; got:\n%s", body)
	}
	if !strings.Contains(body, `data-category-filter-close`) {
		t.Fatalf("expected a close control inside the popover; got:\n%s", body)
	}

	// The unchanged chip row (category_filter.html, ut-docs#2119) still
	// nests inside, with the SAME controlID the call site passed — so
	// category-filter.js's existing getElementById(controlID) lookup and
	// category_filter_2119_test.go's own assertions need no change.
	if !strings.Contains(body, `id="catalog-category-filter" role="group"`) {
		t.Fatalf("expected the original chip row nested inside, same id; got:\n%s", body)
	}
	if !strings.Contains(body, `data-cat-id="cat1"`) {
		t.Fatalf("expected the category chip itself to still render; got:\n%s", body)
	}
}

func TestCategoryFilterPopover_MissingRequiredKeyFailsAtExecute(t *testing.T) {
	tpl := parseCategoryFilterPopover(t)

	type categoryNode struct{ ID, Name, ParentID string }
	full := map[string]any{
		"categories": []categoryNode{{ID: "cat1", Name: "Drinks"}},
		"controlID":  "catalog-category-filter",
	}
	for _, missing := range []string{"categories", "controlID"} {
		var out strings.Builder
		err := tpl.ExecuteTemplate(&out, "category_filter_popover", without(full, missing))
		if err == nil {
			t.Errorf("without %q the popover rendered %d bytes at execute time instead of failing", missing, out.Len())
			continue
		}
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("error for missing %q does not name it: %v", missing, err)
		}
	}
}
