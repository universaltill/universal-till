package pages

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/lantls"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// The joining till's pending link, main-till side (ADR-0114 amendment
// 2026-11-03, ut-docs#5297 slice B1). T-numbers are the Architect's.

const pendingTestSecret = "s3cr3t-0123456789abcdef0123456789abcdef0123456789abcdef"

func commitmentOf(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// countingPairings counts GetByID calls (T13: the limiter runs before any
// database read).
type countingPairings struct {
	inner pairingGetter
	calls atomic.Int32
	// script, when set, may replace the answer to call n (1-based): the
	// first call is the handler's verification, the second the registration
	// recheck. It gets the real answer and returns what to hand back.
	script func(n int32, row data.PendingPairingRow, ok bool, err error) (data.PendingPairingRow, bool, error)
}

func (c *countingPairings) GetByID(ctx context.Context, id string) (data.PendingPairingRow, bool, error) {
	n := c.calls.Add(1)
	row, ok, err := c.inner.GetByID(ctx, id)
	if c.script != nil {
		return c.script(n, row, ok, err)
	}
	return row, ok, err
}

type pendingLinkFixture struct {
	dp      *common.Deps
	mux     *http.ServeMux
	base    string // http://host:port (same port serves TLS and plain)
	cert    *lantls.Cert
	repo    *data.PairingRepo
	hc      *http.Client // pinned to the till's key, no overall timeout
	counted *countingPairings
}

type fixtureOpts struct {
	limiter *pairRateLimiter
}

func newPendingLinkFixture(t *testing.T, o fixtureOpts) *pendingLinkFixture {
	t.Helper()
	t.Setenv("UT_AUTH", "off") // authorizePairingManager only; the middleware below is always on
	dp := newSyncPairingGateTestDeps(t)
	mux := http.NewServeMux()
	tokens := registerSyncAPI(mux, dp)
	registerPairingAPI(mux, dp, dp.AuthSvc, tokens)
	adminRepo := registerSyncAdmin(mux, dp)
	cfg := fleetlink.DefaultConfig()
	cfg.PingInterval = 50 * time.Millisecond
	dp.Link = newSyncLinkHub(dp, cfg, adminRepo)
	dp.PendingLinks = fleetlink.NewPendingHub(cfg)
	repo := data.NewPairingRepo(dp.Db)
	counted := &countingPairings{inner: repo}
	lim := o.limiter
	if lim == nil {
		lim = newPairRateLimiter(time.Minute, 1000)
	}
	registerSyncLinkWith(mux, dp, data.NewTillsRepo(dp.Db), counted, lim)
	base, cert := serveTLSTill(t, auth.Middleware(mux, auth.NewService(dp.Db)))
	t.Cleanup(func() {
		dp.PendingLinks.Close()
		dp.Link.Close()
	})
	pc := lantls.NewPinnedClient(0, cert.Pin())
	hc := *pc.Client
	hc.Timeout = 0
	return &pendingLinkFixture{dp: dp, mux: mux, base: base, cert: cert, repo: repo, hc: &hc, counted: counted}
}

func (f *pendingLinkFixture) newRequest(t *testing.T, secret string) string {
	t.Helper()
	id, err := f.repo.CreatePendingRequestWithRole(t.Context(), "Bar Till", commitmentOf(secret), data.TillRoleAdditional, "", pairingRequestTTL)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *pendingLinkFixture) wss() string {
	return "wss" + strings.TrimPrefix(f.base, "http") + "/api/sync/link"
}

func pairHeaders(id, secret string) http.Header {
	h := http.Header{}
	if id != "" {
		h.Set(fleetlink.HeaderPairRequestID, id)
	}
	if secret != "" {
		h.Set(fleetlink.HeaderPairRequestSecret, secret)
	}
	return h
}

// dial opens a pending link over pinned TLS.
func (f *pendingLinkFixture) dial(t *testing.T, id, secret string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, f.wss(), &websocket.DialOptions{HTTPClient: f.hc, HTTPHeader: pairHeaders(id, secret)})
	if c != nil {
		t.Cleanup(func() { _ = c.CloseNow() })
	}
	return c, resp, err
}

