package catalog

// ut-docs#2211 (reopened): the item editor gets a top-level Modifiers tab,
// placed right after Variants, and EVERYTHING modifier-related leaves the
// Variants panel — the item's own groups, the #2330 attach/detach picker,
// the #2379 way out to /modifiers and the #2284 category-inherited groups
// all render inline in the new tab's panel (#item-modifiers-list), not in a
// nested dialog opened from the Variants tab. The first close-out of this
// card (universal-till#1201) renamed strings and never added the tab; these
// pin the shape so it can't regress to that again.

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// The tab strip reads Details · Variants · Modifiers · Item image · Labels ·
// Keypad, in that DOM order (the order keyboard arrows walk too, since
// focusTab walks querySelectorAll('[role=tab]')).
func TestItemForm_TabOrderHasModifiersAfterVariants_2211(t *testing.T) {
	dialog := itemFormDialog(t, catalogPageBody(t))
	want := []string{
		"item-form-tab-details",
		"item-form-tab-variants",
		"item-form-tab-modifiers",
		"item-form-tab-image",
		"item-form-tab-labels",
		"item-form-tab-keypad",
	}
	re := regexp.MustCompile(`<button[^>]*id="(item-form-tab-[a-z]+)"[^>]*role="tab"`)
	var got []string
	for _, m := range re.FindAllStringSubmatch(dialog, -1) {
		got = append(got, m[1])
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("item editor tab order:\n got %v\nwant %v", got, want)
	}

	tab := regexp.MustCompile(`<button[^>]*id="item-form-tab-modifiers"[^>]*>\s*Modifiers\s*</button>`).FindString(dialog)
	if tab == "" {
		t.Fatalf("#item-form-tab-modifiers must be labelled %q", "Modifiers")
	}
	for _, attr := range []string{`aria-controls="item-form-panel-modifiers"`, `@click="select('modifiers')"`, `:aria-selected="tab === 'modifiers'"`} {
		if !strings.Contains(tab, attr) {
			t.Errorf("#item-form-tab-modifiers missing %s (same Alpine roving-tabindex pattern as its siblings): %s", attr, tab)
		}
	}

	panel := regexp.MustCompile(`<div[^>]*id="item-form-panel-modifiers"[^>]*>`).FindString(dialog)
	if panel == "" {
		t.Fatalf("no #item-form-panel-modifiers tab panel inside the item form dialog")
	}
	for _, attr := range []string{`role="tabpanel"`, `aria-labelledby="item-form-tab-modifiers"`, `x-show="tab === 'modifiers'"`} {
		if !strings.Contains(panel, attr) {
			t.Errorf("#item-form-panel-modifiers missing %s: %s", attr, panel)
		}
	}
}

// panelSection returns one tab panel's markup, from its opening tag to the
// next tab panel's opening tag (panels are siblings in .catalog-form-body).
func panelSection(t *testing.T, dialog, id string) string {
	t.Helper()
	start := strings.Index(dialog, `id="`+id+`"`)
	if start < 0 {
		t.Fatalf("no #%s in the item form dialog", id)
	}
	rest := dialog[start+1:]
	end := strings.Index(rest, `class="tab-panel"`)
	if end < 0 {
		return dialog[start:]
	}
	return dialog[start : start+1+end]
}

// The Modifiers panel is where the item's modifier UI lives now: the inline
// list container the attach/detach/opt-out forms target, lazily filled by
// GET /api/catalog/modifier-groups-panel.
func TestItemForm_ModifiersPanelHostsInlineList_2211(t *testing.T) {
	dialog := itemFormDialog(t, catalogPageBody(t))
	panel := panelSection(t, dialog, "item-form-panel-modifiers")
	if !strings.Contains(panel, `id="item-modifiers-list"`) {
		t.Fatalf("#item-form-panel-modifiers must host #item-modifiers-list inline:\n%s", panel)
	}
	if !strings.Contains(panel, `class="muted item-form-needs-save"`) {
		t.Errorf("#item-form-panel-modifiers needs the create-mode 'save first' hint like its item-bound siblings")
	}
}

