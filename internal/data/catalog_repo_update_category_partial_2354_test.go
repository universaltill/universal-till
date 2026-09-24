package data_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#2354: the cloud's update_category directive edits an EXISTING
// category partially — nil means "keep", non-nil means "set" — and writes
// the row plus both link sets in ONE transaction, refusing the whole edit
// when any referenced group or station doesn't exist.

type catPartialFixture struct {
	d       *db.DB
	catalog *data.CatalogRepo
	mods    *data.ModifierRepo
	pos     *data.POSRepo
	station string
}

func newCatPartialFixture(t *testing.T) catPartialFixture {
	t.Helper()
	d := openModifierTestDB(t)
	seedCategoryFixture(t, d)
	f := catPartialFixture{
		d:       d,
		catalog: data.NewCatalogRepo(d.DB),
		mods:    data.NewModifierRepo(d.DB),
		pos:     data.NewPOSRepo(d.DB),
	}
	ctx := context.Background()
	if _, err := f.d.DB.ExecContext(ctx, `UPDATE categories SET color = '#0f172a' WHERE id = 'cat1'`); err != nil {
		t.Fatal(err)
	}
	createAnchoredGroup(t, f.mods, "grp-milk", "Milk", 0)
	createAnchoredGroup(t, f.mods, "grp-size", "Size", 1)
	st, err := f.pos.CreateKitchenStation(ctx, "Bar", "printer", "")
	if err != nil {
		t.Fatalf("CreateKitchenStation: %v", err)
	}
	f.station = st
	return f
}

func (f catPartialFixture) row(t *testing.T) data.CategoryAdminRow {
	t.Helper()
	rows, err := f.catalog.ListCategoriesForAdmin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == "cat1" {
			return r
		}
	}
	t.Fatalf("cat1 missing")
	return data.CategoryAdminRow{}
}

func (f catPartialFixture) groups(t *testing.T) []string {
	t.Helper()
	gs, err := f.mods.ListAllGroupsForCategory(context.Background(), "cat1")
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, g := range gs {
		out = append(out, g.ID)
	}
	return out
}

func (f catPartialFixture) stations(t *testing.T) []string {
	t.Helper()
	ss, err := f.pos.CategoryStationRoutes(context.Background(), "cat1")
	if err != nil {
		t.Fatal(err)
	}
	if ss == nil {
		ss = []string{}
	}
	return ss
}

func strp(s string) *string        { return &s }
func idsp(ids ...string) *[]string { return &ids }

func TestUpdateCategoryPartial_NilKeepsEverything(t *testing.T) {
	f := newCatPartialFixture(t)
	ctx := context.Background()
	if err := f.mods.SetCategoryModifierGroups(ctx, "cat1", []string{"grp-milk"}); err != nil {
		t.Fatal(err)
	}
	if err := f.pos.SetCategoryStationRoutes(ctx, "cat1", []string{f.station}); err != nil {
		t.Fatal(err)
	}

	res, err := f.catalog.UpdateCategoryPartial(ctx, "cat1", data.CategoryPatch{Name: strp("Hot drinks")})
	if err != nil || res.Name != "Hot drinks" || res.GroupIDs != nil || res.StationIDs != nil {
		t.Fatalf("rename: res=%+v err=%v (untouched link sets must report nil)", res, err)
	}
	r := f.row(t)
	if r.Name != "Hot drinks" || r.Color != "#0f172a" {
		t.Fatalf("row = %+v: absent colour must be kept", r)
	}
	if got := f.groups(t); !reflect.DeepEqual(got, []string{"grp-milk"}) {
		t.Fatalf("groups = %v: absent list must be kept", got)
	}
	if got := f.stations(t); !reflect.DeepEqual(got, []string{f.station}) {
		t.Fatalf("stations = %v: absent list must be kept", got)
	}

	// Colour only: name kept; "" clears.
	res, err = f.catalog.UpdateCategoryPartial(ctx, "cat1", data.CategoryPatch{Color: strp("")})
	if err != nil || res.Name != "Hot drinks" {
		t.Fatalf("clear colour: res=%+v err=%v", res, err)
	}
	if r := f.row(t); r.Name != "Hot drinks" || r.Color != "" {
		t.Fatalf("after clear = %+v", r)
	}
}

