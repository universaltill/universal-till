package cloudsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
)

// ADR-0117 §4 (ut-docs#2824): a cloud-link nudge kicks the check-in loop
// single-flight — kicks that arrive while a check-in runs schedule exactly
// one more run, never a second concurrent one, and never more than one.
func TestKickDuringCheckInRunsExactlyOneMoreNeverConcurrent(t *testing.T) {
	origFirst, origTick := firstDelayNS.Load(), tickIntervalNS.Load()
	t.Cleanup(func() { firstDelayNS.Store(origFirst); tickIntervalNS.Store(origTick) })
	firstDelayNS.Store(int64(time.Millisecond))
	tickIntervalNS.Store(int64(time.Hour)) // only kicks can cause a second run

	var (
		running, maxRunning, calls atomic.Int32
		entered                    = make(chan struct{}, 8)
		release                    = make(chan struct{})
		once                       sync.Once
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/stores/sync", func(w http.ResponseWriter, r *http.Request) {
		n := running.Add(1)
		defer running.Add(-1)
		for {
			m := maxRunning.Load()
			if n <= m || maxRunning.CompareAndSwap(m, n) {
				break
			}
		}
		if calls.Add(1) == 1 {
			entered <- struct{}{}
			<-release // hold the first check-in open while kicks arrive
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"directives": []any{}}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Cleanup(func() { once.Do(func() { close(release) }) })

	kick := make(chan struct{}, 1)
	var after atomic.Int32
	hooks := Hooks{
		Kick:      kick,
		AfterTick: func(context.Context, bool, error) { after.Add(1) },
	}
	ctx, cancel := context.WithCancel(context.Background())
	wg := startJoined(t, ctx, cancel, testCfg(srv.URL), testDB(t), hooks)

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first check-in never started")
	}
	for i := 0; i < 5; i++ { // coalesced: a cap-1 channel, sent non-blocking
		select {
		case kick <- struct{}{}:
		default:
		}
	}
	once.Do(func() { close(release) })

	deadline := time.Now().Add(3 * time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // room for a wrong third run to show up
	cancel()
	waitJoined(t, wg)

	if got := calls.Load(); got != 2 {
		t.Fatalf("check-ins = %d, want exactly 2 (the first + one kicked run)", got)
	}
	if m := maxRunning.Load(); m != 1 {
		t.Fatalf("max concurrent check-ins = %d, want 1", m)
	}
	if got := after.Load(); got != 2 {
		t.Fatalf("AfterTick calls = %d, want one per check-in (2)", got)
	}
}

// Review finding 2 (ut-docs#2824): AfterTick's contacted flag is true only
// for a check-in that really reached the cloud — an unregistered till's
// early return and a failed POST are both "not contacted", so neither can
// lift the cloud link's wait for the next check-in.
func TestAfterTickReportsARealCloudContact(t *testing.T) {
	origFirst, origTick := firstDelayNS.Load(), tickIntervalNS.Load()
	t.Cleanup(func() { firstDelayNS.Store(origFirst); tickIntervalNS.Store(origTick) })
	firstDelayNS.Store(int64(time.Millisecond))
	tickIntervalNS.Store(int64(time.Hour))

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"directives": []any{}}})
	}))
	defer ok.Close()
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer failing.Close()
	unregistered := testCfg(ok.URL)
	unregistered.Marketplace.MerchantToken = ""

	for _, tc := range []struct {
		name          string
		cfg           *config.Config
		wantContacted bool
		wantErr       bool
	}{
		{"unregistered skip", unregistered, false, false},
		{"failed check-in", testCfg(failing.URL), false, true},
		{"real check-in", testCfg(ok.URL), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type outcome struct {
				contacted bool
				err       error
			}
			got := make(chan outcome, 4)
			hooks := Hooks{AfterTick: func(_ context.Context, contacted bool, err error) {
				select {
				case got <- outcome{contacted, err}:
				default:
				}
			}}
			ctx, cancel := context.WithCancel(context.Background())
			wg := startJoined(t, ctx, cancel, tc.cfg, testDB(t), hooks)
			var o outcome
			select {
			case o = <-got:
			case <-time.After(3 * time.Second):
				t.Fatal("no check-in ran")
			}
			cancel()
			waitJoined(t, wg)
			if o.contacted != tc.wantContacted || (o.err != nil) != tc.wantErr {
				t.Fatalf("AfterTick(contacted=%v, err=%v), want contacted=%v err=%v", o.contacted, o.err, tc.wantContacted, tc.wantErr)
			}
		})
	}
}

