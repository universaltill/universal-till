package updates

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
)

// These tests share package-level state (the atomic status, the seam vars, the
// build version) — none of them may use t.Parallel.

// withCheckServer points releasesURL at a local httptest server and pins
// buildinfo.Version, restoring both afterward. No test ever talks to the real
// GitHub API.
func withCheckServer(t *testing.T, version string, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	oldURL, oldVer := releasesURL, buildinfo.Version
	releasesURL = srv.URL
	buildinfo.Version = version
	t.Cleanup(func() {
		releasesURL = oldURL
		buildinfo.Version = oldVer
		srv.Close()
	})
}

func resetState(t *testing.T) {
	t.Helper()
	state.Store(Status{})
	t.Cleanup(func() { state.Store(Status{}) })
}

func TestCurrentZeroBeforeFirstCheck(t *testing.T) {
	resetState(t)
	if got := Current(); got != (Status{}) {
		t.Fatalf("Current() before any check = %+v, want zero", got)
	}
}

func TestCheckNowNewerRelease(t *testing.T) {
	resetState(t)
	withCheckServer(t, "0.1.2", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept header = %q", got)
		}
		w.Write([]byte(`{"tag_name":"v0.2.0","html_url":"https://example.test/rel"}`))
	})
	got := CheckNow(context.Background())
	want := Status{Available: true, Latest: "0.2.0", URL: "https://example.test/rel"}
	if got != want {
		t.Fatalf("CheckNow = %+v, want %+v", got, want)
	}
	if Current() != want {
		t.Fatalf("Current after check = %+v, want %+v", Current(), want)
	}
}

func TestCheckNowSameVersionNotAvailable(t *testing.T) {
	resetState(t)
	withCheckServer(t, "0.2.0", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v0.2.0","html_url":"u"}`))
	})
	got := CheckNow(context.Background())
	if got.Available {
		t.Fatalf("same version reported Available: %+v", got)
	}
	if got.Latest != "0.2.0" {
		t.Fatalf("Latest = %q, want 0.2.0", got.Latest)
	}
}

// A failed check must leave the previously known status in place.
func TestCheckFailuresLeaveStateUntouched(t *testing.T) {
	prior := Status{Available: true, Latest: "9.9.9", URL: "prior"}
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"http error", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }},
		{"bad json", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{not json")) }},
		{"empty tag", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"tag_name":"  ","html_url":"u"}`)) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resetState(t)
			state.Store(prior)
			withCheckServer(t, "0.1.0", c.handler)
			if got := CheckNow(context.Background()); got != prior {
				t.Fatalf("failed check overwrote state: %+v, want %+v", got, prior)
			}
		})
	}
}

func TestCheckOnceUnreachableServer(t *testing.T) {
	resetState(t)
	// A server that is already closed → connection refused, state untouched.
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	old := releasesURL
	releasesURL = srv.URL
	t.Cleanup(func() { releasesURL = old })
	if got := CheckNow(context.Background()); got != (Status{}) {
		t.Fatalf("unreachable server changed state: %+v", got)
	}
}

func TestEnabledFromEnv(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", true}, // unset/empty → enabled
		{"0", false},
		{"false", false},
		{"FALSE", false},
		{"1", true},
		{"true", true},
		{"garbage", true}, // unparseable → enabled (documented: only explicit falsy disables)
	}
	for _, c := range cases {
		t.Run("val="+c.val, func(t *testing.T) {
			t.Setenv("UT_UPDATE_CHECK", c.val)
			if got := enabledFromEnv(); got != c.want {
				t.Errorf("enabledFromEnv() with %q = %v, want %v", c.val, got, c.want)
			}
		})
	}
}

// Start with checks disabled must return without spawning the checker (so wg
// must stay at zero — Wait returns instantly, and that's correct here since
// no goroutine was ever started). With an already-cancelled context the
// boot-wait goroutine exits on ctx.Done — and must actually register with wg
// first, or a caller waiting on wg would never notice it was ever running.
// Neither timer branch (30s boot wait elapsing, 24h ticker) is exercisable in
// a unit test without a fake clock — deliberately left uncovered rather than
// faked.
func TestStartDisabledAndCancelled(t *testing.T) {
	t.Setenv("UT_UPDATE_CHECK", "0")
	var disabledWG sync.WaitGroup
	Start(context.Background(), &disabledWG) // must be a no-op
	if !waitImmediately(&disabledWG) {
		t.Fatal("disabled Start registered a goroutine it never started")
	}

	// Not pre-cancelled this time: the goroutine must actually be running
	// (blocked in its own select) when we check, so a missing wg.Add can't
	// vacuously pass by racing an already-done ctx.
	t.Setenv("UT_UPDATE_CHECK", "1")
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	Start(ctx, &wg)
	if waitWithin(&wg, 150*time.Millisecond) {
		t.Fatal("wg.Wait() returned before ctx was even cancelled — checker goroutine not tracked")
	}
	cancel()
	if !waitWithin(&wg, 2*time.Second) {
		t.Fatal("Start's checker goroutine did not join wg within 2s of ctx cancel")
	}
}

// waitImmediately reports whether wg.Wait() returns without blocking at all.
func waitImmediately(wg *sync.WaitGroup) bool {
	return waitWithin(wg, 20*time.Millisecond)
}

func waitWithin(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.1.3", "0.1.2", true},
		{"0.1.2", "0.1.2", false},
		{"0.1.2", "0.1.3", false},
		{"1.0.0", "0.9.9", true},
		{"0.2.0", "0.1.9", true},
		{"0.1.10", "0.1.9", true}, // numeric, not lexical
		{"0.1.3", "dev", true},    // any release beats a dev build
		{"0.1.3", "", true},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
