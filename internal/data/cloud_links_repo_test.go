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
	got, err := cloudApplyFailures(ctx, d.DB, 10)
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
	if got, _ := cloudApplyFailures(ctx, d.DB, 10); len(got) != 1 || got[0].Entity != "category" {
		t.Fatalf("after clear: %+v", got)
	}
}

// CloudApplyFailure is one cloud_apply_failures row: the latest refusal of
// a pulled cloud row.
type CloudApplyFailure struct {
	Entity  string
	CloudID string
	Version int64
	Reason  string
	At      string
}

// cloudApplyFailures lists up to limit recorded refusals, newest first.
// Test-only: reporting them to the cloud is ut-docs#4493.
func cloudApplyFailures(ctx context.Context, q SyncQuerier, limit int) ([]CloudApplyFailure, error) {
	rows, err := q.QueryContext(ctx, `
SELECT entity, cloud_id, version, reason, at FROM cloud_apply_failures
ORDER BY at DESC, entity, cloud_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("cloud apply failures: %w", err)
	}
	defer rows.Close()
	var out []CloudApplyFailure
	for rows.Next() {
		var f CloudApplyFailure
		if err := rows.Scan(&f.Entity, &f.CloudID, &f.Version, &f.Reason, &f.At); err != nil {
			return nil, fmt.Errorf("cloud apply failures: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
