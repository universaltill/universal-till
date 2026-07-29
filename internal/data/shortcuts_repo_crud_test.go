package data_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

func newShortcutsTestDB(t *testing.T) *data.ShortcutsRepo {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "shortcuts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx,
		`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-a','SKU-A','Latte',320,1,0,'each')`); err != nil {
		t.Fatalf("seed item-a: %v", err)
	}
	if _, err := d.DB.ExecContext(ctx,
		`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-b','SKU-B','Cappuccino',350,0,0,'each')`); err != nil {
		t.Fatalf("seed item-b (inactive): %v", err)
	}
	// db.Open runs the real migrations, which seed demo shortcut buttons for
	// the sample café/shop catalog — clear them so each test starts from a
	// deterministic, empty button list rather than asserting against
	// whatever the current seed data happens to contain.
	if _, err := d.DB.ExecContext(ctx, `DELETE FROM shortcut_buttons`); err != nil {
		t.Fatalf("clear seeded shortcut buttons: %v", err)
	}
	return data.NewShortcutsRepo(d.DB)
}

func TestSaveButtons_ReplacesWholeSetInOrder(t *testing.T) {
	repo := newShortcutsTestDB(t)
	ctx := context.Background()

	if err := repo.SaveButtons(ctx, []data.ShortcutButton{
		{Barcode: "B1", Label: "First", ItemID: "item-a"},
		{Barcode: "B2", Label: "Second", ItemID: "item-a", ImageURL: "/custom.png"},
	}); err != nil {
		t.Fatal(err)
	}

	btns, err := repo.LoadButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 2 {
		t.Fatalf("expected 2 buttons, got %d: %+v", len(btns), btns)
	}
	if btns[0].Label != "First" || btns[1].Label != "Second" {
		t.Fatalf("expected sort_order to follow list order, got %+v", btns)
	}
	if btns[1].ImageURL != "/custom.png" {
		t.Fatalf("expected explicit image preserved, got %q", btns[1].ImageURL)
	}

	// A second call must REPLACE the set, not append to it.
	if err := repo.SaveButtons(ctx, []data.ShortcutButton{
		{Barcode: "B3", Label: "Only", ItemID: "item-a"},
	}); err != nil {
		t.Fatal(err)
	}
	btns, err = repo.LoadButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 1 || btns[0].Label != "Only" {
		t.Fatalf("expected SaveButtons to replace the whole set, got %+v", btns)
	}

	// Saving an empty list clears every button.
	if err := repo.SaveButtons(ctx, nil); err != nil {
		t.Fatal(err)
	}
	btns, err = repo.LoadButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 0 {
		t.Fatalf("expected an empty button list, got %+v", btns)
	}
}

func TestUpdateOrder(t *testing.T) {
	repo := newShortcutsTestDB(t)
	ctx := context.Background()

	if err := repo.SaveButtons(ctx, []data.ShortcutButton{
		{Barcode: "B1", Label: "Alpha", ItemID: "item-a"},
		{Barcode: "B2", Label: "Beta", ItemID: "item-a"},
		{Barcode: "B3", Label: "Gamma", ItemID: "item-a"},
	}); err != nil {
		t.Fatal(err)
	}

	// Reverse the order via drag&drop.
	if err := repo.UpdateOrder(ctx, []string{"B3", "B2", "B1"}); err != nil {
		t.Fatal(err)
	}
	btns, err := repo.LoadButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 3 || btns[0].Label != "Gamma" || btns[1].Label != "Beta" || btns[2].Label != "Alpha" {
		t.Fatalf("expected reversed order Gamma,Beta,Alpha — got %+v", btns)
	}
}

func TestAddButton(t *testing.T) {
	repo := newShortcutsTestDB(t)
	ctx := context.Background()

	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "Latte", Barcode: "B1", ItemID: "item-a"}); err != nil {
		t.Fatal(err)
	}
	btns, err := repo.LoadButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 1 || btns[0].Label != "Latte" {
		t.Fatalf("expected the new button, got %+v", btns)
	}

	// Re-adding the same barcode updates in place (ON CONFLICT), not a
	// duplicate row.
	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "Latte Renamed", Barcode: "B1", ItemID: "item-a"}); err != nil {
		t.Fatal(err)
	}
	btns, err = repo.LoadButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 1 || btns[0].Label != "Latte Renamed" {
		t.Fatalf("expected the existing button updated in place, got %+v", btns)
	}

	// Validation: blank fields rejected.
	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "", Barcode: "B2", ItemID: "item-a"}); err == nil {
		t.Fatal("expected an error for a blank label")
	}
	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "X", Barcode: "", ItemID: "item-a"}); err == nil {
		t.Fatal("expected an error for a blank barcode")
	}
	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "X", Barcode: "B2", ItemID: ""}); err == nil {
		t.Fatal("expected an error for a blank item id")
	}

	// Unknown or inactive item rejected — a shortcut must point at something
	// actually sellable.
	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "X", Barcode: "B2", ItemID: "does-not-exist"}); err == nil {
		t.Fatal("expected an error for an unknown item")
	}
	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "X", Barcode: "B2", ItemID: "item-b"}); err == nil {
		t.Fatal("expected an error for an inactive item")
	}
}

func TestAddButton_AppendsAtEndOfSortOrder(t *testing.T) {
	repo := newShortcutsTestDB(t)
	ctx := context.Background()

	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "First", Barcode: "B1", ItemID: "item-a"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "Second", Barcode: "B2", ItemID: "item-a"}); err != nil {
		t.Fatal(err)
	}
	btns, err := repo.LoadButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 2 || btns[0].Label != "First" || btns[1].Label != "Second" {
		t.Fatalf("expected new buttons appended after existing ones, got %+v", btns)
	}
}

func TestRemoveButton(t *testing.T) {
	repo := newShortcutsTestDB(t)
	ctx := context.Background()

	if err := repo.AddButton(ctx, data.ShortcutButton{Label: "Latte", Barcode: "B1", ItemID: "item-a"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RemoveButton(ctx, "B1"); err != nil {
		t.Fatal(err)
	}
	btns, err := repo.LoadButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 0 {
		t.Fatalf("expected the button removed, got %+v", btns)
	}

	// Removing a code that was never a button is a no-op, not an error.
	if err := repo.RemoveButton(ctx, "never-existed"); err != nil {
		t.Fatalf("expected no error removing an unknown barcode, got %v", err)
	}
}
