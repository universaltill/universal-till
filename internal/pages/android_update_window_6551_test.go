package pages

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/updates"
)

// ut-docs#6551 slice 3: the Android install window. The nightly step is a
// download in the shared update.auto_time slot (+ this till's own offset);
// the manager's install tap is not time-gated. AC1-AC5 pin existing
// behaviour (they pass before the change); AC7-AC9 are new.

// windowAt derives an instant on tickNow's day (or days later) so no new
// fixed date is written (ut-docs#5467).
func windowAt(day, h, m int) time.Time {
	y, mo, dd := tickNow.Date()
	return time.Date(y, mo, dd, h, m, 0, 0, time.UTC).AddDate(0, 0, day)
}

func windowDeps(t *testing.T, hhmm string) (*common.Deps, *fakePrefetcher, *int) {
	t.Helper()
	dp := newAutoUpdateTestDeps(t)
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateEnabled, "true")
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateTime, hhmm)
	fp, applyCalls := stubAndroidTick(t, updates.Status{Available: true}, updates.Status{Available: true}, true)
	return dp, fp, applyCalls
}

func TestAutoUpdateTick_AndroidPrefetchFollowsConfiguredWindow(t *testing.T) {
	cases := []struct {
		name  string
		h, m  int
		calls int
	}{
		{"window opens", 22, 15, 1},
		{"last minute in", 22, 44, 1},
		{"before window", 22, 14, 0},
		{"window closed", 22, 45, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dp, fp, applyCalls := windowDeps(t, "22:15")
			autoUpdateTick(t.Context(), dp, windowAt(0, c.h, c.m))
			if fp.calls != c.calls {
				t.Fatalf("tick %02d:%02d: PrefetchUpdate calls=%d, want %d", c.h, c.m, fp.calls, c.calls)
			}
			wantSlot := ""
			if c.calls == 1 {
				wantSlot = tickNow.Format("2006-01-02")
			}
			if got := slotOf(t, dp); got != wantSlot {
				t.Fatalf("slot=%q, want %q", got, wantSlot)
			}
			if *applyCalls != 0 {
				t.Fatalf("Apply called %d times on Android, want 0", *applyCalls)
			}
		})
	}
}

func TestAutoUpdateTick_AndroidPrefetchMovesWithAutoTime(t *testing.T) {
	dp, fp, _ := windowDeps(t, "22:15")
	autoUpdateTick(t.Context(), dp, windowAt(0, 3, 0))
	if fp.calls != 0 || slotOf(t, dp) != "" {
		t.Fatalf("03:00 with auto_time 22:15: calls=%d slot=%q, want none", fp.calls, slotOf(t, dp))
	}
	autoUpdateTick(t.Context(), dp, windowAt(0, 22, 20))
	if fp.calls != 1 {
		t.Fatalf("22:20 with auto_time 22:15: calls=%d, want 1", fp.calls)
	}
}

func TestAutoUpdateTick_AndroidPrefetchCrossesMidnight(t *testing.T) {
	dp, fp, _ := windowDeps(t, "23:50")
	autoUpdateTick(t.Context(), dp, windowAt(1, 0, 10))
	if fp.calls != 1 {
		t.Fatalf("00:10 in a 23:50 window: calls=%d, want 1", fp.calls)
	}
	if got, want := slotOf(t, dp), tickNow.Format("2006-01-02"); got != want {
		t.Fatalf("slot=%q, want the day the window opened (%q)", got, want)
	}
	autoUpdateTick(t.Context(), dp, windowAt(1, 0, 15))
	if fp.calls != 1 {
		t.Fatalf("00:15 same window prefetched again: calls=%d, want 1", fp.calls)
	}
}

func TestAutoUpdateTick_AndroidPrefetchHonoursJitter(t *testing.T) {
	dp, fp, _ := windowDeps(t, "03:00")
	orig := autoUpdateJitter
	autoUpdateJitter = func(context.Context, *common.Deps) time.Duration { return 12 * time.Minute }
	t.Cleanup(func() { autoUpdateJitter = orig })

	autoUpdateTick(t.Context(), dp, windowAt(0, 3, 5))
	if fp.calls != 0 || slotOf(t, dp) != "" {
		t.Fatalf("03:05 before this till's 03:12 start: calls=%d slot=%q", fp.calls, slotOf(t, dp))
	}
	autoUpdateTick(t.Context(), dp, windowAt(0, 3, 12))
	if fp.calls != 1 {
		t.Fatalf("03:12: calls=%d, want 1", fp.calls)
	}
}

func TestAutoUpdateTick_AndroidPrefetchDefaultTimeWhenUnset(t *testing.T) {
	for _, c := range []struct{ h, m, calls int }{{3, 0, 1}, {10, 0, 0}} {
		dp := newAutoUpdateTestDeps(t)
		fp, _ := stubAndroidTick(t, updates.Status{Available: true}, updates.Status{Available: true}, true)
		autoUpdateTick(t.Context(), dp, windowAt(0, c.h, c.m))
		if fp.calls != c.calls {
			t.Fatalf("unset schedule, tick %02d:%02d: calls=%d, want %d", c.h, c.m, fp.calls, c.calls)
		}
	}
}

