package data

import (
	"context"
	"testing"
)

// ut-docs#5058 review F3: a replacement main till's M<hex>- receipt prefix
// must not leak into the numbers customers hear and read (order display
// numbers, counter orders): those number as the main till did.
func TestNextDisplayNo_ReplacementMainPrefixIsNotShown(t *testing.T) {
	d := openMigratedDB(t, "display-no-replacement.db")
	mustExec(t, d, `INSERT INTO settings (key, value) VALUES ('sync.receipt_prefix', 'M3f2a9c1e-')`)
	got, err := NewPOSRepo(d.DB).NextDisplayNo(context.Background(), nil)
	if err != nil || got != "1" {
		t.Fatalf("NextDisplayNo = (%q,%v), want (1,nil)", got, err)
	}
}

func TestKioskCounterOrdersRepo_ReplacementMainPrefixIsNotShown(t *testing.T) {
	d := openKioskCounterOrdersDB(t, "counter_orders_replacement.db")
	mustExec(t, d, `INSERT INTO settings (key, value) VALUES ('sync.receipt_prefix', 'M3f2a9c1e-')`)
	o, err := NewKioskCounterOrdersRepo(d.DB).Create(context.Background(), KioskCounterOrder{Lines: []KioskCounterOrderLine{{Name: "Tea", Qty: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if o.DisplayNo != "C-1" {
		t.Fatalf("display_no = %q, want C-1", o.DisplayNo)
	}
}
