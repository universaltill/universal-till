package db

import "testing"

// categoryImageMigrationVersion is 039_category_image.sql (ut-docs#2500).
// If the file is ever renumbered (a concurrent lane may also claim 039 —
// whichever merges second moves), this constant moves with it.
const categoryImageMigrationVersion = 39

// TestMigration039_CategoryImageColumn pins ut-docs#2500's schema half:
// categories gains a nullable image_path (NULL = no image, same "one column
// holds a /public/... path" convention as item_images.path), an existing
// row reads back NULL, and a replay against an already-migrated DB is safe
// (the runner skips an ADD COLUMN whose column exists, ut-docs#1412).
func TestMigration039_CategoryImageColumn(t *testing.T) {
	d, err := Open(t.TempDir() + "/category-image.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	var applied int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, categoryImageMigrationVersion).Scan(&applied); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if applied != 1 {
		t.Fatalf("migration %d not recorded as applied on a fresh DB — has it been renumbered?", categoryImageMigrationVersion)
	}
	var notNull int
	if err := d.DB.QueryRow(`SELECT "notnull" FROM pragma_table_info('categories') WHERE name = 'image_path'`).Scan(&notNull); err != nil {
		t.Fatalf("categories.image_path column was not created: %v", err)
	}
	if notNull != 0 {
		t.Fatalf("categories.image_path must be nullable (NULL = no image), notnull=%d", notNull)
	}

	if _, err := d.DB.Exec(`INSERT INTO categories (id, name) VALUES ('c-old', 'Old')`); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	m := loadMigrationVersion(t, categoryImageMigrationVersion)
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version = ?`, categoryImageMigrationVersion); err != nil {
		t.Fatal(err)
	}
	if err := d.applyMigration(m); err != nil {
		t.Fatalf("replaying migration %d: %v", categoryImageMigrationVersion, err)
	}
	var isNull bool
	if err := d.DB.QueryRow(`SELECT image_path IS NULL FROM categories WHERE id = 'c-old'`).Scan(&isNull); err != nil || !isNull {
		t.Fatalf("existing row must read back image_path NULL: isNull=%v err=%v", isNull, err)
	}
}
