package data

import (
	"context"
	"fmt"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#4650: one locale-aware search fold for vouchers, customers and the
// item search.

const nfdMuller = "Müller" // "Müller" as macOS pastes it (NFD)

func TestSearchFold(t *testing.T) {
	cases := []struct{ a, b string }{
		{"MÜLLER", "Müller"},
		{nfdMuller, "Müller"},
		{"ISPARTA", "ısparta"},   // Turkish dotless ı
		{"İSTANBUL", "istanbul"}, // Turkish dotted İ
		{"ıstanbul", "Istanbul"},
		{"STRASSE", "Straße"},
		{"ΟΔΟΣ", "\u03bf\u03b4\u03bf\u03c2"}, // final sigma ς meets σ
		{"  Çay ", "çay"},
		{"i\u0307zmir", "İzmir"}, // İ typed decomposed
	}
	for _, c := range cases {
		if SearchFold(c.a) != SearchFold(c.b) {
			t.Errorf("SearchFold(%q)=%q, SearchFold(%q)=%q: want equal", c.a, SearchFold(c.a), c.b, SearchFold(c.b))
		}
	}
	if SearchFold("Meier") == SearchFold("Müller") {
		t.Fatal("different names must not fold together")
	}
	if got := SearchFold(""); got != "" {
		t.Fatalf("empty: %q", got)
	}
}

func TestSearchItemsForShortcuts_UnicodeFold(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-m", SKU: "M1", Name: "Müller Brot", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-t", SKU: "T1", Name: "ısparta gülü", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-p", SKU: "P1", Name: "50% Rabatt", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-o", SKU: "O1", Name: "Other", BasePrice: 100, IsActive: true})
	repo := NewPOSRepo(db)
	ctx := context.Background()

	for q, want := range map[string]string{"MÜLLER": "i-m", nfdMuller: "i-m", "ISPARTA": "i-t", "GÜLÜ": "i-t", "50%": "i-p"} {
		res, err := repo.SearchItemsForShortcuts(ctx, q, 0, 10)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		if len(res) != 1 || res[0].ItemID != want {
			t.Fatalf("%q: want [%s], got %+v", q, want, res)
		}
	}
	// An empty query still lists every active item, name order (as LIKE '%%' did).
	if res, _ := repo.SearchItemsForShortcuts(ctx, "  ", 0, 10); len(res) != 4 || res[0].ItemID != "i-p" || res[1].ItemID != "i-m" {
		t.Fatalf("empty query: %+v", res)
	}
	// SKU and barcode keep their substring match.
	if res, _ := repo.SearchItemsForShortcuts(ctx, "o1", 0, 10); len(res) != 1 || res[0].ItemID != "i-o" {
		t.Fatalf("sku match: %+v", res)
	}
}

func TestSearchCustomers_UnicodeFold(t *testing.T) {
	d := b8OpenDB(t, "customer-search-fold.db")
	mustExec(t, d, `INSERT INTO customers (id, name, email) VALUES
		('c-m', 'Hans Müller', 'hans@example.com'),
		('c-t', 'Ayşe Isparta', ''),
		('c-o', 'Other Person', 'other@example.com')`)
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()
	for q, want := range map[string]string{"MÜLLER": "c-m", nfdMuller: "c-m", "ısparta": "c-t", "AYŞE": "c-t", "OTHER@": "c-o"} {
		got, err := repo.SearchCustomers(ctx, q, 25)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		if len(got) != 1 || got[0].ID != want {
			t.Fatalf("%q: want [%s], got %+v", q, want, got)
		}
	}
}

func TestListVouchers_LocaleFold(t *testing.T) {
	d := b8OpenDB(t, "voucher-list-fold.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	vlSeed(t, ctx, repo, Voucher{ID: "GS-1", HolderLabel: "Müller", OriginalAmountMinor: 1000, CreatedAt: "2026-10-01T10:00:00Z"})
	vlSeed(t, ctx, repo, Voucher{ID: "GS-2", HolderLabel: "ısparta", OriginalAmountMinor: 1000, CreatedAt: "2026-10-02T10:00:00Z"})
	vlSeed(t, ctx, repo, Voucher{ID: "GS-3", HolderLabel: "Straße", OriginalAmountMinor: 1000, CreatedAt: "2026-10-03T10:00:00Z"})
	for q, want := range map[string]string{nfdMuller: "GS-1", "ISPARTA": "GS-2", "STRASSE": "GS-3"} {
		got, err := repo.ListVouchers(ctx, VoucherFilter{Query: q})
		if err != nil || !vlEqual(vlIDs(got), []string{want}) {
			t.Fatalf("%q: want [%s], got %v %v", q, want, vlIDs(got), err)
		}
	}
}

// BenchmarkSearchItemsForShortcuts10k measures the sell screen's live search
// over a 10k-item catalogue (ut-docs#4650 AC), ASCII names and all-Turkish
// names (every row on SearchFold's slow path).
func BenchmarkSearchItemsForShortcuts10k(b *testing.B) {
	for _, bc := range []struct{ name, nameFmt, query string }{
		{"ascii", "Brown Bread %05d", "BREAD 0999"},
		{"turkish", "Ürün Çeşit %05d", "ÇEŞİT 0999"},
	} {
		b.Run(bc.name, func(b *testing.B) {
			d, err := db.Open(testsupport.MigratedDBFile(b, "search-bench-"+bc.name+".db"))
			if err != nil {
				b.Fatal(err)
			}
			defer d.Close()
			tx, err := d.DB.Begin()
			if err != nil {
				b.Fatal(err)
			}
			for i := 0; i < 10000; i++ {
				if _, err := tx.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES (?, ?, ?, 100, 1)`,
					fmt.Sprintf("item-%05d", i), fmt.Sprintf("SKU%05d", i), fmt.Sprintf(bc.nameFmt, i)); err != nil {
					b.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			repo := NewPOSRepo(d.DB)
			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				res, err := repo.SearchItemsForShortcuts(ctx, bc.query, 0, 20)
				if err != nil {
					b.Fatal(err)
				}
				if len(res) != 10 {
					b.Fatalf("want 10 matches, got %d", len(res))
				}
			}
		})
	}
}
