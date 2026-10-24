package stockupload_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/stockupload"
	"github.com/universaltill/universal-till/internal/testsupport"
)

const (
	cloudA = "11111111-1111-4111-8111-111111111111"
	cloudB = "22222222-2222-4222-8222-222222222222"
)

type harness struct {
	t    *testing.T
	d    *db.DB
	loc  string
	sent []stockupload.Request
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	d, err := db.Open(testsupport.MigratedDBFile(t, "stockupload.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	h := &harness{t: t, d: d, loc: "loc_main"}
	h.exec(`DELETE FROM stock_upload_outbox`)
	return h
}

func (h *harness) exec(q string, args ...any) {
	h.t.Helper()
	if _, err := h.d.Exec(q, args...); err != nil {
		h.t.Fatalf("%s: %v", q, err)
	}
}

func (h *harness) item(id, cloud string, qty float64) {
	h.t.Helper()
	h.exec(`INSERT INTO items (id, name, base_price) VALUES (?, ?, 100)`, id, id)
	if cloud != "" {
		h.exec(`INSERT INTO cloud_links (entity, cloud_id, local_id, version, updated_at) VALUES ('item', ?, ?, 1, 'x')`, cloud, id)
	}
	h.exec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES (?, ?, ?, ?)`, id+"-inv", id, h.loc, qty)
}

func (h *harness) marks() int {
	h.t.Helper()
	var n int
	if err := h.d.QueryRow(`SELECT COUNT(*) FROM stock_upload_outbox`).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

// poster answers every row with status, recording the requests.
func (h *harness) poster(status string) stockupload.Poster {
	return func(_ context.Context, req stockupload.Request) ([]stockupload.Answer, error) {
		h.sent = append(h.sent, req)
		out := make([]stockupload.Answer, len(req.Rows))
		for i, r := range req.Rows {
			out[i] = stockupload.Answer{Seq: r.Seq, Status: status}
		}
		return out, nil
	}
}

func (h *harness) run(p stockupload.Poster) (stockupload.Result, error) {
	return stockupload.Run(context.Background(), h.d.DB, p, "0.34.18")
}

func TestRun_UploadsOwedRowsAndClearsAcceptedMarks(t *testing.T) {
	h := newHarness(t)
	h.item("a", cloudA, 12.5)
	h.item("b", cloudB, 3)
	res, err := h.run(h.poster("applied"))
	if err != nil || res.Sent != 2 || res.Applied != 2 {
		t.Fatalf("res = %+v, err %v", res, err)
	}
	if len(h.sent) != 1 || len(h.sent[0].Rows) != 2 || h.sent[0].TillVersion != "0.34.18" || h.sent[0].StreamID == "" {
		t.Fatalf("sent = %+v", h.sent)
	}
	row := h.sent[0].Rows[0]
	if row.ItemID != cloudA || row.OnHandMilli != 12500 || row.LocationID != "loc_main" || row.Seq == 0 || row.VariantID != "" {
		t.Fatalf("row = %+v", row)
	}
	if h.marks() != 0 {
		t.Fatalf("%d marks left after an accepted upload", h.marks())
	}
	// Nothing owed: no request at all.
	h.sent = nil
	if res, err := h.run(h.poster("applied")); err != nil || res.Sent != 0 || len(h.sent) != 0 {
		t.Fatalf("idle run = %+v, %v, %d requests", res, err, len(h.sent))
	}
}

func TestRun_StaleAndRejectedAnswersAlsoClearTheMark(t *testing.T) {
	for _, status := range []string{"stale", "rejected"} {
		h := newHarness(t)
		h.item("a", cloudA, 1)
		if _, err := h.run(h.poster(status)); err != nil {
			t.Fatal(err)
		}
		if h.marks() != 0 {
			t.Errorf("%s: mark left", status)
		}
	}
}

func TestRun_CloudUnreachableKeepsEveryMarkAndTheNextRunSendsThem(t *testing.T) {
	h := newHarness(t)
	h.item("a", cloudA, 4)
	down := func(context.Context, stockupload.Request) ([]stockupload.Answer, error) {
		return nil, errors.New("dial tcp: connection refused")
	}
	res, err := h.run(down)
	if err == nil || res.Sent != 0 {
		t.Fatalf("res = %+v, err %v", res, err)
	}
	if h.marks() != 1 {
		t.Fatalf("offline run lost the mark: %d", h.marks())
	}
	// A quantity change while offline replaces the mark; the upload follows with the latest.
	h.exec(`UPDATE inventory SET quantity = 2 WHERE item_id = 'a'`)
	res, err = h.run(h.poster("applied"))
	if err != nil || res.Applied != 1 || h.sent[0].Rows[0].OnHandMilli != 2000 || h.marks() != 0 {
		t.Fatalf("follow-up = %+v, %v, sent %+v", res, err, h.sent)
	}
}

func TestRun_StopErrorKeepsMarksAndReportsWhy(t *testing.T) {
	h := newHarness(t)
	h.item("a", cloudA, 1)
	stop := func(context.Context, stockupload.Request) ([]stockupload.Answer, error) {
		return nil, &stockupload.StopError{Status: 403, Code: "not_main_till"}
	}
	res, err := h.run(stop)
	if err != nil || res.Stopped != "not_main_till" || res.StopStatus != 403 || h.marks() != 1 {
		t.Fatalf("res = %+v err %v marks %d", res, err, h.marks())
	}
}

func TestRun_UnlinkedRowsWaitAndDoNotBlockTheRest(t *testing.T) {
	h := newHarness(t)
	h.item("a", "", 1) // no cloud link yet
	h.item("b", cloudB, 2)
	res, err := h.run(h.poster("applied"))
	if err != nil || res.Sent != 1 || len(h.sent[0].Rows) != 1 || h.sent[0].Rows[0].ItemID != cloudB {
		t.Fatalf("res = %+v err %v sent %+v", res, err, h.sent)
	}
	if h.marks() != 1 {
		t.Fatalf("the unlinked row's mark must stay, have %d", h.marks())
	}
}

func TestRun_ChangeBetweenReadAndAnswerStaysOwed(t *testing.T) {
	h := newHarness(t)
	h.item("a", cloudA, 5)
	// One request only (a Poster that stops the run after the first batch):
	// the in-flight sale's mark must survive the first answer.
	calls := 0
	p := func(_ context.Context, req stockupload.Request) ([]stockupload.Answer, error) {
		calls++
		if calls > 1 {
			return nil, errors.New("stop after the first batch")
		}
		h.exec(`UPDATE inventory SET quantity = 4 WHERE item_id = 'a'`) // a sale lands while the request is in flight
		return []stockupload.Answer{{Seq: req.Rows[0].Seq, Status: "applied"}}, nil
	}
	_, _ = h.run(p)
	if h.marks() != 1 {
		t.Fatalf("the sale made during the upload was lost: %d marks", h.marks())
	}
	res, _ := h.run(h.poster("applied"))
	if res.Sent != 1 || h.sent[0].Rows[0].OnHandMilli != 4000 {
		t.Fatalf("follow-up = %+v sent %+v", res, h.sent)
	}
}

func TestRun_BatchesOf500(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 1201; i++ {
		id := string(rune('A'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('0'+(i/676)%10)) + "-" + itoa(i)
		h.exec(`INSERT INTO items (id, name, base_price) VALUES (?, ?, 1)`, id, id)
		h.exec(`INSERT INTO cloud_links (entity, cloud_id, local_id, version, updated_at) VALUES ('item', ?, ?, 1, 'x')`, uuidFor(i), id)
		h.exec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES (?, ?, ?, 1)`, id+"-i", id, h.loc)
	}
	res, err := h.run(h.poster("applied"))
	if err != nil || res.Sent != 1201 || len(h.sent) != 3 || len(h.sent[0].Rows) != stockupload.MaxRows {
		t.Fatalf("res = %+v err %v batches %d", res, err, len(h.sent))
	}
	if h.marks() != 0 {
		t.Fatal("marks left")
	}
}

