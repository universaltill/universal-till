package data_test

import (
	"context"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#5858: the admin-screen entry points refuse control and invisible
// characters in single-line names; the directive-shared ones stay unchecked
// so the till never refuses a name my. accepted.

const (
	nlEsc   = "Co\x1bla"
	nlRLO   = "Co\u202ela"
	nlRTL   = "\u200f שם \u2067x\u2069 "
	nlRTLOk = "\u200f שם \u2067x\u2069"
)

func nlBadNames() map[string]string { return map[string]string{"esc": nlEsc, "rlo": nlRLO} }

func nlRefused(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, data.ErrNameInvalidCharacters) {
		t.Fatalf("err = %v, want ErrNameInvalidCharacters", err)
	}
}

func nlOpen(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(testsupport.MigratedDBFile(t, "nl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestSingleLineName_CategoryAdminPaths(t *testing.T) {
	d := nlOpen(t)
	repo := data.NewCatalogRepo(d.DB)
	ctx := context.Background()
	for label, bad := range nlBadNames() {
		t.Run(label, func(t *testing.T) {
			_, err := repo.CreateCategoryWithColorHidden(ctx, bad, "", false)
			nlRefused(t, err)
		})
	}
	id, err := repo.CreateCategoryWithColorHidden(ctx, nlRTL, "", false)
	if err != nil {
		t.Fatal(err)
	}
	for label, bad := range nlBadNames() {
		t.Run("update "+label, func(t *testing.T) {
			nlRefused(t, repo.UpdateCategoryWithHidden(ctx, id, bad, "", nil))
		})
	}
	cats, _ := repo.ListCategories(ctx)
	if len(cats) != 1 || cats[0].Name != nlRTLOk {
		t.Fatalf("categories = %+v, want one named %q", cats, nlRTLOk)
	}
}

// No gap with my.: the directive-shared category methods still store U+202E.
func TestSingleLineName_CategoryDirectivePathsUnchecked(t *testing.T) {
	d := nlOpen(t)
	repo := data.NewCatalogRepo(d.DB)
	ctx := context.Background()
	id, err := repo.CreateCategoryWithColor(ctx, nlRLO, "")
	if err != nil {
		t.Fatalf("CreateCategoryWithColor refused a name my. accepts: %v", err)
	}
	if err := repo.UpdateCategory(ctx, id, "x"+nlRLO, ""); err != nil {
		t.Fatalf("UpdateCategory refused a name my. accepts: %v", err)
	}
	cats, _ := repo.ListCategories(ctx)
	if len(cats) != 1 || cats[0].Name != "x"+nlRLO {
		t.Fatalf("categories = %+v", cats)
	}
}

func TestSingleLineName_ItemDirectivePathUnchecked(t *testing.T) {
	d := nlOpen(t)
	repo := data.NewCatalogRepo(d.DB)
	if _, err := repo.CreateItem(context.Background(), itemInputNamed(nlRLO)); err != nil {
		t.Fatalf("data.CreateItem refused a name my. accepts: %v", err)
	}
}

func TestSingleLineName_TaxCode(t *testing.T) {
	d := nlOpen(t)
	repo := data.NewCatalogRepo(d.DB)
	ctx := context.Background()
	for label, bad := range nlBadNames() {
		t.Run(label, func(t *testing.T) {
			_, err := repo.CreateTaxCode(ctx, bad, 1000, nil)
			nlRefused(t, err)
		})
	}
	id, err := repo.CreateTaxCode(ctx, nlRTL, 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	for label, bad := range nlBadNames() {
		t.Run("update "+label, func(t *testing.T) {
			nlRefused(t, repo.UpdateTaxCode(ctx, id, bad, 1000, nil, true))
		})
	}
	tc, err := repo.GetTaxCode(ctx, id)
	if err != nil || tc.Name != nlRTLOk {
		t.Fatalf("tax code = %+v, %v; want name %q", tc, err, nlRTLOk)
	}
}

func TestSingleLineName_Modifiers(t *testing.T) {
	d := nlOpen(t)
	repo := data.NewModifierRepo(d.DB)
	ctx := context.Background()
	for label, bad := range nlBadNames() {
		t.Run("group "+label, func(t *testing.T) {
			_, err := repo.CreateGroup(ctx, "g-bad", bad, false, 0, 1, 0)
			nlRefused(t, err)
		})
	}
	if _, err := repo.CreateGroup(ctx, "g1", nlRTL, false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	for label, bad := range nlBadNames() {
		t.Run("group update "+label, func(t *testing.T) {
			nlRefused(t, repo.UpdateGroup(ctx, "g1", bad, false, 0, 1, 0, true))
		})
		t.Run("option "+label, func(t *testing.T) {
			_, err := repo.CreateOption(ctx, "o-bad", "g1", bad, 0, 0)
			nlRefused(t, err)
		})
	}
	if _, err := repo.CreateOption(ctx, "o1", "g1", nlRTL, 0, 0); err != nil {
		t.Fatal(err)
	}
	for label, bad := range nlBadNames() {
		t.Run("option update "+label, func(t *testing.T) {
			nlRefused(t, repo.UpdateOption(ctx, "o1", bad, 0, 0, true))
		})
	}
	if _, err := data.NewCatalogRepo(d.DB).CreateItem(ctx, itemInputNamed("Host")); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkGroupToItem(ctx, "nl-item", "g1", 0); err != nil {
		t.Fatal(err)
	}
	groups, err := repo.ListAllGroupsForItem(ctx, "nl-item")
	if err != nil || len(groups) != 1 || groups[0].Name != nlRTLOk ||
		len(groups[0].Options) != 1 || groups[0].Options[0].Name != nlRTLOk {
		t.Fatalf("groups = %+v, %v; want one group and option named %q", groups, err, nlRTLOk)
	}
}

func TestSingleLineName_OptionSets(t *testing.T) {
	d := nlOpen(t)
	repo := data.NewOptionSetRepo(d.DB)
	ctx := context.Background()
	for label, bad := range nlBadNames() {
		t.Run("create "+label, func(t *testing.T) {
			_, err := repo.CreateOptionSet(ctx, bad)
			nlRefused(t, err)
		})
	}
	setID, err := repo.CreateOptionSet(ctx, nlRTL)
	if err != nil {
		t.Fatal(err)
	}
	for label, bad := range nlBadNames() {
		t.Run("rename "+label, func(t *testing.T) {
			nlRefused(t, repo.RenameOptionSet(ctx, setID, bad))
		})
		t.Run("add value "+label, func(t *testing.T) {
			_, err := repo.AddOptionSetValue(ctx, setID, bad)
			nlRefused(t, err)
		})
		t.Run("replace values "+label, func(t *testing.T) {
			nlRefused(t, repo.ReplaceOptionSetValues(ctx, setID, []data.OptionSetValueInput{{Value: "ok"}, {Value: bad}}))
		})
	}
	if _, err := repo.AddOptionSetValue(ctx, setID, nlRTL); err != nil {
		t.Fatal(err)
	}
	sets, err := repo.ListOptionSets(ctx)
	if err != nil || len(sets) != 1 || sets[0].Name != nlRTLOk || len(sets[0].Values) != 1 || sets[0].Values[0].Value != nlRTLOk {
		t.Fatalf("sets = %+v, %v", sets, err)
	}
}

// No gap with my.: SaveOptionSet (family B) is untouched, U+2028 still passes.
func TestSingleLineName_SaveOptionSetStillAcceptsLineSeparator(t *testing.T) {
	d := nlOpen(t)
	repo := data.NewOptionSetRepo(d.DB)
	ctx := context.Background()
	setID, err := repo.CreateOptionSet(ctx, "Sizes")
	if err != nil {
		t.Fatal(err)
	}
	vals := []data.OptionSetValueInput{{ID: "v1", Value: "a\u2028b"}}
	if _, err := repo.SaveOptionSet(ctx, data.OptionSetSave{ID: setID, Values: &vals}); err != nil {
		t.Fatalf("SaveOptionSet refused U+2028: %v", err)
	}
}

func TestSingleLineName_Registers(t *testing.T) {
	d := nlOpen(t)
	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	for label, bad := range nlBadNames() {
		t.Run("create "+label, func(t *testing.T) {
			_, err := repo.CreateRegister(ctx, bad, nil)
			nlRefused(t, err)
		})
	}
	id, err := repo.CreateRegister(ctx, nlRTL, nil)
	if err != nil {
		t.Fatal(err)
	}
	for label, bad := range nlBadNames() {
		t.Run("rename "+label, func(t *testing.T) {
			nlRefused(t, repo.RenameRegister(ctx, id, bad))
		})
	}
	regs, _ := repo.ListRegisters(ctx)
	if len(regs) != 1 || regs[0].Name != nlRTLOk {
		t.Fatalf("registers = %+v, want one named %q", regs, nlRTLOk)
	}
}

// ut-docs#5862: enrolment provisioning checks the name too. A refused name is
// an error (the enroll handler fails open to a manual assignment), never a
// silently stripped one.
func TestSingleLineName_EnrolmentRegisterRefused(t *testing.T) {
	d := nlOpen(t)
	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	for label, bad := range nlBadNames() {
		t.Run(label, func(t *testing.T) {
			id, name, err := repo.CreateRegisterForEnrolment(ctx, bad)
			nlRefused(t, err)
			if id != "" || name != "" {
				t.Fatalf("got id=%q name=%q alongside the refusal", id, name)
			}
		})
	}
	if regs, _ := repo.ListRegisters(ctx); len(regs) != 0 {
		t.Fatalf("registers = %+v, want none after refused enrolments", regs)
	}
	if _, name, err := repo.CreateRegisterForEnrolment(ctx, nlRTL); err != nil || name != nlRTLOk {
		t.Fatalf("CreateRegisterForEnrolment(balanced isolate) = %q, %v; want trimmed %q", name, err, nlRTLOk)
	}
}

// ut-docs#5862: following the till's rename is best-effort — a refused name is
// skipped (false, nil), never an error that fails the till rename.
func TestSingleLineName_RegisterFollowsTillSkipsBadName(t *testing.T) {
	d := nlOpen(t)
	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	id, err := repo.CreateRegister(ctx, "Bar", nil)
	if err != nil {
		t.Fatal(err)
	}
	for label, bad := range nlBadNames() {
		t.Run(label, func(t *testing.T) {
			if renamed, err := repo.RenameRegisterToFollowTill(ctx, id, bad); renamed || err != nil {
				t.Fatalf("RenameRegisterToFollowTill = %v, %v; want false, nil", renamed, err)
			}
		})
	}
	regs, _ := repo.ListRegisters(ctx)
	if len(regs) != 1 || regs[0].Name != "Bar" {
		t.Fatalf("registers = %+v, want one still named Bar", regs)
	}
}

func itemInputNamed(name string) catalogtypes.ItemInput {
	return catalogtypes.ItemInput{ID: "nl-item", SKU: "NL-1", Name: name, BasePrice: 100, IsActive: true}
}

// A bad value stored through the unchecked path (my. directive) must not
// block editing the other values; only new or changed texts are checked.
func TestSingleLineName_ReplaceValuesIgnoresUnchangedBadValue(t *testing.T) {
	d := nlOpen(t)
	repo := data.NewOptionSetRepo(d.DB)
	ctx := context.Background()
	setID, err := repo.CreateOptionSet(ctx, "Sizes")
	if err != nil {
		t.Fatal(err)
	}
	badText := "Lar\u202ege"
	vals := []data.OptionSetValueInput{{ID: "v-small", Value: "Small"}, {ID: "v-bad", Value: badText}}
	if _, err := repo.SaveOptionSet(ctx, data.OptionSetSave{ID: setID, Values: &vals}); err != nil {
		t.Fatal(err)
	}
	// Rename a sibling, bad value passed through unchanged.
	if err := repo.ReplaceOptionSetValues(ctx, setID, []data.OptionSetValueInput{{ID: "v-small", Value: "Smol"}, {ID: "v-bad", Value: badText}}); err != nil {
		t.Fatalf("sibling rename: %v", err)
	}
	// Move.
	if err := repo.ReplaceOptionSetValues(ctx, setID, []data.OptionSetValueInput{{ID: "v-bad", Value: badText}, {ID: "v-small", Value: "Smol"}}); err != nil {
		t.Fatalf("move: %v", err)
	}
	// Remove the sibling.
	if err := repo.ReplaceOptionSetValues(ctx, setID, []data.OptionSetValueInput{{ID: "v-bad", Value: badText}}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// Changing the bad one to another bad text is refused.
	nlRefused(t, repo.ReplaceOptionSetValues(ctx, setID, []data.OptionSetValueInput{{ID: "v-bad", Value: "Lar\u202dge"}}))
	// A new bad value is refused.
	nlRefused(t, repo.ReplaceOptionSetValues(ctx, setID, []data.OptionSetValueInput{{ID: "v-bad", Value: badText}, {ID: "v-new", Value: "x\u200by"}}))
	got, err := repo.GetOptionSet(ctx, setID)
	if err != nil || len(got.Values) != 1 || got.Values[0].Value != badText {
		t.Fatalf("set = %+v, %v", got, err)
	}
}
