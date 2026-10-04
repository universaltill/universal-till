package pages

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/updates"
)

// ut-docs#2733: a linux till whose install tree the service user can't write
// (the .deb postinstall chown, ut-docs#151) makes selfupdate.Supported() false,
// and autoUpdateTick then returns silently every night while Settings still
// shows "Update automatically" ticked. autoUpdateStuckFor is the one decision
// behind the status chip and the Settings "Check now" line that say so.
func TestAutoUpdateStuckFor(t *testing.T) {
	cases := []struct {
		name                                    string
		goos                                    string
		enabled, available, unwritable, replica bool
		want                                    bool
	}{
		{"linux, on, update waiting, install folder unwritable", "linux", true, true, true, false, true},
		{"auto-update off", "linux", false, true, true, false, false},
		{"no update waiting", "linux", true, false, true, false, false},
		{"folder writable (self-installs, or unsupported for another reason)", "linux", true, true, false, false, false},
		{"replica: its follow chip reports this (ut-docs#2738)", "linux", true, true, true, true, false},
		{"windows portable zip: the download-link chip covers it", "windows", true, true, true, false, false},
		{"darwin: different remedy than a .deb", "darwin", true, true, true, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			probed := false
			unwritable := func() bool { probed = true; return c.unwritable }
			got := autoUpdateStuckFor(c.goos, c.enabled, c.available, c.replica, unwritable)
			if got != c.want {
				t.Fatalf("autoUpdateStuckFor = %v, want %v", got, c.want)
			}
			// The unwritable probe touches the disk; only ask when every cheap input
			// already says "stuck if unsupported".
			if wantProbe := c.goos == "linux" && c.enabled && c.available && !c.replica; probed != wantProbe {
				t.Fatalf("Supported() probed = %v, want %v", probed, wantProbe)
			}
		})
	}
}

// stubAutoUpdateGOOS also stubs the install-folder probe (unwritable) and
// empties its TTL cache; it returns the probe's call count.
func stubAutoUpdateGOOS(t *testing.T, goos string, unwritable bool) *int {
	t.Helper()
	origGOOS, origProbe := autoUpdateGOOS, autoUpdateInstallUnwritable
	resetCache := func() {
		autoUpdateUnwritableCache.Lock()
		autoUpdateUnwritableCache.at = time.Time{}
		autoUpdateUnwritableCache.Unlock()
	}
	t.Cleanup(func() {
		autoUpdateGOOS, autoUpdateInstallUnwritable = origGOOS, origProbe
		resetCache()
		updates.SetAutoUpdateStuck(false)
	})
	resetCache()
	calls := 0
	autoUpdateGOOS = goos
	autoUpdateInstallUnwritable = func() bool { calls++; return unwritable }
	return &calls
}

// Every scheduler tick publishes the flag (not only the due one), so the chip
// appears within ~30 s of an update becoming available, not the next night.
func TestAutoUpdateTick_PublishesStuckEveryTick(t *testing.T) {
	dp := newAutoUpdateTestDeps(t)
	stubAutoUpdateGOOS(t, "linux", true)
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateTime, "03:00") // not due at tickNow
	stubAutoUpdateSeams(t, updates.Status{Available: true, Latest: "9.9.9"}, updates.Status{}, false, nil)

	autoUpdateTick(t.Context(), dp, tickNow)
	if !updates.AutoUpdateStuck() {
		t.Fatal("expected the stuck flag published for a linux till with auto-update on (default), an update waiting and no self-install")
	}

	_ = dp.Settings.Set(t.Context(), keyAutoUpdateEnabled, "false")
	autoUpdateTick(t.Context(), dp, tickNow)
	if updates.AutoUpdateStuck() {
		t.Fatal("expected the stuck flag cleared once auto-update is switched off")
	}
}

func TestAutoUpdateTick_ReplicaNeverStuck(t *testing.T) {
	dp := newAutoUpdateTestDeps(t)
	stubAutoUpdateGOOS(t, "linux", true)
	updates.SetAutoUpdateStuck(true)
	_ = dp.Settings.Set(t.Context(), "sync.primary_url", "http://127.0.0.1:1")
	stubAutoUpdateSeams(t, updates.Status{Available: true, Latest: "9.9.9"}, updates.Status{}, false, nil)
	autoUpdateTick(t.Context(), dp, tickNow) // follow path: 127.0.0.1:1 refuses at once
	if updates.AutoUpdateStuck() {
		t.Fatal("a replica's follow chip reports an unsupported install; the nightly stuck flag must stay off")
	}
}

