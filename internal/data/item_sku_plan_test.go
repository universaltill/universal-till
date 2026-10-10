package data

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#3280: the backfill and its preview plan every missing SKU in
// memory (skuPlanner) instead of calling nextItemSKU once per item. These
// tests pin that the in-memory plan assigns exactly what the per-item
// generator assigned, that the preview takes no write lock, and that a
// large single-category backfill is fast.

func planTestDB(t *testing.T, name string) *db.DB {
	t.Helper()
	d, err := db.Open(testsupport.MigratedDBFile(t, name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func planExec(t *testing.T, d *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("%s %v: %v", q, args, err)
	}
}

func planItem(t *testing.T, d *sql.DB, id, name string, sku, categoryID any) {
	t.Helper()
	planExec(t, d, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES(?,?,?,?,?,1)`,
		id, sku, name, 100, categoryID)
}

// referenceMissingSKUs is the pre-#3280 algorithm: nextItemSKU once per
// blank item, each UPDATE visible to the next call, all rolled back.
func referenceMissingSKUs(t *testing.T, d *sql.DB) []AssignedItemSKU {
	t.Helper()
	ctx := context.Background()
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	items, err := itemsMissingSKU(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	var out []AssignedItemSKU
	for _, it := range items {
		var cat *string
		if it.categoryID.Valid {
			cat = &it.categoryID.String
		}
		sku, err := nextItemSKU(ctx, tx, cat, nil)
		if err != nil {
			t.Fatalf("reference nextItemSKU(%s): %v", it.id, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE items SET sku = ? WHERE id = ?`, sku, it.id); err != nil {
			t.Fatal(err)
		}
		out = append(out, AssignedItemSKU{ItemID: it.id, Name: it.name, SKU: sku})
	}
	return out
}

// seedSKUEdgeCases covers every branch of nextItemSKU: numeric width and
// tie-breaking, the 7-digit cap falling back to the prefix, collisions
// with item/variant SKUs and both barcode tables, two categories sharing
// a prefix, non-Latin and unknown categories, the old hex shape, a
// lower-case look-alike and an over-long remainder.
func seedSKUEdgeCases(t *testing.T, d *sql.DB) {
	t.Helper()
	cats := [][2]string{
		{"c-kuchen", "Kuchen"}, {"c-bar", "Bar"}, {"c-cafe", "Café"},
		{"c-cafeteria", "Cafeteria"}, {"c-eis", "Eis"}, {"c-fa", "کیک"},
		{"c-pad", "Pads"}, {"c-tie", "Tie"}, {"c-empty", "Übergrößen"},
	}
	for _, c := range cats {
		planExec(t, d, `INSERT INTO categories(id, name) VALUES(?, ?)`, c[0], c[1])
	}
	// Kuchen: numeric, plus an over-long remainder that must not count
	// for the KUC prefix.
	planItem(t, d, "k1", "Apfel", "30089", "c-kuchen")
	planItem(t, d, "k2", "Birne", "30091", "c-kuchen")
	planItem(t, d, "kx", "Long", "KUC-1234567890123456789", nil)
	// Bar: one below the 7-digit cap, so the second blank item overflows
	// to the BAR- prefix.
	planItem(t, d, "b1", "Gin", "9999998", "c-bar")
	// Café and Cafeteria share CAF; the next numbers are taken by a
	// barcode, a variant SKU and a variant barcode.
	planItem(t, d, "f1", "Espresso", "CAF-0001", "c-cafe")
	planItem(t, d, "f2", "Mocha", "caf-0099", "c-cafe")
	planExec(t, d, `INSERT INTO item_barcodes(barcode, item_id) VALUES('CAF-0002', 'f1')`)
	planExec(t, d, `INSERT INTO item_variants(id, item_id, sku, name, price) VALUES('v1', 'f1', 'CAF-0003', 'Large', 200)`)
	planExec(t, d, `INSERT INTO variant_barcodes(barcode, variant_id) VALUES('CAF-0004', 'v1')`)
	// Eis: numeric with a barcode on the next number.
	planItem(t, d, "e1", "Vanille", "500", "c-eis")
	planExec(t, d, `INSERT INTO item_barcodes(barcode, item_id) VALUES('501', 'e1')`)
	// Pads: leading zeros keep their width.
	planItem(t, d, "p1", "Pad", "0001", "c-pad")
	// Tie: same value, the wider spelling sets the width.
	planItem(t, d, "t1", "Tie A", "99", "c-tie")
	planItem(t, d, "t2", "Tie B", "0098", "c-tie")
	// No category: ITEM- continues past 7, ignoring the old hex shape.
	planItem(t, d, "n1", "Old", "ITEM-0007", nil)
	planItem(t, d, "n2", "Older", "ITEM-3F9A0C12", nil)

	blank := []struct {
		id, name string
		sku, cat any
	}{
		{"zk1", "Kuchen A", nil, "c-kuchen"}, {"zk2", "Kuchen B", "", "c-kuchen"},
		{"zk3", "Kuchen C", "  ", "c-kuchen"},
		{"zb1", "Bar A", nil, "c-bar"}, {"zb2", "Bar B", nil, "c-bar"}, {"zb3", "Bar C", nil, "c-bar"},
		{"zf1", "Cafe A", nil, "c-cafe"}, {"zf2", "Cafe B", nil, "c-cafe"},
		{"zt1", "Cafeteria A", nil, "c-cafeteria"},
		{"ze1", "Eis A", nil, "c-eis"}, {"ze2", "Eis B", nil, "c-eis"},
		{"zp1", "Pad A", nil, "c-pad"}, {"zi1", "Tie C", nil, "c-tie"},
		{"zfa", "Persian", nil, "c-fa"}, {"zu1", "Big", nil, "c-empty"}, {"zu2", "Bigger", nil, "c-empty"},
		{"zn1", "None A", nil, nil}, {"zn2", "None B", nil, nil},
	}
	for _, b := range blank {
		planItem(t, d, b.id, b.name, b.sku, b.cat)
	}
	// A category id with no categories row (foreign keys off only for the
	// insert): the default prefix, like nextItemSKU's ErrNoRows branch.
	planExec(t, d, `PRAGMA foreign_keys = OFF`)
	planItem(t, d, "zg1", "Ghost", nil, "c-ghost")
	planExec(t, d, `PRAGMA foreign_keys = ON`)
}

func TestSKUPlan_MatchesPerItemGenerator(t *testing.T) {
	d := planTestDB(t, "sku-plan-equivalence.db")
	d.SetMaxOpenConns(1) // keep the PRAGMA foreign_keys toggle on one connection
	seedSKUEdgeCases(t, d.DB)
	ctx := context.Background()
	repo := NewCatalogRepo(d.DB)

	want := referenceMissingSKUs(t, d.DB)
	if len(want) != 19 {
		t.Fatalf("reference assigned %d SKUs, want 19: %+v", len(want), want)
	}
	plan, err := repo.PlanMissingItemSKUs(ctx)
	if err != nil {
		t.Fatalf("PlanMissingItemSKUs: %v", err)
	}
	if !reflect.DeepEqual(plan, want) {
		t.Errorf("plan differs from the per-item generator:\n got %+v\nwant %+v", plan, want)
	}
	done, err := repo.BackfillMissingItemSKUs(ctx)
	if err != nil {
		t.Fatalf("BackfillMissingItemSKUs: %v", err)
	}
	if !reflect.DeepEqual(done, want) {
		t.Errorf("backfill differs from the per-item generator:\n got %+v\nwant %+v", done, want)
	}
	for _, a := range want {
		var got string
		if err := d.QueryRow(`SELECT sku FROM items WHERE id = ?`, a.ItemID).Scan(&got); err != nil || got != a.SKU {
			t.Errorf("stored sku of %s = %q (%v), want %q", a.ItemID, got, err, a.SKU)
		}
	}
	// Spot-check a few of the shapes the fixture exists for.
	spot := map[string]string{"zb2": "BAR-0001", "zf1": "CAF-0005", "zt1": "CAF-0007", "ze1": "502", "zp1": "0002", "zi1": "100", "zn1": "ITEM-0008"}
	for _, a := range want {
		if w, ok := spot[a.ItemID]; ok && a.SKU != w {
			t.Errorf("%s got %q, want %q", a.ItemID, a.SKU, w)
		}
	}
}

func TestPlanMissingItemSKUs_TakesNoWriteLock(t *testing.T) {
	d := planTestDB(t, "sku-plan-no-write-lock.db")
	planItem(t, d.DB, "a", "A", nil, nil)
	ctx := context.Background()

	// Another writer holds the write lock (the DSN's _txlock=immediate).
	writer, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback() }()
	if _, err := writer.ExecContext(ctx, `UPDATE items SET name = 'A2' WHERE id = 'a'`); err != nil {
		t.Fatal(err)
	}

	pctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	plan, err := NewCatalogRepo(d.DB).PlanMissingItemSKUs(pctx)
	if err != nil {
		t.Fatalf("preview blocked behind a writer (it must take no write lock): %v", err)
	}
	if len(plan) != 1 || plan[0].SKU != "ITEM-0001" || plan[0].Name != "A" {
		t.Fatalf("plan = %+v, want one ITEM-0001 for the committed row A", plan)
	}
}

