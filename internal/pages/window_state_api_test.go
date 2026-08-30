package pages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// GET /api/window-mode (ut-docs#611, reshaped by ADR-0064 / ut-docs#1039)
// is both the desktop shell's pre-login read of the display preferences AND
// the shell's live control poll. It must work unauthenticated (mirrors
// /healthz), and it must be fail-closed: chrome-hiding modes (kiosk,
// fullscreen) are served ONLY to a client advertising control=live — i.e.
// one that will really apply them and can also leave them — everything
// else gets "normal". The shell, not this handler, applies either
// preference (see settings_page.go's launch-on-startup handler comment).

// liveShellRequest builds a control-poll request the way the real shell
// makes one: from loopback. httptest.NewRequest's default RemoteAddr is
// 192.0.2.1 (TEST-NET), which the handler now rightly treats as a plain
// LAN client (review of ut-docs#1039, finding 6).
func liveShellRequest(target string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:54321"
	return req
}

func decodeWindowModeBody(t *testing.T, rec *httptest.ResponseRecorder) (mode string, launch bool, rev uint64) {
	t.Helper()
	var body struct {
		Data struct {
			WindowMode      string `json:"window_mode"`
			LaunchOnStartup bool   `json:"launch_on_startup"`
			Rev             uint64 `json:"rev"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
	}
	if body.Error != nil {
		t.Fatalf("error = %v, want nil", body.Error)
	}
	return body.Data.WindowMode, body.Data.LaunchOnStartup, body.Data.Rev
}

// TestWindowStateAPI_FailClosedDowngradesChromeHidingForPlainClients is
// the load-bearing test of ADR-0064 Decision 3: with kiosk live, a plain
// request (old shell, browser, curl, any other local process — anything
// that cannot promise a working exit) must be served "normal", while a
// control=live poll gets the real mode. Before ut-docs#1039 this endpoint
// served kiosk to everyone, which is exactly how a .deb install ended up
// fullscreen-undecorated with an exit button wired to a no-op.
func TestWindowStateAPI_FailClosedDowngradesChromeHidingForPlainClients(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	d.Shell.SetMode("kiosk")
	st := d.CurrentState()
	st.WindowMode = "kiosk"
	st.LaunchOnStartup = true
	d.SetState(st)

	// Plain request: downgraded to normal; launch_on_startup untouched.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/window-mode", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/window-mode = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	mode, launch, _ := decodeWindowModeBody(t, rec)
	if mode != "normal" {
		t.Fatalf("plain client window_mode = %q, want normal (fail-closed downgrade of live kiosk)", mode)
	}
	if !launch {
		t.Fatal("launch_on_startup = false, want true (not part of the downgrade)")
	}

	// control=live: the real mode, with its revision.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, liveShellRequest("/api/window-mode?control=live&applied="))
	mode, _, rev := decodeWindowModeBody(t, rec)
	if mode != "kiosk" {
		t.Fatalf("control=live window_mode = %q, want kiosk", mode)
	}
	if rev == 0 {
		t.Fatal("control=live rev = 0, want the channel's revision")
	}

	// Only the exact value "live" counts as the capability claim.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, liveShellRequest("/api/window-mode?control=yes-really"))
	if mode, _, _ = decodeWindowModeBody(t, rec); mode != "normal" {
		t.Fatalf("control=yes-really window_mode = %q, want normal (not an exact live claim)", mode)
	}
}

func TestWindowStateAPI_NonChromeHidingModeServedToEveryone(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	d.Shell.SetMode("maximized")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/window-mode", nil))
	if mode, _, _ := decodeWindowModeBody(t, rec); mode != "maximized" {
		t.Fatalf("plain client window_mode = %q, want maximized (only chrome-hiding modes downgrade)", mode)
	}
}

func TestWindowStateAPI_DefaultsToNormalAndStartupOff(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/window-mode", nil))
	mode, launch, _ := decodeWindowModeBody(t, rec)
	if mode != "normal" {
		t.Fatalf("window_mode = %q, want normal (common.DefaultWindowMode)", mode)
	}
	if launch {
		t.Fatal("launch_on_startup = true, want false (zero value)")
	}
}

// TestWindowStateAPI_LongPollReleasedBySetMode: a control=live request with
// wait= parked on the current revision must return promptly — with the new
// mode — when another goroutine changes it (the exit-to-os path).
func TestWindowStateAPI_LongPollReleasedBySetMode(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	d.Shell.SetMode("kiosk")
	_, rev := d.Shell.Snapshot()

	type result struct {
		mode string
		rev  uint64
	}
	done := make(chan result, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := liveShellRequest("/api/window-mode?control=live&wait=20&since=" + strconv.FormatUint(rev, 10) + "&applied=kiosk")
		mux.ServeHTTP(rec, req)
		var body struct {
			Data struct {
				WindowMode string `json:"window_mode"`
				Rev        uint64 `json:"rev"`
			} `json:"data"`
		}
		_ = json.NewDecoder(rec.Body).Decode(&body)
		done <- result{body.Data.WindowMode, body.Data.Rev}
	}()

	time.Sleep(100 * time.Millisecond) // let the handler park
	d.Shell.SetMode("normal")

	select {
	case got := <-done:
		if got.mode != "normal" || got.rev != rev+1 {
			t.Fatalf("long poll returned (%q, %d), want (normal, %d)", got.mode, got.rev, rev+1)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("long poll not released by SetMode within 3s (wait=20 would mean it is holding, not woken)")
	}
}

// TestWindowStateAPI_StaleSinceReturnsImmediatelyEvenWithHugeWait: a stale
// since returns immediately regardless of wait= — this covers the
// "never miss a change between two polls" contract, NOT the wait clamp
// (a stale since short-circuits before the wait matters, so this passes
// with or without clamping — review of ut-docs#1039, finding 10). The
// clamp itself is pinned numerically by TestClampShellPollWait below.
func TestWindowStateAPI_StaleSinceReturnsImmediatelyEvenWithHugeWait(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	d.Shell.SetMode("kiosk") // rev now 2; since=1 is stale

	start := time.Now()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, liveShellRequest("/api/window-mode?control=live&wait=9999&since=1&applied="))
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("stale-since request held for %v, want immediate return", elapsed)
	}
	if mode, _, rev := decodeWindowModeBody(t, rec); mode != "kiosk" || rev != 2 {
		t.Fatalf("stale-since response = (%q, %d), want (kiosk, 2)", mode, rev)
	}
}

// TestWindowStateAPI_AppliedParamUpdatesAttached: carrying applied= on a
// control=live poll is the shell's heartbeat AND its acknowledgement.
func TestWindowStateAPI_AppliedParamUpdatesAttached(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	if d.Shell.Attached(common.ShellAttachedWindow) {
		t.Fatal("Attached = true before any poll, want false")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, liveShellRequest("/api/window-mode?control=live&applied=normal"))
	if rec.Code != http.StatusOK {
		t.Fatalf("control=live poll = %d, want 200", rec.Code)
	}
	if !d.Shell.Attached(common.ShellAttachedWindow) {
		t.Fatal("Attached = false after a control=live poll, want true")
	}
	// The acknowledgement is visible to WaitApplied.
	if !d.Shell.WaitApplied(context.Background(), "normal", 10*time.Millisecond) {
		t.Fatal("WaitApplied(normal) = false after applied=normal poll, want true")
	}
	// A plain (non-live) request is NOT a heartbeat — no capability claim,
	// no attach. Verified via a fresh deps so the previous poll can't mask it.
	mux2, _, d2 := newFullAuthDeps(t)
	rec = httptest.NewRecorder()
	mux2.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/window-mode?applied=normal", nil))
	if d2.Shell.Attached(common.ShellAttachedWindow) {
		t.Fatal("Attached = true after a plain request, want false (only control=live counts)")
	}
}

// TestWindowStateAPI_ClientDisconnectReleasesHandler: cancelling the
// request context (the client hung up) must release a parked long poll
// immediately, not leak the goroutine until the wait elapses.
func TestWindowStateAPI_ClientDisconnectReleasesHandler(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	_, rev := d.Shell.Snapshot()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		rec := httptest.NewRecorder()
		req := liveShellRequest("/api/window-mode?control=live&wait=20&since=" + strconv.FormatUint(rev, 10) + "&applied=").WithContext(ctx)
		mux.ServeHTTP(rec, req)
		close(done)
	}()
	time.Sleep(100 * time.Millisecond) // let the handler park
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler not released by client disconnect within 3s")
	}
}

// TestWindowStateAPI_LiveClientDowngradedUnlessShellChannelIsExitPath is
// the permanent version of the review probe that blocked ut-docs#1039's
// first round: on a box whose Deps.WindowCtl is the Pi kiosk controller
// (UT_KIOSK=1 or a leftover unitill-kiosk.service unit file — reachable on
// desktop images and non-Pi Debian boxes, and the documented undo leaves
// the unit file on disk), NOTHING ever consumes the shell channel:
// exit-to-os drives systemd, not the channel. Serving a control=live GTK
// shell the chrome-hiding mode there rebuilds the exact ut-docs#1039 trap
// — gtk_window_fullscreen() + set_decorated(FALSE) with a dead exit. The
// fail-closed guarantee must therefore key on BOTH facts: the client holds
// a live poll AND the shell channel really is the exit path (marked only
// by NewShellPollWindowController, the one controller that consumes it).
// This test fails if either half of the conjunction is removed.
func TestWindowStateAPI_LiveClientDowngradedUnlessShellChannelIsExitPath(t *testing.T) {
	// Half 1: the Pi kiosk topology — channel seeded kiosk from the
	// persisted preference (exactly what pages.Init does), never marked as
	// the exit path because the kiosk controller, not
	// ShellPollWindowController, owns the window there.
	d := &common.Deps{Shell: common.NewShellChannel("kiosk")}
	mux := http.NewServeMux()
	registerWindowState(mux, d)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, liveShellRequest("/api/window-mode?control=live&since=0&applied="))
	if mode, _, _ := decodeWindowModeBody(t, rec); mode != "normal" {
		t.Fatalf("control=live client served %q while the shell channel is not the exit path, want normal (the rebuilt ut-docs#1039 trap)", mode)
	}

	// Half 2: same request, but the channel IS consumed by the polled
	// controller (the desktop-shell topology) — the real mode is served.
	d2 := &common.Deps{Shell: common.NewShellChannel("kiosk")}
	d2.WindowCtl = common.NewShellPollWindowController(d2.Shell, nil)
	mux2 := http.NewServeMux()
	registerWindowState(mux2, d2)

	rec = httptest.NewRecorder()
	mux2.ServeHTTP(rec, liveShellRequest("/api/window-mode?control=live&since=0&applied="))
	if mode, _, _ := decodeWindowModeBody(t, rec); mode != "kiosk" {
		t.Fatalf("control=live client served %q with the exit-path controller wired, want kiosk", mode)
	}
}

// TestWindowStateAPI_ControlLiveFromLANTreatedAsPlainClient (review of
// ut-docs#1039, finding 6): the shell is always same-host, so control=live
// counts only from loopback. A LAN caller claiming it must get the plain
// downgraded answer, immediately — and must be able to neither park a long
// poll, keep Attached() true on a shell-less till, nor spoof an applied=
// acknowledgement into WaitApplied.
func TestWindowStateAPI_ControlLiveFromLANTreatedAsPlainClient(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	d.Shell.SetMode("kiosk")

	start := time.Now()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/window-mode?control=live&wait=20&since=0&applied=kiosk", nil)
	req.RemoteAddr = "192.168.1.50:40123" // a LAN host
	mux.ServeHTTP(rec, req)

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("LAN control=live held for %v, want an immediate answer (no parked poll for LAN callers)", elapsed)
	}
	if mode, _, _ := decodeWindowModeBody(t, rec); mode != "normal" {
		t.Fatalf("LAN control=live served %q, want normal (the fail-closed downgrade)", mode)
	}
	if d.Shell.Attached(common.ShellAttachedWindow) {
		t.Fatal("Attached = true after a LAN control=live request — a LAN host must not be able to impersonate the shell heartbeat")
	}
	if d.Shell.WaitApplied(context.Background(), "kiosk", 10*time.Millisecond) {
		t.Fatal("WaitApplied(kiosk) = true after a LAN applied= — a LAN host must not be able to drive acknowledgements")
	}
}

// TestWindowStateAPI_ParkedPollsCapped (review of ut-docs#1039, finding 6):
// at most maxParkedShellPolls long polls park at once; past the cap the
// handler answers immediately with the current state instead of pinning
// another goroutine+connection for up to ShellPollMaxWait.
func TestWindowStateAPI_ParkedPollsCapped(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	_, rev := d.Shell.Snapshot()

	var wg sync.WaitGroup
	for i := 0; i < maxParkedShellPolls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, liveShellRequest("/api/window-mode?control=live&wait=20&since="+strconv.FormatUint(rev, 10)+"&applied="))
		}()
	}
	// Let the cap fill. Attached() doubles as a cheap "requests arrived"
	// signal but not "parked"; a short settle keeps this deterministic
	// enough — an unfilled slot only makes the test pass vacuously slower,
	// never flakily fail, because the over-cap request below asserts an
	// immediate answer either way.
	time.Sleep(200 * time.Millisecond)

	start := time.Now()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, liveShellRequest("/api/window-mode?control=live&wait=20&since="+strconv.FormatUint(rev, 10)+"&applied="))
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("over-cap poll held for %v, want an immediate answer", elapsed)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("over-cap poll = %d, want 200", rec.Code)
	}

	// Release the parked four and join them.
	d.Shell.SetMode("kiosk")
	wg.Wait()
}

// TestWindowStateAPI_AdoptsShellReportedModeAfterServerRestart (review of
// ut-docs#1039, finding 7): a unitill-pos restart (Restart=always, a .deb
// upgrade) re-seeds the live mode from the persisted preference, which
// exit-to-os deliberately leaves at kiosk — but the still-running shell's
// window is the authority on what it currently IS. The first valid
// applied= on a live poll after boot, with no explicit SetMode yet, is
// adopted: an operator who escaped stays escaped.
func TestWindowStateAPI_AdoptsShellReportedModeAfterServerRestart(t *testing.T) {
	// A fresh server process: channel seeded kiosk from the persisted
	// preference, no SetMode yet, exit path marked (desktop topology).
	d := &common.Deps{Shell: common.NewShellChannel("kiosk")}
	d.WindowCtl = common.NewShellPollWindowController(d.Shell, nil)
	mux := http.NewServeMux()
	registerWindowState(mux, d)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, liveShellRequest("/api/window-mode?control=live&since=0&applied=normal"))
	if mode, _, _ := decodeWindowModeBody(t, rec); mode != "normal" {
		t.Fatalf("first live poll after restart served %q, want the shell's own reported normal adopted (or the window is re-sealed 3s after every crash/upgrade)", mode)
	}
	if mode, _ := d.Shell.Snapshot(); mode != "normal" {
		t.Fatalf("live mode = %q after adoption, want normal", mode)
	}
}

// TestWindowStateAPI_AdoptionNeverEscalatesToChromeHiding (review of
// ut-docs#1039, finding 7's security condition): adoption is only ever
// "keep what the window already is" — a first poll reporting kiosk while
// the live mode is normal must NOT talk the server into serving kiosk;
// otherwise a hostile loopback caller could rebuild the lockdown remotely.
func TestWindowStateAPI_AdoptionNeverEscalatesToChromeHiding(t *testing.T) {
	d := &common.Deps{Shell: common.NewShellChannel("normal")}
	d.WindowCtl = common.NewShellPollWindowController(d.Shell, nil)
	mux := http.NewServeMux()
	registerWindowState(mux, d)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, liveShellRequest("/api/window-mode?control=live&since=0&applied=kiosk"))
	if mode, _, _ := decodeWindowModeBody(t, rec); mode != "normal" {
		t.Fatalf("adoption escalated the live mode to %q, want normal kept", mode)
	}
	if mode, _ := d.Shell.Snapshot(); mode != "normal" {
		t.Fatalf("live mode = %q after an applied=kiosk first poll, want normal", mode)
	}
}

// TestWindowStateAPI_NoAdoptionAfterExplicitSetMode (review of ut-docs#1039,
// finding 7): any explicit SetMode — a Settings apply, a cloud set_setting
// re-derive, exit-to-os itself — closes the adoption window, no-op
// included: an operator who re-applies kiosk right after a server start
// means it.
func TestWindowStateAPI_NoAdoptionAfterExplicitSetMode(t *testing.T) {
	d := &common.Deps{Shell: common.NewShellChannel("kiosk")}
	d.WindowCtl = common.NewShellPollWindowController(d.Shell, nil)
	d.Shell.SetMode("kiosk") // explicit (a no-op change, still an instruction)
	mux := http.NewServeMux()
	registerWindowState(mux, d)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, liveShellRequest("/api/window-mode?control=live&since=0&applied=normal"))
	if mode, _, _ := decodeWindowModeBody(t, rec); mode != "kiosk" {
		t.Fatalf("post-SetMode live poll served %q, want kiosk (no adoption once explicitly set)", mode)
	}
}

// recordingHeartbeatController is a minimal WindowController test double
// for POST /api/window/input-heartbeat (ut-docs#1329) — it only needs to
// prove the handler calls through, not exercise ExitToOS/ApplyMode.
type recordingHeartbeatController struct {
	calls int
	err   error
}

func (r *recordingHeartbeatController) ExitToOS() error             { return nil }
func (r *recordingHeartbeatController) ApplyMode(mode string) error { return nil }
func (r *recordingHeartbeatController) RecordInputHeartbeat() error {
	r.calls++
	return r.err
}

// TestInputHeartbeatEndpoint_CallsThroughToWindowCtl (ut-docs#1329, split
// from #1228's input-freeze incident) proves the route reaches WindowCtl
// and answers 204, mirroring how the analogous exit-to-os/apply-mode
// handler tests prove theirs do.
func TestInputHeartbeatEndpoint_CallsThroughToWindowCtl(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	wc := &recordingHeartbeatController{}
	d.WindowCtl = wc

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/window/input-heartbeat", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST /api/window/input-heartbeat = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if wc.calls != 1 {
		t.Fatalf("RecordInputHeartbeat calls = %d, want 1", wc.calls)
	}
}

// TestInputHeartbeatEndpoint_NoInlinePINOrElevationGate proves the handler
// itself never asks for a manager PIN or elevation — unlike
// POST /api/settings/exit-to-os (AuthorizeManager) or
// POST /api/settings/window-mode (checkOrElevate) above. The OUTER session
// requirement (this route is not in auth.exempt()) is proven separately at
// the auth package level (TestInputHeartbeatRouteIsNotExempt,
// TestInputHeartbeatUnauthenticatedGetsRejectedByMiddleware) — this
// helper's mux (newFullAuthDeps) never wires auth.Middleware itself, so it
// cannot stand in for that proof.
func TestInputHeartbeatEndpoint_NoInlinePINOrElevationGate(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	d.WindowCtl = &recordingHeartbeatController{}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/window/input-heartbeat", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (no PIN/elevation gate on this route)", rec.Code)
	}
}

// TestInputHeartbeatEndpoint_ForwardErrorStillAnswersNoContent proves a
// WindowCtl failure is logged, never surfaced as a non-2xx to the caller —
// this is best-effort telemetry fired from every kiosk screen; a delivery
// failure must not make the page's own JS see or retry an error (see
// web/public/input-heartbeat.js).
func TestInputHeartbeatEndpoint_ForwardErrorStillAnswersNoContent(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	d.WindowCtl = &recordingHeartbeatController{err: errors.New("desktop shell control channel unreachable")}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/window/input-heartbeat", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 even when the forward fails", rec.Code)
	}
}

// TestInputHeartbeatEndpoint_NilWindowCtlFallsBackToNoop covers the
// bare-Deps case (d.WindowCtl unset) other handlers in this package
// already fall back to common.NoopWindowController for.
func TestInputHeartbeatEndpoint_NilWindowCtlFallsBackToNoop(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/window/input-heartbeat", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 with WindowCtl unset", rec.Code)
	}
}

// TestClampShellPollWait pins the ?wait= clamp numerically (review of
// ut-docs#1039, finding 10 — the clamp previously had zero coverage; the
// stale-since test passes with or without it).
func TestClampShellPollWait(t *testing.T) {
	cases := []struct {
		in   int
		want time.Duration
	}{
		{-3, 0},
		{0, 0},
		{5, 5 * time.Second},
		{int(common.ShellPollMaxWait / time.Second), common.ShellPollMaxWait},
		{int(common.ShellPollMaxWait/time.Second) + 1, common.ShellPollMaxWait},
		{9999, common.ShellPollMaxWait},
	}
	for _, c := range cases {
		if got := clampShellPollWait(c.in); got != c.want {
			t.Errorf("clampShellPollWait(%d) = %v, want %v", c.in, got, c.want)
		}
	}
}
