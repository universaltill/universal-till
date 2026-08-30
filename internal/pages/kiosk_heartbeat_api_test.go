package pages

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// fakeHeartbeatController lets TestKioskInputHeartbeat_* assert
// registerKioskHeartbeat (ut-docs#1329) actually reaches Deps.WindowCtl,
// and that a relay failure never surfaces to the caller.
type fakeHeartbeatController struct {
	common.NoopWindowController
	calls int
	err   error
}

func (f *fakeHeartbeatController) InputHeartbeat() error {
	f.calls++
	return f.err
}

func TestKioskInputHeartbeat_ForwardsToWindowCtl(t *testing.T) {
	mux := http.NewServeMux()
	wc := &fakeHeartbeatController{}
	d := &common.Deps{WindowCtl: wc}
	registerKioskHeartbeat(mux, d)

	req := httptest.NewRequest(http.MethodPost, "/api/kiosk/input-heartbeat", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if wc.calls != 1 {
		t.Fatalf("WindowCtl.InputHeartbeat calls = %d, want 1", wc.calls)
	}
}

// TestKioskInputHeartbeat_RelayFailureStillReturns204 proves a relay
// failure (no live shell channel — the ordinary case) is never surfaced to
// the kiosk page as an error; this is a passive diagnostic signal, not an
// action the caller needs to react to.
func TestKioskInputHeartbeat_RelayFailureStillReturns204(t *testing.T) {
	mux := http.NewServeMux()
	wc := &fakeHeartbeatController{err: errors.New("no live shell control channel")}
	d := &common.Deps{WindowCtl: wc}
	registerKioskHeartbeat(mux, d)

	req := httptest.NewRequest(http.MethodPost, "/api/kiosk/input-heartbeat", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 even when the relay failed", rec.Code)
	}
}

// TestKioskInputHeartbeat_ThrottlesRapidRepeats proves the server-side
// floor (review of ut-docs#1329) actually bounds the forward rate: this
// endpoint is auth-exempt and LAN-reachable (unlike the shell's own
// loopback-only control server), so without it a single hostile or
// misbehaving LAN client could fan out unbounded concurrent calls against
// the shell's control channel. A rapid second POST must be a no-op on
// WindowCtl, not a second relay.
func TestKioskInputHeartbeat_ThrottlesRapidRepeats(t *testing.T) {
	mux := http.NewServeMux()
	wc := &fakeHeartbeatController{}
	d := &common.Deps{WindowCtl: wc}
	registerKioskHeartbeat(mux, d)

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/kiosk/input-heartbeat", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("POST #%d status = %d, want 204 even while throttled", i, rec.Code)
		}
	}
	if wc.calls != 1 {
		t.Fatalf("WindowCtl.InputHeartbeat calls = %d, want 1 (4 of 5 rapid POSTs must be throttled)", wc.calls)
	}
}

// TestKioskInputHeartbeat_NilWindowCtlFallsBackToNoop proves the same
// nil-check convention every other WindowCtl-using handler already follows
// (e.g. settings_page.go's exit-to-os handler) — bare-Deps callers must not
// panic.
func TestKioskInputHeartbeat_NilWindowCtlFallsBackToNoop(t *testing.T) {
	mux := http.NewServeMux()
	d := &common.Deps{}
	registerKioskHeartbeat(mux, d)

	req := httptest.NewRequest(http.MethodPost, "/api/kiosk/input-heartbeat", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}