type snapshot struct {
	code int
	body string
	hdr  string
}

// probe sends a plain GET (no upgrade) and captures what a prober sees.
func (f *pendingLinkFixture) probe(t *testing.T, rawURL string, hdr http.Header) snapshot {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := f.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var lines []string
	for k, v := range resp.Header {
		if k == "Date" {
			continue
		}
		lines = append(lines, k+"="+strings.Join(v, ","))
	}
	sort.Strings(lines)
	return snapshot{resp.StatusCode, string(b), strings.Join(lines, ";")}
}

func (f *pendingLinkFixture) httpsURL(path string) string {
	return "https" + strings.TrimPrefix(f.base, "http") + path
}

// readPairing waits for the pairing frame and the close that follows it.
func readPairing(t *testing.T, c *websocket.Conn) (status string, closeCode websocket.StatusCode) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read before a pairing frame: %v", err)
		}
		e, err := fleetlink.Decode(b)
		if err != nil {
			t.Fatalf("undecodable %s", b)
		}
		if e.Type == fleetlink.TypePing {
			continue
		}
		if e.Type != fleetlink.TypePairing {
			t.Fatalf("frame %q, want pairing", e.Type)
		}
		var p map[string]string
		if json.Unmarshal(e.Payload, &p) != nil || len(p) != 1 {
			t.Fatalf("payload %s", e.Payload)
		}
		status = p["status"]
		break
	}
	for {
		_, _, err := c.Read(ctx)
		if err != nil {
			return status, websocket.CloseStatus(err)
		}
	}
}

func (f *pendingLinkFixture) approve(t *testing.T, id string) {
	t.Helper()
	if rec := doPostForm(t, f.mux, "/api/sync/pair-requests/"+id+"/approve", url.Values{}); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
}

func (f *pendingLinkFixture) deny(t *testing.T, id string) {
	t.Helper()
	if rec := doPostForm(t, f.mux, "/api/sync/pair-requests/"+id+"/deny", url.Values{}); rec.Code != http.StatusOK {
		t.Fatalf("deny: %d %s", rec.Code, rec.Body)
	}
}

// waitLive reads until the link's first ping: the server side has then
// registered the link and its writer runs (there is no production accessor
// for the link count; the deadcode baseline forbids test-only code).
func waitLive(t *testing.T, c *websocket.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("waiting for the link to go live: %v", err)
	}
	if e, derr := fleetlink.Decode(b); derr != nil || e.Type != fleetlink.TypePing {
		t.Fatalf("first frame %s, want a ping", b)
	}
}

// An accepted open, then the manager approves: one pairing frame, normal
// close, link gone (AC7, AC14).
func TestPendingLink_AcceptedThenApproveSendsOneFrameAndCloses(t *testing.T) {
	f := newPendingLinkFixture(t, fixtureOpts{})
	id := f.newRequest(t, pendingTestSecret)
	c, _, err := f.dial(t, id, pendingTestSecret)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	waitLive(t, c)
	f.approve(t, id)
	st, code := readPairing(t, c)
	if st != "approved" || code != websocket.StatusNormalClosure {
		t.Fatalf("status %q close %d", st, code)
	}
}

func TestPendingLink_DenySendsDeniedAndCloses(t *testing.T) {
	f := newPendingLinkFixture(t, fixtureOpts{})
	id := f.newRequest(t, pendingTestSecret)
	c, _, err := f.dial(t, id, pendingTestSecret)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	waitLive(t, c)
	f.deny(t, id)
	if st, code := readPairing(t, c); st != "denied" || code != websocket.StatusNormalClosure {
		t.Fatalf("status %q close %d", st, code)
	}
}

