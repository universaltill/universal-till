package pages

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ADR-0114 §2–§4 (ut-docs#2735): the replica side of the main-till link,
// end to end against the real GET /api/sync/link handler.

// fastLinkClientOptions shrinks the client's timings; the main-till
// fixture pings every 50 ms.
func fastLinkClientOptions() fleetlink.ClientOptions {
	o := fleetlink.DefaultClientOptions()
	o.PingInterval = 50 * time.Millisecond
	o.PeerTimeout = 2 * time.Second
	o.BackoffMin = 10 * time.Millisecond
	o.BackoffMax = 100 * time.Millisecond
	o.RecheckEvery = 50 * time.Millisecond
	o.ReportEvery = time.Hour
	return o
}

// linkReplica is a replica paired to baseURL as tillID with bearer
// "token-abc", its link client built but not started.
func linkReplica(t *testing.T, baseURL, tillID string, opts fleetlink.ClientOptions) *common.Deps {
	t.Helper()
	r := newPullTestReplica(t, baseURL)
	if err := r.Settings.Set(t.Context(), "sync.till_id", tillID); err != nil {
		t.Fatal(err)
	}
	r.LinkClient = newSyncLinkClient(r, opts)
	return r
}

// runReplica starts the replica's pull loop (intervals as given, tick
// counted) and link client, joined on cleanup like app.Run's drain.
func runReplica(t *testing.T, r *common.Deps, unlinked, linked time.Duration) *atomic.Int32 {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var pulls atomic.Int32
	client := &http.Client{Timeout: 5 * time.Second}
	startSyncPullLoop(ctx, r, &wg, unlinked, linked, func() {
		syncPullTick(ctx, r, client, func(context.Context) {})
		pulls.Add(1)
	})
	StartSyncLinkClient(ctx, r, &wg)
	t.Cleanup(func() {
		cancel()
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("replica goroutines did not stop on shutdown")
		}
	})
	return &pulls
}

func settingOf(t *testing.T, d *common.Deps, key string) string {
	t.Helper()
	v, _, _ := d.Settings.Get(context.Background(), key)
	return v
}

func TestReplicaLink_HelloKicksAPullAndAnAdminChangeArrivesInASecond(t *testing.T) {
	f := newSyncLinkFixture(t)
	tillID := f.enrol(t, "Till 2", "token-abc")
	adminRepo := data.NewSyncAdminRepo(f.dp.Db)
	watchCtx, stopWatch := context.WithCancel(context.Background())
	var watchWG sync.WaitGroup
	runLinkAdminWatch(watchCtx, f.dp, &watchWG, 20*time.Millisecond, adminRepo)
	t.Cleanup(func() { stopWatch(); watchWG.Wait() })

	replica := linkReplica(t, f.srv.URL, tillID, fastLinkClientOptions())
	// Hour-long intervals: every pull in this test is a link kick.
	pulls := runReplica(t, replica, time.Hour, time.Hour)

	// The main till's fingerprint read at check time: its own bookkeeping
	// (tills.last_seen_at on every authed call) can move it meanwhile.
	currentFP := func() string {
		fp, err := adminRepo.AdminFingerprint(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return fp
	}
	if !waitFor(t, 3*time.Second, func() bool {
		return replica.LinkClient.Linked() && pulls.Load() >= 1 && settingOf(t, replica, "sync.pull_version") != ""
	}) {
		t.Fatalf("hello with a different admin cursor did not pull (linked=%v, pulls=%d)",
			replica.LinkClient.Linked(), pulls.Load())
	}
	fp := settingOf(t, replica, "sync.pull_version")
	if got := syncPullInterval(replica, syncPullEvery, syncPullEveryLinked)(); got != syncPullEveryLinked {
		t.Fatalf("linked polling floor = %v, want %v", got, syncPullEveryLinked)
	}
	if p := f.dp.Link.Peer(tillID); p == nil {
		t.Fatal("the main till has no link for the replica")
	} else if h, ok := p.Hello(); !ok || h.Role != "replica" || h.TillID != tillID || h.SyncProtocol != fleetlink.SyncProtocolLevel {
		t.Fatalf("replica hello at the main till = %+v (ok=%v)", h, ok)
	}
	if !waitFor(t, 2*time.Second, func() bool {
		r, ok := f.dp.Link.Report(tillID)
		return ok && r.UpdateState == "idle" && !r.TLSPinned
	}) {
		t.Fatal("no report from the replica on connect")
	}
	if !waitFor(t, time.Second, func() bool { return settingOf(t, replica, "sync.last_contact_at") != "" }) {
		t.Fatal("the link's hello did not record contact with the main till")
	}

	time.Sleep(80 * time.Millisecond) // the admin watch's baseline
	before := pulls.Load()
	start := time.Now()
	if _, err := data.NewCatalogRepo(f.dp.Db).CreateItem(context.Background(), catalogtypes.ItemInput{
		Name: "Linked Widget", BasePrice: 450, IsActive: true,
	}); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 2*time.Second, func() bool {
		v := settingOf(t, replica, "sync.pull_version")
		return v != fp && v == currentFP()
	}) {
		t.Fatalf("admin change never reached the replica (pulls %d → %d)", before, pulls.Load())
	}
	items, err := data.NewCatalogRepo(replica.Db).ListItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range items {
		found = found || it.Name == "Linked Widget"
	}
	if !found {
		t.Fatal("the pulled bundle did not bring the new item")
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Fatalf("admin change took %v to reach the replica, want about a second", took)
	}
}