// The manager's tap is never time-gated (AC6).
func TestAndroidInstall_NotTimeGated(t *testing.T) {
	api, d := androidInstallMux(t)
	hhmm := time.Now().Add(12 * time.Hour).Format("15:04")
	_ = d.Settings.Set(t.Context(), keyAutoUpdateEnabled, "true")
	_ = d.Settings.Set(t.Context(), keyAutoUpdateTime, hhmm)
	if in, _ := autoUpdateSlot(time.Now(), hhmm, autoUpdateJitter(t.Context(), d)); in {
		t.Fatal("precondition: now must be outside the download window")
	}
	orig := androidInstallCheckNow
	androidInstallCheckNow = func(context.Context) updates.Status { return updates.Status{Available: true} }
	t.Cleanup(func() { androidInstallCheckNow = orig })

	rec := postForm(api, "/api/update/android-install", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("install tap outside the window = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func windowLine(t *testing.T, from, to string) string {
	t.Helper()
	tmpl := httpx.T("en", "settings.update.android.download_window")
	if tmpl == "settings.update.android.download_window" {
		t.Fatal("download_window key is missing from en.json")
	}
	return html.EscapeString(fmt.Sprintf(tmpl, from, to))
}

func pinJitter(t *testing.T, d time.Duration) {
	t.Helper()
	orig := autoUpdateJitter
	autoUpdateJitter = func(context.Context, *common.Deps) time.Duration { return d }
	t.Cleanup(func() { autoUpdateJitter = orig })
}

func TestSettingsPage_AndroidShowsDownloadWindow(t *testing.T) {
	pinJitter(t, 7*time.Minute)
	mux, _, d := newFullAuthDeps(t)
	_ = d.Settings.Set(t.Context(), keyAutoUpdateTime, "03:00")

	body := renderSettingsAs(t, mux, mgrUser, true)
	if !strings.Contains(body, `data-testid="update-android-window"`) {
		t.Fatal("no download-window line on a sideload Android till")
	}
	if !strings.Contains(body, windowLine(t, "03:07", "03:37")) {
		t.Fatalf("line text missing 03:07-03:37")
	}
	if !regexp.MustCompile(`(?s)<fieldset class="set-lock" data-lock-region="settings-update-schedule".*?data-testid="update-android-window".*?</fieldset>`).MatchString(body) {
		t.Fatal("the line must sit inside the settings-update-schedule fieldset")
	}
	if !strings.Contains(body, `aria-describedby="update-android-window"`) || !strings.Contains(body, `id="update-android-window"`) {
		t.Fatal("time input must be described by the line (aria-describedby + id)")
	}

	_ = d.Settings.Set(t.Context(), keyAutoUpdateTime, "23:50")
	body = renderSettingsAs(t, mux, mgrUser, true)
	if !strings.Contains(body, windowLine(t, "23:57", "00:27")) {
		t.Fatal("cross-midnight window 23:57-00:27 not rendered")
	}
}

func TestSettingsPage_AndroidDownloadWindowHidden(t *testing.T) {
	cases := []struct {
		name   string
		bridge bool
		setup  func(t *testing.T, d *common.Deps)
		follow bool
	}{
		{"bridge off", false, func(t *testing.T, d *common.Deps) {}, false},
		{"store channel", true, func(t *testing.T, d *common.Deps) {
			t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelPlay))
		}, false},
		{"replica", true, func(t *testing.T, d *common.Deps) {
			setReplicaSettings(t, d.Settings, "http://127.0.0.1:1", syncSettingsBearer)
		}, true},
		{"auto off", true, func(t *testing.T, d *common.Deps) {
			_ = d.Settings.Set(t.Context(), keyAutoUpdateEnabled, "false")
		}, false},
		{"invalid stored time", true, func(t *testing.T, d *common.Deps) {
			_ = d.Settings.Set(t.Context(), keyAutoUpdateTime, "25:99")
		}, false},
		{"legacy non-canonical time", true, func(t *testing.T, d *common.Deps) {
			_ = d.Settings.Set(t.Context(), keyAutoUpdateTime, "3:05")
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pinJitter(t, 7*time.Minute)
			mux, _, d := newFullAuthDeps(t)
			c.setup(t, d)
			body := renderSettingsAs(t, mux, mgrUser, c.bridge)
			if strings.Contains(body, `data-testid="update-android-window"`) {
				t.Fatal("download-window line rendered where it must be hidden")
			}
			if strings.Contains(body, `aria-describedby="update-android-window"`) {
				t.Fatal("dangling aria-describedby")
			}
			if c.follow && !strings.Contains(body, `data-testid="update-follows-main"`) {
				t.Fatal("replica must show the follow line instead")
			}
		})
	}
}

func TestPostSettingsUpdateSchedule_RefusesInvalidTimeWhenDisabled(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newEODTestDeps(t)
	mux := http.NewServeMux()
	registerUpdateAPI(mux, dp)
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateTime, "04:00")
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateEnabled, "")

	post := func(body string) int {
		return postForm(mux, "/api/settings/update-schedule", mustValues(t, body), nil).Code
	}
	if code := post("enabled=off&time=25:99"); code != http.StatusBadRequest {
		t.Fatalf("disabled + garbage time = %d, want 400", code)
	}
	if v, _, _ := dp.Settings.Get(t.Context(), keyAutoUpdateTime); v != "04:00" {
		t.Fatalf("refused save changed auto_time to %q", v)
	}
	if v, _, _ := dp.Settings.Get(t.Context(), keyAutoUpdateEnabled); v != "" {
		t.Fatalf("refused save changed auto_enabled to %q", v)
	}
	if code := post("enabled=off&time="); code != http.StatusNoContent {
		t.Fatalf("disabled + empty time = %d, want 204", code)
	}
	if code := post("enabled=off&time=02:00"); code != http.StatusNoContent {
		t.Fatalf("disabled + valid time = %d, want 204", code)
	}
	if v, _, _ := dp.Settings.Get(t.Context(), keyAutoUpdateTime); v != "02:00" {
		t.Fatalf("auto_time=%q, want 02:00", v)
	}
}

func mustValues(t *testing.T, q string) url.Values {
	t.Helper()
	v, err := url.ParseQuery(q)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
