package db

import (
	"path/filepath"
	"testing"
)

// 120 / 121 (ADR-0215, ut-docs#6581 / #6583): delivery columns on the channel
// order tables, the delivery actions in channel_order_actions, items.
// unavailable_until and the delivery_signals outbox.

func TestMigration120_DeliveryActionsAndColumns(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m120.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	x := func(q string, a ...any) error { _, err := d.DB.Exec(q, a...); return err }
	if err := x(`INSERT INTO channel_orders (id, connection_id, plugin_key, external_order_id, revision, placed_at, created_at, updated_at)
VALUES ('o1','c1','p','e1',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	var kind, note string
	var late, tip int
	if err := d.DB.QueryRow(`SELECT order_kind, note, late_cancel, tip_minor FROM channel_orders WHERE id='o1'`).Scan(&kind, &note, &late, &tip); err != nil ||
		kind != "webshop" || note != "" || late != 0 || tip != 0 {
		t.Fatalf("defaults = %q %q %d %d %v", kind, note, late, tip, err)
	}
	for _, a := range []string{"accept", "pick", "pack", "ship", "cancel", "reject", "preparing", "ready", "collected", "prep_time"} {
		if err := x(`INSERT INTO channel_order_actions (action_id, order_id, connection_id, external_order_id, action, base_revision, created_at)
VALUES (?, 'o1','c1','e1', ?, 1, 'now')`, "a-"+a, a); err != nil {
			t.Errorf("action %s refused: %v", a, err)
		}
	}
	if err := x(`INSERT INTO channel_order_actions (action_id, order_id, connection_id, external_order_id, action, base_revision, created_at)
VALUES ('bad','o1','c1','e1','refund',1,'now')`); err == nil {
		t.Error("an unknown action must still be refused")
	}
	// Deleting the order cascades to its actions (the rebuilt table keeps the FK).
	if err := x(`DELETE FROM channel_orders WHERE id='o1'`); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM channel_order_actions`).Scan(&n)
	if n != 0 {
		t.Fatalf("actions after cascade = %d", n)
	}
	var idx int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM pragma_index_list('channel_order_actions') WHERE name IN ('idx_channel_order_actions_order','idx_channel_order_actions_state')`).Scan(&idx)
	if idx != 2 {
		t.Fatalf("indexes kept = %d", idx)
	}
}

func TestMigration121_UnavailableUntilAndSignals(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m121.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.DB.Exec(`INSERT INTO tax_codes(id,name,rate_basis_points,is_active) VALUES('t','S',0,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('i','S-1','Soup',100,'t',1)`); err != nil {
		t.Fatal(err)
	}
	var u string
	if err := d.DB.QueryRow(`SELECT unavailable_until FROM items WHERE id='i'`).Scan(&u); err != nil || u != "" {
		t.Fatalf("default = %q %v", u, err)
	}
	if _, err := d.DB.Exec(`INSERT INTO delivery_signals (signal_id, kind, payload_json, created_at) VALUES ('s1','availability.set','{}','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO delivery_signals (signal_id, kind, payload_json, created_at) VALUES ('s2','stock.set','{}','now')`); err == nil {
		t.Fatal("only the two neutral kinds are valid")
	}
}
