package pages

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// seedCategorizedButtons seeds one item + one shortcut button per (code,
// categoryID) pair, IN THE GIVEN ORDER (sort_order 0, 1, 2, ...) — the
// shape TestButtonsMove_WithinSameCategoryOnly's own docstring (A(cat1),
// B(cat2), C(cat1)) and its siblings below all need. Distinct real
// categories rows are inserted first: items.category_id carries a real FK
// under the migrated schema openPagesTestDB now runs (ut-docs#1676/#1677),
// so a category must exist before an item can reference it — unlike
// buttons_reorder_test.go's own minimal fixture, which never sets
// category_id at all and so never needed one.
func seedCategorizedButtons(t *testing.T, db *sql.DB, rows [][2]string) {
	t.Helper()
	cats := map[string]bool{}
	for _, r := range rows {
		cat := r[1]
		if cat == "" || cats[cat] {
			continue
		}
		cats[cat] = true
		if _, err := db.Exec(`INSERT INTO categories(id,name) VALUES(?,?)`, cat, cat); err != nil {
			t.Fatalf("seed category %s: %v", cat, err)
		}
	}
	for i, r := range rows {
		code, cat := r[0], r[1]
		itemID := "itm-" + code
		var catArg any
		if cat != "" {
			catArg = cat
		}
		if _, err := db.Exec(`INSERT INTO items(id,sku,name,base_price,category_id,is_active) VALUES(?,?,?,100,?,1)`, itemID, code+"-SKU", code, catArg); err != nil {
			t.Fatalf("seed item for %s: %v", code, err)
		}
		if _, err := db.Exec(`INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES(?,?,?,?)`, code, code, itemID, i); err != nil {
			t.Fatalf("seed button %s: %v", code, err)
		}
	}
}

