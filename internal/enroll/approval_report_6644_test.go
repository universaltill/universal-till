package enroll

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ut-docs#6644: a report goroutine started without a WaitGroup (every
// register path) must be waitable, so a test's TempDir is not removed while
// it is still creating the approval key under the data dir.

func TestWaitDetachedReports_NothingInFlight(t *testing.T) {
	if !WaitDetachedReports(time.Second) {
		t.Fatal("WaitDetachedReports = false with nothing in flight")
	}
}

func TestWaitDetachedReports_WaitsForTheDetachedGoroutine(t *testing.T) {
	identify(t)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	var once bool
	releaseOnce := func() {
		if !once {
			once = true
			close(release)
		}
	}
	t.Cleanup(releaseOnce)

	m := marketplace(&reportCloud{srv: srv})
	kickApprovalKeyReport(t.Context(), m, nil)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the report never reached the cloud")
	}
	if WaitDetachedReports(50 * time.Millisecond) {
		t.Fatal("WaitDetachedReports returned true while the report was still in flight")
	}
	releaseOnce()
	if !WaitDetachedReports(5 * time.Second) {
		t.Fatal("WaitDetachedReports timed out after the report finished")
	}
}
