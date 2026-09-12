package httpx

import (
	"strings"
	"testing"
)

// ut-docs#2119 — category_filter.html follows the same req-guard convention
// as record_dialog.html/list_header.html (ut-docs#2010's B2 finding: a
// misspelled/missing dict key must fail the render at execute time, naming
// the key, rather than silently shipping a broken control). Pinned against
// the REAL partial, per key — same style as record_dialog_partial_test.go's
// TestRecordDialog_MissingRequiredKeyFailsAtExecute.
func TestCategoryFilter_MissingRequiredKeyFailsAtExecute(t *testing.T) {
	tpl := parsePartialWithPage(t, "ui/partials/category_filter.html", "")

	type categoryNode struct{ ID, Name, ParentID string }
	full := map[string]any{
		"categories": []categoryNode{{ID: "cat1", Name: "Drinks"}},
		"controlID":  "catalog-category-filter",
	}
	var ok strings.Builder
	if err := tpl.ExecuteTemplate(&ok, "category_filter", full); err != nil {
		t.Fatalf("complete dict must render: %v", err)
	}
	if !strings.Contains(ok.String(), `id="catalog-category-filter"`) {
		t.Fatalf("complete dict did not render the wrapper id:\n%s", ok.String())
	}
	if !strings.Contains(ok.String(), `data-cat-id="cat1"`) {
		t.Fatalf("complete dict did not render the category chip:\n%s", ok.String())
	}

	for _, missing := range []string{"categories", "controlID"} {
		var out strings.Builder
		err := tpl.ExecuteTemplate(&out, "category_filter", without(full, missing))
		if err == nil {
			t.Errorf("without %q the chip row rendered %d bytes at execute time instead of failing", missing, out.Len())
			continue
		}
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("error for missing %q does not name it: %v", missing, err)
		}
	}

	// controlID present but empty is the same authoring bug (req's own
	// empty-string check).
	empty := make(map[string]any, len(full))
	for k, v := range full {
		empty[k] = v
	}
	empty["controlID"] = ""
	var out strings.Builder
	if err := tpl.ExecuteTemplate(&out, "category_filter", empty); err == nil {
		t.Errorf("empty controlID rendered %d bytes at execute time instead of failing", out.Len())
	} else if !strings.Contains(err.Error(), "controlID") {
		t.Errorf("error for empty controlID does not name it: %v", err)
	}
}

// A nil/missing "categories" dict entry (as opposed to an empty non-nil
// slice) must also fail the req guard, not be treated as "zero categories".
func TestCategoryFilter_NoDictFailsAtExecute(t *testing.T) {
	tpl := parsePartialWithPage(t, "ui/partials/category_filter.html", "")
	var out strings.Builder
	err := tpl.ExecuteTemplate(&out, "category_filter", nil)
	if err == nil {
		t.Fatalf("nil dict rendered %d bytes at execute time instead of failing", out.Len())
	}
	if !strings.Contains(err.Error(), "categories") {
		t.Fatalf("error for nil dict does not name the first required key: %v", err)
	}
}
