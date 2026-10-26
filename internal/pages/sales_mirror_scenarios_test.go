package pages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/diskspace"
)

// The three-till scenarios of design §13 that the pull path (M2 + M3)
// covers, on the harness: real tills, real journal push, real TLS feed,
// real apply. The link fast path (frames) is M4's.

func TestSalesMirrorScenario_FanOutWithOneTillBrieflyOffline(t *testing.T) {
	shop := newMirrorShop(t, "A", "B", "C")
	shop.enable()
	a, b, c := shop.tills["A"], shop.tills["B"], shop.tills["C"]
	for _, till := range shop.values() {
		shop.pull(till) // first pull: state re-sync of an empty window
	}
	// A sells; it reaches M by the journal and B by the copy (AC1).
	s1 := shop.sell(a, 250)
	shop.push(a)
	shop.pull(b)
	var origin string
	if err := b.d.Db.QueryRow(`SELECT origin_till_id FROM mirror_sales WHERE id = ?`, s1).Scan(&origin); err != nil || origin != a.tillID {
		t.Fatalf("B's copy of S1: origin %q err %v, want A", origin, err)
	}
	if n := harnessCount(t, a.d.Db, `SELECT COUNT(*) FROM mirror_sales`); n != 0 {
		t.Fatalf("A holds %d copied rows of its own sale", n)
	}
	head, _ := data.NewSalesMirrorRepo(shop.main.d.Db).LogHead(t.Context())
	if ack := shop.seqAcked(b); ack < head-1 {
		// B acknowledges what it holds on its next request.
		shop.pull(b)
	}
	// C is offline (does not pull) through 20 sales on A, B and M.
	for i := range 20 {
		switch i % 3 {
		case 0:
			shop.sell(a, int64(100+i))
		case 1:
			shop.sell(b, int64(100+i))
		default:
			shop.sell(shop.main, int64(100+i))
		}
	}
	shop.push(a)
	shop.push(b)
	shop.pull(b)
	shop.pull(c) // C comes back
	shop.pull(a)
	for _, till := range shop.values() {
		shop.assertCopyEqualsMain(till)
	}
	_ = c
}

