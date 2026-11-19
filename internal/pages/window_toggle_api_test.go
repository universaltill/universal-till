package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// F11 window toggle (ut-docs#3735): POST /api/settings/window-mode/toggle
// flips the LIVE window mode fullscreen <-> normal over the ADR-0064
// channel, for a signed-in manager on the till itself, and never touches
// kiosk (leaving kiosk stays behind the Exit-to-OS PIN) nor the persisted
// display.window_mode preference.

const windowToggleURL = "/api/settings/window-mode/toggle"

// attachedShellDeps is newFullAuthDeps with the desktop-shell topology wired
// for real: the poll controller consuming the channel, a poll seen just now,
// and the live mode set to start.
func attachedShellDeps(t *testing.T, start string) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, _, d := newFullAuthDeps(t)
	d.WindowCtl = common.NewShellPollWindowController(d.Shell, nil)
	d.Shell.SetMode(start)
	d.Shell.NoteSeen(start)
	return mux, d
}

type toggleResult struct {
	Data struct {
		WindowMode string `json:"window_mode"`
		Applied    bool   `json:"applied"`
		Reason     string `json:"reason"`
	} `json:"data"`
	Error any `json:"error"`
}

func doToggle(t *testing.T, mux *http.ServeMux, user *auth.User, mut func(*http.Request)) (*httptest.ResponseRecorder, toggleResult) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, windowToggleURL, nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("HX-Request", "true")
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	if mut != nil {
		mut(req)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var res toggleResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	return rec, res
}

func TestWindowToggle_NormalAndMaximizedGoFullscreen(t *testing.T) {
	for _, start := range []string{"normal", "maximized"} {
		t.Run(start, func(t *testing.T) {
			mux, d := attachedShellDeps(t, start)
			rec, res := doToggle(t, mux, &mgrUser, nil)
			if rec.Code != http.StatusOK || res.Error != nil {
				t.Fatalf("toggle = %d %s, want 200", rec.Code, rec.Body.String())
			}
			if !res.Data.Applied || res.Data.WindowMode != "fullscreen" {
				t.Fatalf("result = %+v, want applied fullscreen", res.Data)
			}
			if mode, _ := d.Shell.Snapshot(); mode != "fullscreen" {
				t.Fatalf("live mode = %q, want fullscreen", mode)
			}
		})
	}
}

func TestWindowToggle_FullscreenGoesNormal(t *testing.T) {
	mux, d := attachedShellDeps(t, "fullscreen")
	rec, res := doToggle(t, mux, &mgrUser, nil)
	if rec.Code != http.StatusOK || !res.Data.Applied || res.Data.WindowMode != "normal" {
		t.Fatalf("toggle = %d %s, want applied normal", rec.Code, rec.Body.String())
	}
	if mode, _ := d.Shell.Snapshot(); mode != "normal" {
		t.Fatalf("live mode = %q, want normal", mode)
	}
}

// Kiosk is never entered or left by F11: leaving it is the Exit-to-OS PIN's job.
func TestWindowToggle_KioskIsNoOp(t *testing.T) {
	mux, d := attachedShellDeps(t, "kiosk")
	_, rev := d.Shell.Snapshot()
	rec, res := doToggle(t, mux, &mgrUser, nil)
	if rec.Code != http.StatusOK || res.Data.Applied || res.Data.Reason != "kiosk_locked" || res.Data.WindowMode != "kiosk" {
		t.Fatalf("toggle = %d %s, want 200 applied=false reason=kiosk_locked", rec.Code, rec.Body.String())
	}
	if mode, r2 := d.Shell.Snapshot(); mode != "kiosk" || r2 != rev {
		t.Fatalf("channel = %q rev %d (was %d), want untouched kiosk", mode, r2, rev)
	}
}

func TestWindowToggle_NoShellIsNoOp(t *testing.T) {
	// Plain browser / shell not polling: nothing attached.
	mux, _, d := newFullAuthDeps(t)
	d.WindowCtl = common.NewShellPollWindowController(d.Shell, nil)
	_, rev := d.Shell.Snapshot()
	rec, res := doToggle(t, mux, &mgrUser, nil)
	if rec.Code != http.StatusOK || res.Data.Applied || res.Data.Reason != "no_shell" {
		t.Fatalf("toggle = %d %s, want 200 applied=false reason=no_shell", rec.Code, rec.Body.String())
	}
	if _, r2 := d.Shell.Snapshot(); r2 != rev {
		t.Fatal("channel revision moved with no shell attached")
	}
	// A till whose channel is not the exit path (Pi appliance) is also a no-op.
	mux2, _, d2 := newFullAuthDeps(t)
	d2.Shell = common.NewShellChannel("normal") // not marked exit path
	d2.Shell.NoteSeen("normal")
	_, rev2 := d2.Shell.Snapshot()
	_, res = doToggle(t, mux2, &mgrUser, nil)
	if res.Data.Applied || res.Data.Reason != "no_shell" {
		t.Fatalf("non-exit-path result = %+v, want no_shell", res.Data)
	}
	if _, r3 := d2.Shell.Snapshot(); r3 != rev2 {
		t.Fatal("channel revision moved on a non-exit-path channel")
	}
}

func TestWindowToggle_CashierForbiddenAndChannelUntouched(t *testing.T) {
	mux, d := attachedShellDeps(t, "normal")
	_, rev := d.Shell.Snapshot()
	rec, _ := doToggle(t, mux, &cashUser, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier toggle = %d, want 403 (no elevation prompt on a keypress)", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatal("cashier got an elevation prompt")
	}
	if mode, r2 := d.Shell.Snapshot(); mode != "normal" || r2 != rev {
		t.Fatalf("channel = %q rev %d (was %d), want untouched", mode, r2, rev)
	}
}

