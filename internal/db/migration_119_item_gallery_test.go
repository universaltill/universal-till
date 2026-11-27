package db

import (
	"path/filepath"
	"testing"
)

// ut-docs#7101 (ADR-0218): item_images grows from one thumbnail to a gallery.
// The migration must keep every installed thumbnail, keep one-thumbnail-per-item,
// and allow several gallery rows plus a variant gallery.
func TestMigrationGallery_PreservesThumbnails(t *testing.T) {
	d := openMigratedTo(t, filepath.Join(t.TempDir(), "g.db"), 117)
	defer d.Close()
	for _, s := range []string{
		`INSERT INTO items (id, name, base_price) VALUES ('i1','A',100),('i2','B',200)`,
		`INSERT INTO item_images (id, item_id, path, role) VALUES ('t1','i1','/p/1.png','thumbnail'),('t2','i2','/p/2.png','thumbnail')`,
	} {
		if _, err := d.DB.Exec(s); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if err := d.migrateUpTo(119); err != nil {
		t.Fatalf("migrate 119: %v", err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM item_images WHERE role='thumbnail'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("thumbnails after migration: %d err %v", n, err)
	}
	var path, alt, variant string
	if err := d.DB.QueryRow(`SELECT path, alt, variant_id FROM item_images WHERE id='t1'`).Scan(&path, &alt, &variant); err != nil || path != "/p/1.png" || alt != "" || variant != "" {
		t.Fatalf("t1: %q %q %q %v", path, alt, variant, err)
	}
	// still exactly one thumbnail per item
	if _, err := d.DB.Exec(`INSERT INTO item_images (id, item_id, path, role, sort_order) VALUES ('t3','i1','/p/x.png','thumbnail',5)`); err == nil {
		t.Fatal("a second thumbnail for one item must be refused")
	}
	// but a gallery: several rows per item, ordered, unique per position
	for i, id := range []string{"g2", "g3"} {
		if _, err := d.DB.Exec(`INSERT INTO item_images (id, item_id, path, role, sort_order, alt) VALUES (?, 'i1', ?, 'gallery', ?, 'alt')`, id, "/p/"+id+".png", i+2); err != nil {
			t.Fatalf("gallery row %s: %v", id, err)
		}
	}
	if _, err := d.DB.Exec(`INSERT INTO item_images (id, item_id, path, role, sort_order) VALUES ('g2b','i1','/p/dup.png','gallery',2)`); err == nil {
		t.Fatal("two gallery rows at one position must be refused")
	}
	// variant gallery rows, separate from the item's
	if _, err := d.DB.Exec(`INSERT INTO item_images (id, item_id, path, role, sort_order, variant_id) VALUES ('v1','i1','/p/v.png','variant',1,'var1'),('v2','i1','/p/v2.png','variant',2,'var1')`); err != nil {
		t.Fatalf("variant rows: %v", err)
	}
}

// The sell-screen version trigger (042) still fires for a gallery row.
func TestMigrationGallery_SellScreenVersionTriggerFires(t *testing.T) {
	d := openMigratedTo(t, filepath.Join(t.TempDir(), "g.db"), 119)
	defer d.Close()
	if _, err := d.DB.Exec(`INSERT INTO items (id, name, base_price) VALUES ('i1','A',100)`); err != nil {
		t.Fatal(err)
	}
	var before, after int
	_ = d.DB.QueryRow(`SELECT generation FROM sell_screen_version WHERE id=1`).Scan(&before)
	if _, err := d.DB.Exec(`INSERT INTO item_images (id, item_id, path, role, sort_order) VALUES ('g','i1','/p/g.png','gallery',2)`); err != nil {
		t.Fatal(err)
	}
	_ = d.DB.QueryRow(`SELECT generation FROM sell_screen_version WHERE id=1`).Scan(&after)
	if after <= before {
		t.Fatalf("generation %d -> %d", before, after)
	}
}
