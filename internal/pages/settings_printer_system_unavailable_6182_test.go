package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/print"
)

const systemUnavailableCopy6182 = "Regular printer can&#39;t print on this till"

func TestSystemPrinterMissing_6182(t *testing.T) {
	cases := []struct {
		name string
		d    *common.Deps
		want bool
	}{
		{"nil_deps", nil, false},
		{"nil_probe", &common.Deps{}, false},
		{"none", &common.Deps{SystemPrinters: fakeDest{print.DestNone}}, true},
		{"unknown", &common.Deps{SystemPrinters: fakeDest{print.DestUnknown}}, false},
		{"present", &common.Deps{SystemPrinters: fakeDest{print.DestPresent}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := systemPrinterMissing(c.d); got != c.want {
				t.Fatalf("systemPrinterMissing = %v, want %v", got, c.want)
			}
		})
	}
}

// ut-docs#6182: the Connection select's "no system printer" warning.
func TestSettingsPrinter_SystemUnavailableWarning_6182(t *testing.T) {
	warnTag := regexp.MustCompile(`<p [^>]*data-testid="printer-system-unavailable"[^>]*>`)
	cases := []struct {
		name       string
		probe      interface{ State() print.DestState }
		mode       string
		want       bool
		wantHidden bool
		wantDesc   bool
	}{
		{"none_system", fakeDest{print.DestNone}, "system", true, false, true},
		{"none_network", fakeDest{print.DestNone}, "network", true, true, false},
		{"none_off", fakeDest{print.DestNone}, "off", true, true, false},
		{"unknown_system", fakeDest{print.DestUnknown}, "system", false, false, false},
		{"present_system", fakeDest{print.DestPresent}, "system", false, false, false},
		{"nil_system", nil, "system", false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mux, _, d := newFullAuthDeps(t)
			plugins.SharedBus(d.Db).ResetSubscribers()
			d.SystemPrinters = c.probe
			if err := d.Settings.Set(context.Background(), keyPrinterMode, c.mode); err != nil {
				t.Fatalf("seed mode: %v", err)
			}
			req := httptest.NewRequest(http.MethodGet, "/settings", nil)
			req.RemoteAddr = "192.168.1.20:5000" // not loopback: viewer-independent
			req = auth.WithUser(req, mgrUser)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /settings = %d", rec.Code)
			}
			body := rec.Body.String()
			tag := warnTag.FindString(body)
			if (tag != "") != c.want {
				t.Fatalf("warning present = %v, want %v", tag != "", c.want)
			}
			if c.want && strings.Contains(tag, " hidden") != c.wantHidden {
				t.Fatalf("hidden = %v, want %v (tag %s)", strings.Contains(tag, " hidden"), c.wantHidden, tag)
			}
			if got := strings.Contains(body, `aria-describedby="printer-system-unavailable"`); got != c.wantDesc {
				t.Fatalf("aria-describedby on select = %v, want %v", got, c.wantDesc)
			}
			if !c.want {
				if strings.Contains(body, `id="printer-system-unavailable"`) {
					t.Fatal("no element id should be rendered when the probe is not none")
				}
				return
			}
			start := strings.Index(body, tag)
			end := strings.Index(body[start:], "</p>")
			para := body[start : start+end]
			if !strings.Contains(para, systemUnavailableCopy6182) {
				t.Fatalf("warning copy missing: %s", para)
			}
			if !strings.Contains(para, `href="/help/printing"`) {
				t.Fatalf("help link missing inside the warning: %s", para)
			}
		})
	}
}

// AC5: advisory only — saving Regular printer is unchanged with probe=None.
func TestSettingsPrinter_SystemModeStillSaves_6182(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPrintAPITestDeps(t)
	plugins.SharedBus(dp.Db).ResetSubscribers()
	dp.SystemPrinters = fakeDest{print.DestNone}
	req := httptest.NewRequest(http.MethodPost, "/api/settings/printer", strings.NewReader("mode=system"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if got, _, _ := dp.Settings.Get(context.Background(), keyPrinterMode); got != "system" {
		t.Fatalf("mode = %q, want system", got)
	}
}
