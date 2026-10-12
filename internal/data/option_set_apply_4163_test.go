package data_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#4163: apply_item_option_sets from my. — the till applies the
// sets and (optionally) generates the variant matrix through the same
// generator its own catalog panel uses, so my.-generated variants carry
// item_variant_options links and a later Generate never duplicates them.

func appliedSetIDs(t *testing.T, d *db.DB, itemID string) []string {
	t.Helper()
	rows, err := d.DB.Query(`SELECT option_set_id FROM item_option_sets WHERE item_id = ? ORDER BY axis_order`, itemID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func newSetWithValues(t *testing.T, repo *data.OptionSetRepo, name string, values ...string) string {
	t.Helper()
	ctx := context.Background()
	id, err := repo.CreateOptionSet(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range values {
		if _, err := repo.AddOptionSetValue(ctx, id, v); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestApplyOptionSetsAndGenerate_AppliesInOrderGeneratesAndReplays(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)
	seedOptionSetItem(t, d, "tee", "T-shirt", 1500)
	colour := newSetWithValues(t, repo, "Colour", "Red", "Blue")
	size := newSetWithValues(t, repo, "Size", "S", "M")

	res, err := repo.ApplyOptionSetsAndGenerate(ctx, "tee", []string{size, colour}, true)
	if err != nil {
		t.Fatalf("apply+generate: %v", err)
	}
	if res.Created != 4 || res.ItemName != "T-shirt" {
		t.Fatalf("result = %+v, want 4 created for T-shirt", res)
	}
	if got := appliedSetIDs(t, d, "tee"); strings.Join(got, ",") != size+","+colour {
		t.Fatalf("applied = %v, want size then colour (axis order as sent)", got)
	}
	names := map[string]bool{}
	for _, v := range readVariants(t, d, "tee") {
		names[v.Name] = true
		if !v.SKU.Valid || v.SKU.String == "" {
			t.Fatalf("variant %q has no SKU", v.Name)
		}
	}
	for _, n := range []string{"S / Red", "S / Blue", "M / Red", "M / Blue"} {
		if !names[n] {
			t.Fatalf("missing generated variant %q in %v", n, names)
		}
	}
	// The links are what the till's own Generate recognises: it now finds
	// nothing to add.
	if n, err := repo.GenerateVariants(ctx, "tee"); err != nil || n != 0 {
		t.Fatalf("till Generate after my. generate: %d %v, want 0", n, err)
	}
	// A replayed directive converges: nothing new.
	res, err = repo.ApplyOptionSetsAndGenerate(ctx, "tee", []string{size, colour}, true)
	if err != nil || res.Created != 0 {
		t.Fatalf("replay: %+v %v, want 0 created", res, err)
	}
	if len(readVariants(t, d, "tee")) != 4 {
		t.Fatal("replay changed the variant count")
	}
}

func TestApplyOptionSetsAndGenerate_ApplyOnlyAndClear(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)
	seedOptionSetItem(t, d, "tee", "T-shirt", 1500)
	size := newSetWithValues(t, repo, "Size", "S", "M")

	res, err := repo.ApplyOptionSetsAndGenerate(ctx, "tee", []string{size}, false)
	if err != nil || res.Created != 0 {
		t.Fatalf("apply only: %+v %v", res, err)
	}
	if got := appliedSetIDs(t, d, "tee"); len(got) != 1 || got[0] != size {
		t.Fatalf("applied = %v", got)
	}
	if len(readVariants(t, d, "tee")) != 0 {
		t.Fatal("generate=false created variants")
	}
	// [] clears; generate with no sets is not an error, it creates nothing.
	if res, err := repo.ApplyOptionSetsAndGenerate(ctx, "tee", []string{}, true); err != nil || res.Created != 0 {
		t.Fatalf("clear: %+v %v", res, err)
	}
	if got := appliedSetIDs(t, d, "tee"); len(got) != 0 {
		t.Fatalf("clear left %v", got)
	}
}

func TestApplyOptionSetsAndGenerate_RefusalsWriteNothing(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)
	seedOptionSetItem(t, d, "tee", "T-shirt", 1500)
	size := newSetWithValues(t, repo, "Size", "S", "M")
	colour := newSetWithValues(t, repo, "Colour", "Red")
	fit := newSetWithValues(t, repo, "Fit", "Slim")
	retired := newSetWithValues(t, repo, "Retired", "X")
	if _, err := d.DB.Exec(`UPDATE option_sets SET is_active = 0 WHERE id = ?`, retired); err != nil {
		t.Fatal(err)
	}
	if err := repo.ApplyOptionSetsToItem(ctx, "tee", []string{size}); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name string
		item string
		ids  []string
		want string
	}{
		{"missing item", "ghost", []string{size}, "item ghost does not exist on this till"},
		{"unknown set", "tee", []string{"nope"}, "option set nope does not exist on this till"},
		{"inactive set", "tee", []string{retired}, "option set Retired is inactive"},
		{"three axes", "tee", []string{size, colour, fit}, "at most 2 option sets"},
		{"duplicate", "tee", []string{size, size}, "listed twice"},
		{"blank id", "tee", []string{" "}, "option_set_id required"},
	} {
		_, err := repo.ApplyOptionSetsAndGenerate(ctx, c.item, c.ids, true)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to contain %q", c.name, err, c.want)
		}
		if got := appliedSetIDs(t, d, "tee"); len(got) != 1 || got[0] != size {
			t.Fatalf("%s: a refusal changed the applied sets to %v", c.name, got)
		}
		if n := len(readVariants(t, d, "tee")); n != 0 {
			t.Fatalf("%s: a refusal created %d variants", c.name, n)
		}
	}
}

