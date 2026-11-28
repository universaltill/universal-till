package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5922: the replica's journal push tick must not write sync.* back
// (cursor, last_push_at, last_contact_at, held sale, learned batch size) when
// a promote lands while its request is in flight, must not re-open the "sale
// too large" Problem, and must not POST again once promoted.

const promotedMidPushLine = "[INFO] sync push: skipped, this till was promoted to main till during the push"

func promoteNow(t *testing.T, d *common.Deps) {
	t.Helper()
	if err := data.NewSettingsRepo(d.Db).ClearReplicaIdentity(context.Background()); err != nil {
		t.Errorf("promote: %v", err)
	}
}

// promoteOnce returns a hook that promotes d the first time it runs.
func promoteOnce(t *testing.T, d *common.Deps) func() {
	var once sync.Once
	return func() { once.Do(func() { promoteNow(t, d) }) }
}

func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	restore := logging.CaptureForTest(buf)
	t.Cleanup(restore)
	return buf
}

func assertSkipLogged(t *testing.T, logs *syncBuffer, want int) {
	t.Helper()
	out := logs.String()
	if n := strings.Count(out, promotedMidPushLine); n != want {
		t.Fatalf("promoted-mid-push INFO line logged %d times, want %d; log:\n%s", n, want, out)
	}
	if strings.Contains(out, "[WARN]") || strings.Contains(out, "[ERROR]") {
		t.Fatalf("promote race logged a WARN/ERROR; log:\n%s", out)
	}
}

func assertKeysAbsent(t *testing.T, d *common.Deps, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if v, found, _ := d.Settings.Get(context.Background(), k); found || v != "" {
			t.Errorf("%s = %q (row exists: %v) after the promote, want absent", k, v, found)
		}
	}
}

// --- sales step (rig from #5428) ---

func TestSyncPushSales_PromotedMidPushWritesNothing_5922(t *testing.T) {
	run := func(t *testing.T, promote bool) (*pushRig, *syncBuffer) {
		r := newPushRig(t)
		noKicks(t)
		ids := r.seed(t, 2)
		// A limit that fits one sale but not two: request 1 is a 413, the
		// halved request 2 is accepted.
		r.fake.limit.Store(int64(r.oneSale) * 3 / 2)
		if err := r.replicaDp.Settings.Set(context.Background(), salesPushHeldKey, ids[0]); err != nil {
			t.Fatal(err)
		}
		if promote {
			r.fake.before = promoteOnce(t, r.replicaDp)
		}
		logs := captureLogs(t)
		r.tick()
		return r, logs
	}
	t.Run("control_still_replica_writes_everything", func(t *testing.T) {
		r, _ := run(t, false)
		for _, k := range []string{"sync.push_cursor", "sync.last_push_at", "sync.last_contact_at", salesPushBatchKey} {
			if r.setting(k) == "" {
				t.Errorf("%s not written on a till that stays a replica", k)
			}
		}
		if r.setting(salesPushHeldKey) != "" {
			t.Errorf("held sale not cleared on a till that stays a replica")
		}
		if r.replicaDp.MainContact().IsZero() {
			t.Errorf("main contact not marked on a till that stays a replica")
		}
	})
	t.Run("promoted_mid_push", func(t *testing.T) {
		r, logs := run(t, true)
		assertKeysAbsent(t, r.replicaDp, "sync.push_cursor", "sync.last_push_at", "sync.last_contact_at", salesPushHeldKey, salesPushBatchKey)
		if !r.replicaDp.MainContact().IsZero() {
			t.Errorf("MainContact advanced to %v after a promote", r.replicaDp.MainContact())
		}
		assertSkipLogged(t, logs, 1)
	})
}

func TestSyncPushSales_PromotedMid413HoldsNothing_5922(t *testing.T) {
	r := newPushRig(t)
	noKicks(t)
	r.seedBig(t, "big-1", time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC), 4*r.oneSale)
	r.fake.limit.Store(int64(2 * r.oneSale))
	r.fake.before = promoteOnce(t, r.replicaDp)
	logs := captureLogs(t)
	r.tick()
	assertKeysAbsent(t, r.replicaDp, salesPushHeldKey, salesPushBatchKey, "sync.push_cursor")
	if logging.HasOpenProblem(SalePushTooLargeProblemKey) {
		t.Errorf("the sale-too-large Problem was re-opened after a promote")
	}
	assertSkipLogged(t, logs, 1)
}

// --- no-sale / cleared-basket / closes rigs ---

type promoRig struct {
	primaryDp, replicaDp *common.Deps
	srv                  *httptest.Server
	client               *http.Client
	mu                   sync.Mutex
	hits                 map[string]int
	before               map[string]func()
}

func (r *promoRig) hit(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits[path]
}

