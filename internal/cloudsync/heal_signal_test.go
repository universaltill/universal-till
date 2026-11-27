package cloudsync

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/credheal"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
)

// ADR-0222 T1/T3: every cloud caller's 401 envelope reaches the heal engine;
// the check-in tick's 401 shows the chip at once; probes are check-ins.

// engineRig runs a real credheal engine with a fake Recover so a test can see
// what a 401 sets in motion.
type engineRig struct {
	recovers atomic.Int32
	probes   atomic.Int32
	probe    atomic.Pointer[credheal.ProbeResult]
	gate     chan struct{}
}

func startEngine(t *testing.T, cfg *config.Config) *engineRig {
	t.Helper()
	r := &engineRig{gate: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	credheal.SetHooks(credheal.Hooks{Probe: func(context.Context) credheal.ProbeResult {
		r.probes.Add(1)
		if p := r.probe.Load(); p != nil {
			return *p
		}
		return credheal.ProbeResult{Code: "unauthorized"}
	}})
	credheal.Start(ctx, credheal.Deps{
		Cfg: cfg, KV: healKV{}, Pause: func(context.Context, time.Duration) {},
		CanRecover: func(context.Context) (bool, string) { return true, "" },
		Recover: func(context.Context) (enroll.RecoverOutcome, error) {
			r.recovers.Add(1)
			<-r.gate
			return enroll.RecoverOutcome{Healed: true}, nil
		},
	}, &wg)
	t.Cleanup(func() {
		select {
		case <-r.gate:
		default:
			close(r.gate)
		}
		cancel()
		wg.Wait()
		credheal.SetHooks(credheal.Hooks{})
	})
	return r
}

type healKV struct{}

func (healKV) Get(context.Context, string) (string, bool, error)          { return "", false, nil }
func (healKV) Set(context.Context, string, string) error                  { return nil }
func (healKV) GetOrCreate(_ context.Context, _, d string) (string, error) { return d, nil }

func serve401(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(body))
	}))
}

// resetRefusalLog clears the once-per-change refusal log between tests. It lives
// here, not in heal.go, because only tests call it (the deadcode baseline guard).
func resetRefusalLog() {
	refusalLog.Lock()
	refusalLog.last = nil
	refusalLog.Unlock()
}

// The revoke signal carries reason and the recover hint on every route.
func TestRevokeSignalCarriesReasonAndRecoverHint(t *testing.T) {
	body := `{"data":{"reason":"pair_abandoned","recover":true},"error":{"code":"device_revoked"}}`
	srv := serve401(body)
	defer srv.Close()
	_, err := post(context.Background(), testCfg(srv.URL), "/v1/stores/sync", []byte("{}"))
	var se *statusError
	if !errors.As(err, &se) || se.Reason != "pair_abandoned" || !se.Recover || se.Code != "device_revoked" {
		t.Fatalf("post: %#v", err)
	}
	_, _, err = getCheckin(context.Background(), srv.URL, "store-1", "tok-1", false, 0, nil)
	if !errors.As(err, &se) || se.Reason != "pair_abandoned" || !se.Recover {
		t.Fatalf("checkin: %#v", err)
	}
}

func TestRevokeSignalDetailAndReasonsAreClamped(t *testing.T) {
	cases := map[string]string{
		`{"data":{"reason":"moved","moved_to":"Other shop"},"error":{"code":"device_revoked"}}`:      "", // a store id, never kept
		`{"data":{"reason":"replaced","replaced_by":"Counter 2"},"error":{"code":"device_revoked"}}`: "Counter 2",
		`{"data":{"reason":"removed","removed_by":"owner"},"error":{"code":"device_revoked"}}`:       "",
		`{"data":{"reason":"moved","moved_to":"x"},"error":{"code":"unauthorized"}}`:                 "",
	}
	for body, want := range cases {
		sig := parseRevokeSignal([]byte(body))
		got := ""
		if sig != nil {
			got = sig.Detail
		}
		if got != want {
			t.Errorf("%s: detail %q, want %q", body, got, want)
		}
	}
}

// AC12: the check-in tick's FIRST 401 with an envelope code puts the chip into
// "reconnecting" at once, and the engine heals once, with no probe (the tick
// is the probe).
func TestTick401ReportsToTheEngineAtOnce(t *testing.T) {
	resetTillAuth(t)
	srv := serve401(unauthorizedBody("unauthorized"))
	defer srv.Close()
	cfg := testCfg(srv.URL)
	r := startEngine(t, cfg)

	_, err := tick(context.Background(), cfg, testDB(t), Hooks{})
	if err == nil {
		t.Fatal("tick succeeded against a 401")
	}
	if st := credheal.Current(); st.State != credheal.StateHealing || st.Reason != credheal.ReasonReconnecting {
		t.Fatalf("chip data one tick after the first 401 = %+v", st)
	}
	waitFor(t, "the recover", func() bool { return r.recovers.Load() == 1 })
	close(r.gate)
	waitFor(t, "ok", func() bool { return credheal.Current().State == credheal.StateOK })
	if r.probes.Load() != 0 {
		t.Fatalf("probes = %d, want 0: the tick's own 401 is the evidence", r.probes.Load())
	}
}