// Another request's link is untouched by this one's approval (AC7).
func TestPendingLink_ApproveReachesOnlyItsOwnRequest(t *testing.T) {
	f := newPendingLinkFixture(t, fixtureOpts{})
	idA := f.newRequest(t, pendingTestSecret)
	idB := f.newRequest(t, "other-secret-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a, _, err := f.dial(t, idA, pendingTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := f.dial(t, idB, "other-secret-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	waitLive(t, a)
	waitLive(t, b)
	f.approve(t, idA)
	readPairing(t, a)
	// B is still open: its next frame is a ping, not a pairing frame.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, raw, err := b.Read(ctx)
	if err != nil {
		t.Fatalf("B was closed by A's approval: %v", err)
	}
	if e, _ := fleetlink.Decode(raw); e.Type != fleetlink.TypePing {
		t.Fatalf("B saw %q", e.Type)
	}
}

// An already-approved request is refused at the door (the branch only admits
// a still-pending one).
func TestPendingLink_ApprovedBeforeOpenIsRefused(t *testing.T) {
	f := newPendingLinkFixture(t, fixtureOpts{})
	id := f.newRequest(t, pendingTestSecret)
	f.approve(t, id)
	if _, resp, err := f.dial(t, id, pendingTestSecret); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("dial after approve: err %v resp %v", err, resp)
	}
}

// The race: the request is resolved after the handler verified it but before
// the link registered. The pages-level recheck (the second GetByID) must
// resolve the link at once, with the right status.
func TestPendingLink_RecheckAtRegistration(t *testing.T) {
	resolved := func(f *pendingLinkFixture, mut func(row data.PendingPairingRow) (data.PendingPairingRow, bool, error)) {
		f.counted.script = func(n int32, row data.PendingPairingRow, ok bool, err error) (data.PendingPairingRow, bool, error) {
			if n == 2 {
				return mut(row)
			}
			return row, ok, err
		}
	}
	t.Run("approved", func(t *testing.T) {
		f := newPendingLinkFixture(t, fixtureOpts{})
		id := f.newRequest(t, pendingTestSecret)
		resolved(f, func(row data.PendingPairingRow) (data.PendingPairingRow, bool, error) {
			row.Status = "approved"
			return row, true, nil
		})
		c, _, err := f.dial(t, id, pendingTestSecret)
		if err != nil {
			t.Fatal(err)
		}
		if st, code := readPairing(t, c); st != fleetlink.PairingApproved || code != websocket.StatusNormalClosure {
			t.Fatalf("got %q close %d, want approved + 1000", st, code)
		}
	})
	t.Run("denied_row_gone_before_expiry", func(t *testing.T) {
		f := newPendingLinkFixture(t, fixtureOpts{})
		id := f.newRequest(t, pendingTestSecret)
		resolved(f, func(row data.PendingPairingRow) (data.PendingPairingRow, bool, error) {
			return data.PendingPairingRow{}, false, nil
		})
		c, _, err := f.dial(t, id, pendingTestSecret)
		if err != nil {
			t.Fatal(err)
		}
		if st, code := readPairing(t, c); st != fleetlink.PairingDenied || code != websocket.StatusNormalClosure {
			t.Fatalf("got %q close %d, want denied + 1000", st, code)
		}
	})
	t.Run("db_error_leaves_link_open", func(t *testing.T) {
		f := newPendingLinkFixture(t, fixtureOpts{})
		id := f.newRequest(t, pendingTestSecret)
		resolved(f, func(row data.PendingPairingRow) (data.PendingPairingRow, bool, error) {
			return data.PendingPairingRow{}, false, errors.New("db down")
		})
		c, _, err := f.dial(t, id, pendingTestSecret)
		if err != nil {
			t.Fatal(err)
		}
		waitLive(t, c)
		// Still open and still resolvable by the manager.
		f.approve(t, id)
		if st, _ := readPairing(t, c); st != fleetlink.PairingApproved {
			t.Fatalf("got %q after approve", st)
		}
	})
}

