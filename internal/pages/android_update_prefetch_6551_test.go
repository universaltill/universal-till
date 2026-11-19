package pages

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/androidupdate"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/updates"
)

// ut-docs#6551: Android cannot self-swap, so its nightly step is a
// pre-download the manager installs with one tap; the install endpoint
// refuses while a basket is open.

type fakePrefetcher struct {
	calls int
	err   error
}

func (f *fakePrefetcher) PrefetchUpdate() error { f.calls++; return f.err }

// stubAndroidTick pins the seams for an Android till and returns the fake
// shell (nil shell when withShell is false) and the Apply counter.
func stubAndroidTick(t *testing.T, current, fresh updates.Status, withShell bool) (*fakePrefetcher, *int) {
	t.Helper()
	applyCalls := stubAutoUpdateSeams(t, current, fresh, false, nil)
	origGOOS, origPF := autoUpdateGOOS, autoUpdatePrefetcher
	t.Cleanup(func() { autoUpdateGOOS, autoUpdatePrefetcher = origGOOS, origPF })
	autoUpdateGOOS = "android"
	fp := &fakePrefetcher{}
	autoUpdatePrefetcher = func() androidupdate.Prefetcher { return nil }
	if withShell {
		autoUpdatePrefetcher = func() androidupdate.Prefetcher { return fp }
	}
	return fp, applyCalls
}

func dueAndroidDeps(t *testing.T) *common.Deps {
	t.Helper()
	dp := newAutoUpdateTestDeps(t)
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateEnabled, "true")
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateTime, tickHHMM)
	return dp
}

func slotOf(t *testing.T, dp *common.Deps) string {
	t.Helper()
	v, _, _ := dp.Settings.Get(t.Context(), keyAutoUpdateLastAttempt)
	return v
}

func TestAutoUpdateTick_AndroidPrefetchesOnce(t *testing.T) {
	dp := dueAndroidDeps(t)
	fp, applyCalls := stubAndroidTick(t, updates.Status{Available: true}, updates.Status{Available: true}, true)

	autoUpdateTick(t.Context(), dp, tickNow)

	if fp.calls != 1 {
		t.Fatalf("PrefetchUpdate called %d times, want 1", fp.calls)
	}
	if *applyCalls != 0 {
		t.Fatalf("Apply called %d times on Android, want 0", *applyCalls)
	}
	if slotOf(t, dp) == "" {
		t.Fatal("slot not recorded after a prefetch")
	}
	autoUpdateTick(t.Context(), dp, tickNow.Add(30*time.Second))
	if fp.calls != 1 {
		t.Fatalf("second tick in the same slot prefetched again: %d calls", fp.calls)
	}
}

func TestAutoUpdateTick_AndroidBusyDefersWithoutSpendingSlot(t *testing.T) {
	dp := dueAndroidDeps(t)
	fp, _ := stubAndroidTick(t, updates.Status{Available: true}, updates.Status{Available: true}, true)
	dp.Engine.AddLineWithModifiers(pos.BasketLine{SKU: "sku-1", Name: "Coffee", Qty: 1, PriceCents: 250}, 1, nil)

	autoUpdateTick(t.Context(), dp, tickNow)
	if fp.calls != 0 || slotOf(t, dp) != "" {
		t.Fatalf("prefetched mid-sale: calls=%d slot=%q", fp.calls, slotOf(t, dp))
	}

	dp.Engine = pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000}, nil) // sale done
	autoUpdateTick(t.Context(), dp, tickNow.Add(30*time.Second))
	if fp.calls != 1 {
		t.Fatalf("PrefetchUpdate called %d times once idle, want 1", fp.calls)
	}
}

func TestAutoUpdateTick_AndroidNoBridgeKeepsSlot(t *testing.T) {
	dp := dueAndroidDeps(t)
	_, applyCalls := stubAndroidTick(t, updates.Status{Available: true}, updates.Status{Available: true}, false)

	autoUpdateTick(t.Context(), dp, tickNow)

	if slotOf(t, dp) != "" {
		t.Fatalf("slot spent with no shell registered: %q", slotOf(t, dp))
	}
	if *applyCalls != 0 {
		t.Fatalf("Apply called %d times, want 0", *applyCalls)
	}
}

func TestAutoUpdateTick_AndroidNoPrefetch(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(t *testing.T, dp *common.Deps)
		current  updates.Status
		fresh    updates.Status
		wantSlot bool
	}{
		{"dev build", func(t *testing.T, dp *common.Deps) { autoUpdateBuildVersion = func() string { return "dev" } },
			updates.Status{Available: true}, updates.Status{Available: true}, false},
		{"disabled", func(t *testing.T, dp *common.Deps) { _ = dp.Settings.Set(t.Context(), keyAutoUpdateEnabled, "false") },
			updates.Status{Available: true}, updates.Status{Available: true}, false},
		{"replica", func(t *testing.T, dp *common.Deps) {
			_ = dp.Settings.Set(t.Context(), "sync.primary_url", "http://main.local")
		},
			updates.Status{Available: true}, updates.Status{Available: true}, false},
		{"cached not available", func(t *testing.T, dp *common.Deps) {},
			updates.Status{Available: false}, updates.Status{Available: true}, false},
		{"fresh check not available", func(t *testing.T, dp *common.Deps) {},
			updates.Status{Available: true}, updates.Status{Available: false}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dp := dueAndroidDeps(t)
			fp, applyCalls := stubAndroidTick(t, c.current, c.fresh, true)
			c.setup(t, dp)

			autoUpdateTick(t.Context(), dp, tickNow)

			if fp.calls != 0 || *applyCalls != 0 {
				t.Fatalf("prefetch=%d apply=%d, want 0/0", fp.calls, *applyCalls)
			}
			if got := slotOf(t, dp) != ""; got != c.wantSlot {
				t.Fatalf("slot recorded = %v, want %v", got, c.wantSlot)
			}
		})
	}
}