// A 401 with no envelope code (a proxy) is logged, never reported.
func TestTick401WithoutEnvelopeNeverReports(t *testing.T) {
	resetTillAuth(t)
	srv := serve401("<html>401</html>")
	defer srv.Close()
	cfg := testCfg(srv.URL)
	r := startEngine(t, cfg)
	_, _ = tick(context.Background(), cfg, testDB(t), Hooks{})
	time.Sleep(50 * time.Millisecond)
	if r.recovers.Load() != 0 || r.probes.Load() != 0 || credheal.Current().State != credheal.StateOK {
		t.Fatalf("recovers %d probes %d state %+v", r.recovers.Load(), r.probes.Load(), credheal.Current())
	}
}

// A final revoke on the tick needs no network call and shows the reason.
func TestTickReplacedBecomesAFinalState(t *testing.T) {
	resetTillAuth(t)
	srv := serve401(`{"data":{"reason":"removed"},"error":{"code":"device_revoked"}}`)
	defer srv.Close()
	cfg := testCfg(srv.URL)
	r := startEngine(t, cfg)
	_, _ = tick(context.Background(), cfg, testDB(t), Hooks{})
	waitFor(t, "removed", func() bool {
		st := credheal.Current()
		return st.State == credheal.StateNeedsPerson && st.Reason == credheal.ReasonRemoved
	})
	if r.recovers.Load() != 0 || r.probes.Load() != 0 {
		t.Fatal("a removal made a network call")
	}
}

// A successful tick tells the engine the credential works.
func TestSuccessfulTickClearsTheEngine(t *testing.T) {
	resetTillAuth(t)
	cloud := &fakeCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	cfg := testCfg(srv.URL)
	r := startEngine(t, cfg)
	credheal.Report(credheal.Signal{Source: "x", Code: "unauthorized", FromCheckin: true})
	waitFor(t, "a recover in flight", func() bool { return r.recovers.Load() == 1 })
	if st := credheal.Current(); st.State != credheal.StateHealing {
		t.Fatalf("state %+v", st)
	}
	contacted, err := tick(context.Background(), cfg, testDB(t), Hooks{})
	if err != nil || !contacted {
		t.Fatalf("tick: contacted %v err %v", contacted, err)
	}
	if st := credheal.Current(); st.State != credheal.StateOK {
		t.Fatalf("after a good tick: %+v", st)
	}
}

// Other callers on the shared post path report too; the sync POST does not
// report twice (the tick reports it).
func TestPostBody401ReportsExceptForSync(t *testing.T) {
	srv := serve401(unauthorizedBody("unauthorized"))
	defer srv.Close()
	cfg := testCfg(srv.URL)
	r := startEngine(t, cfg)
	_, _ = post(context.Background(), cfg, "/v1/stores/sync", []byte("{}"))
	time.Sleep(50 * time.Millisecond)
	if r.probes.Load() != 0 {
		t.Fatal("the sync POST reported from postBody")
	}
	_, _ = post(context.Background(), cfg, "/v1/stores/sales/aggregates", []byte("{}"))
	waitFor(t, "a probe for the sales route's 401", func() bool { return r.probes.Load() == 1 })
}

func TestReportDeadCredential(t *testing.T) {
	srv := serve401("")
	defer srv.Close()
	cfg := testCfg(srv.URL)
	r := startEngine(t, cfg)
	reportRefusal("catalog", http.StatusForbidden, "device_mismatch") // 403 never reports
	reportRefusal("catalog", http.StatusUnauthorized, "http_401")     // no envelope code
	time.Sleep(30 * time.Millisecond)
	if r.probes.Load() != 0 {
		t.Fatal("a 403 or envelope-less answer reported")
	}
	reportRefusal("stock", http.StatusUnauthorized, "unauthorized")
	waitFor(t, "stock's 401 to be probed", func() bool { return r.probes.Load() == 1 })
}

