package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ADR-0114 §1/§2/§11 (ut-docs#2734): the main-till side of the link.
// GET /api/sync/link is bearer-authed by syncTill like every /api/sync/*
// route, sends the main till's hello, nudges on real change points, and a
// revoked till loses its link at once.

type syncLinkFixture struct {
	dp  *common.Deps
	mux *http.ServeMux
	srv *httptest.Server
}

func newSyncLinkFixture(t *testing.T) *syncLinkFixture {
	t.Helper()
	dp := newSyncPairingGateTestDeps(t)
	mux := http.NewServeMux()
	registerSyncAPI(mux, dp)
	adminRepo := registerSyncAdmin(mux, dp)
	cfg := fleetlink.DefaultConfig()
	cfg.PingInterval = 50 * time.Millisecond
	dp.Link = newSyncLinkHub(dp, cfg, adminRepo)
	registerSyncLink(mux, dp)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		dp.Link.Close()
		srv.Close()
	})
	return &syncLinkFixture{dp: dp, mux: mux, srv: srv}
}

func (f *syncLinkFixture) enrol(t *testing.T, name, bearer string) string {
	t.Helper()
	id, err := data.NewTillsRepo(f.dp.Db).InsertTill(context.Background(), name, hashBearer(bearer))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *syncLinkFixture) dial(t *testing.T, bearer string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	hdr := http.Header{}
	if bearer != "" {
		hdr.Set("Authorization", "Bearer "+bearer)
	}
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(f.srv.URL, "http")+"/api/sync/link",
		&websocket.DialOptions{HTTPHeader: hdr})
	if c != nil {
		t.Cleanup(func() { _ = c.CloseNow() })
	}
	return c, resp, err
}

// nextLinkFrame reads the next non-ping envelope.
func nextLinkFrame(t *testing.T, c *websocket.Conn) (fleetlink.Envelope, error) {
	t.Helper()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, b, err := c.Read(ctx)
		cancel()
		if err != nil {
			return fleetlink.Envelope{}, err
		}
		e, err := fleetlink.Decode(b)
		if err != nil {
			t.Fatalf("undecodable frame %s: %v", b, err)
		}
		if e.Type == fleetlink.TypePing {
			continue
		}
		return e, nil
	}
}

// waitSync reads frames until a sync frame naming scope arrives.
func waitSync(t *testing.T, c *websocket.Conn, scope fleetlink.Scope) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		e, err := nextLinkFrame(t, c)
		if err != nil {
			t.Fatalf("waiting for sync %s: %v", scope, err)
		}
		if e.Type != fleetlink.TypeSync {
			continue
		}
		var p fleetlink.SyncPayload
		_ = json.Unmarshal(e.Payload, &p)
		for _, s := range p.Scopes {
			if s == scope {
				return
			}
		}
	}
	t.Fatalf("no sync %s nudge", scope)
}

func TestSyncLink_RejectsMissingOrBadBearer(t *testing.T) {
	f := newSyncLinkFixture(t)
	f.enrol(t, "Till 2", "bearer-t2")
	for _, bearer := range []string{"", "not-a-real-bearer"} {
		_, resp, err := f.dial(t, bearer)
		if err == nil {
			t.Fatalf("bearer %q: upgrade succeeded", bearer)
		}
		if resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("bearer %q: resp = %v, want 401", bearer, resp)
		}
	}
	if f.dp.Link.Len() != 0 {
		t.Fatal("a rejected dial registered a link")
	}
}