// T11: every refusal is the same 401 — status, body and headers.
func TestPendingLink_T11_RefusalsAreByteIdentical(t *testing.T) {
	f := newPendingLinkFixture(t, fixtureOpts{})
	id := f.newRequest(t, pendingTestSecret)
	approvedID := f.newRequest(t, "approved-secret-cccccccccccccccccccccccccccccccccccccccccccccc")
	f.approve(t, approvedID)
	deniedID := f.newRequest(t, "denied-secret-dddddddddddddddddddddddddddddddddddddddddddddddd")
	f.deny(t, deniedID)
	expiredID := f.newRequest(t, "expired-secret-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	if _, err := f.dp.Db.ExecContext(t.Context(), `UPDATE pending_pairings SET expires_at = '2000-01-01T00:00:00Z' WHERE id = ?`, expiredID); err != nil {
		t.Fatal(err)
	}

	ref := f.probe(t, f.httpsURL("/api/sync/link"), nil) // the bearer path's own refusal
	if ref.code != http.StatusUnauthorized {
		t.Fatalf("reference refusal %d", ref.code)
	}
	good := f.httpsURL("/api/sync/link")
	cases := map[string]struct {
		url string
		hdr http.Header
	}{
		"no secret":            {good, pairHeaders(id, "")},
		"no id":                {good, pairHeaders("", pendingTestSecret)},
		"wrong secret":         {good, pairHeaders(id, "nope")},
		"unknown id":           {good, pairHeaders("00000000-0000-0000-0000-000000000000", pendingTestSecret)},
		"overlong id":          {good, pairHeaders(strings.Repeat("a", 500), pendingTestSecret)},
		"expired id":           {good, pairHeaders(expiredID, "expired-secret-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")},
		"approved id":          {good, pairHeaders(approvedID, "approved-secret-cccccccccccccccccccccccccccccccccccccccccccccc")},
		"denied id":            {good, pairHeaders(deniedID, "denied-secret-dddddddddddddddddddddddddddddddddddddddddddddddd")},
		"other request secret": {good, pairHeaders(id, "another-secret-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")},
		"valid, plain HTTP":    {f.base + "/api/sync/link", pairHeaders(id, pendingTestSecret)},
		"valid header + query": {good + "?request_secret=" + pendingTestSecret, pairHeaders(id, pendingTestSecret)},
		"valid header + other": {good + "?x=1", pairHeaders(id, pendingTestSecret)},
		"secret in query only": {good + "?request_secret=" + pendingTestSecret + "&id=" + id, pairHeaders(id, "")},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := f.probe(t, c.url, c.hdr)
			if got != ref {
				t.Fatalf("refusal differs from the reference\n got: %+v\nwant: %+v", got, ref)
			}
		})
	}
}

// T12: the secret never reaches a log line, refused or accepted.
func TestPendingLink_T12_SecretNeverLogged(t *testing.T) {
	var buf bytes.Buffer
	restore := logging.CaptureForTest(&buf)
	defer restore()
	f := newPendingLinkFixture(t, fixtureOpts{})
	id := f.newRequest(t, pendingTestSecret)
	f.probe(t, f.httpsURL("/api/sync/link"), pairHeaders(id, "wrong-"+pendingTestSecret))
	f.probe(t, f.httpsURL("/api/sync/link?request_secret="+pendingTestSecret), pairHeaders(id, pendingTestSecret))
	f.probe(t, f.base+"/api/sync/link", pairHeaders(id, pendingTestSecret))
	c, _, err := f.dial(t, id, pendingTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	waitLive(t, c)
	f.approve(t, id)
	readPairing(t, c)
	f.dp.PendingLinks.Close()
	if strings.Contains(buf.String(), pendingTestSecret) || strings.Contains(buf.String(), commitmentOf(pendingTestSecret)) {
		t.Fatalf("secret or commitment in logs:\n%s", buf.String())
	}
}

// T13: the dedicated limiter answers 429 before the replica/replaced checks
// (the settings reads) and before the credential lookup: a limited request is
// 429 even on a replica, and reads no pairing row.
func TestPendingLink_T13_LimiterRunsBeforeReplicaReplacedAndCredentialChecks(t *testing.T) {
	f := newPendingLinkFixture(t, fixtureOpts{limiter: newPairRateLimiter(time.Minute, 5)})
	id := f.newRequest(t, pendingTestSecret)
	for i := range 5 {
		if got := f.probe(t, f.httpsURL("/api/sync/link"), pairHeaders(id, "wrong")); got.code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i+1, got.code)
		}
	}
	if n := f.counted.calls.Load(); n != 5 {
		t.Fatalf("GetByID calls %d after 5 attempts, want 5", n)
	}
	got := f.probe(t, f.httpsURL("/api/sync/link"), pairHeaders(id, pendingTestSecret)) // even a valid one
	if got.code != http.StatusTooManyRequests || !strings.Contains(got.hdr, "Retry-After=60") {
		t.Fatalf("6th: %+v", got)
	}
	if n := f.counted.calls.Load(); n != 5 {
		t.Fatalf("the limited request read the pairing table (%d calls)", n)
	}
	// A replica would answer 409 from a settings read; limited, it is 429.
	if err := f.dp.Settings.Set(t.Context(), "sync.primary_url", "https://main.example"); err != nil {
		t.Fatal(err)
	}
	if got := f.probe(t, f.httpsURL("/api/sync/link"), pairHeaders(id, pendingTestSecret)); got.code != http.StatusTooManyRequests {
		t.Fatalf("limited request on a replica: %d, want 429 (the settings read ran before the limiter)", got.code)
	}
	if err := f.dp.Settings.Set(t.Context(), "sync.primary_url", ""); err != nil {
		t.Fatal(err)
	}
	// Link opens never spend the per-id GET budget (AC11): the token fetch
	// from the same source still works.
	f.approve(t, id)
	req := httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests/"+id, nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set(fleetlink.HeaderPairRequestSecret, pendingTestSecret)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"token"`) {
		t.Fatalf("token fetch after link attempts: %d %s", rec.Code, rec.Body)
	}
}