// The cloud keeps at most MaxItemVariants per item and SaveVariant refuses
// the 101st, so generation refuses up front — before applying or creating
// anything — rather than leaving variants the cloud would never show.
func TestApplyOptionSetsAndGenerate_VariantCapRefusesBeforeWriting(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)
	seedOptionSetItem(t, d, "tee", "T-shirt", 1500)
	var a, b []string
	for i := range 11 {
		a = append(a, fmt.Sprintf("A%02d", i))
	}
	for i := range 10 {
		b = append(b, fmt.Sprintf("B%02d", i))
	}
	setA := newSetWithValues(t, repo, "Length", a...)
	setB := newSetWithValues(t, repo, "Width", b...)

	_, err := repo.ApplyOptionSetsAndGenerate(ctx, "tee", []string{setA, setB}, true)
	if !errors.Is(err, data.ErrTooManyVariants) {
		t.Fatalf("err = %v, want ErrTooManyVariants", err)
	}
	if !strings.Contains(err.Error(), "110") || !strings.Contains(err.Error(), "100") {
		t.Fatalf("message should name the count and the cap: %v", err)
	}
	if got := appliedSetIDs(t, d, "tee"); len(got) != 0 {
		t.Fatalf("a capped generate applied the sets: %v", got)
	}
	if n := len(readVariants(t, d, "tee")); n != 0 {
		t.Fatalf("a capped generate created %d variants", n)
	}

	// The till's own Generate button path refuses the same way.
	if err := repo.ApplyOptionSetsToItem(ctx, "tee", []string{setA, setB}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GenerateVariants(ctx, "tee"); !errors.Is(err, data.ErrTooManyVariants) {
		t.Fatalf("GenerateVariants err = %v, want ErrTooManyVariants", err)
	}
	if n := len(readVariants(t, d, "tee")); n != 0 {
		t.Fatalf("a capped GenerateVariants created %d variants", n)
	}
}

// Existing variants (hand-added ones included) count towards the cap.
func TestGenerateVariants_CapCountsExistingVariants(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)
	seedOptionSetItem(t, d, "tee", "T-shirt", 1500)
	for i := range 99 {
		if _, err := d.DB.Exec(`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES (?, 'tee', ?, ?, 1500, 1)`,
			fmt.Sprintf("hand-%d", i), fmt.Sprintf("HAND-%d", i), fmt.Sprintf("Hand %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	size := newSetWithValues(t, repo, "Size", "S", "M")
	if _, err := repo.ApplyOptionSetsAndGenerate(ctx, "tee", []string{size}, true); !errors.Is(err, data.ErrTooManyVariants) {
		t.Fatalf("99 + 2 > 100 must be refused, got %v", err)
	}
	one := newSetWithValues(t, repo, "One", "Only")
	if res, err := repo.ApplyOptionSetsAndGenerate(ctx, "tee", []string{one}, true); err != nil || res.Created != 1 {
		t.Fatalf("99 + 1 = 100 must be allowed: %+v %v", res, err)
	}
}

// A set deactivated after it was applied stays applied on the till (and in
// the snapshot), so re-sending the item's current list — or replaying a
// directive that applied before the deactivation — still applies and
// generates; only newly applying an inactive set is refused.
func TestApplyOptionSetsAndGenerate_KeepsAnAlreadyAppliedInactiveSet(t *testing.T) {
	d := openOptionSetTestDB(t)
	ctx := context.Background()
	repo := data.NewOptionSetRepo(d.DB)
	seedOptionSetItem(t, d, "tee", "T-shirt", 1500)
	size := newSetWithValues(t, repo, "Size", "S", "M")
	colour := newSetWithValues(t, repo, "Colour", "Red")
	if _, err := repo.ApplyOptionSetsAndGenerate(ctx, "tee", []string{size, colour}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`UPDATE option_sets SET is_active = 0 WHERE id = ?`, colour); err != nil {
		t.Fatal(err)
	}
	res, err := repo.ApplyOptionSetsAndGenerate(ctx, "tee", []string{size, colour}, true)
	if err != nil || res.Created != 2 {
		t.Fatalf("re-sending the applied list: %+v %v, want 2 created", res, err)
	}
	seedOptionSetItem(t, d, "mug", "Mug", 800)
	if _, err := repo.ApplyOptionSetsAndGenerate(ctx, "mug", []string{colour}, false); err == nil || !strings.Contains(err.Error(), "option set Colour is inactive") {
		t.Fatalf("newly applying an inactive set: %v", err)
	}
}
