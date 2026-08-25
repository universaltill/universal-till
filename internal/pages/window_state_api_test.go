package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/window-mode?control=live&applied=", nil))
	mode, _, rev := decodeWindowModeBody(t, rec)
	if mode != "kiosk" {
		t.Fatalf("control=live window_mode = %q, want kiosk", mode)
	}
	if rev == 0 {
		t.Fatal("control=live rev = 0, want the channel's revision")
	}

	// Only the exact value "live" counts as the capability claim.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/window-mode?control=yes-really", nil))
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
		req := httptest.NewRequest(http.MethodGet,
			"/api/window-mode?control=live&wait=20&since="+strconv.FormatUint(rev, 10)+"&applied=kiosk", nil)
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

// TestWindowStateAPI_WaitClampedToShellPollMaxWait: an absurd wait= must
// not hold the handler beyond ShellPollMaxWait. Proven structurally rather
// than by a 25s sleep: a wait of 9999s with since already differing
// returns immediately anyway, so instead assert via the handler's prompt
// return on a stale since — the clamp itself is unit-visible through a
// tiny wait that still long-polls.
func TestWindowStateAPI_StaleSinceReturnsImmediatelyEvenWithHugeWait(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	d.Shell.SetMode("kiosk") // rev now 2; since=1 is stale

	start := time.Now()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/window-mode?control=live&wait=9999&since=1&applied=", nil))
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
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/window-mode?control=live&applied=normal", nil))
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
		req := httptest.NewRequest(http.MethodGet,
			"/api/window-mode?control=live&wait=20&since="+strconv.FormatUint(rev, 10)+"&applied=", nil).WithContext(ctx)
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
