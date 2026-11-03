package pages

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/diskspace"
)

// The main till's sales-copy feed (ut-docs#5074 M2), driven over real TLS
// through the harness.

type feedReply struct {
	status int
	page   SalesMirrorPage
	state  SalesMirrorStatePage
	err    salesMirrorError
	header http.Header
}

func (s *mirrorShop) get(till *mirrorTill, path string, q url.Values) feedReply {
	s.t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.server.URL+path+"?"+q.Encode(), nil)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+till.bearer)
	resp, err := s.client.Do(req)
	if err != nil {
		s.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := feedReply{status: resp.StatusCode, header: resp.Header}
	var env struct {
		Data  json.RawMessage   `json:"data"`
		Error *salesMirrorError `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		s.t.Fatalf("GET %s: %d %s: %v", path, resp.StatusCode, raw, err)
	}
	if env.Error != nil {
		out.err = *env.Error
		return out
	}
	if strings.HasSuffix(path, "/state") {
		_ = json.Unmarshal(env.Data, &out.state)
	} else {
		_ = json.Unmarshal(env.Data, &out.page)
	}
	return out
}

func (s *mirrorShop) feedAfter(till *mirrorTill, epoch string, after int64, limit int) feedReply {
	return s.get(till, "/api/sync/sales-mirror", url.Values{"epoch": {epoch}, "after": {fmt.Sprint(after)}, "limit": {fmt.Sprint(limit)}})
}

func (s *mirrorShop) enable() string {
	s.t.Helper()
	s.setShop(data.SalesMirrorEnabledKey, "1")
	epoch, err := salesMirrorEpoch(s.t.Context(), s.main.d)
	if err != nil {
		s.t.Fatal(err)
	}
	s.setShop(data.SalesMirrorEpochKey, epoch)
	return epoch
}

func TestSalesMirrorFeed_RefusalsInContractOrder(t *testing.T) {
	shop := newMirrorShop(t, "A")
	a := shop.tills["A"]
	if r := shop.feedAfter(a, "", 0, 10); r.status != http.StatusConflict || r.err.Code != "sales_mirror_off" {
		t.Fatalf("off: %d %+v, want 409 sales_mirror_off", r.status, r.err)
	}
	epoch := shop.enable()
	// Plain HTTP is refused before anything else (D5).
	plain := httptest.NewServer(shop.main.mux)
	defer plain.Close()
	req, _ := http.NewRequest(http.MethodGet, plain.URL+"/api/sync/sales-mirror?after=0&epoch="+epoch, nil)
	req.Header.Set("Authorization", "Bearer "+a.bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("plain HTTP: %d, want 403", resp.StatusCode)
	}
	// Unknown bearer.
	if r := shop.feedAfter(&mirrorTill{bearer: "nope"}, epoch, 0, 10); r.status != http.StatusUnauthorized {
		t.Fatalf("unknown bearer: %d", r.status)
	}
	// A satellite gets nothing (ADR-0086).
	sat := shop.addTill("D", 9)
	if _, err := shop.main.d.Db.Exec(`UPDATE tills SET role = 'satellite' WHERE id = ?`, sat.tillID); err != nil {
		t.Fatal(err)
	}
	if r := shop.feedAfter(sat, epoch, 0, 10); r.status != http.StatusForbidden || r.err.Code != "satellite" {
		t.Fatalf("satellite: %d %+v", r.status, r.err)
	}
	// The main till's own kill switch and the process kill switch.
	_ = shop.main.d.Settings.Set(t.Context(), data.SalesMirrorTillOffKey, "1")
	if r := shop.feedAfter(a, epoch, 0, 10); r.status != http.StatusConflict {
		t.Fatalf("killed on the main till: %d", r.status)
	}
	_ = shop.main.d.Settings.Set(t.Context(), data.SalesMirrorTillOffKey, "0")
	t.Setenv("UT_SALES_MIRROR", "off")
	if r := shop.feedAfter(a, epoch, 0, 10); r.status != http.StatusConflict {
		t.Fatalf("UT_SALES_MIRROR=off: %d", r.status)
	}
}

// The feed pages the log: A's own sale is left out of A's page but covered,
// B's page has it; a void reaches as the current state; a close travels; an
// entry whose sale left the sales table is "gone".
func TestSalesMirrorFeed_PagesTheLogWithOwnOriginCovered(t *testing.T) {
	shop := newMirrorShop(t, "A", "B")
	epoch := shop.enable()
	a, b := shop.tills["A"], shop.tills["B"]
	ctx := t.Context()

	// A first pull without an epoch is told to re-sync by state.
	if r := shop.feedAfter(b, "", 0, 10); r.status != http.StatusGone || r.err.Code != "horizon_passed" || r.err.Epoch != epoch {
		t.Fatalf("first pull: %d %+v", r.status, r.err)
	}
	saleA := shop.sell(a, 250)
	shop.push(a)
	saleM := shop.sell(shop.main, 400)
	repo := data.NewPOSRepo(shop.main.d.Db)
	if err := repo.UpdateSaleStatus(ctx, nil, saleM, "completed", "voided"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ArchiveReport(ctx, "eod", "2026-10-26T18:00:00Z", []byte(`{"total":650}`), "", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	head, _ := data.NewSalesMirrorRepo(shop.main.d.Db).LogHead(ctx)

	rb := shop.feedAfter(b, epoch, 0, 200)
	if rb.status != http.StatusOK {
		t.Fatalf("B's page: %d %+v", rb.status, rb.err)
	}
	p := rb.page
	if p.FromSeq != 0 || p.ToSeq != head || p.HeadSeq != head || p.More || p.FeedVersion != 1 || p.MainTillID != shop.mainID {
		t.Fatalf("B's page header = %+v (head %d)", p, head)
	}
	got := map[string]data.MirrorRecord{}
	for _, rec := range p.Records {
		if rec.Sale != nil {
			got[rec.Sale.ID] = rec
		}
		if rec.Close != nil {
			got["close"] = rec
		}
	}
	if len(p.Records) != 3 {
		t.Fatalf("B got %d records, want 3 (A's sale, M's voided sale once, the close): %+v", len(p.Records), p.Records)
	}
	if r := got[saleA]; r.Sale == nil || r.Sale.OriginTillID != a.tillID || r.Sale.OriginTillName != "Till A" || len(r.Sale.Lines) != 1 || len(r.Sale.Payments) != 1 {
		t.Fatalf("A's sale on B's page = %+v", r.Sale)
	}
	if r := got[saleM]; r.Sale == nil || r.Sale.Status != "voided" || r.Sale.OriginTillID != shop.mainID {
		t.Fatalf("M's sale on B's page = %+v", r.Sale)
	}
	// Its two entries (insert, void) collapsed to one record carrying the
	// later seq.
	var voidSeq int64
	_ = shop.main.d.Db.QueryRow(`SELECT MAX(seq) FROM sales_mirror_log WHERE ref_id = ?`, saleM).Scan(&voidSeq)
	if got[saleM].Seq != voidSeq {
		t.Fatalf("collapsed record seq = %d, want the void's %d", got[saleM].Seq, voidSeq)
	}
	if r := got["close"]; r.Close == nil || r.Close.ContentJSON != `{"total":650}` || r.Close.OriginTillID != shop.mainID {
		t.Fatalf("close on B's page = %+v", r.Close)
	}

	// A's page covers the same range without A's own sale.
	ra := shop.feedAfter(a, epoch, 0, 200)
	if ra.status != http.StatusOK || ra.page.ToSeq != head {
		t.Fatalf("A's page: %d %+v", ra.status, ra.page)
	}
	for _, rec := range ra.page.Records {
		if rec.Sale != nil && rec.Sale.ID == saleA {
			t.Fatal("A was sent its own sale")
		}
	}

	// An entry whose sale is no longer in sales (a history reset moved it
	// to the archive) is served as gone.
	if _, err := shop.main.d.Db.Exec(`INSERT INTO sales_mirror_log (op, ref_id, origin_till_id) VALUES ('sale', 'moved-away', '')`); err != nil {
		t.Fatal(err)
	}
	rb = shop.feedAfter(b, epoch, 0, 200)
	var gone bool
	for _, rec := range rb.page.Records {
		if rec.Gone && rec.ID == "moved-away" {
			gone = true
		}
	}
	if !gone {
		t.Fatalf("deleted sale not served as gone: %+v", rb.page.Records)
	}

	// Beyond the head, or a stale epoch: 410.
	if r := shop.feedAfter(b, epoch, head+5, 10); r.status != http.StatusGone {
		t.Fatalf("after > head: %d", r.status)
	}
	if r := shop.feedAfter(b, "other", 0, 10); r.status != http.StatusGone {
		t.Fatalf("stale epoch: %d", r.status)
	}
}

// Limit and the 1 MiB bound cut a page at an exact seq, with more=true.
func TestSalesMirrorFeed_PageBounds(t *testing.T) {
	shop := newMirrorShop(t, "A")
	epoch := shop.enable()
	a := shop.tills["A"]
	big := strings.Repeat("n", 20_000)
	for range 80 {
		id := shop.sell(shop.main, 100)
		if _, err := shop.main.d.Db.Exec(`UPDATE sales SET note = ? WHERE id = ?`, big, id); err != nil {
			t.Fatal(err)
		}
	}
	r := shop.feedAfter(a, epoch, 0, 3)
	// Each sale is two log entries here (insert + note update), collapsed
	// to one record per sale within a page.
	if r.status != http.StatusOK || len(r.page.Records) != 2 || r.page.ToSeq != 3 || !r.page.More {
		t.Fatalf("limit 3: %d records, to %d, more %v", len(r.page.Records), r.page.ToSeq, r.page.More)
	}
	r = shop.feedAfter(a, epoch, 0, 500)
	raw, _ := json.Marshal(r.page.Records)
	// The bound is on the sum of the encoded records; the array adds its
	// brackets and commas.
	if r.status != http.StatusOK || len(raw) > salesMirrorMaxPageBytes+len(r.page.Records)+1 || !r.page.More || r.page.ToSeq >= r.page.HeadSeq {
		t.Fatalf("byte bound: %d bytes, to %d of %d, more %v", len(raw), r.page.ToSeq, r.page.HeadSeq, r.page.More)
	}
	// The next page continues exactly where this one stopped.
	next := shop.feedAfter(a, epoch, r.page.ToSeq, 500)
	if next.status != http.StatusOK || next.page.FromSeq != r.page.ToSeq || len(next.page.Records) == 0 {
		t.Fatalf("next page: %d %+v", next.status, next.page.FromSeq)
	}
}

// A pull acknowledges its cursor (throttled), which holds the prune; a
// pruned cursor gets 410.
func TestSalesMirrorFeed_AckHoldsPruneAndHorizonAnswers410(t *testing.T) {
	shop := newMirrorShop(t, "A", "B")
	epoch := shop.enable()
	a, b := shop.tills["A"], shop.tills["B"]
	ctx := t.Context()
	for range 10 {
		shop.sell(shop.main, 100)
	}
	if r := shop.feedAfter(a, epoch, 4, 200); r.status != http.StatusOK {
		t.Fatalf("A: %d", r.status)
	}
	if r := shop.feedAfter(b, epoch, 7, 200); r.status != http.StatusOK {
		t.Fatalf("B: %d", r.status)
	}
	salesMirrorMaintenanceTick(ctx, shop.main.d, time.Now(), func(string) (diskspace.Usage, error) { return diskspace.Usage{}, diskspace.ErrUnsupported })
	m := data.NewSalesMirrorRepo(shop.main.d.Db)
	if hz, _ := m.LogHorizon(ctx); hz != 4 {
		t.Fatalf("horizon after prune = %d, want 4 (A's ack)", hz)
	}
	if r := shop.feedAfter(b, epoch, 3, 200); r.status != http.StatusGone || r.err.Horizon == nil || *r.err.Horizon != 4 {
		t.Fatalf("below the horizon: %d %+v", r.status, r.err)
	}
	// Revoking A (deleting its till row) frees the prune to B's ack.
	if err := data.NewTillsRepo(shop.main.d.Db).DeleteTill(ctx, a.tillID); err != nil {
		t.Fatal(err)
	}
	salesMirrorMaintenanceTick(ctx, shop.main.d, time.Now(), func(string) (diskspace.Usage, error) { return diskspace.Usage{}, diskspace.ErrUnsupported })
	if hz, _ := m.LogHorizon(ctx); hz != 7 {
		t.Fatalf("horizon after revoking A = %d, want 7", hz)
	}
}

func TestSalesMirrorAcks_Throttle(t *testing.T) {
	shop := newMirrorShop(t, "A")
	ctx := t.Context()
	repo := data.NewSalesMirrorRepo(shop.main.d.Db)
	acks := &salesMirrorAcks{last: map[string]salesMirrorAckMark{}}
	t0 := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	read := func() int64 {
		list, _ := repo.Acks(ctx)
		if len(list) == 0 {
			return -1
		}
		return list[0].AckedSeq
	}
	acks.record(ctx, repo, "t", "e", 10, false, t0) // first: written
	acks.record(ctx, repo, "t", "e", 20, false, t0.Add(time.Second))
	if read() != 10 {
		t.Fatalf("moved by 10 within 1 s was written: %d", read())
	}
	acks.record(ctx, repo, "t", "e", 60, false, t0.Add(2*time.Second)) // moved by 50
	if read() != 60 {
		t.Fatalf("moved by 50 not written: %d", read())
	}
	acks.record(ctx, repo, "t", "e", 61, false, t0.Add(8*time.Second)) // 5 s passed
	if read() != 61 {
		t.Fatalf("5 s later not written: %d", read())
	}
	acks.record(ctx, repo, "t", "e2", 3, false, t0.Add(9*time.Second)) // new epoch
	if read() != 3 {
		t.Fatalf("new epoch not written: %d", read())
	}
}

// The state re-sync pages sales (not the caller's own) then closes, all at
// resume_seq, and holds the log above resume_seq for the caller.
func TestSalesMirrorState_PagesSalesThenCloses(t *testing.T) {
	shop := newMirrorShop(t, "A", "B")
	epoch := shop.enable()
	a, b := shop.tills["A"], shop.tills["B"]
	ctx := t.Context()
	var want []string
	for range 5 {
		want = append(want, shop.sell(shop.main, 100))
	}
	own := shop.sell(b, 100)
	shop.push(b)
	want = append(want, shop.sell(a, 100))
	shop.push(a)
	if _, err := data.NewPOSRepo(shop.main.d.Db).ArchiveReport(ctx, "eod", "2026-10-26T18:00:00Z", []byte(`{}`), "", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	head, _ := data.NewSalesMirrorRepo(shop.main.d.Db).LogHead(ctx)
	from := time.Now().AddDate(0, 0, -400).Format("2006-01-02")
	seen := map[string]bool{}
	closes := 0
	cursor, resume := "", ""
	for i := 0; ; i++ {
		if i > 20 {
			t.Fatal("state re-sync did not end")
		}
		q := url.Values{"epoch": {epoch}, "from_date": {from}, "cursor": {cursor}, "resume_seq": {resume}, "limit": {"2"}}
		r := shop.get(b, "/api/sync/sales-mirror/state", q)
		if r.status != http.StatusOK {
			t.Fatalf("state page %d: %d %+v", i, r.status, r.err)
		}
		if r.state.ResumeSeq != head {
			t.Fatalf("resume_seq = %d, want head %d", r.state.ResumeSeq, head)
		}
		for _, rec := range r.state.Records {
			if rec.Seq != head {
				t.Fatalf("record seq %d, want resume_seq %d", rec.Seq, head)
			}
			if rec.Sale != nil {
				seen[rec.Sale.ID] = true
			}
			if rec.Close != nil {
				closes++
			}
		}
		if !r.state.More {
			break
		}
		cursor, resume = r.state.NextCursor, fmt.Sprint(r.state.ResumeSeq)
	}
	if seen[own] {
		t.Fatal("B was sent its own sale")
	}
	for _, id := range want {
		if !seen[id] {
			t.Errorf("sale %s missing from the state re-sync", id)
		}
	}
	if closes != 1 {
		t.Fatalf("closes = %d, want 1", closes)
	}
	acks, _ := data.NewSalesMirrorRepo(shop.main.d.Db).Acks(ctx)
	if len(acks) != 1 || acks[0].TillID != b.tillID || acks[0].AckedSeq != head {
		t.Fatalf("state re-sync ack = %+v, want B at %d", acks, head)
	}
	// A stale epoch restarts the re-sync.
	if r := shop.get(b, "/api/sync/sales-mirror/state", url.Values{"epoch": {"x"}, "from_date": {from}}); r.status != http.StatusGone {
		t.Fatalf("stale epoch: %d", r.status)
	}
	if r := shop.get(b, "/api/sync/sales-mirror/state", url.Values{"epoch": {epoch}, "from_date": {"26/10/2026"}}); r.status != http.StatusBadRequest {
		t.Fatalf("bad date: %d", r.status)
	}
}

// Only two feed or state requests are served at once.
func TestSalesMirrorFeed_BusyWhenTwoInFlight(t *testing.T) {
	shop := newMirrorShop(t, "A")
	epoch := shop.enable()
	shop.feed.sem <- struct{}{}
	shop.feed.sem <- struct{}{}
	r := shop.feedAfter(shop.tills["A"], epoch, 0, 10)
	<-shop.feed.sem
	<-shop.feed.sem
	if r.status != http.StatusServiceUnavailable || r.header.Get("Retry-After") != "5" {
		t.Fatalf("busy: %d Retry-After=%q", r.status, r.header.Get("Retry-After"))
	}
}

// Every enable mints a new epoch (sales made while off were never logged,
// so every copy must re-sync); disable keeps it; a replica cannot switch.
func TestSalesMirrorToggle_EnableMintsEpoch(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	shop := newMirrorShop(t, "A")
	ctx := t.Context()
	post := func(mux http.Handler, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		return rec
	}
	if rec := post(shop.main.mux, "/api/sync/sales-mirror/enable"); rec.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body)
	}
	e1, _, _ := shop.main.d.Settings.Get(ctx, data.SalesMirrorEpochKey)
	on, _, _ := shop.main.d.Settings.Get(ctx, data.SalesMirrorEnabledKey)
	if e1 == "" || on != "1" {
		t.Fatalf("after enable: on=%q epoch=%q", on, e1)
	}
	if rec := post(shop.main.mux, "/api/sync/sales-mirror/disable"); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d", rec.Code)
	}
	on, _, _ = shop.main.d.Settings.Get(ctx, data.SalesMirrorEnabledKey)
	if on != "0" {
		t.Fatalf("after disable: on=%q", on)
	}
	_ = post(shop.main.mux, "/api/sync/sales-mirror/enable")
	e2, _, _ := shop.main.d.Settings.Get(ctx, data.SalesMirrorEpochKey)
	if e2 == "" || e2 == e1 {
		t.Fatalf("re-enable kept epoch %q", e2)
	}
	replicaMux := http.NewServeMux()
	registerSyncSalesMirror(replicaMux, shop.tills["A"].d)
	if rec := post(replicaMux, "/api/sync/sales-mirror/enable"); rec.Code != http.StatusConflict {
		t.Fatalf("enable on a replica: %d", rec.Code)
	}
}

// Off, the maintenance tick touches nothing; low disk halves the caps.
func TestSalesMirrorMaintenance_OffIdleAndLowDiskHalvesCaps(t *testing.T) {
	shop := newMirrorShop(t, "A")
	ctx := t.Context()
	db := shop.main.d.Db
	if _, err := db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 100)
INSERT INTO sales_mirror_log (op, ref_id) SELECT 'sale', 'r' || i FROM n`); err != nil {
		t.Fatal(err)
	}
	salesMirrorMaintenanceTick(ctx, shop.main.d, time.Now(), func(string) (diskspace.Usage, error) {
		t.Fatal("probed the disk while the copy is off")
		return diskspace.Usage{}, nil
	})
	if n := harnessCount(t, db, `SELECT COUNT(*) FROM sales_mirror_log`); n != 100 {
		t.Fatalf("off tick pruned: %d rows left", n)
	}
	epoch := shop.enable()
	_ = data.NewSalesMirrorRepo(db).UpsertAck(ctx, shop.tills["A"].tillID, epoch, 0, time.Now())
	shop.setShop(data.SalesMirrorLogMaxRowsKey, "40")
	shop.main.d.Cfg.DBPath = "/x"
	salesMirrorMaintenanceTick(ctx, shop.main.d, time.Now(), func(string) (diskspace.Usage, error) {
		return diskspace.Usage{Total: 100 << 30, Free: 1 << 20}, nil
	})
	if n := harnessCount(t, db, `SELECT COUNT(*) FROM sales_mirror_log`); n != 20 {
		t.Fatalf("low disk: %d rows left, want 20 (cap 40 halved)", n)
	}
}

