package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#2379: a shop with ZERO modifier groups anywhere in the catalog
// (nothing directly linked, nothing attachable, nothing inherited from a
// category) had no discoverable way out of the item-editor's nested "Manage
// customization groups" dialog to reach /modifiers — the only place a group
// can be created (ADR-0101 made this dialog attach-only). The dialog only
// ever showed the neutral "No Modifiers yet." with no control of any kind.
//
// #modifier-empty-goto-modifiers is the new control: only rendered when
// ModifierGroups, AttachableGroups AND InheritedGroups are all empty — the
// genuinely-stuck case the card describes, not merely "nothing attachable
// right now" (which the existing attach-picker's own {{ if .AttachableGroups }}
// guard already handles by simply omitting the attach form). Its guarded
// same-tab navigation (never target="_blank"/window.open — the kiosk build
// is fully chromeless, catalog_variants.html's own header comment) lives in
// catalog.html's inline script (window.utCatalogGoToModifiers), covered by
// e2e rather than this Go-side test — this file only proves the server-side
// condition that shows/hides the control.
func TestModifierGroupsPanel_GET_ZeroGroupsAnywhere_ShowsGoToModifiersLink(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/modifier-groups-panel?item_id=itm1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No Modifiers yet.") {
		t.Fatalf("expected the existing empty-state message to still render, got: %s", body)
	}
	if !strings.Contains(body, `id="modifier-empty-goto-modifiers"`) {
		t.Fatalf("expected the new go-to-Modifiers control when the shop has zero groups anywhere, got: %s", body)
	}
	if strings.Contains(body, `target="_blank"`) || strings.Contains(body, "window.open") {
		t.Fatal("the go-to-Modifiers control must never use page navigation the kiosk (chromeless) build can't land (target=_blank/window.open)")
	}
}

// The SAME shop, but with a group that exists elsewhere and is therefore
// attachable to itm1 — the dialog already offers a real path forward (the
// attach picker), so the new control must NOT also appear; two competing
// "go create/attach a group" affordances in the same empty-ish dialog would
// be confusing, and the card's own scope is specifically the "nothing at
// all" case.
func TestModifierGroupsPanel_GET_AttachableGroupExists_HidesGoToModifiersLink(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm2", SKU: "TEA", Name: "Tea", BasePrice: 250, IsActive: true})

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	form := "panelItem=itm2&itemId=itm2&name=Milk&isActive=1&minSelect=0&maxSelect=1"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), req)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/modifier-groups-panel?item_id=itm1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="modifier-attach-option"`) {
		t.Fatalf("expected the attach picker to offer itm2's group, got: %s", body)
	}
	if strings.Contains(body, `id="modifier-empty-goto-modifiers"`) {
		t.Fatal("must not show the go-to-Modifiers control when an attachable group already offers a path forward")
	}
}

// Independent-review finding (ut-docs#2379): the first two tests in this
// file both happen to pass against a WEAKER condition than the template
// actually needs (e.g. `{{ if not .AttachableGroups }}` alone) — the first
// leaves AttachableGroups empty for an unrelated reason (no other item
// exists at all) and the second only exercises AttachableGroups directly.
// Neither discriminates ModifierGroups or InheritedGroups on their own.
// This test closes that gap for ModifierGroups: itm1 has a group DIRECTLY
// linked to itself and is the only item in the shop, so ModifierGroups is
// non-empty while AttachableGroups/InheritedGroups are empty for
// unrelated, legitimate reasons (nothing else exists to attach; no
// category is even wired into this DB). A `not .AttachableGroups`-only
// condition would incorrectly show the control here too.
func TestModifierGroupsPanel_GET_OwnGroupOnly_HidesGoToModifiersLink(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	form := "panelItem=itm1&itemId=itm1&name=Extras&isActive=1&minSelect=0&maxSelect=2"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), req)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/modifier-groups-panel?item_id=itm1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="modifier-admin-group-name">Extras<`) {
		t.Fatalf("expected itm1's own directly-linked group to render, got: %s", body)
	}
	if strings.Contains(body, `id="modifier-empty-goto-modifiers"`) {
		t.Fatal("must not show the go-to-Modifiers control when the item already has a directly-linked group")
	}
}

// A second real path to the same "must stay hidden" outcome: itm1 inherits
// a group from its category (ADR-0094) instead of having one directly
// linked. Note this does NOT further discriminate InheritedGroups on its
// own beyond what the attachable-group test above already covers — under
// the repo's current semantics ListAttachableModifierGroups excludes only
// DIRECTLY-linked groups, so any group visible in InheritedGroups (which
// itself already excludes direct links, see renderItemModifierGroupsPanel's
// ownIDs filter) is necessarily also present in AttachableGroups. Kept as
// its own test anyway because it locks in real, user-reachable behaviour
// (a category-linked shop must not show the button), not as a distinct
// branch of the condition.
func TestModifierGroupsPanel_GET_InheritedGroupOnly_HidesGoToModifiersLink(t *testing.T) {
	mux, dbase := setupInheritDeps(t)
	ctx := context.Background()
	modRepo := data.NewModifierRepo(dbase.DB)
	if _, err := modRepo.CreateGroup(ctx, "gMilk", "Milk", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := modRepo.SetCategoryModifierGroups(ctx, "cat1", []string{"gMilk"}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/modifier-groups-panel?item_id=itm1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-inherited-group="gMilk"`) {
		t.Fatalf("expected itm1 to show gMilk as inherited from its category, got: %s", body)
	}
	if strings.Contains(body, `id="modifier-empty-goto-modifiers"`) {
		t.Fatal("must not show the go-to-Modifiers control when the item already inherits a group from its category")
	}
}