func TestSyncLink_AuthedLinkGetsMainTillHello(t *testing.T) {
	f := newSyncLinkFixture(t)
	tillID := f.enrol(t, "Till 2", "bearer-t2")
	c, _, err := f.dial(t, "bearer-t2")
	if err != nil {
		t.Fatal(err)
	}
	e, err := nextLinkFrame(t, c)
	if err != nil {
		t.Fatal(err)
	}
	if e.Type != fleetlink.TypeHello {
		t.Fatalf("first frame = %s, want hello", e.Type)
	}
	var h fleetlink.Hello
	if err := json.Unmarshal(e.Payload, &h); err != nil {
		t.Fatal(err)
	}
	mainID, _ := discovery.TillID(context.Background(), data.NewSettingsRepo(f.dp.Db))
	if h.Role != "main" || h.TillID != mainID || h.PeerTillID != tillID || h.SyncProtocol != fleetlink.SyncProtocolLevel {
		t.Fatalf("hello = %+v (main id %s, till %s)", h, mainID, tillID)
	}
	// Cursors are the same fingerprints the HTTP pulls serve, so a replica
	// compares them to its sync.*_version settings directly.
	wantAdmin, _ := data.NewSyncAdminRepo(f.dp.Db).AdminFingerprint(context.Background())
	if h.Cursors.Admin == "" || h.Cursors.Admin != wantAdmin || h.Cursors.Plugins == "" || h.Cursors.Stock == "" {
		t.Fatalf("cursors = %+v, want admin %s and non-empty plugins/stock", h.Cursors, wantAdmin)
	}
	if h.Version == "" || h.Platform == "" {
		t.Fatalf("hello lacks version/platform: %+v", h)
	}
}

func TestSyncLink_RefusedOnAReplica(t *testing.T) {
	f := newSyncLinkFixture(t)
	f.enrol(t, "Till 2", "bearer-t2")
	if err := f.dp.Settings.Set(context.Background(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
	_, resp, err := f.dial(t, "bearer-t2")
	if err == nil || resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("resp = %v err = %v, want 409 (links terminate on the main till only)", resp, err)
	}
}

func TestSyncLink_RevokeClosesTheLink(t *testing.T) {
	f := newSyncLinkFixture(t)
	tillID := f.enrol(t, "Till 2", "bearer-t2")
	c, _, err := f.dial(t, "bearer-t2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nextLinkFrame(t, c); err != nil {
		t.Fatal(err)
	}
	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/sync/tills/"+tillID+"/revoke", nil),
		auth.User{ID: "u1", Role: "manager"})
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body)
	}
	var closeErr error
	for closeErr == nil {
		_, closeErr = nextLinkFrame(t, c)
	}
	if websocket.CloseStatus(closeErr) != websocket.StatusCode(fleetlink.CloseRevoked) {
		t.Fatalf("close = %v, want %d", closeErr, fleetlink.CloseRevoked)
	}
	// And it can't come back: the bearer is gone.
	if _, resp, err := f.dial(t, "bearer-t2"); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("redial after revoke: resp=%v err=%v, want 401", resp, err)
	}
}

