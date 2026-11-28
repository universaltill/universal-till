package db

import "testing"

// Migration 124: sale_lines.plugin_ref / plugin_ref_by (ut-docs#7206). Old
// rows read as no ref; the archive twin gets the same columns.
func TestMigration124_SaleLinesPluginRef(t *testing.T) {
	d, _ := openAtPreMigrationSchema(t, 124, "sale-lines-plugin-ref.db")
	if err := d.applyMigration(loadMigrationVersion(t, 124)); err != nil {
		t.Fatalf("apply 124: %v", err)
	}
	for _, tbl := range []string{"sale_lines", "sale_lines_archive"} {
		for _, col := range []string{"plugin_ref", "plugin_ref_by"} {
			var n int
			if err := d.DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, tbl, col).Scan(&n); err != nil || n != 1 {
				t.Errorf("%s.%s present = %d (err %v), want 1", tbl, col, n, err)
			}
		}
	}
}
