package cloudsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/cloudnotice"
	"github.com/universaltill/universal-till/internal/data"
)

// ADR-0176 (ut-docs#5366): the till's notices client against a fake cloud.

var tNow = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func noticeItem(id string, rev int, sev string) map[string]any {
	return map[string]any{
		"id": id, "rev": rev,
		"sender":   map[string]any{"kind": "platform", "name": "Universal Till"},
		"severity": sev,
		"texts": map[string]any{
			"en-gb": map[string]any{"title": "Title " + id, "body": "Body **" + id + "**"},
			"de-de": map[string]any{"title": "Titel " + id, "body": "Text " + id},
		},
		"starts_at":  tNow.Add(-time.Hour).Format(time.RFC3339),
		"expires_at": tNow.Add(48 * time.Hour).Format(time.RFC3339),
		"created_at": tNow.Add(-time.Hour).Format(time.RFC3339),
	}
}

func block(rev string, items ...map[string]any) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"rev": rev, "items": items})
	return b
}

func noticeSetup(t *testing.T, on bool) (*sql.DB, *data.CloudNoticeRepo, *data.SettingsRepo) {
	t.Helper()
	d := openMigratedDB(t, "n.db").DB
	orig, origMem := noticeNow, noticeMemAvailable
	noticeNow = func() time.Time { return tNow }
	noticeMemAvailable = func() (uint64, bool) { return 1 << 30, true }
	t.Cleanup(func() { noticeNow, noticeMemAvailable = orig, origMem })
	s := data.NewSettingsRepo(d)
	if on {
		if err := s.Set(context.Background(), cloudnotice.SettingEnabled, "1"); err != nil {
			t.Fatal(err)
		}
	}
	return d, data.NewCloudNoticeRepo(d), s
}

func lastNoticesReq(t *testing.T, c *fakeCloud) map[string]any {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.syncBodies) == 0 {
		t.Fatal("no sync body")
	}
	m, _ := c.syncBodies[len(c.syncBodies)-1]["notices"].(map[string]any)
	return m
}

func runTick(t *testing.T, srvURL string, d *sql.DB, lang string) {
	t.Helper()
	h := Hooks{NoticeLang: func() string { return lang }}
	if _, err := tick(context.Background(), testCfg(srvURL), d, h); err != nil {
		t.Fatalf("tick: %v", err)
	}
}

func TestNotices_FlagOffSendsNothingAndStoresNothing(t *testing.T) {
	d, repo, _ := noticeSetup(t, false)
	c := &fakeCloud{notices: block("r1", noticeItem("a", 1, "info"))}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()
	runTick(t, srv.URL, d, "en")
	if m := lastNoticesReq(t, c); m != nil {
		t.Fatalf("flag off must send no notices object: %v", m)
	}
	if p, _ := repo.Pairs(context.Background()); len(p) != 0 {
		t.Fatal("flag off stores nothing")
	}
}

func TestNotices_PullStoresPicksLanguageAndMirrorsTheSet(t *testing.T) {
	d, repo, _ := noticeSetup(t, true)
	ctx := context.Background()
	c := &fakeCloud{notices: block("r1", noticeItem("a", 1, "important"), noticeItem("b", 1, "info"))}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()
	runTick(t, srv.URL, d, "de")
	req := lastNoticesReq(t, c)
	if req["lang"] != "de" || req["have"] != "" {
		t.Fatalf("first request = %v", req)
	}
	got, _ := repo.List(ctx, tNow, data.NoticeAll)
	if len(got) != 2 || got[0].Title != "Titel a" || got[0].Lang != "de-de" {
		t.Fatalf("stored = %+v", got)
	}
	// Second pull: have = hash of the held set; cloud lists only b.
	c.mu.Lock()
	c.notices = block("r2", noticeItem("b", 1, "info"))
	c.mu.Unlock()
	runTick(t, srv.URL, d, "de")
	req = lastNoticesReq(t, c)
	want := cloudnotice.Have([]cloudnotice.Pair{{ID: "a", Rev: 1}, {ID: "b", Rev: 1}})
	if req["have"] != want || req["rev"] != "r1" {
		t.Fatalf("second request have/rev = %v/%v, want %v/r1", req["have"], req["rev"], want)
	}
	got, _ = repo.List(ctx, tNow, data.NoticeAll)
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("mirror must drop a: %+v", got)
	}
}