// sortedBarcodes returns shortcut_buttons.barcode ordered by sort_order —
// the persisted display order Move (and reorder) actually leave behind.
func sortedBarcodes(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT barcode FROM shortcut_buttons ORDER BY sort_order`)
	if err != nil {
		t.Fatalf("query order: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, b)
	}
	return out
}

func assertStrSlicesEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// TestTileSheet_RendersActions pins GET /ui/pos/tile-sheet's happy path:
// A(cat1) B(cat2) C(cat1) — B is the ONLY button in the whole set with a
// same-category neighbour on both sides (A before it is cat1, not cat2;
// C after it is cat1, not cat2 either — B is alone in cat2), so its sheet
// must show BOTH Move buttons enabled while A's (first overall, and first
// in cat1) shows Move-earlier disabled and C's (last overall, last in
// cat1) shows Move-later disabled. Also pins that the three actions plus
// Close all render.
func TestTileSheet_RendersActions(t *testing.T) {
	mux, d := newButtonsMux(t)
	seedCategorizedButtons(t, d.Db, [][2]string{{"A", "cat1"}, {"B", "cat2"}, {"C", "cat1"}})

	get := func(code string) string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/pos/tile-sheet?code="+code, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("tile-sheet %s = %d (%s)", code, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	for _, want := range []string{
		`data-testid="tile-sheet-move-earlier"`,
		`data-testid="tile-sheet-move-later"`,
		`data-testid="tile-sheet-remove"`,
		`data-testid="tile-sheet-edit"`,
		`data-testid="tile-sheet-close"`,
		"A", // the item's own label
	} {
		if body := get("A"); !strings.Contains(body, want) {
			t.Fatalf("tile-sheet body missing %q: %.800s", want, body)
		}
	}

	// A: first overall AND first in cat1 -- move-earlier disabled, later not.
	bodyA := get("A")
	idxEarlierA := strings.Index(bodyA, `data-testid="tile-sheet-move-earlier"`)
	idxLaterA := strings.Index(bodyA, `data-testid="tile-sheet-move-later"`)
	if !strings.Contains(bodyA[idxEarlierA:idxLaterA], "disabled") {
		t.Fatalf("A's move-earlier must be disabled (no same-category neighbour before it): %.800s", bodyA)
	}
	if strings.Contains(bodyA[idxLaterA:idxLaterA+300], "disabled") {
		t.Fatalf("A's move-later must NOT be disabled (C is a same-category neighbour after it): %.800s", bodyA)
	}

	// B: alone in cat2 -- both directions disabled.
	bodyB := get("B")
	idxEarlierB := strings.Index(bodyB, `data-testid="tile-sheet-move-earlier"`)
	idxLaterB := strings.Index(bodyB, `data-testid="tile-sheet-move-later"`)
	if !strings.Contains(bodyB[idxEarlierB:idxLaterB], "disabled") {
		t.Fatalf("B's move-earlier must be disabled (alone in cat2): %.800s", bodyB)
	}
	if !strings.Contains(bodyB[idxLaterB:idxLaterB+300], "disabled") {
		t.Fatalf("B's move-later must be disabled (alone in cat2): %.800s", bodyB)
	}

	// C: last overall AND last in cat1 -- move-later disabled, earlier not.
	bodyC := get("C")
	idxEarlierC := strings.Index(bodyC, `data-testid="tile-sheet-move-earlier"`)
	idxLaterC := strings.Index(bodyC, `data-testid="tile-sheet-move-later"`)
	if strings.Contains(bodyC[idxEarlierC:idxLaterC], "disabled") {
		t.Fatalf("C's move-earlier must NOT be disabled (A is a same-category neighbour before it): %.800s", bodyC)
	}
	if !strings.Contains(bodyC[idxLaterC:idxLaterC+300], "disabled") {
		t.Fatalf("C's move-later must be disabled (last in cat1): %.800s", bodyC)
	}
}

func TestTileSheet_UnknownCode404(t *testing.T) {
	mux, d := newButtonsMux(t)
	seedCategorizedButtons(t, d.Db, [][2]string{{"A", "cat1"}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/pos/tile-sheet?code=NOPE", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown code = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

// TestTileSheet_ReplicaDisablesActions: on a replica till (SyncPrimaryURL
// set), the sheet still RENDERS (it's a read — GET /ui/pos/tile-sheet is
// never gated by requirePrimary, only the mutating POST routes are) but
// every action is disabled and the replica hint shows, exactly like
// item_replica_gate_test.go's own gate tests assert for the catalog form.
func TestTileSheet_ReplicaDisablesActions(t *testing.T) {
	mux, d := newButtonsMux(t)
	seedCategorizedButtons(t, d.Db, [][2]string{{"A", "cat1"}, {"B", "cat1"}})
	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("set primary_url: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/pos/tile-sheet?code=A", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("tile-sheet on replica = %d (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "manage quick-sale buttons on the primary till") {
		t.Fatalf("expected the replica hint (designer.error.replica_use_primary), got: %.800s", body)
	}
	// A's move-LATER would otherwise be enabled (B is a same-category
	// neighbour after it) -- on a replica it must be disabled anyway.
	idxLater := strings.Index(body, `data-testid="tile-sheet-move-later"`)
	if !strings.Contains(body[idxLater:idxLater+300], "disabled") {
		t.Fatalf("move-later must be disabled on a replica even though A/B share a category: %.800s", body)
	}
	idxRemove := strings.Index(body, `data-testid="tile-sheet-remove"`)
	if !strings.Contains(body[idxRemove:idxRemove+400], "disabled") {
		t.Fatalf("remove must be disabled on a replica: %.800s", body)
	}
	// Edit renders as a disabled BUTTON, not a live link, on a replica.
	idxEdit := strings.Index(body, `data-testid="tile-sheet-edit"`)
	if idxEdit == -1 {
		t.Fatalf("edit control missing: %.800s", body)
	}
	editTag := body[max0(idxEdit-160):idxEdit]
	if !strings.Contains(editTag, "<button") {
		t.Fatalf("edit must render as a disabled <button>, not a live <a href>, on a replica: %.800s", body[max0(idxEdit-160):idxEdit+40])
	}
}

func max0(i int) int {
	if i < 0 {
		return 0
	}
	return i
}

// TestButtonsMove_WithinSameCategoryOnly is the card's own worked example:
// A(cat1) B(cat2) C(cat1), moving A LATER must skip past B (a different
// category) and land immediately AFTER C, its nearest same-category
// neighbour -- persisted order becomes B, C, A.
func TestButtonsMove_WithinSameCategoryOnly(t *testing.T) {
	mux, d := newButtonsMux(t)
	seedCategorizedButtons(t, d.Db, [][2]string{{"A", "cat1"}, {"B", "cat2"}, {"C", "cat1"}})

	rec := postForm(mux, "/api/buttons/move", url.Values{"code": {"A"}, "dir": {"1"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("move A later = %d (%s)", rec.Code, rec.Body.String())
	}
	assertStrSlicesEqual(t, sortedBarcodes(t, d.Db), []string{"B", "C", "A"})

	// The response is the re-rendered sheet for the SAME code, now showing
	// A's fresh position (last overall, last in cat1 -- move-later disabled).
	body := rec.Body.String()
	idxLater := strings.Index(body, `data-testid="tile-sheet-move-later"`)
	if idxLater == -1 || !strings.Contains(body[idxLater:idxLater+300], "disabled") {
		t.Fatalf("re-rendered sheet must show A's move-later now disabled (A is last in cat1): %.800s", body)
	}
}

// TestButtonsMove_EarlierOverOtherCategory pins the nb < idx branch of
// ButtonStore.Move's insert arithmetic (the neighbour sits EARLIER in the
// global order, with a foreign-category button between): C(cat1) moving
// earlier must land immediately before A(cat1), skipping B(cat2), giving
// C, A, B. Added on independent-review finding m4 (2026-09-16): the
// committed tests only drove Move for dir=+1 (nb > idx) and the no-op
// edge, so a future refactor of the post-removal index shift for the
// other direction had nothing to fail against.
func TestButtonsMove_EarlierOverOtherCategory(t *testing.T) {
	mux, d := newButtonsMux(t)
	seedCategorizedButtons(t, d.Db, [][2]string{{"A", "cat1"}, {"B", "cat2"}, {"C", "cat1"}})

	rec := postForm(mux, "/api/buttons/move", url.Values{"code": {"C"}, "dir": {"-1"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("move C earlier = %d (%s)", rec.Code, rec.Body.String())
	}
	assertStrSlicesEqual(t, sortedBarcodes(t, d.Db), []string{"C", "A", "B"})

	// And straight back: A (now second, cat1) earlier over C lands first
	// again -- the adjacent nb < idx case.
	rec = postForm(mux, "/api/buttons/move", url.Values{"code": {"A"}, "dir": {"-1"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("move A earlier = %d (%s)", rec.Code, rec.Body.String())
	}
	assertStrSlicesEqual(t, sortedBarcodes(t, d.Db), []string{"A", "C", "B"})
}

// TestButtonsMove_EdgeNoOp: A is already first in cat1 with nothing before
// it (B, the only earlier button, is cat2) -- moving it earlier is a
// deliberate no-op: still 200, order unchanged, sheet re-rendered with
// move-earlier disabled.
func TestButtonsMove_EdgeNoOp(t *testing.T) {
	mux, d := newButtonsMux(t)
	seedCategorizedButtons(t, d.Db, [][2]string{{"A", "cat1"}, {"B", "cat2"}, {"C", "cat1"}})

	rec := postForm(mux, "/api/buttons/move", url.Values{"code": {"A"}, "dir": {"-1"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("no-op move = %d (%s)", rec.Code, rec.Body.String())
	}
	assertStrSlicesEqual(t, sortedBarcodes(t, d.Db), []string{"A", "B", "C"})

	body := rec.Body.String()
	idxEarlier := strings.Index(body, `data-testid="tile-sheet-move-earlier"`)
	idxLater := strings.Index(body, `data-testid="tile-sheet-move-later"`)
	if idxEarlier == -1 || !strings.Contains(body[idxEarlier:idxLater], "disabled") {
		t.Fatalf("re-rendered sheet must still show move-earlier disabled after a no-op: %.800s", body)
	}
}

func TestButtonsMove_BadDir400(t *testing.T) {
	mux, d := newButtonsMux(t)
	seedCategorizedButtons(t, d.Db, [][2]string{{"A", "cat1"}, {"B", "cat1"}})

	for _, bad := range []string{"0", "2", "-2", "", "abc"} {
		rec := postForm(mux, "/api/buttons/move", url.Values{"code": {"A"}, "dir": {bad}}, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("dir=%q = %d, want 400 (%s)", bad, rec.Code, rec.Body.String())
		}
	}
	assertStrSlicesEqual(t, sortedBarcodes(t, d.Db), []string{"A", "B"})
}

// TestButtonsMove_Replica409 mirrors TestButtonsAPI_MutationsRefusedOnReplica
// for the new route: same requirePrimary gate, same 409 shape.
func TestButtonsMove_Replica409(t *testing.T) {
	mux, d := newButtonsMux(t)
	seedCategorizedButtons(t, d.Db, [][2]string{{"A", "cat1"}, {"B", "cat1"}})
	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("set primary_url: %v", err)
	}

	rec := postForm(mux, "/api/buttons/move", url.Values{"code": {"A"}, "dir": {"1"}}, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("move on replica = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "manage quick-sale buttons on the primary till") {
		t.Fatalf("expected the localized replica_use_primary message, got %q", rec.Body.String())
	}
	assertStrSlicesEqual(t, sortedBarcodes(t, d.Db), []string{"A", "B"})
}

func TestButtonsMove_SetsHXTrigger(t *testing.T) {
	mux, d := newButtonsMux(t)
	seedCategorizedButtons(t, d.Db, [][2]string{{"A", "cat1"}, {"B", "cat1"}})

	rec := postForm(mux, "/api/buttons/move", url.Values{"code": {"A"}, "dir": {"1"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("move = %d (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Trigger"); got != "buttons-changed" {
		t.Fatalf("move response HX-Trigger = %q, want %q", got, "buttons-changed")
	}
}

func TestButtonsRemove_SetsHXTrigger(t *testing.T) {
	mux, d := newButtonsMux(t)
	seedCategorizedButtons(t, d.Db, [][2]string{{"A", "cat1"}})

	rec := postForm(mux, "/api/buttons/remove", url.Values{"code": {"A"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("remove = %d (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Trigger"); got != "buttons-changed" {
		t.Fatalf("remove response HX-Trigger = %q, want %q", got, "buttons-changed")
	}
}