func TestRun_CloudAheadOfTheTillRotatesTheStreamAndResends(t *testing.T) {
	h := newHarness(t)
	h.item("a", cloudA, 5)
	repo := data.NewStockUploadRepo(h.d.DB)
	oldS, _ := repo.Stream(context.Background())
	old := oldS.ID
	calls := 0
	p := func(_ context.Context, req stockupload.Request) ([]stockupload.Answer, error) {
		calls++
		h.sent = append(h.sent, req)
		if req.StreamID == old { // the cloud holds a higher sequence of the old stream
			return []stockupload.Answer{{Seq: req.Rows[0].Seq, Status: "stale", StoredSeq: req.Rows[0].Seq + 900}}, nil
		}
		return []stockupload.Answer{{Seq: req.Rows[0].Seq, Status: "applied"}}, nil
	}
	res, err := h.run(p)
	if err != nil || !res.Rotated || res.Applied != 1 || calls != 2 || h.marks() != 0 {
		t.Fatalf("res = %+v err %v calls %d marks %d", res, err, calls, h.marks())
	}
	if now, _ := repo.Stream(context.Background()); now.ID == old || h.sent[1].StreamID != now.ID || !h.sent[1].StreamStartedAt.After(h.sent[0].StreamStartedAt) {
		t.Fatalf("stream not rotated: %q -> %q (second request %q)", old, now.ID, h.sent[1].StreamID)
	}
}

