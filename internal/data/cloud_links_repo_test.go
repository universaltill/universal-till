package data

import (
	"context"
	"fmt"
	"testing"
)

// ADR-0162 §4 (ut-docs#4463): cloud_links maps a cloud catalogue row
// (entity, cloud_id) to its local id, stamped with the version applied and
// the resync generation that last saw it; cloud_apply_failures keeps the
// latest refusal per row.
func TestCloudLinks_UpsertLookupStaleDelete(t *testing.T) {
	d := openMigratedDB(t, "cloud-links.db")
	ctx := context.Background()
	links := NewCloudLinkRepo()

	if _, ok, err := links.LocalID(ctx, d.DB, "item", "c1"); err != nil || ok {
		t.Fatalf("empty table: ok=%v err=%v", ok, err)
	}
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []CloudLink{
		{Entity: "item", CloudID: "c1", LocalID: "c1", Version: 3, Gen: 0},
		{Entity: "item", CloudID: "c2", LocalID: "local-2", Version: 4, Gen: 0},
		{Entity: "category", CloudID: "c1", LocalID: "cat-1", Version: 5, Gen: 1},
	} {
		if err := links.Upsert(ctx, tx, l); err != nil {
			t.Fatal(err)
		}
	}
	// Upsert re-stamps version and gen, never duplicates.
	if err := links.Upsert(ctx, tx, CloudLink{Entity: "item", CloudID: "c1", LocalID: "c1", Version: 9, Gen: 1}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if id, ok, err := links.LocalID(ctx, d.DB, "item", "c2"); err != nil || !ok || id != "local-2" {
		t.Fatalf("LocalID = %q %v %v, want local-2", id, ok, err)
	}
	if id, _, _ := links.LocalID(ctx, d.DB, "category", "c1"); id != "cat-1" {
		t.Fatalf("entity scoping: got %q, want cat-1", id)
	}
	stale, err := links.Stale(ctx, d.DB, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0].CloudID != "c2" || stale[0].Entity != "item" || stale[0].LocalID != "local-2" {
		t.Fatalf("Stale(gen 1) = %+v, want only item c2", stale)
	}
	tx, _ = d.DB.BeginTx(ctx, nil)
	if err := links.Touch(ctx, tx, "item", "c2", 1); err != nil {
		t.Fatal(err)
	}
	if err := links.Delete(ctx, tx, "item", "c1"); err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit()
	if stale, _ := links.Stale(ctx, d.DB, 1, 100); len(stale) != 0 {
		t.Fatalf("after touch: stale = %+v", stale)
	}
	if _, ok, _ := links.LocalID(ctx, d.DB, "item", "c1"); ok {
		t.Fatal("deleted link still found")
	}
	if err := links.Upsert(ctx, nil, CloudLink{Entity: "", CloudID: "x", LocalID: "y"}); err == nil {
		t.Fatal("blank entity accepted")
	}
}

func TestCloudApplyFailures_LatestPerRow(t *testing.T) {
	d := openMigratedDB(t, "cloud-failures.db")
	ctx := context.Background()
	links := NewCloudLinkRepo()

	if err := links.RecordFailure(ctx, d.DB, "item", "c1", 3, "the price must not be negative"); err != nil {
		t.Fatal(err)
	}
	if err := links.RecordFailure(ctx, d.DB, "item", "c1", 7, "colour \"#123\" is not one of the palette colours"); err != nil {
		t.Fatal(err)
	}
	if err := links.RecordFailure(ctx, d.DB, "category", "c1", 2, "x"); err != nil {
		t.Fatal(err)
	}
	got, err := links.ListFailures(ctx, d.DB, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("failures = %+v, want 2 (latest per row)", got)
	}
	var item CloudApplyFailure
	for _, f := range got {
		if f.Entity == "item" {
			item = f
		}
	}
	if item.Version != 7 || item.Reason == "" || item.At == "" {
		t.Fatalf("item failure = %+v, want version 7", item)
	}
	if err := links.ClearFailure(ctx, d.DB, "item", "c1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := links.ListFailures(ctx, d.DB, 10); len(got) != 1 || got[0].Entity != "category" {
		t.Fatalf("after clear: %+v", got)
	}
}

// ut-docs#4493: ListFailures feeds the till's apply-failures report —
// newest `at` first, ties broken by (entity, cloud_id), at most limit rows.
func TestCloudApplyFailures_ListNewestFirst(t *testing.T) {
	d := openMigratedDB(t, "cloud-failures-list.db")
	ctx := context.Background()
	links := NewCloudLinkRepo()

	if got, err := links.ListFailures(ctx, d.DB, 10); err != nil || len(got) != 0 {
		t.Fatalf("empty table: %+v %v", got, err)
	}
	for _, r := range []struct {
		entity, id, at string
		version        int64
	}{
		{"item", "b", "2026-10-16T10:00:00Z", 4},
		{"item", "a", "2026-10-16T10:00:00Z", 3},
		{"category", "z", "2026-10-16T10:00:00Z", 2},
		{"tax_code", "t", "2026-10-16T12:00:00Z", 9},
		{"item", "old", "2026-10-15T09:00:00Z", 1},
	} {
		if _, err := d.DB.ExecContext(ctx, `INSERT INTO cloud_apply_failures (entity, cloud_id, version, reason, at) VALUES (?, ?, ?, ?, ?)`,
			r.entity, r.id, r.version, "reason "+r.id, r.at); err != nil {
			t.Fatal(err)
		}
	}
	got, err := links.ListFailures(ctx, d.DB, 10)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, f := range got {
		order = append(order, f.Entity+"/"+f.CloudID)
	}
	want := []string{"tax_code/t", "category/z", "item/a", "item/b", "item/old"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	if got[0].Version != 9 || got[0].Reason != "reason t" || got[0].At != "2026-10-16T12:00:00Z" {
		t.Fatalf("first = %+v", got[0])
	}
	got, err = links.ListFailures(ctx, d.DB, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].CloudID != "z" {
		t.Fatalf("limit 2 = %+v, want the two newest", got)
	}
}

// ut-docs#4494: the seed ids are migration 001's tax codes, and only an
// unlinked one of exactly the given name is adoptable.
func TestAdoptableSeedTaxCode(t *testing.T) {
	d := openMigratedDB(t, "cloud-links-seed-tax.db")
	ctx := context.Background()
	links := NewCloudLinkRepo()
	for _, id := range SeedTaxCodeIDs {
		var n int
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM tax_codes WHERE id = ?`, id).Scan(&n); err != nil || n != 1 {
			t.Fatalf("seed tax code %s missing from a migrated till (n=%d err=%v)", id, n, err)
		}
	}
	mustExec(t, d, `INSERT INTO tax_codes (id, name, rate_basis_points, is_active) VALUES ('tax_local', 'Takeaway VAT', 700, 1)`)
	for _, c := range []struct {
		name, want string
		ok         bool
	}{
		{"Standard VAT", "tax_std", true},
		{"Zero-rated", "tax_zero", true},
		{"standard vat", "", false}, // BINARY: a different name to UNIQUE too
		{"Takeaway VAT", "", false}, // not a seed row
		{"Nothing", "", false},
	} {
		got, ok, err := links.AdoptableSeedTaxCode(ctx, d.DB, "tax_code", c.name)
		if err != nil || ok != c.ok || got != c.want {
			t.Fatalf("%q: got %q %v %v, want %q %v", c.name, got, ok, err, c.want, c.ok)
		}
	}
	if err := links.Upsert(ctx, d.DB, CloudLink{Entity: "tax_code", CloudID: "c1", LocalID: "tax_std", Version: 1}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := links.AdoptableSeedTaxCode(ctx, d.DB, "tax_code", "Standard VAT"); err != nil || ok {
		t.Fatalf("a linked seed row is adoptable: ok=%v err=%v", ok, err)
	}
	// A link of another entity to the same local id does not count.
	if _, ok, _ := links.AdoptableSeedTaxCode(ctx, d.DB, "tax_code", "Reduced VAT"); !ok {
		t.Fatal("Reduced VAT not adoptable")
	}
	if err := links.Upsert(ctx, d.DB, CloudLink{Entity: "item", CloudID: "c2", LocalID: "tax_red", Version: 1}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := links.AdoptableSeedTaxCode(ctx, d.DB, "tax_code", "Reduced VAT"); !ok {
		t.Fatal("an item link with the same local id blocked the tax code's adoption")
	}
}