// Nothing modifier-related remains in the Variants tab or anywhere on the
// page outside the Modifiers panel: no Manage button, no nested dialog.
func TestItemForm_NoModifierControlsOutsideModifiersTab_2211(t *testing.T) {
	body := catalogPageBody(t)
	for _, gone := range []string{`id="modifier-groups-modal"`, `id="manage-modifiers-btn"`, `modifier-groups-modal-list`} {
		if strings.Contains(body, gone) {
			t.Errorf("/catalog still renders %s — the Modifiers tab replaces the nested dialog", gone)
		}
	}
	variants := panelSection(t, itemFormDialog(t, body), "item-form-panel-variants")
	for _, gone := range []string{"modifier", "Modifiers"} {
		if strings.Contains(variants, gone) {
			t.Errorf("Variants panel still mentions %q", gone)
		}
	}
}

// The Variants fragment itself (what openCatalogRow swaps in) carries no
// modifier controls either — even for an item with direct AND
// category-inherited groups.
func TestCatalogVariantsFragment_HasNoModifierUI_2211(t *testing.T) {
	mux, dbase := setupInheritDeps(t)
	ctx := context.Background()
	modRepo := data.NewModifierRepo(dbase.DB)
	if _, err := modRepo.CreateGroup(ctx, "gMilk", "Milk", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := modRepo.SetCategoryModifierGroups(ctx, "cat1", []string{"gMilk"}); err != nil {
		t.Fatal(err)
	}
	if rec := postPanel(t, mux, "/api/catalog/modifier-group", "panelItem=itm1&itemId=itm1&name=Extras&isActive=1&minSelect=0&maxSelect=2", "item-modifiers-list"); rec.Code != http.StatusOK {
		t.Fatalf("create group: %d %s", rec.Code, rec.Body.String())
	}
	rec := get(t, mux, "/api/catalog/item-variants?item_id=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("variants fragment: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, gone := range []string{"manage-modifiers-btn", "modifier-groups", "catalog-detail-modifiers", "Modifiers", "Extras", "Milk (from category)", "/api/catalog/modifier"} {
		if strings.Contains(body, gone) {
			t.Errorf("Variants fragment still contains %q", gone)
		}
	}
}

// The inline list answers — and every mutation targeting it re-renders —
// with the item's direct groups, the attach picker and the category's
// inherited groups, under the #item-modifiers-list container.
func TestItemModifiersList_RendersDirectAttachableAndInherited_2211(t *testing.T) {
	mux, dbase := setupInheritDeps(t)
	ctx := context.Background()
	modRepo := data.NewModifierRepo(dbase.DB)
	if _, err := modRepo.CreateGroup(ctx, "gMilk", "Milk", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := modRepo.SetCategoryModifierGroups(ctx, "cat1", []string{"gMilk"}); err != nil {
		t.Fatal(err)
	}
	if _, err := modRepo.CreateGroup(ctx, "gSyrup", "Syrup", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}

	rec := get(t, mux, "/api/catalog/modifier-groups-panel?item_id=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("panel: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.HasPrefix(strings.TrimSpace(body), `<div id="item-modifiers-list"`) {
		t.Fatalf("panel fragment must be rooted at #item-modifiers-list:\n%s", body)
	}
	if !strings.Contains(body, `hx-target="#item-modifiers-list"`) {
		t.Errorf("attach picker must target #item-modifiers-list")
	}
	if !strings.Contains(body, " Syrup</label>") {
		t.Errorf("unattached group Syrup must be offered in the attach picker")
	}
	if !strings.Contains(body, `data-inherited-group="gMilk"`) {
		t.Errorf("category-inherited group Milk must be listed as inherited")
	}

	rec = postPanel(t, mux, "/api/catalog/modifier-group/attach", "panelItem=itm1&itemId=itm1&groupId=gSyrup", "item-modifiers-list")
	if rec.Code != http.StatusOK {
		t.Fatalf("attach: %d %s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if !strings.Contains(body, `id="item-modifiers-list"`) {
		t.Fatalf("attach targeting #item-modifiers-list must re-render that list, got:\n%s", body)
	}
	if !strings.Contains(body, `class="modifier-admin-group-name">Syrup<`) {
		t.Fatalf("attached group must now be listed as the item's own:\n%s", body)
	}
}
