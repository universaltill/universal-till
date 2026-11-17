package data_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#6097: no shortcut write stores shortcut_buttons.image_path.
func TestShortcutWritesNeverStoreImagePath_6097(t *testing.T) {
	d, err := db.Open(testsupport.MigratedDBFile(t, "shortcuts6097.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	for _, id := range []string{"item-a", "item-c", "item-d"} {
		if _, err := d.DB.ExecContext(ctx,
			`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES (?,?,?,100,1,0,'each')`, id, "SKU-"+id, id); err != nil {
			t.Fatal(err)
		}
	}
	repo := data.NewShortcutsRepo(d.DB)
	const img = "/public/images/x.png"
	reset := func(t *testing.T, seed string) {
		t.Helper()
		if _, err := d.DB.ExecContext(ctx, `DELETE FROM shortcut_buttons`); err != nil {
			t.Fatal(err)
		}
		if seed != "" {
			if _, err := d.DB.ExecContext(ctx, seed); err != nil {
				t.Fatal(err)
			}
		}
	}
	noNewPath := func(t *testing.T, barcode string) {
		t.Helper()
		var path sql.NullString
		if err := d.DB.QueryRowContext(ctx, `SELECT image_path FROM shortcut_buttons WHERE barcode = ?`, barcode).Scan(&path); err != nil {
			t.Fatal(err)
		}
		if path.Valid {
			t.Fatalf("%s stored image_path %q, want NULL", barcode, path.String)
		}
	}
	pathAndLabel := func(t *testing.T, barcode string) (string, sql.NullString) {
		t.Helper()
		var label string
		var path sql.NullString
		if err := d.DB.QueryRowContext(ctx, `SELECT label, image_path FROM shortcut_buttons WHERE barcode = ?`, barcode).Scan(&label, &path); err != nil {
			t.Fatal(err)
		}
		return label, path
	}

	t.Run("SaveButtons", func(t *testing.T) {
		reset(t, "")
		if err := repo.SaveButtons(ctx, []data.ShortcutButton{{Barcode: "B1", Label: "One", ItemID: "item-a", ImageURL: img}}); err != nil {
			t.Fatal(err)
		}
		noNewPath(t, "B1")
	})

	t.Run("MaterializeAndReorder", func(t *testing.T) {
		reset(t, "")
		if err := repo.MaterializeAndReorder(ctx, []data.ShortcutButton{{Barcode: "B2", Label: "Two", ItemID: "item-a", ImageURL: img}}, nil); err != nil {
			t.Fatal(err)
		}
		noNewPath(t, "B2")
	})

	t.Run("AddButton insert", func(t *testing.T) {
		reset(t, "")
		if err := repo.AddButton(ctx, data.ShortcutButton{Barcode: "B3", Label: "Three", ItemID: "item-c", ImageURL: img}); err != nil {
			t.Fatal(err)
		}
		noNewPath(t, "B3")
	})

	// ON CONFLICT path: the barcode exists for a different item.
	t.Run("AddButton on conflict, no legacy value", func(t *testing.T) {
		reset(t, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('B4','Old','item-a',0)`)
		if err := repo.AddButton(ctx, data.ShortcutButton{Barcode: "B4", Label: "New", ItemID: "item-d", ImageURL: img}); err != nil {
			t.Fatal(err)
		}
		noNewPath(t, "B4")
	})

	t.Run("AddButton on conflict keeps a legacy value", func(t *testing.T) {
		reset(t, `INSERT INTO shortcut_buttons(barcode,label,item_id,image_path,sort_order) VALUES('B7','Old','item-a','/old.png',0)`)
		if err := repo.AddButton(ctx, data.ShortcutButton{Barcode: "B7", Label: "New", ItemID: "item-d", ImageURL: img}); err != nil {
			t.Fatal(err)
		}
		label, path := pathAndLabel(t, "B7")
		if label != "New" || path.String != "/old.png" {
			t.Fatalf("conflict row: label=%q image_path=%v, want New and untouched /old.png", label, path)
		}
	})

	// The item's existing row keeps a legacy value (untouched, not cleared); label refreshed.
	t.Run("AddButton existing row keeps a legacy value", func(t *testing.T) {
		reset(t, `INSERT INTO shortcut_buttons(barcode,label,item_id,image_path,sort_order) VALUES('B5','Old','item-a','/old.png',0)`)
		if err := repo.AddButton(ctx, data.ShortcutButton{Barcode: "B6", Label: "Fresh", ItemID: "item-a", ImageURL: img}); err != nil {
			t.Fatal(err)
		}
		label, path := pathAndLabel(t, "B5")
		if label != "Fresh" || path.String != "/old.png" {
			t.Fatalf("existing row: label=%q image_path=%v, want Fresh and untouched /old.png", label, path)
		}
	})
}
