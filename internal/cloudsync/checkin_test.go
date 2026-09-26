package cloudsync

import (
	"context"
	"database/sql"
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
	"github.com/universaltill/universal-till/internal/entitlement"
)

// ADR-0117 §3 (ut-docs#2827): the conditional check-in. Before the full
// /v1/stores/sync POST the till asks GET /v1/stores/checkin with
// If-None-Match "<store_id>:<link_version>"; a 304 skips the POST unless
// the till's own state changed or the 10-minute floor passed.

// checkinCloud is fakeCloud plus the conditional-read endpoint.
type checkinCloud struct {
	*fakeCloud
	mu      sync.Mutex
	version int64
	status  int      // non-zero: answer every GET with this status (401, 404 …)
	inm     []string // If-None-Match of every GET, in order
}

func newCheckinCloud(version int64) (*checkinCloud, *httptest.Server) {
	c := &checkinCloud{fakeCloud: &fakeCloud{}, version: version}
	inner := c.fakeCloud.handler()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/stores/checkin", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.inm = append(c.inm, r.Header.Get("If-None-Match"))
		if r.Method != http.MethodGet || r.URL.Query().Get("store_id") != "store-1" || r.Header.Get("Authorization") != "Bearer tok-1" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if c.status != 0 {
			w.WriteHeader(c.status)
			return
		}
		etag := fmt.Sprintf(`"store-1:%d"`, c.version)
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"link_version": c.version}, "error": nil})
	})
	mux.Handle("/", inner)
	return c, httptest.NewServer(mux)
}

func (c *checkinCloud) posts() int {
	c.fakeCloud.mu.Lock()
	defer c.fakeCloud.mu.Unlock()
	return len(c.fakeCloud.syncBodies)
}

func (c *checkinCloud) gets() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.inm...)
}

func (c *checkinCloud) set(fn func(*checkinCloud)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn(c)
}

// fakeClock pins checkinNow for one test.
func fakeClock(t *testing.T) *time.Time {
	t.Helper()
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	orig := checkinNow
	checkinNow = func() time.Time { return now }
	t.Cleanup(func() { checkinNow = orig })
	return &now
}

func mustTick(t *testing.T, srvURL string, db *sql.DB, hooks Hooks) bool {
	t.Helper()
	contacted, err := tick(context.Background(), testCfg(srvURL), db, hooks)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	return contacted
}

func TestCheckin200RunsThePostAnd304SkipsIt(t *testing.T) {
	fakeClock(t)
	cloud, srv := newCheckinCloud(3)
	defer srv.Close()
	db := testDB(t)

	mustTick(t, srv.URL, db, Hooks{})
	if got := cloud.gets(); len(got) != 1 {
		t.Fatalf("first tick GETs = %v, want one", got)
	}
	if cloud.posts() != 1 {
		t.Fatalf("200 → POST: posts = %d, want 1", cloud.posts())
	}

	if !mustTick(t, srv.URL, db, Hooks{}) {
		t.Fatal("a 304 reached the cloud: contacted must be true")
	}
	if got := cloud.gets(); len(got) != 2 || got[1] != `"store-1:3"` {
		t.Fatalf("second GET If-None-Match = %v, want \"store-1:3\"", got)
	}
	if cloud.posts() != 1 {
		t.Fatalf("304 with unchanged state must skip the POST: posts = %d", cloud.posts())
	}

	cloud.set(func(c *checkinCloud) { c.version = 4 })
	mustTick(t, srv.URL, db, Hooks{})
	if cloud.posts() != 2 {
		t.Fatalf("a newer link_version (200) must run the POST: posts = %d", cloud.posts())
	}
	mustTick(t, srv.URL, db, Hooks{})
	if got := cloud.gets(); got[len(got)-1] != `"store-1:4"` || cloud.posts() != 2 {
		t.Fatalf("after the 200 the till must send the new version: GETs %v posts %d", got, cloud.posts())
	}
}

