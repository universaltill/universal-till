package db

import "testing"

// 090 adds channel_orders / channel_order_lines (ADR-0169 §9, ut-docs#4768).
// The order key is (connection, external order) and lines are keyed by line
// id inside an order; deleting an order takes its lines.
func TestMigration090_ChannelOrders(t *testing.T) {
	d, path := openAtPreMigrationSchema(t, 90, "channel-orders.db")
	if _, err := d.DB.Exec(`SELECT 1 FROM channel_orders`); err == nil {
		t.Fatal("channel_orders must not exist before 090")
	}
	d.Close()
	d = openMigratedTo(t, path, 90)

	ins := `INSERT INTO channel_orders (id, connection_id, plugin_key, external_order_id, revision, placed_at, created_at, updated_at)
	        VALUES (?, ?, 'p', ?, 1, '2026-10-22T10:00:00Z', '2026-10-22T10:00:01Z', '2026-10-22T10:00:01Z')`
	if _, err := d.DB.Exec(ins, "a", "c1", "o1"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := d.DB.Exec(ins, "c", "c1", "o1"); err == nil {
		t.Error("the same external order on the same connection must be refused")
	}
	if _, err := d.DB.Exec(ins, "e", "c2", "o1"); err != nil {
		t.Errorf("the same external id on another connection is a different order: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO channel_order_lines (id, order_id, line_id, sku, qty) VALUES ('l1','a','x','S',1)`); err != nil {
		t.Fatalf("line: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO channel_order_lines (id, order_id, line_id, sku, qty) VALUES ('l2','a','x','S',1)`); err == nil {
		t.Error("a line id repeats within an order")
	}
	if _, err := d.DB.Exec(`DELETE FROM channel_orders WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM channel_order_lines`).Scan(&n)
	if n != 0 {
		t.Errorf("lines left after order delete = %d", n)
	}
}
