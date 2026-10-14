package data

import (
	"context"
	"testing"
	"time"
)

// seedRunningOutCola seeds one fast seller (~2/day) with 6 on hand: ~3 days
// of cover, so IsRunningOut is true.
func seedRunningOutCola(t *testing.T) *POSRepo {
	t.Helper()
	d, repo := openB8InvDB(t)
	seedB8Item(t, d, "ro-cola", "RO-COLA", "Fast Cola", 0, 1)
	seedB8Inventory(t, d, "ro-cola", "loc_main", 6)
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, tax_total, total, created_at)
	          VALUES ('ro-s','RO-R','completed','sale',5600,0,5600, datetime('now','-1 days'))`)
	mustExec(t, d, `INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
	          VALUES ('ro-l','ro-s',1,'ro-cola','Fast Cola',56,100,0,0,0,5600,5600)`)
	return repo
}

// ut-docs#4134: a shop that sells without tracking stock never has a
// running-out count, however low the recorded quantities are.
func TestRunningOutCount_UntrackedShopIsZero(t *testing.T) {
	repo := seedRunningOutCola(t)
	until := time.Now().Add(time.Second)

	n, err := repo.RunningOutCount(context.Background(), until, false)
	if err != nil || n != 1 {
		t.Fatalf("tracked shop: RunningOutCount = (%d, %v), want (1, nil)", n, err)
	}
	n, err = repo.RunningOutCount(context.Background(), until, true)
	if err != nil || n != 0 {
		t.Fatalf("untracked shop: RunningOutCount = (%d, %v), want (0, nil)", n, err)
	}
}

func TestShopStockUntracked(t *testing.T) {
	d, _ := openB8InvDB(t)
	repo := NewSettingsRepo(d.DB)
	ctx := context.Background()

	if got, err := repo.ShopStockUntracked(ctx); err != nil || got {
		t.Fatalf("missing key = (%v, %v), want (false, nil)", got, err)
	}
	for _, c := range []struct {
		val  string
		want bool
	}{{"true", true}, {"false", false}, {"garbage", false}} {
		if err := repo.Set(ctx, KeyAllowNegativeInventory, c.val); err != nil {
			t.Fatalf("set: %v", err)
		}
		if got, err := repo.ShopStockUntracked(ctx); err != nil || got != c.want {
			t.Fatalf("value %q = (%v, %v), want (%v, nil)", c.val, got, err, c.want)
		}
	}
	mustExec(t, d, `DROP TABLE settings`)
	if _, err := repo.ShopStockUntracked(ctx); err == nil {
		t.Fatal("want the DB error propagated when the settings table is gone")
	}
}