// Settings → Software update → Check now: same combination, same message.
func TestUpdateCheck_StuckSaysWhatToDo(t *testing.T) {
	for _, enabled := range []string{"", "false"} {
		t.Run("auto_enabled="+enabled, func(t *testing.T) {
			dp := newAutoUpdateTestDeps(t)
			stubAutoUpdateGOOS(t, "linux", true)
			t.Setenv("UT_AUTH", "off")
			stubAutoUpdateSeams(t, updates.Status{}, updates.Status{}, false, nil)
			origCheck := updateCheckNow
			t.Cleanup(func() { updateCheckNow = origCheck })
			updateCheckNow = func(context.Context) updates.Status { return updates.Status{Available: true, Latest: "9.9.9"} }
			if enabled != "" {
				_ = dp.Settings.Set(t.Context(), keyAutoUpdateEnabled, enabled)
			}
			mux := http.NewServeMux()
			registerUpdateAPI(mux, dp)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/update/check", nil))
			body := rec.Body.String()
			stuck := strings.Contains(body, html.EscapeString(httpx.T("en", "settings.update.auto_blocked")))
			if want := enabled == ""; stuck != want {
				t.Fatalf("stuck message shown = %v, want %v; body %q", stuck, want, body)
			}
			if !strings.Contains(body, "9.9.9") {
				t.Fatalf("expected the available version in the line, got %q", body)
			}
		})
	}
}

// The scheduler ticks every 30 s; the folder probe writes two temp files, so
// it runs at most once per autoUpdateUnwritableTTL (review finding 2).
func TestAutoUpdateTick_ProbesInstallFolderAtMostOncePerTTL(t *testing.T) {
	dp := newAutoUpdateTestDeps(t)
	calls := stubAutoUpdateGOOS(t, "linux", true)
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateTime, "03:00")
	stubAutoUpdateSeams(t, updates.Status{Available: true, Latest: "9.9.9"}, updates.Status{}, false, nil)

	for i := 0; i < 5; i++ {
		autoUpdateTick(t.Context(), dp, tickNow.Add(time.Duration(i)*30*time.Second))
	}
	if *calls != 1 {
		t.Fatalf("probe ran %d times in 2 minutes of ticks, want 1", *calls)
	}
	autoUpdateTick(t.Context(), dp, tickNow.Add(autoUpdateUnwritableTTL))
	if *calls != 2 {
		t.Fatalf("probe ran %d times after the TTL, want 2", *calls)
	}
}

// A "dev" build never auto-applies (ut-docs#369), so it is never "stuck".
func TestAutoUpdateTick_DevBuildNeverStuck(t *testing.T) {
	dp := newAutoUpdateTestDeps(t)
	calls := stubAutoUpdateGOOS(t, "linux", true)
	stubAutoUpdateSeams(t, updates.Status{Available: true, Latest: "9.9.9"}, updates.Status{}, false, nil)
	autoUpdateBuildVersion = func() string { return "dev" }
	autoUpdateTick(t.Context(), dp, tickNow)
	if updates.AutoUpdateStuck() || *calls != 0 {
		t.Fatalf("dev build: stuck=%v probes=%d, want false/0", updates.AutoUpdateStuck(), *calls)
	}
}

// Check now on a till that is fixed since the last tick clears the chip at
// once (review finding 6).
func TestUpdateCheck_SupportedClearsStuck(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newAutoUpdateTestDeps(t)
	stubAutoUpdateGOOS(t, "linux", false)
	stubAutoUpdateSeams(t, updates.Status{}, updates.Status{}, true, nil)
	origCheck := updateCheckNow
	t.Cleanup(func() { updateCheckNow = origCheck })
	updateCheckNow = func(context.Context) updates.Status { return updates.Status{Available: true, Latest: "9.9.9"} }
	updates.SetAutoUpdateStuck(true)
	mux := http.NewServeMux()
	registerUpdateAPI(mux, dp)
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/update/check", nil))
	if updates.AutoUpdateStuck() {
		t.Fatal("expected Check now on a self-updatable till to clear the stuck flag")
	}
}