func TestBackfillMissingItemSKUs_2000InOneCategoryIsFast(t *testing.T) {
	cases := []struct {
		name, category, seedSKU, first, last string
	}{
		// Rule 1: the category's numbers continue.
		{"numeric", "Kuchen", "30000", "30001", "32000"},
		// Rule 2: past maxSKUProbe items in one prefix sequence.
		{"prefix", "Torten", "", "TOR-0001", "TOR-2000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := planTestDB(t, "sku-plan-2000-"+tc.name+".db")
			planExec(t, d.DB, `INSERT INTO categories(id, name) VALUES('c', ?)`, tc.category)
			if tc.seedSKU != "" {
				planItem(t, d.DB, "seed", "Seed", tc.seedSKU, "c")
			}
			tx, err := d.Begin()
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2000; i++ {
				if _, err := tx.Exec(`INSERT INTO items(id, name, base_price, category_id, is_active) VALUES(?,?,100,'c',1)`,
					fmt.Sprintf("i%04d", i), fmt.Sprintf("Item %04d", i)); err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}

			start := time.Now()
			got, err := NewCatalogRepo(d.DB).BackfillMissingItemSKUs(context.Background())
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("BackfillMissingItemSKUs: %v", err)
			}
			if len(got) != 2000 || got[0].SKU != tc.first || got[1999].SKU != tc.last {
				t.Fatalf("assigned %d, first %+v last %+v", len(got), got[0], got[len(got)-1])
			}
			// Measured: ~40ms plain (item-by-item it was ~2.5s), ~1.6s
			// under -race; CI runs internal/data without -race.
			budget := time.Second
			if raceEnabled {
				budget = 10 * time.Second
			}
			t.Logf("2,000-item backfill: %v", elapsed)
			if elapsed > budget {
				t.Fatalf("2,000-item backfill took %v, want under %v (ut-docs#3280)", elapsed, budget)
			}
		})
	}
}