// ut-docs#2897: the replica's hello carries its own cloud device id
// (enroll.CurrentStatus, #2730) alongside the LAN pairing till_id, so the
// main till's status frame can name a satellite/replica by the id my.'s
// Tills rows and Live panel actually key on.
func TestReplicaLink_HelloCarriesItsOwnCloudDeviceID(t *testing.T) {
	f := newSyncLinkFixture(t)
	tillID := f.enrol(t, "Till 2", "token-abc")

	cfg := &config.Config{Marketplace: config.MarketplaceConfig{DeviceID: "till-cloud-xyz"}}
	enroll.Init(t.Context(), cfg, newMemKV(), &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })

	replica := linkReplica(t, f.srv.URL, tillID, fastLinkClientOptions())
	runReplica(t, replica, time.Hour, time.Hour)

	if !waitFor(t, 3*time.Second, replica.LinkClient.Linked) {
		t.Fatal("replica never linked")
	}
	p := f.dp.Link.Peer(tillID)
	if p == nil {
		t.Fatal("the main till has no link for the replica")
	}
	h, ok := p.Hello()
	if !ok || h.TillID != tillID || h.CloudDeviceID != "till-cloud-xyz" {
		t.Fatalf("replica hello at the main till = %+v (ok=%v), want till_id %q and cloud_device_id till-cloud-xyz", h, ok, tillID)
	}
}

