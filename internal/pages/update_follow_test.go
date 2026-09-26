package pages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/selfupdate"
	"github.com/universaltill/universal-till/internal/updates"
)

// ut-docs#2738: an additional (replica) till follows its main till's version
// by itself — exactly that version, never past it, never a downgrade, never
// mid-sale — instead of the nightly "latest" path main and standalone tills use.

func TestFollowDecision(t *testing.T) {
	base := followInputs{Replica: true, This: "1.3.0", Target: "1.4.0", Supported: true, ChecksOn: true}
	with := func(f func(*followInputs)) followInputs { in := base; f(&in); return in }
	for _, c := range []struct {
		name string
		in   followInputs
		act  bool
		why  string
	}{
		{"main till newer: follow", base, true, "follow"},
		{"v-prefixed versions compare", with(func(in *followInputs) { in.This, in.Target = "v1.3.0", "v1.4.0" }), true, "follow"},
		{"main or standalone till never follows", with(func(in *followInputs) { in.Replica = false }), false, "not_replica"},
		{"no target yet", with(func(in *followInputs) { in.Target = "" }), false, "not_release"},
		{"equal: nothing to do", with(func(in *followInputs) { in.Target = "1.3.0" }), false, "not_newer"},
		{"older main till: never a downgrade", with(func(in *followInputs) { in.Target = "1.2.9" }), false, "not_newer"},
		{"dev build never self-updates", with(func(in *followInputs) { in.This = "dev" }), false, "not_release"},
		{"a dev main till is no target", with(func(in *followInputs) { in.Target = "dev" }), false, "not_release"},
		{"the shop switched updates off", with(func(in *followInputs) { in.AutoEnabled = "false" }), false, "off"},
		{"explicit on still follows", with(func(in *followInputs) { in.AutoEnabled = "true" }), true, "follow"},
		{"update checks disabled on this install", with(func(in *followInputs) { in.ChecksOn = false }), false, "off"},
		{"cannot self-install (Windows/Android/unwritable)", with(func(in *followInputs) { in.Supported = false }), false, "unsupported"},
		{"open sale or self-order/table session", with(func(in *followInputs) { in.Busy = true }), false, "busy"},
		{"already attempted this target in this run", with(func(in *followInputs) { in.AttemptedThisRun = "1.4.0" }), false, "attempted"},
		{"attempted an older target this run: a new target is tried", with(func(in *followInputs) { in.AttemptedThisRun = "1.3.5" }), true, "follow"},
		{"failed last run: retried at the next start", with(func(in *followInputs) { in.LastAttempted, in.LastError = "1.4.0", "failed:download" }), true, "follow"},
		{"interrupted last run: retried at the next start", with(func(in *followInputs) { in.LastAttempted, in.LastError = "1.4.0", followApplying }), true, "follow"},
		{"applied last run but still behind: never loops", with(func(in *followInputs) { in.LastAttempted, in.LastError = "1.4.0", "" }), false, "no_effect"},
		{"applied an older target last run: the new one is tried", with(func(in *followInputs) { in.LastAttempted, in.LastError = "1.3.5", "" }), true, "follow"},
	} {
		t.Run(c.name, func(t *testing.T) {
			act, why := followDecision(c.in)
			if act != c.act || why != c.why {
				t.Fatalf("followDecision(%+v) = (%v, %q), want (%v, %q)", c.in, act, why, c.act, c.why)
			}
		})
	}
}

// The target is the live link's hello version, else the version the last
// ping answered with (a polling replica, or before the link is up).
func TestFollowTargetOf(t *testing.T) {
	for _, c := range []struct {
		name   string
		st     fleetlink.ClientStatus
		has    bool
		stored string
		want   string
	}{
		{"linked: the hello's version", fleetlink.ClientStatus{Linked: true, MainVersion: "1.5.0"}, true, "1.4.0", "1.5.0"},
		{"not linked: the pinged version", fleetlink.ClientStatus{MainVersion: "1.5.0"}, true, "1.4.0", "1.4.0"},
		{"no link client: the pinged version", fleetlink.ClientStatus{}, false, "1.4.0", "1.4.0"},
		{"linked, hello without a version: the pinged version", fleetlink.ClientStatus{Linked: true}, true, "1.4.0", "1.4.0"},
		{"nothing known", fleetlink.ClientStatus{}, false, "", ""},
	} {
		if got := followTargetOf(c.has, c.st, c.stored); got != c.want {
			t.Errorf("%s: followTargetOf = %q, want %q", c.name, got, c.want)
		}
	}
}

