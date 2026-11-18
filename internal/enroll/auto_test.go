package enroll

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/config"
)

// ADR-0200 (ut-docs#6559): automatic registration. The notice text behind
// NoticeVersion is a DRAFT pending owner approval.

func setReleaseVersion(t *testing.T, v string) {
	t.Helper()
	old := buildinfo.Version
	buildinfo.Version = v
	t.Cleanup(func() { buildinfo.Version = old })
}

// readyKV is a set-up till that has shown the notice: every guard passes.
func readyKV(t *testing.T) *fakeKV {
	t.Helper()
	kv := newFakeKV()
	for k, v := range map[string]string{
		keySetupCompleted:    "true",
		KeyNoticeShownAt:     time.Now().UTC().Format(time.RFC3339),
		KeyNoticeVersion:     NoticeVersion,
		StoreNameSettingsKey: "Corner Shop",
	} {
		_ = kv.Set(context.Background(), k, v)
	}
	return kv
}

func TestReportingAllowed_GuardTable(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		version string
		mutate  func(cfg *config.Config, kv *fakeKV)
		want    bool
		reason  string
	}{
		{"release ok", "0.37.0", func(*config.Config, *fakeKV) {}, true, ""},
		{"release with v prefix ok", "v0.37.0", func(*config.Config, *fakeKV) {}, true, ""},
		{"dev build", "dev", func(*config.Config, *fakeKV) {}, false, ReasonDevBuild},
		{"unparsable version", "0.37", func(*config.Config, *fakeKV) {}, false, ReasonDevBuild},
		{"pre-release stamp", "0.37.0-snapshot", func(*config.Config, *fakeKV) {}, false, ReasonDevBuild},
		{"demo", "0.37.0", func(c *config.Config, _ *fakeKV) { c.Demo = true }, false, ReasonDemo},
		{"never register", "0.37.0", func(c *config.Config, _ *fakeKV) { c.NeverRegister = true }, false, ReasonNever},
		// The e2e seam (loopback fake cloud, ADR-0200) waives dev-build and
		// never-register on a SOURCE build only, and nothing else.
		{"e2e seam on dev build", "dev", func(c *config.Config, _ *fakeKV) { c.E2EAutoRegister, c.NeverRegister = true, true }, true, ""},
		{"e2e seam never waives a release build's never switch", "0.37.0", func(c *config.Config, _ *fakeKV) { c.E2EAutoRegister, c.NeverRegister = true, true }, false, ReasonNever},
		{"e2e seam keeps demo", "dev", func(c *config.Config, _ *fakeKV) { c.E2EAutoRegister, c.Demo = true, true }, false, ReasonDemo},
		{"e2e seam keeps stopped", "dev", func(c *config.Config, kv *fakeKV) { c.E2EAutoRegister = true; _ = kv.Set(ctx, KeyStopped, "true") }, false, ReasonStopped},
		{"e2e seam keeps notice", "dev", func(c *config.Config, kv *fakeKV) { c.E2EAutoRegister = true; delete(kv.m, KeyNoticeShownAt) }, false, ReasonNotice},
		{"e2e seam keeps country", "dev", func(c *config.Config, kv *fakeKV) {
			c.E2EAutoRegister = true
			_ = kv.Set(ctx, StoreCountrySettingsKey, "IR")
		}, false, ReasonCountry},
		{"stopped", "0.37.0", func(_ *config.Config, kv *fakeKV) { _ = kv.Set(ctx, KeyStopped, "true") }, false, ReasonStopped},
		{"setup incomplete", "0.37.0", func(_ *config.Config, kv *fakeKV) { _ = kv.Set(ctx, keySetupCompleted, "false") }, false, ReasonSetup},
		{"notice never shown", "0.37.0", func(_ *config.Config, kv *fakeKV) { delete(kv.m, KeyNoticeShownAt) }, false, ReasonNotice},
		{"older notice version", "0.37.0", func(_ *config.Config, kv *fakeKV) { _ = kv.Set(ctx, KeyNoticeVersion, "checkin-notice-v0") }, false, ReasonNotice},
		{"Iran", "0.37.0", func(_ *config.Config, kv *fakeKV) { _ = kv.Set(ctx, StoreCountrySettingsKey, "ir") }, false, ReasonCountry},
		{"other country ok", "0.37.0", func(_ *config.Config, kv *fakeKV) { _ = kv.Set(ctx, StoreCountrySettingsKey, "GB") }, true, ""},
		{"no country ok", "0.37.0", func(_ *config.Config, kv *fakeKV) { _ = kv.Set(ctx, StoreCountrySettingsKey, "") }, true, ""},
		{"replica", "0.37.0", func(_ *config.Config, kv *fakeKV) { _ = kv.Set(ctx, keySyncPrimaryURL, "http://main:8080") }, false, ReasonReplica},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setReleaseVersion(t, tc.version)
			kv := readyKV(t)
			cfg := &config.Config{}
			tc.mutate(cfg, kv)
			got, why := ReportingAllowed(ctx, cfg, kv)
			if got != tc.want || why != tc.reason {
				t.Fatalf("ReportingAllowed = %v, %q; want %v, %q", got, why, tc.want, tc.reason)
			}
			if p := ReportingPolicy(cfg)(kvGetter(ctx, kv)); p != tc.want {
				t.Errorf("ReportingPolicy = %v, want %v", p, tc.want)
			}
		})
	}
}

