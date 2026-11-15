package agentaction

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

func updateFixture(t *testing.T) (*sql.DB, *data.CatalogRepo) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "upd-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	d, err := db.Open(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d.DB, data.NewCatalogRepo(d.DB)
}

func mkItem(t *testing.T, repo *data.CatalogRepo, name string, price int64) string {
	t.Helper()
	id, err := repo.CreateItem(context.Background(), catalogtypes.ItemInput{Name: name, BasePrice: price, IsActive: true})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestUpdate_Registered_4128(t *testing.T) {
	a, ok := Lookup("catalog.item.update.v1")
	if !ok || a.Area != "catalog" || a.RolePermission != "catalog_management" || a.LabelKey != "agent.action.catalog_item_update" {
		t.Fatalf("catalog.item.update.v1 = %+v", a)
	}
}

func TestUpdate_ParseBounds_4128(t *testing.T) {
	bad := []string{
		`{"changes":[]}`,
		`{"changes":[{"item_id":"a"}]}`,                                       // nothing to change
		`{"changes":[{"item_id":"","name":"x"}]}`,                             // no item
		`{"changes":[{"item_id":"a","name":"A‮"}]}`,                           // bidi override
		`{"changes":[{"item_id":"a","price_minor":-1}]}`,                      // negative
		`{"changes":[{"item_id":"a","barcode":"x y"}]}`,                       // bad barcode
		`{"changes":[{"item_id":"a","name":"x"},{"item_id":"a","name":"y"}]}`, // duplicate item
		`{"changes":[{"item_id":"a","name":"x","sku":"S"}]}`,                  // unknown field
		`{"changes":[{"item_id":"a","category_id":""}]}`,                      // cannot clear
	}
	for _, b := range bad {
		if _, err := parseUpdate([]byte(b)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", b, err)
		}
	}
	if _, err := parseUpdate([]byte(`{"changes":[{"item_id":"a","name":"x","price_minor":0,"barcode":"123","category_id":"c"}]}`)); err != nil {
		t.Fatalf("valid refused: %v", err)
	}
}

func TestUpdate_PlanRowsApplyAndStale_4128(t *testing.T) {
	ctx := context.Background()
	d, repo := updateFixture(t)
	env := Env{DB: d}
	id := mkItem(t, repo, "Tea", 100)
	cat, err := repo.CreateCategory(ctx, "Drinks")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := Lookup("catalog.item.update.v1")
	raw := []byte(`{"changes":[{"item_id":"` + id + `","name":"Green tea","price_minor":250,"barcode":"4006381333931","category_id":"` + cat + `"}]}`)
	args, rows, state, err := a.Prepare(ctx, env, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %+v, want name, price, barcode, category", rows)
	}
	for _, r := range rows {
		if r.Item != "Tea" {
			t.Errorf("row item = %q, want the CURRENT name Tea", r.Item)
		}
	}
	// The item changes between propose and confirm: stale, nothing written.
	if err := repo.SetItemName(ctx, id, "Black tea"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(ctx, env, args, state, "u", nil); !errors.Is(err, ErrStale) {
		t.Fatalf("apply after concurrent edit = %v, want ErrStale", err)
	}
	if it, _, _ := repo.GetItem(ctx, id); it.Name != "Black tea" || it.BasePrice != 100 {
		t.Fatalf("stale apply wrote: %+v", it)
	}
	// Fresh proposal applies.
	args, _, state, err = a.Prepare(ctx, env, raw)
	if err != nil {
		t.Fatal(err)
	}
	audited := false
	res, err := a.Apply(ctx, env, args, state, "u", func(*sql.Tx, Result) error { audited = true; return nil })
	if err != nil || !audited || res.Changes != 4 {
		t.Fatalf("apply = %+v, %v (audited %v)", res, err, audited)
	}
	it, _, _ := repo.GetItem(ctx, id)
	if it.Name != "Green tea" || it.BasePrice != 250 || it.CategoryID == nil || *it.CategoryID != cat {
		t.Fatalf("after = %+v", it)
	}
	if _, ok, _ := repo.ItemIDForBarcode(ctx, "4006381333931"); !ok {
		t.Fatal("barcode not attached")
	}
}

// A barcode the operator adds by hand after the proposal makes it stale
// (the card showed "none" as the before value); no second primary lands.
func TestUpdate_BarcodeAddedMeanwhileIsStale_4128(t *testing.T) {
	ctx := context.Background()
	d, repo := updateFixture(t)
	env := Env{DB: d}
	id := mkItem(t, repo, "Tea", 100)
	a, _ := Lookup("catalog.item.update.v1")
	args, _, state, err := a.Prepare(ctx, env, []byte(`{"changes":[{"item_id":"`+id+`","barcode":"4006381333931"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: "5000000000000", ItemID: id, IsPrimary: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(ctx, env, args, state, "u", nil); !errors.Is(err, ErrStale) {
		t.Fatalf("apply = %v, want ErrStale", err)
	}
	if codes, _ := repo.BarcodesForItem(ctx, id); len(codes) != 1 {
		t.Fatalf("barcodes = %+v, want only the manual one", codes)
	}
}

func TestUpdate_RepeatedBarcodeRefusedAtParse_4128(t *testing.T) {
	if _, err := parseUpdate([]byte(`{"changes":[{"item_id":"a","barcode":"111"},{"item_id":"b","barcode":"111"}]}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("repeated barcode: %v, want ErrInvalid", err)
	}
}

func TestUpdate_RefusesBadTargets_4128(t *testing.T) {
	ctx := context.Background()
	d, repo := updateFixture(t)
	env := Env{DB: d}
	a, _ := Lookup("catalog.item.update.v1")
	one := mkItem(t, repo, "One", 100)
	two := mkItem(t, repo, "Two", 100)
	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: "5000000000000", ItemID: one, IsPrimary: true}); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"unknown item":        `{"changes":[{"item_id":"nope","name":"x"}]}`,
		"unknown category":    `{"changes":[{"item_id":"` + one + `","category_id":"nope"}]}`,
		"barcode taken":       `{"changes":[{"item_id":"` + two + `","barcode":"5000000000000"}]}`,
		"already has barcode": `{"changes":[{"item_id":"` + one + `","barcode":"6000000000000"}]}`,
	} {
		if _, _, _, err := a.Prepare(ctx, env, []byte(raw)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}