func TestWindowToggle_UnauthenticatedRefused(t *testing.T) {
	mux, d := attachedShellDeps(t, "normal")
	_, rev := d.Shell.Snapshot()
	rec, _ := doToggle(t, mux, nil, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("anonymous toggle = %d, want 403", rec.Code)
	}
	if _, r2 := d.Shell.Snapshot(); r2 != rev {
		t.Fatal("anonymous request moved the channel")
	}
}

func TestWindowToggle_CrossOriginRefused(t *testing.T) {
	mux, d := attachedShellDeps(t, "normal")
	_, rev := d.Shell.Snapshot()
	rec, _ := doToggle(t, mux, &mgrUser, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site toggle = %d, want 403", rec.Code)
	}
	rec, _ = doToggle(t, mux, &mgrUser, func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") })
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign-Origin toggle = %d, want 403", rec.Code)
	}
	if _, r2 := d.Shell.Snapshot(); r2 != rev {
		t.Fatal("cross-origin request moved the channel")
	}
}

// A LAN browser (even a signed-in manager) must not drive the till's window.
func TestWindowToggle_NonLoopbackIsNoOp(t *testing.T) {
	mux, d := attachedShellDeps(t, "normal")
	_, rev := d.Shell.Snapshot()
	_, res := doToggle(t, mux, &mgrUser, func(r *http.Request) { r.RemoteAddr = "192.0.2.7:4000" })
	if res.Data.Applied || res.Data.Reason != "no_shell" {
		t.Fatalf("LAN result = %+v, want no-op", res.Data)
	}
	if _, r2 := d.Shell.Snapshot(); r2 != rev {
		t.Fatal("LAN request moved the channel")
	}
}

// Live-only: the persisted launch preference is never rewritten by F11.
func TestWindowToggle_DoesNotPersistPreference(t *testing.T) {
	mux, d := attachedShellDeps(t, "normal")
	st := d.CurrentState()
	st.WindowMode = "maximized"
	d.SetState(st)
	if _, res := doToggle(t, mux, &mgrUser, nil); !res.Data.Applied {
		t.Fatal("toggle did not apply")
	}
	if got := d.CurrentState().WindowMode; got != "maximized" {
		t.Fatalf("runtime WindowMode = %q, want untouched maximized", got)
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyWindowMode); v == "fullscreen" {
		t.Fatalf("stored %s = fullscreen — the toggle must be live-only", common.KeyWindowMode)
	}
}

func TestWindowToggle_Audited(t *testing.T) {
	mux, d := attachedShellDeps(t, "normal")
	if _, res := doToggle(t, mux, &mgrUser, nil); !res.Data.Applied {
		t.Fatal("toggle did not apply")
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'window_mode_toggled'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("window_mode_toggled audit rows = %d (err %v), want 1", n, err)
	}
}

// base.html's data-window-toggle flag: rendered only for a manager on the
// till itself with a live shell attached; JS intercepts F11 only with it.
func TestWindowToggle_BodyFlag(t *testing.T) {
	render := func(t *testing.T, d *common.Deps, mux *http.ServeMux, user *auth.User, remote string) string {
		t.Helper()
		httpx.InitWindowToggle(func(r *http.Request) bool { return windowToggleOffered(d, r) })
		t.Cleanup(func() { httpx.InitWindowToggle(nil) })
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		req.RemoteAddr = remote
		req = auth.WithUser(req, *user)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		// A cashier's 403 error page still renders base.html (rail intact),
		// which is exactly where a leaked flag would show.
		if rec.Code != http.StatusOK && user.Role != "cashier" {
			t.Fatalf("GET /settings = %d", rec.Code)
		}
		return rec.Body.String()
	}
	const flag = `data-window-toggle="1"`

	mux, d := attachedShellDeps(t, "normal")
	body := render(t, d, mux, &mgrUser, "127.0.0.1:5000")
	if !strings.Contains(body, flag) {
		t.Fatal("manager + loopback + attached shell: body flag missing")
	}
	if !strings.Contains(body, "/public/window-toggle.js") {
		t.Fatal("layout does not load window-toggle.js")
	}
	if strings.Contains(render(t, d, mux, &cashUser, "127.0.0.1:5000"), flag) {
		t.Fatal("cashier must not get the flag")
	}
	if strings.Contains(render(t, d, mux, &mgrUser, "192.0.2.7:5000"), flag) {
		t.Fatal("non-loopback request must not get the flag")
	}
	mux2, _, d2 := newFullAuthDeps(t) // no shell attached
	if strings.Contains(render(t, d2, mux2, &mgrUser, "127.0.0.1:5000"), flag) {
		t.Fatal("no attached shell: flag must be absent so the browser keeps its own F11")
	}
	// Same manager, loopback, attached shell as the positive case above: only
	// the /self-order prefix check can make this false.
	so := httptest.NewRequest(http.MethodGet, "/self-order", nil)
	so.RemoteAddr = "127.0.0.1:5000"
	so = auth.WithUser(so, mgrUser)
	if !windowToggleOffered(d, func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/settings", nil)
		r.RemoteAddr = "127.0.0.1:5000"
		return auth.WithUser(r, mgrUser)
	}()) {
		t.Fatal("control: same request on /settings must be offered")
	}
	if windowToggleOffered(d, so) {
		t.Fatal("/self-order pages must never offer the toggle")
	}
}