func newPromoRig(t *testing.T) *promoRig {
	t.Helper()
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)
	ctx := context.Background()
	mux, pdp := newSyncSalesTestDeps(t)
	registerSyncNoSales(mux, pdp)
	registerSyncClearedBaskets(mux, pdp)
	if _, err := data.NewTillsRepo(pdp.Db).InsertTill(ctx, "Replica 1", hashBearer("token-abc")); err != nil {
		t.Fatal(err)
	}
	r := &promoRig{primaryDp: pdp, hits: map[string]int{}, before: map[string]func(){}}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.hits[req.URL.Path]++
		hook := r.before[req.URL.Path]
		r.mu.Unlock()
		if hook != nil {
			hook()
		}
		mux.ServeHTTP(w, req)
	}))
	t.Cleanup(r.srv.Close)
	_, r.replicaDp = newSyncSalesTestDeps(t)
	for k, v := range map[string]string{"sync.primary_url": r.srv.URL, "sync.bearer": "token-abc"} {
		if err := r.replicaDp.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	r.client = &http.Client{Timeout: 10 * time.Second}
	return r
}

func (r *promoRig) seedSale(t *testing.T) {
	t.Helper()
	seedLocalSale(t, r.replicaDp, "ps-1", "R-ps-1", "2026-01-01T10:00:00Z")
}

func (r *promoRig) seedNoSale(t *testing.T) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := data.NewPOSRepo(r.replicaDp.Db).InsertNoSaleEvent(context.Background(), nil,
		data.NoSaleEvent{ID: "ns-1", CreatedAt: now, RegisterID: "reg-replica", ActorID: "u1"}); err != nil {
		t.Fatal(err)
	}
}