// ut-cloud's default CLOUD_REFUSED_COUNTRIES is IR (ADR-0145); the till's
// compiled-in set must be the same.
func TestDenyCountriesMatchTheCloudDefault(t *testing.T) {
	if len(DenyCountries) != 1 || !DenyCountries["IR"] {
		t.Fatalf("DenyCountries = %v, want exactly {IR} (ut-cloud default CLOUD_REFUSED_COUNTRIES)", DenyCountries)
	}
}

func TestAutoRegisterAllowed_HeldBlockedRegistered(t *testing.T) {
	setReleaseVersion(t, "0.37.0")
	resetState()
	ctx := context.Background()
	cfg := &config.Config{}
	kv := readyKV(t)
	if ok, why := AutoRegisterAllowed(ctx, cfg, kv); !ok {
		t.Fatalf("fresh ready till: not allowed (%s)", why)
	}
	HoldRegistration()
	if ok, why := AutoRegisterAllowed(ctx, cfg, kv); ok || why != ReasonHeld {
		t.Errorf("held pairing: %v %q, want not allowed (held)", ok, why)
	}
	ReleaseRegistration()
	_ = kv.Set(ctx, RegisterBlockedUntilKey, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	if ok, why := AutoRegisterAllowed(ctx, cfg, kv); ok || why != ReasonHeld {
		t.Errorf("register blocked: %v %q, want not allowed", ok, why)
	}
	_ = kv.Set(ctx, RegisterBlockedUntilKey, "")
	_ = kv.Set(ctx, keyRegisterKeyRefused, time.Now().UTC().Format(time.RFC3339))
	if ok, _ := AutoRegisterAllowed(ctx, cfg, kv); ok {
		t.Error("key refused: still allowed")
	}
	_ = kv.Set(ctx, keyRegisterKeyRefused, "")
	mu.Lock()
	cur = identity{StoreID: "s", Token: "t"}
	mu.Unlock()
	t.Cleanup(resetState)
	if ok, why := AutoRegisterAllowed(ctx, cfg, kv); ok || why != ReasonRegistered {
		t.Errorf("registered till: %v %q, want not allowed (registered)", ok, why)
	}
}

func TestAutoBackoff_SequenceIsCappedAndJittered(t *testing.T) {
	old := autoJitter
	t.Cleanup(func() { autoJitter = old })
	autoJitter = func() float64 { return 0 }
	want := []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second, 240 * time.Second, 480 * time.Second, 10 * time.Minute, 10 * time.Minute, 10 * time.Minute}
	for i, w := range want {
		if got := autoBackoff(i + 1); got != w {
			t.Errorf("backoff(%d) = %s, want %s", i+1, got, w)
		}
	}
	autoJitter = func() float64 { return 0.2 }
	if got := autoBackoff(20); got > 10*time.Minute {
		t.Errorf("jittered backoff %s exceeds the 10 min cap", got)
	}
	autoJitter = func() float64 { return -0.2 }
	if got := autoBackoff(1); got != 24*time.Second {
		t.Errorf("backoff(1) with -20%% jitter = %s, want 24s", got)
	}
}

