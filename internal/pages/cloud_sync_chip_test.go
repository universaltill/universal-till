package pages

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#6977 (S1): the status-bar "Synced N min ago" chip and its manager
// "Sync now" (an operator check-in, ADR-0148 §2).

func TestCloudSyncText(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name  string
		last  time.Time
		state string
		text  string
	}{
		{"never", time.Time{}, cloudSyncNever, "Not synced yet"},
		{"skew", now.Add(5 * time.Second), cloudSyncFresh, "Synced just now"},
		{"30s", now.Add(-30 * time.Second), cloudSyncFresh, "Synced just now"},
		{"59s", now.Add(-59 * time.Second), cloudSyncFresh, "Synced just now"},
		{"60s", now.Add(-60 * time.Second), cloudSyncFresh, "Synced 1 min ago"},
		{"5m", now.Add(-5 * time.Minute), cloudSyncFresh, "Synced 5 min ago"},
		{"9m59", now.Add(-9*time.Minute - 59*time.Second), cloudSyncFresh, "Synced 9 min ago"},
		{"10m", now.Add(-10 * time.Minute), cloudSyncStale, "Synced 10 min ago"},
		{"59m", now.Add(-59 * time.Minute), cloudSyncStale, "Synced 59 min ago"},
		{"60m", now.Add(-60 * time.Minute), cloudSyncStale, "Synced at " + httpx.FormatShortDateTime(now.Add(-60*time.Minute).Local(), now.Local(), "en")},
		{"2h", now.Add(-2 * time.Hour), cloudSyncStale, "Synced at " + httpx.FormatShortDateTime(now.Add(-2*time.Hour).Local(), now.Local(), "en")},
		{"3d", now.Add(-72 * time.Hour), cloudSyncStale, "Synced at " + httpx.FormatShortDateTime(now.Add(-72*time.Hour).Local(), now.Local(), "en")},
	}
	for _, c := range cases {
		state, text := cloudSyncText("en", c.last, now)
		if state != c.state || text != c.text {
			t.Errorf("%s: got (%s, %q), want (%s, %q)", c.name, state, text, c.state, c.text)
		}
	}
}

// chipMux serves the chip on a registered, paid main till.
func chipMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	recordWindows(t)
	mux, _, d := newFullAuthDeps(t)
	registerCloudSyncChip(mux, d)
	registerSubscriptionUI(mux, d)
	registerForCheckPlan(d)
	d.CloudSyncNow = make(chan struct{}, 1)
	seed := subscriptionSeed("active-fresh")
	if err := d.Settings.SetMany(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	prevWait, prevPoll := refreshEntitlementWait, refreshEntitlementPoll
	refreshEntitlementWait, refreshEntitlementPoll = 600*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { refreshEntitlementWait, refreshEntitlementPoll = prevWait, prevPoll })
	resetRefreshThrottle(t)
	orig := cloudAuthChipStatus
	cloudAuthChipStatus = func() cloudsync.AuthChip { return cloudsync.AuthChip{} }
	t.Cleanup(func() { cloudAuthChipStatus = orig })
	return mux, d
}

func setLast(t *testing.T, d *common.Deps, age time.Duration) {
	t.Helper()
	if err := d.Settings.Set(t.Context(), enroll.KeyLastCheckinAt, time.Now().Add(-age).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
}

func TestCloudSyncChip_FreshForManagerHasSyncNow(t *testing.T) {
	mux, d := chipMux(t)
	setLast(t, d, 30*time.Second)
	body := getAs(mux, "/ui/cloud-sync-chip", &mgrUser).Body.String()
	for _, want := range []string{`data-testid="sb-cloud-sync"`, "Synced just now", `data-testid="sb-cloud-sync-now"`, "is-fresh"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %s", want, body)
		}
	}
}

func TestCloudSyncChip_CashierSeesTimeOnly(t *testing.T) {
	mux, d := chipMux(t)
	setLast(t, d, 5*time.Minute)
	body := getAs(mux, "/ui/cloud-sync-chip", &cashUser).Body.String()
	if !strings.Contains(body, "Synced 5 min ago") {
		t.Errorf("cashier lost the time: %s", body)
	}
	if strings.Contains(body, "sb-cloud-sync-now") {
		t.Errorf("cashier sees Sync now: %s", body)
	}
}