// AC2: B offline while A sells 120 and M sells 30 and voids one, plus a
// close; B comes back and one pull makes its copy equal M's state, the void
// included, with no duplicate rows.
func TestSalesMirrorScenario_OfflineThroughManyChanges(t *testing.T) {
	shop := newMirrorShop(t, "A", "B")
	shop.enable()
	a, b := shop.tills["A"], shop.tills["B"]
	shop.pull(b)
	for i := range 120 {
		shop.sell(a, int64(100+i))
	}
	for range 3 { // the journal pushes 50 at a time
		shop.push(a)
	}
	var mine []string
	for i := range 30 {
		mine = append(mine, shop.sell(shop.main, int64(200+i)))
	}
	ctx := t.Context()
	repo := data.NewPOSRepo(shop.main.d.Db)
	if err := repo.UpdateSaleStatus(ctx, nil, mine[7], "completed", "voided"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ArchiveReport(ctx, "eod", "2026-10-26T18:00:00Z", []byte(`{"n":150}`), "", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if n := harnessCount(t, shop.main.d.Db, `SELECT COUNT(*) FROM sales WHERE till_id = ?`, a.tillID); n != 120 {
		t.Fatalf("main till holds %d of A's 120 sales", n)
	}
	shop.pull(b)
	shop.assertCopyEqualsMain(b)
	var status string
	_ = b.d.Db.QueryRow(`SELECT status FROM mirror_sales WHERE id = ?`, mine[7]).Scan(&status)
	if status != "voided" {
		t.Fatalf("the void reached B as %q", status)
	}
	if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM mirror_closes`); n != 1 {
		t.Fatalf("B holds %d closes, want 1", n)
	}
	if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM mirror_sales`); n != 150 {
		t.Fatalf("B holds %d copied sales, want 150", n)
	}
}

// Two tills at different lags both converge; the prune horizon never
// passes the lower acknowledgement until both caught up (design §13.3, §13.4).
func TestSalesMirrorScenario_DifferentLagsAndPruneSafety(t *testing.T) {
	shop := newMirrorShop(t, "B", "C")
	shop.enable()
	b, c := shop.tills["B"], shop.tills["C"]
	shop.pull(b)
	shop.pull(c)
	ctx := t.Context()
	m := data.NewSalesMirrorRepo(shop.main.d.Db)
	noDisk := func(string) (diskspace.Usage, error) { return diskspace.Usage{}, diskspace.ErrUnsupported }
	for range 10 {
		shop.sell(shop.main, 100)
	}
	shop.pull(b) // B catches up; C stays behind
	shop.pull(b) // and acknowledges
	cAck := shop.seqAcked(c)
	for range 600 {
		shop.sell(shop.main, 100)
	}
	for range 3 {
		salesMirrorMaintenanceTick(ctx, shop.main.d, time.Now(), noDisk)
		if hz, _ := m.LogHorizon(ctx); hz > cAck {
			t.Fatalf("prune passed C's ack: horizon %d > %d", hz, cAck)
		}
	}
	shop.pull(c) // 3+ pages
	shop.pull(b)
	shop.assertCopyEqualsMain(b)
	shop.assertCopyEqualsMain(c)
	shop.pull(c)
	shop.pull(b)
	salesMirrorMaintenanceTick(ctx, shop.main.d, time.Now(), noDisk)
	head, _ := m.LogHead(ctx)
	// Acks are written at most every 5 s unless they moved by 50 (design
	// §4.2), so the prune may trail the head by less than that.
	if hz, _ := m.LogHorizon(ctx); hz < head-salesMirrorAckEvery {
		t.Fatalf("after both caught up the log should prune near the head: horizon %d head %d", hz, head)
	}
}

// AC10 / §13.5: caps lowered to 100 rows, B offline through 300 changes:
// the cap overrides B's ack, B gets 410, re-syncs by state, and its copy
// equals M's with no duplicates — never a database replace.
func TestSalesMirrorScenario_HorizonOverflowResyncsByState(t *testing.T) {
	shop := newMirrorShop(t, "B")
	shop.enable()
	b := shop.tills["B"]
	ctx := t.Context()
	shop.pull(b)
	early := shop.sell(shop.main, 100)
	shop.pull(b)
	shop.pull(b)
	// B's own sales stay in its sales table, untouched by the re-sync.
	ownB := shop.sell(b, 77)
	shop.setShop(data.SalesMirrorLogMaxRowsKey, "100")
	for range 300 {
		shop.sell(shop.main, 100)
	}
	salesMirrorMaintenanceTick(ctx, shop.main.d, time.Now(), func(string) (diskspace.Usage, error) { return diskspace.Usage{}, diskspace.ErrUnsupported })
	st, _ := data.NewSalesMirrorRepo(b.d.Db).State(ctx)
	if r := shop.feedAfter(b, st.Epoch, st.Cursor, 10); r.status != http.StatusGone {
		t.Fatalf("B behind the horizon got %d, want 410", r.status)
	}
	shop.pull(b)
	shop.assertCopyEqualsMain(b)
	if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM mirror_sales WHERE id = ?`, early); n != 1 {
		t.Fatalf("the sale B held before the overflow: %d rows", n)
	}
	if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM sales WHERE id = ?`, ownB); n != 1 {
		t.Fatal("B's own sale is gone after the re-sync")
	}
	if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM mirror_sales`); n != 301 {
		t.Fatalf("B's copy holds %d sales, want 301", n)
	}
}

// failAfter is a transport that fails every request after the first n: a
// till that dies (or loses the network) mid-catch-up.
type failAfter struct {
	n    int32
	seen atomic.Int32
	rt   http.RoundTripper
}

func (f *failAfter) RoundTrip(r *http.Request) (*http.Response, error) {
	if f.seen.Add(1) > f.n {
		return nil, errors.New("injected: till gone mid-catch-up")
	}
	return f.rt.RoundTrip(r)
}

// AC4 / §13.6: the catch-up is cut between pages, three times at different
// points; each page committed with its cursor, so the next pull resumes and
// the final copy equals M's with no duplicates. (In-process: the transport
// dies instead of the process; what survives is exactly what a SIGKILL
// leaves, the committed pages.)
func TestSalesMirrorScenario_CutMidCatchUpResumes(t *testing.T) {
	shop := newMirrorShop(t, "B")
	shop.enable()
	b := shop.tills["B"]
	for range 1100 {
		shop.sell(shop.main, 100)
	}
	p := shop.puller(b)
	orig := p.client
	for _, n := range []int32{2, 3, 1} {
		p.client = &http.Client{Transport: &failAfter{n: n, rt: orig.Transport}}
		p.tick(t.Context())
		st, _ := p.repo.State(t.Context())
		t.Logf("after a cut at request %d: cursor %d, re-sync %q", n, st.Cursor, st.ResyncCursor)
	}
	p.client = orig
	shop.pull(b)
	shop.pull(b)
	shop.assertCopyEqualsMain(b)
	if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM mirror_sales`); n != 1100 {
		t.Fatalf("copy holds %d, want 1100", n)
	}
}

