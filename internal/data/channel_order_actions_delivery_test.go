package data

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// ADR-0215 (ut-docs#6581): the till's delivery action ladder.

func deliveryOrderID(t *testing.T, repo *ChannelOrdersRepo, status string) string {
	t.Helper()
	res := applyStatus(t, repo, 1, status, nil)
	return res.OrderID
}

func TestDeliveryAllowedActions(t *testing.T) {
	cases := []struct {
		status  string
		rank    int
		pending []string
		want    []string
	}{
		{"new", 0, nil, []string{"accept", "reject"}},
		{"new", 0, []string{"accept"}, []string{"preparing", "ready", "prep_time", "collected"}},
		{"new", 0, []string{"accept", "ready"}, []string{"collected"}},
		{"new", 0, []string{"reject"}, nil},
		{"accepted", 1, nil, []string{"preparing", "ready", "prep_time", "collected"}},
		{"preparing", 2, nil, []string{"ready", "prep_time", "collected"}},
		{"ready", 3, nil, []string{"collected"}},
		{"collected", 4, nil, nil},
		{"rejected", 0, nil, nil},
		{"cancelled", 2, nil, nil},
	}
	for _, c := range cases {
		if got := DeliveryOrderAllowedActions(c.status, c.rank, c.pending); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s/%d/%v = %v, want %v", c.status, c.rank, c.pending, got, c.want)
		}
	}
}

func TestDeliveryEnqueue_AcceptRejectReadyAndOrder(t *testing.T) {
	d, repo := openChannelOrders(t)
	id := deliveryOrderID(t, repo, "new")
	acts := NewChannelOrderActionsRepo(d)
	ctx := context.Background()
	mustExecCO(t, d, `INSERT INTO users (id, username, display_name, role) VALUES ('u1','u1','Staff One','cashier')`)
	enq := func(in ChannelOrderActionInput) (string, bool, error) {
		in.OrderID, in.StaffID, in.Now = id, "u1", time.Now()
		return acts.Enqueue(ctx, in)
	}
	if _, _, err := enq(ChannelOrderActionInput{Action: "ready"}); err != nil {
		// ready straight after new is allowed only behind a queued accept in the same ladder.
		if !errors.Is(err, ErrChannelActionNotAllowed) {
			t.Fatal(err)
		}
	} else {
		t.Fatal("ready on a new order must be refused")
	}
	if _, dup, err := enq(ChannelOrderActionInput{Action: "accept", PrepMinutes: 20}); err != nil || dup {
		t.Fatalf("accept: %v %v", dup, err)
	}
	if _, dup, err := enq(ChannelOrderActionInput{Action: "accept", PrepMinutes: 20}); err != nil || !dup {
		t.Fatalf("second accept must be a duplicate: %v %v", dup, err)
	}
	if _, _, err := enq(ChannelOrderActionInput{Action: "reject", RejectReason: "busy"}); !errors.Is(err, ErrChannelActionNotAllowed) {
		t.Fatalf("reject behind a queued accept: %v", err)
	}
	if _, _, err := enq(ChannelOrderActionInput{Action: "ready"}); err != nil {
		t.Fatal(err)
	}
	rows, err := acts.ForOrder(ctx, id)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if rows[0].Action != "accept" || rows[0].PrepMinutes != 20 || rows[1].Action != "ready" || rows[0].Seq >= rows[1].Seq {
		t.Fatalf("queue order = %+v", rows)
	}
	if n := scalarCO(t, d, `SELECT COUNT(*) FROM audit_log WHERE action='channel_order_action_queued' AND entity_id=?`, id); n != 2 {
		t.Fatalf("audit rows = %v", n)
	}
	// The order itself did not move: an action never changes status.
	if o, _, _ := repo.Get(ctx, id); o.Status != "new" {
		t.Fatalf("status = %s", o.Status)
	}
}