func TestCloudSyncChip_StaleAndNever(t *testing.T) {
	mux, d := chipMux(t)
	setLast(t, d, 2*time.Hour)
	body := getAs(mux, "/ui/cloud-sync-chip", &mgrUser).Body.String()
	if !strings.Contains(body, "is-stale") || !strings.Contains(body, "has not heard from the cloud for a while") {
		t.Errorf("stale chip = %s", body)
	}
	if err := d.Settings.Set(t.Context(), enroll.KeyLastCheckinAt, ""); err != nil {
		t.Fatal(err)
	}
	body = getAs(mux, "/ui/cloud-sync-chip", &mgrUser).Body.String()
	if !strings.Contains(body, "Not synced yet") || !strings.Contains(body, "is-never") {
		t.Errorf("never chip = %s", body)
	}
}

func TestCloudSyncChip_EmptyWhen(t *testing.T) {
	cases := map[string]func(t *testing.T, d *common.Deps){
		"no credentials": func(t *testing.T, d *common.Deps) {
			d.Cfg.Marketplace.StoreID, d.Cfg.Marketplace.MerchantToken = "", ""
		},
		"replica": func(t *testing.T, d *common.Deps) {
			if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://main.local:8080"); err != nil {
				t.Fatal(err)
			}
		},
		"sync not allowed": func(t *testing.T, d *common.Deps) {
			if err := d.Settings.SetMany(t.Context(), subscriptionSeed("lapsed")); err != nil {
				t.Fatal(err)
			}
		},
		"refused credential": func(t *testing.T, d *common.Deps) {
			cloudAuthChipStatus = func() cloudsync.AuthChip { return cloudsync.AuthChip{Show: true} }
		},
		"replaced": func(t *testing.T, d *common.Deps) {
			enroll.MarkReplaced(t.Context(), d.Settings, "Till 2", time.Now().UTC().Format(time.RFC3339))
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			mux, d := chipMux(t)
			setLast(t, d, time.Minute)
			mutate(t, d)
			rec := getAs(mux, "/ui/cloud-sync-chip", &mgrUser)
			if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" {
				t.Fatalf("%s: %d %q, want empty 200", name, rec.Code, rec.Body.String())
			}
		})
	}
}

// answerKick plays the loop: after the kick it writes last_checkin_at.
func answerKick(d *common.Deps, stamp func() string) {
	go func() {
		select {
		case <-d.CloudSyncNow:
		case <-time.After(3 * time.Second):
			return
		}
		_ = d.Settings.Set(context.Background(), enroll.KeyLastCheckinAt, stamp())
	}()
}

func TestCloudSyncNow_ManagerKicksAndWaits(t *testing.T) {
	mux, d := chipMux(t)
	windows := recordWindows(t)
	setLast(t, d, 5*time.Minute)
	answerKick(d, func() string { return time.Now().Add(time.Second).UTC().Format(time.RFC3339) })
	rec := postForm(mux, "/api/cloud/sync-now", url.Values{}, &mgrUser)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Synced just now") {
		t.Fatalf("sync-now = %d %s", rec.Code, body)
	}
	if got := rec.Header().Get("HX-Trigger"); got != "ut-cloud-synced" {
		t.Fatalf("HX-Trigger = %q", got)
	}
	if w := windows(); len(w) != 1 || w[0] != operatorCheckinWindow {
		t.Fatalf("windows = %v", w)
	}
}

func TestCloudSyncNow_SameSecondCheckinStillCounts(t *testing.T) {
	mux, d := chipMux(t)
	refreshEntitlementWait = 3 * time.Second
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second + 20*time.Millisecond)))
	stamp := func() string { return time.Now().UTC().Format(time.RFC3339) }
	if err := d.Settings.Set(t.Context(), enroll.KeyLastCheckinAt, stamp()); err != nil {
		t.Fatal(err)
	}
	answerKick(d, stamp)
	rec := postForm(mux, "/api/cloud/sync-now", url.Values{}, &mgrUser)
	if rec.Header().Get("HX-Trigger") != "ut-cloud-synced" || strings.Contains(rec.Body.String(), "Still trying") {
		t.Fatalf("same-second check-in not counted: %s", rec.Body.String())
	}
}

