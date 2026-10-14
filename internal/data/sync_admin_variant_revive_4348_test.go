package data

import (
	"context"
	"testing"
)

// ut-docs#4348: item_variants.retired_with_item travels main till -> joined
// till with the rest of the row, so a joined till sees the same variants
// retire with their item and come back with it.
func TestAdminDumpApply_VariantsFollowItemOffOn_4348(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	mustExec(t, primary, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm1', 'FW', 'Flat White', 320, 1)`)
	mustExec(t, primary, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES
		('v-small', 'itm1', 'FW-S', 'Small', 300, 1),
		('v-off',   'itm1', 'FW-X', 'Extra', 420, 0)`)

	cat := NewCatalogRepo(primary.DB)
	sync := func(step string) []string {
		t.Helper()
		bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
		if err != nil {
			t.Fatalf("%s: dump: %v", step, err)
		}
		if _, err := NewSyncAdminRepo(replica.DB).ApplyAdminWithResult(ctx, wireTrip(t, bundle)); err != nil {
			t.Fatalf("%s: apply: %v", step, err)
		}
		rows, err := replica.Query(`SELECT id || ':' || is_active || '/' || retired_with_item FROM item_variants WHERE item_id = 'itm1' ORDER BY id`)
		if err != nil {
			t.Fatalf("%s: query replica: %v", step, err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}
	check := func(step string, got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: replica variants = %v, want %v", step, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: replica variants = %v, want %v", step, got, want)
			}
		}
	}

	check("initial", sync("initial"), "v-off:0/0", "v-small:1/0")

	if err := cat.DeactivateItem(ctx, "itm1"); err != nil {
		t.Fatal(err)
	}
	check("after main till switch-off", sync("off"), "v-off:0/0", "v-small:0/1")

	if _, err := cat.SaveItem(ctx, ItemPatch{ID: "itm1", Active: boolPtr4348(true)}); err != nil {
		t.Fatal(err)
	}
	check("after main till switch-on", sync("on"), "v-off:0/0", "v-small:1/0")
}

func boolPtr4348(b bool) *bool { return &b }