// registerServer counts register hits; the handler decides the answer.
type registerServer struct {
	srv   *httptest.Server
	hits  atomic.Int32
	mu    sync.Mutex
	bodys []map[string]any
}

func newRegisterServer(t *testing.T, answer func(n int, w http.ResponseWriter)) *registerServer {
	t.Helper()
	rs := &registerServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/stores/register", func(w http.ResponseWriter, r *http.Request) {
		n := int(rs.hits.Add(1))
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		rs.mu.Lock()
		rs.bodys = append(rs.bodys, body)
		rs.mu.Unlock()
		if answer != nil {
			answer(n, w)
			return
		}
		devID, _ := body["device_id"].(string)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{
			"store_id": "store-abc", "merchant_id": "store-abc", "device_id": devID, "token": "tok-123",
		}})
	})
	mux.HandleFunc("/api/v1/stores/devices/register", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{}})
	})
	mux.HandleFunc("/ui/api/signing-key", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"algorithm": "ed25519", "public_key_hex": strings.Repeat("ab", 32)}})
	})
	rs.srv = httptest.NewServer(mux)
	t.Cleanup(rs.srv.Close)
	return rs
}

// loopFixture boots enroll state (Init with NeverRegister, so Init starts no
// loop of its own) and returns the config the loop under test runs with.
func loopFixture(t *testing.T, rs *registerServer, kv Settings) *config.Config {
	t.Helper()
	setReleaseVersion(t, "0.37.0")
	resetState()
	t.Cleanup(resetState)
	boot := freshConfig(rs.srv.URL)
	boot.NeverRegister = true
	initForTest(t, boot, kv)
	cfg := freshConfig(rs.srv.URL)
	return cfg
}

// fakeWaits replaces the loop's wait: it records every delay and returns true
// (keep going) until stop says otherwise.
func fakeWaits(t *testing.T, onWait func(n int, d time.Duration) bool) *[]time.Duration {
	t.Helper()
	old := autoWait
	oldJ := autoJitter
	t.Cleanup(func() { autoWait, autoJitter = old, oldJ })
	autoJitter = func() float64 { return 0 }
	var mu sync.Mutex
	var waits []time.Duration
	autoWait = func(ctx context.Context, d time.Duration) bool {
		mu.Lock()
		waits = append(waits, d)
		n := len(waits)
		mu.Unlock()
		if ctx.Err() != nil {
			return false
		}
		return onWait == nil || onWait(n, d)
	}
	return &waits
}

func TestAutoLoop_RetriesWithCappedBackoffThenSucceedsAndStops(t *testing.T) {
	rs := newRegisterServer(t, func(n int, w http.ResponseWriter) {
		if n <= 7 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"store_id": "s", "merchant_id": "s", "device_id": "d", "token": "tok"}})
	})
	kv := readyKV(t)
	cfg := loopFixture(t, rs, kv)
	waits := fakeWaits(t, nil)
	var registered atomic.Int32
	SetRegisteredHook(func() { registered.Add(1) })
	t.Cleanup(func() { SetRegisteredHook(nil) })

	autoRegisterLoop(t.Context(), cfg, kv)

	if got := rs.hits.Load(); got != 8 {
		t.Fatalf("register hits = %d, want 8 (7 failures then success)", got)
	}
	want := []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second, 240 * time.Second, 480 * time.Second, 10 * time.Minute, 10 * time.Minute}
	if len(*waits) != len(want) {
		t.Fatalf("waits = %v, want %v", *waits, want)
	}
	for i, w := range want {
		if (*waits)[i] != w {
			t.Errorf("wait %d = %s, want %s", i, (*waits)[i], w)
		}
	}
	if !CurrentStatus().Registered {
		t.Error("till is not registered after the loop succeeded")
	}
	if registered.Load() != 1 {
		t.Errorf("registered hook ran %d times, want once", registered.Load())
	}
	// The loop is over: a registered till makes no further call.
	before := rs.hits.Load()
	autoRegisterLoop(t.Context(), cfg, kv)
	if rs.hits.Load() != before {
		t.Error("loop called the cloud again after the till registered")
	}
}

