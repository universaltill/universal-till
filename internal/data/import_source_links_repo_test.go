package data

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
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

// ut-docs#4734: SiblingItemsByName finds items of an exact name linked by a
// sibling source (prefix "<plugin>#") or the legacy bare source, excluding
// items already linked under the caller's own source, capped at two ids.
func TestImportSourceLinks_SiblingItemsByName(t *testing.T) {
	d := openMigratedDB(t, "import-source-links-sibling.db")
	ctx := context.Background()
	links := NewImportSourceLinkRepo()
	cat := NewCatalogRepo(d.DB)
	mk := func(name string) string {
		id, err := cat.CreateItem(ctx, catalogtypes.ItemInput{Name: name, BasePrice: 100, IsActive: true})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	link := func(source, ext, id string) {
		tx, _ := d.DB.BeginTx(ctx, nil)
		if err := links.Upsert(ctx, tx, source, "items", ext, id); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	find := func(name, plugin, own, legacy string) []string {
		byName, err := links.SiblingItemsByName(ctx, d.DB, plugin, own, legacy)
		if err != nil {
			t.Fatal(err)
		}
		return byName[name]
	}
	a, legacy, other, own := mk("Tea"), mk("Tea"), mk("Tea"), mk("Tea")
	link("p#a", "1", a)
	link("p#a", "2", a) // two links, one distinct item
	link("p", "3", legacy)
	link("q#a", "4", other)
	link("p#b", "5", own)
	link("p#a", "6", own) // also linked by a sibling, but under own source too

	got := find("Tea", "p", "p#b", "p")
	if len(got) != 2 || got[0] == got[1] {
		t.Fatalf("got %v, want the two distinct candidates a and legacy", got)
	}
	for _, id := range got {
		if id != a && id != legacy {
			t.Fatalf("unexpected candidate %q (a=%q legacy=%q)", id, a, legacy)
		}
	}
	if got := find("tea", "p", "p#b", "p"); len(got) != 0 {
		t.Fatalf("case-folded match: %v", got)
	}
	if got := find("Tea", "", "solo", ""); len(got) != 0 {
		t.Fatalf("no prefix, no legacy: %v", got)
	}
	if got := find("Tea", "", "solo", "p"); len(got) != 1 || got[0] != legacy {
		t.Fatalf("legacy only: %v", got)
	}
	// '_' and '%' in a plugin id are literal.
	link("p_x#a", "7", mk("Scone"))
	if got := find("Scone", "pax", "pax#b", ""); len(got) != 0 {
		t.Fatalf("wildcard leaked: %v", got)
	}
	if got := find("Scone", "p_x", "p_x#b", ""); len(got) != 1 {
		t.Fatalf("literal underscore: %v", got)
	}
	// Sources just outside the [p#, p$) range are not siblings.
	link(`p"#a`, "8", mk("Bun"))
	link("p$a", "9", mk("Bun"))
	if got := find("Bun", "p", "p#b", ""); len(got) != 0 {
		t.Fatalf("range boundary leaked: %v", got)
	}
}