func TestNotices_UnchangedKeepsTheStore(t *testing.T) {
	d, repo, _ := noticeSetup(t, true)
	c := &fakeCloud{notices: block("r1", noticeItem("a", 1, "info"))}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()
	runTick(t, srv.URL, d, "en")
	c.mu.Lock()
	c.notices = json.RawMessage(`{"unchanged":true,"rev":"r1"}`)
	c.mu.Unlock()
	runTick(t, srv.URL, d, "en")
	if p, _ := repo.Pairs(context.Background()); len(p) != 1 {
		t.Fatalf("unchanged must keep the store, pairs=%v", p)
	}
}

func TestNotices_LanguageChangeResetsHave(t *testing.T) {
	d, _, _ := noticeSetup(t, true)
	c := &fakeCloud{notices: block("r1", noticeItem("a", 1, "info"))}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()
	runTick(t, srv.URL, d, "en")
	runTick(t, srv.URL, d, "de")
	if req := lastNoticesReq(t, c); req["have"] != "" || req["rev"] != "" {
		t.Fatalf("a language change must reset have and rev: %v", req)
	}
}

func TestNotices_BadItemsAreDroppedGoodOnesKept(t *testing.T) {
	d, repo, _ := noticeSetup(t, true)
	bad1 := noticeItem("x1", 1, "info")
	bad1["texts"] = map[string]any{"en-gb": map[string]any{"title": "<script>alert(1)</script>", "body": "b"}}
	bad2 := noticeItem("x2", 1, "info")
	bad2["link"] = map[string]any{"kind": "https", "target": "javascript:alert(1)"}
	bad3 := noticeItem("x3", 1, "critical")
	bad3["sender"] = map[string]any{"kind": "partner", "name": "Acme"}
	bad4 := noticeItem("x4", 1, "gigantic")
	c := &fakeCloud{notices: block("r1", bad1, bad2, bad3, bad4, noticeItem("ok", 1, "info"))}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()
	runTick(t, srv.URL, d, "en")
	p, _ := repo.Pairs(context.Background())
	// x2 has only a bad LINK: the notice is kept, the link dropped (ADR-0176 §6).
	if len(p) != 2 {
		t.Fatalf("held = %v, want ok and x2", p)
	}
	got, found, err := repo.Get(context.Background(), "x2")
	if err != nil || !found || got.LinkJSON != "" {
		t.Fatalf("x2 = %+v found=%v err=%v, want kept with no link", got, found, err)
	}
}

func TestNotices_GoodLinkIsStored(t *testing.T) {
	d, repo, _ := noticeSetup(t, true)
	it := noticeItem("lk", 1, "important")
	it["link"] = map[string]any{"kind": "route", "target": "/settings", "label": "Open settings"}
	c := &fakeCloud{notices: block("r1", it)}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()
	runTick(t, srv.URL, d, "en")
	got, found, err := repo.Get(context.Background(), "lk")
	if err != nil || !found || !strings.Contains(got.LinkJSON, `"/settings"`) {
		t.Fatalf("lk = %+v found=%v err=%v, want the link stored", got, found, err)
	}
}

func TestNotices_CriticalWithBadLinkStillShowsOnTheStrip(t *testing.T) {
	d, repo, _ := noticeSetup(t, true)
	crit := noticeItem("crit", 1, "critical")
	crit["link"] = map[string]any{"kind": "https", "target": "https://universaltill.com/" + strings.Repeat("a", 300)}
	c := &fakeCloud{notices: block("r1", crit)}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()
	runTick(t, srv.URL, d, "en")
	s, err := repo.Strip(context.Background(), tNow)
	if err != nil || len(s) != 1 || s[0].ID != "crit" || s[0].LinkJSON != "" {
		t.Fatalf("strip = %+v err=%v, want the critical notice without a link", s, err)
	}
}