func TestDeliveryEnqueue_RejectNeedsAKnownReasonAndEndsTheChain(t *testing.T) {
	d, repo := openChannelOrders(t)
	id := deliveryOrderID(t, repo, "new")
	acts := NewChannelOrderActionsRepo(d)
	ctx := context.Background()
	for _, bad := range []string{"", "because", "BUSY"} {
		_, _, err := acts.Enqueue(ctx, ChannelOrderActionInput{OrderID: id, Action: "reject", RejectReason: bad, Now: time.Now()})
		if !errors.Is(err, ErrChannelActionNotAllowed) {
			t.Fatalf("reason %q: %v", bad, err)
		}
	}
	if _, _, err := acts.Enqueue(ctx, ChannelOrderActionInput{OrderID: id, Action: "reject", RejectReason: "closing_early", Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := acts.Enqueue(ctx, ChannelOrderActionInput{OrderID: id, Action: "accept", Now: time.Now()}); !errors.Is(err, ErrChannelActionNotAllowed) {
		t.Fatalf("accept after a queued reject: %v", err)
	}
}

func TestDeliveryEnqueue_PrepTimeBoundsAndReplace(t *testing.T) {
	d, repo := openChannelOrders(t)
	id := deliveryOrderID(t, repo, "accepted")
	acts := NewChannelOrderActionsRepo(d)
	ctx := context.Background()
	for _, bad := range []int64{0, -5, DeliveryMaxPrepMinutes + 1} {
		_, _, err := acts.Enqueue(ctx, ChannelOrderActionInput{OrderID: id, Action: "prep_time", PrepMinutes: bad, Now: time.Now()})
		if !errors.Is(err, ErrChannelActionNotAllowed) {
			t.Fatalf("minutes %d: %v", bad, err)
		}
	}
	first, _, err := acts.Enqueue(ctx, ChannelOrderActionInput{OrderID: id, Action: "prep_time", PrepMinutes: 20, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	second, dup, err := acts.Enqueue(ctx, ChannelOrderActionInput{OrderID: id, Action: "prep_time", PrepMinutes: 30, Now: time.Now()})
	if err != nil || !dup || second != first {
		t.Fatalf("replace: %q %q %v %v", first, second, dup, err)
	}
	if n := scalarCO(t, d, `SELECT prep_minutes FROM channel_order_actions WHERE action_id=?`, first); n != 30 {
		t.Fatalf("prep_minutes = %v, want 30", n)
	}
}

func TestDeliveryEnqueue_InvalidTransitionsRefused(t *testing.T) {
	d, repo := openChannelOrders(t)
	acts := NewChannelOrderActionsRepo(d)
	ctx := context.Background()
	for _, c := range []struct{ status, action string }{
		{"collected", "ready"}, {"rejected", "accept"}, {"cancelled", "ready"}, {"ready", "accept"}, {"ready", "preparing"},
	} {
		in := sampleDeliveryOrder()
		in.ExternalOrderID, in.OrderNumber, in.Status = "x-"+c.status+c.action, "#x", c.status
		res, err := repo.Apply(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := acts.Enqueue(ctx, ChannelOrderActionInput{OrderID: res.OrderID, Action: c.action, Now: time.Now()}); !errors.Is(err, ErrChannelActionNotAllowed) {
			t.Fatalf("%s then %s: %v", c.status, c.action, err)
		}
	}
}

func TestDeliveryEnqueue_ShipAndCancelNotOfferedOnDelivery(t *testing.T) {
	d, repo := openChannelOrders(t)
	id := deliveryOrderID(t, repo, "accepted")
	acts := NewChannelOrderActionsRepo(d)
	for _, a := range []string{"ship", "cancel", "pick", "pack"} {
		if _, _, err := acts.Enqueue(context.Background(), ChannelOrderActionInput{OrderID: id, Action: a, Now: time.Now(),
			Shipment: &ChannelShipment{Carrier: "x"}, Cancel: &ChannelCancel{Reason: "other"}}); !errors.Is(err, ErrChannelActionNotAllowed) {
			t.Fatalf("%s: %v", a, err)
		}
	}
}

func TestWebshopEnqueue_RefusesDeliveryActions(t *testing.T) {
	d, repo := openChannelOrders(t)
	res, err := repo.Apply(context.Background(), sampleChannelOrder())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewChannelOrderActionsRepo(d).Enqueue(context.Background(), ChannelOrderActionInput{OrderID: res.OrderID, Action: "reject", RejectReason: "busy", Now: time.Now()}); !errors.Is(err, ErrChannelActionNotAllowed) {
		t.Fatalf("reject on a webshop order: %v", err)
	}
}
