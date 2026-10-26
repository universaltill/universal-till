package pages

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// Additional tills' own closes reach the main till (ut-docs#5074 M7).

func closeOn(t *testing.T, till *mirrorTill, period string, at time.Time) string {
	t.Helper()
	if _, err := data.NewPOSRepo(till.d.Db).ArchiveReport(t.Context(), "eod", period, []byte(`{"till":"`+till.name+`"}`), "", "", at); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := till.d.Db.QueryRow(`SELECT id FROM report_archive WHERE period = ?`, period).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSalesMirrorCloses_PushStoresAsPeerCloseAndLogs(t *testing.T) {
	shop := newMirrorShop(t, "B")
	b := shop.tills["B"]
	ctx := t.Context()
	mdb := shop.main.d.Db
	// Off: the push step sends nothing; nothing is stored or logged.
	closeOn(t, b, "p-off", time.Now().Add(-48*time.Hour))
	shop.pushCloses(b)
	if n := harnessCount(t, mdb, `SELECT COUNT(*) FROM peer_closes`); n != 0 {
		t.Fatalf("closes pushed with the copy off: %d", n)
	}
	shop.enable()
	// The main till's own close that B inherited in its snapshot (same id)
	// is never stored as B's.
	if _, err := mdb.Exec(`INSERT INTO report_archive (id, kind, period, content_json, created_at) VALUES ('main-own', 'eod', 'm1', '{}', datetime('now', '-3 hours'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := b.d.Db.Exec(`INSERT INTO report_archive (id, kind, period, content_json, created_at) VALUES ('main-own', 'eod', 'm1', '{}', datetime('now', '-3 hours'))`); err != nil {
		t.Fatal(err)
	}
	own := closeOn(t, b, "p-on", time.Now().Add(-24*time.Hour))
	shop.pushCloses(b)
	shop.pushCloses(b) // idempotent
	if n := harnessCount(t, mdb, `SELECT COUNT(*) FROM peer_closes WHERE till_id = ? AND id = ?`, b.tillID, own); n != 1 {
		t.Fatalf("B's own close stored %d times on the main till", n)
	}
	if n := harnessCount(t, mdb, `SELECT COUNT(*) FROM peer_closes WHERE id = 'main-own'`); n != 0 {
		t.Fatal("the main till's own close was stored as B's")
	}
	if n := harnessCount(t, mdb, `SELECT COUNT(*) FROM sales_mirror_log WHERE op = 'close' AND ref_id = ? AND origin_till_id = ?`, own, b.tillID); n != 1 {
		t.Fatalf("B's close logged %d times, want 1", n)
	}
	// The main till's own Z chain is untouched.
	if n := harnessCount(t, mdb, `SELECT COUNT(*) FROM report_archive`); n != 1 {
		t.Fatalf("main till report_archive has %d rows, want only its own", n)
	}
	cur, _, _ := b.d.Settings.Get(ctx, salesMirrorClosePushCursorKey)
	if cur == "" {
		t.Fatal("close cursor not advanced")
	}
}

func TestSalesMirrorCloses_RouteRefusals(t *testing.T) {
	shop := newMirrorShop(t, "B")
	b := shop.tills["B"]
	post := func(c *http.Client, url string) int {
		req, _ := http.NewRequest(http.MethodPost, url+"/api/sync/closes", bytes.NewReader([]byte(`[]`)))
		req.Header.Set("Authorization", "Bearer "+b.bearer)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(shop.client, shop.server.URL); code != http.StatusConflict {
		t.Fatalf("off: %d, want 409", code)
	}
	shop.enable()
	plain := httptest.NewServer(shop.main.mux)
	defer plain.Close()
	if code := post(http.DefaultClient, plain.URL); code != http.StatusForbidden {
		t.Fatalf("plain HTTP: %d, want 403", code)
	}
	if code := post(shop.client, shop.server.URL); code != http.StatusOK {
		t.Fatalf("on, TLS, empty batch: %d", code)
	}
}

// D9: B's own close reaches A's copy through the main till (feed and state
// re-sync), under B as origin; B never gets its own close back; the main
// till's report_archive and A's report_archive are untouched.
func TestSalesMirrorCloses_RelayedToEveryOtherTill(t *testing.T) {
	shop := newMirrorShop(t, "A", "B", "C")
	shop.enable()
	a, b, c := shop.tills["A"], shop.tills["B"], shop.tills["C"]
	shop.pull(a)
	shop.pull(b)
	zB := closeOn(t, b, "b-day", time.Now().Add(-24*time.Hour))
	shop.pushCloses(b)
	closeOn(t, shop.main, "m-day", time.Now().Add(-48*time.Hour))
	shop.pull(a) // by the feed
	shop.pull(b)
	shop.pull(c) // first pull: by the state re-sync
	for _, till := range []*mirrorTill{a, c} {
		var origin, content string
		if err := till.d.Db.QueryRow(`SELECT origin_till_id, content_json FROM mirror_closes WHERE src_id = ?`, zB).Scan(&origin, &content); err != nil {
			t.Fatalf("%s has no copy of B's close: %v", till.name, err)
		}
		if origin != b.tillID || content != `{"till":"B"}` {
			t.Fatalf("%s's copy of B's close: origin %q content %s", till.name, origin, content)
		}
		if n := harnessCount(t, till.d.Db, `SELECT COUNT(*) FROM mirror_closes`); n != 2 {
			t.Fatalf("%s holds %d closes, want B's and M's", till.name, n)
		}
		if n := harnessCount(t, till.d.Db, `SELECT COUNT(*) FROM report_archive`); n != 0 {
			t.Fatalf("%s's own Z chain gained %d rows", till.name, n)
		}
	}
	if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM mirror_closes WHERE origin_till_id = ?`, b.tillID); n != 0 {
		t.Fatal("B was sent its own close")
	}
	if n := harnessCount(t, shop.main.d.Db, `SELECT COUNT(*) FROM report_archive`); n != 1 {
		t.Fatalf("main till report_archive = %d rows, want only its own", n)
	}
}

// The route refuses malformed and oversized batches, and satellites.
func TestSalesMirrorCloses_RouteValidates(t *testing.T) {
	shop := newMirrorShop(t, "B")
	shop.enable()
	b := shop.tills["B"]
	post := func(body string) int {
		req, _ := http.NewRequest(http.MethodPost, shop.server.URL+"/api/sync/closes", bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", "Bearer "+b.bearer)
		resp, err := shop.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	good := `{"id":"c1","kind":"eod","period":"p1","content_json":"{}","created_at":"2026-10-26 18:00:00"}`
	many := "[" + good
	for range 20 {
		many += "," + good
	}
	many += "]"
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"21 closes":       {many, http.StatusBadRequest},
		"invalid content": {`[{"id":"c1","kind":"eod","period":"p1","content_json":"{nope","created_at":"2026-10-26 18:00:00"}]`, http.StatusBadRequest},
		"bad created_at":  {`[{"id":"c1","kind":"eod","period":"p1","content_json":"{}","created_at":"yesterday"}]`, http.StatusBadRequest},
		"padded id":       {`[{"id":" c1","kind":"eod","period":"p1","content_json":"{}","created_at":"2026-10-26 18:00:00"}]`, http.StatusBadRequest},
		"not an array":    {`{}`, http.StatusBadRequest},
		"one good close":  {"[" + good + "]", http.StatusOK},
	} {
		if got := post(tc.body); got != tc.want {
			t.Errorf("%s: %d, want %d", name, got, tc.want)
		}
	}
	if _, err := shop.main.d.Db.Exec(`UPDATE tills SET role = 'satellite' WHERE id = ?`, b.tillID); err != nil {
		t.Fatal(err)
	}
	if got := post("[" + good + "]"); got != http.StatusForbidden {
		t.Fatalf("satellite: %d, want 403", got)
	}
}

// A till joined before ut-docs#5074 has no push cursor: it pushes its whole
// report_archive, and the main till's own closes in it (inherited at join)
// are never stored as that till's; a malformed own close is skipped without
// stopping the next one.
func TestSalesMirrorCloses_NoCursorAndMalformedRowDoNotWedge(t *testing.T) {
	shop := newMirrorShop(t, "B")
	shop.enable()
	b := shop.tills["B"]
	mdb := shop.main.d.Db
	if _, err := mdb.Exec(`INSERT INTO report_archive (id, kind, period, content_json, created_at) VALUES ('main-z', 'eod', 'm', '{}', '2026-01-01 10:00:00')`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO report_archive (id, kind, period, content_json, created_at) VALUES ('main-z', 'eod', 'm', '{}', '2026-01-01 10:00:00')`,
		`INSERT INTO report_archive (id, kind, period, content_json, created_at) VALUES ('bad-z', 'eod', 'b1', '{broken', '2026-01-02 10:00:00')`,
		`INSERT INTO report_archive (id, kind, period, content_json, created_at) VALUES ('own-z', 'eod', 'b2', '{}', '2026-01-03 10:00:00')`,
	} {
		if _, err := b.d.Db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	shop.pushCloses(b)
	if n := harnessCount(t, mdb, `SELECT COUNT(*) FROM peer_closes WHERE till_id = ?`, b.tillID); n != 1 {
		t.Fatalf("peer closes from B = %d, want only its own good one", n)
	}
	if n := harnessCount(t, mdb, `SELECT COUNT(*) FROM peer_closes WHERE id = 'own-z'`); n != 1 {
		t.Fatal("B's own close did not get through after the malformed one")
	}
}

// A satellite pushes no closes.
func TestSalesMirrorCloses_SatelliteSendsNothing(t *testing.T) {
	shop := newMirrorShop(t, "B")
	shop.enable()
	b := shop.tills["B"]
	_ = b.d.Settings.Set(t.Context(), "sync.till_role", "satellite")
	closeOn(t, b, "s-day", time.Now().Add(-24*time.Hour))
	before := shop.copyRequests.Load()
	shop.pushCloses(b)
	if n := harnessCount(t, shop.main.d.Db, `SELECT COUNT(*) FROM peer_closes`); n != 0 {
		t.Fatalf("a satellite pushed %d closes", n)
	}
	_ = before
}

// The state re-sync pages other tills' closes across page boundaries, after
// the main till's own, and a peer close missing on the main till is "gone".
func TestSalesMirrorCloses_StatePagesPeerClosesAndGone(t *testing.T) {
	shop := newMirrorShop(t, "A", "B")
	epoch := shop.enable()
	a, b := shop.tills["A"], shop.tills["B"]
	for i := range 5 {
		closeOn(t, b, fmt.Sprintf("b-%d", i), time.Now().Add(-time.Duration(24*(i+1))*time.Hour))
	}
	for range 3 {
		shop.pushCloses(b)
	}
	from := time.Now().AddDate(0, 0, -400).Format("2006-01-02")
	cursor, resume := "", ""
	got := 0
	for i := 0; i < 20; i++ {
		r := shop.get(a, "/api/sync/sales-mirror/state", url.Values{"epoch": {epoch}, "from_date": {from}, "cursor": {cursor}, "resume_seq": {resume}, "limit": {"2"}})
		if r.status != http.StatusOK {
			t.Fatalf("page %d: %d", i, r.status)
		}
		for _, rec := range r.state.Records {
			if rec.Close != nil && rec.Close.OriginTillID == b.tillID {
				got++
			}
		}
		if !r.state.More {
			break
		}
		cursor, resume = r.state.NextCursor, fmt.Sprint(r.state.ResumeSeq)
	}
	if got != 5 {
		t.Fatalf("A got %d of B's 5 closes through the state re-sync", got)
	}
	// Gone: a logged peer close the main till no longer holds.
	if _, err := shop.main.d.Db.Exec(`INSERT INTO sales_mirror_log (op, ref_id, origin_till_id) VALUES ('close', 'vanished', ?)`, b.tillID); err != nil {
		t.Fatal(err)
	}
	head, _ := data.NewSalesMirrorRepo(shop.main.d.Db).LogHead(t.Context())
	r := shop.feedAfter(a, epoch, head-1, 10)
	if r.status != http.StatusOK || len(r.page.Records) != 1 || !r.page.Records[0].Gone {
		t.Fatalf("vanished peer close: %d %+v", r.status, r.page.Records)
	}
}