func TestNotices_MalformedBlockChangesNothing(t *testing.T) {
	d, repo, _ := noticeSetup(t, true)
	c := &fakeCloud{notices: block("r1", noticeItem("a", 1, "info"))}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()
	runTick(t, srv.URL, d, "en")
	c.mu.Lock()
	c.notices = json.RawMessage(`{"items": "not a list"}`)
	c.mu.Unlock()
	runTick(t, srv.URL, d, "en") // must not fail the tick
	if p, _ := repo.Pairs(context.Background()); len(p) != 1 {
		t.Fatalf("a malformed block must leave the store: %v", p)
	}
}

func TestNotices_ItemCapIsTwenty_KeepsTheMostSevere(t *testing.T) {
	d, repo, _ := noticeSetup(t, true)
	// 30 items, info first; the one critical and one important sit LAST,
	// where a cloud that forgot its own ordering (or a hostile one) would
	// put them. The cap keeps the 20 most severe, never the first 20.
	var items []map[string]any
	for i := 0; i < 28; i++ {
		items = append(items, noticeItem(fmt.Sprintf("n%02d", i), 1, "info"))
	}
	items = append(items, noticeItem("imp", 1, "important"), noticeItem("crit", 1, "critical"))
	b, _ := json.Marshal(map[string]any{"rev": "r1", "more": true, "items": items})
	c := &fakeCloud{notices: b}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()
	runTick(t, srv.URL, d, "en")
	p, _ := repo.Pairs(context.Background())
	if len(p) != cloudnotice.MaxItems {
		t.Fatalf("held %d, want cap %d", len(p), cloudnotice.MaxItems)
	}
	held := map[string]bool{}
	for _, x := range p {
		held[x.ID] = true
	}
	if !held["crit"] || !held["imp"] {
		t.Fatalf("the cap dropped a critical/important at position 29/30: held %v", held)
	}
}

func TestNotices_OversizedItemIsDroppedAlone(t *testing.T) {
	d, repo, _ := noticeSetup(t, true)
	big := noticeItem("big", 1, "info")
	texts := map[string]any{}
	for i := 0; i < 40; i++ { // 40 languages x ~150 bytes > the 4 KiB item cap
		texts[fmt.Sprintf("x%02d-xx", i)] = map[string]any{"title": "t", "body": strings.Repeat("b", 120)}
	}
	texts["en-gb"] = map[string]any{"title": "t", "body": "b"}
	big["texts"] = texts
	c := &fakeCloud{notices: block("r1", big, noticeItem("ok", 1, "info"))}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()
	runTick(t, srv.URL, d, "en")
	p, _ := repo.Pairs(context.Background())
	if len(p) != 1 || p[0].ID != "ok" {
		t.Fatalf("held = %v, want only ok (the oversized item costs only itself)", p)
	}
}

func TestNotices_MarkMadeWhileThePostIsInFlightIsStillReported(t *testing.T) {
	d, repo, _ := noticeSetup(t, true)
	ctx := context.Background()
	// The clock moves one minute per read: attach (collecting acks) sees an
	// earlier time than apply (stamping reported_at).
	calls := 0
	noticeNow = func() time.Time { calls++; return tNow.Add(time.Duration(calls) * time.Minute) }
	c := &fakeCloud{notices: block("r1", noticeItem("a", 1, "info"))}
	// While the sync request is being answered, the operator opens the notice.
	c.noticesFn = func(req map[string]any) json.RawMessage {
		if req != nil && req["acks"] != nil {
			_ = repo.MarkRead(ctx, "a", tNow.Add(time.Duration(calls)*time.Minute+30*time.Second))
		}
		return block("r1", noticeItem("a", 1, "info"))
	}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()
	runTick(t, srv.URL, d, "en") // stores a
	runTick(t, srv.URL, d, "en") // sends delivered_at; the read happens mid-flight
	runTick(t, srv.URL, d, "en") // the read must go up now
	req := lastNoticesReq(t, c)
	acks, _ := req["acks"].([]any)
	if len(acks) != 1 || acks[0].(map[string]any)["read_at"] == nil {
		t.Fatalf("a read marked while the previous POST was in flight must still be reported: %v", req["acks"])
	}
}