func TestCloudSyncNow_SecondPressWithinFloorDoesNotKick(t *testing.T) {
	mux, d := chipMux(t)
	setLast(t, d, 5*time.Minute)
	answerKick(d, func() string { return time.Now().Add(time.Second).UTC().Format(time.RFC3339) })
	postForm(mux, "/api/cloud/sync-now", url.Values{}, &mgrUser)
	_ = kicked(d.CloudSyncNow)
	start := time.Now()
	rec := postForm(mux, "/api/cloud/sync-now", url.Values{}, &mgrUser)
	if kicked(d.CloudSyncNow) {
		t.Fatal("second press inside the floor kicked again")
	}
	if time.Since(start) >= refreshEntitlementWait || rec.Code != http.StatusOK {
		t.Fatalf("throttled press waited %v (code %d)", time.Since(start), rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Still trying") {
		t.Fatalf("throttled press shows a result line: %s", rec.Body.String())
	}
}

func TestCloudSyncNow_SharedFloorWithCheckNow(t *testing.T) {
	mux, d := chipMux(t)
	postForm(mux, "/api/subscription/check-now", url.Values{}, &mgrUser) // times out, but claims the floor
	_ = kicked(d.CloudSyncNow)
	postForm(mux, "/api/cloud/sync-now", url.Values{}, &mgrUser)
	if kicked(d.CloudSyncNow) {
		t.Fatal("Sync now kicked inside Check now's floor")
	}
}

func TestCloudSyncNow_TimeoutSaysStillTrying(t *testing.T) {
	mux, d := chipMux(t)
	setLast(t, d, 5*time.Minute)
	start := time.Now()
	rec := postForm(mux, "/api/cloud/sync-now", url.Values{}, &mgrUser)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Still trying") || !strings.Contains(body, "sales keep working offline") {
		t.Fatalf("timeout = %d %s", rec.Code, body)
	}
	if rec.Header().Get("HX-Trigger") != "" {
		t.Fatal("timeout fired ut-cloud-synced")
	}
	if took := time.Since(start); took > refreshEntitlementWait+1500*time.Millisecond {
		t.Fatalf("took %v", took)
	}
}

func TestCloudSyncNow_CashierGetsElevationPrompt(t *testing.T) {
	mux, d := chipMux(t)
	rec := postForm(mux, "/api/cloud/sync-now", url.Values{}, &cashUser)
	assertElevationPrompt(t, "/api/cloud/sync-now", rec.Code, rec.Body.String())
	if kicked(d.CloudSyncNow) {
		t.Fatal("a cashier's sync-now kicked")
	}
}

func TestCloudSyncNow_NotShownStateNoKick(t *testing.T) {
	mux, d := chipMux(t)
	d.Cfg.Marketplace.StoreID, d.Cfg.Marketplace.MerchantToken = "", ""
	rec := postForm(mux, "/api/cloud/sync-now", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" || kicked(d.CloudSyncNow) {
		t.Fatalf("not-shown sync-now = %d %q", rec.Code, rec.Body.String())
	}
}

// Review finding 2: a till with no chip answers empty before any PIN prompt,
// so a crafted cashier POST on an unpaid or replica till gets nothing to approve.
func TestCloudSyncNow_NotShownStateNoPromptForCashier(t *testing.T) {
	mux, d := chipMux(t)
	d.Cfg.Marketplace.StoreID, d.Cfg.Marketplace.MerchantToken = "", ""
	rec := postForm(mux, "/api/cloud/sync-now", url.Values{}, &cashUser)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" || kicked(d.CloudSyncNow) {
		t.Fatalf("not-shown cashier sync-now = %d %q", rec.Code, rec.Body.String())
	}
}

func TestCloudSyncNow_GetIs405(t *testing.T) {
	mux, _ := chipMux(t)
	if rec := getAs(mux, "/api/cloud/sync-now", &mgrUser); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d", rec.Code)
	}
}
