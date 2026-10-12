package ui

import "testing"

// ut-docs#4214: every resolver tier carries items.item_type onto the basket
// line (a variant line carries its parent item's), so the scan handler can
// route a gift voucher item to its popup.
func TestResolve_ItemTypeCarriedOnEveryTier(t *testing.T) {
	store, db := seedResolverFixture(t)
	mustExec(t, db, `UPDATE items SET item_type = 'gift_voucher' WHERE id IN ('itm-cof','itm-ban')`)
	r := PriceResolverAdapter{Store: store}
	for _, code := range []string{"IB-1", "COF-SKU", "SC-1", "VB-1"} {
		line, ok := r.Resolve(code)
		if !ok || line.ItemType != "gift_voucher" || !line.IsGiftVoucherItem() {
			t.Errorf("%s: want gift_voucher, got ok=%v type=%q", code, ok, line.ItemType)
		}
	}
	mustExec(t, db, `UPDATE items SET item_type = 'standard'`)
	if line, _ := r.Resolve("IB-1"); line.IsGiftVoucherItem() {
		t.Errorf("standard item read as gift voucher: %+v", line)
	}
}