// reset forgets this run's attempt, as a restart does.
func (a *followAttempt) reset() { a.set("") }

// stubFollowSeams pins this build's version, Supported() and ApplyVersion,
// and clears the in-process attempt, restoring all of it afterwards.
func stubFollowSeams(t *testing.T, this string, supported bool, applyErr error) *[]string {
	t.Helper()
	origVer, origSupported, origApplyVersion, origApply := autoUpdateBuildVersion, autoUpdateSupported, autoUpdateApplyVersion, autoUpdateApply
	t.Cleanup(func() {
		autoUpdateBuildVersion, autoUpdateSupported, autoUpdateApplyVersion, autoUpdateApply = origVer, origSupported, origApplyVersion, origApply
		followRun.reset()
	})
	followRun.reset()
	var applied []string
	autoUpdateBuildVersion = func() string { return this }
	autoUpdateSupported = func() bool { return supported }
	autoUpdateApplyVersion = func(_ context.Context, v string, _ func() bool) error {
		applied = append(applied, v)
		return applyErr
	}
	autoUpdateApply = func(context.Context, func() bool) error {
		t.Fatal("a replica ran the nightly latest-release path")
		return nil
	}
	return &applied
}

func newFollowReplicaDeps(t *testing.T, mainVersion string) *common.Deps {
	t.Helper()
	dp := newAutoUpdateTestDeps(t)
	ctx := t.Context()
	_ = dp.Settings.Set(ctx, "sync.primary_url", "http://main.local:8080")
	_ = dp.Settings.Set(ctx, keyMainVersion, mainVersion)
	return dp
}

func TestFollowTick_InstallsExactlyTheMainTillsVersionOnce(t *testing.T) {
	dp := newFollowReplicaDeps(t, "1.4.0")
	applied := stubFollowSeams(t, "1.3.0", true, nil)

	autoUpdateTick(t.Context(), dp, tickNow)
	autoUpdateTick(t.Context(), dp, tickNow.Add(30*time.Second)) // a later tick in the same run

	if len(*applied) != 1 || (*applied)[0] != "1.4.0" {
		t.Fatalf("applied %v, want exactly one ApplyVersion(1.4.0)", *applied)
	}
	if got := settingOf(t, dp, keyFollowAttempted); got != "1.4.0" {
		t.Fatalf("%s = %q, want 1.4.0", keyFollowAttempted, got)
	}
	if got := settingOf(t, dp, keyFollowError); got != "" {
		t.Fatalf("%s = %q after a staged update, want empty", keyFollowError, got)
	}
}

// A replica never runs the nightly path, even with the switch explicitly on
// and inside its nightly window: it would install latest, past its main till.
func TestFollowTick_ReplicaNeverRunsTheNightlyPath(t *testing.T) {
	dp := newFollowReplicaDeps(t, "1.3.0") // same as this till: nothing to follow
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateEnabled, "true")
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateTime, tickHHMM)
	stubAutoUpdateSeams(t, updates.Status{Available: true, Latest: "9.9.9"}, updates.Status{Available: true, Latest: "9.9.9"}, true, nil)
	applied := stubFollowSeams(t, "1.3.0", true, nil) // autoUpdateApply fails the test if called

	autoUpdateTick(t.Context(), dp, tickNow)
	if len(*applied) != 0 {
		t.Fatalf("applied %v with the main till on the same version", *applied)
	}
}

// Main and standalone tills keep the nightly path; the follow never runs.
func TestFollowTick_NeverOnAMainTill(t *testing.T) {
	dp := newAutoUpdateTestDeps(t)
	_ = dp.Settings.Set(t.Context(), keyMainVersion, "9.0.0") // stale, from an earlier life as a replica
	origApplyVersion := autoUpdateApplyVersion
	t.Cleanup(func() { autoUpdateApplyVersion = origApplyVersion; followRun.reset() })
	autoUpdateApplyVersion = func(context.Context, string, func() bool) error {
		t.Fatal("a main till followed a version")
		return nil
	}
	applyCalls := stubAutoUpdateSeams(t, updates.Status{Available: true}, updates.Status{Available: true}, true, nil)
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateTime, tickHHMM)

	autoUpdateTick(t.Context(), dp, tickNow)
	if *applyCalls != 1 {
		t.Fatalf("the main till's nightly apply ran %d times, want 1", *applyCalls)
	}
}