// TestSKUPlan_MatchesPerItemGeneratorRandomized compares the planner with
// the per-item generator on random catalogs: categories sharing prefixes,
// numeric and prefixed SKUs near each other, and barcodes/variant SKUs
// sitting on the next free numbers.
func TestSKUPlan_MatchesPerItemGeneratorRandomized(t *testing.T) {
	names := []string{"Kuchen", "Kuchenbar", "Café", "Cafeteria", "Eis", "کیک", "Ab", "Übergrößen"}
	for seed := int64(1); seed <= 5; seed++ {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			d := planTestDB(t, fmt.Sprintf("sku-plan-random-%d.db", seed))
			for i, n := range names {
				planExec(t, d.DB, `INSERT INTO categories(id, name) VALUES(?, ?)`, fmt.Sprintf("c%d", i), n)
			}
			cat := func() any {
				if rng.Intn(5) == 0 {
					return nil
				}
				return fmt.Sprintf("c%d", rng.Intn(len(names)))
			}
			code := func() string {
				switch rng.Intn(4) {
				case 0:
					return fmt.Sprintf("%d", 30000+rng.Intn(40))
				case 1:
					return fmt.Sprintf("%0*d", 3+rng.Intn(3), rng.Intn(30))
				case 2:
					return fmt.Sprintf("%s-%04d", []string{"KUC", "CAF", "EIS", "ITEM", "AB", "UBE"}[rng.Intn(6)], 1+rng.Intn(30))
				default:
					return fmt.Sprintf("%07d", 9999990+rng.Intn(10))
				}
			}
			used := map[string]bool{}
			fresh := func() string {
				for {
					if c := code(); !used[c] {
						used[c] = true
						return c
					}
				}
			}
			for i := 0; i < 60; i++ {
				planItem(t, d.DB, fmt.Sprintf("s%02d", i), fmt.Sprintf("Has %02d", i), fresh(), cat())
			}
			for i := 0; i < 15; i++ {
				planExec(t, d.DB, `INSERT INTO item_barcodes(barcode, item_id) VALUES(?, 's00')`, fresh())
				planExec(t, d.DB, `INSERT INTO item_variants(id, item_id, sku, name, price) VALUES(?, 's00', ?, 'V', 1)`, fmt.Sprintf("v%02d", i), fresh())
			}
			for i := 0; i < 80; i++ {
				planItem(t, d.DB, fmt.Sprintf("z%02d", i), fmt.Sprintf("Blank %02d", rng.Intn(100)), nil, cat())
			}
			want := referenceMissingSKUs(t, d.DB)
			got, err := NewCatalogRepo(d.DB).PlanMissingItemSKUs(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("plan differs:\n got %+v\nwant %+v", got, want)
			}
		})
	}
}
