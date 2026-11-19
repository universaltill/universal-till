package channelactions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// The uploader of the main till's channel order actions (ut-docs#4768 slice D,
// reference/contracts/channel-order-action.md "Till side").

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(testsupport.MigratedDBFile(t, "channelactions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	testsupport.SeedTaxCode(t, d.DB, "tc1", "Standard", 2000)
	testsupport.SeedItem(t, d.DB, testsupport.ItemSeed{ID: "itm1", SKU: "LS-M-BLU", Name: "Shirt", BasePrice: 3500, TaxCodeID: "tc1", IsActive: true})
	if _, err := d.DB.Exec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv1','itm1','loc_main',100)`); err != nil {
		t.Fatal(err)
	}
	return d.DB
}

const connID = "0b3b9c34-5c1e-4a3e-8f55-3f7f3a9a7c02"

func seedOrder(t *testing.T, d *sql.DB, ext string, status string, rank int) string {
	t.Helper()
	res, err := data.NewChannelOrdersRepo(d).Apply(context.Background(), data.ChannelOrderInput{
		DirectiveID: "dir-" + ext, ConnectionID: connID, PluginKey: "com.example.channel", ExternalOrderID: ext, Revision: 1,
		OrderNumber: "#" + ext, PlacedAt: "2026-10-22T10:12:00Z", Currency: "GBP", Status: "new", Total: money.FromMinor(7000),
		LocationID: "loc_main", Lines: []data.ChannelOrderLineInput{{LineID: "l1", CloudItemID: "itm1", SKU: "LS-M-BLU", Name: "Shirt", Qty: 1, UnitPrice: money.FromMinor(7000)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE channel_orders SET status=?, fulfilment_rank=? WHERE id=?`, status, rank, res.OrderID); err != nil {
		t.Fatal(err)
	}
	return res.OrderID
}

func enqueue(t *testing.T, d *sql.DB, order, action string) string {
	t.Helper()
	in := data.ChannelOrderActionInput{OrderID: order, Action: action, StaffID: "", Now: time.Now()}
	switch action {
	case "ship":
		in.Shipment = &data.ChannelShipment{Carrier: "Royal Mail", TrackingNumber: "AB1"}
	case "cancel":
		in.Cancel = &data.ChannelCancel{Reason: "out_of_stock", Restock: true}
	}
	id, _, err := data.NewChannelOrderActionsRepo(d).Enqueue(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func state(t *testing.T, d *sql.DB, actionID string) (string, string) {
	t.Helper()
	var st, code string
	err := d.QueryRow(`SELECT state, result_code FROM channel_order_actions WHERE action_id=?`, actionID).Scan(&st, &code)
	if errors.Is(err, sql.ErrNoRows) {
		return "gone", ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return st, code
}

func fixedNow() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }

// stub answers every action "applied" with revision 2 unless told otherwise.
type stub struct {
	mu    sync.Mutex
	calls [][]Action
	body  [][]byte
	reply func(call int, as []Action) ([]Result, error)
}

func (s *stub) post(_ context.Context, as []Action) ([]Result, error) {
	s.mu.Lock()
	s.calls = append(s.calls, as)
	b, _ := json.Marshal(as)
	s.body = append(s.body, b)
	n := len(s.calls) - 1
	reply := s.reply
	s.mu.Unlock()
	if reply != nil {
		return reply(n, as)
	}
	return applied(as, 2), nil
}

func applied(as []Action, rev int64) []Result {
	out := make([]Result, len(as))
	for i, a := range as {
		out[i] = Result{ActionID: a.ActionID, Status: "applied", Revision: rev, Writeback: "not_applicable", Current: map[string]any{"revision": float64(rev)}}
	}
	return out
}

func noApply(context.Context, string, map[string]any) error { return nil }

func TestRun_FiftyPerCallOnePerOrderInSeqOrder(t *testing.T) {
	d := openDB(t)
	var ids []string
	for i := 0; i < 60; i++ {
		o := seedOrder(t, d, fmt.Sprintf("o%02d", i), "new", 0)
		ids = append(ids, enqueue(t, d, o, "accept"))
	}
	s := &stub{}
	sum, err := Run(context.Background(), d, s.post, noApply, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 2 || len(s.calls[0]) != 50 || len(s.calls[1]) != 10 || sum.Sent != 60 || sum.Applied != 60 {
		t.Fatalf("calls = %d (%d,%d) sum=%+v", len(s.calls), len(s.calls[0]), len(s.calls[1]), sum)
	}
	for i, a := range append(append([]Action{}, s.calls[0]...), s.calls[1]...) {
		if a.ActionID != ids[i] {
			t.Fatalf("action %d out of order", i)
		}
	}
	for _, id := range ids {
		if st, _ := state(t, d, id); st != "gone" {
			t.Fatalf("an applied row must be deleted, got %s", st)
		}
	}
}

// Contract test 10: offline accept+cancel, the cancel rebased to the accept's answer.
func TestRun_SecondActionOfAnOrderWaitsAndIsRebased(t *testing.T) {
	d := openDB(t)
	o := seedOrder(t, d, "o1", "new", 0)
	accept := enqueue(t, d, o, "accept")
	cancel := enqueue(t, d, o, "cancel")
	s := &stub{reply: func(n int, as []Action) ([]Result, error) { return applied(as, 7), nil }}
	if _, err := Run(context.Background(), d, s.post, noApply, fixedNow); err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 2 || len(s.calls[0]) != 1 || s.calls[0][0].ActionID != accept || s.calls[0][0].BaseRevision != 1 {
		t.Fatalf("first call = %+v", s.calls)
	}
	c := s.calls[1][0]
	if len(s.calls[1]) != 1 || c.ActionID != cancel || c.BaseRevision != 7 || c.Cancel == nil || !c.Cancel.Restock || c.Cancel.Reason != "out_of_stock" {
		t.Fatalf("second call = %+v", s.calls[1])
	}
}

func TestRun_RefusedIsKeptWithItsCodeAndNeverResent(t *testing.T) {
	d := openDB(t)
	o := seedOrder(t, d, "o1", "accepted", 1)
	id := enqueue(t, d, o, "ship")
	s := &stub{reply: func(_ int, as []Action) ([]Result, error) {
		return []Result{{ActionID: as[0].ActionID, Status: "refused", Code: "already_shipped", Revision: 5, Current: map[string]any{"revision": float64(5)}}}, nil
	}}
	var got []map[string]any
	apply := func(_ context.Context, _ string, cur map[string]any) error { got = append(got, cur); return nil }
	sum, err := Run(context.Background(), d, s.post, apply, fixedNow)
	if err != nil || sum.Refused != 1 {
		t.Fatalf("sum=%+v err=%v", sum, err)
	}
	if st, code := state(t, d, id); st != "refused" || code != "already_shipped" {
		t.Fatalf("row = %s %s", st, code)
	}
	if len(got) != 1 {
		t.Fatalf("current of a refusal must be applied: %v", got)
	}
	if _, err := Run(context.Background(), d, s.post, apply, fixedNow); err != nil || len(s.calls) != 1 {
		t.Fatalf("a refused row was sent again: calls=%d err=%v", len(s.calls), err)
	}
}

func TestRun_RefusedCodeIsSanitised(t *testing.T) {
	d := openDB(t)
	o := seedOrder(t, d, "o1", "accepted", 1)
	id := enqueue(t, d, o, "ship")
	s := &stub{reply: func(_ int, as []Action) ([]Result, error) {
		return []Result{{ActionID: as[0].ActionID, Status: "refused", Code: "<script>alert(1)</script>"}}, nil
	}}
	if _, err := Run(context.Background(), d, s.post, noApply, fixedNow); err != nil {
		t.Fatal(err)
	}
	if st, code := state(t, d, id); st != "refused" || code != "unknown" {
		t.Fatalf("row = %s %q", st, code)
	}
}

func TestRun_AnswerMismatchKeepsEveryRowPending(t *testing.T) {
	d := openDB(t)
	o1, o2 := seedOrder(t, d, "o1", "new", 0), seedOrder(t, d, "o2", "new", 0)
	a1, a2 := enqueue(t, d, o1, "accept"), enqueue(t, d, o2, "accept")
	for name, reply := range map[string]func(as []Action) []Result{
		"short":     func(as []Action) []Result { return applied(as[:1], 2) },
		"wrong id":  func(as []Action) []Result { r := applied(as, 2); r[1].ActionID = "x"; return r },
		"swapped":   func(as []Action) []Result { r := applied(as, 2); r[0], r[1] = r[1], r[0]; return r },
		"bad state": func(as []Action) []Result { r := applied(as, 2); r[1].Status = "sending"; return r },
	} {
		s := &stub{reply: func(_ int, as []Action) ([]Result, error) { return reply(as), nil }}
		_, err := Run(context.Background(), d, s.post, noApply, fixedNow)
		if !errors.Is(err, ErrAnswerMismatch) {
			t.Fatalf("%s: err = %v", name, err)
		}
		for _, id := range []string{a1, a2} {
			if st, _ := state(t, d, id); st != "pending" {
				t.Fatalf("%s: %s is %s", name, id, st)
			}
		}
	}
}

func TestRun_MainTillRequiredBlocksEveryPendingRowAndStopsSending(t *testing.T) {
	d := openDB(t)
	o1, o2 := seedOrder(t, d, "o1", "new", 0), seedOrder(t, d, "o2", "new", 0)
	a1, a2 := enqueue(t, d, o1, "accept"), enqueue(t, d, o2, "accept")
	a3 := enqueue(t, d, o1, "cancel")
	s := &stub{reply: func(int, []Action) ([]Result, error) {
		return nil, &StopError{Status: 403, Code: "main_till_required"}
	}}
	sum, err := Run(context.Background(), d, s.post, noApply, fixedNow)
	if err != nil || sum.Stopped != "main_till_required" || sum.StopStatus != 403 || sum.Blocked != 3 {
		t.Fatalf("sum=%+v err=%v", sum, err)
	}
	for _, id := range []string{a1, a2, a3} {
		if st, code := state(t, d, id); st != "blocked" || code != "main_till_required" {
			t.Fatalf("%s = %s %s", id, st, code)
		}
	}
	if _, err := Run(context.Background(), d, s.post, noApply, fixedNow); err != nil || len(s.calls) != 1 {
		t.Fatalf("a blocked row was sent: calls=%d", len(s.calls))
	}
}

func TestRun_OtherErrorsKeepRowsPending(t *testing.T) {
	for name, perr := range map[string]error{
		"transport": errors.New("connection reset"),
		"503":       &StatusError{Status: 503, Code: "actions_unavailable"},
		"401":       &StatusError{Status: 401, Code: "unauthorized"},
		"402":       &StatusError{Status: 402, Code: "subscription_read_only"},
		"409":       &StatusError{Status: 409, Code: "channel_not_enabled"},
		"400":       &BadRequestError{Code: "invalid_request"},
	} {
		d := openDB(t)
		o := seedOrder(t, d, "o1", "new", 0)
		id := enqueue(t, d, o, "accept")
		s := &stub{reply: func(int, []Action) ([]Result, error) { return nil, perr }}
		sum, err := Run(context.Background(), d, s.post, noApply, fixedNow)
		if !errors.Is(err, perr) || sum.Stopped != "" {
			t.Fatalf("%s: sum=%+v err=%v", name, sum, err)
		}
		if st, _ := state(t, d, id); st != "pending" {
			t.Fatalf("%s: row is %s", name, st)
		}
	}
}

// A lost answer: the retry re-sends the same id and body; the cloud answers
// the same verdict; the order's revision is applied once.
func TestRun_LostAnswerIsRetriedWithTheSameIdAndBody(t *testing.T) {
	d := openDB(t)
	o := seedOrder(t, d, "o1", "accepted", 1)
	id := enqueue(t, d, o, "ship")
	cloudApplied := 0
	s := &stub{reply: func(n int, as []Action) ([]Result, error) {
		if n == 0 {
			cloudApplied++ // the server applied it, the answer was lost
			return nil, errors.New("read: connection reset by peer")
		}
		return applied(as, 3), nil // the same verdict the second time
	}}
	if _, err := Run(context.Background(), d, s.post, noApply, fixedNow); err == nil {
		t.Fatal("want the transport error")
	}
	if st, _ := state(t, d, id); st != "pending" {
		t.Fatalf("row = %s", st)
	}
	var applies []int64
	apply := func(_ context.Context, _ string, cur map[string]any) error {
		applies = append(applies, int64(cur["revision"].(float64)))
		return nil
	}
	if _, err := Run(context.Background(), d, s.post, apply, fixedNow); err != nil {
		t.Fatal(err)
	}
	if len(s.body) != 2 || string(s.body[0]) != string(s.body[1]) || cloudApplied != 1 {
		t.Fatalf("bodies differ:\n%s\n%s", s.body[0], s.body[1])
	}
	if len(applies) != 1 || applies[0] != 3 {
		t.Fatalf("applies = %v", applies)
	}
	if st, _ := state(t, d, id); st != "gone" {
		t.Fatalf("row = %s", st)
	}
}

func TestRun_ApplyFailureStillRecordsEveryVerdict(t *testing.T) {
	d := openDB(t)
	var ids []string
	for i := 0; i < 3; i++ {
		ids = append(ids, enqueue(t, d, seedOrder(t, d, fmt.Sprintf("o%d", i), "new", 0), "accept"))
	}
	s := &stub{}
	apply := func(context.Context, string, map[string]any) error {
		return errors.New("no stock location configured")
	}
	sum, err := Run(context.Background(), d, s.post, apply, fixedNow)
	if err != nil || sum.Applied != 3 {
		t.Fatalf("sum=%+v err=%v", sum, err)
	}
	for i, id := range ids {
		if st, _ := state(t, d, id); st != "gone" {
			t.Fatalf("row %d = %s, want gone", i, st)
		}
	}
	if _, err := Run(context.Background(), d, s.post, apply, fixedNow); err != nil || len(s.calls) != 1 {
		t.Fatalf("rows were re-sent: calls=%d err=%v", len(s.calls), err)
	}
}

func TestRun_RefusedCurrentIsStillRecorded(t *testing.T) {
	d := openDB(t)
	id := enqueue(t, d, seedOrder(t, d, "o1", "new", 0), "accept")
	s := &stub{}
	apply := func(context.Context, string, map[string]any) error {
		return fmt.Errorf("%w: unknown field", ErrCurrentRefused)
	}
	sum, err := Run(context.Background(), d, s.post, apply, fixedNow)
	if err != nil || sum.Applied != 1 {
		t.Fatalf("sum=%+v err=%v", sum, err)
	}
	if st, _ := state(t, d, id); st != "gone" {
		t.Fatalf("row = %s", st)
	}
}

func TestRun_WritebackSkippedIsKeptAsANoteOnShipAndCancelOnly(t *testing.T) {
	d := openDB(t)
	ship := enqueue(t, d, seedOrder(t, d, "o1", "accepted", 1), "ship")
	pick := enqueue(t, d, seedOrder(t, d, "o2", "accepted", 1), "pick")
	queued := enqueue(t, d, seedOrder(t, d, "o3", "accepted", 1), "ship")
	s := &stub{reply: func(_ int, as []Action) ([]Result, error) {
		r := applied(as, 2)
		for i := range r {
			r[i].Writeback = "skipped"
			if as[i].ActionID == queued {
				r[i].Writeback = "queued"
			}
		}
		return r, nil
	}}
	sum, err := Run(context.Background(), d, s.post, noApply, fixedNow)
	if err != nil || sum.Noted != 1 {
		t.Fatalf("sum=%+v err=%v", sum, err)
	}
	if st, _ := state(t, d, ship); st != "noted" {
		t.Fatalf("ship = %s", st)
	}
	if st, _ := state(t, d, pick); st != "gone" {
		t.Fatalf("pick = %s", st)
	}
	if st, _ := state(t, d, queued); st != "gone" {
		t.Fatalf("queued ship = %s", st)
	}
}

func TestRun_OneRunAtATime(t *testing.T) {
	d := openDB(t)
	enqueue(t, d, seedOrder(t, d, "o1", "new", 0), "accept")
	started, release := make(chan struct{}), make(chan struct{})
	s := &stub{reply: func(_ int, as []Action) ([]Result, error) {
		close(started)
		<-release
		return applied(as, 2), nil
	}}
	done := make(chan Summary)
	go func() {
		sum, _ := Run(context.Background(), d, s.post, noApply, fixedNow)
		done <- sum
	}()
	<-started
	sum, err := Run(context.Background(), d, s.post, noApply, fixedNow)
	if err != nil || !sum.Busy {
		t.Fatalf("second run = %+v %v", sum, err)
	}
	close(release)
	if first := <-done; first.Busy || first.Applied != 1 {
		t.Fatalf("first run = %+v", first)
	}
}

func TestRun_NothingPendingPostsNothing(t *testing.T) {
	d := openDB(t)
	s := &stub{}
	if sum, err := Run(context.Background(), d, s.post, noApply, fixedNow); err != nil || sum.Sent != 0 || len(s.calls) != 0 {
		t.Fatalf("sum=%+v err=%v calls=%d", sum, err, len(s.calls))
	}
}

func TestRun_StaleFlagIsDecidedAtSendTime(t *testing.T) {
	d := openDB(t)
	id := enqueue(t, d, seedOrder(t, d, "o1", "new", 0), "accept")
	if _, err := d.Exec(`UPDATE channel_order_actions SET created_at='2026-09-29T11:59:59Z' WHERE action_id=?`, id); err != nil {
		t.Fatal(err)
	}
	s := &stub{reply: func(_ int, as []Action) ([]Result, error) {
		return []Result{{ActionID: as[0].ActionID, Status: "refused", Code: "stale"}}, nil
	}}
	if _, err := Run(context.Background(), d, s.post, noApply, fixedNow); err != nil {
		t.Fatal(err)
	}
	if !s.calls[0][0].Stale {
		t.Fatal("an action older than 30 days must be sent with stale: true")
	}
}

func TestToWire_StaleBoundary(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) data.ChannelOrderAction {
		return data.ChannelOrderAction{ActionID: "a", Action: "accept", BaseRevision: 1, CreatedAt: now.Add(-d).Format(time.RFC3339)}
	}
	if ToWire(at(StaleAfter+time.Second), now).Stale != true {
		t.Error("30 days and 1 s old must be stale")
	}
	if ToWire(at(StaleAfter), now).Stale {
		t.Error("exactly 30 days old is not stale")
	}
	if ToWire(data.ChannelOrderAction{CreatedAt: "garbage"}, now).Stale {
		t.Error("an unparseable created_at is not stale")
	}
}