func TestRun_AStaleAnswerForTheSameSequenceIsJustADuplicate(t *testing.T) {
	h := newHarness(t)
	h.item("a", cloudA, 5)
	p := func(_ context.Context, req stockupload.Request) ([]stockupload.Answer, error) {
		return []stockupload.Answer{{Seq: req.Rows[0].Seq, Status: "stale", StoredSeq: req.Rows[0].Seq}}, nil
	}
	res, err := h.run(p)
	if err != nil || res.Rotated || res.Stale != 1 || h.marks() != 0 {
		t.Fatalf("res = %+v err %v", res, err)
	}
}

func TestRun_AMismatchedAnswerClearsNothing(t *testing.T) {
	h := newHarness(t)
	h.item("a", cloudA, 5)
	for name, ans := range map[string][]stockupload.Answer{
		"too few":        {},
		"wrong sequence": {{Seq: 99999, Status: "applied"}},
		"unknown status": nil, // answered below with a status the till does not know
	} {
		p := func(_ context.Context, req stockupload.Request) ([]stockupload.Answer, error) {
			if name == "unknown status" {
				return []stockupload.Answer{{Seq: req.Rows[0].Seq, Status: "weird"}}, nil
			}
			return ans, nil
		}
		if _, err := h.run(p); err == nil {
			t.Errorf("%s: no error", name)
		}
		if h.marks() != 1 {
			t.Errorf("%s: mark cleared on a bad answer", name)
		}
	}
}

func TestRun_OnlyOneRunAtATime(t *testing.T) {
	h := newHarness(t)
	h.item("a", cloudA, 5)
	inner := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = h.run(func(_ context.Context, req stockupload.Request) ([]stockupload.Answer, error) {
			close(inner)
			<-release
			return []stockupload.Answer{{Seq: req.Rows[0].Seq, Status: "applied"}}, nil
		})
	}()
	<-inner
	res, err := h.run(h.poster("applied"))
	if err != nil || !res.Busy || len(h.sent) != 0 {
		t.Fatalf("a concurrent run = %+v, %v, %d sent", res, err, len(h.sent))
	}
	close(release)
	<-done
}

func TestRun_AnUnsendableLocationIsDroppedNotRetriedForever(t *testing.T) {
	h := newHarness(t)
	long := "loc-" + repeat("x", 70)
	h.exec(`INSERT INTO stock_locations (id, name, is_active) VALUES (?, 'Long', 1)`, long)
	h.exec(`INSERT INTO items (id, name, base_price) VALUES ('a', 'a', 1)`)
	h.exec(`INSERT INTO cloud_links (entity, cloud_id, local_id, version, updated_at) VALUES ('item', ?, 'a', 1, 'x')`, cloudA)
	h.exec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('a-inv', 'a', ?, 5)`, long)
	h.exec(`INSERT INTO items (id, name, base_price) VALUES ('b', 'b', 1)`)
	h.exec(`INSERT INTO cloud_links (entity, cloud_id, local_id, version, updated_at) VALUES ('item', ?, 'b', 1, 'x')`, cloudB)
	h.exec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('b-inv', 'b', 'loc_main', 1)`)
	res, err := h.run(h.poster("applied"))
	if err != nil || res.Dropped != 1 || res.Applied != 1 || h.marks() != 0 {
		t.Fatalf("res = %+v err %v marks %d", res, err, h.marks())
	}
	for _, r := range h.sent[0].Rows {
		if len(r.LocationID) > 64 {
			t.Fatal("an over-long location id was sent")
		}
	}
}