// Review finding 3: a kick that arrives while the loop backs off is
// satisfied by the check-in the backoff ends in — it must not stay
// buffered and fire a redundant POST right after that check-in.
func TestKickDuringBackoffIsSatisfiedByTheNextCheckIn(t *testing.T) {
	origFirst, origTick := firstDelayNS.Load(), tickIntervalNS.Load()
	t.Cleanup(func() { firstDelayNS.Store(origFirst); tickIntervalNS.Store(origTick) })
	firstDelayNS.Store(int64(time.Millisecond))
	tickIntervalNS.Store(int64(20 * time.Millisecond)) // backoff after one failure: 40 ms

	kick := make(chan struct{}, 1)
	var calls atomic.Int32
	pendingAtSecond := make(chan int, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/stores/sync" {
			return
		}
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusBadGateway)
			return
		case 2:
			pendingAtSecond <- len(kick)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"directives": []any{}}})
	}))
	defer srv.Close()

	var afterFirst sync.Once
	hooks := Hooks{
		Kick: kick,
		AfterTick: func(context.Context, bool, error) {
			afterFirst.Do(func() { kick <- struct{}{} }) // lands during the backoff
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	wg := startJoined(t, ctx, cancel, testCfg(srv.URL), testDB(t), hooks)
	select {
	case n := <-pendingAtSecond:
		if n != 0 {
			t.Fatalf("a kick from the backoff was still pending during the check-in that ended it (%d); it would fire a redundant POST", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the backoff never ended in a check-in")
	}
	cancel()
	waitJoined(t, wg)
}

// ut-docs#2893: BeforeTick runs on the loop's goroutine after a pending
// kick is drained and before the check-in's POST — so a caller can tell
// the check-in a kick caused (it started after the kick) from one that
// was already running when the kick came (the main till relays a cloud
// nudge to its replicas only after the former).
func TestBeforeTickRunsAfterTheKickDrainAndBeforeThePOST(t *testing.T) {
	origFirst, origTick := firstDelayNS.Load(), tickIntervalNS.Load()
	t.Cleanup(func() { firstDelayNS.Store(origFirst); tickIntervalNS.Store(origTick) })
	firstDelayNS.Store(int64(time.Millisecond))
	tickIntervalNS.Store(int64(time.Hour))

	var (
		mu     sync.Mutex
		events []string
	)
	record := func(e string) { mu.Lock(); events = append(events, e); mu.Unlock() }
	kick := make(chan struct{}, 1)
	pendingAtBefore := make(chan int, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/stores/sync" {
			record("post")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"directives": []any{}}})
	}))
	defer srv.Close()
	var afterFirst sync.Once
	hooks := Hooks{
		Kick: kick,
		BeforeTick: func() {
			pendingAtBefore <- len(kick)
			record("before")
		},
		AfterTick: func(context.Context, bool, error) {
			record("after")
			afterFirst.Do(func() { kick <- struct{}{} })
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	wg := startJoined(t, ctx, cancel, testCfg(srv.URL), testDB(t), hooks)
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(events)
		mu.Unlock()
		if n >= 6 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	waitJoined(t, wg)
	mu.Lock()
	defer mu.Unlock()
	want := []string{"before", "post", "after", "before", "post", "after"}
	if len(events) < len(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	for i, w := range want {
		if events[i] != w {
			t.Fatalf("events = %v, want %v", events, want)
		}
	}
	close(pendingAtBefore)
	for n := range pendingAtBefore {
		if n != 0 {
			t.Fatalf("a kick was still pending when BeforeTick ran (%d)", n)
		}
	}
}