// The probe is a check-in GET: 200/304/402 mean the credential is fine, a 401
// gives code, reason and hint, anything else is "could not tell".
func TestProbeCredential(t *testing.T) {
	serve := func(status int, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/stores/checkin" || r.Header.Get("Authorization") != "Bearer tok-1" {
				w.WriteHeader(http.StatusTeapot)
				return
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
	}
	cases := []struct {
		name   string
		status int
		body   string
		want   credheal.ProbeResult
		errOK  bool
	}{
		{"ok", 200, `{"data":{"link_version":3}}`, credheal.ProbeResult{OK: true}, false},
		{"ok without version", 200, `{}`, credheal.ProbeResult{OK: true}, false},
		{"not modified", 304, ``, credheal.ProbeResult{OK: true}, false},
		{"plan required still authenticated", 402, `{}`, credheal.ProbeResult{OK: true}, false},
		{"dead", 401, unauthorizedBody("unauthorized"), credheal.ProbeResult{Code: "unauthorized"}, false},
		{"revoked with hint", 401, `{"data":{"reason":"pair_abandoned","recover":true},"error":{"code":"device_revoked"}}`,
			credheal.ProbeResult{Code: "device_revoked", Reason: "pair_abandoned", Recover: true}, false},
		{"unavailable", 503, `{}`, credheal.ProbeResult{}, true},
		{"rate limited", 429, `{}`, credheal.ProbeResult{}, true},
		{"not found", 404, `{}`, credheal.ProbeResult{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := serve(c.status, c.body)
			defer srv.Close()
			got := ProbeCredential(context.Background(), testCfg(srv.URL))
			if (got.Err != nil) != c.errOK {
				t.Fatalf("err = %v, want error %v", got.Err, c.errOK)
			}
			got.Err = nil
			if got != c.want {
				t.Fatalf("probe = %+v, want %+v", got, c.want)
			}
		})
	}
}

// AC17: every refused call logs path, status, code and reason once per change,
// never the token.
func TestRefusalIsLoggedOncePerChangeWithoutTheToken(t *testing.T) {
	buf := &lockedBuf{}
	restore := logging.CaptureForTest(buf)
	defer restore()
	resetRefusalLog()
	for i := 0; i < 3; i++ {
		noteRefusal("/v1/stores/sync", 401, "unauthorized", "")
	}
	noteRefusal("/v1/stores/sync", 401, "device_revoked", "removed")
	noteRefusal("/v1/stores/sync", 401, "device_revoked", "removed")
	noteRefusal("/v1/stores/checkin", 403, "device_mismatch", "")
	out := buf.String()
	if n := strings.Count(out, "/v1/stores/sync"); n != 2 {
		t.Fatalf("sync logged %d times, want 2 (one per change):\n%s", n, out)
	}
	for _, want := range []string{"status 401", "code unauthorized", "code device_revoked", "reason removed", "/v1/stores/checkin", "status 403"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log lacks %q:\n%s", want, out)
		}
	}
	// The real call path never puts the bearer in the line.
	srv := serve401(unauthorizedBody("unauthorized"))
	defer srv.Close()
	resetRefusalLog()
	_, _ = post(context.Background(), testCfg(srv.URL), "/v1/stores/directives/result", []byte("{}"))
	if strings.Contains(buf.String(), "tok-1") {
		t.Fatalf("the bearer reached the log:\n%s", buf.String())
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// T3: the catalogue pull's and the stock upload's 401 (with an envelope code)
// reach the engine, once per run; a 403 or another 401 code does not.
func TestCatalogPullAndStockUploadReportA401(t *testing.T) {
	t.Run("catalogue", func(t *testing.T) {
		feed := &catalogFeed{status: 401, code: "unauthorized"}
		// The engine's cfg is only for its (faked) recover: any cfg will do.
		r := startEngine(t, testCfg("http://127.0.0.1:1"))
		if _, _, _, _, err := tickWithFeedOf(t, "cloud", "", feed); err != nil {
			t.Fatalf("tick: %v", err)
		}
		waitFor(t, "the catalogue's 401 to be probed", func() bool { return r.probes.Load() == 1 })
	})
	t.Run("catalogue 403 is not evidence", func(t *testing.T) {
		r := startEngine(t, testCfg("http://127.0.0.1:1"))
		feed := &catalogFeed{status: 403, code: "unauthorized"}
		if _, _, _, _, err := tickWithFeedOf(t, "cloud", "", feed); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
		if r.probes.Load() != 0 {
			t.Fatal("a 403 reached the engine")
		}
	})
	t.Run("stock", func(t *testing.T) {
		r := startEngine(t, testCfg("http://127.0.0.1:1"))
		d := openMigratedDB(t, "stockheal.db")
		seedStock(d.DB)
		stock := &stockFeed{}
		stock.set(401, "unauthorized")
		url := stockServer(t, "cloud", stock)
		if err := Tick(context.Background(), testCfg(url), d.DB, Hooks{}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the stock 401 to be probed", func() bool { return r.probes.Load() == 1 })
	})
}