var _ = sql.ErrNoRows

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for ; i > 0; i /= 10 {
		s = string(rune('0'+i%10)) + s
	}
	return s
}

// uuidFor makes a distinct, valid UUID per index.
func uuidFor(i int) string {
	const hex = "0123456789abcdef"
	b := []byte("00000000-0000-4000-8000-000000000000")
	for p, n := len(b)-1, i; n > 0 && p >= 0; p-- {
		if b[p] == '-' {
			continue
		}
		b[p] = hex[n%16]
		n /= 16
	}
	return string(b)
}

// ---- review fixes (ut-docs#4763, Fable review) ----

// bulkItems adds n linked items with stock, each under a RANDOM cloud id
// (sequential ids hid the cloud_links index problem).
func (h *harness) bulkItems(n int) {
	h.t.Helper()
	h.exec(`WITH RECURSIVE s(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM s WHERE i < ?)
INSERT INTO items (id, name, base_price) SELECT 'p'||i, 'p'||i, 1 FROM s`, n)
	h.exec(`WITH RECURSIVE s(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM s WHERE i < ?)
INSERT INTO cloud_links (entity, cloud_id, local_id, version, updated_at)
SELECT 'item', lower(substr(hex(randomblob(16)),1,8)||'-'||substr(hex(randomblob(16)),1,4)||'-4'||substr(hex(randomblob(16)),1,3)||'-a'||substr(hex(randomblob(16)),1,3)||'-'||substr(hex(randomblob(16)),1,12)), 'p'||i, 1, 'x' FROM s`, n)
	h.exec(`WITH RECURSIVE s(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM s WHERE i < ?)
INSERT INTO inventory (id, item_id, location_id, quantity) SELECT 'pi'||i, 'p'||i, ?, 1 FROM s`, n, h.loc)
}