func TestUpdateCategoryPartial_ReplacesAndClearsLinks(t *testing.T) {
	f := newCatPartialFixture(t)
	ctx := context.Background()
	if err := f.mods.SetCategoryModifierGroups(ctx, "cat1", []string{"grp-milk"}); err != nil {
		t.Fatal(err)
	}

	if _, err := f.catalog.UpdateCategoryPartial(ctx, "cat1", data.CategoryPatch{
		GroupIDs:   idsp("grp-size", "grp-milk"),
		StationIDs: idsp(f.station),
	}); err != nil {
		t.Fatalf("set links: %v", err)
	}
	if got := f.groups(t); !reflect.DeepEqual(got, []string{"grp-size", "grp-milk"}) {
		t.Fatalf("groups = %v, want submitted order", got)
	}
	if got := f.stations(t); !reflect.DeepEqual(got, []string{f.station}) {
		t.Fatalf("stations = %v", got)
	}

	// Present-but-empty clears.
	if _, err := f.catalog.UpdateCategoryPartial(ctx, "cat1", data.CategoryPatch{
		GroupIDs: idsp(), StationIDs: idsp(),
	}); err != nil {
		t.Fatalf("clear links: %v", err)
	}
	if got := f.groups(t); len(got) != 0 {
		t.Fatalf("groups after clear = %v", got)
	}
	if got := f.stations(t); len(got) != 0 {
		t.Fatalf("stations after clear = %v", got)
	}
}

// Same rule as the local dialog (ut-docs#2284): a link to a group that was
// deactivated after linking can't be expressed by a picker of active
// groups, so a replace-all keeps it instead of silently dropping it.
func TestUpdateCategoryPartial_KeepsInactiveGroupLinks(t *testing.T) {
	f := newCatPartialFixture(t)
	ctx := context.Background()
	if err := f.mods.SetCategoryModifierGroups(ctx, "cat1", []string{"grp-milk", "grp-size"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.DB.ExecContext(ctx, `UPDATE item_modifier_groups SET is_active = 0 WHERE id = 'grp-milk'`); err != nil {
		t.Fatal(err)
	}
	res, err := f.catalog.UpdateCategoryPartial(ctx, "cat1", data.CategoryPatch{GroupIDs: idsp(" grp-size ", "grp-size")})
	if err != nil {
		t.Fatalf("resubmit active link: %v", err)
	}
	// The result is the effective set: deduped, plus the kept inactive link.
	if !reflect.DeepEqual(res.GroupIDs, []string{"grp-size", "grp-milk"}) {
		t.Fatalf("result groups = %v, want effective set", res.GroupIDs)
	}
	if _, err := f.catalog.UpdateCategoryPartial(ctx, "cat1", data.CategoryPatch{GroupIDs: idsp()}); err != nil {
		t.Fatalf("clear active links: %v", err)
	}
	if got := f.groups(t); !reflect.DeepEqual(got, []string{"grp-milk"}) {
		t.Fatalf("groups = %v, want the inactive link kept", got)
	}
}

// Validation happens before any write: an unknown group or station, a blank
// name or a missing category leaves the row and both link sets untouched.
func TestUpdateCategoryPartial_RefusesWithoutWriting(t *testing.T) {
	f := newCatPartialFixture(t)
	ctx := context.Background()
	if err := f.mods.SetCategoryModifierGroups(ctx, "cat1", []string{"grp-milk"}); err != nil {
		t.Fatal(err)
	}
	// Same rule as the local dialog: an inactive group can't be newly linked.
	createAnchoredGroup(t, f.mods, "grp-off", "Retired", 2)
	if _, err := f.d.DB.ExecContext(ctx, `UPDATE item_modifier_groups SET is_active = 0 WHERE id = 'grp-off'`); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		id    string
		patch data.CategoryPatch
		want  error
	}{
		{"unknown group", "cat1", data.CategoryPatch{Name: strp("X"), GroupIDs: idsp("grp-size", "nope")}, data.ErrModifierGroupNotFound},
		{"inactive group submitted", "cat1", data.CategoryPatch{GroupIDs: idsp("grp-off")}, data.ErrModifierGroupNotFound},
		{"unknown station", "cat1", data.CategoryPatch{Name: strp("X"), GroupIDs: idsp("grp-size"), StationIDs: idsp("nope")}, data.ErrKitchenStationNotFound},
		{"blank name", "cat1", data.CategoryPatch{Name: strp("  "), GroupIDs: idsp("grp-size")}, data.ErrCategoryNameRequired},
		{"missing category", "no-such-cat", data.CategoryPatch{Name: strp("X")}, data.ErrCategoryNotFound},
	}
	for _, c := range cases {
		if _, err := f.catalog.UpdateCategoryPartial(ctx, c.id, c.patch); !errors.Is(err, c.want) {
			t.Fatalf("%s: err = %v, want %v", c.name, err, c.want)
		}
		if r := f.row(t); r.Name != "Drinks" || r.Color != "#0f172a" {
			t.Fatalf("%s: row written: %+v", c.name, r)
		}
		if got := f.groups(t); !reflect.DeepEqual(got, []string{"grp-milk"}) {
			t.Fatalf("%s: groups written: %v", c.name, got)
		}
		if got := f.stations(t); len(got) != 0 {
			t.Fatalf("%s: stations written: %v", c.name, got)
		}
	}
}
