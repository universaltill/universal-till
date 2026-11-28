package pos

import (
	"context"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#7206: a page plugin's overlay adds basket lines with an opaque
// per-line ref. The ref (and its plugin) ride on the line through
// hold/resume and complete sale; a ref line never merges.

func refTestResolver() mapResolver {
	return mapResolver{
		"SVC": {SKU: "SVC", Name: "Skin fade", Qty: 1, PriceCents: 2500, ItemID: "svc", TaxRateBP: 0},
	}
}

func TestScanRefLine_TwoLinesNeverMerge_7206(t *testing.T) {
	s := NewServiceWithResolver(Config{}, refTestResolver())
	if _, ok := s.ScanRefLine("SVC", 1, "com.example.page", "appt:1:0"); !ok {
		t.Fatal("first ref line not added")
	}
	b, ok := s.ScanRefLine("SVC", 1, "com.example.page", "appt:1:1")
	if !ok || len(b.Lines) != 2 {
		t.Fatalf("lines = %d ok=%v, want 2 distinct ref lines", len(b.Lines), ok)
	}
	// A plain scan of the same SKU adds its own line: never merged into a ref line.
	b, _, _ = s.ScanQtyWithResult("SVC", 1)
	if len(b.Lines) != 3 {
		t.Fatalf("plain scan lines = %d, want 3 (ref lines never absorb a scan)", len(b.Lines))
	}
	if b.Lines[0].PluginRef != "appt:1:0" || b.Lines[1].PluginRef != "appt:1:1" || b.Lines[2].PluginRef != "" {
		t.Errorf("refs = %q %q %q", b.Lines[0].PluginRef, b.Lines[1].PluginRef, b.Lines[2].PluginRef)
	}
	if b.Lines[0].PluginRefBy != "com.example.page" || b.Lines[0].PriceCents != money.FromMinor(2500) {
		t.Errorf("line 0 = %+v, want plugin com.example.page at catalogue price 2500", b.Lines[0])
	}
	// The scan cache must not carry the ref into later plain scans.
	b, _, _ = s.ScanQtyWithResult("SVC", 1)
	if got := b.Lines[2]; got.PluginRef != "" || got.Qty != 2 {
		t.Errorf("plain line = %+v, want no ref and qty 2", got)
	}
}

func TestScanRefLine_UnknownSKU_7206(t *testing.T) {
	s := NewServiceWithResolver(Config{}, refTestResolver())
	if b, ok := s.ScanRefLine("NOPE", 1, "p", "r"); ok || len(b.Lines) != 0 {
		t.Fatalf("unknown sku: ok=%v lines=%d", ok, len(b.Lines))
	}
}

func TestHoldResume_KeepsPluginRef_7206(t *testing.T) {
	s := NewServiceWithResolver(Config{}, refTestResolver())
	s.ScanRefLine("SVC", 1, "com.example.page", "appt:9:0")
	snap := s.Snapshot()
	s2 := NewServiceWithResolver(Config{}, refTestResolver())
	s2.RestoreHeld(snap, HeldOrigin{})
	b := s2.Basket()
	if len(b.Lines) != 1 || b.Lines[0].PluginRef != "appt:9:0" || b.Lines[0].PluginRefBy != "com.example.page" || !b.Lines[0].NoMerge {
		t.Fatalf("resumed line = %+v, want ref and plugin kept, never-merge kept", b.Lines)
	}
}

func TestCompleteSale_StoresPluginRef_7206(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(testsupport.MigratedDBFile(t, "plugin-ref-sale.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	for _, q := range []string{
		`INSERT INTO stock_locations (id, name) VALUES ('loc1', 'Main')`,
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('svc', 'SVC', 'Skin fade', 2500, 1)`,
		`INSERT INTO inventory (id, item_id, variant_id, location_id, quantity, updated_at) VALUES ('inv-svc', 'svc', NULL, 'loc1', 10, datetime('now'))`,
		`INSERT OR IGNORE INTO payment_methods (id, name, type, is_active) VALUES ('cash', 'Cash', 'cash', 1)`,
	} {
		if _, err := d.DB.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	line := func(ref, by string) SaleLineInput {
		return SaleLineInput{ItemID: "svc", SKU: "SVC", Name: "Skin fade", Qty: 1, UnitPrice: money.FromMinor(2500), LocationID: "loc1", PluginRef: ref, PluginRefBy: by}
	}
	saleID, err := CompleteSale(ctx, d.DB, SaleInput{
		SaleType: "sale", Currency: "GBP",
		Lines:    []SaleLineInput{line("appt:1:0", "com.example.page"), line("appt:1:1", "com.example.page"), line("", "")},
		Payments: []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(7500), Currency: "GBP"}},
	})
	if err != nil {
		t.Fatalf("CompleteSale: %v", err)
	}
	rows, err := d.DB.Query(`SELECT COALESCE(plugin_ref,''), COALESCE(plugin_ref_by,'') FROM sale_lines WHERE sale_id = ? ORDER BY line_no`, saleID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got [][2]string
	for rows.Next() {
		var r, by string
		if err := rows.Scan(&r, &by); err != nil {
			t.Fatal(err)
		}
		got = append(got, [2]string{r, by})
	}
	want := [][2]string{{"appt:1:0", "com.example.page"}, {"appt:1:1", "com.example.page"}, {"", ""}}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("stored refs = %v, want %v", got, want)
	}
}

// ScanRefLine must hand back a private copy of the basket, like every other
// locked method: returning &s.basket aliases live state that the next
// locked call rewrites while the caller (pages/pos_api.go) still reads it.
// Run with -race.
func TestScanRefLine_ReturnsPrivateBasketCopy_7206(t *testing.T) {
	s := NewServiceWithResolver(Config{}, refTestResolver())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			s.ScanQtyWithResult("SVC", 1)
		}
	}()
	var sink int
	for i := 0; i < 200; i++ {
		b, _ := s.ScanRefLine("SVC", 1, "com.example.page", "appt:1:0")
		sink += len(b.Lines) + int(b.Total.Minor())
	}
	<-done
	_ = sink
}

// A held-sale snapshot is JSON that also crosses tills
// (/api/sync/held-sales/upsert), so a restored ref is re-validated: a ref or
// plugin id that would never have been accepted at pick time is dropped
// (both together), and the line itself is kept.
func TestRestoreHeld_DropsInvalidPluginRef_7206(t *testing.T) {
	cases := []struct{ name, ref, by string }{
		{"ref with space", "appt 1", "com.example.page"},
		{"ref too long", strings.Repeat("a", 65), "com.example.page"},
		{"ref with markup", "<script>", "com.example.page"},
		{"bad plugin id", "appt:1:0", "Bad/Plugin"},
		{"ref without plugin", "appt:1:0", ""},
		{"plugin without ref", "", "com.example.page"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServiceWithResolver(Config{}, refTestResolver())
			s.ScanRefLine("SVC", 1, "com.example.page", "appt:1:0")
			snap := s.Snapshot()
			snap.Lines[0].PluginRef, snap.Lines[0].PluginRefBy = tc.ref, tc.by
			s2 := NewServiceWithResolver(Config{}, refTestResolver())
			s2.RestoreHeld(snap, HeldOrigin{})
			b := s2.Basket()
			if len(b.Lines) != 1 {
				t.Fatalf("lines = %d, want the line kept", len(b.Lines))
			}
			if b.Lines[0].PluginRef != "" || b.Lines[0].PluginRefBy != "" {
				t.Errorf("restored ref=%q by=%q, want both dropped", b.Lines[0].PluginRef, b.Lines[0].PluginRefBy)
			}
			if b.Lines[0].SKU != "SVC" || b.Lines[0].PriceCents != money.FromMinor(2500) {
				t.Errorf("line = %+v, want SVC at 2500 kept", b.Lines[0])
			}
		})
	}
}