func TestCheckin304PostsWhenTillStateChanged(t *testing.T) {
	fakeClock(t)
	cloud, srv := newCheckinCloud(1)
	defer srv.Close()
	db := testDB(t)
	theme := "light"
	hooks := Hooks{DeviceExtra: func(context.Context) map[string]any { return map[string]any{"theme": theme} }}

	mustTick(t, srv.URL, db, hooks)
	// uptime moves every minute; it is not "till state" and must not
	// defeat the 304.
	origStarted := started
	started = started.Add(-7 * time.Minute)
	t.Cleanup(func() { started = origStarted })
	mustTick(t, srv.URL, db, hooks)
	if cloud.posts() != 1 {
		t.Fatalf("uptime alone must not force a POST: posts = %d", cloud.posts())
	}

	theme = "dark"
	mustTick(t, srv.URL, db, hooks)
	if cloud.posts() != 2 {
		t.Fatalf("a changed device body must POST despite the 304: posts = %d", cloud.posts())
	}
	mustTick(t, srv.URL, db, hooks)
	if cloud.posts() != 2 {
		t.Fatalf("unchanged again → skip: posts = %d", cloud.posts())
	}
}

func TestCheckin304PostsOnTheTenMinuteFloor(t *testing.T) {
	now := fakeClock(t)
	cloud, srv := newCheckinCloud(1)
	defer srv.Close()
	db := testDB(t)

	mustTick(t, srv.URL, db, Hooks{})
	*now = now.Add(9 * time.Minute)
	mustTick(t, srv.URL, db, Hooks{})
	if cloud.posts() != 1 {
		t.Fatalf("9 min after the last POST: posts = %d, want 1", cloud.posts())
	}
	*now = now.Add(time.Minute)
	mustTick(t, srv.URL, db, Hooks{})
	if cloud.posts() != 2 {
		t.Fatalf("10 min after the last POST the floor must POST: posts = %d", cloud.posts())
	}
	*now = now.Add(2 * time.Minute)
	mustTick(t, srv.URL, db, Hooks{})
	if cloud.posts() != 2 {
		t.Fatalf("the floor restarts from the last POST: posts = %d", cloud.posts())
	}
}

func TestCheckin304ConfirmsTheEntitlement(t *testing.T) {
	now := fakeClock(t)
	cloud, srv := newCheckinCloud(1)
	defer srv.Close()
	db := testDB(t)
	mustTick(t, srv.URL, db, Hooks{})
	seedEntitlementCache(t, db) // confirmed 2026-09-20

	*now = now.Add(2 * time.Minute)
	mustTick(t, srv.URL, db, Hooks{})
	if cloud.posts() != 1 {
		t.Fatalf("expected a 304 skip: posts = %d", cloud.posts())
	}
	got := readEntitlementCache(t, db)
	if want := now.UTC().Format(time.RFC3339); got[entitlement.KeyLastConfirmedAt] != want {
		t.Fatalf("last_confirmed_at = %q, want %q (a 304 confirms, ADR-0060 §3 amended)", got[entitlement.KeyLastConfirmedAt], want)
	}
	if got[entitlement.KeyPlan] != "pro" || got[entitlement.KeySubscriptionStatus] != "active" {
		t.Fatalf("a 304 must leave the cached plan alone: %v", got)
	}
}

func TestCheckin304WithNoCachedEntitlementWritesNothing(t *testing.T) {
	fakeClock(t)
	_, srv := newCheckinCloud(1)
	defer srv.Close()
	db := testDB(t)
	mustTick(t, srv.URL, db, Hooks{})
	mustTick(t, srv.URL, db, Hooks{})
	if _, ok, _ := data.NewSettingsRepo(db).Get(context.Background(), entitlement.KeyLastConfirmedAt); ok {
		t.Fatal("a 304 must not invent an entitlement confirmation the till never had")
	}
}

func TestCheckin401FailsTheTickLikeThePost(t *testing.T) {
	fakeClock(t)
	cloud, srv := newCheckinCloud(1)
	defer srv.Close()
	cloud.set(func(c *checkinCloud) { c.status = http.StatusUnauthorized })
	contacted, err := tick(context.Background(), testCfg(srv.URL), testDB(t), Hooks{})
	var se *statusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusUnauthorized {
		t.Fatalf("err = %v, want a 401 statusError", err)
	}
	if contacted || cloud.posts() != 0 {
		t.Fatalf("a revoked credential: contacted=%t posts=%d, want false/0", contacted, cloud.posts())
	}
}

