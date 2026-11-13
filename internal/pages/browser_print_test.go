package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
)

type fakeDest struct{ st print.DestState }

func (f fakeDest) State() print.DestState { return f.st }

func piDeps(st print.DestState) *common.Deps {
	return &common.Deps{
		WindowCtl:      common.KioskSystemdWindowController{},
		SystemPrinters: fakeDest{st},
	}
}

// ut-docs#6154 AC1: only Pi kiosk + loopback + definitely-no-destination is off.
func TestBrowserPrintOff_Table(t *testing.T) {
	ctls := map[string]common.WindowController{
		"pi":      common.KioskSystemdWindowController{},
		"shell":   common.NewShellPollWindowController(common.NewShellChannel("normal"), nil),
		"android": common.AndroidNativeWindowController{},
	}
	addrs := []string{"127.0.0.1:5000", "[::1]:5000", "192.168.1.20:5000"}
	states := map[string]print.DestState{"none": print.DestNone, "present": print.DestPresent, "unknown": print.DestUnknown}
	for cn, ctl := range ctls {
		for _, addr := range addrs {
			for sn, st := range states {
				d := &common.Deps{WindowCtl: ctl, SystemPrinters: fakeDest{st}}
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.RemoteAddr = addr
				want := cn == "pi" && !strings.HasPrefix(addr, "192.") && sn == "none"
				if got := browserPrintOff(d, r); got != want {
					t.Errorf("%s %s %s: got %v want %v", cn, addr, sn, got, want)
				}
			}
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:1"
	if browserPrintOff(&common.Deps{WindowCtl: common.KioskSystemdWindowController{}}, r) {
		t.Error("nil probe must not be off")
	}
	if browserPrintOff(nil, r) || browserPrintOff(piDeps(print.DestNone), nil) {
		t.Error("nil deps/request must not be off")
	}
}

// AC4 + AC7: the <html> marker, the always-rendered hidden notice, and no
// marker/notice/Settings link on self-order pages.
func TestBrowserPrint_HTMLMarker(t *testing.T) {
	const marker = `data-browser-print="off"`
	render := func(t *testing.T, path string, mut func(*common.Deps), remote string) string {
		t.Helper()
		mux, _, d := newFullAuthDeps(t)
		d.WindowCtl = common.KioskSystemdWindowController{}
		d.SystemPrinters = fakeDest{print.DestNone}
		if mut != nil {
			mut(d)
		}
		httpx.InitBrowserPrint(func(r *http.Request) bool { return browserPrintOff(d, r) })
		t.Cleanup(func() { httpx.InitBrowserPrint(nil) })
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = remote
		req = auth.WithUser(req, mgrUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Body.String()
	}
	body := render(t, "/settings", nil, "127.0.0.1:5000")
	if !strings.Contains(body, marker) {
		t.Fatal("Pi + loopback + none: marker missing")
	}
	for _, id := range []string{`id="ut-print-unavailable"`, `data-testid="print-unavailable"`, `role="status"`, "data-reusable"} {
		if !strings.Contains(body, id) {
			t.Fatalf("notice missing %s", id)
		}
	}
	if !strings.Contains(body, "Nothing was printed. Check that the printer is set up and switched on.") {
		t.Fatal("notice text missing")
	}
	variants := map[string]struct {
		mut    func(*common.Deps)
		remote string
	}{
		"lan":     {nil, "192.168.1.20:5000"},
		"present": {func(d *common.Deps) { d.SystemPrinters = fakeDest{print.DestPresent} }, "127.0.0.1:5000"},
		"unknown": {func(d *common.Deps) { d.SystemPrinters = fakeDest{print.DestUnknown} }, "127.0.0.1:5000"},
		"shell": {func(d *common.Deps) {
			d.WindowCtl = common.NewShellPollWindowController(common.NewShellChannel("normal"), nil)
		}, "127.0.0.1:5000"},
	}
	for name, v := range variants {
		b := render(t, "/settings", v.mut, v.remote)
		if strings.Contains(b, marker) {
			t.Errorf("%s: marker must be absent", name)
		}
		if !strings.Contains(b, `id="ut-print-unavailable"`) {
			t.Errorf("%s: hidden notice must still be rendered", name)
		}
	}
	so := render(t, "/self-order", nil, "127.0.0.1:5000")
	for _, bad := range []string{marker, "ut-print-unavailable", "/settings"} {
		if strings.Contains(so, bad) {
			t.Errorf("self-order must not contain %q", bad)
		}
	}
}