// T14: a bearer plus pairing headers is refused; a bearer alone is the
// unchanged peer path.
func TestPendingLink_T14_BearerAndPairingHeadersRefused(t *testing.T) {
	f := newPendingLinkFixture(t, fixtureOpts{})
	id := f.newRequest(t, pendingTestSecret)
	if _, err := data.NewTillsRepo(f.dp.Db).InsertTill(t.Context(), "Bar", hashBearer("good-bearer")); err != nil {
		t.Fatal(err)
	}
	ref := f.probe(t, f.httpsURL("/api/sync/link"), nil)
	both := pairHeaders(id, pendingTestSecret)
	both.Set("Authorization", "Bearer good-bearer")
	if got := f.probe(t, f.httpsURL("/api/sync/link"), both); got != ref {
		t.Fatalf("bearer+pairing headers: %+v, want the common refusal %+v", got, ref)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, f.wss(), &websocket.DialOptions{HTTPClient: f.hc,
		HTTPHeader: http.Header{"Authorization": {"Bearer good-bearer"}}})
	if err != nil {
		t.Fatalf("bearer alone: %v", err)
	}
	defer c.CloseNow()
	_, b, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if e, derr := fleetlink.Decode(b); derr != nil || e.Type != fleetlink.TypeHello {
		t.Fatalf("bearer link first frame %s, want hello", b)
	}
}

// T15: a replica holds no pending links; the demo refuses the route.
func TestPendingLink_T15_ReplicaAndDemo(t *testing.T) {
	f := newPendingLinkFixture(t, fixtureOpts{})
	id := f.newRequest(t, pendingTestSecret)
	if err := f.dp.Settings.Set(t.Context(), "sync.primary_url", "https://main.example"); err != nil {
		t.Fatal(err)
	}
	if got := f.probe(t, f.httpsURL("/api/sync/link"), pairHeaders(id, pendingTestSecret)); got.code != http.StatusConflict {
		t.Fatalf("replica: %d, want 409", got.code)
	}
	if f.counted.calls.Load() != 0 {
		t.Fatal("a replica read the pairing table")
	}

	dm := newDemoMiddleware(f.mux, f.mux, "tok")
	req := httptest.NewRequest(http.MethodGet, "/api/sync/link", nil)
	req.Header.Set(demoTokenHeader, "tok")
	for k, v := range pairHeaders(id, pendingTestSecret) {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	dm.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("demo: %d, want 403", rec.Code)
	}
}

