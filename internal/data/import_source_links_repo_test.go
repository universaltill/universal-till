package data

import (
	"context"
	"testing"
)

// ADR-0161 §5 (ut-docs#4429): import_source_links remembers which local
// record a foreign (source, entity, external_id) became. The link is keyed
// per source and entity, an upsert re-points it (a recreated item), and the
// reverse lookup finds which external id already owns a local record.
func TestImportSourceLinks_UpsertLookupAndReverse(t *testing.T) {
	d := openMigratedDB(t, "import-source-links.db")
	ctx := context.Background()
	links := NewImportSourceLinkRepo()

	if _, ok, err := links.LocalID(ctx, d.DB, "src.a", "items", "x1"); err != nil || ok {
		t.Fatalf("empty table: ok=%v err=%v", ok, err)
	}
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := links.Upsert(ctx, tx, "src.a", "items", "x1", "item-1"); err != nil {
		t.Fatal(err)
	}
	if err := links.Upsert(ctx, tx, "src.b", "items", "x1", "item-9"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if id, ok, err := links.LocalID(ctx, d.DB, "src.a", "items", "x1"); err != nil || !ok || id != "item-1" {
		t.Fatalf("LocalID = %q %v %v, want item-1", id, ok, err)
	}
	if id, _, _ := links.LocalID(ctx, d.DB, "src.b", "items", "x1"); id != "item-9" {
		t.Fatalf("source scoping: got %q, want item-9", id)
	}
	if ext, ok, err := links.ExternalIDFor(ctx, d.DB, "src.a", "items", "item-1"); err != nil || !ok || ext != "x1" {
		t.Fatalf("ExternalIDFor = %q %v %v", ext, ok, err)
	}

	// Re-pointing (the local item was deleted and recreated) replaces, never duplicates.
	tx, _ = d.DB.BeginTx(ctx, nil)
	if err := links.Upsert(ctx, tx, "src.a", "items", "x1", "item-2"); err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit()
	if id, _, _ := links.LocalID(ctx, d.DB, "src.a", "items", "x1"); id != "item-2" {
		t.Fatalf("after re-point got %q, want item-2", id)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM import_source_links`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rows = %d (%v), want 2", n, err)
	}
}