func TestReplicaLink_OlderMainWithoutLinkIsNeverDialled(t *testing.T) {
	var dials atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sync/ping", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"till_id": "t"}, "error": nil})
	})
	mux.HandleFunc("GET /api/sync/link", func(w http.ResponseWriter, r *http.Request) {
		dials.Add(1)
		http.Error(w, "no", http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	replica := linkReplica(t, srv.URL, "till-2", fastLinkClientOptions())
	runReplica(t, replica, time.Hour, time.Hour)
	time.Sleep(300 * time.Millisecond)
	if n := dials.Load(); n != 0 {
		t.Fatalf("dialled an older main till %d times", n)
	}
	if replica.LinkClient.Linked() {
		t.Fatal("linked to a main till that has no link")
	}
}

func TestReplicaLink_RevokeStopsTheClient(t *testing.T) {
	f := newSyncLinkFixture(t)
	tillID := f.enrol(t, "Till 2", "token-abc")
	replica := linkReplica(t, f.srv.URL, tillID, fastLinkClientOptions())
	runReplica(t, replica, time.Hour, time.Hour)
	if !waitFor(t, 3*time.Second, replica.LinkClient.Linked) {
		t.Fatal("never linked")
	}
	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/sync/tills/"+tillID+"/revoke", nil),
		auth.User{ID: "u1", Role: "manager"})
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body)
	}
	if !waitFor(t, 2*time.Second, func() bool { return !replica.LinkClient.Linked() }) {
		t.Fatal("still linked after revoke")
	}
	time.Sleep(300 * time.Millisecond) // many backoff periods
	if f.dp.Link.Len() != 0 || replica.LinkClient.Linked() {
		t.Fatal("a revoked replica linked again")
	}
}

// restartableMain serves the main till's sync API on a fixed address that
// can be stopped and started again, like a main till restarting.
type restartableMain struct {
	dp   *common.Deps
	addr string
	srv  *httptest.Server
}

func (m *restartableMain) start(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	registerSyncAPI(mux, m.dp)
	adminRepo := registerSyncAdmin(mux, m.dp)
	cfg := fleetlink.DefaultConfig()
	cfg.PingInterval = 50 * time.Millisecond
	m.dp.Link = newSyncLinkHub(m.dp, cfg, adminRepo)
	registerSyncLink(mux, m.dp)
	srv := httptest.NewUnstartedServer(mux)
	if m.addr != "" {
		var l net.Listener
		var err error
		for range 50 { // the old socket may take a moment to free
			if l, err = net.Listen("tcp", m.addr); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		srv.Listener.Close()
		srv.Listener = l
	}
	srv.Start()
	m.addr = srv.Listener.Addr().String()
	m.srv = srv
}

// stop is a graceful shutdown: bye to every link, then the server goes.
func (m *restartableMain) stop() {
	m.dp.Link.Close()
	m.srv.Close()
}

func TestReplicaLink_ReconnectsAfterTheMainTillRestarts(t *testing.T) {
	m := &restartableMain{dp: newSyncPairingGateTestDeps(t)}
	m.start(t)
	t.Cleanup(func() { m.stop() })
	tillID, err := data.NewTillsRepo(m.dp.Db).InsertTill(context.Background(), "Till 2", hashBearer("token-abc"))
	if err != nil {
		t.Fatal(err)
	}
	replica := linkReplica(t, m.srv.URL, tillID, fastLinkClientOptions())
	runReplica(t, replica, time.Hour, time.Hour)
	if !waitFor(t, 3*time.Second, replica.LinkClient.Linked) {
		t.Fatal("never linked")
	}

	m.stop()
	if !waitFor(t, 2*time.Second, func() bool { return !replica.LinkClient.Linked() }) {
		t.Fatal("still linked after the main till stopped")
	}
	m.start(t)
	if !waitFor(t, 3*time.Second, func() bool { return replica.LinkClient.Linked() && m.dp.Link.Peer(tillID) != nil }) {
		t.Fatal("did not relink after the main till came back")
	}
}

// tcpProxy forwards to target; freeze makes it swallow every byte both
// ways without closing anything — a main till that silently vanished
// (Wi-Fi gone, power cut) as far as the heartbeat can tell.
type tcpProxy struct {
	l      net.Listener
	target string
	frozen atomic.Bool
	mu     sync.Mutex
	conns  []net.Conn
}

func newTCPProxy(t *testing.T, target string) *tcpProxy {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &tcpProxy{l: l, target: target}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			if p.frozen.Load() {
				p.track(c)
				continue
			}
			u, err := net.Dial("tcp", target)
			if err != nil {
				_ = c.Close()
				continue
			}
			p.track(c)
			p.track(u)
			go p.pipe(c, u)
			go p.pipe(u, c)
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, c := range p.conns {
			_ = c.Close()
		}
	})
	return p
}