func TestRun_ACloudIdThatIsNotAUUIDIsDroppedAndTheOthersStillGo(t *testing.T) {
	h := newHarness(t)
	h.bulkItems(499)
	h.exec(`INSERT INTO items (id, name, base_price) VALUES ('bad', 'bad', 1)`)
	h.exec(`INSERT INTO cloud_links (entity, cloud_id, local_id, version, updated_at) VALUES ('item', 'not-a-uuid', 'bad', 1, 'x')`)
	h.exec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('bad-inv', 'bad', ?, 1)`, h.loc)
	res, err := h.run(h.poster("applied"))
	if err != nil || res.Applied != 499 || res.Dropped != 1 || res.DropReasons[stockupload.DropBadCloudID] != 1 || h.marks() != 0 {
		t.Fatalf("res = %+v err %v marks %d", res, err, h.marks())
	}
	for _, r := range h.sent[0].Rows {
		if r.ItemID == "not-a-uuid" {
			t.Fatal("a non-UUID id was sent")
		}
	}
}

// poisonPoster refuses (400) any request holding the poison seq, as the
// cloud's strict decoder would, and applies the rest.
func (h *harness) poisonPoster(poison func(stockupload.Row) bool) (stockupload.Poster, *int) {
	calls := 0
	return func(_ context.Context, req stockupload.Request) ([]stockupload.Answer, error) {
		calls++
		out := make([]stockupload.Answer, len(req.Rows))
		for i, r := range req.Rows {
			if poison(r) {
				return nil, &stockupload.StopError{Status: 400, Code: "invalid_request"}
			}
			out[i] = stockupload.Answer{Seq: r.Seq, Status: "applied"}
		}
		return out, nil
	}, &calls
}

func TestRun_OneRefusedRowDoesNotStopFourHundredNinetyNineGoodOnes(t *testing.T) {
	h := newHarness(t)
	h.bulkItems(500)
	var poisonSeq int64
	if err := h.d.QueryRow(`SELECT o.seq FROM stock_upload_outbox o WHERE o.item_id = 'p137'`).Scan(&poisonSeq); err != nil {
		t.Fatal(err)
	}
	p, calls := h.poisonPoster(func(r stockupload.Row) bool { return r.Seq == poisonSeq })
	res, err := h.run(p)
	if err != nil || res.Stopped != "" || res.Applied != 499 || res.Dropped != 1 || res.DropReasons[stockupload.DropCloudRefused] != 1 {
		t.Fatalf("res = %+v err %v", res, err)
	}
	if h.marks() != 0 {
		t.Fatalf("%d marks left: the poison row must go and the good rows must be acked", h.marks())
	}
	if *calls > 30 {
		t.Fatalf("finding one refused row took %d requests", *calls)
	}
}

func TestRun_AWholeRequestThatIsRefusedKeepsEveryMark(t *testing.T) {
	h := newHarness(t)
	h.bulkItems(20)
	p, _ := h.poisonPoster(func(stockupload.Row) bool { return true }) // e.g. a bad stream id: every part is refused
	res, err := h.run(p)
	if err != nil || res.Stopped == "" || res.StopStatus != 400 || res.Dropped != 0 || h.marks() != 20 {
		t.Fatalf("res = %+v err %v marks %d (nothing may be dropped for a systemic refusal)", res, err, h.marks())
	}
}

func TestRun_UnknownItemIsKeptThenRetriedLaterThenDropped(t *testing.T) {
	h := newHarness(t)
	h.item("a", cloudA, 3)
	unknown := func(_ context.Context, req stockupload.Request) ([]stockupload.Answer, error) {
		h.sent = append(h.sent, req)
		return []stockupload.Answer{{Seq: req.Rows[0].Seq, Status: "rejected", Code: "unknown_item"}}, nil
	}
	res, err := h.run(unknown)
	if err != nil || res.Rejected != 1 || res.Deferred != 1 || res.Dropped != 0 || h.marks() != 1 {
		t.Fatalf("first answer: res = %+v err %v marks %d (the quantity must be kept)", res, err, h.marks())
	}
	// Not re-sent at once: it waits out its retry delay (also within one Run).
	h.sent = nil
	if res, _ := h.run(h.poster("applied")); res.Sent != 0 || len(h.sent) != 0 {
		t.Fatalf("a deferred mark was re-sent at once: %+v", res)
	}
	// Its delay passes: sent again, and applied this time.
	h.exec(`UPDATE stock_upload_outbox SET retry_after = ''`)
	if res, _ := h.run(h.poster("applied")); res.Applied != 1 || h.marks() != 0 {
		t.Fatalf("after the delay: %+v marks %d", res, h.marks())
	}
	// Still unknown on the last allowed attempt: dropped with a code.
	h.exec(`UPDATE inventory SET quantity = 9`)
	h.exec(`UPDATE stock_upload_outbox SET attempts = 4`)
	res, err = h.run(unknown)
	if err != nil || res.Dropped != 1 || res.DropReasons[stockupload.DropUnknownItem] != 1 || h.marks() != 0 {
		t.Fatalf("giving up: res = %+v err %v marks %d", res, err, h.marks())
	}
}

func TestRun_AnOtherRejectedCodeIsDroppedNotRetried(t *testing.T) {
	h := newHarness(t)
	h.item("a", cloudA, 3)
	p := func(_ context.Context, req stockupload.Request) ([]stockupload.Answer, error) {
		return []stockupload.Answer{{Seq: req.Rows[0].Seq, Status: "rejected", Code: "invalid_row"}}, nil
	}
	res, err := h.run(p)
	if err != nil || res.Deferred != 0 || res.DropReasons[stockupload.DropRejectedOther] != 1 || h.marks() != 0 {
		t.Fatalf("res = %+v err %v marks %d", res, err, h.marks())
	}
}

func TestRun_AnOlderStreamAnswerStartsANewerStream(t *testing.T) {
	h := newHarness(t)
	h.item("a", cloudA, 5)
	repo := data.NewStockUploadRepo(h.d.DB)
	oldS, _ := repo.Stream(context.Background())
	p := func(_ context.Context, req stockupload.Request) ([]stockupload.Answer, error) {
		h.sent = append(h.sent, req)
		if req.StreamID == oldS.ID { // the cloud holds a newer stream (no stored_seq)
			return []stockupload.Answer{{Seq: req.Rows[0].Seq, Status: "stale", Code: "older_stream"}}, nil
		}
		return []stockupload.Answer{{Seq: req.Rows[0].Seq, Status: "applied"}}, nil
	}
	res, err := h.run(p)
	if err != nil || !res.Rotated || res.Applied != 1 || len(h.sent) != 2 || h.marks() != 0 {
		t.Fatalf("res = %+v err %v sent %d", res, err, len(h.sent))
	}
	if !h.sent[1].StreamStartedAt.After(h.sent[0].StreamStartedAt) {
		t.Fatal("the new stream must start after the old one")
	}
}

func TestRun_ASlowCloudIsBoundedByTheContext(t *testing.T) {
	h := newHarness(t)
	h.item("a", cloudA, 5)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := stockupload.Run(ctx, h.d.DB, func(ctx context.Context, _ stockupload.Request) ([]stockupload.Answer, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}, "0.34.18")
	if err == nil || time.Since(start) > 2*time.Second || h.marks() != 1 {
		t.Fatalf("err %v after %v, marks %d", err, time.Since(start), h.marks())
	}
}

func TestRun_PurgesMarksOfDeletedInventoryOnceNothingIsOwed(t *testing.T) {
	h := newHarness(t)
	h.item("a", cloudA, 1)
	h.item("gone", cloudB, 1)
	h.exec(`DELETE FROM inventory WHERE item_id = 'gone'`)
	if _, err := h.run(h.poster("applied")); err != nil {
		t.Fatal(err)
	}
	if h.marks() != 0 {
		t.Fatalf("%d marks left; the orphan must be purged", h.marks())
	}
}

// ---- review round 2 (ut-docs#4763, Opus) ----

func (h *harness) streamID() string {
	h.t.Helper()
	s, err := data.NewStockUploadRepo(h.d.DB).Stream(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return s.ID
}

// The cloud always answers older_stream (a cloned disk out-rotating this till,
// a stored start up to a day ahead, a Pi without an RTC rotating before NTP).
// The first Run may rotate once; it must then FAIL, keeping every mark (the
// old code acked them all as done: the mirror kept the other stream's levels
// and nothing was reported). Later Runs neither rotate again nor lose a mark,
// and cost one request each.
func TestRun_AnAlwaysOlderStreamCloudLosesNoMarkAndCannotMakeARotationStorm(t *testing.T) {
	h := newHarness(t)
	h.bulkItems(60)
	calls := 0
	always := func(_ context.Context, req stockupload.Request) ([]stockupload.Answer, error) {
		calls++
		out := make([]stockupload.Answer, len(req.Rows))
		for i, r := range req.Rows {
			out[i] = stockupload.Answer{Seq: r.Seq, Status: "stale", Code: "older_stream"}
		}
		return out, nil
	}
	first := h.streamID()
	res, err := h.run(always)
	if !errors.Is(err, stockupload.ErrStreamConflict) || !res.Rotated || h.marks() != 60 || calls != 2 {
		t.Fatalf("run 1: res %+v err %v marks %d calls %d (want a failure, one rotation, 2 requests, 60 marks kept)", res, err, h.marks(), calls)
	}
	rotated := h.streamID()
	if rotated == first {
		t.Fatal("run 1 did not rotate")
	}
	for i := 2; i <= 6; i++ {
		before := calls
		res, err = h.run(always)
		if !errors.Is(err, stockupload.ErrStreamConflict) || res.Rotated || h.marks() != 60 || calls-before != 1 || h.streamID() != rotated {
			t.Fatalf("run %d: res %+v err %v marks %d requests %d stream changed %t", i, res, err, h.marks(), calls-before, h.streamID() != rotated)
		}
	}
}

// Once the rate limit has passed, a stale-ahead cloud (a real restore) is
// still recovered by a rotation.
func TestRun_ARotationIsAllowedAgainAfterTheRateLimit(t *testing.T) {
	h := newHarness(t)
	h.item("a", cloudA, 5)
	older := func(_ context.Context, req stockupload.Request) ([]stockupload.Answer, error) {
		return []stockupload.Answer{{Seq: req.Rows[0].Seq, Status: "stale", Code: "older_stream"}}, nil
	}
	if _, err := h.run(older); !errors.Is(err, stockupload.ErrStreamConflict) {
		t.Fatalf("first: %v", err)
	}
	s1 := h.streamID()
	if _, err := h.run(older); !errors.Is(err, stockupload.ErrStreamConflict) || h.streamID() != s1 {
		t.Fatalf("within the limit it must not rotate: %v", err)
	}
	h.exec(`UPDATE stock_upload_state SET rotated_at = ?`, time.Now().Add(-data.StockUploadRotateMinInterval-time.Minute).UTC().Format(time.RFC3339))
	if res, err := h.run(older); !errors.Is(err, stockupload.ErrStreamConflict) || !res.Rotated || h.streamID() == s1 {
		t.Fatalf("after the limit it must rotate again: %+v %v", res, err)
	}
}

// A stored stream start in the future (the till's clock was wrong when it was
// written) is never sent as is.
func TestRun_AStreamStartInTheFutureIsResetToNow(t *testing.T) {
	h := newHarness(t)
	h.item("a", cloudA, 5)
	h.exec(`UPDATE stock_upload_state SET started_at = ?`, time.Now().Add(20*time.Hour).UTC().Format(time.RFC3339Nano))
	if _, err := h.run(h.poster("applied")); err != nil {
		t.Fatal(err)
	}
	if len(h.sent) != 1 || h.sent[0].StreamStartedAt.After(time.Now().Add(time.Minute)) {
		t.Fatalf("sent start %v, now %v", h.sent[0].StreamStartedAt, time.Now())
	}
}

// The cloud's 400 stream_start_in_future (a clock the till could not tell was
// fast) resets the start to now and ends the Run as a refusal; marks kept.
func TestRun_TheCloudRefusingAFutureStartResetsIt(t *testing.T) {
	h := newHarness(t)
	h.item("a", cloudA, 5)
	h.exec(`UPDATE stock_upload_state SET started_at = ?`, time.Now().Add(30*time.Second).UTC().Format(time.RFC3339Nano))
	refuse := func(context.Context, stockupload.Request) ([]stockupload.Answer, error) {
		return nil, &stockupload.StopError{Status: 400, Code: "stream_start_in_future"}
	}
	res, err := h.run(refuse)
	if err != nil || res.Stopped != "stream_start_in_future" || h.marks() != 1 {
		t.Fatalf("res %+v err %v marks %d", res, err, h.marks())
	}
	s, _ := data.NewStockUploadRepo(h.d.DB).Stream(context.Background())
	if s.StartedAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("start still in the future: %v", s.StartedAt)
	}
}

// Three poison rows in one batch of 500 exceed one Run's bisect budget. The
// Run must still make progress (drop what it found, ack what it proved good)
// and the next Runs finish the batch: nothing is stuck forever.
func TestRun_ThreePoisonRowsInOneBatchAreWorkedOffAcrossRuns(t *testing.T) {
	h := newHarness(t)
	h.bulkItems(500)
	poison := map[int64]bool{}
	for _, id := range []string{"p13", "p250", "p487"} {
		var seq int64
		if err := h.d.QueryRow(`SELECT seq FROM stock_upload_outbox WHERE item_id = ?`, id).Scan(&seq); err != nil {
			t.Fatal(err)
		}
		poison[seq] = true
	}
	p, calls := h.poisonPoster(func(r stockupload.Row) bool { return poison[r.Seq] })
	dropped := 0
	for run := 1; run <= 4 && h.marks() > 0; run++ {
		before := *calls
		res, err := h.run(p)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if *calls-before > 45 {
			t.Fatalf("run %d spent %d requests", run, *calls-before)
		}
		dropped += res.Dropped
	}
	if h.marks() != 0 || dropped != 3 {
		t.Fatalf("after 4 runs: %d marks left, %d dropped (want 0 and 3)", h.marks(), dropped)
	}
}