func TestAutoLoop_NoticeFirst_ZeroRequestsBeforeShown(t *testing.T) {
	rs := newRegisterServer(t, nil)
	kv := readyKV(t)
	delete(kv.m, KeyNoticeShownAt)
	cfg := loopFixture(t, rs, kv)
	waits := fakeWaits(t, func(n int, _ time.Duration) bool {
		if rs.hits.Load() != 0 {
			t.Errorf("register request left before the notice was shown (wait %d)", n)
		}
		if n == 3 { // the operator sees the notice
			if err := MarkNoticeShown(t.Context(), kv, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		return true
	})
	autoRegisterLoop(t.Context(), cfg, kv)
	if len(*waits) != 3 || rs.hits.Load() != 1 {
		t.Fatalf("waits=%v hits=%d, want 3 quiet polls then exactly one registration", *waits, rs.hits.Load())
	}
	for _, d := range *waits {
		if d != autoPollEvery {
			t.Errorf("blocked loop waited %s, want the %s guard poll", d, autoPollEvery)
		}
	}
}

func TestAutoLoop_StoppedSendsNothingAndResumesWhenSwitchedOn(t *testing.T) {
	rs := newRegisterServer(t, nil)
	kv := readyKV(t)
	_ = kv.Set(t.Context(), KeyStopped, "true")
	cfg := loopFixture(t, rs, kv)
	fakeWaits(t, func(n int, _ time.Duration) bool {
		if n < 4 && rs.hits.Load() != 0 {
			t.Error("a stopped till registered")
		}
		if n == 4 {
			_ = SetStopped(t.Context(), kv, false)
		}
		return true
	})
	autoRegisterLoop(t.Context(), cfg, kv)
	if rs.hits.Load() != 1 {
		t.Fatalf("hits = %d, want one registration after the switch went on", rs.hits.Load())
	}
}

func TestAutoLoop_RefusedByCloudStopsForGood(t *testing.T) {
	rs := newRegisterServer(t, func(_ int, w http.ResponseWriter) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"service_unavailable","message":"x"}}`))
	})
	kv := readyKV(t)
	cfg := loopFixture(t, rs, kv)
	waits := fakeWaits(t, nil)
	autoRegisterLoop(t.Context(), cfg, kv)
	if rs.hits.Load() != 1 || len(*waits) != 0 {
		t.Fatalf("hits=%d waits=%v, want one request and no retry after a refusal", rs.hits.Load(), *waits)
	}
}

func TestAutoLoop_RateLimitedBacksOffNeverTightLoops(t *testing.T) {
	rs := newRegisterServer(t, func(n int, w http.ResponseWriter) {
		if n < 3 {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"store_id": "s", "merchant_id": "s", "device_id": "d", "token": "tok"}})
	})
	kv := readyKV(t)
	cfg := loopFixture(t, rs, kv)
	waits := fakeWaits(t, nil)
	autoRegisterLoop(t.Context(), cfg, kv)
	if len(*waits) != 2 || (*waits)[0] < 30*time.Second || (*waits)[1] <= (*waits)[0] {
		t.Fatalf("waits = %v, want growing delays of at least 30s", *waits)
	}
}

func TestAutoLoop_EndsWhenContextEnds(t *testing.T) {
	rs := newRegisterServer(t, func(_ int, w http.ResponseWriter) { http.Error(w, "down", http.StatusServiceUnavailable) })
	kv := readyKV(t)
	cfg := loopFixture(t, rs, kv)
	ctx, cancel := context.WithCancel(t.Context())
	fakeWaits(t, func(n int, _ time.Duration) bool {
		if n == 2 {
			cancel()
		}
		return true
	})
	done := make(chan struct{})
	go func() { autoRegisterLoop(ctx, cfg, kv); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not end with its context")
	}
}

// Init starts the loop only for a process that can ever report; a dev build,
// a demo and a never-register till never even start it.
func TestInit_StartsLoopOnlyWhenAllowed(t *testing.T) {
	cases := []struct {
		name    string
		version string
		mutate  func(*config.Config)
		wantHit bool
	}{
		{"release", "0.37.0", func(*config.Config) {}, true},
		{"dev build", "dev", func(*config.Config) {}, false},
		{"never register", "0.37.0", func(c *config.Config) { c.NeverRegister = true }, false},
		{"demo", "0.37.0", func(c *config.Config) { c.Demo = true }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rs := newRegisterServer(t, nil)
			setReleaseVersion(t, tc.version)
			resetState()
			t.Cleanup(resetState)
			fakeWaits(t, nil)
			kv := readyKV(t)
			cfg := freshConfig(rs.srv.URL)
			tc.mutate(cfg)
			initForTest(t, cfg, kv)
			if tc.wantHit {
				waitFor(t, "the loop to register", func() bool { return rs.hits.Load() == 1 })
				return
			}
			time.Sleep(100 * time.Millisecond)
			if n := rs.hits.Load(); n != 0 {
				t.Fatalf("%s: %d register requests, want none", tc.name, n)
			}
		})
	}
}

// Golden: the register body never carries a key the notice does not list.
func TestRegisterPayloadMatchesTheNotice(t *testing.T) {
	rs := newRegisterServer(t, nil)
	kv := readyKV(t)
	_ = kv.Set(t.Context(), StoreCountrySettingsKey, "GB")
	cfg := loopFixture(t, rs, kv)
	fakeWaits(t, nil)
	autoRegisterLoop(t.Context(), cfg, kv)
	if len(rs.bodys) != 1 {
		t.Fatalf("bodies = %d", len(rs.bodys))
	}
	allowed := map[string]bool{}
	for _, k := range NoticePayloadKeys {
		allowed[k] = true
	}
	for k := range rs.bodys[0] {
		if !allowed[k] {
			t.Errorf("register payload key %q is not covered by notice %s: review the notice text and bump NoticeVersion", k, NoticeVersion)
		}
	}
	for _, k := range []string{"store_name", "device_name"} {
		if _, has := rs.bodys[0][k]; has {
			t.Errorf("automatic registration sent %q (owner-approved: no shop or till name in the automatic POST)", k)
		}
	}
	if _, has := rs.bodys[0]["terms_version"]; has {
		t.Error("automatic registration sent terms_version (ADR-0200: no terms acceptance on the auto path)")
	}
}

func TestReportVersionChange(t *testing.T) {
	type ev struct{ from, to string }
	run := func(t *testing.T, kv *fakeKV, version string) []ev {
		t.Helper()
		setReleaseVersion(t, version)
		var got []ev
		ReportVersionChange(t.Context(), &config.Config{}, kv, func(_ context.Context, from, to string) { got = append(got, ev{from, to}) })
		return got
	}
	kv := readyKV(t)
	if got := run(t, kv, "0.36.0"); len(got) != 0 {
		t.Fatalf("first ever start recorded %v, want none", got)
	}
	if kv.get(KeyLastReportedVersion) != "0.36.0" {
		t.Fatal("first start did not remember the version")
	}
	if got := run(t, kv, "0.36.0"); len(got) != 0 {
		t.Fatalf("restart on the same version recorded %v", got)
	}
	if got := run(t, kv, "0.37.0"); len(got) != 1 || got[0] != (ev{"0.36.0", "0.37.0"}) {
		t.Fatalf("update recorded %v, want exactly 0.36.0 -> 0.37.0", got)
	}
	if got := run(t, kv, "0.37.0"); len(got) != 0 {
		t.Fatalf("second start after the update recorded %v", got)
	}
	if got := run(t, kv, "0.36.5"); len(got) != 1 {
		t.Fatalf("downgrade recorded %v, want one event", got)
	}
	// A guard says no: nothing recorded, but the running version is remembered
	// so the change is not reported after the guard lifts (ut-docs#6618).
	kv2 := readyKV(t)
	_ = kv2.Set(t.Context(), KeyStopped, "true")
	if got := run(t, kv2, "0.37.0"); len(got) != 0 || kv2.get(KeyLastReportedVersion) != "0.37.0" {
		t.Fatalf("stopped till reported/not remembered: %v %q", got, kv2.get(KeyLastReportedVersion))
	}
}

func TestNoticeBookkeeping(t *testing.T) {
	kv := newFakeKV()
	ctx := t.Context()
	if NoticeShown(ctx, kv) {
		t.Fatal("notice shown on an empty till")
	}
	first := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) // fixed-date:ok pure formatting test, no clock read
	if err := MarkNoticeShown(ctx, kv, first); err != nil {
		t.Fatal(err)
	}
	if !NoticeShown(ctx, kv) || kv.get(KeyNoticeVersion) != NoticeVersion {
		t.Fatal("notice not recorded")
	}
	if err := MarkNoticeShown(ctx, kv, first.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if kv.get(KeyNoticeShownAt) != first.Format(time.RFC3339) {
		t.Errorf("shown-at moved to %q; the first display is the one kept", kv.get(KeyNoticeShownAt))
	}
	_ = kv.Set(ctx, KeyNoticeVersion, "checkin-notice-v0")
	if NoticeShown(ctx, kv) {
		t.Error("an older notice version counts as shown")
	}
}

func TestCardStatus(t *testing.T) {
	resetState()
	t.Cleanup(resetState)
	kv := newFakeKV()
	ctx := t.Context()
	if cs := CurrentCardStatus(ctx, kv); cs.State != StateNotRegistered {
		t.Errorf("fresh: %v", cs.State)
	}
	mu.Lock()
	cur = identity{StoreID: "s", Token: "t"}
	mu.Unlock()
	at := time.Now().UTC().Truncate(time.Second)
	_ = kv.Set(ctx, KeyLastCheckinAt, at.Format(time.RFC3339))
	if cs := CurrentCardStatus(ctx, kv); cs.State != StateRegistered || !cs.LastCheckin.Equal(at) {
		t.Errorf("registered: %+v", cs)
	}
	_ = SetStopped(ctx, kv, true)
	if cs := CurrentCardStatus(ctx, kv); cs.State != StatePaused {
		t.Errorf("stopped: %v, want paused", cs.State)
	}
}

// ADR-0200 (ut-docs#6559): "stopped" halts EVERY automatic registration, not
// only the loop: the plugin-download fallback (EnsureRegistered) and the main
// till's device-registration loop (run) too. Explicit operator actions (claim
// code, pair, new shop, move) are not automatic and stay allowed while stopped.
func TestStopped_EnsureRegisteredRegistersNothing(t *testing.T) {
	rs := newRegisterServer(t, nil)
	cfg := &config.Config{}
	cfg.Marketplace.EndpointURL = rs.srv.URL + "/api"
	kv := newFakeKV()
	initForTest(t, cfg, kv)
	_ = kv.Set(t.Context(), StoreNameSettingsKey, "Cloud Cafe")
	_ = kv.Set(t.Context(), KeyStopped, "true")

	EnsureRegistered(t.Context(), cfg, kv)
	if n := rs.hits.Load(); n != 0 {
		t.Fatalf("stopped till sent %d register request(s) from EnsureRegistered", n)
	}
	_ = kv.Set(t.Context(), KeyStopped, "false")
	EnsureRegistered(t.Context(), cfg, kv)
	if n := rs.hits.Load(); n != 1 {
		t.Fatalf("register hits after switching back on = %d, want 1", n)
	}
}

// The lazy fallback is an automatic path: no shop name, no till name (owner
// decision, ADR-0200).
func TestEnsureRegistered_IsAnonymous(t *testing.T) {
	rs := newRegisterServer(t, nil)
	cfg := &config.Config{}
	cfg.Marketplace.EndpointURL = rs.srv.URL + "/api"
	kv := newFakeKV()
	initForTest(t, cfg, kv)
	_ = kv.Set(t.Context(), StoreNameSettingsKey, "Cloud Cafe")
	EnsureRegistered(t.Context(), cfg, kv)
	if len(rs.bodys) != 1 {
		t.Fatalf("register bodies = %d, want 1", len(rs.bodys))
	}
	for _, k := range []string{"store_name", "device_name"} {
		if _, has := rs.bodys[0][k]; has {
			t.Errorf("lazy register sent %q", k)
		}
	}
}

func TestStopped_DeviceLoopRegistersNothing(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{}})
	}))
	defer srv.Close()
	kv := newFakeKV()
	_ = kv.Set(t.Context(), KeyStopped, "true")
	waits := 0
	fakeWaits(t, func(n int, _ time.Duration) bool { waits = n; return n < 3 })
	m := config.MarketplaceConfig{EndpointURL: srv.URL + "/api", StoreID: "s", MerchantToken: "t"}
	run(t.Context(), m, "Till 1", kv)
	if hits.Load() != 0 {
		t.Fatalf("stopped till's device loop sent %d request(s)", hits.Load())
	}
	if waits != 3 {
		t.Fatalf("device loop polled %d times, want it to keep waiting (3)", waits)
	}
}

// ADR-0200 owner decision: an auto-registered till (anonymous register) is
// marked so its later check-ins carry no shop or till name; an operator
// action (named register, pair, claim-code mint) clears the mark.
func TestAutoRegisteredMarker(t *testing.T) {
	ctx := context.Background()
	t.Run("anonymous register sets it", func(t *testing.T) {
		resetState()
		rs := newRegisterServer(t, nil)
		kv := newFakeKV()
		m := config.MarketplaceConfig{EndpointURL: rs.srv.URL + "/api"}
		if err := register(withAnonymousRegister(ctx), m, "", kv, ""); err != nil {
			t.Fatal(err)
		}
		if !AutoRegistered(ctx, kv) {
			t.Fatal("anonymous register did not mark the till auto-registered")
		}
	})
	t.Run("named register clears it", func(t *testing.T) {
		resetState()
		rs := newRegisterServer(t, nil)
		kv := newFakeKV()
		_ = kv.Set(ctx, KeyAutoRegistered, "true")
		m := config.MarketplaceConfig{EndpointURL: rs.srv.URL + "/api"}
		if err := register(ctx, m, "Corner Shop", kv, ""); err != nil {
			t.Fatal(err)
		}
		if AutoRegistered(ctx, kv) {
			t.Fatal("an operator-initiated register left the auto-registered mark")
		}
	})
	t.Run("pairing clears it", func(t *testing.T) {
		resetState()
		kv := newFakeKV()
		_ = kv.Set(ctx, KeyAutoRegistered, "true")
		adoptPaired(ctx, kv, false, "dev-1", "store-1", "store-1", "tok")
		if AutoRegistered(ctx, kv) {
			t.Fatal("pairing left the auto-registered mark")
		}
	})
	t.Run("claim mint clears it", func(t *testing.T) {
		kv := newFakeKV()
		_ = kv.Set(ctx, KeyAutoRegistered, "true")
		MarkOperatorClaimed(ctx, kv)
		if AutoRegistered(ctx, kv) {
			t.Fatal("MarkOperatorClaimed left the mark")
		}
	})
}
