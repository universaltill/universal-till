package data_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/universaltill/universal-till/catimport/catalogrules/namecases"
	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#5871 (#5860 part 3): every name a my. directive or the catalogue
// pull hands the till goes through the single-line rule, ranging the shared
// namecases table. A name equal to the stored one is tolerated (legacy rows).

func nlTx(t *testing.T, d *db.DB, fn func(tx *sql.Tx) error) error {
	t.Helper()
	tx, err := d.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func nlRead(d *db.DB, query string, args ...any) (string, bool) {
	var s string
	err := d.DB.QueryRow(query, args...).Scan(&s)
	return s, err == nil
}

func nlMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// nlRangePath is one entry point: seed (nil for a create path) makes the row
// to rename, op calls the entry point with a case's input, read returns the
// stored name afterwards, seeded the name before op ("" for a create).
type nlRangePath struct {
	label  string
	seed   func(t *testing.T, d *db.DB, i int)
	op     func(t *testing.T, d *db.DB, i int, in string) error
	read   func(d *db.DB, i int, c namecases.Case) (string, bool)
	seeded func(i int) string
}

func nlRunRange(t *testing.T, p nlRangePath) {
	t.Helper()
	d := nlOpen(t)
	for i, c := range namecases.Cases() {
		if p.seed != nil {
			p.seed(t, d, i)
		}
		err := p.op(t, d, i, c.In)
		got, found := p.read(d, i, c)
		if c.OK {
			if err != nil || !found || got != c.Want {
				t.Errorf("%s [%s]: got %q found=%v err=%v; want stored %q", p.label, c.Label, got, found, err, c.Want)
			}
			continue
		}
		if !errors.Is(err, data.ErrNameInvalidCharacters) {
			t.Errorf("%s [%s]: err = %v, want ErrNameInvalidCharacters", p.label, c.Label, err)
		}
		if p.seeded == nil {
			if found {
				t.Errorf("%s [%s]: refused call still stored %q", p.label, c.Label, got)
			}
		} else if !found || got != p.seeded(i) {
			t.Errorf("%s [%s]: refused call changed the row to %q (found=%v), want %q", p.label, c.Label, got, found, p.seeded(i))
		}
	}
}

func nlID(prefix string, i int) string { return fmt.Sprintf("%s-%d", prefix, i) }
func nlSeed(i int) string              { return fmt.Sprintf("seed-%d", i) }

func nlItemIn(i int, name string) catalogtypes.ItemInput {
	return catalogtypes.ItemInput{ID: nlID("it", i), SKU: nlID("SKU", i), Name: name, BasePrice: 100, IsActive: true}
}

func nlItemName(d *db.DB, i int, _ namecases.Case) (string, bool) {
	return nlRead(d, `SELECT name FROM items WHERE id = ?`, nlID("it", i))
}

func nlSeedItem(t *testing.T, d *db.DB, i int) {
	_, err := data.NewCatalogRepo(d.DB).CreateItem(context.Background(), nlItemIn(i, nlSeed(i)))
	nlMust(t, err)
}

func TestSingleLineName_ItemDirectivePathsChecked(t *testing.T) {
	ctx := context.Background()
	repo := func(d *db.DB) *data.CatalogRepo { return data.NewCatalogRepo(d.DB) }
	create := func(t *testing.T, d *db.DB, i int, in string) error {
		_, err := repo(d).CreateItem(ctx, nlItemIn(i, in))
		return err
	}
	paths := []nlRangePath{
		{label: "CreateItem", op: create, read: nlItemName},
		{label: "CreateItemTx", read: nlItemName, op: func(t *testing.T, d *db.DB, i int, in string) error {
			return nlTx(t, d, func(tx *sql.Tx) error {
				_, err := repo(d).CreateItemTx(ctx, tx, nlItemIn(i, in))
				return err
			})
		}},
		{label: "SaveItem create", read: nlItemName, op: func(t *testing.T, d *db.DB, i int, in string) error {
			_, err := repo(d).SaveItem(ctx, data.ItemPatch{ID: nlID("it", i), Create: true, Name: &in, PriceMinor: i64p(100)})
			return err
		}},
		{label: "SaveItem rename", seed: nlSeedItem, seeded: nlSeed, read: nlItemName, op: func(t *testing.T, d *db.DB, i int, in string) error {
			_, err := repo(d).SaveItem(ctx, data.ItemPatch{ID: nlID("it", i), Name: &in})
			return err
		}},
		{label: "SaveItemTx rename", seed: nlSeedItem, seeded: nlSeed, read: nlItemName, op: func(t *testing.T, d *db.DB, i int, in string) error {
			return nlTx(t, d, func(tx *sql.Tx) error {
				_, err := repo(d).SaveItemTx(ctx, tx, data.ItemPatch{ID: nlID("it", i), Name: &in})
				return err
			})
		}},
		{label: "SetItemName item", seed: nlSeedItem, seeded: nlSeed, read: nlItemName, op: func(t *testing.T, d *db.DB, i int, in string) error {
			return repo(d).SetItemName(ctx, nlID("it", i), in)
		}},
		{label: "SetItemName variant", seeded: nlSeed,
			seed: func(t *testing.T, d *db.DB, i int) {
				nlSeedItem(t, d, i)
				_, err := repo(d).SaveVariant(ctx, data.VariantSave{ItemID: nlID("it", i), ID: nlID("v", i), Create: true, Name: strp(nlSeed(i)), PriceMinor: i64p(100)})
				nlMust(t, err)
			},
			read: func(d *db.DB, i int, _ namecases.Case) (string, bool) {
				return nlRead(d, `SELECT name FROM item_variants WHERE id = ?`, nlID("v", i))
			},
			op: func(t *testing.T, d *db.DB, i int, in string) error {
				return repo(d).SetItemName(ctx, nlID("v", i), in)
			}},
	}
	for _, p := range paths {
		t.Run(p.label, func(t *testing.T) { nlRunRange(t, p) })
	}
	// A whitespace-only name stays "name required", never a blank row.
	d := nlOpen(t)
	if _, err := repo(d).CreateItem(ctx, nlItemIn(0, "   ")); err == nil || errors.Is(err, data.ErrNameInvalidCharacters) {
		t.Fatalf("blank CreateItem err = %v, want name required", err)
	}
	// SetItemName stores the trimmed name.
	nlSeedItem(t, d, 1)
	nlMust(t, repo(d).SetItemName(ctx, nlID("it", 1), "  Tea  "))
	if got, _ := nlItemName(d, 1, namecases.Case{}); got != "Tea" {
		t.Fatalf("SetItemName stored %q, want Tea", got)
	}
}

func nlCatName(d *db.DB, i int, _ namecases.Case) (string, bool) {
	return nlRead(d, `SELECT name FROM categories WHERE id = ?`, nlID("cat", i))
}

func nlSeedCat(t *testing.T, d *db.DB, i int) {
	_, err := data.NewCatalogRepo(d.DB).SaveCategory(context.Background(), data.CategorySave{ID: nlID("cat", i), Create: true, Name: strp(nlSeed(i))})
	nlMust(t, err)
}

func TestSingleLineName_CategoryDirectivePathsChecked(t *testing.T) {
	ctx := context.Background()
	repo := func(d *db.DB) *data.CatalogRepo { return data.NewCatalogRepo(d.DB) }
	paths := []nlRangePath{
		{label: "CreateCategoryWithColor", op: func(t *testing.T, d *db.DB, i int, in string) error {
			_, err := repo(d).CreateCategoryWithColor(ctx, in, "")
			return err
		}, read: func(d *db.DB, i int, c namecases.Case) (string, bool) {
			want := c.Want
			if !c.OK {
				want = c.In
			}
			return nlRead(d, `SELECT name FROM categories WHERE name = ?`, want)
		}},
		{label: "SaveCategory create", read: nlCatName, op: func(t *testing.T, d *db.DB, i int, in string) error {
			_, err := repo(d).SaveCategory(ctx, data.CategorySave{ID: nlID("cat", i), Create: true, Name: &in})
			return err
		}},
		{label: "SaveCategoryTx create", read: nlCatName, op: func(t *testing.T, d *db.DB, i int, in string) error {
			return nlTx(t, d, func(tx *sql.Tx) error {
				_, err := repo(d).SaveCategoryTx(ctx, tx, data.CategorySave{ID: nlID("cat", i), Create: true, Name: &in})
				return err
			})
		}},
		{label: "SaveCategory rename", seed: nlSeedCat, seeded: nlSeed, read: nlCatName, op: func(t *testing.T, d *db.DB, i int, in string) error {
			_, err := repo(d).SaveCategory(ctx, data.CategorySave{ID: nlID("cat", i), Name: &in})
			return err
		}},
		{label: "SaveCategoryTx rename", seed: nlSeedCat, seeded: nlSeed, read: nlCatName, op: func(t *testing.T, d *db.DB, i int, in string) error {
			return nlTx(t, d, func(tx *sql.Tx) error {
				_, err := repo(d).SaveCategoryTx(ctx, tx, data.CategorySave{ID: nlID("cat", i), Name: &in})
				return err
			})
		}},
		{label: "UpdateCategory", seed: nlSeedCat, seeded: nlSeed, read: nlCatName, op: func(t *testing.T, d *db.DB, i int, in string) error {
			return repo(d).UpdateCategory(ctx, nlID("cat", i), in, "")
		}},
		{label: "UpdateCategoryPartial", seed: nlSeedCat, seeded: nlSeed, read: nlCatName, op: func(t *testing.T, d *db.DB, i int, in string) error {
			_, err := repo(d).UpdateCategoryPartial(ctx, nlID("cat", i), data.CategoryPatch{Name: &in})
			return err
		}},
	}
	for _, p := range paths {
		t.Run(p.label, func(t *testing.T) { nlRunRange(t, p) })
	}
}

func nlGroupName(d *db.DB, i int, _ namecases.Case) (string, bool) {
	return nlRead(d, `SELECT name FROM item_modifier_groups WHERE id = ?`, nlID("g", i))
}

func nlSeedGroup(t *testing.T, d *db.DB, i int) {
	_, err := data.NewModifierRepo(d.DB).SaveGroup(context.Background(), data.ModifierGroupSave{ID: nlID("g", i), Create: true, Name: strp(nlSeed(i))})
	nlMust(t, err)
}

func nlOptName(d *db.DB, i int, _ namecases.Case) (string, bool) {
	return nlRead(d, `SELECT name FROM item_modifier_options WHERE id = ?`, nlID("o", i))
}

func TestSingleLineName_SaveGroupChecked(t *testing.T) {
	ctx := context.Background()
	repo := func(d *db.DB) *data.ModifierRepo { return data.NewModifierRepo(d.DB) }
	opt := func(i int, name string) []data.ModifierOption {
		return []data.ModifierOption{{ID: nlID("o", i), Name: name, IsActive: true}}
	}
	paths := []nlRangePath{
		{label: "SaveGroup create", read: nlGroupName, op: func(t *testing.T, d *db.DB, i int, in string) error {
			_, err := repo(d).SaveGroup(ctx, data.ModifierGroupSave{ID: nlID("g", i), Create: true, Name: &in})
			return err
		}},
		{label: "SaveGroupTx create", read: nlGroupName, op: func(t *testing.T, d *db.DB, i int, in string) error {
			return nlTx(t, d, func(tx *sql.Tx) error {
				_, err := repo(d).SaveGroupTx(ctx, tx, data.ModifierGroupSave{ID: nlID("g", i), Create: true, Name: &in})
				return err
			})
		}},
		{label: "SaveGroup rename", seed: nlSeedGroup, seeded: nlSeed, read: nlGroupName, op: func(t *testing.T, d *db.DB, i int, in string) error {
			_, err := repo(d).SaveGroup(ctx, data.ModifierGroupSave{ID: nlID("g", i), Name: &in})
			return err
		}},
		{label: "SaveGroup new option", seed: nlSeedGroup, read: nlOptName, op: func(t *testing.T, d *db.DB, i int, in string) error {
			o := opt(i, in)
			_, err := repo(d).SaveGroup(ctx, data.ModifierGroupSave{ID: nlID("g", i), Options: &o})
			return err
		}},
		{label: "SaveGroupTx renamed option", seeded: nlSeed, read: nlOptName,
			seed: func(t *testing.T, d *db.DB, i int) {
				nlSeedGroup(t, d, i)
				o := opt(i, nlSeed(i))
				_, err := repo(d).SaveGroup(ctx, data.ModifierGroupSave{ID: nlID("g", i), Options: &o})
				nlMust(t, err)
			},
			op: func(t *testing.T, d *db.DB, i int, in string) error {
				o := opt(i, in)
				return nlTx(t, d, func(tx *sql.Tx) error {
					_, err := repo(d).SaveGroupTx(ctx, tx, data.ModifierGroupSave{ID: nlID("g", i), Options: &o})
					return err
				})
			}},
		{label: "CreateGroupWithOptions group", read: nlGroupName, op: func(t *testing.T, d *db.DB, i int, in string) error {
			_, err := repo(d).CreateGroupWithOptions(ctx, nlID("g", i), "", in, false, 0, 1, 0, nil)
			return err
		}},
		{label: "CreateGroupWithOptions option", read: nlOptName, op: func(t *testing.T, d *db.DB, i int, in string) error {
			_, err := repo(d).CreateGroupWithOptions(ctx, nlID("g", i), "", nlSeed(i), false, 0, 1, 0,
				[]data.ModifierOption{{Name: "fine"}, {ID: nlID("o", i), Name: in}})
			return err
		}},
	}
	for _, p := range paths {
		t.Run(p.label, func(t *testing.T) { nlRunRange(t, p) })
	}
}

// A refused save leaves the group and every option unchanged.
func TestSingleLineName_SaveGroupRefusalWritesNothing(t *testing.T) {
	ctx := context.Background()
	d := nlOpen(t)
	repo := data.NewModifierRepo(d.DB)
	opts := []data.ModifierOption{{ID: "o1", Name: "One", IsActive: true}, {ID: "o2", Name: "Two", IsActive: true}}
	_, err := repo.SaveGroup(ctx, data.ModifierGroupSave{ID: "g1", Create: true, Name: strp("Sauces"), Options: &opts})
	nlMust(t, err)
	bad := []data.ModifierOption{{ID: "o1", Name: "Uno", IsActive: true}, {ID: "o2", Name: "Do\u202es", IsActive: true}, {ID: "o3", Name: "Three", IsActive: true}}
	_, err = repo.SaveGroup(ctx, data.ModifierGroupSave{ID: "g1", Name: strp("Dips"), Options: &bad})
	nlRefused(t, err)
	if got, _ := nlRead(d, `SELECT name FROM item_modifier_groups WHERE id = 'g1'`); got != "Sauces" {
		t.Fatalf("group name = %q, want Sauces", got)
	}
	if got, _ := nlRead(d, `SELECT group_concat(name, ',') FROM (SELECT name FROM item_modifier_options WHERE group_id = 'g1' ORDER BY id)`); got != "One,Two" {
		t.Fatalf("options = %q, want One,Two", got)
	}
}

func TestSingleLineName_CreateGroupWithOptionsChecked(t *testing.T) {
	ctx := context.Background()
	d := nlOpen(t)
	nlMust(t, func() error { _, err := data.NewCatalogRepo(d.DB).CreateItem(ctx, nlItemIn(0, "Host")); return err }())
	repo := data.NewModifierRepo(d.DB)
	count := func(q string) int {
		var n int
		if err := d.DB.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	empty := func(label string) {
		t.Helper()
		if g, o, l := count(`SELECT COUNT(*) FROM item_modifier_groups`), count(`SELECT COUNT(*) FROM item_modifier_options`), count(`SELECT COUNT(*) FROM item_modifier_group_links`); g+o+l != 0 {
			t.Fatalf("%s: left %d groups, %d options, %d links", label, g, o, l)
		}
	}
	_, err := repo.CreateGroupWithOptions(ctx, "g1", nlID("it", 0), "Sa\u202euce", false, 0, 1, 0, []data.ModifierOption{{Name: "Hot"}})
	nlRefused(t, err)
	empty("bad group name")
	_, err = repo.CreateGroupWithOptions(ctx, "g1", nlID("it", 0), "Sauce", false, 0, 1, 0, []data.ModifierOption{{Name: "Hot"}, {Name: "Mi\x1bld"}})
	nlRefused(t, err)
	empty("bad second option name")
	in := []data.ModifierOption{{Name: "  Hot  "}}
	if _, err := repo.CreateGroupWithOptions(ctx, "g1", nlID("it", 0), "  Sauce ", false, 0, 1, 0, in); err != nil {
		t.Fatal(err)
	}
	if in[0].Name != "  Hot  " {
		t.Fatalf("caller's slice mutated: %q", in[0].Name)
	}
	if g, _ := nlRead(d, `SELECT name FROM item_modifier_groups WHERE id = 'g1'`); g != "Sauce" {
		t.Fatalf("group = %q, want trimmed Sauce", g)
	}
	if o, _ := nlRead(d, `SELECT name FROM item_modifier_options WHERE group_id = 'g1'`); o != "Hot" {
		t.Fatalf("option = %q, want trimmed Hot", o)
	}
}

func nlSetName(d *db.DB, i int, _ namecases.Case) (string, bool) {
	return nlRead(d, `SELECT name FROM option_sets WHERE id = ?`, nlID("s", i))
}

func nlValue(d *db.DB, i int, _ namecases.Case) (string, bool) {
	return nlRead(d, `SELECT value FROM option_set_values WHERE id = ?`, nlID("v", i))
}

func nlSeedSet(t *testing.T, d *db.DB, i int) {
	_, err := data.NewOptionSetRepo(d.DB).SaveOptionSet(context.Background(), data.OptionSetSave{ID: nlID("s", i), Create: true, Name: strp(nlSeed(i))})
	nlMust(t, err)
}

func TestSingleLineName_SaveOptionSetChecked(t *testing.T) {
	ctx := context.Background()
	repo := func(d *db.DB) *data.OptionSetRepo { return data.NewOptionSetRepo(d.DB) }
	vals := func(i int, v string) *[]data.OptionSetValueInput {
		return &[]data.OptionSetValueInput{{ID: nlID("v", i), Value: v}}
	}
	paths := []nlRangePath{
		{label: "SaveOptionSet create", read: nlSetName, op: func(t *testing.T, d *db.DB, i int, in string) error {
			_, err := repo(d).SaveOptionSet(ctx, data.OptionSetSave{ID: nlID("s", i), Create: true, Name: &in})
			return err
		}},
		{label: "SaveOptionSetTx create", read: nlSetName, op: func(t *testing.T, d *db.DB, i int, in string) error {
			return nlTx(t, d, func(tx *sql.Tx) error {
				_, err := repo(d).SaveOptionSetTx(ctx, tx, data.OptionSetSave{ID: nlID("s", i), Create: true, Name: &in})
				return err
			})
		}},
		{label: "SaveOptionSet rename", seed: nlSeedSet, seeded: nlSeed, read: nlSetName, op: func(t *testing.T, d *db.DB, i int, in string) error {
			_, err := repo(d).SaveOptionSet(ctx, data.OptionSetSave{ID: nlID("s", i), Name: &in})
			return err
		}},
		{label: "SaveOptionSet new value", seed: nlSeedSet, read: nlValue, op: func(t *testing.T, d *db.DB, i int, in string) error {
			_, err := repo(d).SaveOptionSet(ctx, data.OptionSetSave{ID: nlID("s", i), Values: vals(i, in)})
			return err
		}},
		{label: "SaveOptionSetTx changed value", seeded: nlSeed, read: nlValue,
			seed: func(t *testing.T, d *db.DB, i int) {
				nlSeedSet(t, d, i)
				_, err := repo(d).SaveOptionSet(ctx, data.OptionSetSave{ID: nlID("s", i), Values: vals(i, nlSeed(i))})
				nlMust(t, err)
			},
			op: func(t *testing.T, d *db.DB, i int, in string) error {
				return nlTx(t, d, func(tx *sql.Tx) error {
					_, err := repo(d).SaveOptionSetTx(ctx, tx, data.OptionSetSave{ID: nlID("s", i), Values: vals(i, in)})
					return err
				})
			}},
	}
	for _, p := range paths {
		t.Run(p.label, func(t *testing.T) { nlRunRange(t, p) })
	}
}

func TestNormalizeOptionSetValues_ControlIsBothErrors(t *testing.T) {
	d := nlOpen(t)
	repo := data.NewOptionSetRepo(d.DB)
	nlMust(t, func() error { _, err := repo.CreateOptionSet(context.Background(), "Sizes"); return err }())
	sets, _ := repo.ListOptionSets(context.Background())
	v := []data.OptionSetValueInput{{ID: "v1", Value: "a\x1bb"}}
	_, err := repo.SaveOptionSet(context.Background(), data.OptionSetSave{ID: sets[0].ID, Values: &v})
	if !errors.Is(err, data.ErrOptionSetValueInvalid) || !errors.Is(err, data.ErrNameInvalidCharacters) {
		t.Fatalf("err = %v, want both ErrOptionSetValueInvalid and ErrNameInvalidCharacters", err)
	}
}

func TestSingleLineName_SaveVariantChecked(t *testing.T) {
	ctx := context.Background()
	repo := func(d *db.DB) *data.CatalogRepo { return data.NewCatalogRepo(d.DB) }
	read := func(d *db.DB, i int, _ namecases.Case) (string, bool) {
		return nlRead(d, `SELECT name FROM item_variants WHERE id = ?`, nlID("v", i))
	}
	seedVar := func(t *testing.T, d *db.DB, i int) {
		nlSeedItem(t, d, i)
		_, err := repo(d).SaveVariant(ctx, data.VariantSave{ItemID: nlID("it", i), ID: nlID("v", i), Create: true, Name: strp(nlSeed(i)), PriceMinor: i64p(100)})
		nlMust(t, err)
	}
	patch := func(i int, in string, create bool) data.VariantSave {
		p := data.VariantSave{ItemID: nlID("it", i), ID: nlID("v", i), Name: &in}
		if create {
			p.Create, p.PriceMinor = true, i64p(100)
		}
		return p
	}
	paths := []nlRangePath{
		{label: "SaveVariant create", read: read, seed: nlSeedItem, op: func(t *testing.T, d *db.DB, i int, in string) error {
			_, err := repo(d).SaveVariant(ctx, patch(i, in, true))
			return err
		}},
		{label: "SaveVariantTx create", read: read, seed: nlSeedItem, op: func(t *testing.T, d *db.DB, i int, in string) error {
			return nlTx(t, d, func(tx *sql.Tx) error {
				_, err := repo(d).SaveVariantTx(ctx, tx, patch(i, in, true))
				return err
			})
		}},
		{label: "SaveVariant rename", read: read, seed: seedVar, seeded: nlSeed, op: func(t *testing.T, d *db.DB, i int, in string) error {
			_, err := repo(d).SaveVariant(ctx, patch(i, in, false))
			return err
		}},
		{label: "SaveVariantTx rename", read: read, seed: seedVar, seeded: nlSeed, op: func(t *testing.T, d *db.DB, i int, in string) error {
			return nlTx(t, d, func(tx *sql.Tx) error {
				_, err := repo(d).SaveVariantTx(ctx, tx, patch(i, in, false))
				return err
			})
		}},
	}
	for _, p := range paths {
		t.Run(p.label, func(t *testing.T) {
			// seed for create paths only makes the parent item; reuse the DB
			// per path, so each case needs its own item id (nlID("it", i)).
			nlRunRange(t, p)
		})
	}
}

func TestSingleLineName_SaveTaxCodeTxChecked(t *testing.T) {
	ctx := context.Background()
	repo := func(d *db.DB) *data.CatalogRepo { return data.NewCatalogRepo(d.DB) }
	read := func(d *db.DB, i int, _ namecases.Case) (string, bool) {
		return nlRead(d, `SELECT name FROM tax_codes WHERE id = ?`, nlID("tc", i))
	}
	save := func(t *testing.T, d *db.DB, i int, name string, create bool) error {
		return nlTx(t, d, func(tx *sql.Tx) error {
			s := data.TaxCodeSave{ID: nlID("tc", i), Create: create, Name: &name}
			if create {
				s.RateBP = intp(1000)
			}
			_, err := repo(d).SaveTaxCodeTx(ctx, tx, s, nil)
			return err
		})
	}
	paths := []nlRangePath{
		{label: "SaveTaxCodeTx create", read: read, op: func(t *testing.T, d *db.DB, i int, in string) error {
			return save(t, d, i, in, true)
		}},
		{label: "SaveTaxCodeTx rename", read: read, seeded: nlSeed,
			seed: func(t *testing.T, d *db.DB, i int) { nlMust(t, save(t, d, i, nlSeed(i), true)) },
			op: func(t *testing.T, d *db.DB, i int, in string) error {
				return save(t, d, i, in, false)
			}},
	}
	for _, p := range paths {
		t.Run(p.label, func(t *testing.T) { nlRunRange(t, p) })
	}
}

// Legacy tolerance (#5860 R8): a row stored before the rule stays editable
// when my. resends its name with another field changed.
type nlTol struct {
	label  string
	create func(t *testing.T, d *db.DB)
	setBad func(t *testing.T, d *db.DB, bad string)
	apply  func(t *testing.T, d *db.DB, name string) error
	read   func(d *db.DB) (name, other string)
	before string // other field before / after a successful apply
	after  string
}

func nlExec(t *testing.T, d *db.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.DB.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func TestSingleLineName_ReceivedNameLegacyTolerance(t *testing.T) {
	ctx := context.Background()
	cat := func(d *db.DB) *data.CatalogRepo { return data.NewCatalogRepo(d.DB) }
	mod := func(d *db.DB) *data.ModifierRepo { return data.NewModifierRepo(d.DB) }
	sets := func(d *db.DB) *data.OptionSetRepo { return data.NewOptionSetRepo(d.DB) }
	colour := catalogtypes.ItemColors()[1].Hex
	itemName := func(d *db.DB) (string, string) {
		n, _ := nlRead(d, `SELECT name FROM items WHERE id = 'x'`)
		p, _ := nlRead(d, `SELECT CAST(base_price AS TEXT) FROM items WHERE id = 'x'`)
		return n, p
	}
	seedItem := func(t *testing.T, d *db.DB) {
		_, err := cat(d).CreateItem(ctx, catalogtypes.ItemInput{ID: "x", SKU: "X-1", Name: "Cola", BasePrice: 100, IsActive: true})
		nlMust(t, err)
	}
	badItem := func(t *testing.T, d *db.DB, bad string) {
		nlExec(t, d, `UPDATE items SET name = ? WHERE id = 'x'`, bad)
	}
	seedCat := func(t *testing.T, d *db.DB) {
		_, err := cat(d).SaveCategory(ctx, data.CategorySave{ID: "x", Create: true, Name: strp("Cola")})
		nlMust(t, err)
	}
	badCat := func(t *testing.T, d *db.DB, bad string) {
		nlExec(t, d, `UPDATE categories SET name = ? WHERE id = 'x'`, bad)
	}
	readCat := func(d *db.DB) (string, string) {
		n, _ := nlRead(d, `SELECT name FROM categories WHERE id = 'x'`)
		c, _ := nlRead(d, `SELECT COALESCE(color, '') FROM categories WHERE id = 'x'`)
		return n, c
	}
	seedGroup := func(t *testing.T, d *db.DB) {
		opts := []data.ModifierOption{{ID: "o1", Name: "Cola", IsActive: true}}
		_, err := mod(d).SaveGroup(ctx, data.ModifierGroupSave{ID: "x", Create: true, Name: strp("Cola"), Options: &opts})
		nlMust(t, err)
	}
	seedSet := func(t *testing.T, d *db.DB) {
		v := []data.OptionSetValueInput{{ID: "v1", Value: "Cola"}}
		_, err := sets(d).SaveOptionSet(ctx, data.OptionSetSave{ID: "x", Create: true, Name: strp("Cola"), Values: &v})
		nlMust(t, err)
	}
	seedVariant := func(t *testing.T, d *db.DB) {
		seedItem(t, d)
		_, err := cat(d).SaveVariant(ctx, data.VariantSave{ItemID: "x", ID: "var", Create: true, Name: strp("Cola"), PriceMinor: i64p(100)})
		nlMust(t, err)
	}
	readVar := func(d *db.DB) (string, string) {
		n, _ := nlRead(d, `SELECT name FROM item_variants WHERE id = 'var'`)
		p, _ := nlRead(d, `SELECT CAST(price AS TEXT) FROM item_variants WHERE id = 'var'`)
		return n, p
	}
	seedTax := func(t *testing.T, d *db.DB) {
		nlMust(t, nlTx(t, d, func(tx *sql.Tx) error {
			_, err := cat(d).SaveTaxCodeTx(ctx, tx, data.TaxCodeSave{ID: "x", Create: true, Name: strp("Cola"), RateBP: intp(1000)}, nil)
			return err
		}))
	}
	itemPatch := func(name string) data.ItemPatch { return data.ItemPatch{ID: "x", Name: &name, PriceMinor: i64p(777)} }

	cases := []nlTol{
		{label: "SaveItem", create: seedItem, setBad: badItem, read: itemName, before: "100", after: "777",
			apply: func(t *testing.T, d *db.DB, n string) error { _, err := cat(d).SaveItem(ctx, itemPatch(n)); return err }},
		{label: "SaveItemTx", create: seedItem, setBad: badItem, read: itemName, before: "100", after: "777",
			apply: func(t *testing.T, d *db.DB, n string) error {
				return nlTx(t, d, func(tx *sql.Tx) error { _, err := cat(d).SaveItemTx(ctx, tx, itemPatch(n)); return err })
			}},
		{label: "SetItemName item", create: seedItem, setBad: badItem,
			read:  func(d *db.DB) (string, string) { n, _ := itemName(d); return n, "" },
			apply: func(t *testing.T, d *db.DB, n string) error { return cat(d).SetItemName(ctx, "x", n) }},
		{label: "SetItemName variant", create: seedVariant,
			setBad: func(t *testing.T, d *db.DB, bad string) {
				nlExec(t, d, `UPDATE item_variants SET name = ? WHERE id = 'var'`, bad)
			},
			read:  func(d *db.DB) (string, string) { n, _ := readVar(d); return n, "" },
			apply: func(t *testing.T, d *db.DB, n string) error { return cat(d).SetItemName(ctx, "var", n) }},
		{label: "SaveCategory", create: seedCat, setBad: badCat, read: readCat, before: "", after: colour,
			apply: func(t *testing.T, d *db.DB, n string) error {
				_, err := cat(d).SaveCategory(ctx, data.CategorySave{ID: "x", Name: &n, Color: &colour})
				return err
			}},
		{label: "UpdateCategory", create: seedCat, setBad: badCat, read: readCat, before: "", after: colour,
			apply: func(t *testing.T, d *db.DB, n string) error { return cat(d).UpdateCategory(ctx, "x", n, colour) }},
		{label: "UpdateCategoryPartial", create: seedCat, setBad: badCat, read: readCat, before: "", after: colour,
			apply: func(t *testing.T, d *db.DB, n string) error {
				_, err := cat(d).UpdateCategoryPartial(ctx, "x", data.CategoryPatch{Name: &n, Color: &colour})
				return err
			}},
		{label: "SaveGroup name", create: seedGroup,
			setBad: func(t *testing.T, d *db.DB, bad string) {
				nlExec(t, d, `UPDATE item_modifier_groups SET name = ? WHERE id = 'x'`, bad)
			},
			read: func(d *db.DB) (string, string) {
				n, _ := nlRead(d, `SELECT name FROM item_modifier_groups WHERE id = 'x'`)
				m, _ := nlRead(d, `SELECT CAST(max_select AS TEXT) FROM item_modifier_groups WHERE id = 'x'`)
				return n, m
			}, before: "1", after: "3",
			apply: func(t *testing.T, d *db.DB, n string) error {
				_, err := mod(d).SaveGroup(ctx, data.ModifierGroupSave{ID: "x", Name: &n, MaxSelect: intp(3)})
				return err
			}},
		{label: "SaveGroup option", create: seedGroup,
			setBad: func(t *testing.T, d *db.DB, bad string) {
				nlExec(t, d, `UPDATE item_modifier_options SET name = ? WHERE id = 'o1'`, bad)
			},
			read: func(d *db.DB) (string, string) {
				n, _ := nlRead(d, `SELECT name FROM item_modifier_options WHERE id = 'o1'`)
				c, _ := nlRead(d, `SELECT CAST(COUNT(*) AS TEXT) FROM item_modifier_options WHERE group_id = 'x'`)
				return n, c
			}, before: "1", after: "2",
			apply: func(t *testing.T, d *db.DB, n string) error {
				o := []data.ModifierOption{{ID: "o1", Name: n, IsActive: true}, {ID: "o2", Name: "Extra", IsActive: true}}
				_, err := mod(d).SaveGroup(ctx, data.ModifierGroupSave{ID: "x", Options: &o})
				return err
			}},
		{label: "SaveOptionSet name", create: seedSet,
			setBad: func(t *testing.T, d *db.DB, bad string) {
				nlExec(t, d, `UPDATE option_sets SET name = ? WHERE id = 'x'`, bad)
			},
			read: func(d *db.DB) (string, string) {
				n, _ := nlRead(d, `SELECT name FROM option_sets WHERE id = 'x'`)
				a, _ := nlRead(d, `SELECT CAST(is_active AS TEXT) FROM option_sets WHERE id = 'x'`)
				return n, a
			}, before: "1", after: "0",
			apply: func(t *testing.T, d *db.DB, n string) error {
				_, err := sets(d).SaveOptionSet(ctx, data.OptionSetSave{ID: "x", Name: &n, Active: boolp(false)})
				return err
			}},
		{label: "SaveOptionSet value", create: seedSet,
			setBad: func(t *testing.T, d *db.DB, bad string) {
				nlExec(t, d, `UPDATE option_set_values SET value = ? WHERE id = 'v1'`, bad)
			},
			read: func(d *db.DB) (string, string) {
				n, _ := nlRead(d, `SELECT value FROM option_set_values WHERE id = 'v1'`)
				c, _ := nlRead(d, `SELECT CAST(COUNT(*) AS TEXT) FROM option_set_values WHERE option_set_id = 'x'`)
				return n, c
			}, before: "1", after: "2",
			apply: func(t *testing.T, d *db.DB, n string) error {
				v := []data.OptionSetValueInput{{ID: "v1", Value: n}, {ID: "v2", Value: "Extra"}}
				_, err := sets(d).SaveOptionSet(ctx, data.OptionSetSave{ID: "x", Values: &v})
				return err
			}},
		{label: "SaveVariant", create: seedVariant,
			setBad: func(t *testing.T, d *db.DB, bad string) {
				nlExec(t, d, `UPDATE item_variants SET name = ? WHERE id = 'var'`, bad)
			},
			read: readVar, before: "100", after: "777",
			apply: func(t *testing.T, d *db.DB, n string) error {
				_, err := cat(d).SaveVariant(ctx, data.VariantSave{ItemID: "x", ID: "var", Name: &n, PriceMinor: i64p(777)})
				return err
			}},
		{label: "SaveTaxCodeTx", create: seedTax,
			setBad: func(t *testing.T, d *db.DB, bad string) {
				nlExec(t, d, `UPDATE tax_codes SET name = ? WHERE id = 'x'`, bad)
			},
			read: func(d *db.DB) (string, string) {
				n, _ := nlRead(d, `SELECT name FROM tax_codes WHERE id = 'x'`)
				r, _ := nlRead(d, `SELECT CAST(rate_basis_points AS TEXT) FROM tax_codes WHERE id = 'x'`)
				return n, r
			}, before: "1000", after: "777",
			apply: func(t *testing.T, d *db.DB, n string) error {
				return nlTx(t, d, func(tx *sql.Tx) error {
					_, err := cat(d).SaveTaxCodeTx(ctx, tx, data.TaxCodeSave{ID: "x", Name: &n, RateBP: intp(777)}, nil)
					return err
				})
			}},
	}
	const bad, other = "Co\u202ela", "Co\u202dla"
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			d := nlOpen(t)
			c.create(t, d)
			c.setBad(t, d, bad)
			// (b) a different bad name is refused and the row is unchanged.
			nlRefused(t, c.apply(t, d, other))
			if n, o := c.read(d); n != bad || o != c.before {
				t.Fatalf("after refusal: name %q other %q, want %q / %q", n, o, bad, c.before)
			}
			// (a) the stored bad name resent, another field changed: applied.
			if err := c.apply(t, d, "  "+bad+" "); err != nil {
				t.Fatalf("resending the stored name was refused: %v", err)
			}
			if n, o := c.read(d); n != bad || o != c.after {
				t.Fatalf("after tolerated resend: name %q other %q, want %q / %q", n, o, bad, c.after)
			}
			// (c) a good name is accepted.
			if err := c.apply(t, d, "Cola"); err != nil {
				t.Fatalf("good name refused: %v", err)
			}
			if n, _ := c.read(d); n != "Cola" {
				t.Fatalf("name = %q, want Cola", n)
			}
		})
	}
}

// A set stored before the rule (bad legacy name) must still retire: the
// freed name is the till's own data, not a received name (review F1).
func TestRetireOptionSet_LegacyBadNameStillRetires_5871(t *testing.T) {
	d := nlOpen(t)
	nlSeedSet(t, d, 1)
	bad := "Si\u202eze"
	nlExec(t, d, `UPDATE option_sets SET name = ? WHERE id = ?`, bad, nlID("s", 1))
	repo := data.NewOptionSetRepo(d.DB)
	nlMust(t, nlTx(t, d, func(tx *sql.Tx) error {
		return repo.RetireOptionSetTx(context.Background(), tx, nlID("s", 1))
	}))
	got, ok := nlSetName(d, 1, namecases.Case{})
	if !ok || got != bad+" (s-1)" {
		t.Fatalf("freed name = %q ok=%v, want %q", got, ok, bad+" (s-1)")
	}
	var active int
	nlMust(t, d.DB.QueryRow(`SELECT is_active FROM option_sets WHERE id = ?`, nlID("s", 1)).Scan(&active))
	if active != 0 {
		t.Fatal("set still active after retire")
	}
}