// T16: the route is reachable without a session (the auth exempt list is
// unchanged) — every probe above went through auth.Middleware. Pin it
// explicitly with no cookie and no headers.
func TestPendingLink_T16_NoNewAuthExemption(t *testing.T) {
	f := newPendingLinkFixture(t, fixtureOpts{})
	got := f.probe(t, f.httpsURL("/api/sync/link"), nil)
	if got.code != http.StatusUnauthorized || !strings.Contains(got.body, "unauthorized") || strings.Contains(got.hdr, "Location=") {
		t.Fatalf("%+v: expected the handler's own 401, not a login redirect", got)
	}
	// A path next to it is NOT exempt.
	other := f.probe(t, f.httpsURL("/api/sync/link/x"), pairHeaders("a", "b"))
	if other.body == got.body && other.code == got.code {
		t.Fatal("/api/sync/link/x reached the sync handler without a session")
	}
}

// No-network service (AC14): shutdown says bye and closes.
func TestPendingLink_ShutdownClosesLinks(t *testing.T) {
	f := newPendingLinkFixture(t, fixtureOpts{})
	id := f.newRequest(t, pendingTestSecret)
	c, _, err := f.dial(t, id, pendingTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	waitLive(t, c)
	f.dp.PendingLinks.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	sawBye := false
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			break
		}
		if e, _ := fleetlink.Decode(b); e.Type == fleetlink.TypeBye {
			sawBye = true
		}
	}
	if !sawBye {
		t.Fatal("no bye on shutdown")
	}
}

// Goroutines settle after approve, deny, disconnect and shutdown (AC16).
func TestPendingLink_NoGoroutineLeak(t *testing.T) {
	f := newPendingLinkFixture(t, fixtureOpts{})
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	base := runtime.NumGoroutine()
	for _, end := range []string{"approve", "deny", "disconnect", "shutdown"} {
		id := f.newRequest(t, pendingTestSecret)
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		c, _, err := websocket.Dial(ctx, f.wss(), &websocket.DialOptions{HTTPClient: f.hc, HTTPHeader: pairHeaders(id, pendingTestSecret)})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		waitLive(t, c)
		switch end {
		case "approve":
			f.approve(t, id)
			readPairing(t, c)
		case "deny":
			f.deny(t, id)
			readPairing(t, c)
		case "disconnect":
			_ = c.CloseNow()
		case "shutdown":
			f.dp.PendingLinks.Close()
			_ = c.CloseNow()
		}
		_ = c.CloseNow()
	}
	f.hc.CloseIdleConnections()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && runtime.NumGoroutine() > base+2 {
		time.Sleep(50 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > base+2 {
		buf := make([]byte, 1<<16)
		t.Fatalf("goroutines %d > base %d\n%s", n, base, buf[:runtime.Stack(buf, true)])
	}
}

// T19 + the advertisement: the per-id GET takes the secret from the header
// and still from the legacy query; the pair-request answer says
// pending_link.
func TestPendingLink_T19_PerIDGetAcceptsHeaderAndLegacyQuery(t *testing.T) {
	f := newPendingLinkFixture(t, fixtureOpts{})
	id := f.newRequest(t, pendingTestSecret)
	f.approve(t, id)
	get := func(path string, hdr http.Header) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for k, v := range hdr {
			req.Header[k] = v
		}
		rec := httptest.NewRecorder()
		f.mux.ServeHTTP(rec, req)
		return rec
	}
	p := "/api/sync/pair-requests/" + id
	if rec := get(p, pairHeaders("", pendingTestSecret)); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"token"`) {
		t.Fatalf("header: %d %s", rec.Code, rec.Body)
	}
	if rec := get(p+"?request_secret="+pendingTestSecret, nil); rec.Code != http.StatusOK {
		t.Fatalf("legacy query: %d", rec.Code)
	}
	if rec := get(p, pairHeaders("", "wrong")); rec.Code != http.StatusNotFound {
		t.Fatalf("wrong header secret: %d", rec.Code)
	}
	if rec := get(p, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("no secret: %d", rec.Code)
	}

	rec := doPostJSON(t, f.mux, "/api/sync/pair-request",
		`{"device_name":"Bar","commitment":"`+commitmentOf("x")+`"}`)
	var out struct {
		Data struct {
			ID          string `json:"id"`
			PendingLink bool   `json:"pending_link"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Data.ID == "" || !out.Data.PendingLink {
		t.Fatalf("pair-request answer %s (%v)", rec.Body, err)
	}
}

func doPostJSON(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
