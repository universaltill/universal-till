package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ut-docs#4768 slice D: the main till's outbox of staff actions on channel
// orders. A tap writes a row and an audit row; it never changes the order's
// status and never moves stock.

func caSeedOrder(t *testing.T, d *sql.DB, repo *ChannelOrdersRepo, ext, status string, rank int) string {
	t.Helper()
	in := sampleChannelOrder()
	in.ExternalOrderID, in.OrderNumber, in.DirectiveID = ext, "#"+ext, "dir-"+ext
	res, err := repo.Apply(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	mustExecCO(t, d, `UPDATE channel_orders SET status=?, fulfilment_rank=? WHERE id=?`, status, rank, res.OrderID)
	return res.OrderID
}

func caEnqueue(repo *ChannelOrderActionsRepo, order, action string) (string, bool, error) {
	in := ChannelOrderActionInput{OrderID: order, Action: action, StaffID: "", Now: time.Now()}
	switch action {
	case "ship":
		in.Shipment = &ChannelShipment{Carrier: "Royal Mail"}
	case "cancel":
		in.Cancel = &ChannelCancel{Reason: "other", Restock: true}
	}
	return repo.Enqueue(context.Background(), in)
}

func TestChannelOrderAllowedActions_Table(t *testing.T) {
	cases := []struct {
		name    string
		status  string
		rank    int
		pending []string
		want    []string
	}{
		{"new", "new", 0, nil, []string{"accept", "cancel"}},
		{"accepted", "accepted", 1, nil, []string{"pick", "pack", "ship", "cancel"}},
		{"picked", "picked", 2, nil, []string{"pack", "ship", "cancel"}},
		{"packed", "packed", 3, nil, []string{"ship", "cancel"}},
		{"shipped", "shipped", 4, nil, nil},
		{"partially refunded after pick", "partially_refunded", 2, nil, []string{"pack", "ship", "cancel"}},
		{"partially refunded after ship", "partially_refunded", 4, nil, nil},
		{"cancelled", "cancelled", 1, nil, nil},
		{"refunded", "refunded", 0, nil, nil},
		{"new + pending accept", "new", 0, []string{"accept"}, []string{"pick", "pack", "ship", "cancel"}},
		{"new + pending ship", "new", 0, []string{"ship"}, nil},
		{"new + pending cancel", "new", 0, []string{"cancel"}, nil},
		{"accepted + pending ship", "accepted", 1, []string{"ship"}, nil},
		{"accepted + pending pack", "accepted", 1, []string{"pack"}, []string{"ship", "cancel"}},
		{"accepted + pending cancel", "accepted", 1, []string{"cancel"}, nil},
		{"new + accept then ship", "new", 0, []string{"accept", "ship"}, nil},
	}
	for _, c := range cases {
		got := ChannelOrderAllowedActions(c.status, c.rank, c.pending)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// AC 3: the buttons and Enqueue agree on every cell.
func TestChannelOrderActions_EnqueueAgreesWithAllowedActionsForEveryCell(t *testing.T) {
	d, orders := openChannelOrders(t)
	repo := NewChannelOrderActionsRepo(d)
	all := []string{"accept", "pick", "pack", "ship", "cancel"}
	states := []struct {
		status string
		rank   int
	}{{"new", 0}, {"accepted", 1}, {"picked", 2}, {"packed", 3}, {"shipped", 4}, {"partially_refunded", 2}, {"cancelled", 1}, {"refunded", 0}}
	pendings := [][]string{nil, {"accept"}, {"ship"}, {"cancel"}}
	n := 0
	for _, st := range states {
		for _, pend := range pendings {
			for _, act := range all {
				n++
				id := caSeedOrder(t, d, orders, fmt.Sprintf("m%d", n), st.status, st.rank)
				for _, p := range pend {
					// Queue the pending ones straight into the table so the
					// order's state is what the cell says, whatever the machine allows.
					mustExecCO(t, d, `INSERT INTO channel_order_actions (action_id, order_id, connection_id, external_order_id, action, base_revision, created_at)
VALUES (?,?,?,?,?,1,'2026-10-22T10:12:00Z')`, fmt.Sprintf("pre-%d-%s", n, p), id, "conn-1", "x", p)
				}
				allowed := ChannelOrderAllowedActions(st.status, st.rank, pend)
				want := false
				for _, a := range allowed {
					if a == act {
						want = true
					}
				}
				_, dup, err := caEnqueue(repo, id, act)
				// A double tap on an already-pending action is a duplicate, not a new row.
				isPending := false
				for _, p := range pend {
					if p == act {
						isPending = true
					}
				}
				switch {
				case isPending:
					if err != nil || !dup {
						t.Errorf("%s %v %s: want duplicate, got dup=%v err=%v", st.status, pend, act, dup, err)
					}
				case want && err != nil:
					t.Errorf("%s %v %s: allowed but refused: %v", st.status, pend, act, err)
				case !want && !errors.Is(err, ErrChannelActionNotAllowed):
					t.Errorf("%s %v %s: not allowed but got err=%v", st.status, pend, act, err)
				}
			}
		}
	}
}

func TestChannelOrderActions_EnqueueWritesRowAndAuditAndNothingElse(t *testing.T) {
	d, orders := openChannelOrders(t)
	repo := NewChannelOrderActionsRepo(d)
	id := caSeedOrder(t, d, orders, "e1", "new", 0)
	stockBefore := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`)
	moves := scalarCO(t, d, `SELECT COUNT(*) FROM stock_movements`)
	mustExecCO(t, d, `UPDATE channel_orders SET revision=4 WHERE id=?`, id)

	actionID, dup, err := repo.Enqueue(context.Background(), ChannelOrderActionInput{OrderID: id, Action: "accept", StaffID: "", Now: time.Date(2026, 10, 23, 9, 12, 44, 0, time.UTC)})
	if err != nil || dup || actionID == "" {
		t.Fatalf("enqueue = %q %v %v", actionID, dup, err)
	}
	rows, err := repo.ForOrder(context.Background(), id)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v %v", rows, err)
	}
	r := rows[0]
	if r.ActionID != actionID || r.Action != "accept" || r.BaseRevision != 4 || r.State != "pending" ||
		r.CreatedAt != "2026-10-23T09:12:44Z" || r.ConnectionID != "conn-1" || r.ExternalOrderID != "e1" {
		t.Fatalf("row = %+v", r)
	}
	var status string
	_ = d.QueryRow(`SELECT status FROM channel_orders WHERE id=?`, id).Scan(&status)
	if status != "new" {
		t.Fatalf("order status = %q; a queued action must not change it", status)
	}
	if q := scalarCO(t, d, `SELECT quantity FROM inventory WHERE id='inv1'`); q != stockBefore {
		t.Fatalf("stock %v -> %v", stockBefore, q)
	}
	if m := scalarCO(t, d, `SELECT COUNT(*) FROM stock_movements`); m != moves {
		t.Fatalf("movements %v -> %v", moves, m)
	}
	var data string
	if err := d.QueryRow(`SELECT data_json FROM audit_log WHERE entity_type='channel_order' AND entity_id=? AND action='channel_order_action_queued'`, id).Scan(&data); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if !strings.Contains(data, actionID) || !strings.Contains(data, `"accept"`) || strings.Count(data, ":") != 2 {
		t.Fatalf("audit data = %s (ids only)", data)
	}
}

func TestChannelOrderActions_ShipAndCancelCarryTheirData(t *testing.T) {
	d, orders := openChannelOrders(t)
	repo := NewChannelOrderActionsRepo(d)
	ctx := context.Background()
	id := caSeedOrder(t, d, orders, "s1", "accepted", 1)
	if _, _, err := repo.Enqueue(ctx, ChannelOrderActionInput{OrderID: id, Action: "ship", Now: time.Now(),
		Shipment: &ChannelShipment{Carrier: "Royal Mail", TrackingNumber: "AB1", TrackingURL: "https://t.example/AB1"}}); err != nil {
		t.Fatal(err)
	}
	rows, _ := repo.ForOrder(ctx, id)
	if rows[0].Shipment == nil || *rows[0].Shipment != (ChannelShipment{Carrier: "Royal Mail", TrackingNumber: "AB1", TrackingURL: "https://t.example/AB1"}) || rows[0].Cancel != nil {
		t.Fatalf("ship row = %+v", rows[0])
	}
	id2 := caSeedOrder(t, d, orders, "s2", "accepted", 1)
	if _, _, err := repo.Enqueue(ctx, ChannelOrderActionInput{OrderID: id2, Action: "cancel", Now: time.Now(), Cancel: &ChannelCancel{Reason: "fraud", Restock: false}}); err != nil {
		t.Fatal(err)
	}
	rows, _ = repo.ForOrder(ctx, id2)
	if rows[0].Cancel == nil || *rows[0].Cancel != (ChannelCancel{Reason: "fraud", Restock: false}) || rows[0].Shipment != nil {
		t.Fatalf("cancel row = %+v", rows[0])
	}
	// Data on the wrong action is a programmer error.
	id3 := caSeedOrder(t, d, orders, "s3", "new", 0)
	if _, _, err := repo.Enqueue(ctx, ChannelOrderActionInput{OrderID: id3, Action: "accept", Now: time.Now(), Cancel: &ChannelCancel{Reason: "fraud"}}); !errors.Is(err, ErrChannelActionNotAllowed) {
		t.Fatalf("accept with cancel data = %v", err)
	}
	if _, _, err := repo.Enqueue(ctx, ChannelOrderActionInput{OrderID: id2, Action: "ship", Now: time.Now()}); !errors.Is(err, ErrChannelActionNotAllowed) {
		t.Fatalf("ship without shipment = %v", err)
	}
}

func TestChannelOrderActions_DoubleTapIsOneRow(t *testing.T) {
	d, orders := openChannelOrders(t)
	repo := NewChannelOrderActionsRepo(d)
	id := caSeedOrder(t, d, orders, "dt", "new", 0)
	a1, dup1, err := caEnqueue(repo, id, "accept")
	if err != nil || dup1 {
		t.Fatal(err, dup1)
	}
	a2, dup2, err := caEnqueue(repo, id, "accept")
	if err != nil || !dup2 || a2 != a1 {
		t.Fatalf("second = %q %v %v", a2, dup2, err)
	}
	if n := scalarCO(t, d, `SELECT COUNT(*) FROM channel_order_actions`); n != 1 {
		t.Fatalf("rows = %v", n)
	}
	if n := scalarCO(t, d, `SELECT COUNT(*) FROM audit_log WHERE action='channel_order_action_queued'`); n != 1 {
		t.Fatalf("audit rows = %v", n)
	}
}

func TestChannelOrderActions_EnqueueErrors(t *testing.T) {
	d, orders := openChannelOrders(t)
	repo := NewChannelOrderActionsRepo(d)
	ctx := context.Background()
	if _, _, err := repo.Enqueue(ctx, ChannelOrderActionInput{OrderID: "nope", Action: "accept", Now: time.Now()}); !errors.Is(err, ErrChannelOrderNotFound) {
		t.Fatalf("unknown order = %v", err)
	}
	id := caSeedOrder(t, d, orders, "bl", "new", 0)
	if _, _, err := caEnqueue(repo, id, "accept"); err != nil {
		t.Fatal(err)
	}
	if n, err := repo.BlockPending(ctx, "main_till_required"); err != nil || n != 1 {
		t.Fatalf("block = %d %v", n, err)
	}
	if _, _, err := caEnqueue(repo, id, "cancel"); !errors.Is(err, ErrChannelActionsBlocked) {
		t.Fatalf("enqueue while blocked = %v", err)
	}
	if n, code, err := repo.Blocked(ctx); err != nil || n != 1 || code != "main_till_required" {
		t.Fatalf("Blocked = %d %q %v", n, code, err)
	}
	if has, _ := repo.HasSendable(ctx); has {
		t.Fatal("a blocked row is not sendable")
	}
	cnt, err := repo.DismissBlocked(ctx, "")
	if err != nil || cnt != 1 {
		t.Fatalf("dismiss blocked = %d %v", cnt, err)
	}
	if n := scalarCO(t, d, `SELECT COUNT(*) FROM audit_log WHERE action='channel_order_actions_blocked_dismissed'`); n != 1 {
		t.Fatalf("audit = %v", n)
	}
	if _, _, err := caEnqueue(repo, id, "accept"); err != nil {
		t.Fatalf("after dismiss: %v", err)
	}
}

func TestChannelOrderActions_EnqueueRollsBackWhenAuditFails(t *testing.T) {
	d, orders := openChannelOrders(t)
	repo := NewChannelOrderActionsRepo(d)
	id := caSeedOrder(t, d, orders, "rb", "new", 0)
	mustExecCO(t, d, `CREATE TRIGGER fail_audit BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'audit down'); END`)
	if _, _, err := caEnqueue(repo, id, "accept"); err == nil {
		t.Fatal("want an error")
	}
	if n := scalarCO(t, d, `SELECT COUNT(*) FROM channel_order_actions`); n != 0 {
		t.Fatalf("a row survived the failed audit: %v", n)
	}
}

func TestChannelOrderActions_EnqueueDeletesSupersededOutcomes(t *testing.T) {
	d, orders := openChannelOrders(t)
	repo := NewChannelOrderActionsRepo(d)
	ctx := context.Background()
	id := caSeedOrder(t, d, orders, "so", "accepted", 1)
	a, _, _ := caEnqueue(repo, id, "pack")
	if err := repo.AnswerRefused(ctx, a, "invalid_transition"); err != nil {
		t.Fatal(err)
	}
	if rows, _ := repo.ForOrder(ctx, id); len(rows) != 1 || rows[0].State != "refused" || rows[0].ResultCode != "invalid_transition" {
		t.Fatalf("rows = %+v", rows)
	}
	if _, _, err := caEnqueue(repo, id, "ship"); err != nil {
		t.Fatal(err)
	}
	rows, _ := repo.ForOrder(ctx, id)
	if len(rows) != 1 || rows[0].Action != "ship" {
		t.Fatalf("the refused row should be superseded: %+v", rows)
	}
}

func TestChannelOrderActions_SendableBatchOnePerOrderOldestFirst(t *testing.T) {
	d, orders := openChannelOrders(t)
	repo := NewChannelOrderActionsRepo(d)
	ctx := context.Background()
	o1 := caSeedOrder(t, d, orders, "b1", "new", 0)
	o2 := caSeedOrder(t, d, orders, "b2", "new", 0)
	a1, _, _ := caEnqueue(repo, o1, "accept")
	a2, _, _ := caEnqueue(repo, o2, "accept")
	_, _, _ = caEnqueue(repo, o1, "ship") // behind a1: not in the batch
	batch, err := repo.SendableBatch(ctx, 50)
	if err != nil || len(batch) != 2 || batch[0].ActionID != a1 || batch[1].ActionID != a2 {
		t.Fatalf("batch = %+v %v", batch, err)
	}
	if one, _ := repo.SendableBatch(ctx, 1); len(one) != 1 || one[0].ActionID != a1 {
		t.Fatalf("limit not honoured: %+v", one)
	}
	if has, _ := repo.HasSendable(ctx); !has {
		t.Fatal("HasSendable")
	}
}

func TestChannelOrderActions_AnswerAppliedRebasesDeletesAndNotes(t *testing.T) {
	d, orders := openChannelOrders(t)
	repo := NewChannelOrderActionsRepo(d)
	ctx := context.Background()
	o1 := caSeedOrder(t, d, orders, "r1", "new", 0)
	o2 := caSeedOrder(t, d, orders, "r2", "new", 0)
	a1, _, _ := caEnqueue(repo, o1, "accept")
	cancel1, _, _ := caEnqueue(repo, o1, "cancel")
	_, _, _ = caEnqueue(repo, o2, "accept")
	if err := repo.AnswerApplied(ctx, a1, 7, "not_applicable"); err != nil {
		t.Fatal(err)
	}
	rows, _ := repo.ForOrder(ctx, o1)
	if len(rows) != 1 || rows[0].ActionID != cancel1 || rows[0].BaseRevision != 7 {
		t.Fatalf("o1 rows = %+v", rows)
	}
	rows2, _ := repo.ForOrder(ctx, o2)
	if len(rows2) != 1 || rows2[0].BaseRevision != 1 {
		t.Fatalf("another order must not be rebased: %+v", rows2)
	}
	// A lower revision never lowers a base.
	if err := repo.AnswerApplied(ctx, "gone", 3, ""); err != nil {
		t.Fatalf("a missing row is not an error: %v", err)
	}
	// ship + writeback skipped -> noted; queued -> deleted.
	o3 := caSeedOrder(t, d, orders, "r3", "accepted", 1)
	s3, _, _ := caEnqueue(repo, o3, "ship")
	if err := repo.AnswerApplied(ctx, s3, 2, "skipped"); err != nil {
		t.Fatal(err)
	}
	rows3, _ := repo.ForOrder(ctx, o3)
	if len(rows3) != 1 || rows3[0].State != "noted" || rows3[0].Writeback != "skipped" {
		t.Fatalf("o3 = %+v", rows3)
	}
	o4 := caSeedOrder(t, d, orders, "r4", "accepted", 1)
	s4, _, _ := caEnqueue(repo, o4, "ship")
	if err := repo.AnswerApplied(ctx, s4, 2, "queued"); err != nil {
		t.Fatal(err)
	}
	if rows4, _ := repo.ForOrder(ctx, o4); len(rows4) != 0 {
		t.Fatalf("o4 = %+v", rows4)
	}
	// A pick with writeback skipped is not noted (only ship/cancel take a write-back).
	o5 := caSeedOrder(t, d, orders, "r5", "accepted", 1)
	p5, _, _ := caEnqueue(repo, o5, "pick")
	if err := repo.AnswerApplied(ctx, p5, 2, "skipped"); err != nil {
		t.Fatal(err)
	}
	if rows5, _ := repo.ForOrder(ctx, o5); len(rows5) != 0 {
		t.Fatalf("o5 = %+v", rows5)
	}
}

func TestChannelOrderActions_DismissRefusesPendingAndAudits(t *testing.T) {
	d, orders := openChannelOrders(t)
	repo := NewChannelOrderActionsRepo(d)
	ctx := context.Background()
	id := caSeedOrder(t, d, orders, "dm", "accepted", 1)
	a, _, _ := caEnqueue(repo, id, "pack")
	if ok, err := repo.Dismiss(ctx, id, a, ""); err != nil || ok {
		t.Fatalf("dismissing a pending row = %v %v", ok, err)
	}
	if err := repo.AnswerRefused(ctx, a, "stale"); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.Dismiss(ctx, "other-order", a, ""); err != nil || ok {
		t.Fatalf("wrong order = %v %v", ok, err)
	}
	if ok, err := repo.Dismiss(ctx, id, a, ""); err != nil || !ok {
		t.Fatalf("dismiss = %v %v", ok, err)
	}
	if n := scalarCO(t, d, `SELECT COUNT(*) FROM audit_log WHERE action='channel_order_action_dismissed'`); n != 1 {
		t.Fatalf("audit = %v", n)
	}
}

func TestChannelOrders_ListCarriesFulfilmentRankAndPendingAction(t *testing.T) {
	d, orders := openChannelOrders(t)
	repo := NewChannelOrderActionsRepo(d)
	ctx := context.Background()
	id := caSeedOrder(t, d, orders, "ls", "accepted", 1)
	if _, _, err := caEnqueue(repo, id, "pack"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := caEnqueue(repo, id, "ship"); err != nil {
		t.Fatal(err)
	}
	list, err := orders.List(ctx, 10)
	if err != nil || len(list) == 0 {
		t.Fatal(err)
	}
	if list[0].FulfilmentRank != 1 || list[0].PendingAction != "ship" || list[0].RefusedAction {
		t.Fatalf("list[0] = %+v", list[0])
	}
	got, _, _ := orders.Get(ctx, id)
	if got.PendingAction != "ship" {
		t.Fatalf("Get = %+v", got)
	}
	// An undismissed refusal shows on the list.
	rows, _ := repo.ForOrder(ctx, id)
	_ = repo.AnswerRefused(ctx, rows[1].ActionID, "already_shipped")
	got, _, _ = orders.Get(ctx, id)
	if !got.RefusedAction {
		t.Fatalf("RefusedAction not set: %+v", got)
	}
}