func TestFollowTick_WaitsForTheOpenSale(t *testing.T) {
	dp := newFollowReplicaDeps(t, "1.4.0")
	applied := stubFollowSeams(t, "1.3.0", true, nil)
	dp.Engine.AddLineWithModifiers(pos.BasketLine{SKU: "sku-1", Name: "Coffee", Qty: 1, PriceCents: 250}, 1, nil)

	autoUpdateTick(t.Context(), dp, tickNow)
	if len(*applied) != 0 || settingOf(t, dp, keyFollowAttempted) != "" {
		t.Fatalf("followed mid-sale: applied %v, attempted %q", *applied, settingOf(t, dp, keyFollowAttempted))
	}

	dp.Engine = pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000}, nil) // the sale is done
	autoUpdateTick(t.Context(), dp, tickNow.Add(30*time.Second))
	if len(*applied) != 1 {
		t.Fatalf("applied %v once the sale cleared, want one attempt", *applied)
	}
}

// A failing release is attempted once per run (no download loop), recorded
// as failed:<code>, and retried at the next start or when the target moves.
func TestFollowTick_FailureRecordedAndNotRetriedUntilRestartOrNewTarget(t *testing.T) {
	dp := newFollowReplicaDeps(t, "1.4.0")
	applied := stubFollowSeams(t, "1.3.0", true, errors.New("download: HTTP 503"))

	autoUpdateTick(t.Context(), dp, tickNow)
	autoUpdateTick(t.Context(), dp, tickNow.Add(30*time.Second))
	if len(*applied) != 1 {
		t.Fatalf("applied %v, want exactly one attempt in this run", *applied)
	}
	if got := settingOf(t, dp, keyFollowError); got != "failed:download" {
		t.Fatalf("%s = %q, want failed:download", keyFollowError, got)
	}

	followRun.reset() // the till restarts
	autoUpdateTick(t.Context(), dp, tickNow)
	if len(*applied) != 2 {
		t.Fatalf("applied %v, want a retry after the restart", *applied)
	}

	_ = dp.Settings.Set(t.Context(), keyMainVersion, "1.5.0") // the main till moved on
	autoUpdateTick(t.Context(), dp, tickNow)
	if len(*applied) != 3 || (*applied)[2] != "1.5.0" {
		t.Fatalf("applied %v, want the new target tried", *applied)
	}
}

func TestFollowErrorCode(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{selfupdate.ErrUnsupported, "unsupported"},
		{errors.New("an update is already being applied"), "busy"},
		{errors.New("releases API: HTTP 404"), "release"},
		{errors.New("download: HTTP 503"), "download"},
		{errors.New("checksum mismatch: got a want b"), "checksum"},
		{errors.New("release v1.4.0 has no checksums.txt — refusing"), "checksum"},
		{errors.New("no release archive for linux/arm64 (x)"), "no_archive"},
		{errors.New("no macOS .dmg in release v1.4.0"), "no_archive"},
		{errors.New("back up current binary: permission denied"), "install"},
		{&url.Error{Op: "Get", URL: "https://api.github.com/x", Err: errors.New("dial tcp: no route to host")}, "network"},
	} {
		if got := followErrorCode(c.err); got != c.want {
			t.Errorf("followErrorCode(%q) = %q, want %q", c.err, got, c.want)
		}
	}
}

// The chip's two states: following (this till will install it by itself)
// and manual (it can't: Windows/Android/unwritable, switched off, or the
// attempt failed or didn't take).
func TestFollowCanInstall(t *testing.T) {
	base := followInputs{Replica: true, This: "1.3.0", Target: "1.4.0", Supported: true, ChecksOn: true}
	with := func(f func(*followInputs)) followInputs { in := base; f(&in); return in }
	for _, c := range []struct {
		name string
		in   followInputs
		want bool
	}{
		{"can follow", base, true},
		{"busy is still 'following' (it waits)", with(func(in *followInputs) { in.Busy = true }), true},
		{"applying now", with(func(in *followInputs) { in.LastAttempted, in.LastError = "1.4.0", followApplying }), true},
		{"unsupported install", with(func(in *followInputs) { in.Supported = false }), false},
		{"switched off", with(func(in *followInputs) { in.AutoEnabled = "false" }), false},
		{"checks disabled", with(func(in *followInputs) { in.ChecksOn = false }), false},
		{"this target failed", with(func(in *followInputs) { in.LastAttempted, in.LastError = "1.4.0", "failed:checksum" }), false},
		{"this target applied without effect", with(func(in *followInputs) { in.LastAttempted, in.LastError = "1.4.0", "" }), false},
		{"an older target failed: the new one can still follow", with(func(in *followInputs) { in.LastAttempted, in.LastError = "1.3.5", "failed:checksum" }), true},
	} {
		if got := followCanInstall(c.in); got != c.want {
			t.Errorf("%s: followCanInstall = %v, want %v", c.name, got, c.want)
		}
	}
}

