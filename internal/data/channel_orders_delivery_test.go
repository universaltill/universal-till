package data

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

// ADR-0215, ut-docs#6581 (S3): the till side of a delivery channel order.

func sampleDeliveryOrder() ChannelOrderInput {
	in := sampleChannelOrder()
	in.ExternalOrderID, in.OrderNumber, in.DirectiveID = "d-501", "#D501", "dir-d501"
	in.Shipping, in.Total = 0, money.FromMinor(7000)
	in.Note = "No nuts please"
	in.Delivery = &ChannelDeliveryInput{
		Service: "delivery", AcceptBy: "2031-12-01T12:10:00Z", PrepMinutes: 15, CourierRef: "c-9", PickupCode: "A7",
		Tip: money.FromMinor(150), Fees: []ChannelDeliveryFee{{Kind: "delivery_fee", Amount: money.FromMinor(199)}},
	}
	in.Lines[0].Note = "extra napkins"
	in.Lines[0].Modifiers = []ChannelLineModifier{{ExternalID: "m1", Name: "Large", Qty: 1, PriceDelta: money.FromMinor(100)}}
	return in
}

func TestDelivery_RecordsDeliveryFieldsAndNoSaleRows(t *testing.T) {
	d, repo := openChannelOrders(t)
	res, err := repo.Apply(context.Background(), sampleDeliveryOrder())
	if err != nil || res.Duplicate || res.AutoRejected {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	o, ok, err := repo.Get(context.Background(), res.OrderID)
	if err != nil || !ok {
		t.Fatalf("get: %v %v", ok, err)
	}
	if o.Kind != "delivery" || o.Service != "delivery" || o.AcceptBy != "2031-12-01T12:10:00Z" || o.PrepMinutes != 15 ||
		o.PickupCode != "A7" || o.CourierRef != "c-9" || o.Tip != money.FromMinor(150) || o.Note != "No nuts please" {
		t.Fatalf("order = %+v", o)
	}
	if len(o.Fees) != 1 || o.Fees[0].Kind != "delivery_fee" || o.Fees[0].Amount != money.FromMinor(199) {
		t.Fatalf("fees = %+v", o.Fees)
	}
	if len(o.Lines) != 1 || o.Lines[0].Note != "extra napkins" || len(o.Lines[0].Modifiers) != 1 || o.Lines[0].Modifiers[0].PriceDelta != money.FromMinor(100) {
		t.Fatalf("lines = %+v", o.Lines)
	}
	for _, tbl := range []string{"sales", "payments"} {
		if n := scalarCO(t, d, `SELECT COUNT(*) FROM `+tbl); n != 0 {
			t.Fatalf("%s rows = %v, want none", tbl, n)
		}
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 8 {
		t.Fatalf("stock = %v, want 8", q)
	}
}

func TestDelivery_DuplicateRevisionMovesStockOnce(t *testing.T) {
	d, repo := openChannelOrders(t)
	in := sampleDeliveryOrder()
	if _, err := repo.Apply(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	res, err := repo.Apply(context.Background(), in)
	if err != nil || !res.Duplicate {
		t.Fatalf("second apply: %+v %v", res, err)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 8 {
		t.Fatalf("stock = %v, want 8 (moved once)", q)
	}
	if n := scalarCO(t, d, `SELECT COUNT(*) FROM channel_orders`); n != 1 {
		t.Fatalf("orders = %v", n)
	}
	in.Revision = 2
	in.Delivery.PrepMinutes = 25
	if res, err = repo.Apply(context.Background(), in); err != nil || res.Duplicate {
		t.Fatalf("revision 2: %+v %v", res, err)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 8 {
		t.Fatalf("stock after rev 2 = %v, want 8", q)
	}
	if n := scalarCO(t, d, `SELECT prep_minutes FROM channel_orders`); n != 25 {
		t.Fatalf("prep_minutes = %v", n)
	}
}

func deliveryUnmapped() ChannelOrderInput {
	in := sampleDeliveryOrder()
	in.Lines = append(in.Lines, ChannelOrderLineInput{LineID: "l-2", SKU: "NOPE-1", Name: "Mystery", Qty: 1, UnitPrice: money.FromMinor(0)})
	return in
}

func TestDelivery_UnmappedLineDefaultPolicyRejectsWithReasonAndMovesNoStock(t *testing.T) {
	d, repo := openChannelOrders(t)
	res, err := repo.Apply(context.Background(), deliveryUnmapped()) // policy "" = reject
	if err != nil || !res.AutoRejected {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 10 {
		t.Fatalf("stock = %v, want untouched 10", q)
	}
	var reason, action, why string
	if err := d.QueryRow(`SELECT reject_reason FROM channel_orders`).Scan(&reason); err != nil || reason != "unmapped_line" {
		t.Fatalf("reject_reason = %q %v", reason, err)
	}
	if err := d.QueryRow(`SELECT action, reject_reason FROM channel_order_actions WHERE state='pending'`).Scan(&action, &why); err != nil ||
		action != "reject" || why != "item_unavailable" {
		t.Fatalf("queued = %q %q %v", action, why, err)
	}
	in := deliveryUnmapped()
	in.Revision = 2
	if _, err := repo.Apply(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 10 {
		t.Fatalf("stock after rev 2 = %v, want 10", q)
	}
	if n := scalarCO(t, d, `SELECT COUNT(*) FROM channel_order_actions`); n != 1 {
		t.Fatalf("actions = %v, want exactly one auto reject", n)
	}
}

func TestDelivery_UnmappedLinePlaceholderPolicyKeepsOrderAndFlagsLine(t *testing.T) {
	d, repo := openChannelOrders(t)
	in := deliveryUnmapped()
	in.UnmappedPolicy = DeliveryUnmappedPlaceholder
	res, err := repo.Apply(context.Background(), in)
	if err != nil || res.AutoRejected || len(res.Unmatched) != 1 || res.Unmatched[0] != "l-2" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 8 {
		t.Fatalf("stock = %v, want 8", q)
	}
	if n := scalarCO(t, d, `SELECT COUNT(*) FROM channel_order_actions`); n != 0 {
		t.Fatalf("actions = %v, want none", n)
	}
}

func TestDelivery_UnmappedPolicyOnlyAppliesToDeliveryOrders(t *testing.T) {
	_, repo := openChannelOrders(t)
	in := sampleChannelOrder()
	in.Lines = append(in.Lines, ChannelOrderLineInput{LineID: "l-2", SKU: "NOPE-1", Name: "Mystery", Qty: 1})
	res, err := repo.Apply(context.Background(), in)
	if err != nil || res.AutoRejected {
		t.Fatalf("webshop order must keep today's behaviour: %+v %v", res, err)
	}
}

func applyStatus(t *testing.T, repo *ChannelOrdersRepo, rev int64, status string, mutate func(*ChannelOrderInput)) ChannelOrderResult {
	t.Helper()
	in := sampleDeliveryOrder()
	in.Revision, in.Status = rev, status
	if mutate != nil {
		mutate(&in)
	}
	res, err := repo.Apply(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestDelivery_StateMachineWalksForwardOnly(t *testing.T) {
	_, repo := openChannelOrders(t)
	res := applyStatus(t, repo, 1, "new", nil)
	get := func() ChannelOrder {
		o, _, err := repo.Get(context.Background(), res.OrderID)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	for i, want := range []string{"accepted", "preparing", "ready", "collected"} {
		applyStatus(t, repo, int64(i+2), want, nil)
		if got := get().Status; got != want {
			t.Fatalf("after %s: status = %s", want, got)
		}
	}
	applyStatus(t, repo, 6, "accepted", nil) // older step: ignored
	if got := get().Status; got != "collected" {
		t.Fatalf("regressed to %s", got)
	}
	applyStatus(t, repo, 7, "cancelled", nil) // terminal
	if got := get().Status; got != "collected" {
		t.Fatalf("collected is terminal, got %s", got)
	}
}

func TestDelivery_AcceptedMayJumpToReady(t *testing.T) {
	_, repo := openChannelOrders(t)
	res := applyStatus(t, repo, 1, "accepted", nil)
	applyStatus(t, repo, 2, "ready", nil)
	o, _, _ := repo.Get(context.Background(), res.OrderID)
	if o.Status != "ready" || o.FulfilmentRank != 3 {
		t.Fatalf("status=%s rank=%d", o.Status, o.FulfilmentRank)
	}
}

func TestDelivery_RejectedIsTerminalAndPutsStockBack(t *testing.T) {
	d, repo := openChannelOrders(t)
	res := applyStatus(t, repo, 1, "new", nil)
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 8 {
		t.Fatalf("stock = %v", q)
	}
	applyStatus(t, repo, 2, "rejected", nil)
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 10 {
		t.Fatalf("stock after reject = %v, want 10", q)
	}
	applyStatus(t, repo, 3, "accepted", nil)
	o, _, _ := repo.Get(context.Background(), res.OrderID)
	if o.Status != "rejected" {
		t.Fatalf("rejected is terminal, got %s", o.Status)
	}
	applyStatus(t, repo, 4, "rejected", nil)
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 10 {
		t.Fatalf("stock restored twice: %v", q)
	}
}

func TestDelivery_PlatformCancelAfterPreparingIsFlaggedLate(t *testing.T) {
	d, repo := openChannelOrders(t)
	res := applyStatus(t, repo, 1, "preparing", nil)
	cancel := func(in *ChannelOrderInput) {
		in.Lines[0].RefundedQuantity, in.Lines[0].RestockedQuantity = 2, 2
		in.RefundTotal = in.Total
	}
	res2 := applyStatus(t, repo, 2, "cancelled", cancel)
	if !res2.LateCancel {
		t.Fatal("cancel after preparing must be reported as late")
	}
	o, _, _ := repo.Get(context.Background(), res.OrderID)
	if o.Status != "cancelled" || !o.LateCancel {
		t.Fatalf("status=%s late=%v", o.Status, o.LateCancel)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != 10 {
		t.Fatalf("stock = %v, want 10 (channel_return)", q)
	}
	if n := scalarCO(t, d, `SELECT COUNT(*) FROM stock_movements WHERE type='channel_return'`); n != 1 {
		t.Fatalf("channel_return movements = %v", n)
	}
}

func TestDelivery_PlatformCancelBeforePreparingIsNotLate(t *testing.T) {
	_, repo := openChannelOrders(t)
	res := applyStatus(t, repo, 1, "accepted", nil)
	res2 := applyStatus(t, repo, 2, "cancelled", nil)
	o, _, _ := repo.Get(context.Background(), res.OrderID)
	if res2.LateCancel || o.LateCancel || o.Status != "cancelled" {
		t.Fatalf("late=%v/%v status=%s", res2.LateCancel, o.LateCancel, o.Status)
	}
}
