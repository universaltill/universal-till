package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pairingpush"
)

// Pairing push over SSE (ut-docs#2743, ADR-0114 §2 -> ADR-0079 §1):
// GET /api/pairings/stream streams a payload-free `event: pairing` per
// pending-pairing change; the browser re-fetches the existing fragments.

func newPairingStreamRig(t *testing.T, maxStreams int) (*httptest.Server, *common.Deps, *http.ServeMux) {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	dp.PairingPush = pairingpush.New(maxStreams)
	t.Cleanup(dp.PairingPush.Close)
	registerPendingPairingsUI(mux, dp)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, dp, mux
}

func postCreate(t *testing.T, mux *http.ServeMux, name, secret, addr string) string {
	t.Helper()
	rec := postPairRequest(t, mux, name, commitOf(secret), addr)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Data.ID
}

func assertPairingFrame(t *testing.T, block []string, forbidden ...string) {
	t.Helper()
	if len(block) != 2 || block[0] != "event: pairing" || block[1] != "data: {}" {
		t.Fatalf("frame = %q, want [event: pairing, data: {}]", block)
	}
	for _, f := range forbidden {
		if strings.Contains(strings.Join(block, "\n"), f) {
			t.Fatalf("frame leaks %q: %q", f, block)
		}
	}
}

func TestPairingStream_Headers200(t *testing.T) {
	srv, _, _ := newPairingStreamRig(t, 4)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	resp, _ := openOrderStream(t, ctx, srv.URL+"/api/pairings/stream", "")
	assertStreamHeaders(t, resp)
}

