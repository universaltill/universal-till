package catalog

// ut-docs#2727: Remove / Attach / Skip-Use-again swap #item-modifiers-list
// (outerHTML). htmx 1.9.12 restores focus to the new element with the SAME
// id as the focused one, so those controls need stable ids — and a keyboard
// user needs a heading to land on when the focused control no longer exists.

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

func focusPanel(t *testing.T, optOut bool) string {
	t.Helper()
	mux, dbase := setupInheritDeps(t)
	ctx := context.Background()
	modRepo := data.NewModifierRepo(dbase.DB)
	for _, g := range []struct{ id, name string }{{"gMilk", "Milk"}, {"gOwn", "Extras"}, {"gSyrup", "Syrup"}} {
		if _, err := modRepo.CreateGroup(ctx, g.id, g.name, false, 0, 1, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := modRepo.SetCategoryModifierGroups(ctx, "cat1", []string{"gMilk"}); err != nil {
		t.Fatal(err)
	}
	if rec := postPanel(t, mux, "/api/catalog/modifier-group/attach", "panelItem=itm1&itemId=itm1&groupId=gOwn", "item-modifiers-list"); rec.Code != 200 {
		t.Fatalf("attach: %d %s", rec.Code, rec.Body.String())
	}
	if optOut {
		if rec := postPanel(t, mux, "/api/catalog/modifier-group/opt-out", "panelItem=itm1&itemId=itm1&groupId=gMilk", "item-modifiers-list"); rec.Code != 200 {
			t.Fatalf("opt-out: %d %s", rec.Code, rec.Body.String())
		}
	}
	rec := get(t, mux, "/api/catalog/modifier-groups-panel?item_id=itm1")
	if rec.Code != 200 {
		t.Fatalf("panel: %d", rec.Code)
	}
	return rec.Body.String()
}

// tagWith returns the opening tag of the element carrying id.
func tagWith(body, id string) string {
	return regexp.MustCompile(`<[a-z]+[^>]*\bid="` + regexp.QuoteMeta(id) + `"[^>]*>`).FindString(body)
}

func TestItemModifiersPanel_StableFocusIDs_2727(t *testing.T) {
	for _, optOut := range []bool{false, true} {
		body := focusPanel(t, optOut)
		name := map[bool]string{false: "opted-in", true: "opted-out"}[optOut]

		if tag := tagWith(body, "modifier-detach-gOwn"); !strings.Contains(tag, "<button") || !strings.Contains(tag, "/api/catalog/modifier-group/detach") {
			t.Errorf("%s: detach button needs id=modifier-detach-<group>: %q", name, tag)
		}
		// One id for both states, so Skip -> Use again keeps focus.
		tog := tagWith(body, "modifier-inherited-toggle-gMilk")
		wantPath := map[bool]string{false: "/opt-out", true: "/opt-in"}[optOut]
		if !strings.Contains(tog, "<button") || !strings.Contains(tog, wantPath) {
			t.Errorf("%s: inherited toggle needs id=modifier-inherited-toggle-<group> posting %s: %q", name, wantPath, tog)
		}
		if n := strings.Count(body, `id="modifier-inherited-toggle-gMilk"`); n != 1 {
			t.Errorf("%s: toggle id appears %d times", name, n)
		}
		if tag := tagWith(body, "modifier-attach-submit"); !strings.Contains(tag, `type="submit"`) {
			t.Errorf("%s: attach submit needs id=modifier-attach-submit: %q", name, tag)
		}
		head := tagWith(body, "item-modifiers-heading")
		if !strings.Contains(head, `tabindex="-1"`) {
			t.Errorf("%s: heading #item-modifiers-heading needs tabindex=-1: %q", name, head)
		}
		if i := strings.Index(body, `id="item-modifiers-heading"`); i < 0 || !strings.Contains(body[i:], "Modifiers") {
			t.Errorf("%s: heading must be the catalog.modifiers.title label", name)
		}
	}
}

// With nothing left to attach the submit button is gone; the heading is the
// fallback focus target and must still render.
func TestItemModifiersPanel_HeadingRendersWithoutAttachForm_2727(t *testing.T) {
	mux, _ := setupInheritDeps(t)
	body := get(t, mux, "/api/catalog/modifier-groups-panel?item_id=itm1").Body.String()
	if strings.Contains(body, "modifier-attach-submit") {
		t.Fatalf("no attachable groups: attach submit must not render")
	}
	if !strings.Contains(tagWith(body, "item-modifiers-heading"), `tabindex="-1"`) {
		t.Fatalf("heading missing:\n%s", body)
	}
}