func (p *tcpProxy) track(c net.Conn) {
	p.mu.Lock()
	p.conns = append(p.conns, c)
	p.mu.Unlock()
}

func (p *tcpProxy) pipe(dst, src net.Conn) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 && !p.frozen.Load() {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				return
			}
			return
		}
	}
}

func (p *tcpProxy) url() string { return "http://" + p.l.Addr().String() }

// Heartbeat loss is a failed contact for the shared PrimaryWatch (§4); its
// re-discovery switches sync.primary_url, and the link follows the switch.
func TestReplicaLink_HeartbeatLossFeedsPrimaryWatchAndTheLinkFollowsASwitch(t *testing.T) {
	f := newSyncLinkFixture(t)
	registerPrimaryProof(f.mux, f.dp)
	tillID := f.enrol(t, "Till 2", "token-abc")
	primaryID, err := discovery.TillID(context.Background(), data.NewSettingsRepo(f.dp.Db))
	if err != nil {
		t.Fatal(err)
	}
	// The replica paired through an address that will go silent.
	proxy := newTCPProxy(t, f.srv.Listener.Addr().String())
	opts := fastLinkClientOptions()
	opts.PeerTimeout = 300 * time.Millisecond
	opts.RecheckEvery = time.Hour // only the hello writes last contact here
	// A long backoff: after the loss only the re-discovery's Redial can
	// bring the link back within the test's window (full jitter makes a
	// short draw possible, so this can pass without it ~5% of runs, never
	// fail with it).
	opts.BackoffMin, opts.BackoffMax = time.Minute, time.Minute
	replica := linkReplica(t, proxy.url(), tillID, opts)
	if err := replica.Settings.Set(context.Background(), discovery.TillIDSettingKey, primaryID); err != nil {
		t.Fatal(err)
	}
	var browses atomic.Int32
	replica.PrimaryWatch = discovery.NewPrimaryWatch(replica.Settings, func(context.Context, time.Duration) ([]discovery.Candidate, error) {
		browses.Add(1)
		return []discovery.Candidate{{Name: "Shop", TillID: primaryID, BaseURL: f.srv.URL}}, nil
	})
	pulls := runReplica(t, replica, time.Hour, time.Hour)
	// Linked, and the pull its hello kicked has finished — both write
	// sync.last_contact_at, so only now can the test age it.
	if !waitFor(t, 3*time.Second, func() bool { return replica.LinkClient.Linked() && pulls.Load() >= 1 }) {
		t.Fatal("never linked through the proxy")
	}
	// Last contact long ago: one failed contact is enough to count as
	// unreachable (PrimaryWatch's after-a-restart rule), so the one
	// heartbeat loss below must be what triggers re-discovery.
	if err := replica.Settings.Set(context.Background(), "sync.last_contact_at",
		time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	proxy.frozen.Store(true)

	if !waitFor(t, 3*time.Second, func() bool { return browses.Load() > 0 }) {
		t.Fatal("a lost heartbeat was not recorded as a failed contact (no re-discovery ran)")
	}
	if !waitFor(t, 3*time.Second, func() bool { return settingOf(t, replica, "sync.primary_url") == f.srv.URL }) {
		t.Fatalf("primary_url = %q, want the proven %q", settingOf(t, replica, "sync.primary_url"), f.srv.URL)
	}
	if !waitFor(t, 3*time.Second, func() bool { return replica.LinkClient.Linked() && f.dp.Link.Peer(tillID) != nil }) {
		t.Fatal("the link did not redial the main till's new address")
	}
}

// §3: while linked the admin pull falls back to a 5-minute floor; unlinked
// it polls every 30 s as before, and a link loss re-arms the short interval
// at once instead of waiting out the long one.
func TestSyncPullLoop_PollingFloorFollowsTheLink(t *testing.T) {
	if syncPullEvery != 30*time.Second || syncPullEveryLinked != 5*time.Minute {
		t.Fatalf("polling floor drifted from ADR-0114 §3: %v / %v", syncPullEvery, syncPullEveryLinked)
	}
	var linked atomic.Bool
	reeval := make(chan struct{}, 1)
	var ticks atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	interval := func() time.Duration {
		if linked.Load() {
			return time.Hour
		}
		return 30 * time.Millisecond
	}
	runPullLoop(ctx, &wg, nil, reeval, interval, func() { ticks.Add(1) })
	t.Cleanup(func() { cancel(); wg.Wait() })

	if !waitFor(t, time.Second, func() bool { return ticks.Load() >= 3 }) {
		t.Fatal("unlinked loop is not polling")
	}
	linked.Store(true)
	reeval <- struct{}{}
	time.Sleep(40 * time.Millisecond) // a tick already in flight may land
	n := ticks.Load()
	time.Sleep(200 * time.Millisecond)
	if got := ticks.Load(); got != n {
		t.Fatalf("linked loop still polled %d times in 200 ms", got-n)
	}
	linked.Store(false)
	reeval <- struct{}{}
	if !waitFor(t, 60*time.Millisecond, func() bool { return ticks.Load() > n }) {
		t.Fatal("link loss did not bring the short interval back at once")
	}
}

func TestSyncPullLoop_KickRunsATickAtOnce(t *testing.T) {
	kick := make(chan struct{}, 1)
	var ticks atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	runPullLoop(ctx, &wg, kick, nil, func() time.Duration { return time.Hour }, func() { ticks.Add(1) })
	t.Cleanup(func() { cancel(); wg.Wait() })
	kick <- struct{}{}
	if !waitFor(t, time.Second, func() bool { return ticks.Load() == 1 }) {
		t.Fatal("kick did not run a tick")
	}
}

func TestSyncPullInterval_UsesTheLinkState(t *testing.T) {
	d := &common.Deps{}
	if got := syncPullInterval(d, time.Second, time.Minute)(); got != time.Second {
		t.Fatalf("no link client: %v, want the unlinked interval", got)
	}
	d.LinkClient = fleetlink.NewClient(fleetlink.DefaultClientOptions())
	if got := syncPullInterval(d, time.Second, time.Minute)(); got != time.Second {
		t.Fatalf("not linked: %v, want the unlinked interval", got)
	}
}

func TestRequestSyncPull_IsNonBlockingAndCoalesced(t *testing.T) {
	d := &common.Deps{}
	d.RequestSyncPull() // no loop: a no-op
	d.SyncPullNow = make(chan struct{}, 1)
	for range 5 {
		d.RequestSyncPull()
	}
	if len(d.SyncPullNow) != 1 {
		t.Fatalf("pending kicks = %d, want 1", len(d.SyncPullNow))
	}
}

// ADR-0114 §2 (review): a stock receipt or adjustment on the MAIN till
// changes the levels its linked tills pull; it must nudge `stock` (the
// sale/refund paths reach the same nudge through RequestSyncPush).
func TestSyncLink_MainTillStockMovementNudgesStock(t *testing.T) {
	f := newSyncLinkFixture(t)
	f.enrol(t, "Till 2", "bearer-t2")
	registerInventoryAPI(f.mux, f.dp)
	c, _, err := f.dial(t, "bearer-t2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nextLinkFrame(t, c); err != nil {
		t.Fatal(err)
	}
	rec := postInvJSON(t, f.mux, "/api/inventory/receipt",
		`{"type":"receive","item_id":"itm1","location_id":"loc_main","quantity":5,"reason":"delivery"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("receipt = %d: %s", rec.Code, rec.Body.String())
	}
	waitSync(t, c, fleetlink.ScopeStock)
}

// Held sales, tables and orders are read live from the main till; only a
// nudge naming a scope the HTTP pull carries should cost a pull.
func TestLinkScopesNeedPull_OnlyPulledScopes(t *testing.T) {
	for _, tc := range []struct {
		in   []fleetlink.Scope
		want bool
	}{
		{nil, false},
		{[]fleetlink.Scope{fleetlink.ScopeTables}, false},
		{[]fleetlink.Scope{fleetlink.ScopeHeldSales, fleetlink.ScopeOrders}, false},
		{[]fleetlink.Scope{fleetlink.ScopeAdmin}, true},
		{[]fleetlink.Scope{fleetlink.ScopePlugins}, true},
		{[]fleetlink.Scope{fleetlink.ScopeTables, fleetlink.ScopeStock}, true},
	} {
		if got := linkScopesNeedPull(tc.in); got != tc.want {
			t.Errorf("linkScopesNeedPull(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestReplicaLink_TableNudgeDoesNotPull(t *testing.T) {
	f := newSyncLinkFixture(t)
	tillID := f.enrol(t, "Till 2", "token-abc")
	replica := linkReplica(t, f.srv.URL, tillID, fastLinkClientOptions())
	pulls := runReplica(t, replica, time.Hour, time.Hour)
	if !waitFor(t, 3*time.Second, func() bool { return replica.LinkClient.Linked() && pulls.Load() >= 1 }) {
		t.Fatal("never linked")
	}
	before := pulls.Load()
	f.dp.Link.Nudge(fleetlink.ScopeTables, fleetlink.ScopeHeldSales, fleetlink.ScopeOrders)
	time.Sleep(300 * time.Millisecond)
	if n := pulls.Load(); n != before {
		t.Fatalf("a tables/held_sales/orders nudge ran %d pull(s)", n-before)
	}
	f.dp.Link.Nudge(fleetlink.ScopeStock)
	if !waitFor(t, 2*time.Second, func() bool { return pulls.Load() > before }) {
		t.Fatal("a stock nudge did not pull")
	}
}

// ut-docs#807's rule survives the link: sync.last_contact_at backs the
// chip's freshness and PrimaryWatch's after-a-restart rule, and a link that
// is up while every pull fails must not keep it green. The link refreshes
// contact only while the pull itself succeeded within the linked floor.
func TestRefreshLinkContact_OnlyWhileThePullIsHealthy(t *testing.T) {
	primary := newPullTestPrimary(t)
	replica := newPullTestReplica(t, primary.server.URL)
	ctx := context.Background()
	set := func(k, v string) {
		if err := replica.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	stale := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)

	// No successful pull recorded yet: the link alone is not contact.
	set("sync.last_contact_at", stale)
	refreshLinkContact(ctx, replica)
	if got := settingOf(t, replica, "sync.last_contact_at"); got != stale {
		t.Fatalf("with no pull on record the link refreshed contact to %q", got)
	}
	// A pull that last succeeded before the window: same.
	set("sync.last_pull_ok_at", stale)
	refreshLinkContact(ctx, replica)
	if got := settingOf(t, replica, "sync.last_contact_at"); got != stale {
		t.Fatalf("with a stale pull the link refreshed contact to %q", got)
	}
	// A successful pull records its time; from then on the link keeps
	// contact fresh between the 5-min pulls.
	syncPullTick(ctx, replica, &http.Client{Timeout: 5 * time.Second}, func(context.Context) {})
	if !withinLast(settingOf(t, replica, "sync.last_pull_ok_at"), time.Minute) {
		t.Fatalf("a successful pull did not record sync.last_pull_ok_at (%q)", settingOf(t, replica, "sync.last_pull_ok_at"))
	}
	set("sync.last_contact_at", stale)
	refreshLinkContact(ctx, replica)
	if !withinLast(settingOf(t, replica, "sync.last_contact_at"), time.Minute) {
		t.Fatal("with a healthy pull the link did not refresh contact")
	}
}