// AC6 / §13.8: the cursor is the log seq, never a clock: B's clock skewed
// +3 h and C's -2 h still converge.
func TestSalesMirrorScenario_ClockSkew(t *testing.T) {
	shop := newMirrorShop(t, "A", "B", "C")
	shop.enable()
	a, b, c := shop.tills["A"], shop.tills["B"], shop.tills["C"]
	shop.puller(b).now = func() time.Time { return time.Now().Add(3 * time.Hour) }
	shop.puller(c).now = func() time.Time { return time.Now().Add(-2 * time.Hour) }
	for i := range 30 {
		shop.sell(a, int64(100+i))
		shop.sell(shop.main, int64(100+i))
	}
	shop.push(a)
	for _, till := range []*mirrorTill{b, c} {
		shop.pull(till)
		shop.pull(till)
		shop.assertCopyEqualsMain(till)
	}
}

// AC7 + kill switches: off on M answers 409 and logs nothing; the copy is
// kept; on again mints a new epoch and B re-syncs. B's own kill switch and
// the process kill switch stop B from asking at all.
func TestSalesMirrorScenario_KillSwitchesKeepTheCopy(t *testing.T) {
	shop := newMirrorShop(t, "B")
	e1 := shop.enable()
	b := shop.tills["B"]
	ctx := t.Context()
	shop.sell(shop.main, 100)
	shop.pull(b)
	shop.pull(b)
	shop.setShop(data.SalesMirrorEnabledKey, "0")
	logged := harnessCount(t, shop.main.d.Db, `SELECT COUNT(*) FROM sales_mirror_log`)
	offSale := shop.sell(shop.main, 100)
	if n := harnessCount(t, shop.main.d.Db, `SELECT COUNT(*) FROM sales_mirror_log`); n != logged {
		t.Fatal("the main till logged a sale with the copy off")
	}
	// B's admin bundle says off: it does not even ask.
	shop.pull(b)
	if st := shop.puller(b).Status(ctx); st.State != "off" {
		t.Fatalf("B status with the copy off = %q", st.State)
	}
	// Off on M only (B still thinks on): 409, B keeps its copy.
	_ = b.d.Settings.Set(ctx, data.SalesMirrorEnabledKey, "1")
	shop.pull(b)
	if st := shop.puller(b).Status(ctx); st.State != "main_off" {
		t.Fatalf("B status with M off = %q", st.State)
	}
	if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM mirror_sales`); n != 1 {
		t.Fatalf("B's copy after off = %d rows, want kept (1)", n)
	}
	// On again: new epoch; B re-syncs by state and picks up the sale made
	// while off, which was never logged.
	e2 := shop.enableNewEpoch()
	if e2 == e1 {
		t.Fatal("re-enable kept the epoch")
	}
	shop.pull(b)
	shop.pull(b)
	shop.assertCopyEqualsMain(b)
	if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM mirror_sales WHERE id = ?`, offSale); n != 1 {
		t.Fatal("the sale made while the copy was off never reached B")
	}
	// B's own kill switch, then the process kill switch, then the shop
	// setting off on B: no request reaches the main till at all.
	shop.sell(shop.main, 100)
	before := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM mirror_sales`)
	reqs := shop.copyRequests.Load()
	_ = b.d.Settings.Set(ctx, data.SalesMirrorTillOffKey, "1")
	shop.pull(b)
	_ = b.d.Settings.Set(ctx, data.SalesMirrorTillOffKey, "0")
	t.Setenv("UT_SALES_MIRROR", "off")
	shop.pull(b)
	t.Setenv("UT_SALES_MIRROR", "")
	_ = b.d.Settings.Set(ctx, data.SalesMirrorEnabledKey, "0")
	shop.pull(b)
	if n := shop.copyRequests.Load() - reqs; n != 0 {
		t.Fatalf("a killed or switched-off till sent %d copy request(s)", n)
	}
	if after := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM mirror_sales`); after != before {
		t.Fatalf("a killed till copied: %d → %d", before, after)
	}
}