func TestAutoUpdateTick_NonAndroidUnsupportedStillSkips(t *testing.T) {
	dp := dueAndroidDeps(t)
	fp, applyCalls := stubAndroidTick(t, updates.Status{Available: true}, updates.Status{Available: true}, true)
	autoUpdateGOOS = "linux"

	autoUpdateTick(t.Context(), dp, tickNow)

	if fp.calls != 0 || *applyCalls != 0 || slotOf(t, dp) != "" {
		t.Fatalf("linux+unsupported: prefetch=%d apply=%d slot=%q", fp.calls, *applyCalls, slotOf(t, dp))
	}
}

func TestAndroidInstall_RefusesWhileBasketOpen(t *testing.T) {
	coffee := pos.BasketLine{SKU: "sku-1", Name: "Coffee", Qty: 1, PriceCents: 250}
	basket := func(t *testing.T, d *common.Deps, kind string) {
		switch kind {
		case "cashier":
			d.Engine.AddLineWithModifiers(coffee, 1, nil)
		case "kiosk":
			d.KioskEngine = pos.NewServiceWithResolver(pos.Config{}, nil)
			d.KioskEngine.AddLineWithModifiers(coffee, 1, nil)
		case "table":
			// A table-QR order (ADR-0103) lives in its own session basket.
			d.SelfOrderSessions = pos.NewSessionBasketManager(func() *pos.Service {
				return pos.NewServiceWithResolver(pos.Config{}, nil)
			})
			_, sess, _ := d.SelfOrderSessions.Create()
			sess.AddLineWithModifiers(coffee, 1, nil)
		case "shift-close":
			shiftCloseInFlight.Add(1)
			t.Cleanup(func() { shiftCloseInFlight.Add(-1) })
		}
	}
	for _, tc := range []struct {
		name string
		kind string
		form url.Values
	}{
		{"cashier basket", "cashier", url.Values{}},
		{"kiosk basket", "kiosk", url.Values{}},
		{"table-QR order", "table", url.Values{}},
		{"shift close in flight", "shift-close", url.Values{}},
		{"cashier basket with a PIN", "cashier", url.Values{"manager_pin": {"000000"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, d := androidInstallMux(t)
			checked := 0
			orig := androidInstallCheckNow
			androidInstallCheckNow = func(context.Context) updates.Status { checked++; return updates.Status{Available: true} }
			t.Cleanup(func() { androidInstallCheckNow = orig })
			basket(t, d, tc.kind)

			rec := postForm(api, "/api/update/android-install", tc.form, &mgrUser)

			if rec.Code != http.StatusConflict {
				t.Fatalf("open basket = %d, want 409: %s", rec.Code, rec.Body.String())
			}
			want := httpx.T("en", "settings.update.android.busy")
			if want == "settings.update.android.busy" || !strings.Contains(rec.Body.String(), strings.ReplaceAll(want, "—", `—`)) &&
				!strings.Contains(rec.Body.String(), want) {
				t.Errorf("body %q lacks the busy message %q", rec.Body.String(), want)
			}
			if checked != 0 {
				t.Errorf("freshness check ran %d times, want 0 (409 comes first)", checked)
			}
			var n int
			_ = d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='update_authorized'`).Scan(&n)
			if n != 0 {
				t.Errorf("update_authorized audit rows = %d, want 0", n)
			}
		})
	}
}

func TestUpdateUnavailableHTML_AndroidCarriesBusyAndDownloaded(t *testing.T) {
	out := updateUnavailableHTML("en", "1.2.3", "android")
	for _, want := range []string{`data-busy="`, `data-downloaded="`} {
		if !strings.Contains(out, want) {
			t.Errorf("android check-result button lacks %s: %s", want, out)
		}
	}
}

// The Settings form carries the downloaded / busy / install-now texts and the
// shared script maps the 409 to its own busy outcome (ut-docs#6551).
func TestAndroidSettingsFormAndScriptKnowDownloadedAndBusy(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	body := renderSettingsAs(t, mux, mgrUser, true)
	for _, want := range []string{`data-busy="`, `data-downloaded="`, `data-installnow="Install now"`} {
		if !strings.Contains(body, want) {
			t.Errorf("settings form lacks %s", want)
		}
	}
	js, err := os.ReadFile("web/public/android-update.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"res.status === 409", "'busy'", "state === 'downloaded'", "announceReady"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("android-update.js lacks %s", want)
		}
	}
}
