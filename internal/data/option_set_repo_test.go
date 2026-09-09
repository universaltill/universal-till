package data_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

func openOptionSetTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "optset.db"))
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
