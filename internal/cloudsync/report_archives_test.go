package cloudsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#574 (ADR-0040 card 4, ADR-0147 §2/§3): the main till uploads its
// un-acked report_archive rows to POST /v1/stores/report-archives.

// archiveCloud answers each POST with the next status from statuses (the
// last one repeats; empty = 200) and records the decoded body.
type archiveCloud struct {
	mu       sync.Mutex
	statuses []int
	posts    []map[string]any
	raws     [][]byte
}

func (c *archiveCloud) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		c.mu.Lock()
		c.posts = append(c.posts, body)
		c.raws = append(c.raws, raw)
		status := http.StatusOK
		if len(c.statuses) > 0 {
			status = c.statuses[0]
			if len(c.statuses) > 1 {
				c.statuses = c.statuses[1:]
			}
		}
		c.mu.Unlock()
		if r.URL.Path != reportArchivePath {
			status = http.StatusNotFound
		}
		w.WriteHeader(status)
		if status == http.StatusPaymentRequired {
			_, _ = w.Write([]byte(`{"data":null,"error":{"code":"subscription_inactive","message":"x"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"ok":true},"error":null}`))
	})
}

func (c *archiveCloud) setStatuses(s ...int) { c.mu.Lock(); c.statuses = s; c.mu.Unlock() }
func (c *archiveCloud) count() int           { c.mu.Lock(); defer c.mu.Unlock(); return len(c.posts) }
func (c *archiveCloud) post(i int) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.posts[i]
}

func noArchiveThrottle(t *testing.T) {
	t.Helper()
	prev := reportArchiveIntervalNS.Load()
	reportArchiveIntervalNS.Store(0)
	reportArchiveLastNS.Store(0)
	t.Cleanup(func() {
		reportArchiveIntervalNS.Store(prev)
		reportArchiveLastNS.Store(0)
	})
}

type archiveFixture struct {
	db     *sql.DB
	repo   *data.POSRepo
	ids    []string
	closes []time.Time
}

