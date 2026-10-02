package catalog

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// GET /catalog/option-sets (ut-docs#1900) is the shop-wide screen where a
// merchant creates a reusable option set ("Size") and its values once; the
// item panel below then applies it to any item. An empty shop must say what
// to do next, not render a blank list.
func TestOptionSetsPage_EmptyStateThenCreateSetAndValues(t *testing.T) {
	mux, db := newCatalogMux(t)

	rec := get(t, mux, "/catalog/option-sets")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "No option sets yet") {
		t.Fatalf("empty shop must show the empty-state hint, got: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/api/catalog/option-set") {
		t.Fatal("page missing the add-option-set form")
	}

	rec = postForm(t, mux, "/api/catalog/option-set", "name=Size")
	if rec.Code != http.StatusOK {
		t.Fatalf("create set: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Size") {
		t.Fatal("response after creating a set must list it")
	}
	var setID string
	if err := db.QueryRow(`SELECT id FROM option_sets WHERE name = 'Size'`).Scan(&setID); err != nil {
		t.Fatalf("set not persisted: %v", err)
	}

	for _, v := range []string{"S", "M", "L"} {
		rec = postForm(t, mux, "/api/catalog/option-set-value", "optionSetId="+setID+"&value="+v)
		if rec.Code != http.StatusOK {
			t.Fatalf("add value %q: want 200, got %d: %s", v, rec.Code, rec.Body.String())
		}
	}
	// The list re-render carries the values as chips in insertion order
	// (each chip's text is an in-place edit field since ut-docs#3319).
	body := rec.Body.String()
	iS, iM, iL := strings.Index(body, `value="S"`), strings.Index(body, `value="M"`), strings.Index(body, `value="L"`)
	if iS < 0 || iM < 0 || iL < 0 || !(iS < iM && iM < iL) {
		t.Fatalf("values must render as chips in insertion order S, M, L; got: %s", body)
	}
	if strings.Contains(body, "No option sets yet") {
		t.Fatal("empty-state hint must disappear once a set exists")
	}

	// The full page lists it too.
	rec = get(t, mux, "/catalog/option-sets")
	if !strings.Contains(rec.Body.String(), "Size") || !strings.Contains(rec.Body.String(), `value="M"`) {
		t.Fatalf("page must list the set and its values, got: %s", rec.Body.String())
	}
}

func TestOptionSetsPage_DuplicateNameAndBlankValueAreActionable400s(t *testing.T) {
	mux, _ := newCatalogMux(t)
	if rec := postForm(t, mux, "/api/catalog/option-set", "name=Size"); rec.Code != http.StatusOK {
		t.Fatalf("first create: %d %s", rec.Code, rec.Body.String())
	}
	rec := postForm(t, mux, "/api/catalog/option-set", "name=Size")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate set name: want 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "already in use") {
		t.Fatalf("duplicate set name must say so, got: %s", rec.Body.String())
	}
	if rec := postForm(t, mux, "/api/catalog/option-set", "name="); rec.Code != http.StatusBadRequest {
		t.Fatalf("blank set name: want 400, got %d", rec.Code)
	}
	if rec := postForm(t, mux, "/api/catalog/option-set-value", "optionSetId=nope&value="); rec.Code != http.StatusBadRequest {
		t.Fatalf("blank value: want 400, got %d", rec.Code)
	}
}