func TestSyncLink_AdminChangeNudgesAdmin(t *testing.T) {
	f := newSyncLinkFixture(t)
	f.enrol(t, "Till 2", "bearer-t2")
	c, _, err := f.dial(t, "bearer-t2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nextLinkFrame(t, c); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	runLinkAdminWatch(ctx, f.dp, &wg, 20*time.Millisecond, data.NewSyncAdminRepo(f.dp.Db))
	t.Cleanup(func() { cancel(); wg.Wait() })
	time.Sleep(60 * time.Millisecond) // let the watch take its baseline

	// A user/role/PIN-class admin write (#2731): any admin table bumps
	// sync_admin_version, the settings table included.
	if err := f.dp.Settings.Set(context.Background(), "store.name", "Changed shop"); err != nil {
		t.Fatal(err)
	}
	waitSync(t, c, fleetlink.ScopeAdmin)
}

// ut-docs#2792: a per-till settings write (the entitlement refresh on every
// cloud tick, a printer, a sync cursor) bumps sync_admin_version through the
// settings trigger, but the bundle a replica pulls is unchanged — so it must
// not nudge every linked till into a full admin re-pull.
func TestSyncLink_PerTillSettingWriteDoesNotNudge(t *testing.T) {
	f := newSyncLinkFixture(t)
	f.enrol(t, "Till 2", "bearer-t2")
	c, _, err := f.dial(t, "bearer-t2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nextLinkFrame(t, c); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	runLinkAdminWatch(ctx, f.dp, &wg, 20*time.Millisecond, data.NewSyncAdminRepo(f.dp.Db))
	t.Cleanup(func() { cancel(); wg.Wait() })
	time.Sleep(60 * time.Millisecond) // let the watch take its baseline

	for i := 0; i < 3; i++ {
		if err := f.dp.Settings.Set(context.Background(), "entitlement.last_confirmed_at", fmt.Sprintf("2026-09-25T14:0%d:00Z", i)); err != nil {
			t.Fatal(err)
		}
		if err := f.dp.Settings.Set(context.Background(), "printer.host", fmt.Sprintf("10.0.0.%d", i)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(60 * time.Millisecond) // several watch ticks per write
	}
	// A real admin change still nudges — and must be the first sync frame.
	if err := f.dp.Settings.Set(context.Background(), "store.name", "Changed shop"); err != nil {
		t.Fatal(err)
	}
	e, err := nextLinkFrame(t, c)
	if err != nil {
		t.Fatalf("waiting for the admin nudge: %v", err)
	}
	if e.Type != fleetlink.TypeSync {
		t.Fatalf("first frame after the writes = %s, want the store.name sync nudge", e.Type)
	}
	// Anything already queued before store.name was a spurious nudge; the
	// fixture has no other writer, so exactly one admin nudge is expected.
	readCtx, readCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer readCancel()
	for {
		_, b, err := c.Read(readCtx)
		if err != nil {
			break
		}
		if extra, derr := fleetlink.Decode(b); derr == nil && extra.Type == fleetlink.TypeSync {
			t.Fatalf("a second sync nudge arrived: per-till settings writes nudged the linked till (%s)", b)
		}
	}
}

func TestSyncLink_ChangePointsNudgeTheirScopes(t *testing.T) {
	f := newSyncLinkFixture(t)
	f.enrol(t, "Till 2", "bearer-t2")
	c, _, err := f.dial(t, "bearer-t2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nextLinkFrame(t, c); err != nil {
		t.Fatal(err)
	}
	// Plugin lifecycle: ReloadPlugins is the one choke point every
	// install/enable/disable/update/rollback goes through.
	if err := f.dp.ReloadPlugins(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitSync(t, c, fleetlink.ScopePlugins)
	// A completed sale on the main till (the RequestSyncPush call sites).
	f.dp.RequestSyncPush()
	waitSync(t, c, fleetlink.ScopeStock)
}

func TestSyncLink_JournalIngestNudgesStockAndOrders(t *testing.T) {
	f := newSyncLinkFixture(t)
	f.enrol(t, "Till 2", "bearer-t2")
	registerSyncSales(f.mux, f.dp)
	c, _, err := f.dial(t, "bearer-t2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nextLinkFrame(t, c); err != nil {
		t.Fatal(err)
	}
	// An empty batch applies nothing and must not nudge; a batch that
	// applies a sale must.
	req := httptest.NewRequest(http.MethodPost, "/api/sync/sales", strings.NewReader(`[]`))
	req.Header.Set("Authorization", "Bearer bearer-t2")
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty batch = %d %s", rec.Code, rec.Body)
	}
	f.dp.Link.Nudge(fleetlink.ScopeTables) // marker: the next sync frame must be tables only
	e, err := nextLinkFrame(t, c)
	if err != nil {
		t.Fatal(err)
	}
	var p fleetlink.SyncPayload
	_ = json.Unmarshal(e.Payload, &p)
	if e.Type != fleetlink.TypeSync || len(p.Scopes) != 1 || p.Scopes[0] != fleetlink.ScopeTables {
		t.Fatalf("after an empty batch got %s %s, want only the tables marker", e.Type, e.Payload)
	}

	j := seedJournalSale("sale-link-1", "R-LINK-1", "sale", "", "itm1", 1, 500)
	body, _ := json.Marshal([]journalSale{j})
	req = httptest.NewRequest(http.MethodPost, "/api/sync/sales", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer bearer-t2")
	rec = httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"applied":1`) {
		t.Fatalf("batch = %d %s", rec.Code, rec.Body)
	}
	e, err = nextLinkFrame(t, c)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(e.Payload, &p)
	if e.Type != fleetlink.TypeSync || scopesCSV(p.Scopes) != "stock,orders" {
		t.Fatalf("after an applied sale got %s %s, want sync stock,orders", e.Type, e.Payload)
	}
}

func scopesCSV(s []fleetlink.Scope) string {
	parts := make([]string, len(s))
	for i, v := range s {
		parts[i] = string(v)
	}
	return strings.Join(parts, ",")
}

func TestSyncPing_AdvertisesLink(t *testing.T) {
	f := newSyncLinkFixture(t)
	f.enrol(t, "Till 2", "bearer-t2")
	req := httptest.NewRequest(http.MethodGet, "/api/sync/ping", nil)
	req.Header.Set("Authorization", "Bearer bearer-t2")
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	var out struct {
		Data struct {
			TillID string `json:"till_id"`
			Link   int    `json:"link"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: %v", rec.Body, err)
	}
	if out.Data.Link != 1 || out.Data.TillID == "" {
		t.Fatalf("ping = %s, want link:1", rec.Body)
	}
}

func TestSyncLink_FrameTouchesLastSeen(t *testing.T) {
	f := newSyncLinkFixture(t)
	tillID := f.enrol(t, "Till 2", "bearer-t2")
	c, _, err := f.dial(t, "bearer-t2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nextLinkFrame(t, c); err != nil {
		t.Fatal(err)
	}
	// Clear last_seen_at (the upgrade's syncTill set it), then send a frame.
	if _, err := f.dp.Db.Exec(`UPDATE tills SET last_seen_at = NULL WHERE id = ?`, tillID); err != nil {
		t.Fatal(err)
	}
	b, _ := fleetlink.Encode(fleetlink.Envelope{ID: "r1", Type: fleetlink.TypePong})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
	ok := waitFor(t, 2*time.Second, func() bool {
		list, _ := data.NewTillsRepo(f.dp.Db).ListTills(context.Background())
		for _, r := range list {
			if r.ID == tillID {
				return r.LastSeenAt != ""
			}
		}
		return false
	})
	if !ok {
		t.Fatal("a link frame never refreshed tills.last_seen_at")
	}
}

// TestSyncLink_PeriodicTableReaffirmDoesNotNudge (review, ADR-0114 §2/§3):
// the held-order re-affirm runs every runSyncLoop tick for every held table
// and only refreshes a claim that already holds. Nudging `tables` from it
// would turn the link into a 30 s tables poll for every linked till whenever
// any table is held — the cadence the link exists to retire. Only an
// operator's claim nudges: locally on the main till, and over a replica's
// proxy, which marks its periodic calls periodic=1.
func TestSyncLink_PeriodicTableReaffirmDoesNotNudge(t *testing.T) {
	f := newSyncLinkFixture(t)
	f.enrol(t, "Till 2", "bearer-t2")
	registerSyncTablesClaim(f.mux, f.dp)
	c, _, err := f.dial(t, "bearer-t2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nextLinkFrame(t, c); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	posRepo := data.NewPOSRepo(f.dp.Db)
	t1, err := posRepo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	t2, err := posRepo.CreateTable(ctx, "T2", "", 4, "rect", 200, 100)
	if err != nil {
		t.Fatal(err)
	}
	expectOnlyMarker := func(what string) {
		t.Helper()
		f.dp.Link.Nudge(fleetlink.ScopeOrders) // marker: the next sync frame must be orders only
		e, err := nextLinkFrame(t, c)
		if err != nil {
			t.Fatal(err)
		}
		var p fleetlink.SyncPayload
		_ = json.Unmarshal(e.Payload, &p)
		if e.Type != fleetlink.TypeSync || scopesCSV(p.Scopes) != "orders" {
			t.Fatalf("%s: got %s %s, want only the orders marker", what, e.Type, e.Payload)
		}
	}
	postClaim := func(form string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/sync/tables/claim", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Authorization", "Bearer bearer-t2")
		rec := httptest.NewRecorder()
		f.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"claimed":true`) {
			t.Fatalf("claim %q = %d %s", form, rec.Code, rec.Body)
		}
	}

	// The main till's own periodic re-affirm (no primary: the local branch).
	if claimed, err := claimTableWriteThrough(ctx, f.dp, posRepo, t1, true); err != nil || !claimed {
		t.Fatalf("periodic re-affirm: claimed=%v err=%v", claimed, err)
	}
	expectOnlyMarker("main till periodic re-affirm")
	// A replica's periodic re-affirm over the proxy.
	postClaim("table_id=" + t2 + "&periodic=1")
	expectOnlyMarker("replica periodic re-affirm")

	// Operator claims nudge, both ways.
	if claimed, err := claimTableWriteThrough(ctx, f.dp, posRepo, t1, false); err != nil || !claimed {
		t.Fatalf("operator claim: claimed=%v err=%v", claimed, err)
	}
	waitSync(t, c, fleetlink.ScopeTables)
	postClaim("table_id=" + t2)
	waitSync(t, c, fleetlink.ScopeTables)
}