// enableNewEpoch is the switch handler's enable: on, with a fresh epoch,
// carried to every till.
func (s *mirrorShop) enableNewEpoch() string {
	s.t.Helper()
	e := fmt.Sprintf("epoch-%d", time.Now().UnixNano())
	s.setShop(data.SalesMirrorEpochKey, e)
	s.setShop(data.SalesMirrorEnabledKey, "1")
	return e
}

// AC3 on a real additional till: mirroring changes none of B's own numbers.
func TestSalesMirrorScenario_CopyNeverCountsOnTheAdditionalTill(t *testing.T) {
	shop := newMirrorShop(t, "A", "B")
	shop.enable()
	a, b := shop.tills["A"], shop.tills["B"]
	ctx := t.Context()
	shop.sell(b, 300)
	shop.sell(b, 400)
	repo := data.NewPOSRepo(b.d.Db)
	from, to := time.Now().Add(-24*time.Hour), time.Now().Add(time.Hour)
	snap := func() string {
		eod, _ := repo.EndOfDayInstant(ctx, from, to)
		eod.GeneratedAt = "" // stamped with the wall clock on every call (M3 review finding 1)
		next, _ := repo.NextReceiptNo(ctx, nil)
		queue, _ := repo.LocalSalesSince(ctx, "", 1000)
		var stock float64
		_ = b.d.Db.QueryRow(`SELECT COALESCE(SUM(quantity), 0) FROM inventory`).Scan(&stock)
		cursor, _, _ := b.d.Settings.Get(ctx, "sync.push_cursor")
		return fmt.Sprintf("%#v|%s|%v|%v|%s|%d", eod, next, queue, stock, cursor, harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM sales`))
	}
	before := snap()
	for range 200 {
		shop.sell(a, 100)
		shop.sell(shop.main, 100)
	}
	for range 5 {
		shop.push(a)
	}
	shop.pull(b)
	shop.pull(b)
	if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM mirror_sales`); n != 400 {
		t.Fatalf("B copied %d sales, want 400", n)
	}
	if after := snap(); after != before {
		t.Fatalf("B's own figures changed after copying 400 sales:\n before %s\n after  %s", before, after)
	}
}

// AC11: below the low-disk floor the copy pauses and trims to 31 days; no
// sale is affected. Over the budget it drops the oldest days first.
func TestSalesMirrorBounds_LowDiskPausesAndTrims(t *testing.T) {
	shop := newMirrorShop(t, "B")
	shop.enable()
	b := shop.tills["B"]
	ctx := t.Context()
	shop.pull(b)
	repo := data.NewSalesMirrorRepo(b.d.Db)
	st, _ := repo.State(ctx)
	var recs []data.MirrorRecord
	for i, days := range []int{200, 100, 40, 10, 0} {
		s := &data.MirrorSale{ID: fmt.Sprintf("old-%d", i), OriginTillID: "x", ReceiptNo: "T9-1", Status: "completed",
			SaleType: "sale", TenderType: "cash", Currency: "GBP", CreatedAt: "2026-01-01T00:00:00Z",
			LocalDate: localDateDaysAgo(time.Now(), days)}
		recs = append(recs, data.MirrorRecord{Seq: st.Cursor + int64(i) + 1, Op: data.MirrorOpSale, Sale: s})
	}
	if _, err := repo.ApplyFeedPage(ctx, data.MirrorFeedPage{Epoch: st.Epoch, FromSeq: st.Cursor, ToSeq: st.Cursor + 5, Records: recs},
		data.MirrorApplyOpts{SelfTillID: b.tillID, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	b.d.Cfg.DBPath = "/x"
	p := shop.puller(b)
	p.probe = func(string) (diskspace.Usage, error) { return diskspace.Usage{Total: 100 << 30, Free: 1 << 20}, nil }
	if reason := p.bounds(ctx, true); reason != salesMirrorPausedLowDisk {
		t.Fatalf("pause = %q", reason)
	}
	if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM mirror_sales`); n != 2 {
		t.Fatalf("after low disk %d sales left, want the 2 within 31 days", n)
	}
	shop.sell(shop.main, 100)
	shop.pull(b) // paused: nothing new
	if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM mirror_sales`); n != 2 {
		t.Fatalf("a paused copy grew to %d", n)
	}
	shop.sell(b, 100) // a sale on B is unaffected
	// Disk recovered: copying resumes.
	p.probe = func(string) (diskspace.Usage, error) { return diskspace.Usage{Total: 100 << 30, Free: 50 << 30}, nil }
	p.lastBounds = time.Time{}
	shop.pull(b)
	if st, _ := repo.State(ctx); st.PausedReason != "" {
		t.Fatalf("still paused: %q", st.PausedReason)
	}
	// Over the budget (max_mb = 1, disk roomy): the oldest whole days go
	// first until the copy fits, never below 31 days.
	st, _ = repo.State(ctx)
	recs = recs[:0]
	for i := range 1300 {
		s := &data.MirrorSale{ID: fmt.Sprintf("bulk-%d", i), OriginTillID: "x", ReceiptNo: "T9-2", Status: "completed",
			SaleType: "sale", TenderType: "cash", Currency: "GBP", CreatedAt: "2026-01-01T00:00:00Z",
			LocalDate: localDateDaysAgo(time.Now(), i/6),
			Lines:     []data.MirrorSaleLine{{ID: fmt.Sprintf("bulk-%d-l", i), NameSnapshot: "x", Quantity: 1}},
			Payments:  []data.MirrorPayment{{ID: fmt.Sprintf("bulk-%d-p", i), MethodID: "cash", Currency: "GBP", PaidAt: "x"}}}
		recs = append(recs, data.MirrorRecord{Seq: st.Cursor + 1, Op: data.MirrorOpSale, Sale: s})
	}
	if _, err := repo.ApplyFeedPage(ctx, data.MirrorFeedPage{Epoch: st.Epoch, FromSeq: st.Cursor, ToSeq: st.Cursor + 1, Records: recs},
		data.MirrorApplyOpts{SelfTillID: b.tillID, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	shop.setShop(data.SalesMirrorMaxMBKey, "1")
	if reason := p.bounds(ctx, true); reason != "" {
		t.Fatalf("budget trim paused (%q) though days older than 31 were left to drop", reason)
	}
	stats, _ := repo.CopyStats(ctx)
	if stats.BytesEstimate() > 1<<20 {
		t.Fatalf("copy is %d bytes after the budget trim, over 1 MiB", stats.BytesEstimate())
	}
	if stats.OldestLocalDate < localDateDaysAgo(time.Now(), 199) || stats.OldestLocalDate > localDateDaysAgo(time.Now(), salesMirrorFloorDays) {
		t.Fatalf("oldest kept day %s: expected the oldest days dropped, the last 31 kept", stats.OldestLocalDate)
	}
}

// The loop joins app.Run's drain and exits on cancel; the status route then
// shows this till's copy.
func TestStartSalesMirrorPull_JoinsWaitGroupAndReportsStatus(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	shop := newMirrorShop(t, "B")
	b := shop.tills["B"]
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	StartSalesMirrorPull(ctx, b.d, &wg)
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the pull loop did not exit on cancel")
	}
	mux := http.NewServeMux()
	registerSalesMirrorStatus(mux, b.d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sync/sales-mirror/status", nil))
	var env struct {
		Data struct {
			Main SalesMirrorMainStatus `json:"main"`
			Copy struct {
				Status SalesMirrorStatus `json:"status"`
			} `json:"copy"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Main.State != "not_main" || env.Data.Copy.Status.State != "off" {
		t.Fatalf("status = %+v", env.Data)
	}
}

// A feed version above this build's pauses the copy (version_wait); once
// the feed is readable again the pause clears and the status says "on"
// (M3 review finding 2).
func TestSalesMirrorPull_VersionWaitPausesThenClears(t *testing.T) {
	shop := newMirrorShop(t, "B")
	shop.enable()
	b := shop.tills["B"]
	ctx := t.Context()
	shop.pull(b)
	p := shop.puller(b)
	if err := p.checkVersion(ctx, SalesMirrorFeedVersion+1); !errors.Is(err, errSalesMirrorStop) {
		t.Fatalf("newer feed: %v", err)
	}
	if st := p.Status(ctx); st.State != "paused:version_wait" {
		t.Fatalf("status = %q, want paused:version_wait", st.State)
	}
	shop.sell(shop.main, 100)
	shop.pull(b) // the main till answers feed_version 1 again
	if st := p.Status(ctx); st.State != "on" {
		t.Fatalf("status after a readable page = %q, want on", st.State)
	}
	shop.assertCopyEqualsMain(b)
}

// A satellite's request is refused quietly: a status, not an error every
// tick, and nothing stored.
func TestSalesMirrorPull_SatelliteIsRefusedQuietly(t *testing.T) {
	shop := newMirrorShop(t, "B")
	shop.enable()
	b := shop.tills["B"]
	if _, err := shop.main.d.Db.Exec(`UPDATE tills SET role = 'satellite' WHERE id = ?`, b.tillID); err != nil {
		t.Fatal(err)
	}
	shop.sell(shop.main, 100)
	shop.pull(b)
	st := shop.puller(b).Status(t.Context())
	if st.State != "satellite" || st.LastError != "" {
		t.Fatalf("satellite status = %+v", st)
	}
	if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM mirror_sales`); n != 0 {
		t.Fatalf("a satellite stored %d copied sales", n)
	}
}

// A failure inside the apply transaction (a crash between two statements)
// leaves nothing: no partial rows, cursor unchanged; the next pull applies
// the page whole (design §13 "kill between statements").
func TestSalesMirrorPull_FailureMidTransactionLeavesNothing(t *testing.T) {
	shop := newMirrorShop(t, "B")
	shop.enable()
	b := shop.tills["B"]
	ctx := t.Context()
	shop.pull(b)
	for range 30 {
		shop.sell(shop.main, 100)
	}
	b.d.Db.SetMaxOpenConns(1) // the TEMP trigger lives on one connection
	if _, err := b.d.Db.Exec(`CREATE TEMP TABLE crash_target (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := b.d.Db.Exec(`CREATE TEMP TRIGGER crash_mid_page BEFORE INSERT ON main.mirror_payments
WHEN (SELECT COUNT(*) FROM main.mirror_payments) >= 12 BEGIN INSERT INTO temp.crash_target (x) VALUES (1); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := b.d.Db.Exec(`DROP TABLE temp.crash_target`); err != nil {
		t.Fatal(err)
	}
	before, _ := data.NewSalesMirrorRepo(b.d.Db).State(ctx)
	shop.pull(b)
	after, _ := data.NewSalesMirrorRepo(b.d.Db).State(ctx)
	if after.Cursor != before.Cursor || harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM mirror_sales`) != 0 {
		t.Fatalf("a failed page left rows (%d) or moved the cursor (%d -> %d)",
			harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM mirror_sales`), before.Cursor, after.Cursor)
	}
	// Not a constraint error (a record the schema refuses is rejected and
	// skipped by design): an I/O-class failure — here a table that is gone —
	// must abort the whole page.
	if _, err := b.d.Db.Exec(`DROP TRIGGER temp.crash_mid_page`); err != nil {
		t.Fatal(err)
	}
	shop.pull(b)
	shop.assertCopyEqualsMain(b)
}