// The item panel: apply a set, generate, and the panel re-renders with the
// new rows plus a "N variants created" confirmation. Re-running says 0.
func TestCatalogVariantsPanel_ApplyOptionSetsAndGenerate(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "TEE", Name: "T-shirt", BasePrice: 1500, IsActive: true})
	repo := data.NewOptionSetRepo(db)
	sizeID, err := repo.CreateOptionSet(t.Context(), "Size")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"S", "M", "L"} {
		if _, err := repo.AddOptionSetValue(t.Context(), sizeID, v); err != nil {
			t.Fatal(err)
		}
	}

	// Panel shows the apply row with the set as a checkbox and the button.
	rec := get(t, mux, "/api/catalog/item-variants?item_id=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("panel: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"/api/catalog/item/option-sets", "/api/catalog/item/generate-variants", `value="` + sizeID + `"`, "Generate variants"} {
		if !strings.Contains(body, want) {
			t.Fatalf("panel missing %q, got: %s", want, body)
		}
	}

	rec = postForm(t, mux, "/api/catalog/item/option-sets", "panelItem=itm1&optionSetIds="+sizeID)
	if rec.Code != http.StatusOK {
		t.Fatalf("apply: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `value="`+sizeID+`" checked`) {
		t.Fatalf("applied set must render checked, got: %s", rec.Body.String())
	}

	rec = postForm(t, mux, "/api/catalog/item/generate-variants", "panelItem=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("generate: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if !strings.Contains(body, "3 variant(s) created") {
		t.Fatalf("generate must confirm the count, got: %s", body)
	}
	for _, name := range []string{`value="S"`, `value="M"`, `value="L"`} {
		if !strings.Contains(body, name) {
			t.Fatalf("panel after generate missing variant row %s, got: %s", name, body)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM item_variants WHERE item_id = 'itm1' AND sku IS NOT NULL AND sku != ''`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("want 3 generated variants each with a SKU, got %d", n)
	}

	// Re-run: safe no-op, reported as 0.
	rec = postForm(t, mux, "/api/catalog/item/generate-variants", "panelItem=itm1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "0 variant(s) created") {
		t.Fatalf("re-run must report 0 created, got %d: %s", rec.Code, rec.Body.String())
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM item_variants WHERE item_id = 'itm1'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("re-run must not add rows, count=%d err=%v", n, err)
	}
}

func TestCatalogVariantsPanel_ApplyThreeOptionSetsIs400(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "TEE", Name: "T-shirt", BasePrice: 1500, IsActive: true})
	repo := data.NewOptionSetRepo(db)
	var ids []string
	for _, n := range []string{"Size", "Colour", "Fit"} {
		id, err := repo.CreateOptionSet(t.Context(), n)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rec := postForm(t, mux, "/api/catalog/item/option-sets", "panelItem=itm1&optionSetIds="+ids[0]+"&optionSetIds="+ids[1]+"&optionSetIds="+ids[2])
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("three sets: want 400, got %d: %s", rec.Code, rec.Body.String())
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM item_option_sets WHERE item_id = 'itm1'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("nothing must be applied on a rejected request, count=%d err=%v", n, err)
	}
}

func TestCatalogVariantsPanel_GenerateWithoutAppliedSetsShowsHint(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "TEE", Name: "T-shirt", BasePrice: 1500, IsActive: true})
	rec := postForm(t, mux, "/api/catalog/item/generate-variants", "panelItem=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 (panel re-render with a hint), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Apply at least one option set first") {
		t.Fatalf("must tell the operator to apply a set first, got: %s", rec.Body.String())
	}
	// And with no sets in the shop at all, the panel points at the
	// option-sets screen rather than rendering an empty checkbox row.
	rec = get(t, mux, "/api/catalog/item-variants?item_id=itm1")
	if !strings.Contains(rec.Body.String(), `href="/catalog/option-sets"`) {
		t.Fatalf("panel with no option sets in the shop must link to /catalog/option-sets, got: %s", rec.Body.String())
	}
}

// option_sets & co. are synced shop-wide (sync_admin_repo.go's adminTables),
// so every mutation refuses on a replica up front — same pattern as
// TestCatalogModifiersPanel_MutationsRefusedOnReplica.
func TestOptionSets_MutationsRefusedOnReplica(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "TEE", Name: "T-shirt", BasePrice: 1500, IsActive: true})
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("create settings table: %v", err)
	}
	st := settings.NewStore(db)
	if err := st.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("set primary_url: %v", err)
	}
	setID, err := data.NewOptionSetRepo(db).CreateOptionSet(t.Context(), "Size")
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}, Settings: st})

	for _, tc := range []struct{ path, form string }{
		{"/api/catalog/option-set", "name=Colour"},
		{"/api/catalog/option-set-value", "optionSetId=" + setID + "&value=S"},
		{"/api/catalog/item/option-sets", "panelItem=itm1&optionSetIds=" + setID},
		{"/api/catalog/item/generate-variants", "panelItem=itm1"},
		{"/api/catalog/option-set/rename", "optionSetId=" + setID + "&name=Sizes"},
		{"/api/catalog/option-set/active", "optionSetId=" + setID + "&isActive=0"},
		{"/api/catalog/option-set/delete", "optionSetId=" + setID},
		{"/api/catalog/option-set-value/update", "optionSetId=" + setID + "&valueId=x&value=S"},
		{"/api/catalog/option-set-value/move", "optionSetId=" + setID + "&valueId=x&direction=up"},
		{"/api/catalog/option-set-value/remove", "optionSetId=" + setID + "&valueId=x"},
	} {
		rec := postForm(t, mux, tc.path, tc.form)
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s on replica: want 409, got %d: %s", tc.path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "primary till") {
			t.Fatalf("%s on replica: body missing the localized replica message, got %q", tc.path, rec.Body.String())
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM option_sets`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("no option set may be created on a replica, count=%d err=%v", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM option_set_values`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("no value may be added on a replica, count=%d err=%v", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM item_variants`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("no variant may be generated on a replica, count=%d err=%v", n, err)
	}
}

// ut-docs#2092 superseded the ut-docs#1899 placement this test used to pin:
// Option sets (and Modifiers) duplicated the /items left rail exactly once
// ut-docs#1950 promoted both to rail sections, so the top action row drops
// them entirely rather than keeping a second way to reach the same screen.
// Rail reachability is covered elsewhere (items_page_test.go,
// items_panel_test.go); this asserts the row itself no longer offers a
// second, redundant path.
func TestCatalogPage_TopRowHasNoRailDuplicateButtons(t *testing.T) {
	mux, _ := newCatalogMux(t)
	rec := get(t, mux, "/catalog")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	// Scoped to the top action row itself, not the whole page — catalog
	// page's own item-detail panel (catalog_variants.html) legitimately
	// carries its own href="/catalog/option-sets" link elsewhere on the
	// page (the "apply an option set" flow), and a whole-body substring
	// check would wrongly blame the top row if that link ever starts
	// rendering on a bare GET /catalog.
	start := strings.Index(body, `class="page-head`)
	if start < 0 {
		t.Fatal("no .page-head found on /catalog")
	}
	end := strings.Index(body[start:], `<dialog id="barcode-backfill-modal"`)
	if end < 0 {
		t.Fatal("could not find the end of the top action row (#barcode-backfill-modal marker)")
	}
	topRow := body[start : start+end]
	if strings.Contains(topRow, `href="/catalog/option-sets"`) {
		t.Fatal("catalog page's top action row must not link to /catalog/option-sets — it duplicates the /items rail section (ut-docs#2092)")
	}
	if strings.Contains(topRow, `href="/modifiers"`) {
		t.Fatal("catalog page's top action row must not link to /modifiers — it duplicates the /items rail section (ut-docs#2092)")
	}
}

// --- manage parity (ut-docs#3319): rename, edit/reorder/remove a value,
// active flag, delete-when-unused — all on the till's own screen. ---

func seedSizeSet(t *testing.T, db *sql.DB) (setID string, valueIDs []string) {
	t.Helper()
	repo := data.NewOptionSetRepo(db)
	setID, err := repo.CreateOptionSet(t.Context(), "Size")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"S", "M", "L"} {
		id, err := repo.AddOptionSetValue(t.Context(), setID, v)
		if err != nil {
			t.Fatal(err)
		}
		valueIDs = append(valueIDs, id)
	}
	return setID, valueIDs
}

func valueOrder(t *testing.T, db *sql.DB, setID string) string {
	t.Helper()
	set, err := data.NewOptionSetRepo(db).GetOptionSet(t.Context(), setID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, v := range set.Values {
		out = append(out, v.Value)
	}
	return strings.Join(out, ",")
}

func TestOptionSetsPage_RenameAndCollisionShownInline(t *testing.T) {
	mux, db := newCatalogMux(t)
	setID, _ := seedSizeSet(t, db)
	if _, err := data.NewOptionSetRepo(db).CreateOptionSet(t.Context(), "Colour"); err != nil {
		t.Fatal(err)
	}

	rec := get(t, mux, "/catalog/option-sets")
	if !strings.Contains(rec.Body.String(), `hx-post="/api/catalog/option-set/rename"`) {
		t.Fatalf("page must offer a rename form, got: %s", rec.Body.String())
	}

	rec = postForm(t, mux, "/api/catalog/option-set/rename", "optionSetId="+setID+"&name=Sizes")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `value="Sizes"`) {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
	}

	rec = postForm(t, mux, "/api/catalog/option-set/rename", "optionSetId="+setID+"&name=Colour")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("rename collision: want 400, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// An HTML fragment (app.js force-swaps a text/html 4xx into the target)
	// with the problem named on the card, not a generic banner.
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") || !strings.Contains(body, `id="option-sets-list"`) ||
		!strings.Contains(body, `role="alert"`) || !strings.Contains(body, "already in use") {
		t.Fatalf("collision must re-render the list with an inline alert, got %q: %s", rec.Header().Get("Content-Type"), body)
	}
	if set, _ := data.NewOptionSetRepo(db).GetOptionSet(t.Context(), setID); set.Name != "Sizes" {
		t.Fatalf("refused rename changed the name to %q", set.Name)
	}
	if rec := postForm(t, mux, "/api/catalog/option-set/rename", "optionSetId="+setID+"&name=+"); rec.Code != http.StatusBadRequest {
		t.Fatalf("blank rename: want 400, got %d", rec.Code)
	}
	if rec := postForm(t, mux, "/api/catalog/option-set/rename", "optionSetId=nope&name=Fit"); rec.Code != http.StatusNotFound {
		t.Fatalf("rename of an unknown set: want 404, got %d", rec.Code)
	}
}

// Reorder is explicit up/down buttons, never a drag gesture: on the kiosk
// touchscreen a pointerdown/drag handler can eat the page's scroll (UX note
// on ut-docs#3319).
func TestOptionSetsPage_EditMoveRemoveValue(t *testing.T) {
	mux, db := newCatalogMux(t)
	setID, ids := seedSizeSet(t, db)
	sID, mID, lID := ids[0], ids[1], ids[2]

	body := get(t, mux, "/catalog/option-sets").Body.String()
	for _, want := range []string{
		`hx-post="/api/catalog/option-set-value/move"`, `hx-post="/api/catalog/option-set-value/remove"`,
		`hx-post="/api/catalog/option-set-value/update"`, `aria-label="Move S up"`, `aria-label="Move S down"`, `aria-label="Remove S"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("page missing %q, got: %s", want, body)
		}
	}
	rec := postForm(t, mux, "/api/catalog/option-set-value/move", "optionSetId="+setID+"&valueId="+lID+"&direction=up")
	if rec.Code != http.StatusOK || valueOrder(t, db, setID) != "S,L,M" {
		t.Fatalf("move L up: %d order=%s", rec.Code, valueOrder(t, db, setID))
	}
	// Scoped to the list fragment (the app shell's own scripts are not this
	// screen's): no drag gesture of its own.
	for _, banned := range []string{"draggable", "pointerdown", "dragstart", "<script"} {
		if strings.Contains(rec.Body.String(), banned) {
			t.Fatalf("option-sets list must not use a drag gesture (%q found)", banned)
		}
	}
	// Moving the first value up is a harmless no-op.
	if rec := postForm(t, mux, "/api/catalog/option-set-value/move", "optionSetId="+setID+"&valueId="+sID+"&direction=up"); rec.Code != http.StatusOK || valueOrder(t, db, setID) != "S,L,M" {
		t.Fatalf("move first up: %d order=%s", rec.Code, valueOrder(t, db, setID))
	}
	if rec := postForm(t, mux, "/api/catalog/option-set-value/move", "optionSetId="+setID+"&valueId="+sID+"&direction=down"); rec.Code != http.StatusOK || valueOrder(t, db, setID) != "L,S,M" {
		t.Fatalf("move S down: %d order=%s", rec.Code, valueOrder(t, db, setID))
	}
	if rec := postForm(t, mux, "/api/catalog/option-set-value/move", "optionSetId="+setID+"&valueId="+sID+"&direction=sideways"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad direction: want 400, got %d", rec.Code)
	}

	rec = postForm(t, mux, "/api/catalog/option-set-value/update", "optionSetId="+setID+"&valueId="+mID+"&value=Medium")
	if rec.Code != http.StatusOK || valueOrder(t, db, setID) != "L,S,Medium" {
		t.Fatalf("edit M: %d order=%s", rec.Code, valueOrder(t, db, setID))
	}
	rec = postForm(t, mux, "/api/catalog/option-set-value/update", "optionSetId="+setID+"&valueId="+mID+"&value=S")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `role="alert"`) || !strings.Contains(rec.Body.String(), "already in this set") {
		t.Fatalf("duplicate value: want 400 with an inline alert, got %d: %s", rec.Code, rec.Body.String())
	}
	if valueOrder(t, db, setID) != "L,S,Medium" {
		t.Fatalf("refused edit changed values: %s", valueOrder(t, db, setID))
	}

	rec = postForm(t, mux, "/api/catalog/option-set-value/remove", "optionSetId="+setID+"&valueId="+lID)
	if rec.Code != http.StatusOK || valueOrder(t, db, setID) != "S,Medium" {
		t.Fatalf("remove L: %d order=%s", rec.Code, valueOrder(t, db, setID))
	}
	if rec := postForm(t, mux, "/api/catalog/option-set-value/remove", "optionSetId="+setID+"&valueId=nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("remove unknown value: want 404, got %d", rec.Code)
	}
}

// The active state is a text label, not colour/opacity alone.
func TestOptionSetsPage_ActiveToggleHasTextLabel(t *testing.T) {
	mux, db := newCatalogMux(t)
	setID, _ := seedSizeSet(t, db)
	rec := postForm(t, mux, "/api/catalog/option-set/active", "optionSetId="+setID+"&isActive=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("deactivate: %d %s", rec.Code, rec.Body.String())
	}
	if set, _ := data.NewOptionSetRepo(db).GetOptionSet(t.Context(), setID); set.IsActive {
		t.Fatal("set must be inactive")
	}
	if !strings.Contains(rec.Body.String(), `class="tag option-set-status"`) || !strings.Contains(rec.Body.String(), ">Inactive<") {
		t.Fatalf("an inactive set must say so in text, got: %s", rec.Body.String())
	}
	if rec := postForm(t, mux, "/api/catalog/option-set/active", "optionSetId="+setID+"&isActive=1"); rec.Code != http.StatusOK {
		t.Fatalf("reactivate: %d", rec.Code)
	}
	if set, _ := data.NewOptionSetRepo(db).GetOptionSet(t.Context(), setID); !set.IsActive {
		t.Fatal("set must be active again")
	}
	if rec := postForm(t, mux, "/api/catalog/option-set/active", "optionSetId="+setID+"&isActive=maybe"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad isActive: want 400, got %d", rec.Code)
	}
}

// Delete is offered only when no item uses the set; otherwise the items
// are listed inline, visible without clicking anything, and the delete
// control is disabled and says why. A stale page posting delete anyway is
// refused with the item list.
func TestOptionSetsPage_DeleteBlockedListsItemsInlineThenSucceedsWhenUnused(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "TEE", Name: "T-shirt", BasePrice: 1500, IsActive: true})
	setID, _ := seedSizeSet(t, db)
	repo := data.NewOptionSetRepo(db)
	if err := repo.ApplyOptionSetsToItem(t.Context(), "itm1", []string{setID}); err != nil {
		t.Fatal(err)
	}

	body := get(t, mux, "/catalog/option-sets").Body.String()
	for _, want := range []string{"Used by", `href="/catalog?item=itm1&amp;tab=variants"`, ">T-shirt<", "Remove it from these items before you can delete it"} {
		if !strings.Contains(body, want) {
			t.Fatalf("in-use set must list its items inline (%q missing): %s", want, body)
		}
	}
	if !strings.Contains(body, `class="btn secondary option-set-delete-btn" disabled aria-describedby="option-set-usedby-`+setID+`"`) {
		t.Fatalf("delete must be disabled and point at the used-by explanation: %s", body)
	}

	rec := postForm(t, mux, "/api/catalog/option-set/delete", "optionSetId="+setID)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `role="alert"`) || !strings.Contains(rec.Body.String(), "T-shirt") {
		t.Fatalf("in-use delete: want 409 naming the item inline, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := repo.GetOptionSet(t.Context(), setID); err != nil {
		t.Fatalf("refused delete removed the set: %v", err)
	}

	if err := repo.ApplyOptionSetsToItem(t.Context(), "itm1", nil); err != nil {
		t.Fatal(err)
	}
	body = get(t, mux, "/catalog/option-sets").Body.String()
	if strings.Contains(body, "Used by") || !strings.Contains(body, `hx-post="/api/catalog/option-set/delete"`) || !strings.Contains(body, "hx-confirm=") {
		t.Fatalf("an unused set offers a confirmed delete and no used-by list: %s", body)
	}
	rec = postForm(t, mux, "/api/catalog/option-set/delete", "optionSetId="+setID)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "No option sets yet") {
		t.Fatalf("unused delete: want 200 and the empty state back, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := repo.GetOptionSet(t.Context(), setID); !errors.Is(err, data.ErrOptionSetNotFound) {
		t.Fatalf("set must be gone, got %v", err)
	}
	// A replay (double click, second tab) is harmless.
	if rec := postForm(t, mux, "/api/catalog/option-set/delete", "optionSetId="+setID); rec.Code != http.StatusOK {
		t.Fatalf("delete replay: want 200, got %d", rec.Code)
	}
}