func TestCheckinOldCloudFallsBackToPostingAndRetriesLater(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusInternalServerError} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			now := fakeClock(t)
			cloud, srv := newCheckinCloud(1)
			defer srv.Close()
			cloud.set(func(c *checkinCloud) { c.status = code })
			db := testDB(t)

			mustTick(t, srv.URL, db, Hooks{})
			mustTick(t, srv.URL, db, Hooks{})
			if cloud.posts() != 2 {
				t.Fatalf("old cloud: every tick must POST: posts = %d", cloud.posts())
			}
			if got := cloud.gets(); len(got) != 1 {
				t.Fatalf("old cloud: the GET is not retried every tick: GETs = %d", len(got))
			}
			*now = now.Add(61 * time.Minute)
			cloud.set(func(c *checkinCloud) { c.status = 0 })
			mustTick(t, srv.URL, db, Hooks{})
			if got := cloud.gets(); len(got) != 2 {
				t.Fatalf("after an hour the GET is retried: GETs = %d", len(got))
			}
			mustTick(t, srv.URL, db, Hooks{})
			if cloud.posts() != 3 {
				t.Fatalf("an upgraded cloud's 304 skips again: posts = %d, want 3", cloud.posts())
			}
		})
	}
}

func TestCheckinNewerLinkVersionFromTheLinkForcesThePost(t *testing.T) {
	fakeClock(t)
	cloud, srv := newCheckinCloud(3)
	defer srv.Close()
	db := testDB(t)
	var hint atomic.Int64
	hooks := Hooks{LinkVersion: hint.Load}

	mustTick(t, srv.URL, db, hooks) // learns 3
	hint.Store(3)                   // the hello's version: nothing new
	mustTick(t, srv.URL, db, hooks)
	if cloud.posts() != 1 {
		t.Fatalf("a hello with the known version must not force a POST: posts = %d", cloud.posts())
	}

	// A nudge carries 4. Even if the till sent If-None-Match 4 the cloud
	// would 304 it — the POST must run anyway.
	cloud.set(func(c *checkinCloud) { c.version = 4 })
	hint.Store(4)
	mustTick(t, srv.URL, db, hooks)
	if cloud.posts() != 2 {
		t.Fatalf("a nudge with a newer link_version must POST: posts = %d", cloud.posts())
	}
	mustTick(t, srv.URL, db, hooks)
	if got := cloud.gets(); got[len(got)-1] != `"store-1:4"` || cloud.posts() != 2 {
		t.Fatalf("after the nudged POST: GETs %v posts %d, want If-None-Match 4 and a skip", got, cloud.posts())
	}
}

func TestCheckinFailedPostIsRetriedNextTick(t *testing.T) {
	fakeClock(t)
	var fail atomic.Bool
	cloud, srv := newCheckinCloud(1)
	defer srv.Close()
	wrapped := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/stores/sync" && fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		srv.Config.Handler.ServeHTTP(w, r)
	}))
	defer wrapped.Close()
	db := testDB(t)
	fail.Store(true)
	if _, err := tick(context.Background(), testCfg(wrapped.URL), db, Hooks{}); err == nil {
		t.Fatal("the POST failed: tick must fail")
	}
	fail.Store(false)
	mustTick(t, wrapped.URL, db, Hooks{})
	if cloud.posts() != 1 {
		t.Fatalf("the version a failed POST never delivered must be retried: posts = %d", cloud.posts())
	}
}

// The cloud's link_version can go backwards (a restored cloud DB). The till
// must adopt whatever the cloud now says, not keep its higher number and get
// a 200 + POST on every tick forever (ut-docs#2827 review).
func TestCheckinAdoptsALowerCloudVersion(t *testing.T) {
	fakeClock(t)
	cloud, srv := newCheckinCloud(9)
	defer srv.Close()
	db := testDB(t)

	mustTick(t, srv.URL, db, Hooks{})
	cloud.set(func(c *checkinCloud) { c.version = 2 })
	mustTick(t, srv.URL, db, Hooks{}) // 200 with the lower version → POST
	posts := cloud.posts()
	mustTick(t, srv.URL, db, Hooks{})
	if got := cloud.gets(); got[len(got)-1] != `"store-1:2"` {
		t.Fatalf("If-None-Match after a lower 200 = %v, want \"store-1:2\"", got[len(got)-1])
	}
	if cloud.posts() != posts {
		t.Fatalf("a matching 304 must skip the POST: posts %d → %d", posts, cloud.posts())
	}
}