// seedArchiveFixture archives n eod closes on consecutive days (oldest
// first) and sets the retention mode.
func seedArchiveFixture(t *testing.T, name, mode string, n int) archiveFixture {
	t.Helper()
	d := openMigratedDB(t, name)
	ctx := context.Background()
	repo := data.NewPOSRepo(d.DB)
	f := archiveFixture{db: d.DB, repo: repo}
	base := time.Date(2026, 9, 1, 21, 15, 0, 0, time.FixedZone("CEST", 2*3600))
	for i := range n {
		at := base.AddDate(0, 0, i)
		period := at.Format(time.RFC3339)
		if _, err := repo.ArchiveReport(ctx, "eod", period, []byte(fmt.Sprintf(`{"z":%d}`, i+1)), "", "", at); err != nil {
			t.Fatal(err)
		}
		row, ok, err := repo.GetArchivedReport(ctx, "eod", period)
		if err != nil || !ok {
			t.Fatalf("read back %s: %v", period, err)
		}
		f.ids = append(f.ids, row.ID)
		f.closes = append(f.closes, at)
	}
	if mode != "" {
		if err := data.NewSettingsRepo(d.DB).Set(ctx, data.ReportRetentionModeKey, mode); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func unacked(t *testing.T, f archiveFixture) int {
	t.Helper()
	n, err := f.repo.CountUnackedReportArchives(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func refusedAt(t *testing.T, db *sql.DB) string {
	t.Helper()
	v, _, err := data.NewSettingsRepo(db).Get(context.Background(), data.ReportArchiveCloudRefusedAtKey)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestReportArchiveWirePeriod(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"2026-09-30T23:15:00+02:00", "2026-09-30T23-15-00p02-00", true},
		{"2026-09-30T23:15:00Z", "2026-09-30T23-15-00Z", true},
		{"2026-09-30T23:15:00.5-05:00", "2026-09-30T23-15-00_5-05-00", true},
		{"2026-09-30", "2026-09-30", true}, // pre-ADR-0066 period unchanged
		{"2026-08", "2026-08", true},
		{"", "", false},
		{"2026 09 30", "", false},            // space is not in the identifier alphabet
		{"2026/09/30", "", false},            // nor is a slash
		{strings.Repeat("9", 65), "", false}, // > 64 chars
		{strings.Repeat("9", 64), strings.Repeat("9", 64), true},
	}
	for _, c := range cases {
		got, ok := reportArchiveWirePeriod(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("reportArchiveWirePeriod(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
	// One-to-one on RFC3339: two closes on one day never collide.
	a, _ := reportArchiveWirePeriod("2026-09-30T08:00:00+02:00")
	b, _ := reportArchiveWirePeriod("2026-09-30T20:00:00+02:00")
	if a == b {
		t.Fatalf("two closes on one day collided: %q", a)
	}
}

func TestPushReportArchives_TillModeNeverPostsAndClearsRefusal(t *testing.T) {
	for _, mode := range []string{"", "till", "bogus"} {
		t.Run("mode="+mode, func(t *testing.T) {
			noArchiveThrottle(t)
			cloud := &archiveCloud{}
			srv := httptest.NewServer(cloud.handler())
			defer srv.Close()
			f := seedArchiveFixture(t, "ra-till.db", mode, 2)
			if err := data.NewSettingsRepo(f.db).Set(context.Background(), data.ReportArchiveCloudRefusedAtKey, "2026-10-01T00:00:00Z"); err != nil {
				t.Fatal(err)
			}
			pushReportArchives(context.Background(), testCfg(srv.URL), f.db)
			if cloud.count() != 0 {
				t.Fatalf("mode %q posted %d report archives", mode, cloud.count())
			}
			if v := refusedAt(t, f.db); v != "" {
				t.Fatalf("mode %q left the refusal state %q", mode, v)
			}
		})
	}
}

func TestPushReportArchives_AcksOn200OldestFirst(t *testing.T) {
	for _, mode := range []string{"cloud", "both"} {
		t.Run(mode, func(t *testing.T) {
			noArchiveThrottle(t)
			cloud := &archiveCloud{}
			srv := httptest.NewServer(cloud.handler())
			defer srv.Close()
			f := seedArchiveFixture(t, "ra-ok.db", mode, 2)

			pushReportArchives(context.Background(), testCfg(srv.URL), f.db)

			if cloud.count() != 2 {
				t.Fatalf("posts = %d, want 2", cloud.count())
			}
			first := cloud.post(0)
			if first["store_id"] != "store-1" || first["kind"] != "eod" {
				t.Fatalf("body = %v", first)
			}
			if first["period"] != "2026-09-01T21-15-00p02-00" {
				t.Fatalf("period = %v, want the wire form of the oldest close", first["period"])
			}
			content, ok := first["content"].(map[string]any)
			if !ok || content["z"] != float64(1) {
				t.Fatalf("content = %#v, want the raw JSON object of content_json", first["content"])
			}
			if unacked(t, f) != 0 {
				t.Fatalf("un-acked after 200 = %d, want 0", unacked(t, f))
			}
			// The till's own row keeps its original period.
			if has, _ := f.repo.HasArchivedReport(context.Background(), "eod", f.closes[0].Format(time.RFC3339)); !has {
				t.Fatal("local period was rewritten")
			}
			// Nothing left: the next round sends nothing.
			pushReportArchives(context.Background(), testCfg(srv.URL), f.db)
			if cloud.count() != 2 {
				t.Fatalf("acked rows re-sent: %d posts", cloud.count())
			}
		})
	}
}

func TestPushReportArchives_402RecordsRefusalUntilNext200(t *testing.T) {
	noArchiveThrottle(t)
	cloud := &archiveCloud{statuses: []int{http.StatusPaymentRequired}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	f := seedArchiveFixture(t, "ra-402.db", "cloud", 3)
	ctx := context.Background()

	pushReportArchives(ctx, testCfg(srv.URL), f.db)
	if cloud.count() != 1 {
		t.Fatalf("402 must stop the round: %d posts", cloud.count())
	}
	if unacked(t, f) != 3 {
		t.Fatalf("402 acked something: %d un-acked", unacked(t, f))
	}
	v := refusedAt(t, f.db)
	if _, err := time.Parse(time.RFC3339, v); err != nil {
		t.Fatalf("refusal state = %q, want an RFC3339 time: %v", v, err)
	}

	cloud.setStatuses(http.StatusOK)
	pushReportArchives(ctx, testCfg(srv.URL), f.db)
	if unacked(t, f) != 0 {
		t.Fatalf("after 200: %d un-acked", unacked(t, f))
	}
	if v := refusedAt(t, f.db); v != "" {
		t.Fatalf("a 200 left the refusal state %q", v)
	}
}

func TestPushReportArchives_RejectedRowSkippedNextAcked(t *testing.T) {
	noArchiveThrottle(t)
	cloud := &archiveCloud{statuses: []int{http.StatusBadRequest, http.StatusOK}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	f := seedArchiveFixture(t, "ra-400.db", "both", 2)

	pushReportArchives(context.Background(), testCfg(srv.URL), f.db)
	if cloud.count() != 2 {
		t.Fatalf("a 400 must skip that row and try the next: %d posts", cloud.count())
	}
	rows, err := f.repo.ListUnackedReportArchives(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != f.ids[0] {
		t.Fatalf("un-acked = %+v, want only the rejected oldest row", rows)
	}
}

func TestPushReportArchives_ServerErrorStopsRound(t *testing.T) {
	noArchiveThrottle(t)
	cloud := &archiveCloud{statuses: []int{http.StatusInternalServerError}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	f := seedArchiveFixture(t, "ra-500.db", "cloud", 3)
	pushReportArchives(context.Background(), testCfg(srv.URL), f.db)
	if cloud.count() != 1 {
		t.Fatalf("500 must stop the round: %d posts", cloud.count())
	}
	if unacked(t, f) != 3 || refusedAt(t, f.db) != "" {
		t.Fatalf("500: un-acked=%d refusal=%q, want 3 and none", unacked(t, f), refusedAt(t, f.db))
	}
}

// A period that still fails the identifier check after mapping is never
// sent: logged, left local and un-acked, and it does not hold later rows up.
func TestPushReportArchives_InvalidWirePeriodSkipped(t *testing.T) {
	noArchiveThrottle(t)
	cloud := &archiveCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	f := seedArchiveFixture(t, "ra-bad.db", "cloud", 1)
	if _, err := f.db.Exec(`UPDATE report_archive SET period = '2026 09 01' WHERE id = ?`, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.ArchiveReport(context.Background(), "monthly", "2026-09", []byte(`{}`), "", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	pushReportArchives(context.Background(), testCfg(srv.URL), f.db)
	if cloud.count() != 1 || cloud.post(0)["kind"] != "monthly" {
		t.Fatalf("posts = %d, want only the valid monthly row", cloud.count())
	}
	rows, _ := f.repo.ListUnackedReportArchives(context.Background(), 10)
	if len(rows) != 1 || rows[0].ID != f.ids[0] {
		t.Fatalf("un-acked = %+v, want the invalid-period row kept", rows)
	}
}

func TestPushReportArchives_BatchBounded(t *testing.T) {
	noArchiveThrottle(t)
	cloud := &archiveCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	f := seedArchiveFixture(t, "ra-batch.db", "cloud", reportArchiveBatch+1)
	pushReportArchives(context.Background(), testCfg(srv.URL), f.db)
	if cloud.count() != reportArchiveBatch {
		t.Fatalf("one round sent %d, want the batch bound %d", cloud.count(), reportArchiveBatch)
	}
	if unacked(t, f) != 1 {
		t.Fatalf("un-acked = %d, want 1 left for the next round", unacked(t, f))
	}
}

func TestPushReportArchives_Throttled(t *testing.T) {
	noArchiveThrottle(t)
	reportArchiveIntervalNS.Store(int64(10 * time.Minute))
	cloud := &archiveCloud{statuses: []int{http.StatusInternalServerError, http.StatusOK}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	f := seedArchiveFixture(t, "ra-throttle.db", "cloud", 2)
	pushReportArchives(context.Background(), testCfg(srv.URL), f.db)
	pushReportArchives(context.Background(), testCfg(srv.URL), f.db)
	if cloud.count() != 1 {
		t.Fatalf("a round inside the throttle window posted: %d posts", cloud.count())
	}
}

// Tick gate: only a registered main till uploads (ADR-0147 §2).
func TestTickReportArchives_MainTillOnly(t *testing.T) {
	t.Run("main till uploads", func(t *testing.T) {
		noArchiveThrottle(t)
		noAggThrottle(t)
		cloud := &fakeCloud{}
		srv := httptest.NewServer(cloud.handler())
		defer srv.Close()
		f := seedArchiveFixture(t, "ra-tick-main.db", "cloud", 2)
		if err := Tick(context.Background(), testCfg(srv.URL), f.db, Hooks{}); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if len(cloud.reportArchives) != 2 {
			t.Fatalf("report archives = %d, want 2", len(cloud.reportArchives))
		}
	})
	t.Run("replica never uploads", func(t *testing.T) {
		noArchiveThrottle(t)
		noAggThrottle(t)
		cloud := &fakeCloud{}
		srv := httptest.NewServer(cloud.handler())
		defer srv.Close()
		f := seedArchiveFixture(t, "ra-tick-replica.db", "cloud", 2)
		if err := data.NewSettingsRepo(f.db).Set(context.Background(), "sync.primary_url", "http://10.0.0.2:8080"); err != nil {
			t.Fatal(err)
		}
		if err := Tick(context.Background(), testCfg(srv.URL), f.db, Hooks{}); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if len(cloud.reportArchives) != 0 {
			t.Fatalf("replica uploaded %d report archives", len(cloud.reportArchives))
		}
		if unacked(t, f) != 2 {
			t.Fatalf("replica acked rows: %d un-acked", unacked(t, f))
		}
	})
}

// Review finding 2: a row whose content_json is not valid JSON (or empty)
// can't be sent as the raw content value — it is skipped and stays local
// and un-acked, and the next row still uploads.
func TestPushReportArchives_InvalidContentSkipped(t *testing.T) {
	for _, bad := range []string{`{not json`, ``} {
		t.Run(fmt.Sprintf("%q", bad), func(t *testing.T) {
			noArchiveThrottle(t)
			cloud := &archiveCloud{}
			srv := httptest.NewServer(cloud.handler())
			defer srv.Close()
			f := seedArchiveFixture(t, "ra-badjson.db", "cloud", 2)
			if _, err := f.db.Exec(`UPDATE report_archive SET content_json = ? WHERE id = ?`, bad, f.ids[0]); err != nil {
				t.Fatal(err)
			}
			pushReportArchives(context.Background(), testCfg(srv.URL), f.db)
			if cloud.count() != 1 {
				t.Fatalf("posts = %d, want 1 (only the valid row)", cloud.count())
			}
			rows, _ := f.repo.ListUnackedReportArchives(context.Background(), 10)
			if len(rows) != 1 || rows[0].ID != f.ids[0] {
				t.Fatalf("un-acked = %+v, want only the invalid-content row kept", rows)
			}
		})
	}
}