func TestPairingStream_ForbiddenWithoutSyncManagementAndSlotNotConsumed(t *testing.T) {
	srv, dp, _ := newPairingStreamRig(t, 1)
	t.Setenv("UT_AUTH", "on") // no session -> canPerform false
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	resp, _ := openOrderStream(t, ctx, srv.URL+"/api/pairings/stream", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	_, cancelSub, ok := dp.PairingPush.Subscribe()
	if !ok {
		t.Fatal("a refused request consumed a subscription slot")
	}
	cancelSub()
}

func TestPairingStream_ReplicaGets204(t *testing.T) {
	srv, dp, _ := newPairingStreamRig(t, 4)
	if err := dp.Settings.Set(t.Context(), "sync.primary_url", "http://10.0.0.1:8080"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	resp, _ := openOrderStream(t, ctx, srv.URL+"/api/pairings/stream", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	if _, cancelSub, ok := dp.PairingPush.Subscribe(); !ok {
		t.Fatal("replica request consumed a slot")
	} else {
		cancelSub()
	}
}

func TestPairingStream_CapReached503(t *testing.T) {
	srv, _, _ := newPairingStreamRig(t, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	resp, _ := openOrderStream(t, ctx, srv.URL+"/api/pairings/stream", "")
	assertStreamHeaders(t, resp)
	resp2, _ := openOrderStream(t, ctx, srv.URL+"/api/pairings/stream", "")
	if resp2.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("over-cap status = %d, want 503", resp2.StatusCode)
	}
}

func TestPairingStream_CreateApproveDenyEachNudgeOnce(t *testing.T) {
	srv, _, mux := newPairingStreamRig(t, 4)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	resp, br := openOrderStream(t, ctx, srv.URL+"/api/pairings/stream", "")
	assertStreamHeaders(t, resp)

	idA := postCreate(t, mux, "Secret Kitchen Till", "s-a", "10.0.0.70:1")
	assertPairingFrame(t, readSSEBlock(t, br), "Secret Kitchen Till", idA)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+idA+"/approve", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	assertPairingFrame(t, readSSEBlock(t, br))

	idB := postCreate(t, mux, "Bar Till", "s-b", "10.0.0.71:1")
	assertPairingFrame(t, readSSEBlock(t, br), idB)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+idB+"/deny", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("deny: %d %s", rec.Code, rec.Body.String())
	}
	assertPairingFrame(t, readSSEBlock(t, br))
}

func TestPairingStream_NoNudgeOnRefusedCreate(t *testing.T) {
	_, dp, mux := newPairingStreamRig(t, 4)
	ch, cancel, _ := dp.PairingPush.Subscribe()
	defer cancel()
	rec := postPairRequest(t, mux, "", "bad", "10.0.0.72:1") // 400
	if rec.Code == http.StatusOK {
		t.Fatal("expected refusal")
	}
	select {
	case <-ch:
		t.Fatal("a refused request published a nudge")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestPairingStream_ExpiryNudgeScheduledAtTTLPlusOneSecond(t *testing.T) {
	_, dp, mux := newPairingStreamRig(t, 4)
	var gotDelay time.Duration
	var fire func()
	orig := pairingExpiryAfter
	pairingExpiryAfter = func(d time.Duration, f func()) *time.Timer {
		gotDelay, fire = d, f
		return nil
	}
	t.Cleanup(func() { pairingExpiryAfter = orig })

	ch, cancel, _ := dp.PairingPush.Subscribe()
	defer cancel()
	postCreate(t, mux, "Expiring Till", "s-e", "10.0.0.73:1")
	<-ch // drain the create nudge
	if gotDelay != pairingRequestTTL+time.Second || fire == nil {
		t.Fatalf("expiry scheduled after %v (fire=%v), want %v", gotDelay, fire != nil, pairingRequestTTL+time.Second)
	}
	fire()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("expiry timer did not nudge")
	}
}

func TestSchedulePendingPairingExpiryNudges_ScansExistingRows(t *testing.T) {
	_, dp, mux := newPairingStreamRig(t, 4)
	postCreate(t, mux, "Pre-restart Till", "s-r", "10.0.0.74:1")
	var delays []time.Duration
	orig := pairingExpiryAfter
	pairingExpiryAfter = func(d time.Duration, f func()) *time.Timer {
		delays = append(delays, d)
		return nil
	}
	t.Cleanup(func() { pairingExpiryAfter = orig })

	schedulePendingPairingExpiryNudges(t.Context(), dp)
	if len(delays) != 1 {
		t.Fatalf("scheduled %d timers, want 1", len(delays))
	}
	if delays[0] < pairingRequestTTL-5*time.Second || delays[0] > pairingRequestTTL+2*time.Second {
		t.Fatalf("delay = %v, want ~TTL+1s", delays[0])
	}
}

func TestPairingStream_Heartbeat(t *testing.T) {
	srv, _, _ := newPairingStreamRig(t, 4)
	orig := pairingStreamHeartbeat
	pairingStreamHeartbeat = 20 * time.Millisecond
	t.Cleanup(func() { pairingStreamHeartbeat = orig })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, br := openOrderStream(t, ctx, srv.URL+"/api/pairings/stream", "")
	if blk := readSSEBlock(t, br); len(blk) != 1 || blk[0] != ": ping" {
		t.Fatalf("heartbeat block = %q", blk)
	}
}

func TestPairingStream_DisconnectFreesSlotAndCloseEndsStream(t *testing.T) {
	srv, dp, _ := newPairingStreamRig(t, 1)
	ctx, cancel := context.WithCancel(t.Context())
	resp, br := openOrderStream(t, ctx, srv.URL+"/api/pairings/stream", "")
	assertStreamHeaders(t, resp)
	cancel()
	if !waitFor(t, 3*time.Second, func() bool {
		_, c, ok := dp.PairingPush.Subscribe()
		if ok {
			c()
		}
		return ok
	}) {
		t.Fatal("slot not freed after client disconnect")
	}

	ctx2, cancel2 := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel2()
	_, br2 := openOrderStream(t, ctx2, srv.URL+"/api/pairings/stream", "")
	_ = br
	dp.PairingPush.Close()
	if _, err := br2.ReadString('\n'); err == nil {
		t.Fatal("expected the stream to end after Close")
	}
}

func TestPairingNoticeUI_StreamMarkerOnlyForPrimaryManager(t *testing.T) {
	_, dp, mux := newPairingStreamRig(t, 4)
	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/pairing-notice", nil))
		return rec
	}
	rec := get()
	body := rec.Body.String()
	if !strings.Contains(body, `data-pairing-stream="/api/pairings/stream"`) {
		t.Fatalf("manager on primary with 0 pending must get the stream marker, got %q", body)
	}
	if strings.Contains(body, `id="pairing-notice"`) {
		t.Fatalf("no notice expected with 0 pending, got %q", body)
	}
	postCreate(t, mux, "Kitchen Till", "s-m", "10.0.0.75:1")
	body = get().Body.String()
	if !strings.Contains(body, `data-pairing-stream=`) || !strings.Contains(body, `id="pairing-notice"`) {
		t.Fatalf("pending: want both marker and notice, got %q", body)
	}
	if err := dp.Settings.Set(t.Context(), "sync.primary_url", "http://10.0.0.1:8080"); err != nil {
		t.Fatal(err)
	}
	if b := get().Body.String(); b != "" {
		t.Fatalf("replica must get an empty body, got %q", b)
	}
	t.Setenv("UT_AUTH", "on")
	if b := get().Body.String(); b != "" {
		t.Fatalf("no-session caller must get an empty body, got %q", b)
	}
}

func TestPendingPairingsFragment_TriggersGuardedPollAndPush(t *testing.T) {
	chdirRoot(t)
	raw, err := os.ReadFile("web/ui/partials/pending_pairings.html")
	if err != nil {
		t.Fatal(err)
	}
	b := string(raw)
	want := `hx-trigger="every 30s [!utPairingBusy()], tills-changed[!utPairingBusy()] from:body"`
	if !strings.Contains(b, want) {
		t.Fatalf("pending_pairings.html missing guarded triggers %s", want)
	}
	if strings.Contains(b, "aria-live") {
		t.Fatal("no aria-live on the pairing table (UX condition)")
	}
}

func TestPairingStreamJS_SingleSourceNoUserText(t *testing.T) {
	chdirRoot(t)
	raw, err := os.ReadFile("web/public/pairing-stream.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(raw)
	for _, must := range []string{"new EventSource", "tills-changed", "utPairingBusy", "data-pairing-stream"} {
		if !strings.Contains(js, must) {
			t.Fatalf("pairing-stream.js missing %q", must)
		}
	}
	// Review: the stream is for the manager on a dedicated (UT_KIOSK=1) main
	// till too — body.kiosk marks that till, not the self-order kiosk, which
	// never loads this file. And a refused stream is retried on a timer, not
	// on every htmx swap (F1).
	if strings.Contains(js, "'kiosk'") {
		t.Fatal("pairing-stream.js must not skip dedicated (body.kiosk) tills")
	}
	if !strings.Contains(js, "Date.now() >= retryAt") || !strings.Contains(js, "retryAt = Date.now() + RETRY_MS") {
		t.Fatal("pairing-stream.js must back off after a refused stream (F1)")
	}
	if strings.Count(js, "new EventSource") != 1 {
		t.Fatal("exactly one EventSource construction expected (one stream per page)")
	}
	base, _ := os.ReadFile("web/ui/layouts/base.html")
	if !strings.Contains(string(base), "/public/pairing-stream.js") {
		t.Fatal("base.html must load pairing-stream.js")
	}
}

// Review (ut-docs#2743 AC7): the middleware checks the session only at
// connect, so the stream re-checks it on every heartbeat and ends once the
// session is signed out (or idle-locked), instead of nudging a locked page
// and holding a cap slot until the tab closes.
func TestPairingStream_EndsWhenSessionSignedOut(t *testing.T) {
	_, dp, _ := newPairingStreamRig(t, 1)
	orig := pairingStreamHeartbeat
	pairingStreamHeartbeat = 20 * time.Millisecond
	t.Cleanup(func() { pairingStreamHeartbeat = orig })
	if _, err := dp.Db.Exec(`INSERT INTO users(id, username, display_name, pin_hash, role) VALUES ('mgr-s','mgr-s','mgr-s','x','manager')`); err != nil {
		t.Fatal(err)
	}
	token, err := dp.AuthSvc.CreateSession(t.Context(), "mgr-s")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/pairings/stream", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	req = auth.WithUser(req, auth.User{ID: "mgr-s", Role: "manager"})
	ctx, cancelReq := context.WithTimeout(req.Context(), 5*time.Second)
	defer cancelReq()
	req = req.WithContext(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		streamPairings(httptest.NewRecorder(), req, dp)
	}()
	select {
	case <-done:
		t.Fatal("stream ended while the session was still live")
	case <-time.After(150 * time.Millisecond):
	}
	dp.AuthSvc.Logout(t.Context(), token)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream kept running after the session was signed out")
	}
	if _, c, ok := dp.PairingPush.Subscribe(); !ok {
		t.Fatal("cap slot not released after the session ended")
	} else {
		c()
	}
}