func TestNotices_AcksAreSentOnceThenReported(t *testing.T) {
	d, repo, _ := noticeSetup(t, true)
	ctx := context.Background()
	c := &fakeCloud{notices: block("r1", noticeItem("a", 1, "info"))}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()
	runTick(t, srv.URL, d, "en")
	_ = repo.MarkRead(ctx, "a", tNow.Add(time.Minute))
	noticeNow = func() time.Time { return tNow.Add(2 * time.Minute) } // the clock moves on
	runTick(t, srv.URL, d, "en")
	req := lastNoticesReq(t, c)
	acks, _ := req["acks"].([]any)
	if len(acks) != 1 {
		t.Fatalf("acks = %v", req["acks"])
	}
	a := acks[0].(map[string]any)
	if a["id"] != "a" || a["read_at"] == nil || a["delivered_at"] == nil {
		t.Fatalf("ack = %v (first report carries delivered_at and read_at)", a)
	}
	runTick(t, srv.URL, d, "en")
	if req = lastNoticesReq(t, c); req["acks"] != nil {
		t.Fatalf("a reported ack must not repeat: %v", req["acks"])
	}
}

func TestNotices_LowMemorySkipsThePull(t *testing.T) {
	d, repo, _ := noticeSetup(t, true)
	c := &fakeCloud{notices: block("r1", noticeItem("a", 1, "info"))}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()
	runTick(t, srv.URL, d, "en")
	noticeMemAvailable = func() (uint64, bool) { return 10 << 20, true }
	runTick(t, srv.URL, d, "en")
	if m := lastNoticesReq(t, c); m != nil {
		t.Fatalf("below the memory floor no notices object is sent: %v", m)
	}
	if p, _ := repo.Pairs(context.Background()); len(p) != 1 {
		t.Fatal("the held set stays (have kept)")
	}
}

func TestNotices_CheckinGetCarriesRevAndAckForcesPost(t *testing.T) {
	fakeClock(t)
	d, repo, _ := noticeSetup(t, true)
	cloud, srv := newCheckinCloud(3)
	defer srv.Close()
	cloud.fakeCloud.notices = block("r7", noticeItem("a", 1, "info"))
	mustTick(t, srv.URL, d, Hooks{}) // 200 -> POST, stores a
	mustTick(t, srv.URL, d, Hooks{}) // 304, but the delivered ack is waiting -> POST
	if cloud.posts() != 2 {
		t.Fatalf("posts = %d, want 2 (the delivered ack goes up)", cloud.posts())
	}
	mustTick(t, srv.URL, d, Hooks{}) // 304, nothing to report -> skipped
	if cloud.posts() != 2 {
		t.Fatalf("posts = %d, want 2 (304 skips)", cloud.posts())
	}
	_ = repo.MarkRead(context.Background(), "a", tNow.Add(time.Minute))
	noticeNow = func() time.Time { return tNow.Add(2 * time.Minute) }
	mustTick(t, srv.URL, d, Hooks{})
	if cloud.posts() != 3 {
		t.Fatalf("a waiting read ack must force the POST on a 304: posts = %d", cloud.posts())
	}
	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	last := cloud.queries[len(cloud.queries)-1]
	if !strings.Contains(last, "notices=r7") {
		t.Fatalf("GET query = %q, want notices=r7", last)
	}
}

func TestNotices_FlagOffAddsNoGetParameter(t *testing.T) {
	fakeClock(t)
	d, _, _ := noticeSetup(t, false)
	cloud, srv := newCheckinCloud(3)
	defer srv.Close()
	mustTick(t, srv.URL, d, Hooks{})
	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	if strings.Contains(cloud.queries[0], "notices") {
		t.Fatalf("flag off: GET query = %q", cloud.queries[0])
	}
}