func (r *promoRig) seedClearedBasket(t *testing.T) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := data.NewPOSRepo(r.replicaDp.Db).InsertClearedBasket(context.Background(), data.ClearedBasket{
		ID: "cb-1", CreatedAt: now, RegisterID: "reg-replica", ActorID: "u1", LineCount: 1, Total: money.FromMinor(400),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSyncPushTick_PromotedMidSalesPushSkipsLaterSteps_5922(t *testing.T) {
	r := newPromoRig(t)
	noKicks(t)
	r.seedSale(t)
	r.seedNoSale(t)
	r.seedClearedBasket(t)
	r.before["/api/sync/sales"] = promoteOnce(t, r.replicaDp)
	logs := captureLogs(t)
	syncPushTick(context.Background(), r.replicaDp, r.client)
	if n := r.hit("/api/sync/sales"); n != 1 {
		t.Fatalf("sales POSTs = %d, want 1", n)
	}
	for _, p := range []string{"/api/sync/no-sales", "/api/sync/cleared-baskets", "/api/sync/closes"} {
		if n := r.hit(p); n != 0 {
			t.Errorf("%s got %d POST(s) after the promote, want 0", p, n)
		}
	}
	assertKeysAbsent(t, r.replicaDp, "sync.push_cursor", "sync.last_push_at", "sync.last_contact_at",
		noSalePushCursorKey, clearedBasketPushCursorKey)
	// The real PeerPinned gate keeps closes off here; the closes step has its own tests.
	assertSkipLogged(t, logs, 1)
}

func TestSyncPushNoSales_PromotedMidPushWritesNothing_5922(t *testing.T) {
	r := newPromoRig(t)
	r.seedNoSale(t)
	r.before["/api/sync/no-sales"] = promoteOnce(t, r.replicaDp)
	logs := captureLogs(t)
	syncPushTick(context.Background(), r.replicaDp, r.client)
	if n := r.hit("/api/sync/no-sales"); n != 1 {
		t.Fatalf("no-sale POSTs = %d, want 1", n)
	}
	assertKeysAbsent(t, r.replicaDp, noSalePushCursorKey, "sync.last_push_at", "sync.last_contact_at")
	if !r.replicaDp.MainContact().IsZero() {
		t.Errorf("MainContact advanced after a promote")
	}
	assertSkipLogged(t, logs, 1)
}

func TestSyncPushClearedBaskets_PromotedMidPushWritesNothing_5922(t *testing.T) {
	r := newPromoRig(t)
	r.seedClearedBasket(t)
	r.before["/api/sync/cleared-baskets"] = promoteOnce(t, r.replicaDp)
	logs := captureLogs(t)
	syncPushTick(context.Background(), r.replicaDp, r.client)
	if n := r.hit("/api/sync/cleared-baskets"); n != 1 {
		t.Fatalf("cleared-basket POSTs = %d, want 1", n)
	}
	assertKeysAbsent(t, r.replicaDp, clearedBasketPushCursorKey, "sync.last_push_at", "sync.last_contact_at")
	if !r.replicaDp.MainContact().IsZero() {
		t.Errorf("MainContact advanced after a promote")
	}
	assertSkipLogged(t, logs, 1)
}

// --- closes ---

// closesShop is a mirror shop with till B holding one own close, served over
// TLS through a wrapper that counts and can promote B on /api/sync/closes.
func closesShop(t *testing.T, onClosePost func()) (*mirrorShop, *mirrorTill, *httptest.Server, *int) {
	t.Helper()
	shop := newMirrorShop(t, "B")
	shop.enable()
	b := shop.tills["B"]
	posts := new(int)
	var mu sync.Mutex
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync/closes" {
			mu.Lock()
			*posts++
			mu.Unlock()
			if onClosePost != nil {
				onClosePost()
			}
		}
		shop.main.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return shop, b, srv, posts
}

func TestSyncPushCloses_PromotedMidPushWritesNothing_5922(t *testing.T) {
	var b *mirrorTill
	hook := func() { promoteNow(t, b.d) }
	shop, bb, srv, posts := closesShop(t, hook)
	b = bb
	closeOn(t, b, "pc-1", localNoonDaysAgo(2))
	logs := captureLogs(t)
	syncPushCloses(t.Context(), b.d, srv.Client(), srv.URL, b.bearer, func(context.Context) bool { return true })
	_ = shop
	if *posts != 1 {
		t.Fatalf("closes POSTs = %d, want 1", *posts)
	}
	assertKeysAbsent(t, b.d, salesMirrorClosePushCursorKey)
	assertSkipLogged(t, logs, 1)
}

func TestSyncPushCloses_PromotedAllMalformedWritesNothing_5922(t *testing.T) {
	_, b, srv, posts := closesShop(t, nil)
	// Only a malformed close: the step makes no request and only advances
	// the cursor, which a promote must refuse.
	if _, err := b.d.Db.Exec(`INSERT INTO report_archive (id, kind, period, content_json, created_at) VALUES ('bad-close', 'eod', 'bad', '{broken', datetime('now', '-3 hours'))`); err != nil {
		t.Fatal(err)
	}
	// Control: still a replica, the cursor advances.
	logs := captureLogs(t)
	syncPushCloses(t.Context(), b.d, srv.Client(), srv.URL, b.bearer, func(context.Context) bool { return true })
	if v, _, _ := b.d.Settings.Get(t.Context(), salesMirrorClosePushCursorKey); v == "" {
		t.Fatalf("control: all-malformed close did not advance the cursor (test rig does not exercise the advance path); log:\n%s", logs.String())
	}
	if err := b.d.Settings.Set(t.Context(), salesMirrorClosePushCursorKey, ""); err != nil {
		t.Fatal(err)
	}
	promoteNow(t, b.d)
	if err := b.d.Settings.Set(t.Context(), data.SalesMirrorEnabledKey, "1"); err != nil {
		t.Fatal(err)
	}
	syncPushCloses(t.Context(), b.d, srv.Client(), srv.URL, b.bearer, func(context.Context) bool { return true })
	assertKeysAbsent(t, b.d, salesMirrorClosePushCursorKey)
	if *posts != 0 {
		t.Fatalf("closes POSTs = %d, want 0 (all rows malformed)", *posts)
	}
}

// --- pre-POST check ---

func TestSyncPushSteps_NoPostWhenAlreadyPromoted_5922(t *testing.T) {
	ctx := context.Background()
	r := newPromoRig(t)
	noKicks(t)
	r.seedSale(t)
	r.seedNoSale(t)
	r.seedClearedBasket(t)
	promoteNow(t, r.replicaDp)
	steps := map[string]func() bool{
		"/api/sync/sales": func() bool {
			return syncPushSales(ctx, r.replicaDp, r.client, r.srv.URL, "token-abc")
		},
		"/api/sync/no-sales": func() bool {
			return syncPushNoSales(ctx, r.replicaDp, r.client, r.srv.URL, "token-abc")
		},
		"/api/sync/cleared-baskets": func() bool {
			return syncPushClearedBaskets(ctx, r.replicaDp, r.client, r.srv.URL, "token-abc")
		},
	}
	for path, step := range steps {
		logs := captureLogs(t)
		if !step() {
			t.Errorf("%s: step returned false after a promote, want true", path)
		}
		if n := r.hit(path); n != 0 {
			t.Errorf("%s: %d POST(s) after the promote, want 0", path, n)
		}
		assertSkipLogged(t, logs, 1)
	}
	t.Run("closes", func(t *testing.T) {
		_, b, srv, posts := closesShop(t, nil)
		closeOn(t, b, "pc-2", localNoonDaysAgo(2))
		promoteNow(t, b.d)
		if err := b.d.Settings.Set(ctx, data.SalesMirrorEnabledKey, "1"); err != nil {
			t.Fatal(err)
		}
		logs := captureLogs(t)
		if !syncPushCloses(ctx, b.d, srv.Client(), srv.URL, b.bearer, func(context.Context) bool { return true }) {
			t.Errorf("closes step returned false after a promote, want true")
		}
		if *posts != 0 {
			t.Errorf("closes: %d POST(s) after the promote, want 0", *posts)
		}
		assertSkipLogged(t, logs, 1)
	})
}