// GET /api/sync/ping carries the main till's version, so a replica without
// the live link still has a target.
func TestSyncPing_CarriesTheMainTillsVersion(t *testing.T) {
	f := newSyncLinkFixture(t)
	f.enrol(t, "Till 2", "bearer-t2")
	req := httptest.NewRequest(http.MethodGet, "/api/sync/ping", nil)
	req.Header.Set("Authorization", "Bearer bearer-t2")
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	var out struct {
		Data struct {
			Version *string `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: %v", rec.Body, err)
	}
	if out.Data.Version == nil || *out.Data.Version == "" {
		t.Fatalf("ping = %s, want a version", rec.Body)
	}
}

// The replica's probe stores the pinged version per-till as sync.main_version
// — here against an older main till without the link, the polling case.
func TestReplicaProbe_StoresTheMainTillsVersion(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sync/ping", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"till_id": "t", "version": "1.4.0"}, "error": nil})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	replica := linkReplica(t, srv.URL, "till-2", fastLinkClientOptions())
	runReplica(t, replica, time.Hour, time.Hour)
	if !waitFor(t, 3*time.Second, func() bool { return settingOf(t, replica, keyMainVersion) == "1.4.0" }) {
		t.Fatalf("%s = %q, want 1.4.0 from the ping", keyMainVersion, settingOf(t, replica, keyMainVersion))
	}
}

// The restart gate handed to ApplyVersionWhenIdle reads the live basket: a
// sale opened while the release downloads holds the restart (ut-docs#2738
// review — the pre-download check alone would restart mid-sale).
func TestFollowTick_RestartGateReadsTheLiveBasket(t *testing.T) {
	dp := newFollowReplicaDeps(t, "1.4.0")
	stubFollowSeams(t, "1.3.0", true, nil)
	var gate func() bool
	autoUpdateApplyVersion = func(_ context.Context, _ string, idle func() bool) error {
		gate = idle
		return nil
	}

	autoUpdateTick(t.Context(), dp, tickNow)
	if gate == nil {
		t.Fatal("the follow applied without a restart gate")
	}
	if !gate() {
		t.Fatal("gate says busy with an empty basket")
	}
	dp.Engine.AddLineWithModifiers(pos.BasketLine{SKU: "sku-1", Name: "Coffee", Qty: 1, PriceCents: 250}, 1, nil)
	if gate() {
		t.Fatal("gate says idle while a sale is open — the restart would destroy it")
	}
}

// selfupdate.Supported() probes the disk (a temp file in the exe dir and the
// cwd), and the status-bar chip polls every 5 s on every till. It must only
// run when a replica's target is actually newer (ut-docs#2738 review).
func TestFollowInputsOf_ProbesSupportedOnlyWhenBehind(t *testing.T) {
	origVer, origSupported := autoUpdateBuildVersion, autoUpdateSupported
	t.Cleanup(func() { autoUpdateBuildVersion, autoUpdateSupported = origVer, origSupported })
	probes := 0
	autoUpdateSupported = func() bool { probes++; return true }
	autoUpdateBuildVersion = func() string { return "1.4.0" }

	mainTill := newAutoUpdateTestDeps(t)
	_ = followInputsOf(t.Context(), mainTill)
	current := newFollowReplicaDeps(t, "1.4.0")
	_ = followInputsOf(t.Context(), current)
	if probes != 0 {
		t.Fatalf("Supported() probed %d times for a main till and a current replica, want 0", probes)
	}
	behind := newFollowReplicaDeps(t, "1.5.0")
	if in := followInputsOf(t.Context(), behind); !in.Supported || probes != 1 {
		t.Fatalf("behind replica: Supported=%v after %d probes, want true after 1", in.Supported, probes)
	}
}
