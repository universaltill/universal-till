package data_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

func openOptionSetTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(testsupport.MigratedDBFile(t, "optset.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// variantRow is one item_variants row read back verbatim, so a "must be
// untouched" assertion compares the whole row, not just a count.
type variantRow struct {
	ID       string
	SKU      sql.NullString
	Name     string
	Price    int64
	Cost     sql.NullInt64
	IsActive int
}

func readVariants(t *testing.T, d *db.DB, itemID string) []variantRow {
	t.Helper()
	rows, err := d.DB.Query(`SELECT id, sku, name, price, cost_price, is_active FROM item_variants WHERE item_id = ? ORDER BY id`, itemID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []variantRow
	for rows.Next() {
		var v variantRow
		if err := rows.Scan(&v.ID, &v.SKU, &v.Name, &v.Price, &v.Cost, &v.IsActive); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

func seedOptionSetItem(t *testing.T, d *db.DB, id, name string, price int64) {
	t.Helper()
	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES (?, ?, ?, ?, 1)`, id, "SKU-"+id, name, price); err != nil {
		t.Fatal(err)
	}
}

func TestOptionSetRepo_CreateSetAndValues_ListsInOrder(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)

	sizeID, err := repo.CreateOptionSet(ctx, "Size")
	if err != nil {
		t.Fatalf("CreateOptionSet: %v", err)
	}
	if sizeID == "" {
		t.Fatal("CreateOptionSet returned an empty id")
	}
	for _, v := range []string{"S", "M", "L"} {
		if _, err := repo.AddOptionSetValue(ctx, sizeID, v); err != nil {
			t.Fatalf("AddOptionSetValue(%q): %v", v, err)
		}
	}

	sets, err := repo.ListOptionSets(ctx)
	if err != nil {
		t.Fatalf("ListOptionSets: %v", err)
	}
	if len(sets) != 1 || sets[0].ID != sizeID || sets[0].Name != "Size" || !sets[0].IsActive {
		t.Fatalf("unexpected sets: %+v", sets)
	}
	var got []string
	for i, v := range sets[0].Values {
		got = append(got, v.Value)
		if v.SortOrder != i+1 {
			t.Fatalf("value %q sort_order = %d, want %d (insertion order, max+1)", v.Value, v.SortOrder, i+1)
		}
	}
	if strings.Join(got, ",") != "S,M,L" {
		t.Fatalf("values not in insertion order: %v", got)
	}
}

func TestOptionSetRepo_CreateOptionSet_DuplicateNameIsErrOptionSetExists(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)
	if _, err := repo.CreateOptionSet(ctx, "Size"); err != nil {
		t.Fatal(err)
	}
	_, err := repo.CreateOptionSet(ctx, "Size")
	if !errors.Is(err, data.ErrOptionSetExists) {
		t.Fatalf("want ErrOptionSetExists on a duplicate name, got %v", err)
	}
	if _, err := repo.CreateOptionSet(ctx, "  "); err == nil {
		t.Fatal("a blank name must be rejected")
	}
}

func TestOptionSetRepo_AddOptionSetValue_DuplicateValueIsErrOptionSetValueExists(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)
	sizeID, _ := repo.CreateOptionSet(ctx, "Size")
	if _, err := repo.AddOptionSetValue(ctx, sizeID, "S"); err != nil {
		t.Fatal(err)
	}
	_, err := repo.AddOptionSetValue(ctx, sizeID, "S")
	if !errors.Is(err, data.ErrOptionSetValueExists) {
		t.Fatalf("want ErrOptionSetValueExists on a duplicate value in the same set, got %v", err)
	}
	// The same value in a DIFFERENT set is fine ("Red" can be in both
	// "Colour" and "Trim").
	colourID, _ := repo.CreateOptionSet(ctx, "Colour")
	if _, err := repo.AddOptionSetValue(ctx, colourID, "S"); err != nil {
		t.Fatalf("same value in another set must be allowed: %v", err)
	}
}

func TestOptionSetRepo_ApplyOptionSetsToItem_CapsAtTwoAndReplaces(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)
	seedOptionSetItem(t, d, "tee", "T-shirt", 1500)
	a, _ := repo.CreateOptionSet(ctx, "Size")
	b, _ := repo.CreateOptionSet(ctx, "Colour")
	c, _ := repo.CreateOptionSet(ctx, "Fit")

	if err := repo.ApplyOptionSetsToItem(ctx, "tee", []string{a, b, c}); err == nil {
		t.Fatal("three axes must be rejected (max 2 is a real constraint, not a UI hint)")
	}
	if err := repo.ApplyOptionSetsToItem(ctx, "tee", []string{b, a}); err != nil {
		t.Fatalf("ApplyOptionSetsToItem: %v", err)
	}
	applied, err := repo.ItemOptionSets(ctx, "tee")
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 2 || applied[0].ID != b || applied[1].ID != a {
		t.Fatalf("want [Colour, Size] in axis order, got %+v", applied)
	}
	// Re-applying REPLACES the previous choice (delete-then-insert), it
	// doesn't accumulate.
	if err := repo.ApplyOptionSetsToItem(ctx, "tee", []string{a}); err != nil {
		t.Fatal(err)
	}
	applied, _ = repo.ItemOptionSets(ctx, "tee")
	if len(applied) != 1 || applied[0].ID != a {
		t.Fatalf("want only [Size] after re-apply, got %+v", applied)
	}
	// Clearing is allowed too.
	if err := repo.ApplyOptionSetsToItem(ctx, "tee", nil); err != nil {
		t.Fatal(err)
	}
	if applied, _ = repo.ItemOptionSets(ctx, "tee"); len(applied) != 0 {
		t.Fatalf("want no sets after clearing, got %+v", applied)
	}
}

// The core AC: one 3-value set generates 3 real variants, each with a real
// SKU; re-running is a no-op that leaves the existing rows byte-identical;
// adding a 4th value and re-running creates exactly the one missing
// combination (ut-docs#1839's re-import-duplication bug class must not be
// reintroduced here).
func TestOptionSetRepo_GenerateVariants_OneAxisIsIdempotent(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)
	seedOptionSetItem(t, d, "tee", "T-shirt", 1500)
	sizeID, _ := repo.CreateOptionSet(ctx, "Size")
	for _, v := range []string{"S", "M", "L"} {
		if _, err := repo.AddOptionSetValue(ctx, sizeID, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.ApplyOptionSetsToItem(ctx, "tee", []string{sizeID}); err != nil {
		t.Fatal(err)
	}

	created, err := repo.GenerateVariants(ctx, "tee")
	if err != nil {
		t.Fatalf("GenerateVariants: %v", err)
	}
	if created != 3 {
		t.Fatalf("want 3 variants created, got %d", created)
	}
	first := readVariants(t, d, "tee")
	if len(first) != 3 {
		t.Fatalf("want 3 item_variants rows, got %d", len(first))
	}
	var names []string
	for _, v := range first {
		if !v.SKU.Valid || strings.TrimSpace(v.SKU.String) == "" {
			t.Fatalf("generated variant %q must carry a real SKU, got %+v", v.Name, v.SKU)
		}
		if v.Price != 1500 {
			t.Fatalf("generated variant %q price = %d, want the item's base price 1500", v.Name, v.Price)
		}
		if v.Cost.Valid {
			t.Fatalf("generated variant %q cost_price must be NULL, got %v", v.Name, v.Cost.Int64)
		}
		if v.IsActive != 1 {
			t.Fatalf("generated variant %q must be active", v.Name)
		}
		names = append(names, v.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "L,M,S" {
		t.Fatalf("want variant names S, M, L, got %v", names)
	}

	// Re-run with nothing changed: 0 created, rows untouched.
	created, err = repo.GenerateVariants(ctx, "tee")
	if err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Fatalf("re-running with no changes must create 0 variants, got %d", created)
	}
	second := readVariants(t, d, "tee")
	if len(second) != len(first) {
		t.Fatalf("re-run changed the row count: %d -> %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("re-run altered an existing variant row:\n before %+v\n after  %+v", first[i], second[i])
		}
	}

	// Add a 4th value, regenerate: exactly 1 new, the original 3 untouched.
	if _, err := repo.AddOptionSetValue(ctx, sizeID, "XL"); err != nil {
		t.Fatal(err)
	}
	created, err = repo.GenerateVariants(ctx, "tee")
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("after adding one value, want exactly 1 new variant, got %d", created)
	}
	third := readVariants(t, d, "tee")
	if len(third) != 4 {
		t.Fatalf("want 4 rows after the 4th value, got %d", len(third))
	}
	byID := map[string]variantRow{}
	for _, v := range third {
		byID[v.ID] = v
	}
	for _, orig := range first {
		if byID[orig.ID] != orig {
			t.Fatalf("original variant %s altered by regeneration:\n before %+v\n after  %+v", orig.ID, orig, byID[orig.ID])
		}
	}
	var sawXL bool
	for _, v := range third {
		if v.Name == "XL" {
			sawXL = true
			if !v.SKU.Valid || v.SKU.String == "" {
				t.Fatalf("the new XL variant must carry a real SKU, got %+v", v.SKU)
			}
		}
	}
	if !sawXL {
		t.Fatalf("expected an XL variant among %+v", third)
	}
}

// Two axes (Size × Colour, 2 × 3) generate 6 variants, each linked to
// exactly its own pair of option values in item_variant_options, named in
// axis order ("Small / Red", never "Red / Small").
func TestOptionSetRepo_GenerateVariants_TwoAxesCartesianProduct(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)
	seedOptionSetItem(t, d, "tee", "T-shirt", 1500)
	sizeID, _ := repo.CreateOptionSet(ctx, "Size")
	colourID, _ := repo.CreateOptionSet(ctx, "Colour")
	sizeVals := map[string]string{}
	for _, v := range []string{"Small", "Large"} {
		id, err := repo.AddOptionSetValue(ctx, sizeID, v)
		if err != nil {
			t.Fatal(err)
		}
		sizeVals[v] = id
	}
	colourVals := map[string]string{}
	for _, v := range []string{"Red", "Green", "Blue"} {
		id, err := repo.AddOptionSetValue(ctx, colourID, v)
		if err != nil {
			t.Fatal(err)
		}
		colourVals[v] = id
	}
	if err := repo.ApplyOptionSetsToItem(ctx, "tee", []string{sizeID, colourID}); err != nil {
		t.Fatal(err)
	}

	created, err := repo.GenerateVariants(ctx, "tee")
	if err != nil {
		t.Fatalf("GenerateVariants: %v", err)
	}
	if created != 6 {
		t.Fatalf("want 2x3 = 6 variants, got %d", created)
	}
	variants := readVariants(t, d, "tee")
	if len(variants) != 6 {
		t.Fatalf("want 6 rows, got %d", len(variants))
	}
	// Every (size, colour) pair present exactly once, with the right link rows.
	seen := map[string]bool{}
	for _, v := range variants {
		rows, err := d.DB.Query(`SELECT option_set_value_id FROM item_variant_options WHERE variant_id = ? ORDER BY option_set_value_id`, v.ID)
		if err != nil {
			t.Fatal(err)
		}
		var linked []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			linked = append(linked, id)
		}
		rows.Close()
		if len(linked) != 2 {
			t.Fatalf("variant %q must link exactly 2 option values, got %v", v.Name, linked)
		}
		parts := strings.Split(v.Name, " / ")
		if len(parts) != 2 {
			t.Fatalf("variant name %q must be '<size> / <colour>'", v.Name)
		}
		wantLinked := []string{sizeVals[parts[0]], colourVals[parts[1]]}
		sort.Strings(wantLinked)
		if strings.Join(linked, "|") != strings.Join(wantLinked, "|") {
			t.Fatalf("variant %q links %v, want %v", v.Name, linked, wantLinked)
		}
		if seen[v.Name] {
			t.Fatalf("duplicate combination %q", v.Name)
		}
		seen[v.Name] = true
	}
	for _, s := range []string{"Small", "Large"} {
		for _, c := range []string{"Red", "Green", "Blue"} {
			if !seen[s+" / "+c] {
				t.Fatalf("missing combination %q among %v", s+" / "+c, seen)
			}
		}
	}

	// And re-running is still a no-op with two axes.
	if created, err = repo.GenerateVariants(ctx, "tee"); err != nil || created != 0 {
		t.Fatalf("re-run: want 0 created / nil, got %d / %v", created, err)
	}
}

// An item with no applied option sets: the generator reports that
// distinctly (ErrNoOptionSetsApplied) rather than silently creating nothing,
// so the panel can tell the operator what to do first.
func TestOptionSetRepo_GenerateVariants_NoSetsAppliedIsError(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)
	seedOptionSetItem(t, d, "tee", "T-shirt", 1500)
	_, err := repo.GenerateVariants(ctx, "tee")
	if !errors.Is(err, data.ErrNoOptionSetsApplied) {
		t.Fatalf("want ErrNoOptionSetsApplied, got %v", err)
	}
}

// A generated variant the operator has since DEACTIVATED is still
// "existing" — existingCombinations deliberately ignores is_active — so a
// re-run neither resurrects it nor adds a second, active row for the same
// combination. Without that, retiring the "S" line and pressing Generate
// again would silently undo the retirement by way of a duplicate
// (ut-docs#1900 review: the guarantee was documented on
// existingCombinations but nothing exercised it).
func TestOptionSetRepo_GenerateVariants_DeactivatedGeneratedVariantIsNotResurrected(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)
	seedOptionSetItem(t, d, "tee", "T-shirt", 1500)
	sizeID, _ := repo.CreateOptionSet(ctx, "Size")
	for _, v := range []string{"S", "M"} {
		if _, err := repo.AddOptionSetValue(ctx, sizeID, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.ApplyOptionSetsToItem(ctx, "tee", []string{sizeID}); err != nil {
		t.Fatal(err)
	}
	if created, err := repo.GenerateVariants(ctx, "tee"); err != nil || created != 2 {
		t.Fatalf("first generate: created=%d err=%v", created, err)
	}
	if _, err := d.DB.Exec(`UPDATE item_variants SET is_active = 0 WHERE item_id = 'tee' AND name = 'S'`); err != nil {
		t.Fatal(err)
	}
	created, err := repo.GenerateVariants(ctx, "tee")
	if err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Fatalf("a deactivated generated variant must stay 'existing'; re-run created %d", created)
	}
	rows := readVariants(t, d, "tee")
	if len(rows) != 2 {
		t.Fatalf("want still 2 rows, got %d: %+v", len(rows), rows)
	}
	for _, v := range rows {
		if v.Name == "S" && v.IsActive != 0 {
			t.Fatalf("the deactivated S variant was resurrected: %+v", v)
		}
	}
}

// A hand-added variant (no item_variant_options rows) is neither counted as
// a generated combination nor touched by the generator.
func TestOptionSetRepo_GenerateVariants_LeavesManualVariantsAlone(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)
	seedOptionSetItem(t, d, "tee", "T-shirt", 1500)
	manualID, err := data.NewCatalogRepo(d.DB).CreateVariant(ctx, catalogtypes.VariantInput{ItemID: "tee", SKU: "TEE-MANUAL", Name: "S", Price: 1400, IsActive: true})
	if err != nil {
		t.Fatal(err)
	}
	sizeID, _ := repo.CreateOptionSet(ctx, "Size")
	if _, err := repo.AddOptionSetValue(ctx, sizeID, "S"); err != nil {
		t.Fatal(err)
	}
	if err := repo.ApplyOptionSetsToItem(ctx, "tee", []string{sizeID}); err != nil {
		t.Fatal(err)
	}
	created, err := repo.GenerateVariants(ctx, "tee")
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("a manual variant named S is not the generated S combination; want 1 created, got %d", created)
	}
	variants := readVariants(t, d, "tee")
	if len(variants) != 2 {
		t.Fatalf("want the manual row plus one generated row, got %+v", variants)
	}
	for _, v := range variants {
		if v.ID == manualID && (v.SKU.String != "TEE-MANUAL" || v.Price != 1400) {
			t.Fatalf("manual variant altered: %+v", v)
		}
	}
}

// --- manage parity (ut-docs#3319): rename, values full replace, active
// flag, delete-when-unused, and the used-by read. ---

func optionSetValues(t *testing.T, repo *data.OptionSetRepo, setID string) []data.OptionSetValueView {
	t.Helper()
	set, err := repo.GetOptionSet(context.Background(), setID)
	if err != nil {
		t.Fatalf("GetOptionSet(%s): %v", setID, err)
	}
	return set.Values
}

func TestOptionSetRepo_RenameOptionSet_CollisionAndNotFound(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)
	sizeID, _ := repo.CreateOptionSet(ctx, "Size")
	if _, err := repo.CreateOptionSet(ctx, "Colour"); err != nil {
		t.Fatal(err)
	}

	if err := repo.RenameOptionSet(ctx, sizeID, "  Sizes  "); err != nil {
		t.Fatalf("RenameOptionSet: %v", err)
	}
	set, err := repo.GetOptionSet(ctx, sizeID)
	if err != nil || set.Name != "Sizes" {
		t.Fatalf("renamed set = %+v err=%v, want trimmed name Sizes", set, err)
	}
	// Renaming to its own current name is a no-op, not a collision.
	if err := repo.RenameOptionSet(ctx, sizeID, "Sizes"); err != nil {
		t.Fatalf("rename to the same name: %v", err)
	}
	if err := repo.RenameOptionSet(ctx, sizeID, "Colour"); !errors.Is(err, data.ErrOptionSetExists) {
		t.Fatalf("rename onto another set's name: want ErrOptionSetExists, got %v", err)
	}
	if set, _ := repo.GetOptionSet(ctx, sizeID); set.Name != "Sizes" {
		t.Fatalf("a refused rename must leave the name, got %q", set.Name)
	}
	if err := repo.RenameOptionSet(ctx, sizeID, "   "); err == nil {
		t.Fatal("a blank name must be rejected")
	}
	if err := repo.RenameOptionSet(ctx, "nope", "Fit"); !errors.Is(err, data.ErrOptionSetNotFound) {
		t.Fatalf("rename of an unknown set: want ErrOptionSetNotFound, got %v", err)
	}
	if _, err := repo.GetOptionSet(ctx, "nope"); !errors.Is(err, data.ErrOptionSetNotFound) {
		t.Fatalf("GetOptionSet of an unknown set: want ErrOptionSetNotFound, got %v", err)
	}
}

// One call adds, edits, reorders and removes: known ids keep their id
// (so generated variants' item_variant_options links survive a rename),
// unknown ids are created WITH that id, unlisted ones are deleted, and
// sort_order = index.
func TestOptionSetRepo_ReplaceOptionSetValues_AddEditReorderRemoveInOneCall(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)
	sizeID, _ := repo.CreateOptionSet(ctx, "Size")
	sID, _ := repo.AddOptionSetValue(ctx, sizeID, "S")
	mID, _ := repo.AddOptionSetValue(ctx, sizeID, "M")
	lID, _ := repo.AddOptionSetValue(ctx, sizeID, "L")

	// L first, M renamed to Medium, S removed, XL new.
	err := repo.ReplaceOptionSetValues(ctx, sizeID, []data.OptionSetValueInput{
		{ID: lID, Value: "L"},
		{ID: mID, Value: " Medium "},
		{ID: "val-xl", Value: "XL"},
	})
	if err != nil {
		t.Fatalf("ReplaceOptionSetValues: %v", err)
	}
	got := optionSetValues(t, repo, sizeID)
	want := []data.OptionSetValueView{{ID: lID, Value: "L", SortOrder: 0}, {ID: mID, Value: "Medium", SortOrder: 1}, {ID: "val-xl", Value: "XL", SortOrder: 2}}
	if len(got) != len(want) {
		t.Fatalf("values = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("values[%d] = %+v, want %+v (all: %+v)", i, got[i], want[i], got)
		}
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM option_set_values WHERE id = ?`, sID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("removed value S must be deleted, count=%d err=%v", n, err)
	}

	// Swapping two values' text in one call must not trip UNIQUE
	// (option_set_id, value) midway.
	if err := repo.ReplaceOptionSetValues(ctx, sizeID, []data.OptionSetValueInput{
		{ID: lID, Value: "Medium"}, {ID: mID, Value: "L"}, {ID: "val-xl", Value: "XL"},
	}); err != nil {
		t.Fatalf("swap values: %v", err)
	}
	got = optionSetValues(t, repo, sizeID)
	if got[0].ID != lID || got[0].Value != "Medium" || got[1].ID != mID || got[1].Value != "L" {
		t.Fatalf("after swap = %+v", got)
	}

	// Empty list clears every value.
	if err := repo.ReplaceOptionSetValues(ctx, sizeID, []data.OptionSetValueInput{}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := optionSetValues(t, repo, sizeID); len(got) != 0 {
		t.Fatalf("after clear = %+v", got)
	}
}

func TestOptionSetRepo_ReplaceOptionSetValues_RejectsBadListsWritingNothing(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)
	sizeID, _ := repo.CreateOptionSet(ctx, "Size")
	sID, _ := repo.AddOptionSetValue(ctx, sizeID, "S")
	colourID, _ := repo.CreateOptionSet(ctx, "Colour")
	redID, _ := repo.AddOptionSetValue(ctx, colourID, "Red")

	tooMany := make([]data.OptionSetValueInput, 0, 51)
	for i := 0; i < 51; i++ {
		tooMany = append(tooMany, data.OptionSetValueInput{ID: fmt.Sprintf("v%02d", i), Value: fmt.Sprintf("V%02d", i)})
	}
	for _, tc := range []struct {
		name    string
		setID   string
		values  []data.OptionSetValueInput
		wantErr error
	}{
		{"blank value", sizeID, []data.OptionSetValueInput{{ID: sID, Value: "  "}}, data.ErrOptionSetValueInvalid},
		{"blank id", sizeID, []data.OptionSetValueInput{{ID: " ", Value: "M"}}, nil},
		{"duplicate text", sizeID, []data.OptionSetValueInput{{ID: sID, Value: "S"}, {ID: "new", Value: " S"}}, data.ErrOptionSetValueExists},
		{"duplicate id", sizeID, []data.OptionSetValueInput{{ID: sID, Value: "S"}, {ID: sID, Value: "M"}}, nil},
		{"too long", sizeID, []data.OptionSetValueInput{{ID: sID, Value: strings.Repeat("é", 129)}}, data.ErrOptionSetValueInvalid},
		{"too many", sizeID, tooMany, data.ErrOptionSetTooManyValues},
		{"id from another set", sizeID, []data.OptionSetValueInput{{ID: redID, Value: "Red"}}, nil},
		{"unknown set", "nope", []data.OptionSetValueInput{{ID: "a", Value: "A"}}, data.ErrOptionSetNotFound},
	} {
		err := repo.ReplaceOptionSetValues(ctx, tc.setID, tc.values)
		if err == nil {
			t.Errorf("%s: want an error", tc.name)
			continue
		}
		if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
			t.Errorf("%s: want %v, got %v", tc.name, tc.wantErr, err)
		}
	}
	if got := optionSetValues(t, repo, sizeID); len(got) != 1 || got[0].ID != sID || got[0].Value != "S" {
		t.Fatalf("a refused replace must write nothing, Size values = %+v", got)
	}
	if got := optionSetValues(t, repo, colourID); len(got) != 1 || got[0].ID != redID {
		t.Fatalf("another set's value must never move, Colour values = %+v", got)
	}
	// 128 runes exactly is fine; 50 values exactly is fine.
	if err := repo.ReplaceOptionSetValues(ctx, sizeID, []data.OptionSetValueInput{{ID: sID, Value: strings.Repeat("é", 128)}}); err != nil {
		t.Fatalf("128 runes must be allowed: %v", err)
	}
	if err := repo.ReplaceOptionSetValues(ctx, sizeID, tooMany[:50]); err != nil {
		t.Fatalf("50 values must be allowed: %v", err)
	}
}

func TestOptionSetRepo_SetOptionSetActive(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)
	sizeID, _ := repo.CreateOptionSet(ctx, "Size")
	if err := repo.SetOptionSetActive(ctx, sizeID, false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if set, _ := repo.GetOptionSet(ctx, sizeID); set.IsActive {
		t.Fatal("set must be inactive")
	}
	// Still listed: the admin screen shows a deactivated set.
	if sets, _ := repo.ListOptionSets(ctx); len(sets) != 1 || sets[0].IsActive {
		t.Fatalf("ListOptionSets = %+v", sets)
	}
	if err := repo.SetOptionSetActive(ctx, sizeID, true); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if set, _ := repo.GetOptionSet(ctx, sizeID); !set.IsActive {
		t.Fatal("set must be active again")
	}
	if err := repo.SetOptionSetActive(ctx, "nope", true); !errors.Is(err, data.ErrOptionSetNotFound) {
		t.Fatalf("unknown set: want ErrOptionSetNotFound, got %v", err)
	}
}

// The DB would cascade an in-use set away (item_option_sets ON DELETE
// CASCADE), so the repo refuses explicitly and the used-by read names the
// items.
func TestOptionSetRepo_DeleteOptionSetIfUnused_RefusesInUseNamesItems(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)
	seedOptionSetItem(t, d, "tee", "T-shirt", 1500)
	seedOptionSetItem(t, d, "hood", "Hoodie", 3000)
	sizeID, _ := repo.CreateOptionSet(ctx, "Size")
	if _, err := repo.AddOptionSetValue(ctx, sizeID, "S"); err != nil {
		t.Fatal(err)
	}
	for _, it := range []string{"tee", "hood"} {
		if err := repo.ApplyOptionSetsToItem(ctx, it, []string{sizeID}); err != nil {
			t.Fatal(err)
		}
	}

	found, err := repo.DeleteOptionSetIfUnused(ctx, sizeID)
	if !errors.Is(err, data.ErrOptionSetInUse) || found {
		t.Fatalf("in-use delete: found=%v err=%v, want false + ErrOptionSetInUse", found, err)
	}
	if _, err := repo.GetOptionSet(ctx, sizeID); err != nil {
		t.Fatalf("an in-use set must not be deleted: %v", err)
	}
	items, err := repo.ItemsUsingOptionSet(ctx, sizeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != "hood" || items[0].Name != "Hoodie" || items[1].ID != "tee" || items[1].Name != "T-shirt" {
		t.Fatalf("ItemsUsingOptionSet = %+v, want Hoodie, T-shirt (by name)", items)
	}
	usage, err := repo.ListOptionSetsWithItems(ctx)
	if err != nil || len(usage) != 1 || len(usage[0].Items) != 2 || len(usage[0].Values) != 1 {
		t.Fatalf("ListOptionSetsWithItems = %+v err=%v", usage, err)
	}

	// Unapply from both: the delete now succeeds and cascades its values.
	for _, it := range []string{"tee", "hood"} {
		if err := repo.ApplyOptionSetsToItem(ctx, it, nil); err != nil {
			t.Fatal(err)
		}
	}
	found, err = repo.DeleteOptionSetIfUnused(ctx, sizeID)
	if err != nil || !found {
		t.Fatalf("unused delete: found=%v err=%v", found, err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM option_set_values WHERE option_set_id = ?`, sizeID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("values must cascade with the set, count=%d err=%v", n, err)
	}
	// Replay: nothing there to delete, reported as such, not an error.
	found, err = repo.DeleteOptionSetIfUnused(ctx, sizeID)
	if err != nil || found {
		t.Fatalf("replay delete: found=%v err=%v, want false, nil", found, err)
	}
	if _, err := repo.DeleteOptionSetIfUnused(ctx, " "); err == nil {
		t.Fatal("a blank id must be rejected")
	}
}

// SaveOptionSet is the directive's one call: create-with-id, then a patch
// where nil keeps a field.
func TestOptionSetRepo_SaveOptionSet_CreateWithIDAndPatch(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)
	name := "Size"
	vals := []data.OptionSetValueInput{{ID: "v-s", Value: "S"}, {ID: "v-m", Value: "M"}}
	if _, err := repo.SaveOptionSet(ctx, data.OptionSetSave{ID: "set-1", Name: &name}); !errors.Is(err, data.ErrOptionSetNotFound) {
		t.Fatalf("update of an unknown id without create: want ErrOptionSetNotFound, got %v", err)
	}
	if _, err := repo.SaveOptionSet(ctx, data.OptionSetSave{ID: "set-1", Create: true}); err == nil {
		t.Fatal("create without a name must be rejected")
	}
	res, err := repo.SaveOptionSet(ctx, data.OptionSetSave{ID: "set-1", Create: true, Name: &name, Values: &vals})
	if err != nil || !res.Created || res.Name != "Size" {
		t.Fatalf("create: %+v %v", res, err)
	}
	set, err := repo.GetOptionSet(ctx, "set-1")
	if err != nil || set.Name != "Size" || !set.IsActive || len(set.Values) != 2 || set.Values[0].ID != "v-s" {
		t.Fatalf("created set = %+v err=%v", set, err)
	}
	// Replay of the same create: idempotent update, same state.
	res, err = repo.SaveOptionSet(ctx, data.OptionSetSave{ID: "set-1", Create: true, Name: &name, Values: &vals})
	if err != nil || res.Created {
		t.Fatalf("replay: %+v %v", res, err)
	}
	off := false
	res, err = repo.SaveOptionSet(ctx, data.OptionSetSave{ID: "set-1", Active: &off})
	if err != nil || res.Name != "Size" || strings.Join(res.Changed, ",") != "active" {
		t.Fatalf("active-only patch: %+v %v", res, err)
	}
	set, _ = repo.GetOptionSet(ctx, "set-1")
	if set.IsActive || set.Name != "Size" || len(set.Values) != 2 {
		t.Fatalf("active-only patch must keep name and values: %+v", set)
	}
	// A rename collision rolls back the whole patch (values untouched).
	other := "Colour"
	if _, err := repo.SaveOptionSet(ctx, data.OptionSetSave{ID: "set-2", Create: true, Name: &other}); err != nil {
		t.Fatal(err)
	}
	none := []data.OptionSetValueInput{}
	if _, err := repo.SaveOptionSet(ctx, data.OptionSetSave{ID: "set-1", Name: &other, Values: &none}); !errors.Is(err, data.ErrOptionSetExists) {
		t.Fatalf("collision: want ErrOptionSetExists, got %v", err)
	}
	if set, _ := repo.GetOptionSet(ctx, "set-1"); len(set.Values) != 2 || set.Name != "Size" {
		t.Fatalf("a refused save must write nothing: %+v", set)
	}
}

// A set created by the directive generates variants exactly like a
// till-made one (card AC).
func TestOptionSetRepo_SaveOptionSet_GeneratesVariantsLikeTillMade(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)
	seedOptionSetItem(t, d, "tee", "T-shirt", 1500)
	name := "Size"
	vals := []data.OptionSetValueInput{{ID: "v-s", Value: "S"}, {ID: "v-m", Value: "M"}}
	if _, err := repo.SaveOptionSet(ctx, data.OptionSetSave{ID: "set-1", Create: true, Name: &name, Values: &vals}); err != nil {
		t.Fatal(err)
	}
	if err := repo.ApplyOptionSetsToItem(ctx, "tee", []string{"set-1"}); err != nil {
		t.Fatal(err)
	}
	created, err := repo.GenerateVariants(ctx, "tee")
	if err != nil || created != 2 {
		t.Fatalf("GenerateVariants = %d, %v; want 2", created, err)
	}
	var names []string
	for _, v := range readVariants(t, d, "tee") {
		names = append(names, v.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "M,S" {
		t.Fatalf("variant names = %v", names)
	}
}