// The main till's status shows the log and each till's acknowledgement,
// and writes nothing.
func TestSalesMirrorStatus_MainTillShowsAcks(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	shop := newMirrorShop(t, "A")
	mux := http.NewServeMux()
	registerSalesMirrorStatus(mux, shop.main.d)
	get := func() SalesMirrorMainStatus {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sync/sales-mirror/status", nil))
		var env struct {
			Data struct {
				Main SalesMirrorMainStatus `json:"main"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("status: %d %s", rec.Code, rec.Body)
		}
		return env.Data.Main
	}
	if st := get(); st.State != "off" {
		t.Fatalf("state = %q, want off", st.State)
	}
	epoch := shop.enable()
	for range 4 {
		shop.sell(shop.main, 100)
	}
	shop.feedAfter(shop.tills["A"], epoch, 1, 10)
	st := get()
	if st.State != "on" || st.HeadSeq != 4 || st.LogRows != 4 || len(st.Acks) != 1 || st.Acks[0].AckedSeq != 1 || st.Acks[0].Behind != 3 {
		t.Fatalf("status = %+v", st)
	}
}

// At the head: 200, no records, more=false. A page holding only the
// caller's own sales advances the range with zero records.
func TestSalesMirrorFeed_AtHeadAndOwnOnlyPages(t *testing.T) {
	shop := newMirrorShop(t, "A")
	epoch := shop.enable()
	a := shop.tills["A"]
	for range 3 {
		shop.sell(a, 100)
	}
	shop.push(a)
	head, _ := data.NewSalesMirrorRepo(shop.main.d.Db).LogHead(t.Context())
	r := shop.feedAfter(a, epoch, 0, 200)
	if r.status != http.StatusOK || len(r.page.Records) != 0 || r.page.ToSeq != head || r.page.More {
		t.Fatalf("own-only page: %d records, to %d of %d, more %v", len(r.page.Records), r.page.ToSeq, head, r.page.More)
	}
	r = shop.feedAfter(a, epoch, head, 200)
	if r.status != http.StatusOK || len(r.page.Records) != 0 || r.page.FromSeq != head || r.page.ToSeq != head || r.page.More {
		t.Fatalf("at head: %+v", r.page)
	}
}

// Serving pages writes nothing on the main till but the caller's ack.
func TestSalesMirrorFeed_ServingWritesOnlyTheAck(t *testing.T) {
	shop := newMirrorShop(t, "A", "B")
	epoch := shop.enable()
	for range 5 {
		shop.sell(shop.main, 100)
	}
	db := shop.main.d.Db
	counts := func() map[string]int {
		rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for rows.Next() {
			var n string
			_ = rows.Scan(&n)
			names = append(names, n)
		}
		rows.Close()
		out := map[string]int{}
		for _, n := range names {
			out[n] = harnessCount(t, db, `SELECT COUNT(*) FROM "`+n+`"`)
		}
		return out
	}
	before := counts()
	shop.feedAfter(shop.tills["B"], epoch, 0, 2)
	from := time.Now().AddDate(0, 0, -400).Format("2006-01-02")
	shop.get(shop.tills["B"], "/api/sync/sales-mirror/state", url.Values{"epoch": {epoch}, "from_date": {from}})
	after := counts()
	for tb, n := range after {
		if tb == "sales_mirror_acks" || tb == "tills" || tb == "till_tls_pinned" {
			continue // the ack; tills.last_seen_at/TLS pin by syncTill (precedent)
		}
		if before[tb] != n {
			t.Errorf("serving changed %s: %d -> %d rows", tb, before[tb], n)
		}
	}
	if after["sales_mirror_acks"] != 1 {
		t.Fatalf("acks = %d, want B's", after["sales_mirror_acks"])
	}
}

// A cap prune landing between the horizon check and the log read must not
// produce a page that silently skips the pruned entries (M2 review finding
// 3): the till is sent to a state re-sync instead.
func TestSalesMirrorFeed_PruneDuringReadAnswers410(t *testing.T) {
	shop := newMirrorShop(t, "A")
	epoch := shop.enable()
	for range 10 {
		shop.sell(shop.main, 100)
	}
	shop.feed.afterRead = func() {
		if _, err := shop.main.d.Db.Exec(`DELETE FROM sales_mirror_log WHERE seq <= 6`); err != nil {
			t.Fatal(err)
		}
	}
	r := shop.feedAfter(shop.tills["A"], epoch, 3, 200)
	if r.status != http.StatusGone || r.err.Horizon == nil || *r.err.Horizon != 6 {
		t.Fatalf("prune during read: %d %+v, want 410 with horizon 6", r.status, r.err)
	}
}
